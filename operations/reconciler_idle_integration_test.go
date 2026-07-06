// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

// Integration-тест (testcontainers Postgres): claim-транзакция Sweep'а не должна
// накапливать idle-in-transaction через последовательные OutcomeSkip/resolver-error
// орфаны и умирать по серверному idle_in_transaction_session_timeout — иначе весь
// батч откатывается, и LRO-recovery не прогрессирует именно во время peer-outage,
// ради восстановления которого reconciler и существует.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// delayResolver имитирует медленный доменный Resolver (peer-outage): каждый резолв
// «висит» delay, не касаясь claim-коннекта (repo.Get идёт на другой пул-коннект).
// По умолчанию возвращает OutcomeSkip; для перечисленных id — заданный исход.
type delayResolver struct {
	delay   time.Duration
	outcome map[string]operations.ResolverResult
}

func (r *delayResolver) Resolve(ctx context.Context, op operations.Operation) (operations.ResolverResult, error) {
	select {
	case <-time.After(r.delay):
	case <-ctx.Done():
		return operations.ResolverResult{}, ctx.Err()
	}
	if o, ok := r.outcome[op.ID]; ok {
		return o, nil
	}
	return operations.ResolverResult{Outcome: operations.OutcomeSkip}, nil
}

// TestReconciler_ClaimTx_SurvivesConsecutiveSkips проверяет, что серия OutcomeSkip
// орфанов, каждый из которых сжигает ~delay idle на claim-tx, НЕ приводит к
// серверному abort'у транзакции: cumulative idle (5×500ms = 2.5s) существенно
// превышает idle_in_transaction_session_timeout (1s), поэтому без keep-alive на
// Skip-ветке Postgres убил бы claim-tx и весь батч (включая разрешимый орфан в
// конце) откатился бы.
func TestReconciler_ClaimTx_SurvivesConsecutiveSkips(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	// Короткий серверный потолок idle-in-transaction, чтобы тест был быстрым и
	// детерминированным (в проде — 60s, но принцип тот же).
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "1000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	repo := operations.NewRepo(pool, "public")

	// 5 skip-орфанов (старше) обрабатываются ПЕРЕД разрешимым (modufied_at ASC).
	for i := 0; i < 5; i++ {
		insertOrphan(t, ctx, pool, "idle-skip-"+itoa(i), 20*time.Minute)
	}
	// Разрешимый орфан в конце батча (новее → последний по modified_at ASC).
	insertOrphan(t, ctx, pool, "idle-done-last", 10*time.Minute)

	res := &delayResolver{
		delay: 500 * time.Millisecond,
		outcome: map[string]operations.ResolverResult{
			"idle-done-last": {Outcome: operations.OutcomeDone, Response: mustAnyVal(t, "recovered")},
		},
	}
	rc := operations.NewReconciler(pool, res, operations.ReconcilerConfig{
		Schema:      "public",
		OrphanGrace: time.Minute,
		BatchSize:   100,
	})

	n, err := rc.Sweep(ctx)
	require.NoError(t, err, "claim-tx должна пережить серию Skip (keep-alive сбрасывает idle-таймер)")
	assert.Equal(t, 1, n, "разрешимый орфан в конце батча должен быть восстановлен")

	got, err := repo.Get(ctx, "idle-done-last")
	require.NoError(t, err)
	assert.True(t, got.Done, "орфан позже в батче восстановлен, батч не откачен idle-timeout'ом")
}
