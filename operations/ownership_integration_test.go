// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

// Integration-тесты (testcontainers Postgres) ownership-scoped чтения/отмены
// операций (срез VPC8-B): GetOwned/CancelOwned — ownership-предикат внутри
// SQL WHERE / атомарного CAS. Чужой principal → ErrNotFound (no-leak); владелец
// → OK; идемпотентный re-cancel; уже-завершённая → ErrAlreadyDone; match по паре
// (principal_type, principal_id); concurrent CAS (сценарий 10).

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

func ownedRepo(t *testing.T, pool *pgxpool.Pool) operations.OwnedOperationRepo {
	t.Helper()
	owned, ok := operations.AsOwned(newRepo(pool))
	require.True(t, ok, "*pgRepo обязан реализовывать OwnedOperationRepo")
	return owned
}

func createOwnedOp(t *testing.T, ctx context.Context, repo operations.Repo, desc string, p operations.Principal) operations.Operation {
	t.Helper()
	op, err := operations.New("enp", desc, nil)
	require.NoError(t, err)
	require.NoError(t, repo.CreateWithPrincipal(ctx, op, p))
	return op
}

var (
	usrA = operations.Principal{Type: "user", ID: "usr-A", DisplayName: "A"}
	usrB = operations.Principal{Type: "user", ID: "usr-B", DisplayName: "B"}
	svaX = operations.Principal{Type: "service_account", ID: "sva-X", DisplayName: "X"}
)

// vpc8-B-01 / 02: владелец GetOwned → OK; чужой → ErrNotFound (no-leak).
func TestOwnership_GetOwned_OwnerOK_StrangerNotFound(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	owned := ownedRepo(t, pool)

	op := createOwnedOp(t, ctx, repo, "owned by A", usrA)

	got, err := owned.GetOwned(ctx, op.ID, operations.OwnerFromPrincipal(usrA))
	require.NoError(t, err)
	assert.Equal(t, op.ID, got.ID)

	_, err = owned.GetOwned(ctx, op.ID, operations.OwnerFromPrincipal(usrB))
	assert.ErrorIs(t, err, operations.ErrNotFound, "чужой principal → NotFound (no-leak)")

	// Well-formed-но-нет → тот же ErrNotFound (неотличимо).
	_, err = owned.GetOwned(ctx, "enp00000000000000000", operations.OwnerFromPrincipal(usrB))
	assert.ErrorIs(t, err, operations.ErrNotFound)
}

// vpc8-B-03 / 04: владелец CancelOwned in-flight → OK + CANCELLED; чужой →
// ErrNotFound, жертва НЕ мутирована.
func TestOwnership_CancelOwned_OwnerCancels_StrangerNoMutation(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	owned := ownedRepo(t, pool)

	op := createOwnedOp(t, ctx, repo, "cancel by A", usrA)

	// Чужой Cancel — NotFound, никакой мутации.
	_, err := owned.CancelOwned(ctx, op.ID, operations.OwnerFromPrincipal(usrB))
	require.ErrorIs(t, err, operations.ErrNotFound)
	got, err := owned.GetOwned(ctx, op.ID, operations.OwnerFromPrincipal(usrA))
	require.NoError(t, err)
	assert.False(t, got.Done, "после чужого Cancel жертва осталась done=false")

	// Владелец Cancel — OK + CANCELLED.
	res, err := owned.CancelOwned(ctx, op.ID, operations.OwnerFromPrincipal(usrA))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.Done)
	require.NotNil(t, res.Error)
	assert.Equal(t, int32(1), res.Error.GetCode())
	assert.Equal(t, "operation cancelled", res.Error.GetMessage())
}

