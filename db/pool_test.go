// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestNewPool_ParseErrorDoesNotLeakDSN — CWE-532 boundary-guard: при
// неразбираемом DSN текст возвращаемой NewPool ошибки НЕ должен содержать
// inline-пароль. Сейчас это обеспечивает сам pgx v5: `pgxpool.ParseConfig`
// редактирует пароль (`user:xxxxxx@`) в тексте ошибки для обеих форм DSN
// (URL и keyword/value). Тест фиксирует инвариант на нашей границе — чтобы
// апгрейд/свап pgx или будущая обёртка NewPool, вернувшая raw dsn, были пойманы
// регрессией (composition-root логирует эту ошибку на старте).
//
// Пробел в host форсит сбой url.Parse ВНУТРИ pgx ParseConfig ДО любого сетевого
// I/O — детерминированно, без Postgres.
func TestNewPool_ParseErrorDoesNotLeakDSN(t *testing.T) {
	const secret = "s3cr3t-passw0rd"
	dsn := "postgres://user:" + secret + "@bad host:5432/db"

	_, err := NewPool(context.Background(), dsn)

	require.Error(t, err, "malformed DSN must fail at ParseConfig")
	require.NotContains(t, err.Error(), secret,
		"startup error must not echo the DSN password (CWE-532)")
}

func TestNewPool_PingsAndStatementTimeoutSet(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (testcontainers); skipped with -short")
	}
	ctx := context.Background()
	pgC, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pgC.Terminate(ctx) })

	dsn, err := pgC.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := NewPool(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	require.NoError(t, pool.Ping(ctx))

	// statement_timeout = 30s
	var st string
	require.NoError(t, pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&st))
	require.Equal(t, "30s", st)

	// idle_in_transaction_session_timeout — server-side guard: reaps a
	// transaction left idle (open but not executing) longer than the bound,
	// independent of app-side ctx correctness. Value read in base units (ms)
	// via pg_settings for a deterministic assertion.
	var idleTO string
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT setting FROM pg_settings WHERE name = 'idle_in_transaction_session_timeout'").Scan(&idleTO))
	require.Equal(t, "60000", idleTO,
		"idle_in_transaction_session_timeout must be set (ms) so a hung tx is reaped by Postgres")
}
