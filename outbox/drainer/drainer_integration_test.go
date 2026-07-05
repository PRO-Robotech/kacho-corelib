// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package drainer_test

// Integration tests for kacho-corelib/outbox/drainer.
//
// Each test maps 1:1 to a Given-When-Then delivery scenario for the drainer.
//
// Test design constraints:
//   - testcontainers-go Postgres 16, one container per test (no shared state,
//     parallel-safe).
//   - Inline schema copy of kacho-iam migration 0002_fga_outbox.sql (see
//     drainer_testhelpers_test.go). kacho-corelib doesn't own iam migrations.
//   - Fake-Applier (no real OpenFGA).
//   - Each test wraps its body in context.WithTimeout to bound any hang.
//
// Public API exercised from `package drainer`:
//   - drainer.Config{Table, Channel, BatchSize, PollFallback, MaxAttempts,
//                    BackoffMin, BackoffMax}
//   - drainer.Decoder[T] = func([]byte) (T, error)
//   - drainer.Applier[T] = func(ctx, eventType string, payload T) error
//   - drainer.ErrAlreadyApplied   // idempotent success sentinel
//   - drainer.ErrPermanent        // skip-and-poison sentinel
//   - drainer.Drainer[T]
//   - drainer.New[T](pool, cfg, decoder, applier, logger) (*Drainer[T], error)
//   - (*drainer.Drainer[T]).Run(ctx) error

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/outbox/drainer"
)

// testCfg returns drainer.Config tuned for fast test loops.
func testCfg() drainer.Config {
	return drainer.Config{
		Table:        "kacho_iam.fga_outbox",
		Channel:      "kacho_iam_fga_outbox",
		BatchSize:    32,
		PollFallback: 1 * time.Second, // tight for test responsiveness
		MaxAttempts:  10,
		BackoffMin:   100 * time.Millisecond,
		BackoffMax:   500 * time.Millisecond,
	}
}

// waitForListenerReady blocks until a backend is registered LISTENing on channel
// — its last statement in pg_stat_activity is `LISTEN <channel>` (the drainer's
// dedicated hijacked conn runs exactly that and then parks in
// WaitForNotification). This replaces a fixed pre-insert time.Sleep guess with a
// positive readiness signal, so the NOTIFY-path assertions no longer race the
// LISTEN registration under CI contention (a missed NOTIFY would otherwise fall
// to PollFallback and blow the tight NOTIFY deadline).
func waitForListenerReady(t *testing.T, ctx context.Context, pool *pgxpool.Pool, channel string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE query = $1`,
			"LISTEN "+channel).Scan(&n); err != nil {
			require.NoError(t, err)
		}
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("drainer LISTEN on %q did not register within 5s", channel)
}

// testLogger returns a discard slog.Logger so test output stays clean.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// rawPayload is the test-side decoder target — we keep the JSONB bytes verbatim
// so per-test assertions can inspect any payload shape. The drainer must work
// with any decoder T; raw []byte is the simplest universal T for tests.
type rawPayload []byte

// rawDecoder satisfies drainer.Decoder[rawPayload]: passthrough.
func rawDecoder(payload []byte) (rawPayload, error) {
	out := make(rawPayload, len(payload))
	copy(out, payload)
	return out, nil
}

// applierFromFake adapts our fakeApplier.Apply into drainer.Applier[rawPayload].
func applierFromFake(fa *fakeApplier) drainer.Applier[rawPayload] {
	return func(ctx context.Context, eventType string, payload rawPayload) error {
		return fa.Apply(ctx, eventType, []byte(payload))
	}
}

// startDrainer wires + launches one drainer in a goroutine.
// Returns (cancel, done-chan, err-chan); test cancels and waits on done.
func startDrainer(
	t *testing.T,
	ctx context.Context,
	pool drainerPool,
	cfg drainer.Config,
	fa *fakeApplier,
	opts ...drainer.Option[rawPayload],
) (context.CancelFunc, <-chan struct{}, <-chan error) {
	t.Helper()
	d, err := drainer.New[rawPayload](pool, cfg, rawDecoder, applierFromFake(fa), testLogger(), opts...)
	require.NoError(t, err)

	dCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		errCh <- d.Run(dCtx)
	}()
	return cancel, done, errCh
}

// drainerPool is the pool the drainer consumes — a concrete *pgxpool.Pool
// (LISTEN-conn is Hijack'ed from it).
type drainerPool = *pgxpool.Pool

// ── Functional happy paths ──────────────────────────────────────────────────

// TestW1_1_01_SingleInsert_AppliedWithin500ms — a single inserted row is applied
// within 500ms via NOTIFY.
func TestW1_1_01_SingleInsert_AppliedWithin500ms(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()

	dCancel, done, _ := startDrainer(t, ctx, pool, testCfg(), fa)
	defer func() { dCancel(); <-done }()

	// Wait for the drainer LISTEN to register before INSERT (positive readiness
	// signal instead of a fixed wall-clock guess).
	waitForListenerReady(t, ctx, pool, testCfg().Channel)

	id := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"user":"user:usr01","relation":"system_admin","object":"cluster:default"}`)

	require.True(t, fa.waitForCalls(1, 500*time.Millisecond),
		"applier should be invoked within 500ms via NOTIFY (got %d calls)", fa.countCalls())

	row := waitForRowSent(t, ctx, pool, id, 1*time.Second)
	assert.Equal(t, "fga.tuple.write", row.eventType)
	assert.Equal(t, 1, row.attemptCount)
	assert.Nil(t, row.lastError, "last_error must be NULL on success")

	calls := fa.snapshotCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "fga.tuple.write", calls[0].eventType)
	assert.JSONEq(t,
		`{"user":"user:usr01","relation":"system_admin","object":"cluster:default"}`,
		string(calls[0].payload))
}

