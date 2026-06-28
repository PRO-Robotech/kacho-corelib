// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package outbox_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/PRO-Robotech/kacho-corelib/outbox"
)

// setupPostgres поднимает контейнер Postgres и применяет migration 0001.
func setupPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() || os.Getenv("SKIP_INTEGRATION") == "1" {
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

	// Применяем migration 0001 вручную (без goose, чтобы не добавлять зависимость в test).
	migration := `
CREATE SEQUENCE IF NOT EXISTS resource_version_seq;

CREATE TABLE IF NOT EXISTS resource_events (
  resource_version BIGINT      PRIMARY KEY DEFAULT nextval('resource_version_seq'),
  event_type       TEXT        NOT NULL CHECK (event_type IN ('ADDED', 'MODIFIED', 'DELETED')),
  resource_kind    TEXT        NOT NULL,
  resource_uid     UUID        NOT NULL,
  data             BYTEA,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS organizations (
  uid              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  name             TEXT        NOT NULL UNIQUE,
  resource_version BIGINT
);
`
	_, err = pool.Exec(ctx, migration)
	require.NoError(t, err)

	return pool
}

// A1: WriteEvent + UPDATE ресурса в одной транзакции — обе записи commit вместе.
func TestWriter_A1_AtomicCommit(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	w := outbox.NewWriter("kacho_resource_manager")

	uid := "11111111-1111-1111-1111-111111111111"

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)

	// Вставляем ресурс
	_, err = tx.Exec(ctx,
		`INSERT INTO organizations (uid, name) VALUES ($1, $2)`,
		uid, "test-org-a1",
	)
	require.NoError(t, err)

	// Пишем событие в outbox
	rv, err := w.WriteEvent(ctx, tx, outbox.Event{
		EventType:    "ADDED",
		ResourceKind: "Organization",
		ResourceUID:  uid,
		Data:         []byte("proto-encoded-data"),
	})
	require.NoError(t, err)
	assert.Greater(t, rv, int64(0), "resource_version должен быть > 0")

	// Обновляем resource_version в ресурсной таблице
	_, err = tx.Exec(ctx,
		`UPDATE organizations SET resource_version = $1 WHERE uid = $2`,
		rv, uid,
	)
	require.NoError(t, err)

	require.NoError(t, tx.Commit(ctx))

	// Проверяем, что обе записи сохранились
	var orgCount int
	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM organizations WHERE uid = $1`, uid,
	).Scan(&orgCount)
	require.NoError(t, err)
	assert.Equal(t, 1, orgCount, "организация должна быть в таблице")

	var eventRV int64
	var eventType, eventKind string
	err = pool.QueryRow(ctx,
		`SELECT resource_version, event_type, resource_kind FROM resource_events WHERE resource_uid = $1`,
		uid,
	).Scan(&eventRV, &eventType, &eventKind)
	require.NoError(t, err)
	assert.Equal(t, rv, eventRV, "resource_version должен совпадать")
	assert.Equal(t, "ADDED", eventType)
	assert.Equal(t, "Organization", eventKind)
}

// A2: rollback → нет записи в resource_events и organizations.
func TestWriter_A2_AtomicRollback(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	w := outbox.NewWriter("kacho_resource_manager")

	uid := "22222222-2222-2222-2222-222222222222"

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)

	_, err = tx.Exec(ctx,
		`INSERT INTO organizations (uid, name) VALUES ($1, $2)`,
		uid, "test-org-a2",
	)
	require.NoError(t, err)

	rv, err := w.WriteEvent(ctx, tx, outbox.Event{
		EventType:    "ADDED",
		ResourceKind: "Organization",
		ResourceUID:  uid,
	})
	require.NoError(t, err)
	assert.Greater(t, rv, int64(0))

	// Откатываем транзакцию
	require.NoError(t, tx.Rollback(ctx))

	// Проверяем: в organizations и resource_events записей нет
	var orgCount int
	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM organizations WHERE uid = $1`, uid,
	).Scan(&orgCount)
	require.NoError(t, err)
	assert.Equal(t, 0, orgCount, "организации не должно быть после rollback")

	var evCount int
	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM resource_events WHERE resource_uid = $1`, uid,
	).Scan(&evCount)
	require.NoError(t, err)
	assert.Equal(t, 0, evCount, "события не должно быть после rollback")

	// Sequence не откатывается (gaps допустимы)
	var nextRV int64
	err = pool.QueryRow(ctx, `SELECT nextval('resource_version_seq')`).Scan(&nextRV)
	require.NoError(t, err)
	assert.Greater(t, nextRV, rv, "sequence монотонно возрастает, gaps допустимы")
}

// A3: Notify — pg_notify отправляется после commit, слушатель получает уведомление.
func TestWriter_A3_NotifyAfterCommit(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	w := outbox.NewWriter("kacho_resource_manager")

	uid := "33333333-3333-3333-3333-333333333333"

	// Отдельное соединение для LISTEN
	dsn := pool.Config().ConnString()
	listenConn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = listenConn.Close(ctx) }()

	_, err = listenConn.Exec(ctx, "LISTEN kacho_resource_manager")
	require.NoError(t, err)

	notifyCh := make(chan struct{}, 1)
	go func() {
		notification, err := listenConn.WaitForNotification(context.Background())
		if err == nil && notification.Channel == "kacho_resource_manager" {
			notifyCh <- struct{}{}
		}
	}()

	// Commit транзакции
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)

	_, err = tx.Exec(ctx,
		`INSERT INTO organizations (uid, name) VALUES ($1, $2)`,
		uid, "test-org-a3",
	)
	require.NoError(t, err)

	_, err = w.WriteEvent(ctx, tx, outbox.Event{
		EventType:    "ADDED",
		ResourceKind: "Organization",
		ResourceUID:  uid,
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	// Notify после commit
	err = w.Notify(ctx, pool)
	require.NoError(t, err)

	select {
	case <-notifyCh:
		// ok
	case <-time.After(500 * time.Millisecond):
		t.Fatal("не получили pg_notify в течение 500мс")
	}
}

// A8: атомарность через ROLLBACK injection — WriteEvent + ресурс в одной TX, rollback.
func TestWriter_A8_AtomicRollbackInjection(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	w := outbox.NewWriter("kacho_resource_manager")

	uid := "88888888-8888-8888-8888-888888888888"

	// Первая транзакция: успешный commit
	func() {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		_, err = tx.Exec(ctx,
			`INSERT INTO organizations (uid, name) VALUES ($1, $2)`,
			uid, "test-org-a8",
		)
		require.NoError(t, err)
		_, err = w.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "ADDED",
			ResourceKind: "Organization",
			ResourceUID:  uid,
		})
		require.NoError(t, err)
		require.NoError(t, tx.Commit(ctx))
	}()

	// Вторая транзакция: обновление + ROLLBACK
	func() {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		_, err = tx.Exec(ctx,
			`UPDATE organizations SET name = $1 WHERE uid = $2`,
			"test-org-a8-updated", uid,
		)
		require.NoError(t, err)
		_, err = w.WriteEvent(ctx, tx, outbox.Event{
			EventType:    "MODIFIED",
			ResourceKind: "Organization",
			ResourceUID:  uid,
		})
		require.NoError(t, err)
		// Инъекция отката
		require.NoError(t, tx.Rollback(ctx))
	}()

	// После rollback: имя не изменилось, событие MODIFIED не сохранилось
	var name string
	err := pool.QueryRow(ctx, `SELECT name FROM organizations WHERE uid = $1`, uid).Scan(&name)
	require.NoError(t, err)
	assert.Equal(t, "test-org-a8", name, "имя не должно было измениться")

	var evCount int
	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM resource_events WHERE resource_uid = $1 AND event_type = 'MODIFIED'`,
		uid,
	).Scan(&evCount)
	require.NoError(t, err)
	assert.Equal(t, 0, evCount, "событие MODIFIED не должно быть сохранено")
}
