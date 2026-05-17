package operations_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

const createTable = `
CREATE TABLE IF NOT EXISTS operations (
  id                     TEXT         PRIMARY KEY,
  description            TEXT         NOT NULL,
  created_at             TIMESTAMPTZ  NOT NULL DEFAULT now(),
  created_by             TEXT         NOT NULL DEFAULT 'anonymous',
  modified_at            TIMESTAMPTZ  NOT NULL DEFAULT now(),
  done                   BOOLEAN      NOT NULL DEFAULT false,
  metadata_type          TEXT,
  metadata_data          BYTEA,
  resource_id            TEXT,
  error_code             INT,
  error_message          TEXT,
  error_details          BYTEA,
  response_type          TEXT,
  response_data          BYTEA,
  principal_type         TEXT         NOT NULL DEFAULT 'system',
  principal_id           TEXT         NOT NULL DEFAULT 'bootstrap',
  principal_display_name TEXT         NOT NULL DEFAULT 'System'
);
CREATE INDEX IF NOT EXISTS operations_resource_idx   ON operations (resource_id);
CREATE INDEX IF NOT EXISTS operations_done_idx        ON operations (done);
CREATE INDEX IF NOT EXISTS operations_created_at_idx  ON operations (created_at);
`

// setupPostgres поднимает контейнер Postgres и применяет schema.
func setupPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("SKIP_INTEGRATION") == "1" {
		t.Skip("integration tests skipped (SKIP_INTEGRATION=1)")
	}

	ctx := context.Background()

	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = pool.Exec(ctx, createTable)
	require.NoError(t, err, "ошибка создания таблицы operations")

	return pool
}

// newRepo создаёт Repo для схемы public.
func newRepo(pool *pgxpool.Pool) operations.Repo {
	return operations.NewRepo(pool, "public")
}

// TestRepo_Create_Get — создание и чтение операции.
func TestRepo_Create_Get(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "test create/get", nil)
	require.NoError(t, err)
	op.CreatedAt = op.CreatedAt.Round(time.Microsecond)
	op.ModifiedAt = op.ModifiedAt.Round(time.Microsecond)

	require.NoError(t, repo.Create(ctx, op))

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.Equal(t, op.ID, got.ID)
	assert.Equal(t, op.Description, got.Description)
	assert.Equal(t, op.CreatedBy, got.CreatedBy)
	assert.False(t, got.Done)
	assert.Nil(t, got.Error)
	assert.Nil(t, got.Response)
}

// TestRepo_Get_NotFound — Get несуществующей операции возвращает ErrNotFound.
func TestRepo_Get_NotFound(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	_, err := repo.Get(ctx, "00000000-0000-0000-0000-000000000000")
	assert.ErrorIs(t, err, operations.ErrNotFound)
}

// TestRepo_MarkDone — операция переходит в done=true с финальным ресурсом.
func TestRepo_MarkDone(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "test mark done", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	// Создаём фиктивный response (StringValue как Any)
	strVal := wrapperspb.String("instance-123")
	response, err := anypb.New(strVal)
	require.NoError(t, err)

	require.NoError(t, repo.MarkDone(ctx, op.ID, response))

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.True(t, got.Done)
	assert.Nil(t, got.Error)
	require.NotNil(t, got.Response)
	assert.Equal(t, response.GetTypeUrl(), got.Response.GetTypeUrl())
	assert.Equal(t, response.GetValue(), got.Response.GetValue())
}

// TestRepo_MarkError_WithDetails — операция переходит в done=true с ошибкой.
func TestRepo_MarkError_WithDetails(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "test mark error", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	errStatus := &status.Status{
		Code:    5, // NOT_FOUND
		Message: "resource not found",
	}

	require.NoError(t, repo.MarkError(ctx, op.ID, errStatus))

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.True(t, got.Done)
	require.NotNil(t, got.Error)
	assert.Equal(t, int32(5), got.Error.GetCode())
	assert.Equal(t, "resource not found", got.Error.GetMessage())
	assert.Nil(t, got.Response)
}