// TestW1_1_02_CatchupAfterDowntime_ProcessesPending — startup catch-up drains
// rows enqueued while the drainer was down, in FIFO order.
func TestW1_1_02_CatchupAfterDowntime_ProcessesPending(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()

	// Pre-seed 5 rows with deterministic created_at ordering.
	ids := make([]int64, 5)
	for i := 0; i < 5; i++ {
		ids[i] = insertOutboxRow(t, ctx, pool, "fga.tuple.write",
			fmt.Sprintf(`{"user":"user:usr%02d","relation":"viewer","object":"project:p"}`, i))
		// Ensure created_at strictly ordered (timestamptz resolution is microsecond,
		// but back-to-back inserts can collide; small sleep is cheap insurance).
		time.Sleep(2 * time.Millisecond)
	}

	// NOW start the drainer — NOTIFY messages for those 5 rows are gone forever;
	// only the catch-up SELECT can recover them.
	dCancel, done, _ := startDrainer(t, ctx, pool, testCfg(), fa)
	defer func() { dCancel(); <-done }()

	require.True(t, fa.waitForCalls(5, 2*time.Second),
		"catch-up should drain 5 pending rows within 2s (got %d)", fa.countCalls())

	for _, id := range ids {
		row := waitForRowSent(t, ctx, pool, id, 2*time.Second)
		assert.NotNil(t, row.sentAt, "row id=%d not marked sent", id)
	}

	// Verify ordering: applier calls came in created_at ASC order (FIFO).
	calls := fa.snapshotCalls()
	require.GreaterOrEqual(t, len(calls), 5)
	for i := 1; i < 5; i++ {
		assert.True(t, !calls[i].at.Before(calls[i-1].at),
			"applier should observe FIFO order (call %d at=%v < call %d at=%v)",
			i, calls[i].at, i-1, calls[i-1].at)
	}
}

