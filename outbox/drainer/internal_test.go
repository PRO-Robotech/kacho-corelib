// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package drainer

// White-box unit test for the inter-batch jitter helper.
//
// Locks the documented HA-fairness yield window to 0-10ms (drainBatch step-3
// godoc). Regression guard against doc/comment/code drift: a maintainer tuning
// two-replica claim-race fairness must be able to trust that the code produces
// the window the comments advertise.

import (
	"testing"
	"time"
)

func TestInterBatchJitter_WithinDocumentedWindow(t *testing.T) {
	const (
		samples = 2000
		upper   = 10 * time.Millisecond
	)

	var sawAboveFour bool
	for i := 0; i < samples; i++ {
		j := interBatchJitter()
		if j < 0 || j > upper {
			t.Fatalf("jitter %v outside documented [0,10ms] window", j)
		}
		if j > 4*time.Millisecond {
			sawAboveFour = true
		}
	}

	// The previous code capped the window at 0-4ms (rand.IntN(5)) while the
	// godoc/comment advertised 0-10ms. Prove the real window reaches beyond 4ms
	// so the triple-inconsistency cannot silently regress.
	if !sawAboveFour {
		t.Fatalf("jitter never exceeded 4ms over %d samples — window is narrower than the documented 0-10ms", samples)
	}
}
