// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz

// White-box unit test for reconnect-backoff reset.
//
// Regression guard: the LISTEN reconnect backoff must reset to
// initialReconnectBackoff after any session that actually established the
// subscription. Without the reset, a handful of transient drops over the process
// lifetime ratchet the backoff to the maxReconnectBackoff cap permanently — from
// then on even a brief DB blip re-establishes the subject-revocation LISTEN up to
// maxReconnectBackoff late, serving that whole window with missed revoke NOTIFYs.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestReconnectLoop_BackoffResetsAfterConnectedSession(t *testing.T) {
	li := &ListenInvalidator{Cache: NewCache(time.Second)} // non-nil so invalidateAll is safe

	dropErr := errors.New("conn drop")
	// Three never-connected drops ratchet the backoff up, then a connected
	// session that served and dropped must reset it, then one more drop to
	// observe the post-reset wait.
	connectedScript := []bool{false, false, false, true, false}
	var i int
	session := func(context.Context, *slog.Logger) (bool, error) {
		c := connectedScript[i]
		i++
		return c, dropErr
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits []time.Duration
	sleep := func(_ context.Context, d time.Duration) bool {
		waits = append(waits, d)
		if i >= len(connectedScript) {
			cancel()
			return false
		}
		return true
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := li.reconnectLoop(ctx, logger, session, sleep); err != nil {
		t.Fatalf("reconnectLoop returned err: %v", err)
	}

	// Exponential ratchet on the never-connected drops, then a reset on the
	// connected session: [1s, 2s, 4s, 1s(reset), ...].
	want := []time.Duration{
		initialReconnectBackoff,     // drop #1
		2 * initialReconnectBackoff, // drop #2
		4 * initialReconnectBackoff, // drop #3
		initialReconnectBackoff,     // drop #4 — reset after connected session
	}
	if len(waits) < len(want) {
		t.Fatalf("recorded %d waits, want >= %d: %v", len(waits), len(want), waits)
	}
	for k, w := range want {
		if waits[k] != w {
			t.Fatalf("waits[%d] = %v, want %v (full: %v)", k, waits[k], w, waits)
		}
	}
	// The load-bearing assertion: the wait after the connected session is the
	// reset value, not the ratcheted 8s the pre-fix loop produced.
	if waits[3] != initialReconnectBackoff {
		t.Fatalf("backoff not reset after connected session: waits[3]=%v, want %v",
			waits[3], initialReconnectBackoff)
	}
}

// TestReconnectLoop_ContextCancelDuringSleep — the loop must exit cleanly when
// ctx ends during the reconnect wait (sleep returns false).
func TestReconnectLoop_ContextCancelDuringSleep(t *testing.T) {
	li := &ListenInvalidator{Cache: NewCache(time.Second)}
	session := func(context.Context, *slog.Logger) (bool, error) {
		return false, errors.New("drop")
	}
	sleep := func(context.Context, time.Duration) bool { return false } // ctx ended
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := li.reconnectLoop(context.Background(), logger, session, sleep); err != nil {
		t.Fatalf("reconnectLoop returned err: %v", err)
	}
}
