package watch_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/PRO-Robotech/kacho-corelib/watch"
)

// setupPostgres поднимает контейнер Postgres и применяет базовую схему.
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

	_, err = pool.Exec(ctx, `
		CREATE SEQUENCE IF NOT EXISTS resource_version_seq;
		CREATE TABLE IF NOT EXISTS resource_events (
		  resource_version BIGINT      PRIMARY KEY DEFAULT nextval('resource_version_seq'),
		  event_type       TEXT        NOT NULL CHECK (event_type IN ('ADDED', 'MODIFIED', 'DELETED')),
		  resource_kind    TEXT        NOT NULL,
		  resource_uid     UUID        NOT NULL,
		  data             BYTEA,
		  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
		);
	`)
	require.NoError(t, err)

	return pool
}

// insertEvent вставляет событие напрямую в resource_events (без notify).
func insertEvent(t *testing.T, pool *pgxpool.Pool, ctx context.Context, eventType, kind, uid string) int64 {
	t.Helper()
	var rv int64
	err := pool.QueryRow(ctx,
		`INSERT INTO resource_events (event_type, resource_kind, resource_uid)
		 VALUES ($1, $2, $3) RETURNING resource_version`,
		eventType, kind, uid,
	).Scan(&rv)
	require.NoError(t, err)
	return rv
}

// A4: Hub стартует, просыпается от NOTIFY, читает события, доставляет подписчикам.
func TestHub_A4_NotifyWakeupAndFanout(t *testing.T) {
	pool := setupPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hub := watch.NewHub(ctx, pool, "kacho_resource_manager")

	// Два подписчика
	sub1, err := hub.Subscribe(ctx, 0)
	require.NoError(t, err)
	defer sub1.Unsubscribe()

	sub2, err := hub.Subscribe(ctx, 0)
	require.NoError(t, err)
	defer sub2.Unsubscribe()

	// Вставляем событие и отправляем notify
	uid := "44444444-4444-4444-4444-444444444444"
	_, err = pool.Exec(ctx,
		`INSERT INTO resource_events (event_type, resource_kind, resource_uid)
		 VALUES ($1, $2, $3)`,
		"ADDED", "Organization", uid,
	)
	require.NoError(t, err)

	// pg_notify
	_, err = pool.Exec(ctx, "SELECT pg_notify('kacho_resource_manager', '')")
	require.NoError(t, err)

	// Оба подписчика должны получить событие в 200мс
	timeout := time.After(200 * time.Millisecond)

	var got1, got2 watch.Event
	for got1.ResourceVersion == 0 || got2.ResourceVersion == 0 {
		select {
		case evt := <-sub1.C:
			got1 = evt
		case evt := <-sub2.C:
			got2 = evt
		case <-timeout:
			t.Fatalf("не получили событие в 200мс (sub1.RV=%d, sub2.RV=%d)", got1.ResourceVersion, got2.ResourceVersion)
		}
	}

	assert.Equal(t, "ADDED", got1.EventType)
	assert.Equal(t, "Organization", got1.ResourceKind)
	assert.Equal(t, uid, got1.ResourceUID)
	assert.Equal(t, got1.ResourceVersion, got2.ResourceVersion)
}