// TestRepo_List_FilterByResourceID — List фильтрует по resource_id.
func TestRepo_List_FilterByResourceID(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	// Создаём несколько операций без привязки к ресурсу
	for i := 0; i < 3; i++ {
		op, err := operations.New("opx", fmt.Sprintf("op %d", i), nil)
		require.NoError(t, err)
		require.NoError(t, repo.Create(ctx, op))
	}

	// Создаём операцию с metadata, содержащей поле *_id
	// Используем wrapperspb.StringValue как прокси — у него нет _id поля,
	// поэтому resource_id в БД будет пустым. Создаём вручную через setResourceID helper.
	// Для проверки фильтра создаём операцию через прямой INSERT с resource_id.
	targetResourceID := "test-resource-aabbcc"
	_, err := pool.Exec(ctx, `
		INSERT INTO operations (id, description, done, resource_id)
		VALUES (gen_random_uuid(), 'op for resource', false, $1)
	`, targetResourceID)
	require.NoError(t, err)

	// Ещё одна операция для другого ресурса
	_, err = pool.Exec(ctx, `
		INSERT INTO operations (id, description, done, resource_id)
		VALUES (gen_random_uuid(), 'op for other resource', false, 'other-resource-id')
	`)
	require.NoError(t, err)

	// Фильтр по resource_id
	ops, nextToken, err := repo.List(ctx, operations.ListFilter{
		ResourceID: targetResourceID,
		PageSize:   10,
	})
	require.NoError(t, err)
	assert.Empty(t, nextToken)
	require.Len(t, ops, 1)
	assert.Equal(t, "op for resource", ops[0].Description)
}

// TestRepo_List_Pagination — проверка пагинации.
func TestRepo_List_Pagination(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	// Создаём 5 операций
	for i := 0; i < 5; i++ {
		op, err := operations.New("opx", fmt.Sprintf("paginate op %d", i), nil)
		require.NoError(t, err)
		require.NoError(t, repo.Create(ctx, op))
	}

	// Первая страница — 3 записи
	page1, nextToken, err := repo.List(ctx, operations.ListFilter{PageSize: 3})
	require.NoError(t, err)
	assert.Len(t, page1, 3)
	assert.NotEmpty(t, nextToken)

	// Вторая страница
	page2, nextToken2, err := repo.List(ctx, operations.ListFilter{
		PageSize:  3,
		PageToken: nextToken,
	})
	require.NoError(t, err)
	assert.Len(t, page2, 2)
	assert.Empty(t, nextToken2)

	// Все ID уникальны
	allIDs := make(map[string]struct{})
	for _, op := range append(page1, page2...) {
		allIDs[op.ID] = struct{}{}
	}
	assert.Len(t, allIDs, 5, "все 5 операций должны быть уникальны")
}

// TestRepo_Cancel — Cancel переводит операцию в done=true с кодом CANCELLED.
func TestRepo_Cancel(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "test cancel", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	require.NoError(t, repo.Cancel(ctx, op.ID))

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.True(t, got.Done)
	require.NotNil(t, got.Error)
	assert.Equal(t, int32(1), got.Error.GetCode(), "код должен быть CANCELLED=1")
}

// TestWorker_Success — Run вызывает MarkDone при успехе fn.
func TestWorker_Success(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "worker success test", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	done := make(chan struct{})
	strVal := wrapperspb.String("result-value")
	response, err := anypb.New(strVal)
	require.NoError(t, err)

	operations.Run(ctx, repo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		defer close(done)
		return response, nil
	})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker не завершился в течение 3с")
	}

	// Небольшая пауза для записи в БД
	time.Sleep(50 * time.Millisecond)

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.True(t, got.Done)
	assert.Nil(t, got.Error)
	require.NotNil(t, got.Response)
	assert.Equal(t, response.GetTypeUrl(), got.Response.GetTypeUrl())
}

// TestWorker_Failure — Run вызывает MarkError при ошибке fn.
func TestWorker_Failure(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("opx", "worker failure test", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	done := make(chan struct{})

	operations.Run(ctx, repo, op.ID, func(ctx context.Context) (*anypb.Any, error) {
		defer close(done)
		return nil, fmt.Errorf("something went wrong")
	})

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker не завершился в течение 3с")
	}

	time.Sleep(50 * time.Millisecond)

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.True(t, got.Done)
	require.NotNil(t, got.Error)
	assert.Nil(t, got.Response)
}

// TestNew_WithMetadata — helpers.New создаёт Operation с правильными полями.
func TestNew_WithMetadata(t *testing.T) {
	meta := wrapperspb.String("instance-uid-xyz")
	op, err := operations.New("opx", "create instance", meta)
	require.NoError(t, err)

	assert.NotEmpty(t, op.ID)
	assert.Equal(t, "create instance", op.Description)
	assert.Equal(t, "anonymous", op.CreatedBy)
	assert.False(t, op.Done)
	assert.NotNil(t, op.Metadata)
	assert.NotZero(t, op.CreatedAt)
	assert.NotZero(t, op.ModifiedAt)
}

// TestNew_NilMetadata — helpers.New без metadata.
func TestNew_NilMetadata(t *testing.T) {
	op, err := operations.New("opx", "no metadata op", nil)
	require.NoError(t, err)
	assert.NotEmpty(t, op.ID)
	assert.Nil(t, op.Metadata)
}
