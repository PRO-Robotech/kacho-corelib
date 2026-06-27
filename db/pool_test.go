// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package db

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

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
}
