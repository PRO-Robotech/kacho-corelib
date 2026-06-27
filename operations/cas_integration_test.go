// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

// Integration-тесты (testcontainers Postgres) CAS-on-`done` терминальной
// записи (срез E, стадии S1/S2): MarkDone/MarkError не перезаписывают
// уже-терминальную строку; гонка Cancel <-> MarkDone разрешается ровно в один
// исход, oneof-целостность сохранена.

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// E1-02: MarkDone по уже-`done` строке → ErrAlreadyDone, response не перезаписан.
func TestRepo_MarkDone_CASOnDone_NoOverwrite(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("enp", "cas markdone", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	r1 := mustAnyVal(t, "R1")
	require.NoError(t, repo.MarkDone(ctx, op.ID, r1))

	r2 := mustAnyVal(t, "R2")
	err = repo.MarkDone(ctx, op.ID, r2)
	require.ErrorIs(t, err, operations.ErrAlreadyDone, "повторный MarkDone по done-строке → ErrAlreadyDone")

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Response)
	assert.Equal(t, r1.GetValue(), got.Response.GetValue(), "response остался R1, не перезаписан R2")
}

// E1-06 (a): MarkError по уже-`done` (success) строке → ErrAlreadyDone, oneof не
// повреждён (response сохранён, error не появился).
func TestRepo_MarkError_CASOnDone_NoOverwrite(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	op, err := operations.New("enp", "cas markerror", nil)
	require.NoError(t, err)
	require.NoError(t, repo.Create(ctx, op))

	r1 := mustAnyVal(t, "R1")
	require.NoError(t, repo.MarkDone(ctx, op.ID, r1))

	err = repo.MarkError(ctx, op.ID, &rpcstatus.Status{Code: 13, Message: "boom"})
	require.ErrorIs(t, err, operations.ErrAlreadyDone)

	got, err := repo.Get(ctx, op.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Response, "response сохранён")
	assert.Nil(t, got.Error, "error не появился — oneof-целостность сохранена")
}

// E1-02 backstop: MarkDone по НЕсуществующей строке → ErrNotFound (различение
// вне worker-пути).
func TestRepo_MarkDone_NonexistentRow_NotFound(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	err := repo.MarkDone(ctx, "enp00000000000000000", mustAnyVal(t, "R"))
	assert.ErrorIs(t, err, operations.ErrNotFound)
}

// E2-02: гонка Cancel <-> MarkDone. N goroutine'ов конкурентно: одни Cancel,
// другие MarkDone. Строка терминальна РОВНО в одном состоянии; никогда не set'нуты
// одновременно response и error (oneof-целостность). Прогон с -race.
func TestCancel_vs_MarkDone_Race_ExactlyOne(t *testing.T) {
	pool := setupPostgres(t)
	ctx := context.Background()
	repo := newRepo(pool)

	const rounds = 30
	for round := 0; round < rounds; round++ {
		op, err := operations.New("enp", "race", nil)
		require.NoError(t, err)
		require.NoError(t, repo.Create(ctx, op))

		var wg sync.WaitGroup
		start := make(chan struct{})
		resp := mustAnyVal(t, "R")

		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_ = repo.Cancel(ctx, op.ID)
		}()
		go func() {
			defer wg.Done()
			<-start
			_ = repo.MarkDone(ctx, op.ID, resp)
		}()
		close(start)
		wg.Wait()

		got, err := repo.Get(ctx, op.ID)
		require.NoError(t, err)
		require.True(t, got.Done, "после гонки строка терминальна")

		hasResp := got.Response != nil
		hasErr := got.Error != nil
		assert.False(t, hasResp && hasErr,
			"oneof-целостность: одновременно response И error недопустимы (round %d)", round)
		assert.True(t, hasResp || hasErr, "ровно один из response/error выставлен (round %d)", round)
		if hasErr {
			assert.Equal(t, int32(1), got.Error.GetCode(), "если выиграл Cancel — код CANCELLED")
		}
	}
}

// ---- helpers ----

func mustAnyVal(t *testing.T, s string) *anypb.Any {
	t.Helper()
	a, err := anypb.New(wrapperspb.String(s))
	require.NoError(t, err)
	return a
}
