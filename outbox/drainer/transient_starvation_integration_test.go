// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package drainer_test

// Integration test for the transient head-of-line starvation hazard in the
// outbox/drainer claim ordering.
//
// Background / root cause
//
//	markTransientFailure caps attempt_count at MaxAttempts-1 (LEAST), so a
//	persistently-transient-failing row stays claimable FOREVER (attempt_count <
//	MaxAttempts is always true). Combined with a claim's plain `ORDER BY id`, a
//	set of low-id rows that keep failing transiently permanently SHADOW any
//	higher-id row: the small per-claim batch limit (1..4) plus the retry-backoff
//	means the claim re-selects the same stuck low-id rows on every wake-up and
//	never advances to the fresh higher-id intent. The new intent is NEVER
//	delivered → at-least-once is violated under a sustained outage.
//
//	Fix: claim ORDER BY (attempt_count, id) — a fresh row (attempt_count=0)
//	always sorts before rows pinned at the transient cap, so a new intent is
//	claimed promptly and is never starved by older transient-failing rows.
//	FIFO for the happy path is preserved (equal attempt_count → id order).
//
// Expected behaviour: the fresh row is delivered while the stuck rows keep
// retrying — its sent_at must not stay NULL forever.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Test_1_4_24_TransientBacklog_DoesNotStarveFreshIntent — a backlog of
// persistently-transient-failing rows must NOT block delivery of a freshly
// enqueued intent. The fresh row's applier succeeds; it must reach sent_at
// even while the stuck rows keep failing.
func Test_1_4_24_TransientBacklog_DoesNotStarveFreshIntent(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()

	// Key by the payload's "object" so per-object behaviour can be injected.
	fa.setKeyFn(func(_ string, payload []byte) string {
		var m map[string]string
		if err := json.Unmarshal(payload, &m); err != nil {
			return ""
		}
		return m["object"]
	})

	cfg := testCfg()
	cfg.MaxAttempts = 5
	cfg.BackoffMin = 20 * time.Millisecond
	cfg.BackoffMax = 40 * time.Millisecond
	cfg.PollFallback = 200 * time.Millisecond

	const stuckObjects = 7 // mirrors the migration-seeded fga_outbox backlog
	const freshObject = "vpc_network:fresh000000000000001"

	// Pre-seed N persistently-transient-failing rows (lower ids).
	for i := 0; i < stuckObjects; i++ {
		obj := fmt.Sprintf("stuck:obj%02d", i)
		// Always transient (Unavailable-shaped raw error) → never poisons,
		// stays pinned at the transient cap, stays claimable forever.
		fa.setErrorSeq(obj) // empty seq → falls through to per-... handled below
		insertOutboxRow(t, ctx, pool, "fga.tuple.write",
			fmt.Sprintf(`{"user":"u","relation":"r","object":%q}`, obj))
	}
	// Make every stuck object fail transiently forever: a long error sequence
	// (longer than the test runs) of raw transient errors.
	for i := 0; i < stuckObjects; i++ {
		obj := fmt.Sprintf("stuck:obj%02d", i)
		seq := make([]error, 0, 4096)
		for j := 0; j < 4096; j++ {
			seq = append(seq, fmt.Errorf("openfga write: status 503: backend unavailable"))
		}
		fa.setErrorSeq(obj, seq...)
	}

	// The fresh intent (highest id) succeeds on first apply (default nil for
	// any key not in errorSeq).
	freshID := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		fmt.Sprintf(`{"user":"u","relation":"r","object":%q}`, freshObject))

	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa)
	defer func() { dCancel(); <-done }()

	// The fresh row MUST be delivered despite the stuck backlog. Without
	// attempt_count-first ordering this never happens (a plain ORDER BY id keeps
	// re-claiming the stuck low-id rows).
	row := waitForRowSent(t, ctx, pool, freshID, 8*time.Second)
	assert.NotNil(t, row.sentAt, "fresh intent must be delivered, not starved by transient backlog")
	assert.Nil(t, row.lastError, "fresh intent delivered cleanly (no error)")
}
