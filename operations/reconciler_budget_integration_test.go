// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

// Integration-тест (testcontainers Postgres): claim-транзакция одного Sweep'а
// обязана иметь потолок СУММАРНОЙ длительности (SweepBudget), а не только per-item
// ResolveTimeout. Без агрегатного потолка медленный Resolver (peer-outage) держал
// бы claim-tx открытой до BatchSize×ResolveTimeout (~1000s), удерживая пул-коннект,
// FOR UPDATE row-locks и xmin-горизонт operations-таблицы против VACUUM всё окно
// (findings6 DATA #4). При исчерпании бюджета Sweep коммитит разрешённое и выходит;
// неразрешённые orphan'ы остаются durable и добираются следующим Sweep'ом.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// TestReconciler_SweepBudget_BoundsClaimTx проверяет, что при медленном Resolver'е
// один Sweep разрешает лишь часть батча (укладываясь в SweepBudget) и завершается
// заметно раньше, чем занял бы полный батч без потолка; последующие Sweep'ы
// гарантируют прогресс до полного восстановления.
func TestReconciler_SweepBudget_BoundsClaimTx(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	repo := operations.NewRepo(pool, "public")

	const n = 6
	const delay = 300 * time.Millisecond
	const budget = 450 * time.Millisecond

	ids := make([]string, n)
	outcome := map[string]operations.ResolverResult{}
	for i := 0; i < n; i++ {
		id := "budget-" + itoa(i)
		ids[i] = id
		insertOrphan(t, ctx, pool, id, 20*time.Minute)
		outcome[id] = operations.ResolverResult{Outcome: operations.OutcomeDone, Response: mustAnyVal(t, "r")}
	}

	res := &delayResolver{delay: delay, outcome: outcome}
	rc := operations.NewReconciler(pool, res, operations.ReconcilerConfig{
		Schema:      "public",
		OrphanGrace: time.Minute,
		BatchSize:   100,
		SweepBudget: budget,
	})

	// Первый Sweep: бюджет режет батч — разрешено СТРОГО меньше n, и sweep
	// завершается заметно раньше полного n×delay (=1.8s).
	start := time.Now()
	first, err := rc.Sweep(ctx)
	elapsed := time.Since(start)
	require.NoError(t, err)

	assert.Positive(t, first, "хотя бы один orphan должен быть разрешён до исчерпания бюджета")
	assert.Less(t, first, n, "SweepBudget обязан оборвать батч до полного разрешения всех orphan'ов")
	assert.Less(t, elapsed, time.Duration(n)*delay,
		"claim-tx одного Sweep'а не должна занимать полный n×delay — бюджет ограничивает суммарную длительность")

	// Прогресс гарантирован: повторные Sweep'ы добирают остаток durable-orphan'ов.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		remaining := 0
		for _, id := range ids {
			op, gerr := repo.Get(ctx, id)
			require.NoError(t, gerr)
			if !op.Done {
				remaining++
			}
		}
		if remaining == 0 {
			break
		}
		_, err = rc.Sweep(ctx)
		require.NoError(t, err)
	}

	for _, id := range ids {
		op, gerr := repo.Get(ctx, id)
		require.NoError(t, gerr)
		assert.True(t, op.Done, "orphan %s должен быть восстановлен последующими Sweep'ами", id)
	}
}