// TestW1_1_03_DeleteEventApplied — a delete event is applied with the correct
// event type.
func TestW1_1_03_DeleteEventApplied(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()

	dCancel, done, _ := startDrainer(t, ctx, pool, testCfg(), fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, testCfg().Channel)

	id := insertOutboxRow(t, ctx, pool, "fga.tuple.delete",
		`{"user":"user:usr01","relation":"system_admin","object":"cluster:default"}`)

	require.True(t, fa.waitForCalls(1, 1*time.Second))

	row := waitForRowSent(t, ctx, pool, id, 1*time.Second)
	assert.Equal(t, "fga.tuple.delete", row.eventType)

	calls := fa.snapshotCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, "fga.tuple.delete", calls[0].eventType,
		"applier must observe eventType=delete to dispatch correct OpenFGA verb")
}

// TestW1_1_04_IdempotentErrAlreadyApplied_SuccessPath — ErrAlreadyApplied is
// treated as success (sent_at set, no error).
func TestW1_1_04_IdempotentErrAlreadyApplied_SuccessPath(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()
	fa.setDefaultErr(drainer.ErrAlreadyApplied) // models OpenFGA HTTP 409

	dCancel, done, _ := startDrainer(t, ctx, pool, testCfg(), fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, testCfg().Channel)
	id := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"user":"user:usr02","relation":"viewer","object":"project:p"}`)

	require.True(t, fa.waitForCalls(1, 1*time.Second))

	row := waitForRowSent(t, ctx, pool, id, 1*time.Second)
	assert.NotNil(t, row.sentAt, "ErrAlreadyApplied should be treated as success → sent_at set")
	assert.Nil(t, row.lastError, "last_error must be NULL (idempotent success, not a real error)")
}

// ── Negative paths ──────────────────────────────────────────────────────────

// TestW1_1_05_TransientError_ExpBackoffRetry_EventualSuccess — transient errors
// retry with exponential backoff until success.
func TestW1_1_05_TransientError_ExpBackoffRetry_EventualSuccess(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()
	// First 2 attempts → transient (drainer should retry); 3rd → nil (success).
	fa.setErrorSeq("fga.tuple.write",
		errTestTransient,
		errTestTransient,
		nil,
	)

	cfg := testCfg()
	cfg.BackoffMin = 100 * time.Millisecond
	cfg.BackoffMax = 500 * time.Millisecond

	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, testCfg().Channel)
	id := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"user":"user:usr05","relation":"viewer","object":"project:p"}`)

	require.True(t, fa.waitForCalls(3, 2*time.Second),
		"expected 3 attempts (2 transient + 1 success), got %d", fa.countCalls())

	row := waitForRowSent(t, ctx, pool, id, 2*time.Second)
	assert.Equal(t, 3, row.attemptCount, "attempt_count must reflect all attempts")
	assert.Nil(t, row.lastError, "last_error must be reset to NULL on final success")

	// Verify exponential spacing between attempts.
	calls := fa.snapshotCalls()
	require.Len(t, calls, 3)
	gap1 := calls[1].at.Sub(calls[0].at)
	gap2 := calls[2].at.Sub(calls[1].at)
	assert.GreaterOrEqual(t, gap1, 80*time.Millisecond, "first retry gap ≈ BackoffMin (100ms ±)")
	assert.GreaterOrEqual(t, gap2, gap1, "second gap should not be smaller than first (exp backoff)")
}

