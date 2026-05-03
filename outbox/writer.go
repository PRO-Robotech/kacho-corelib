package outbox

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Writer записывает события в таблицу resource_events и отправляет pg_notify.
type Writer struct {
	channel string
}

// NewWriter создаёт Writer для заданного канала pg_notify.
// channel должен быть "kacho_<svc>", например "kacho_resource_manager".
func NewWriter(channel string) *Writer {
	return &Writer{channel: channel}
}

// WriteEvent вставляет событие в resource_events внутри переданной транзакции.
// Должна вызываться вместе с изменением ресурсной таблицы в одной транзакции.
// Возвращает resource_version, выданный sequence-ом (через DEFAULT nextval).
func (w *Writer) WriteEvent(ctx context.Context, tx pgx.Tx, evt Event) (int64, error) {
	var rv int64
	err := tx.QueryRow(ctx,
		`INSERT INTO resource_events (event_type, resource_kind, resource_uid, data)
		 VALUES ($1, $2, $3, $4) RETURNING resource_version`,
		evt.EventType, evt.ResourceKind, evt.ResourceUID, evt.Data,
	).Scan(&rv)
	return rv, err
}

// Notify отправляет pg_notify на канал после commit транзакции.
// Вызывается вне транзакции — wake-up для Watch Hub в этой и других репликах.
// Payload — пустая строка (только сигнал, без данных).
func (w *Writer) Notify(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, "SELECT pg_notify($1, '')", w.channel)
	return err
}
