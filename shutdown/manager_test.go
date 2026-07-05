// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package shutdown_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/shutdown"
)

// Close выполняет handlers в LIFO-порядке (обратном регистрации).
func TestClose_RunsHandlersLIFO(t *testing.T) {
	m := shutdown.New()
	var order []int
	var mu sync.Mutex
	rec := func(n int) shutdown.Handler {
		return func() error { mu.Lock(); order = append(order, n); mu.Unlock(); return nil }
	}
	m.OnExit(rec(1))
	m.OnExit(rec(2), rec(3))
	require.NoError(t, m.Close())
	assert.Equal(t, []int{3, 2, 1}, order, "handlers выполняются LIFO")
}

// Close идемпотентен — повторный вызов не выполняет handlers снова.
func TestClose_Idempotent(t *testing.T) {
	m := shutdown.New()
	var calls int
	m.OnExit(func() error { calls++; return nil })
	require.NoError(t, m.Close())
	require.NoError(t, m.Close())
	assert.Equal(t, 1, calls, "handler выполнен ровно один раз")
}

// Close возвращает первую ошибку handler'а.
func TestClose_ReturnsFirstHandlerError(t *testing.T) {
	m := shutdown.New()
	boom := errors.New("boom")
	m.OnExit(func() error { return nil })  // выполнится вторым (LIFO)
	m.OnExit(func() error { return boom }) // выполнится первым → его ошибка
	assert.ErrorIs(t, m.Close(), boom)
}

// Wait инициирует Close при отмене внешнего ctx и возвращает ctx.Err().
func TestWait_CtxCancelTriggersClose(t *testing.T) {
	m := shutdown.New()
	var ran bool
	m.OnExit(func() error { ran = true; return nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := m.Wait(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	assert.True(t, ran, "Wait при cancel ctx обязан выполнить handlers")
}

// Зависший handler ограничивается timeout'ом: бросается (ErrHandlerTimeout),
// остальные handlers выполняются, Close завершается в пределах timeout'а.
func TestClose_HandlerTimeout(t *testing.T) {
	m := shutdown.New(shutdown.WithHandlerTimeout(60 * time.Millisecond))
	var fastRan bool
	m.OnExit(func() error { fastRan = true; return nil })        // выполнится вторым (LIFO)
	m.OnExit(func() error { <-make(chan struct{}); return nil }) // зависает навсегда

	start := time.Now()
	err := m.Close()
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, shutdown.ErrHandlerTimeout, "зависший handler → ErrHandlerTimeout")
	assert.True(t, fastRan, "остальные handlers выполняются несмотря на зависший")
	assert.Less(t, elapsed, 2*time.Second, "Close не висит на зависшем handler'е")
}

// OnExit после Close выполняет handler немедленно (no silent loss).
func TestOnExitAfterClose_RunsImmediately(t *testing.T) {
	m := shutdown.New()
	require.NoError(t, m.Close())
	var ran bool
	m.OnExit(func() error { ran = true; return nil })
	assert.True(t, ran, "поздно зарегистрированный handler выполняется сразу, а не теряется")
}

// OnExit после Close, когда handler вернул ошибку — ошибка НЕ теряется молча,
// а логируется через сконфигурированный logger (наблюдаемость best-effort
// cleanup на фазе завершения).
func TestOnExitAfterClose_LogsHandlerError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m := shutdown.New(shutdown.WithLogger(logger))
	require.NoError(t, m.Close())

	boom := errors.New("late cleanup failed")
	m.OnExit(func() error { return boom })

	out := buf.String()
	assert.Contains(t, out, "late cleanup failed",
		"ошибка позднего handler'а должна быть залогирована, а не отброшена молча")
}

// Late-handler timeout также логируется (ErrHandlerTimeout), не теряется молча.
func TestOnExitAfterClose_LogsHandlerTimeout(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	m := shutdown.New(
		shutdown.WithLogger(logger),
		shutdown.WithHandlerTimeout(50*time.Millisecond),
	)
	require.NoError(t, m.Close())

	m.OnExit(func() error { <-make(chan struct{}); return nil }) // зависает навсегда

	out := buf.String()
	assert.True(t, strings.Contains(out, "timed out") || strings.Contains(out, "timeout"),
		"timeout позднего handler'а должен быть залогирован; got: %q", out)
}