// TestW1_1_06_PermanentError_PoisonAndContinue — a permanent error poisons its
// row and the drainer keeps serving others.
func TestW1_1_06_PermanentError_PoisonAndContinue(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()

	// Key by user-prefix: "BAD:" → permanent; everything else → success.
	fa.setKeyFn(func(eventType string, payload []byte) string {
		var v struct {
			User string `json:"user"`
		}
		_ = json.Unmarshal(payload, &v)
		if len(v.User) >= 4 && v.User[:4] == "BAD:" {
			return "POISONED"
		}
		return "OK"
	})
	fa.setErrorSeq("POISONED",
		errors.Join(drainer.ErrPermanent, errors.New("bad payload")),
	)
	fa.setDefaultErr(nil) // OK key → nil → success

	dCancel, done, _ := startDrainer(t, ctx, pool, testCfg(), fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, testCfg().Channel)
	idA := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"user":"BAD:usr","relation":"viewer","object":"project:p"}`)
	time.Sleep(100 * time.Millisecond)
	idB := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"user":"user:good","relation":"viewer","object":"project:p"}`)

	// Wait for B to be drained — proves drainer didn't get stuck on A.
	rowB := waitForRowSent(t, ctx, pool, idB, 2*time.Second)
	assert.NotNil(t, rowB.sentAt, "row B must be drained (drainer not stuck on poisoned A)")

	// A is force-poisoned: attempt_count = MaxAttempts (or ≥), sent_at still NULL,
	// last_error contains the message.
	rowA := waitForAttemptCount(t, ctx, pool, idA, testCfg().MaxAttempts, 2*time.Second)
	assert.Nil(t, rowA.sentAt, "poisoned row A must NOT be marked sent")
	assert.GreaterOrEqual(t, rowA.attemptCount, testCfg().MaxAttempts,
		"permanent error must force attempt_count >= MaxAttempts to stop retry-loop")
	require.NotNil(t, rowA.lastError, "last_error must be populated on permanent failure")
	assert.Contains(t, *rowA.lastError, "bad payload")
}

// TestW1_1_07_DecoderFail_PermanentError — a decoder failure poisons the row
// without calling the applier.
func TestW1_1_07_DecoderFail_PermanentError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()
	fa.setDefaultErr(nil) // applier never reached if decoder fails

	// Strict decoder: rejects payloads missing user/relation/object.
	type fgaEv struct {
		User     string `json:"user"`
		Relation string `json:"relation"`
		Object   string `json:"object"`
	}
	strictDecoder := func(payload []byte) (fgaEv, error) {
		var e fgaEv
		if err := json.Unmarshal(payload, &e); err != nil {
			return e, errors.Join(drainer.ErrPermanent, err)
		}
		if e.User == "" || e.Relation == "" || e.Object == "" {
			return e, errors.Join(drainer.ErrPermanent,
				errors.New("missing user/relation/object"))
		}
		return e, nil
	}
	strictApplier := func(ctx context.Context, eventType string, e fgaEv) error {
		return fa.Apply(ctx, eventType, []byte(e.User+"|"+e.Relation+"|"+e.Object))
	}

	d, err := drainer.New[fgaEv](pool, testCfg(), strictDecoder, strictApplier, testLogger())
	require.NoError(t, err)

	dCtx, dCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(dCtx) }()
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, testCfg().Channel)
	id := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"missing_required_field": true}`)

	// Decoder rejects → drainer marks poisoned without calling applier.
	row := waitForAttemptCount(t, ctx, pool, id, testCfg().MaxAttempts, 3*time.Second)
	assert.Nil(t, row.sentAt, "decoder-rejected row must NOT be marked sent")
	assert.GreaterOrEqual(t, row.attemptCount, testCfg().MaxAttempts,
		"decoder-fail = permanent → force MaxAttempts")
	require.NotNil(t, row.lastError)
	assert.Contains(t, *row.lastError, "missing user/relation/object")

	// Drainer must keep running afterwards — sanity-check on a valid row.
	id2 := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"user":"user:ok","relation":"viewer","object":"project:p"}`)
	row2 := waitForRowSent(t, ctx, pool, id2, 2*time.Second)
	assert.NotNil(t, row2.sentAt, "drainer must continue serving after decoder-fail")
}

