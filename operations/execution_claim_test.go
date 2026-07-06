// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

// Unit-тесты pre-execution liveness re-arm + terminal-state guard (r9b).
//
// Пока задача ждёт в in-memory admission backlog, её operation-строка НЕ
// heartbeat'ится (modified_at залочен на create-time), поэтому под устойчивой
// перегрузкой reconciler может счесть ещё-живую queued-операцию orphan'ом и
// перевести её в терминал ERROR. Если бы worker затем безусловно исполнил fn, он
// создал бы ресурс, чья Operation уже ERROR — phantom, который клиент никогда не
// доразрешит. Guard: перед fn worker атомарно re-arm'ит modified_at=now И
// подтверждает, что строка ещё done=false; иначе fn ПРОПУСКАЕТСЯ.
//
// Прогон без Postgres — поведение проверяется через claimGateRepo (инъекция
// live/terminal-состояния на уровне Repo, реализующего executionClaimer).

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// claimGateRepo — in-memory Repo, реализующий опциональный executionClaimer.
// live управляет тем, что вернёт ClaimForExecution: true (строка ещё done=false)
// либо false (reconciler уже разрешил orphan). claimErr симулирует transient-сбой
// claim'а. Терминальные записи только учитываются (worker-путь).
type claimGateRepo struct {
	mu         sync.Mutex
	live       bool
	claimErr   error
	claimCalls int
	done       map[string]*anypb.Any
	errored    map[string]*rpcstatus.Status
}

func newClaimGateRepo(live bool) *claimGateRepo {
	return &claimGateRepo{
		live:    live,
		done:    map[string]*anypb.Any{},
		errored: map[string]*rpcstatus.Status{},
	}
}

func (r *claimGateRepo) Create(context.Context, operations.Operation) error { return nil }
func (r *claimGateRepo) CreateWithPrincipal(context.Context, operations.Operation, operations.Principal) error {
	return nil
}
func (r *claimGateRepo) Get(context.Context, string) (*operations.Operation, error) {
	return nil, operations.ErrNotFound
}
func (r *claimGateRepo) List(context.Context, operations.ListFilter) ([]operations.Operation, string, error) {
	return nil, "", nil
}
func (r *claimGateRepo) Cancel(context.Context, string) error { return nil }

func (r *claimGateRepo) MarkDone(_ context.Context, id string, resp *anypb.Any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done[id] = resp
	return nil
}

func (r *claimGateRepo) MarkError(_ context.Context, id string, st *rpcstatus.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errored[id] = st
	return nil
}

// ClaimForExecution — реализация опционального executionClaimer.
func (r *claimGateRepo) ClaimForExecution(_ context.Context, _ string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimCalls++
	if r.claimErr != nil {
		return false, r.claimErr
	}
	return r.live, nil
}

func (r *claimGateRepo) claimCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claimCalls
}

func (r *claimGateRepo) doneCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.done)
}

// Строка уже терминальна (reconciler разрешил orphan) → worker ОБЯЗАН пропустить
// fn: иначе fn создал бы phantom-ресурс при ERROR-операции.
func TestWorker_SkipsFn_WhenAlreadyTerminal(t *testing.T) {
	repo := newClaimGateRepo(false) // ClaimForExecution → live=false (уже done)
	w := fastTerminalWorker(t, operations.NewMemRecorder())

	var fnCalled atomic.Bool
	operations.RunWithWorker(w, context.Background(), repo, "op-phantom", func(context.Context) (*anypb.Any, error) {
		fnCalled.Store(true)
		return mustAny(t, wrapperspb.String("phantom")), nil
	})

	waitFor(t, 3*time.Second, func() bool { return repo.claimCount() >= 1 })
	// Дать worker'у время исполнить fn, если бы он его не пропустил.
	time.Sleep(150 * time.Millisecond)

	assert.False(t, fnCalled.Load(),
		"fn НЕ должна исполняться, если операция уже терминальна (иначе phantom-ресурс)")
	assert.Equal(t, 0, repo.doneCount(),
		"пропущенная fn не должна писать терминал (строку уже разрешил reconciler)")
}

// Строка ещё живая (done=false) → ClaimForExecution подтверждает liveness,
// worker исполняет fn и durable-пишет терминал (happy-path не сломан).
func TestWorker_RunsFn_WhenLive(t *testing.T) {
	repo := newClaimGateRepo(true) // ClaimForExecution → live=true
	w := fastTerminalWorker(t, operations.NewMemRecorder())

	var fnCalled atomic.Bool
	resp := mustAny(t, wrapperspb.String("ok"))
	operations.RunWithWorker(w, context.Background(), repo, "op-live", func(context.Context) (*anypb.Any, error) {
		fnCalled.Store(true)
		return resp, nil
	})

	waitFor(t, 3*time.Second, func() bool { return repo.doneCount() >= 1 })
	assert.True(t, fnCalled.Load(), "живая операция должна исполнять fn")
	assert.GreaterOrEqual(t, repo.claimCount(), 1, "claim должен быть вызван перед fn")
}

// Transient-сбой ClaimForExecution НЕ должен ронять операцию: worker деградирует
// к pre-r9b поведению (исполняет fn), terminalWrite-CAS всё ещё защищает от
// двойного терминала.
func TestWorker_ProceedsOnClaimError(t *testing.T) {
	repo := newClaimGateRepo(true)
	repo.claimErr = errors.New("connection refused: claim transient")
	w := fastTerminalWorker(t, operations.NewMemRecorder())

	var fnCalled atomic.Bool
	resp := mustAny(t, wrapperspb.String("degraded"))
	operations.RunWithWorker(w, context.Background(), repo, "op-claim-err", func(context.Context) (*anypb.Any, error) {
		fnCalled.Store(true)
		return resp, nil
	})

	waitFor(t, 3*time.Second, func() bool { return repo.doneCount() >= 1 })
	assert.True(t, fnCalled.Load(),
		"transient claim-сбой не должен пропускать fn (деградация к pre-r9b поведению)")
}
