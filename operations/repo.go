package operations

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// Repo — интерфейс для хранения и обновления Operations.
// Каждый сервис создаёт таблицу operations через migration (см. migrations/common/0001_operations.sql).
type Repo interface {
	// Create сохраняет новую операцию (done=false).
	Create(ctx context.Context, op Operation) error
	// Get возвращает операцию по ID. Возвращает ErrNotFound если операции нет.
	Get(ctx context.Context, id string) (*Operation, error)
	// List возвращает список операций с постраничной навигацией.
	List(ctx context.Context, filter ListFilter) ([]Operation, string, error)
	// MarkDone переводит операцию в done=true, записывает финальный ресурс (response).
	MarkDone(ctx context.Context, id string, response *anypb.Any) error
	// MarkError переводит операцию в done=true, записывает ошибку (google.rpc.Status).
	MarkError(ctx context.Context, id string, err *status.Status) error
	// Cancel переводит операцию в done=true со статусом CANCELLED.
	Cancel(ctx context.Context, id string) error
}

// ListFilter — параметры фильтрации/пагинации для List.
type ListFilter struct {
	ResourceID string // если непуст — фильтр по resource_id (денормализованное поле)
	PageSize   int64
	PageToken  string
}

// ErrNotFound возвращается из Get, если операция не найдена.
var ErrNotFound = errors.New("operation not found")

// pgRepo — реализация Repo поверх pgxpool.
type pgRepo struct {
	pool   *pgxpool.Pool
	schema string // schema name (например "public" или "kacho_compute")
}

// NewRepo создаёт Repo для указанного пула и схемы.
// schema используется как квалификатор таблицы (schema.operations).
// Для схемы "public" передавайте "public".
func NewRepo(pool *pgxpool.Pool, schema string) Repo {
	return &pgRepo{pool: pool, schema: schema}
}

// tableName возвращает полное имя таблицы с квалификатором схемы.
func (r *pgRepo) tableName() string {
	return pgx.Identifier{r.schema, "operations"}.Sanitize()
}

// Create вставляет операцию в таблицу.
func (r *pgRepo) Create(ctx context.Context, op Operation) error {
	metaType, metaData, err := marshalAny(op.Metadata)
	if err != nil {
		return fmt.Errorf("repo.Create: marshal metadata: %w", err)
	}

	// Извлекаем resource_id из метаданных (денормализованный индекс для фильтрации).
	resourceID := extractResourceID(op.Metadata)

	q := fmt.Sprintf(`
		INSERT INTO %s
		  (id, description, created_at, created_by, modified_at, done,
		   metadata_type, metadata_data, resource_id)
		VALUES
		  ($1, $2, $3, $4, $5, false, $6, $7, $8)`,
		r.tableName(),
	)

	createdBy := op.CreatedBy
	if createdBy == "" {
		createdBy = "anonymous"
	}

	_, err = r.pool.Exec(ctx, q,
		op.ID,
		op.Description,
		op.CreatedAt,
		createdBy,
		op.ModifiedAt,
		metaType,
		metaData,
		nullableString(resourceID),
	)
	if err != nil {
		return fmt.Errorf("repo.Create: %w", err)
	}
	return nil
}

// Get возвращает операцию по id.
func (r *pgRepo) Get(ctx context.Context, id string) (*Operation, error) {
	q := fmt.Sprintf(`
		SELECT id, description, created_at, created_by, modified_at, done,
		       metadata_type, metadata_data,
		       error_code, error_message, error_details,
		       response_type, response_data
		FROM %s
		WHERE id = $1`,
		r.tableName(),
	)

	row := r.pool.QueryRow(ctx, q, id)
	op, err := scanOperation(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("repo.Get: %w", err)
	}
	return op, nil
}