// TestW1_1_08_ConnectionDrop_ReconnectAndCatchup — after a LISTEN-conn drop the
// drainer reconnects and catches up pending rows.
func TestW1_1_08_ConnectionDrop_ReconnectAndCatchup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, dsn := setupDrainerPG(t)
	fa := newFakeApplier()

	dCancel, done, _ := startDrainer(t, ctx, pool, testCfg(), fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, testCfg().Channel)

	// 1) Process one row to confirm drainer is alive.
	id1 := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"user":"user:warm","relation":"viewer","object":"project:p"}`)
	waitForRowSent(t, ctx, pool, id1, 2*time.Second)

	// 2) Kill the drainer's LISTEN backend by terminating all backends EXCEPT
	//    the one we're about to use for cleanup. pg_terminate_backend on the
	//    LISTEN-conn forces drainer reconnect.
	adminConn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { _ = adminConn.Close(ctx) }()
	_, err = adminConn.Exec(ctx, `
		SELECT pg_terminate_backend(pid)
		  FROM pg_stat_activity
		 WHERE datname = current_database()
		   AND application_name NOT LIKE '%admin%'
		   AND pid <> pg_backend_pid()
	`)
	require.NoError(t, err)

	// 3) Insert another row while drainer is "broken" — NOTIFY for this row
	//    may or may not reach the (new) drainer connection; catch-up SELECT
	//    on reconnect must pick it up regardless.
	time.Sleep(100 * time.Millisecond) // give drainer a beat to notice the drop
	id2 := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"user":"user:postdrop","relation":"viewer","object":"project:p"}`)

	// 4) Within 5s the drainer should reconnect + drain id2.
	row := waitForRowSent(t, ctx, pool, id2, 5*time.Second)
	assert.NotNil(t, row.sentAt, "drainer must reconnect after LISTEN-conn drop and drain pending row")
}

// ── Concurrency ─────────────────────────────────────────────────────────────

