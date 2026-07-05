// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations

import (
	"context"
	"errors"
	"testing"
	"time"
)

// captureResolver records the ctx it was invoked with and can block until ctx
// is cancelled (to exercise the per-item timeout bound).
type captureResolver struct {
	gotCtx context.Context
	block  bool
}

func (r *captureResolver) Resolve(ctx context.Context, _ Operation) (ResolverResult, error) {
	r.gotCtx = ctx
	if r.block {
		<-ctx.Done()
		return ResolverResult{}, ctx.Err()
	}
	return ResolverResult{Outcome: OutcomeSkip}, nil
}

// TestResolveOne_AppliesTimeout — при resolveTimeout>0 доменный Resolve обязан
// получить ctx с deadline (жёсткий потолок idle-in-transaction).
func TestResolveOne_AppliesTimeout(t *testing.T) {
	r := &captureResolver{}
	rc := &Reconciler{resolver: r, resolveTimeout: 50 * time.Millisecond}
	if _, err := rc.resolveOne(context.Background(), Operation{ID: "op-1"}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, ok := r.gotCtx.Deadline(); !ok {
		t.Fatalf("expected Resolve ctx to carry a deadline when resolveTimeout>0")
	}
}

// TestResolveOne_TimeoutCancelsBlockingResolve — зависший Resolve (peer без
// deadline) отменяется потолком, а не висит вечно, удерживая claim-tx.
func TestResolveOne_TimeoutCancelsBlockingResolve(t *testing.T) {
	r := &captureResolver{block: true}
	rc := &Reconciler{resolver: r, resolveTimeout: 30 * time.Millisecond}
	start := time.Now()
	_, err := rc.resolveOne(context.Background(), Operation{ID: "op-2"})
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("resolveOne did not honour timeout; elapsed=%s", elapsed)
	}
}

// TestResolveOne_NoTimeoutDisablesBound — resolveTimeout≤0 сохраняет прежнее
// поведение: ctx без deadline пробрасывается как есть.
func TestResolveOne_NoTimeoutDisablesBound(t *testing.T) {
	r := &captureResolver{}
	rc := &Reconciler{resolver: r, resolveTimeout: 0}
	if _, err := rc.resolveOne(context.Background(), Operation{ID: "op-3"}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if _, ok := r.gotCtx.Deadline(); ok {
		t.Fatalf("expected no deadline when resolveTimeout<=0 (behaviour preserved)")
	}
}
