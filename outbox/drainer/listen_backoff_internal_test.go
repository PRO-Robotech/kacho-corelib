// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package drainer

// White-box unit test for LISTEN reconnect-backoff reset.
//
// Regression guard: the reconnect backoff must reset to initialListenBackoff
// after any session that actually established the subscription. Without the
// reset, a handful of transient drops over the process lifetime ratchet the
// backoff to the maxListenBackoff cap permanently, so a subsequent single-conn
// hiccup re-establishes LISTEN up to maxListenBackoff late (delivery-latency /
// pool-churn), even though at-least-once is still guaranteed by PollFallback.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReconnectLoop_BackoffResetsAfterConnectedSession(t *testing.T) {
	// Offline pool: MinConns defaults to 0 so New never dials, and Reset() on an
	// idle-empty pool is a safe no-op — lets the error path exercise pool.Reset()
	// without a live Postgres.
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db?sslmode=disable")
	if err != nil {
		t.Fatalf("new offline pool: %v", err)
	}
	defer pool.Close()

	d := &Drainer[struct{}]{
		pool:   pool,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	dropErr := errors.New("conn drop")
	// Three never-connected drops ratchet the backoff up, then a connected
	// session that held the subscription and dropped must reset it, then one more
	// drop to observe the post-reset wait.
	connectedScript := []bool{false, false, false, true, false}
	var i int
	session := func(context.Context, chan<- struct{}) (bool, error) {
		c := connectedScript[i]
		i++
		return c, dropErr
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits []time.Duration
	sleep := func(_ context.Context, w time.Duration) bool {
		waits = append(waits, w)
		if i >= len(connectedScript) {
			cancel()
			return false
		}
		return true
	}

	wakeup := make(chan struct{}, 1)
	d.reconnectLoop(ctx, session, wakeup, sleep)

	want := []time.Duration{
		initialListenBackoff,     // drop #1
		2 * initialListenBackoff, // drop #2
		4 * initialListenBackoff, // drop #3
		initialListenBackoff,     // drop #4 — reset after connected session
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
	if waits[3] != initialListenBackoff {
		t.Fatalf("backoff not reset after connected session: waits[3]=%v, want %v",
			waits[3], initialListenBackoff)
	}
}