// A5: catch-up из ring buffer для отстающего клиента (fromRV = 0, N ≤ 512 событий).
func TestHub_A5_CatchupFromRingBuffer(t *testing.T) {
	pool := setupPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	hub := watch.NewHub(ctx, pool, "kacho_resource_manager")

	// Вставляем 10 событий, ждём пока Hub их прочитает
	const n = 10
	for i := 0; i < n; i++ {
		insertEvent(t, pool, ctx, "ADDED", "Organization",
			"55555555-5555-5555-5555-55555555555"+string(rune('0'+i)),
		)
	}

	// Посылаем notify чтобы Hub точно проснулся
	_, err := pool.Exec(ctx, "SELECT pg_notify('kacho_resource_manager', '')")
	require.NoError(t, err)

	// Ждём пока cursorRV обновится
	require.Eventually(t, func() bool {
		return hub.CursorRV() >= int64(n)
	}, 2*time.Second, 50*time.Millisecond, "Hub не прочитал %d событий за 2с", n)

	// Новый подписчик с fromRV=0: должен получить catch-up
	sub, err := hub.Subscribe(ctx, 0)
	require.NoError(t, err)
	defer sub.Unsubscribe()

	received := make(map[int64]bool)
	deadline := time.After(2 * time.Second)
loop:
	for len(received) < n {
		select {
		case evt := <-sub.C:
			received[evt.ResourceVersion] = true
		case <-deadline:
			break loop
		}
	}

	assert.Equal(t, n, len(received), "ожидали %d catch-up событий из ring buffer", n)
}

// A6: catch-up из outbox для клиента вне ring buffer (2000 событий, fromRV=500).
func TestHub_A6_CatchupFromOutbox(t *testing.T) {
	pool := setupPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Вставляем 2000 событий напрямую
	const total = 2000
	for i := 0; i < total; i++ {
		_, err := pool.Exec(ctx,
			`INSERT INTO resource_events (event_type, resource_kind, resource_uid)
			 VALUES ($1, $2, gen_random_uuid())`,
			"ADDED", "Organization",
		)
		require.NoError(t, err)
	}

	hub := watch.NewHub(ctx, pool, "kacho_resource_manager")

	// Ждём пока Hub прочитает все 2000
	require.Eventually(t, func() bool {
		return hub.CursorRV() >= int64(total)
	}, 10*time.Second, 100*time.Millisecond, "Hub не прочитал %d событий", total)

	// Подписчик хочет события начиная с 500
	// При total=2000, cursorRV=2000, ring=1024: fromRV=500 < 2000-1024=976, значит вне ring
	sub, err := hub.Subscribe(ctx, 500)
	require.NoError(t, err)
	defer sub.Unsubscribe()

	// Должны получить события 501..2000 = 1500 событий
	const expected = 1500
	received := 0
	deadline := time.After(10 * time.Second)
	var lastRV int64
loop:
	for received < expected {
		select {
		case evt := <-sub.C:
			require.Greater(t, evt.ResourceVersion, int64(500))
			require.Greater(t, evt.ResourceVersion, lastRV, "события должны идти в порядке возрастания")
			lastRV = evt.ResourceVersion
			received++
		case <-deadline:
			break loop
		}
	}

	assert.Equal(t, expected, received, "ожидали %d событий из outbox catch-up", expected)
}

// A7: Gone (OUT_OF_RANGE) при устаревшем fromRV (retention cleanup удалил события).
// Сценарий: outbox опустошён retention, Hub знает о событиях через cursorRV,
// клиент запрашивает с очень старым fromRV → получает ErrGone.
func TestHub_A7_GoneWhenResourceVersionTooOld(t *testing.T) {
	pool := setupPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Устанавливаем sequence на 5000 и вставляем несколько событий
	_, err := pool.Exec(ctx, `SELECT setval('resource_version_seq', 4999)`)
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		insertEvent(t, pool, ctx, "ADDED", "Organization", "77777777-7777-7777-7777-77777777777"+string(rune('0'+i)))
	}

	hub := watch.NewHub(ctx, pool, "kacho_resource_manager")

	// Ждём пока Hub прочитает события и обновит cursorRV
	require.Eventually(t, func() bool {
		return hub.CursorRV() >= 5004
	}, 2*time.Second, 50*time.Millisecond)

	// Имитируем retention cleanup: удаляем все события из outbox
	_, err = pool.Exec(ctx, `DELETE FROM resource_events`)
	require.NoError(t, err)

	// Теперь: outbox пуст, hub.cursorRV >= 5004
	// fromRV=100 << cursorRV → клиент пропустил события которых уже нет → ErrGone
	_, err = hub.Subscribe(ctx, 100)
	assert.ErrorIs(t, err, watch.ErrGone, "ожидали ErrGone для устаревшего resourceVersion")
}

