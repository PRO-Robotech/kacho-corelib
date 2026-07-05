// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package outbox_test

// Integration-тест (testcontainers Postgres) для outbox.Emit — единственного
// exported writer'а транзакционного outbox-паттерна. До этого теста пакет покрывал
// только приватный sanitizeTable (emit_internal_test.go), а drainer-тесты пишут
// строки сырым INSERT'ом (insertOutboxRow), НИКОГДА не вызывая Emit — то есть сам
// контракт Emit (атомарность с ресурсной DML + pg_notify-trigger + column-set)
// оставался непроверенным (findings6 TEST #1, project-rule #12).
//
// Проверяем:
//   - happy: Emit внутри tx → commit → строка с ожидаемым column-set и JSONB round-trip;
//   - атомарность: Emit внутри tx → ROLLBACK → НОЛЬ строк (outbox-write не «утёк»
//     мимо транзакции ресурса — ключевая гарантия dual-write-consistency);
//   - trigger: pg_notify доставляет sequence_no подписчику на LISTEN-conn;
//   - negatives: пустое имя таблицы и non-marshalable payload → error без паники.

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/PRO-Robotech/kacho-corelib/outbox"
)

// outboxSchema — таблица с фиксированной схемой из doc-контракта outbox.Emit
// (sequence_no BIGSERIAL PK, resource_kind, resource_id, event_type, payload JSONB,
// created_at) + AFTER INSERT trigger pg_notify('<channel>', sequence_no). Emit
// вставляет (resource_kind, resource_id, event_type, payload); sequence_no/created_at
// проставляет БД.
const outboxNotifyChannel = "kacho_outbox_test"

const outboxSchema = `
CREATE TABLE test_outbox (
    sequence_no   bigserial    PRIMARY KEY,
    resource_kind text         NOT NULL,
    resource_id   text         NOT NULL,
    event_type    text         NOT NULL,
    payload       jsonb        NOT NULL,
    created_at    timestamptz  NOT NULL DEFAULT now()
);

CREATE OR REPLACE FUNCTION test_outbox_notify() RETURNS trigger
LANGUAGE plpgsql AS $fn$
BEGIN
    PERFORM pg_notify('` + outboxNotifyChannel + `', NEW.sequence_no::text);
    RETURN NEW;
END;
$fn$;

CREATE TRIGGER test_outbox_notify_trigger
    AFTER INSERT ON test_outbox
    FOR EACH ROW EXECUTE FUNCTION test_outbox_notify();
`

func setupOutboxPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() || os.Getenv("SKIP_INTEGRATION") == "1" {
		t.Skip("integration tests skipped (SKIP_INTEGRATION=1)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("kacho_outbox_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		termCtx, termCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer termCancel()
		_ = ctr.Terminate(termCtx)
	})

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = pool.Exec(ctx, outboxSchema)
	require.NoError(t, err, "apply outboxSchema")

	return pool
}

// TestEmit_Commit_WritesRowWithContract — happy path: Emit внутри tx → commit →
// ровно одна строка с ожидаемым column-set и JSONB round-trip.
func TestEmit_Commit_WritesRowWithContract(t *testing.T) {
	pool := setupOutboxPG(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)

	payload := map[string]any{"name": "net-a", "cidr": "10.0.0.0/16"}
	require.NoError(t, outbox.Emit(ctx, tx, "test_outbox", "Network", "enp_abc", "CREATED", payload))
	require.NoError(t, tx.Commit(ctx))

	var (
		kind, id, eventType string
		rawPayload          []byte
		seq                 int64
	)
	err = pool.QueryRow(ctx,
		`SELECT sequence_no, resource_kind, resource_id, event_type, payload FROM test_outbox`,
	).Scan(&seq, &kind, &id, &eventType, &rawPayload)
	require.NoError(t, err)

	assert.Positive(t, seq, "sequence_no должен быть проставлен БД (BIGSERIAL)")
	assert.Equal(t, "Network", kind)
	assert.Equal(t, "enp_abc", id)
	assert.Equal(t, "CREATED", eventType)

	var got map[string]any
	require.NoError(t, json.Unmarshal(rawPayload, &got))
	assert.Equal(t, "net-a", got["name"])
	assert.Equal(t, "10.0.0.0/16", got["cidr"])
}

// TestEmit_Rollback_LeavesNoRow — атомарность: Emit внутри tx, затем ROLLBACK →
// НОЛЬ строк. Это ядро транзакционного outbox: outbox-write обязан быть атомарен с
// ресурсной DML — откат ресурса откатывает и событие (иначе dual-write рассинхрон,
// ради предотвращения которого outbox и существует).
func TestEmit_Rollback_LeavesNoRow(t *testing.T) {
	pool := setupOutboxPG(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, outbox.Emit(ctx, tx, "test_outbox", "Network", "enp_rb", "CREATED", map[string]any{"x": 1}))
	require.NoError(t, tx.Rollback(ctx))

	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM test_outbox`).Scan(&count))
	assert.Zero(t, count, "rollback обязан не оставить ни одной outbox-строки")
}

// TestEmit_TriggerDeliversNotify — pg_notify-trigger будит подписчика: LISTEN на
// выделенном connection получает уведомление с sequence_no после commit'а Emit.
func TestEmit_TriggerDeliversNotify(t *testing.T) {
	pool := setupOutboxPG(t)
	ctx := context.Background()

	// Выделенный listener-connection (уведомления PG привязаны к сессии).
	lc, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer lc.Release()
	_, err = lc.Exec(ctx, "LISTEN "+outboxNotifyChannel)
	require.NoError(t, err)

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, outbox.Emit(ctx, tx, "test_outbox", "Subnet", "e9b_xyz", "UPDATED", map[string]any{"k": "v"}))
	require.NoError(t, tx.Commit(ctx)) // NOTIFY доставляется на commit.

	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	n, err := lc.Conn().WaitForNotification(waitCtx)
	require.NoError(t, err, "trigger должен доставить pg_notify после commit")
	assert.Equal(t, outboxNotifyChannel, n.Channel)
	assert.NotEmpty(t, n.Payload, "payload уведомления — sequence_no новой строки")
}

// TestEmit_EmptyTable_Errors — negative: пустое имя таблицы → error, без паники и
// без обращения к БД.
func TestEmit_EmptyTable_Errors(t *testing.T) {
	pool := setupOutboxPG(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	err = outbox.Emit(ctx, tx, "", "Network", "enp_x", "CREATED", map[string]any{"a": 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "table name required")
}

// TestEmit_NonMarshalablePayload_Errors — negative: payload, который json.Marshal
// не умеет сериализовать (канал), → error без паники; строка не пишется.
func TestEmit_NonMarshalablePayload_Errors(t *testing.T) {
	pool := setupOutboxPG(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	err = outbox.Emit(ctx, tx, "test_outbox", "Network", "enp_x", "CREATED", map[string]any{"bad": make(chan int)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "marshal payload")

	require.NoError(t, tx.Commit(ctx))
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM test_outbox`).Scan(&count))
	assert.Zero(t, count, "неудачный marshal не должен оставить строк")
}
