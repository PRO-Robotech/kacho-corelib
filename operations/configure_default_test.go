// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

// Тесты явной проводки default-registry LRO-worker'а composition root'ом:
//   - Start() поднимает dispatcher-loop и делает Ready()=true ДО первого Run
//     (снимает boot-deadlock: readiness-probe зелёный до трафика);
//   - Configure/ConfigureDefault применяют Recorder/Logger/MaxInflight ДО старта
//     и отвергаются (ErrWorkerStarted) после — менять поля под живым dispatcher'ом
//     небезопасно;
//   - подключённый через Configure Recorder реально получает terminal-write
//     метрики от ЖИВОГО worker-пути (а не только от reconciler'а).

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// shortTW — короткий retry-budget терминальной записи, чтобы тест не висел на
// бесконечном сбое MarkDone.
func shortTW() operations.TerminalWriteConfig {
	return operations.TerminalWriteConfig{
		InitialInterval: 2 * time.Millisecond,
		MaxInterval:     8 * time.Millisecond,
		MaxElapsed:      40 * time.Millisecond,
	}
}

// TestWorker_StartReadyBeforeFirstRun — Start() поднимает dispatcher-loop и делает
// Ready()=true БЕЗ единого Run. Это снимает boot-deadlock: под становится Ready и
// начинает получать мутации, иначе вечный NotReady (нет трафика → нет Run → worker
// не стартует → NotReady навсегда).
func TestWorker_StartReadyBeforeFirstRun(t *testing.T) {
	w := operations.NewWorker()
	require.False(t, w.Ready(), "до Start dispatcher не запущен")
	w.Start()
	t.Cleanup(w.Stop)
	require.True(t, w.Ready(), "Start() поднимает dispatcher → Ready без единого Run")
}

// TestWorker_ConfigureBeforeStart_AppliesAndRejectsAfterStart — Configure до Start
// применяет опции (Recorder/MaxInflight); после Start — ErrWorkerStarted.
func TestWorker_ConfigureBeforeStart_AppliesAndRejectsAfterStart(t *testing.T) {
	w := operations.NewWorker()
	rec := operations.NewMemRecorder()
	require.NoError(t, w.Configure(operations.WithRecorder(rec), operations.WithMaxInflight(3)),
		"Configure до Start применяет опции")

	w.Start()
	t.Cleanup(w.Stop)
	require.True(t, w.Ready())

	require.ErrorIs(t, w.Configure(operations.WithMaxInflight(5)), operations.ErrWorkerStarted,
		"Configure после Start → ErrWorkerStarted (опции применимы только до старта)")
}

// TestWorker_LiveWorkerEmitsTerminalWriteMetric — Recorder, подключённый через
// Configure ДО старта, реально получает terminal-write метрики от ЖИВОГО
// worker-пути (RunWithWorker), а не только от reconciler'а. Default-registry
// создаётся с NopRecorder — без ConfigureDefault эти серии мертвы; этот контракт
// фиксит проводка composition root.
func TestWorker_LiveWorkerEmitsTerminalWriteMetric(t *testing.T) {
	rec := operations.NewMemRecorder()
	w := operations.NewWorker(operations.WithTerminalWriteConfig(shortTW()))
	require.NoError(t, w.Configure(operations.WithRecorder(rec)))
	w.Start()
	t.Cleanup(w.Stop)

	repo := newFlakyRepo()
	repo.alwaysFailDone = true // MarkDone всегда падает transient → retries → failure

	operations.RunWithWorker(w, context.Background(), repo, "op-live-metric",
		func(context.Context) (*anypb.Any, error) { return mustAny(t, wrapperspb.String("ok")), nil })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, w.Wait(ctx), "worker должен сдренироваться (budget терминальной записи исчерпан)")

	require.GreaterOrEqual(t, rec.TerminalWriteFailures("MarkDone"), float64(1),
		"живой worker эмитит terminal-write-failure в подключённый Recorder")
	require.GreaterOrEqual(t, rec.TerminalWriteRetries("MarkDone"), float64(1),
		"перед сдачей было >=1 retry")
}

// TestStart_ReadyBeforeFirstRun — package-level Start() поднимает default-registry
// (идемпотентно) и делает operations.Ready()=true. Composition root зовёт его на
// boot, чтобы readiness lro-worker был зелёным до приёма трафика.
func TestStart_ReadyBeforeFirstRun(t *testing.T) {
	operations.Start()
	require.True(t, operations.Ready(), "Start() → default-registry Ready без единого Run")
}

// TestConfigureDefault_AfterStart_Error — ConfigureDefault после старта
// default-registry возвращает ErrWorkerStarted: опции (Recorder/Logger) применимы
// только ДО Start.
func TestConfigureDefault_AfterStart_Error(t *testing.T) {
	operations.Start() // идемпотентно гарантирует запущенный default-registry
	require.ErrorIs(t, operations.ConfigureDefault(operations.WithMaxInflight(8)),
		operations.ErrWorkerStarted)
}