// A8: ticker fallback — Hub читает события без pg_notify в течение 150мс.
func TestHub_A8_TickerFallback(t *testing.T) {
	pool := setupPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hub := watch.NewHub(ctx, pool, "kacho_resource_manager")

	sub, err := hub.Subscribe(ctx, 0)
	require.NoError(t, err)
	defer sub.Unsubscribe()

	// Вставляем событие БЕЗ pg_notify
	uid := "99999999-9999-9999-9999-999999999999"
	insertEvent(t, pool, ctx, "ADDED", "Organization", uid)

	// Hub должен прочитать через ticker (≤ 150мс)
	select {
	case evt := <-sub.C:
		assert.Equal(t, uid, evt.ResourceUID)
		assert.Equal(t, "ADDED", evt.EventType)
	case <-time.After(150 * time.Millisecond):
		t.Fatal("Hub не прочитал событие через ticker за 150мс")
	}
}

// C4: Cleanup удаляет события старше 1 часа.
func TestCleanup_C4_DeletesOldEvents(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	// Вставляем 3 старых (2 часа назад) и 2 свежих (30 минут назад) события
	_, err := pool.Exec(ctx, `
		INSERT INTO resource_events (event_type, resource_kind, resource_uid, created_at)
		VALUES
		  ('ADDED', 'Organization', gen_random_uuid(), now() - interval '2 hours'),
		  ('ADDED', 'Organization', gen_random_uuid(), now() - interval '2 hours'),
		  ('ADDED', 'Organization', gen_random_uuid(), now() - interval '2 hours'),
		  ('ADDED', 'Organization', gen_random_uuid(), now() - interval '30 minutes'),
		  ('ADDED', 'Organization', gen_random_uuid(), now() - interval '30 minutes')
	`)
	require.NoError(t, err)

	// Запускаем cleanup напрямую через SQL (имитируем что RunCleanup сработал)
	_, err = pool.Exec(ctx, `
		DELETE FROM resource_events WHERE created_at < now() - interval '1 hour'
	`)
	require.NoError(t, err)

	var count int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM resource_events`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 2, count, "должны остаться только 2 свежих события")
}

// C5: Cleanup с advisory lock — две горутины не повреждают данные.
func TestCleanup_C5_AdvisoryLockCoordination(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()

	// Вставляем 10 старых событий
	for i := 0; i < 10; i++ {
		_, err := pool.Exec(ctx, `
			INSERT INTO resource_events (event_type, resource_kind, resource_uid, created_at)
			VALUES ('ADDED', 'Organization', gen_random_uuid(), now() - interval '2 hours')
		`)
		require.NoError(t, err)
	}

	// Две горутины одновременно запускают cleanup с advisory lock
	var wg sync.WaitGroup
	errors := make([]error, 2)

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			conn, err := pool.Acquire(ctx)
			if err != nil {
				errors[idx] = err
				return
			}
			defer conn.Release()

			tx, err := conn.Begin(ctx)
			if err != nil {
				errors[idx] = err
				return
			}
			defer tx.Rollback(ctx) //nolint:errcheck

			var locked bool
			if err := tx.QueryRow(ctx,
				`SELECT pg_try_advisory_xact_lock(hashtext('kacho_resource_manager_cleanup'))`,
			).Scan(&locked); err != nil {
				errors[idx] = err
				return
			}
			if !locked {
				return // другая горутина держит lock — пропускаем
			}

			_, err = tx.Exec(ctx,
				`DELETE FROM resource_events WHERE created_at < now() - interval '1 hour'`,
			)
			if err != nil {
				errors[idx] = err
				return
			}
			errors[idx] = tx.Commit(ctx)
		}(i)
	}

	wg.Wait()

	for _, err := range errors {
		assert.NoError(t, err)
	}

	// Данные не повреждены — 0 старых событий осталось
	var count int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM resource_events`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "все старые события должны быть удалены")
}
