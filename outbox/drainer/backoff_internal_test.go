// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package drainer

// White-box unit test for post-batch retry-backoff sizing.
//
// Regression guard: the retry backoff for a mixed batch must be sized off the
// MOST-retried row that failed transiently, never off rows[0]. claimRows orders
// pending rows `ORDER BY attempt_count, id`, so rows[0] is the LEAST-attempted
// row of the batch — frequently a fresh CREATED intent that applies successfully
// (attempt_count~1). Pacing the whole loop off it would re-claim a persistently
// stuck tail row at BackoffMin cadence, hammering the target far faster than the
// intended exponential backoff precisely during a partial outage.

import (
	"testing"
	"time"
)

func TestBackoffAttemptCount_SizesFromMaxTransientRow(t *testing.T) {
	tests := []struct {
		name  string
		rows  []claimedRow
		retry []bool
		want  int
	}{
		{
			name: "mixed batch: fresh success first, long-transient tail",
			rows: []claimedRow{
				{id: 1, attemptCount: 1}, // fresh CREATED intent, applied OK
				{id: 2, attemptCount: 2}, // applied OK
				{id: 3, attemptCount: 9}, // long-transient, stuck near MaxAttempts
			},
			retry: []bool{false, false, true},
			want:  9, // NOT rows[0].attemptCount == 1
		},
		{
			name: "multiple transient rows → max attempt among them",
			rows: []claimedRow{
				{id: 1, attemptCount: 3},
				{id: 2, attemptCount: 7},
			},
			retry: []bool{true, true},
			want:  7,
		},
		{
			name: "no transient rows → 0",
			rows: []claimedRow{
				{id: 1, attemptCount: 4},
			},
			retry: []bool{false},
			want:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := backoffAttemptCount(tc.rows, tc.retry)
			if got != tc.want {
				t.Fatalf("backoffAttemptCount = %d, want %d", got, tc.want)
			}
		})
	}
}

// Ties the helper to the observable it exists to fix: for the mixed batch the
// resulting sleep must be the transient row's exponentially-grown interval
// (clamped to BackoffMax), not BackoffMin.
func TestBackoffAttemptCount_FeedsExpBackoff_NotBackoffMin(t *testing.T) {
	const (
		base = 1 * time.Second
		max  = 30 * time.Second
	)
	rows := []claimedRow{
		{id: 1, attemptCount: 1}, // rows[0] — fresh success
		{id: 2, attemptCount: 9}, // transient tail
	}
	retry := []bool{false, true}

	sleep := expBackoff(backoffAttemptCount(rows, retry), base, max)
	if sleep == base {
		t.Fatalf("sleep sized at BackoffMin (%v) — a stuck transient row would be re-claimed "+
			"at minimum cadence during a partial outage", base)
	}
	if sleep != max {
		t.Fatalf("sleep = %v, want %v (expBackoff of attempt=9 clamps to BackoffMax)", sleep, max)
	}
}