// List возвращает список операций с фильтром и пагинацией по created_at/id курсору.
func (r *pgRepo) List(ctx context.Context, filter ListFilter) ([]Operation, string, error) {
	pageSize := filter.PageSize
	if pageSize <= 0 || pageSize > 1000 {
		pageSize = 50
	}

	args := []any{}
	conditions := []string{}
	argIdx := 1

	if filter.ResourceID != "" {
		conditions = append(conditions, fmt.Sprintf("resource_id = $%d", argIdx))
		args = append(args, filter.ResourceID)
		argIdx++
	}

	// page_token кодирует "created_at:id" последней записи предыдущей страницы.
	if filter.PageToken != "" {
		cursorCreatedAt, cursorID, err := decodePageToken(filter.PageToken)
		if err != nil {
			return nil, "", fmt.Errorf("repo.List: invalid page_token: %w", err)
		}
		conditions = append(conditions, fmt.Sprintf(
			"(created_at, id) > ($%d, $%d)", argIdx, argIdx+1,
		))
		args = append(args, cursorCreatedAt, cursorID)
		argIdx += 2
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	q := fmt.Sprintf(`
		SELECT id, description, created_at, created_by, modified_at, done,
		       metadata_type, metadata_data,
		       error_code, error_message, error_details,
		       response_type, response_data
		FROM %s
		%s
		ORDER BY created_at ASC, id ASC
		LIMIT $%d`,
		r.tableName(), where, argIdx,
	)
	args = append(args, pageSize+1) // запрашиваем на 1 больше для определения наличия следующей страницы

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("repo.List: %w", err)
	}
	defer rows.Close()

	var ops []Operation
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, "", fmt.Errorf("repo.List: scan: %w", err)
		}
		ops = append(ops, *op)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("repo.List: rows: %w", err)
	}

	var nextToken string
	if int64(len(ops)) > pageSize {
		last := ops[pageSize-1]
		nextToken = encodePageToken(last.CreatedAt, last.ID)
		ops = ops[:pageSize]
	}

	return ops, nextToken, nil
}