// vpc8-B-05: идемпотентность — re-Cancel владельцем уже-отменённой → OK, без
// повторной мутации (modified_at не прыгает).
func TestOwnership_CancelOwned_Idempotent_ReCancel(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	owned := ownedRepo(t, pool)

	op := createOwnedOp(t, ctx, repo, "idem", usrA)

	res1, err := owned.CancelOwned(ctx, op.ID, operations.OwnerFromPrincipal(usrA))
	require.NoError(t, err)
	mod1 := res1.ModifiedAt

	res2, err := owned.CancelOwned(ctx, op.ID, operations.OwnerFromPrincipal(usrA))
	require.NoError(t, err, "re-cancel уже-CANCELLED → идемпотентный OK")
	require.NotNil(t, res2)
	assert.True(t, res2.Done)
	assert.Equal(t, int32(1), res2.Error.GetCode())
	assert.Equal(t, mod1.UnixNano(), res2.ModifiedAt.UnixNano(),
		"modified_at не сдвинулся — повторной мутации нет (CAS WHERE done=false не совпал)")
}

// vpc8-B-06: Cancel завершённой УСПЕХОМ операции владельцем → ErrAlreadyDone.
func TestOwnership_CancelOwned_TerminalSuccess_AlreadyDone(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	owned := ownedRepo(t, pool)

	op := createOwnedOp(t, ctx, repo, "succeeded", usrA)
	require.NoError(t, repo.MarkDone(ctx, op.ID, mustAnyVal(t, "R")))

	_, err := owned.CancelOwned(ctx, op.ID, operations.OwnerFromPrincipal(usrA))
	assert.ErrorIs(t, err, operations.ErrAlreadyDone,
		"terminal SUCCESS → FAILED_PRECONDITION (нельзя отменить применённый результат)")
}

// vpc8-B-11: match по ПАРЕ (principal_type, principal_id), не по id.
func TestOwnership_MatchByPrincipalPair_NotIDAlone(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	owned := ownedRepo(t, pool)

	op := createOwnedOp(t, ctx, repo, "owned by SA", svaX)

	// Владелец service_account/sva-X → OK.
	_, err := owned.GetOwned(ctx, op.ID, operations.OwnerFromPrincipal(svaX))
	require.NoError(t, err)

	// user/usr-X (тот же суффикс id, другой тип) → NotFound (коллизии типов нет).
	usrX := operations.Principal{Type: "user", ID: "sva-X"}
	_, err = owned.GetOwned(ctx, op.ID, operations.OwnerFromPrincipal(usrX))
	assert.ErrorIs(t, err, operations.ErrNotFound)
}

// vpc8-B-10: concurrent CancelOwned. N владельческих + M чужих goroutine'ов.
// Ровно один владельческий мутирует (done=false→true, CANCELLED); чужие → 0 строк
// (NotFound), ни один не мутирует; финал — CANCELLED от владельца.
func TestOwnership_CancelOwned_ConcurrentCAS(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)
	owned := ownedRepo(t, pool)

	const n, m = 6, 6
	for round := 0; round < 10; round++ {
		op := createOwnedOp(t, ctx, repo, "conc", usrA)

		var wg sync.WaitGroup
		start := make(chan struct{})
		var ownerOK atomic.Int64 // владельческие OK (мутация ИЛИ идемпотентный)
		var strangerNotFound atomic.Int64

		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				res, err := owned.CancelOwned(ctx, op.ID, operations.OwnerFromPrincipal(usrA))
				if err == nil && res != nil {
					ownerOK.Add(1)
				}
			}()
		}
		for i := 0; i < m; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := owned.CancelOwned(ctx, op.ID, operations.OwnerFromPrincipal(usrB))
				if assert.ErrorIs(t, err, operations.ErrNotFound) {
					strangerNotFound.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()

		assert.Equal(t, int64(m), strangerNotFound.Load(), "все чужие → NotFound (round %d)", round)
		assert.Equal(t, int64(n), ownerOK.Load(), "все владельческие → OK (мутация или идемпотентно)")

		got, err := owned.GetOwned(ctx, op.ID, operations.OwnerFromPrincipal(usrA))
		require.NoError(t, err)
		assert.True(t, got.Done)
		require.NotNil(t, got.Error)
		assert.Equal(t, int32(1), got.Error.GetCode(), "финал — CANCELLED от владельца (round %d)", round)
		assert.Equal(t, "usr-A", got.Principal.ID, "никакого second-writer-wins от usr-B")
	}
}