// TestW1_1_09_TwoConcurrentInserts_ExactlyOnce — concurrent inserts are each
// applied exactly once.
func TestW1_1_09_TwoConcurrentInserts_ExactlyOnce(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()
	// Key by payload to count per-row applies. Each row has unique payload →
	// counts == 1 per row, total == 20.
	fa.setKeyFn(func(eventType string, payload []byte) string { return string(payload) })

	dCancel, done, _ := startDrainer(t, ctx, pool, testCfg(), fa)
	defer func() { dCancel(); <-done }()

	waitForListenerReady(t, ctx, pool, testCfg().Channel)

	const n = 10
	var wg sync.WaitGroup
	insertedIDs := make([]int64, 0, 2*n)
	var idsMu sync.Mutex
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < n; i++ {
				payload := fmt.Sprintf(
					`{"user":"user:w%dr%d","relation":"viewer","object":"project:p"}`,
					worker, i)
				id := insertOutboxRow(t, ctx, pool, "fga.tuple.write", payload)
				idsMu.Lock()
				insertedIDs = append(insertedIDs, id)
				idsMu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	require.True(t, fa.waitForCalls(2*n, 3*time.Second),
		"expected exactly 20 unique applies, got %d", fa.countCalls())

	// Wait for all rows to be marked sent.
	for _, id := range insertedIDs {
		waitForRowSent(t, ctx, pool, id, 3*time.Second)
	}

	// Verify no payload was applied more than once (exactly-once per row).
	calls := fa.snapshotCalls()
	seen := make(map[string]int, len(calls))
	for _, c := range calls {
		seen[string(c.payload)]++
	}
	for payload, count := range seen {
		assert.Equalf(t, 1, count,
			"payload %s applied %d times (expected exactly once)", payload, count)
	}
	assert.Equal(t, 2*n, len(seen),
		"expected %d unique payloads applied, got %d", 2*n, len(seen))
}

// TestW1_1_10_TwoDrainerInstances_HAExactlyOnce — two drainer instances on one
// DB deliver each row exactly once (HA).
func TestW1_1_10_TwoDrainerInstances_HAExactlyOnce(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool1, dsn := setupDrainerPG(t)
	// Second pool on the SAME database simulates HA replica.
	pool2, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool2.Close)

	// Per-drainer counters wrap the same fake-applier core, so we can verify
	// load-spread.
	fa := newFakeApplier()
	fa.setKeyFn(func(eventType string, payload []byte) string { return string(payload) })

	var calls1, calls2 atomic.Int64
	applier1 := func(ctx context.Context, eventType string, payload rawPayload) error {
		calls1.Add(1)
		return fa.Apply(ctx, eventType, []byte(payload))
	}
	applier2 := func(ctx context.Context, eventType string, payload rawPayload) error {
		calls2.Add(1)
		return fa.Apply(ctx, eventType, []byte(payload))
	}

	d1, err := drainer.New[rawPayload](pool1, testCfg(), rawDecoder, applier1, testLogger())
	require.NoError(t, err)
	d2, err := drainer.New[rawPayload](pool2, testCfg(), rawDecoder, applier2, testLogger())
	require.NoError(t, err)

	dCtx, dCancel := context.WithCancel(ctx)
	defer dCancel()
	done1 := make(chan struct{})
	done2 := make(chan struct{})
	go func() { defer close(done1); _ = d1.Run(dCtx) }()
	go func() { defer close(done2); _ = d2.Run(dCtx) }()
	t.Cleanup(func() { <-done1; <-done2 })

	time.Sleep(200 * time.Millisecond)

	// Insert 20 rows in one wave — both drainers will race on each NOTIFY.
	const n = 20
	for i := 0; i < n; i++ {
		insertOutboxRow(t, ctx, pool1, "fga.tuple.write",
			fmt.Sprintf(`{"user":"user:r%02d","relation":"viewer","object":"project:p"}`, i))
	}

	// Reach n applies, then hold a quiescence window: a duplicate apply from the
	// losing instance is caught the moment it lands, not missed by a fixed sleep.
	final, ok := waitStableInt64(
		func() int64 { return int64(fa.countCalls()) }, int64(n), 500*time.Millisecond, 5*time.Second)
	require.Truef(t, ok,
		"exactly-once: %d unique rows must reach and HOLD exactly %d applies across 2 drainers (NO duplicates); observed %d",
		n, n, final)

	// Verify per-payload applied exactly once.
	calls := fa.snapshotCalls()
	seen := make(map[string]int, len(calls))
	for _, c := range calls {
		seen[string(c.payload)]++
	}
	for payload, count := range seen {
		assert.Equalf(t, 1, count, "payload %s applied %dx (must be exactly once across HA)", payload, count)
	}

	// Load-spread is BEST-EFFORT, not an HA-correctness invariant: with
	// FOR UPDATE SKIP LOCKED a single faster instance may legitimately claim
	// every row while the other acts as a hot standby — that is still correct
	// HA (failover-capable, exactly-once preserved). Asserting each drainer
	// applied ≥1 row is therefore inherently racy and was an intermittent CI
	// flake ("0 is not greater than 0"). The real guarantee — exactly-once
	// across both instances with zero duplicates — is asserted above
	// (total == n + per-payload count == 1). Here we only OBSERVE the split.
	c1, c2 := calls1.Load(), calls2.Load()
	t.Logf("HA load split: drainer1=%d drainer2=%d (sum=%d, n=%d)", c1, c2, c1+c2, n)
	assert.Equal(t, int64(n), c1+c2,
		"both drainers together must account for exactly n applies (HA coverage)")
}

// ── Graceful shutdown ───────────────────────────────────────────────────────

