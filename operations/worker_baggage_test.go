// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// memRepo — in-memory implementation of operations.Repo для unit-тестов
// без postgres. Только методы, которые worker.runOn вызывает (MarkDone, MarkError).
type memRepo struct {
	mu      sync.Mutex
	done    map[string]*anypb.Any
	errored map[string]*rpcstatus.Status
}

func newMemRepo() *memRepo {
	return &memRepo{
		done:    map[string]*anypb.Any{},
		errored: map[string]*rpcstatus.Status{},
	}
}

func (m *memRepo) Create(context.Context, operations.Operation) error { return nil }
func (m *memRepo) CreateWithPrincipal(context.Context, operations.Operation, operations.Principal) error {
	return nil
}
func (m *memRepo) Get(context.Context, string) (*operations.Operation, error) {
	return nil, operations.ErrNotFound
}

func (m *memRepo) List(context.Context, operations.ListFilter) ([]operations.Operation, string, error) {
	return nil, "", nil
}
func (m *memRepo) Cancel(context.Context, string) error { return nil }

func (m *memRepo) MarkDone(_ context.Context, opID string, response *anypb.Any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.done[opID] = response
	return nil
}

func (m *memRepo) MarkError(_ context.Context, opID string, st *rpcstatus.Status) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errored[opID] = st
	return nil
}

// waitDone — короткий блок до появления записи в memRepo (timeout 1s).
func (m *memRepo) waitDone(t *testing.T, opID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		_, okDone := m.done[opID]
		_, okErr := m.errored[opID]
		m.mu.Unlock()
		if okDone || okErr {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("op %s did not finish within 1s", opID)
}

type ctxKey string

// TestRun_PropagatesOTelSpanContext — OTel SpanContext caller-ctx
// доступен внутри worker-fn через trace.SpanContextFromContext.
func TestRun_PropagatesOTelSpanContext(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("bbbbbbbbbbbbbbbb")
	require.NoError(t, err)
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	callerCtx := trace.ContextWithSpanContext(context.Background(), sc)

	repo := newMemRepo()
	w := operations.NewWorker()

	var gotTraceID trace.TraceID
	var gotSpanID trace.SpanID
	resp := mustAny(t, wrapperspb.String("ok"))

	operations.RunWithWorker(w, callerCtx, repo, "op-1",
		func(ctx context.Context) (*anypb.Any, error) {
			sc := trace.SpanContextFromContext(ctx)
			gotTraceID = sc.TraceID()
			gotSpanID = sc.SpanID()
			return resp, nil
		})

	require.NoError(t, w.Wait(context.Background()))
	repo.waitDone(t, "op-1")

	assert.Equal(t, traceID, gotTraceID, "TraceID должен propagate'иться в worker-fn")
	assert.Equal(t, spanID, gotSpanID, "SpanID должен propagate'иться в worker-fn")
}

// TestRun_PropagatesCustomValue — request-id (или любой custom WithValue-ключ)
// доступен внутри worker-fn.
func TestRun_PropagatesCustomValue(t *testing.T) {
	const reqIDKey ctxKey = "x-request-id"
	callerCtx := context.WithValue(context.Background(), reqIDKey, "req-42")

	repo := newMemRepo()
	w := operations.NewWorker()
	var gotReqID any

	operations.RunWithWorker(w, callerCtx, repo, "op-2",
		func(ctx context.Context) (*anypb.Any, error) {
			gotReqID = ctx.Value(reqIDKey)
			return mustAny(t, wrapperspb.String("ok")), nil
		})

	require.NoError(t, w.Wait(context.Background()))
	repo.waitDone(t, "op-2")

	assert.Equal(t, "req-42", gotReqID,
		"request-id должен propagate'иться из caller-ctx в worker-fn")
}

// TestRun_CallerDeadlineNotInherited — caller-ctx с deadline 30ms,
// worker продолжает работу 100ms и успешно завершает (deadline не наследуется).
func TestRun_CallerDeadlineNotInherited(t *testing.T) {
	callerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	repo := newMemRepo()
	w := operations.NewWorker()

	operations.RunWithWorker(w, callerCtx, repo, "op-3",
		func(ctx context.Context) (*anypb.Any, error) {
			// Worker делает «длинную» работу — дольше, чем caller-deadline.
			select {
			case <-ctx.Done():
				return nil, errors.New("worker-ctx cancelled while caller-deadline expired — should not happen")
			case <-time.After(100 * time.Millisecond):
			}
			return mustAny(t, wrapperspb.String("ok")), nil
		})

	require.NoError(t, w.Wait(context.Background()))
	repo.waitDone(t, "op-3")

	repo.mu.Lock()
	_, ok := repo.done["op-3"]
	_, errored := repo.errored["op-3"]
	repo.mu.Unlock()

	assert.True(t, ok, "worker должен успешно завершиться (MarkDone)")
	assert.False(t, errored, "worker не должен пометить error при caller-deadline expiry")
}

// TestRun_CallerCancelDoesNotInterruptWorker — cancel() на caller-ctx
// не должен прерывать worker.
func TestRun_CallerCancelDoesNotInterruptWorker(t *testing.T) {
	callerCtx, cancel := context.WithCancel(context.Background())

	repo := newMemRepo()
	w := operations.NewWorker()

	started := make(chan struct{})

	operations.RunWithWorker(w, callerCtx, repo, "op-4",
		func(ctx context.Context) (*anypb.Any, error) {
			close(started)
			// Имитируем работу 80ms.
			select {
			case <-ctx.Done():
				return nil, errors.New("worker-ctx cancelled — caller cancel leaked")
			case <-time.After(80 * time.Millisecond):
			}
			return mustAny(t, wrapperspb.String("ok")), nil
		})

	// Дождемся реального старта worker'а и тут же cancel'нем caller.
	<-started
	cancel()

	require.NoError(t, w.Wait(context.Background()))
	repo.waitDone(t, "op-4")

	repo.mu.Lock()
	_, ok := repo.done["op-4"]
	_, errored := repo.errored["op-4"]
	repo.mu.Unlock()

	assert.True(t, ok, "worker должен успешно завершиться, несмотря на caller cancel")
	assert.False(t, errored)
}

func mustAny(t *testing.T, msg proto.Message) *anypb.Any {
	t.Helper()
	a, err := anypb.New(msg)
	require.NoError(t, err)
	return a
}
