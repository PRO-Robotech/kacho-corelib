// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

// Integration-тесты (testcontainers Postgres) для pgRepo.ClaimForExecution —
// pre-execution liveness re-arm + terminal-state guard. Unit-покрытие
// (execution_claim_test.go) гоняет только in-memory claimGateRepo и НЕ проверяет
// реальный SQL-контракт CAS:
//   - живая (done=false) строка → live=true И modified_at re-arm'ится;
//   - уже-терминальная (done=true) строка → live=false, CAS её не трогает;
//   - несуществующая строка → live=false (0 rows affected);
//   - сериализация с терминальной записью reconciler'а под row-lock (FOR UPDATE
//     SKIP LOCKED): пока reconciler держит lock и коммитит терминал, конкурентный
//     ClaimForExecution блокируется, а после commit'а видит done=true → live=false
//     (нет phantom-исполнения fn поверх уже-разрешённой операции).

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// executionClaimer — узкий интерфейс, реализуемый pgRepo (метод ClaimForExecution
// не входит в публичный operations.Repo, Worker получает его type-assertion'ом).
// Структурная сатисфакция даёт доступ к нему из external test-пакета.
type executionClaimer interface {
	ClaimForExecution(ctx context.Context, id string) (bool, error)
}

func asClaimer(t *testing.T, repo operations.Repo) executionClaimer {
	t.Helper()
	c, ok := repo.(executionClaimer)
	require.True(t, ok, "pgRepo должен реализовывать ClaimForExecution (executionClaimer)")
	return c
}

func getModifiedAt(t *testing.T, pool *pgxpool.Pool, id string) time.Time {
	t.Helper()
	var ts time.Time
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT modified_at FROM operations WHERE id=$1`, id).Scan(&ts))
	return ts
}

// Живая (done=false) строка → live=true, modified_at сдвигается (liveness heartbeat).
func TestRepo_ClaimForExecution_LiveRow_ReturnsLive(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	claimer := asClaimer(t, repo)

	op, err := operations.New("enp", "claim live", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	before := getModifiedAt(t, pool, op.ID)
	time.Sleep(2 * time.Millisecond)

	live, err := claimer.ClaimForExecution(ctx, op.ID)
	require.NoError(t, err)
	assert.True(t, live, "живая (done=false) строка → live=true")

	after := getModifiedAt(t, pool, op.ID)
	assert.True(t, after.After(before),
		"ClaimForExecution должен re-arm'ить modified_at (before=%s after=%s)", before, after)
}

// Уже-терминальная (done=true) строка → live=false, CAS её не трогает.
func TestRepo_ClaimForExecution_TerminalRow_ReturnsNotLive(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	claimer := asClaimer(t, repo)

	op, err := operations.New("enp", "claim terminal", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))
	require.NoError(t, repo.MarkDone(ctx, op.ID, mustAnyVal(t, "R")))

	before := getModifiedAt(t, pool, op.ID)
	time.Sleep(2 * time.Millisecond)

	live, err := claimer.ClaimForExecution(ctx, op.ID)
	require.NoError(t, err)
	assert.False(t, live, "уже-терминальная (done=true) строка → live=false")

	after := getModifiedAt(t, pool, op.ID)
	assert.Equal(t, before, after, "CAS не должен трогать терминальную строку (0 rows affected)")
}

// Несуществующая строка → live=false, без ошибки (0 rows affected).
func TestRepo_ClaimForExecution_NonexistentRow_ReturnsNotLive(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	claimer := asClaimer(t, repo)

	live, err := claimer.ClaimForExecution(ctx, "enp00000000000000000")
	require.NoError(t, err)
	assert.False(t, live, "несуществующая строка → live=false")
}

// Сериализация с терминальной записью reconciler'а под row-lock. Reconciler-tx
// claim'ит orphan (FOR UPDATE SKIP LOCKED) и пишет терминал, НЕ коммитя. Конкурентный
// ClaimForExecution (single-statement UPDATE … WHERE done=false) обязан блокироваться
// на том же row-lock'е; после commit'а reconciler-tx он видит done=true → live=false
// (queued-операцию reconciler уже разрешил как orphan → worker пропустит fn, phantom'а нет).
func TestRepo_ClaimForExecution_SerializesWithTerminalWrite(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	claimer := asClaimer(t, repo)

	op, err := operations.New("enp", "claim serialize", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	// Reconciler-подобная транзакция: claim через FOR UPDATE SKIP LOCKED + терминал,
	// commit отложен (держим row-lock).
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()

	var lockedID string
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT id FROM operations WHERE id=$1 AND done=false FOR UPDATE SKIP LOCKED`, op.ID).
		Scan(&lockedID), "reconciler claim'ит orphan через FOR UPDATE SKIP LOCKED")
	_, err = tx.Exec(ctx,
		`UPDATE operations SET done=true, error_code=13, error_message='interrupted' WHERE id=$1`, op.ID)
	require.NoError(t, err)

	// Конкурентный ClaimForExecution должен блокироваться на row-lock'е.
	liveCh := make(chan bool, 1)
	errCh := make(chan error, 1)
	go func() {
		live, cerr := claimer.ClaimForExecution(context.Background(), op.ID)
		errCh <- cerr
		liveCh <- live
	}()

	select {
	case <-liveCh:
		t.Fatal("ClaimForExecution не должен возвращаться, пока reconciler держит row-lock")
	case <-time.After(200 * time.Millisecond):
		// ожидаемо: claim блокируется на lock'е
	}

	// Reconciler коммитит терминал → row-lock освобождается, done=true виден claim'у.
	require.NoError(t, tx.Commit(ctx))

	select {
	case cerr := <-errCh:
		require.NoError(t, cerr)
		assert.False(t, <-liveCh,
			"после commit'а терминала claim видит done=true → live=false (fn пропускается, phantom'а нет)")
	case <-time.After(3 * time.Second):
		t.Fatal("ClaimForExecution не разблокировался после commit'а reconciler-tx")
	}

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	assert.True(t, got.Done, "строка терминальна после reconciler-commit'а")
}