// TestW1_1_11_CtxCancel_FinishesInflightApply_CleanExit — ctx cancel lets an
// in-flight apply finish, then Run returns cleanly.
func TestW1_1_11_CtxCancel_FinishesInflightApply_CleanExit(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()
	fa.setDelay("fga.tuple.write", 500*time.Millisecond) // simulate slow applier

	// Signal channel: hook fires when applier starts → test can cancel
	// precisely after in-flight begins.
	applierStarted := make(chan struct{}, 1)
	fa.setOnCallHook(func(eventType string, payload []byte, attemptIdx int) {
		select {
		case applierStarted <- struct{}{}:
		default:
		}
	})

	dCancel, done, errCh := startDrainer(t, ctx, pool, testCfg(), fa)

	waitForListenerReady(t, ctx, pool, testCfg().Channel)
	id := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"user":"user:slow","relation":"viewer","object":"project:p"}`)

	// Wait for applier to actually start.
	select {
	case <-applierStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("applier did not start in time")
	}

	// Cancel during in-flight apply.
	cancelTime := time.Now()
	dCancel()

	// Run() must return within 2s of cancel.
	select {
	case err := <-errCh:
		elapsed := time.Since(cancelTime)
		assert.LessOrEqual(t, elapsed, 2*time.Second,
			"Run() must return ≤2s after ctx.Cancel (got %s)", elapsed)
		assert.NoError(t, err, "graceful shutdown should return nil error")
	case <-time.After(3 * time.Second):
		t.Fatal("Run() did not return within 3s after ctx.Cancel")
	}
	<-done

	// In-flight row must have completed (sent_at NOT NULL).
	row := readOutboxRow(t, context.Background(), pool, id)
	assert.NotNil(t, row.sentAt,
		"in-flight apply must complete before exit (sent_at must be set)")
}

// TestW1_1_12_CtxCancel_EmptyQueue_FastExit — an idle drainer exits quickly on
// ctx cancel.
func TestW1_1_12_CtxCancel_EmptyQueue_FastExit(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()

	dCancel, done, errCh := startDrainer(t, ctx, pool, testCfg(), fa)

	// Let drainer settle into idle (queue empty, only LISTEN/poll).
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, 0, fa.countCalls(), "applier must not have been called on empty queue")

	cancelTime := time.Now()
	dCancel()

	select {
	case err := <-errCh:
		elapsed := time.Since(cancelTime)
		assert.Less(t, elapsed, 500*time.Millisecond,
			"idle drainer must exit within 500ms of ctx.Cancel (got %s)", elapsed)
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("idle Run() did not return within 2s after ctx.Cancel")
	}
	<-done
}

// ── Edge cases ──────────────────────────────────────────────────────────────

// TestW1_1_13_IdleDrainer_NoBusyLoop — an idle drainer does not busy-poll the
// outbox table.
func TestW1_1_13_IdleDrainer_NoBusyLoop(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa := newFakeApplier()

	// PollFallback = 30s → during 5s observation, expect 0 poll-driven claims.
	cfg := testCfg()
	cfg.PollFallback = 30 * time.Second

	// Deterministic, in-process claim counter (independent of async pg_stat lag):
	// every claim query against the outbox table increments it.
	var claims atomic.Int64
	dCancel, done, _ := startDrainer(t, ctx, pool, cfg, fa,
		drainer.WithClaimObserver[rawPayload](func() { claims.Add(1) }))
	defer func() { dCancel(); <-done }()

	// Let startup catch-up settle (it issues a bounded number of claims to drain
	// the — empty — backlog), then baseline the counter.
	time.Sleep(500 * time.Millisecond)
	baseline := claims.Load()

	// PRIMARY (deterministic): across a 5s idle window with PollFallback=30s the
	// drainer must issue ZERO additional claims. A reintroduced busy-poll would
	// increment `claims` in-process immediately — no pg_stat propagation lag.
	time.Sleep(5 * time.Second)
	idleClaims := claims.Load() - baseline
	assert.Equal(t, int64(0), idleClaims,
		"idle drainer must issue 0 claims in a 5s window (PollFallback=30s); saw %d", idleClaims)

	// SECONDARY (sanity, best-effort): pg_stat_user_tables scan delta should also
	// be ~0. Kept only as a coarse cross-check; the assertion above is the sound
	// guard (pg_stat counters flush asynchronously and can lag the window).
	var seqScan, idxScan int64
	err := pool.QueryRow(ctx, `
		SELECT COALESCE(seq_scan, 0), COALESCE(idx_scan, 0)
		  FROM pg_stat_user_tables
		 WHERE schemaname = 'kacho_iam' AND relname = 'fga_outbox'
	`).Scan(&seqScan, &idxScan)
	require.NoError(t, err)
	t.Logf("pg_stat cross-check: seq_scan=%d idx_scan=%d (secondary)", seqScan, idxScan)

	assert.Equal(t, 0, fa.countCalls(), "applier must not be called on idle empty queue")
}

// TestW1_1_14_ReapplyAfterRestart_FilteredBySentAt — a restarted drainer does
// not re-apply already-sent rows.
func TestW1_1_14_ReapplyAfterRestart_FilteredBySentAt(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa1 := newFakeApplier()

	// Drainer 1: process one row, then exit.
	dCancel1, done1, _ := startDrainer(t, ctx, pool, testCfg(), fa1)
	waitForListenerReady(t, ctx, pool, testCfg().Channel)
	id := insertOutboxRow(t, ctx, pool, "fga.tuple.write",
		`{"user":"user:once","relation":"viewer","object":"project:p"}`)
	waitForRowSent(t, ctx, pool, id, 1*time.Second)
	require.Equal(t, 1, fa1.countCalls())

	dCancel1()
	<-done1

	// Confirm row has sent_at set in DB.
	row := readOutboxRow(t, ctx, pool, id)
	require.NotNil(t, row.sentAt)

	// Drainer 2: fresh instance, no new inserts. Must NOT re-apply.
	fa2 := newFakeApplier()
	dCancel2, done2, _ := startDrainer(t, ctx, pool, testCfg(), fa2)
	defer func() { dCancel2(); <-done2 }()

	// Hold a 2s quiescence window asserting drainer 2 issues ZERO applies: if it
	// ignored the sent_at filter and re-applied, the count would leave 0 and the
	// poll fails the instant it happens (fail-fast), rather than a single read
	// after a fixed sleep that a late re-apply could slip past.
	_, ok := waitStableInt64(
		func() int64 { return int64(fa2.countCalls()) }, 0, 2*time.Second, 2*time.Second)
	assert.True(t, ok,
		"drainer 2 must not re-apply already-sent row (WHERE sent_at IS NULL must filter it out)")
}

// TestW1_1_15_MissedNotify_StartupCatchup — rows whose NOTIFY was missed are
// recovered by startup catch-up.
func TestW1_1_15_MissedNotify_StartupCatchup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, _ := setupDrainerPG(t)
	fa1 := newFakeApplier()

	// 1) Start drainer briefly, then cancel.
	dCancel1, done1, _ := startDrainer(t, ctx, pool, testCfg(), fa1)
	time.Sleep(200 * time.Millisecond)
	dCancel1()
	<-done1
	require.Equal(t, 0, fa1.countCalls())

	// 2) While drainer is offline, INSERT 3 rows — NOTIFY for these goes to
	//    void (no LISTEN-er).
	ids := make([]int64, 3)
	for i := 0; i < 3; i++ {
		ids[i] = insertOutboxRow(t, ctx, pool, "fga.tuple.write",
			fmt.Sprintf(`{"user":"user:miss%d","relation":"viewer","object":"project:p"}`, i))
	}

	// 3) Restart drainer with the same config + tight PollFallback so a
	//    fallback-poll wouldn't accidentally save the test.
	cfg := testCfg()
	cfg.PollFallback = 30 * time.Second // ensure ONLY startup catch-up can save us

	fa2 := newFakeApplier()
	dCancel2, done2, _ := startDrainer(t, ctx, pool, cfg, fa2)
	defer func() { dCancel2(); <-done2 }()

	// 4) Within 2s startup catch-up must drain all 3.
	require.True(t, fa2.waitForCalls(3, 2*time.Second),
		"startup catch-up must drain missed-NOTIFY rows within 2s (got %d)", fa2.countCalls())

	for _, id := range ids {
		waitForRowSent(t, ctx, pool, id, 2*time.Second)
	}
}
