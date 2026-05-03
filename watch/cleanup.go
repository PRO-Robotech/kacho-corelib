package watch

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	cleanupInterval = 5 * time.Minute
	retentionPeriod = 1 * time.Hour
)

// RunCleanup запускает фоновую горутину cleanup для таблицы resource_events.
// Удаляет события старше 1 часа каждые 5 минут.
// Для координации между репликами использует pg_advisory_xact_lock.
// svc — имя сервиса, используется для генерации lock-ключа ("kacho_<svc>_cleanup").
func RunCleanup(ctx context.Context, pool *pgxpool.Pool, svc string) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCleanupOnce(ctx, pool, svc)
		}
	}
}

// runCleanupOnce выполняет один цикл cleanup в транзакции с advisory lock.
func runCleanupOnce(ctx context.Context, pool *pgxpool.Pool, svc string) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Пробуем взять advisory lock (non-blocking).
	// Если другая реплика уже делает cleanup — пропускаем этот цикл.
	var locked bool
	lockKey := "kacho_" + svc + "_cleanup"
	if err := tx.QueryRow(ctx,
		`SELECT pg_try_advisory_xact_lock(hashtext($1))`, lockKey,
	).Scan(&locked); err != nil || !locked {
		return
	}

	_, err = tx.Exec(ctx,
		`DELETE FROM resource_events WHERE created_at < now() - $1::interval`,
		retentionPeriod.String(),
	)
	if err != nil {
		return
	}

	_ = tx.Commit(ctx)
}
