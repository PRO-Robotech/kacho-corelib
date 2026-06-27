// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestTransactor_InTx_CommitsOnSuccess(t *testing.T) {
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

	txtor := NewTransactor(pool)

	// Транзакция без ошибки — должна закоммититься.
	err = txtor.InTx(ctx, func(_ pgx.Tx) error {
		return nil
	})
	require.NoError(t, err)
}
