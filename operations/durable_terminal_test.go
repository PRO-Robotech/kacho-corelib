// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

// Unit-тесты durable terminal-write: retry/backoff на
// transient DB-сбое, no-swallow на исчерпании ретраев, panic → MarkError,
// симметрия MarkDone/MarkError. Прогон без Postgres — терминальная запись
// тестируется через flakyRepo (инъекция сбоя на уровне Repo).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// flakyRepo — in-memory Repo с инъекцией сбоев в терминальную запись. Только
// методы, нужные worker'у (MarkDone/MarkError). errTransient — не-sentinel
// ошибка, которую durable-loop трактует как transient и ретраит.
type flakyRepo struct {
	mu sync.Mutex

	// markDoneFailsLeft / markErrorFailsLeft — сколько первых попыток упадет.
	markDoneFailsLeft  int
	markErrorFailsLeft int
	// alwaysFailDone / alwaysFailError — бесконечный сбой (для no-swallow теста).
	alwaysFailDone  bool
	alwaysFailError bool

	done    map[string]*anypb.Any
	errored map[string]*rpcstatus.Status

	doneAttempts  int
	errorAttempts int
}

var errTransient = errors.New("connection refused: terminal write transient")

func newFlakyRepo() *flakyRepo {
	return &flakyRepo{
		done:    map[string]*anypb.Any{},
		errored: map[string]*rpcstatus.Status{},
	}
}

func (r *flakyRepo) Create(context.Context, operations.Operation) error { return nil }
func (r *flakyRepo) CreateWithPrincipal(context.Context, operations.Operation, operations.Principal) error {
	return nil
}
func (r *flakyRepo) Get(context.Context, string) (*operations.Operation, error) {
	return nil, operations.ErrNotFound
}
func (r *flakyRepo) List(context.Context, operations.ListFilter) ([]operations.Operation, string, error) {
	return nil, "", nil
}
func (r *flakyRepo) Cancel(context.Context, string) error { return nil }

func (r *flakyRepo) MarkDone(_ context.Context, id string, resp *anypb.Any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doneAttempts++
	if r.alwaysFailDone {
		return errTransient
	}
	if r.markDoneFailsLeft > 0 {
		r.markDoneFailsLeft--
		return errTransient
	}
	r.done[id] = resp
	return nil
}

func (r *flakyRepo) MarkError(_ context.Context, id string, st *rpcstatus.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errorAttempts++
	if r.alwaysFailError {
		return errTransient
	}
	if r.markErrorFailsLeft > 0 {
		r.markErrorFailsLeft--
		return errTransient
	}
	r.errored[id] = st
	return nil
}

func (r *flakyRepo) doneOf(id string) (*anypb.Any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.done[id]
	return v, ok
}

func (r *flakyRepo) errorOf(id string) (*rpcstatus.Status, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.errored[id]
	return v, ok
}

// fastTerminalWorker — Worker с коротким retry-budget (тест быстрый) +
// MemRecorder. Stop через t.Cleanup.
func fastTerminalWorker(t *testing.T, rec operations.Recorder) *operations.Worker {
	t.Helper()
	w := operations.NewWorker(
		operations.WithRecorder(rec),
		operations.WithTerminalWriteConfig(operations.TerminalWriteConfig{
			InitialInterval: 5 * time.Millisecond,
			MaxInterval:     20 * time.Millisecond,
			MaxElapsed:      200 * time.Millisecond,
		}),
	)
	w.Start()
	t.Cleanup(w.Stop)
	return w
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

// Первая попытка MarkDone падает transient → worker ретраит → success.
func TestWorker_MarkDone_RetriesTransient(t *testing.T) {
	rec := operations.NewMemRecorder()
	repo := newFlakyRepo()
	repo.markDoneFailsLeft = 1
	w := fastTerminalWorker(t, rec)

	resp := mustAny(t, wrapperspb.String("ok"))
	operations.RunWithWorker(w, context.Background(), repo, "op-retry", func(context.Context) (*anypb.Any, error) {
		return resp, nil
	})

	waitFor(t, 3*time.Second, func() bool { _, ok := repo.doneOf("op-retry"); return ok })
	assert.GreaterOrEqual(t, rec.TerminalWriteRetries("MarkDone"), float64(1),
		"должен быть >=1 retry на transient-сбое MarkDone")
	assert.Equal(t, float64(0), rec.TerminalWriteFailures("MarkDone"),
		"в итоге запись прошла — failures не инкрементятся")
}

// DB недоступна дольше budget → MarkDone не проглочен (failures++),
// строка done НЕ записана (reconciler-backstop добьет позже).
func TestWorker_MarkDone_PermanentFail_NotSwallowed(t *testing.T) {
	rec := operations.NewMemRecorder()
	repo := newFlakyRepo()
	repo.alwaysFailDone = true
	w := fastTerminalWorker(t, rec)

	resp := mustAny(t, wrapperspb.String("ok"))
	operations.RunWithWorker(w, context.Background(), repo, "op-perm", func(context.Context) (*anypb.Any, error) {
		return resp, nil
	})

	waitFor(t, 3*time.Second, func() bool { return rec.TerminalWriteFailures("MarkDone") >= 1 })
	_, ok := repo.doneOf("op-perm")
	assert.False(t, ok, "при невосстановимом сбое строка НЕ помечается done (остается для reconciler)")
	assert.GreaterOrEqual(t, rec.TerminalWriteRetries("MarkDone"), float64(1),
		"перед сдачей было >=1 retry")
}

// panic в worker-fn → recover → MarkError(INTERNAL "internal worker
// error"); процесс жив (следующая операция на том же worker'е проходит).
func TestWorker_Panic_MarksError_ProcessAlive(t *testing.T) {
	rec := operations.NewMemRecorder()
	repo := newFlakyRepo()
	w := fastTerminalWorker(t, rec)

	operations.RunWithWorker(w, context.Background(), repo, "op-panic", func(context.Context) (*anypb.Any, error) {
		var p *int
		_ = *p // nil-deref panic
		return nil, nil
	})

	waitFor(t, 3*time.Second, func() bool { _, ok := repo.errorOf("op-panic"); return ok })
	st, _ := repo.errorOf("op-panic")
	require.NotNil(t, st)
	assert.Equal(t, int32(13), st.GetCode(), "panic → INTERNAL(13)")
	assert.Equal(t, "internal worker error", st.GetMessage())

	// Процесс жив: следующая операция успешно завершается.
	resp := mustAny(t, wrapperspb.String("alive"))
	operations.RunWithWorker(w, context.Background(), repo, "op-after-panic", func(context.Context) (*anypb.Any, error) {
		return resp, nil
	})
	waitFor(t, 3*time.Second, func() bool { _, ok := repo.doneOf("op-after-panic"); return ok })
}

// MarkError симметрично MarkDone — retry на transient-сбое.
func TestWorker_MarkError_RetriesTransient(t *testing.T) {
	rec := operations.NewMemRecorder()
	repo := newFlakyRepo()
	repo.markErrorFailsLeft = 1
	w := fastTerminalWorker(t, rec)

	operations.RunWithWorker(w, context.Background(), repo, "op-err-retry", func(context.Context) (*anypb.Any, error) {
		return nil, errors.New("business failure")
	})

	waitFor(t, 3*time.Second, func() bool { _, ok := repo.errorOf("op-err-retry"); return ok })
	assert.GreaterOrEqual(t, rec.TerminalWriteRetries("MarkError"), float64(1),
		"должен быть >=1 retry на transient-сбое MarkError")
}