// MarkDone переводит операцию в done=true с финальным ресурсом.
func (r *pgRepo) MarkDone(ctx context.Context, id string, response *anypb.Any) error {
	respType, respData, err := marshalAny(response)
	if err != nil {
		return fmt.Errorf("repo.MarkDone: marshal response: %w", err)
	}

	q := fmt.Sprintf(`
		UPDATE %s
		SET done = true, modified_at = $2, response_type = $3, response_data = $4
		WHERE id = $1`,
		r.tableName(),
	)
	tag, err := r.pool.Exec(ctx, q, id, time.Now().UTC(), respType, respData)
	if err != nil {
		return fmt.Errorf("repo.MarkDone: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkError переводит операцию в done=true с ошибкой.
func (r *pgRepo) MarkError(ctx context.Context, id string, errStatus *status.Status) error {
	var errCode *int32
	var errMsg *string
	var errDetails []byte

	if errStatus != nil {
		code := errStatus.GetCode()
		msg := errStatus.GetMessage()
		errCode = &code
		errMsg = &msg

		if len(errStatus.GetDetails()) > 0 {
			b, marshalErr := proto.Marshal(errStatus)
			if marshalErr == nil {
				errDetails = b
			}
		}
	}

	q := fmt.Sprintf(`
		UPDATE %s
		SET done = true, modified_at = $2,
		    error_code = $3, error_message = $4, error_details = $5
		WHERE id = $1`,
		r.tableName(),
	)
	tag, err := r.pool.Exec(ctx, q, id, time.Now().UTC(), errCode, errMsg, errDetails)
	if err != nil {
		return fmt.Errorf("repo.MarkError: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrAlreadyDone возвращается из Cancel, если операция уже завершена.
var ErrAlreadyDone = errors.New("operation already completed")

// Cancel переводит операцию в done=true со статусом CANCELLED (gRPC code 1).
// Если операция уже done — возвращает ErrAlreadyDone (FAILED_PRECONDITION).
// Если операция не существует — возвращает ErrNotFound.
func (r *pgRepo) Cancel(ctx context.Context, id string) error {
	cancelledCode := int32(1) // codes.Cancelled
	cancelledMsg := "operation cancelled"

	// UPDATE only when done=false, чтобы детектировать "already done" по rows-affected.
	q := fmt.Sprintf(`
		UPDATE %s
		SET done = true, modified_at = $2,
		    error_code = $3, error_message = $4
		WHERE id = $1 AND done = false`,
		r.tableName(),
	)
	tag, err := r.pool.Exec(ctx, q, id, time.Now().UTC(), cancelledCode, cancelledMsg)
	if err != nil {
		return fmt.Errorf("repo.Cancel: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	// 0 rows: либо запись не существует, либо done=true. Различим через Get.
	op, getErr := r.Get(ctx, id)
	if getErr != nil {
		return getErr // ErrNotFound или другое
	}
	if op.Done {
		return ErrAlreadyDone
	}
	// race: только что был done=false, но UPDATE не сработал — отдадим ErrNotFound как safe-default.
	return ErrNotFound
}

// ---- helpers ----

// scanOperation читает одну строку из курсора (pgx.Row или pgx.Rows).
func scanOperation(row interface {
	Scan(dest ...any) error
}) (*Operation, error) {
	var op Operation
	var metaType, metaData *[]byte
	var metaTypeStr *string
	var errCode *int32
	var errMsg *string
	var errDetails *[]byte
	var respType *string
	var respData *[]byte

	err := row.Scan(
		&op.ID,
		&op.Description,
		&op.CreatedAt,
		&op.CreatedBy,
		&op.ModifiedAt,
		&op.Done,
		&metaTypeStr,
		&metaData,
		&errCode,
		&errMsg,
		&errDetails,
		&respType,
		&respData,
	)
	if err != nil {
		return nil, err
	}
	_ = metaType

	// восстанавливаем Metadata
	if metaTypeStr != nil && metaData != nil {
		op.Metadata = &anypb.Any{
			TypeUrl: *metaTypeStr,
			Value:   *metaData,
		}
	}

	// восстанавливаем Error
	if errCode != nil {
		op.Error = &status.Status{
			Code:    *errCode,
			Message: stringOrEmpty(errMsg),
		}
		if errDetails != nil {
			// десериализуем Details из сохранённого proto.Marshal(Status)
			var fullStatus status.Status
			if unmarshalErr := proto.Unmarshal(*errDetails, &fullStatus); unmarshalErr == nil {
				op.Error.Details = fullStatus.GetDetails()
			}
		}
	}

	// восстанавливаем Response
	if respType != nil && respData != nil {
		op.Response = &anypb.Any{
			TypeUrl: *respType,
			Value:   *respData,
		}
	}

	return &op, nil
}

// marshalAny сериализует *anypb.Any в (type_url, value) для хранения в БД.
func marshalAny(a *anypb.Any) (*string, []byte, error) {
	if a == nil {
		return nil, nil, nil
	}
	t := a.GetTypeUrl()
	return &t, a.GetValue(), nil
}

// extractResourceID пытается извлечь поле resource_id из метаданных операции.
// Конвенция: в metadata-сообщениях поле с именем, заканчивающимся на _id,
// содержит id связанного ресурса. Это поле сохраняется в денормализованное
// поле resource_id для эффективного фильтра по ресурсу в List.
// Поддерживается только через рефлексию protobuf.
func extractResourceID(metadata *anypb.Any) string {
	if metadata == nil {
		return ""
	}
	msg, err := metadata.UnmarshalNew()
	if err != nil {
		return ""
	}
	// Ищем поле с суффиксом _id в proto reflection
	fields := msg.ProtoReflect().Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		name := string(fd.Name())
		if strings.HasSuffix(name, "_id") {
			val := msg.ProtoReflect().Get(fd)
			if val.IsValid() {
				return val.String()
			}
		}
	}
	return ""
}

// nullableString возвращает *string или nil если строка пустая.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// stringOrEmpty разыменовывает *string или возвращает "".
func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// encodePageToken кодирует created_at + id в непрозрачный page_token.
func encodePageToken(createdAt time.Time, id string) string {
	raw := strconv.FormatInt(createdAt.UnixNano(), 10) + ":" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodePageToken декодирует page_token обратно в (created_at, id).
func decodePageToken(token string) (time.Time, string, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, "", err
	}
	parts := strings.SplitN(string(b), ":", 2)
	if len(parts) != 2 {
		return time.Time{}, "", errors.New("malformed token")
	}
	ns, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", err
	}
	return time.Unix(0, ns).UTC(), parts[1], nil
}
