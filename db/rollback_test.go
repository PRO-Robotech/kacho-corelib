// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// rollback/panic/commit-семантика InTx на реальной БД: транзакция атомарна —
// при ошибке/панике fn записи откатываются.
func TestTransactor_InTx_RollbackSemantics(t *testing.T) {
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

	_, err = pool.Exec(ctx, `CREATE TABLE items (id INT PRIMARY KEY)`)
	require.NoError(t, err)

	txtor := NewTransactor(pool)
	count := func() int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM items`).Scan(&n))
		return n
	}

	t.Run("commit_persists", func(t *testing.T) {
		err := txtor.InTx(ctx, func(tx pgx.Tx) error {
			_, e := tx.Exec(ctx, `INSERT INTO items (id) VALUES (1)`)
			return e
		})
		require.NoError(t, err)
		assert.Equal(t, 1, count(), "успешная транзакция закоммичена")
	})

	t.Run("error_rolls_back", func(t *testing.T) {
		boom := errors.New("boom")
		err := txtor.InTx(ctx, func(tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `INSERT INTO items (id) VALUES (2)`); e != nil {
				return e
			}
			return boom // откат
		})
		assert.ErrorIs(t, err, boom, "InTx возвращает ошибку fn")
		assert.Equal(t, 1, count(), "запись id=2 откатана (нет в таблице)")
	})

	t.Run("panic_rolls_back_and_propagates", func(t *testing.T) {
		func() {
			defer func() {
				r := recover()
				assert.NotNil(t, r, "паника fn должна пробрасываться наружу")
			}()
			_ = txtor.InTx(ctx, func(tx pgx.Tx) error {
				_, _ = tx.Exec(ctx, `INSERT INTO items (id) VALUES (3)`)
				panic("kaboom")
			})
		}()
		assert.Equal(t, 1, count(), "запись id=3 откатана при панике")
	})
}

// NewPool делает fail-fast при недоступной БД (Ping), а не отдает ленивый пул.
func TestNewPool_FailFastOnUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// Валидный DSN, но порт закрыт — Ping обязан упасть → NewPool возвращает ошибку.
	_, err := NewPool(ctx, "postgres://test:test@127.0.0.1:1/test?sslmode=disable")
	require.Error(t, err, "NewPool обязан fail-fast при недоступной БД")
}
