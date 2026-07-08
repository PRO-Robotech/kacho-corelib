// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

// Регрессия: Worker.Stop() не должен «терять» wg-счётчик задач, застрявших в
// admission backlog. runOn делает wg.Add(1) синхронно с enqueue; если dispatcher
// останавливается, оставив backlog неразобранным, парный wg.Done обязан всё равно
// произойти (drainBacklog) — иначе последующий/конкурентный Wait() навсегда
// заблокируется на wg != 0 (spurious timeout, утечка wg.Wait-горутины). Задачи не
// исполняются — они durable в БД и добираются reconciler'ом.

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

func TestWorker_Stop_DrainsBacklog_WaitCompletes(t *testing.T) {
	// maxInflight=1: единственный слот занимает блокирующий job, остальные копятся
	// в backlog за ним — ровно та ситуация, где Stop оставляет backlog неразобранным.
	w := operations.NewWorker(operations.WithMaxInflight(1))
	w.Start()

	repo := newClaimGateRepo(true)

	release := make(chan struct{})
	var running atomic.Bool
	operations.RunWithWorker(w, context.Background(), repo, "op-blocker",
		func(context.Context) (*anypb.Any, error) {
			running.Store(true)
			<-release
			return mustAny(t, wrapperspb.String("done")), nil
		})
	// Дожидаемся, пока блокер реально стартовал и занял единственный слот.
	waitFor(t, 3*time.Second, func() bool { return running.Load() })

	// Ещё N задач — слот занят блокером, они застревают в backlog.
	const backlogN = 8
	for i := 0; i < backlogN; i++ {
		operations.RunWithWorker(w, context.Background(), repo, fmt.Sprintf("op-backlog-%d", i),
			func(context.Context) (*anypb.Any, error) {
				return mustAny(t, wrapperspb.String("x")), nil
			})
	}

	// Stop: dispatcher останавливается, backlog abandon'ится (durable → reconciler),
	// но wg-счётчик каждой задачи ОБЯЗАН быть закрыт (drainBacklog + in-hand Done).
	w.Stop()
	// Разблокируем исполняемый job — его launch-defer сделает свой wg.Done.
	close(release)

	// Без drainBacklog оставшиеся backlog-задачи держали бы wg > 0 → Wait таймаутит.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := w.Wait(ctx); err != nil {
		t.Fatalf("Wait after Stop timed out — abandoned backlog jobs leaked wg.Add without wg.Done: %v", err)
	}
}

// Enqueue после Stop не должен создавать «висящую» wg-задачу: dispatcher мёртв,
// задача никогда не исполнится, поэтому runOn обязан НЕ делать wg.Add (durable →
// reconciler). Иначе Wait() заблокировался бы навсегда.
func TestWorker_RunAfterStop_DoesNotLeakWait(t *testing.T) {
	w := operations.NewWorker(operations.WithMaxInflight(2))
	w.Start()
	w.Stop()

	repo := newClaimGateRepo(true)
	for i := 0; i < 5; i++ {
		operations.RunWithWorker(w, context.Background(), repo, fmt.Sprintf("op-post-stop-%d", i),
			func(context.Context) (*anypb.Any, error) {
				return mustAny(t, wrapperspb.String("x")), nil
			})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := w.Wait(ctx); err != nil {
		t.Fatalf("Wait after post-Stop submits timed out — enqueue-after-stop leaked wg: %v", err)
	}
}
