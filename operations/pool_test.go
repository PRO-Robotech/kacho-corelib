// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

// Unit-тесты bounded worker-pool + backpressure + readiness (срез E, стадия
// S4). Прогон без Postgres — пул и admission-backlog проверяются на in-memory
// Repo и «медленных» fn.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// E4-01 + E4-02: max-inflight = K; submit M >> K «медленных» операций. В любой
// момент число одновременно исполняемых worker'ов <= K (operations_inflight <=
// K), ни одна не теряется — в итоге ВСЕ M доходят до done.
func TestPool_BoundedUnderBurst(t *testing.T) {
	const (
		k = 4
		m = 40
	)
	rec := operations.NewMemRecorder()
	repo := newFlakyRepo()
	w := operations.NewWorker(
		operations.WithMaxInflight(k),
		operations.WithRecorder(rec),
	)
	w.Start()
	t.Cleanup(w.Stop)

	var concurrent atomic.Int64
	var peak atomic.Int64
	release := make(chan struct{})
	resp := mustAny(t, wrapperspb.String("done"))

	for i := 0; i < m; i++ {
		id := "burst-" + itoa(i)
		operations.RunWithWorker(w, context.Background(), repo, id, func(context.Context) (*anypb.Any, error) {
			cur := concurrent.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			<-release // держим слот занятым, пока тест не разрешит
			concurrent.Add(-1)
			return resp, nil
		})
	}

	// Дождёмся насыщения пула: ровно K в работе, остальные ждут в backlog.
	waitFor(t, 3*time.Second, func() bool { return concurrent.Load() == int64(k) })
	assert.LessOrEqual(t, peak.Load(), int64(k), "одновременно исполняемых worker'ов не больше K")
	assert.LessOrEqual(t, rec.MaxInflight(), float64(k), "operations_inflight <= max всегда")

	// Освобождаем — все M должны завершиться (ничего не потеряно).
	close(release)
	waitFor(t, 5*time.Second, func() bool {
		cnt := 0
		for i := 0; i < m; i++ {
			if _, ok := repo.doneOf("burst-" + itoa(i)); ok {
				cnt++
			}
		}
		return cnt == m
	})
	assert.LessOrEqual(t, peak.Load(), int64(k), "пик одновременности так и не превысил K")
}

// E4-03: readiness отражает dispatcher-loop. Loop жив → Ready; loop остановлен
// → NotReady.
func TestReadiness_ReflectsDispatcher(t *testing.T) {
	w := operations.NewWorker(operations.WithMaxInflight(2))
	w.Start()
	assert.True(t, w.Ready(), "запущенный dispatcher-loop → Ready")

	w.Stop()
	assert.False(t, w.Ready(), "остановленный dispatcher-loop → NotReady")
}

// E4-04: backpressure не ломает async-контракт — RunWithWorker не блокируется
// сверх admission-границы (возвращает управление сразу, операция durable).
func TestPool_AsyncContract_RunDoesNotBlock(t *testing.T) {
	rec := operations.NewMemRecorder()
	repo := newFlakyRepo()
	w := operations.NewWorker(operations.WithMaxInflight(1), operations.WithRecorder(rec))
	w.Start()
	t.Cleanup(w.Stop)

	release := make(chan struct{})
	resp := mustAny(t, wrapperspb.String("ok"))
	var wg sync.WaitGroup

	// Насыщаем единственный слот «медленной» операцией.
	operations.RunWithWorker(w, context.Background(), repo, "slow", func(context.Context) (*anypb.Any, error) {
		<-release
		return resp, nil
	})
	waitFor(t, 2*time.Second, func() bool { return rec.Inflight() == 1 })

	// Пул насыщен. Ещё один submit — RunWithWorker обязан вернуться немедленно.
	returned := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		operations.RunWithWorker(w, context.Background(), repo, "queued", func(context.Context) (*anypb.Any, error) {
			return resp, nil
		})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RunWithWorker заблокировался при насыщенном пуле — нарушен async-контракт")
	}

	close(release)
	wg.Wait()
	// Обе операции в итоге терминальны (durable backlog не потерян).
	waitFor(t, 3*time.Second, func() bool {
		_, a := repo.doneOf("slow")
		_, b := repo.doneOf("queued")
		return a && b
	})
}

// itoa — локальный helper без strconv-импорта в hot-loop теста.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

var _ = require.NoError
