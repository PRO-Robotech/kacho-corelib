// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package retry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/retry"
)

// Unavailable повторяется до успеха; первый интервал ~100ms (а не cenkalti 500ms)
// — косвенно подтверждает, что backoff.Build() корректно инициализирован.
func TestOnUnavailable_RetriesThenSucceeds(t *testing.T) {
	calls := 0
	start := time.Now()
	err := retry.OnUnavailable(context.Background(), func(context.Context) error {
		calls++
		if calls < 3 {
			return status.Error(codes.Unavailable, "peer down")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, calls, "fn вызван до успеха")
	assert.Less(t, time.Since(start), 5*time.Second, "ретраи быстрые (первый интервал ~100ms)")
}

// Не-retryable код возвращается немедленно (fail-fast), без повторов.
func TestOnUnavailable_NonRetryableFailFast(t *testing.T) {
	calls := 0
	err := retry.OnUnavailable(context.Background(), func(context.Context) error {
		calls++
		return status.Error(codes.InvalidArgument, "bad")
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	assert.Equal(t, 1, calls, "не-retryable код не повторяется")
}

// nil от fn — немедленный успех, ровно один вызов.
func TestOnCodes_SuccessFirstTry(t *testing.T) {
	calls := 0
	err := retry.OnCodes(context.Background(), func(context.Context) error { calls++; return nil }, codes.Unavailable)
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

// Отмена ctx прерывает retry-цикл и возвращает ctx.Err().
func TestOnCodes_CtxCancelStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	calls := 0
	err := retry.OnCodes(ctx, func(context.Context) error {
		calls++
		return status.Error(codes.Unavailable, "still down")
	}, codes.Unavailable)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.GreaterOrEqual(t, calls, 1)
}

// Aborted повторяется через OnAborted (OCC-retry), Unavailable — нет.
func TestOnAborted_SelectsAbortedOnly(t *testing.T) {
	calls := 0
	err := retry.OnAborted(context.Background(), func(context.Context) error {
		calls++
		return status.Error(codes.Unavailable, "down")
	})
	assert.Equal(t, codes.Unavailable, status.Code(err), "Unavailable не входит в OnAborted-набор")
	assert.Equal(t, 1, calls)
}

// Не-grpc error (например, обычный errors.New) — fail-fast без повторов.
func TestOnCodes_NonGRPCErrorFailFast(t *testing.T) {
	calls := 0
	sentinel := errors.New("plain")
	err := retry.OnCodes(context.Background(), func(context.Context) error { calls++; return sentinel }, codes.Unavailable)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, 1, calls)
}
