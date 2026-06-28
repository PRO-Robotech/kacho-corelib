// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package shutdown_test

import (
	"context"
	"errors"
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
