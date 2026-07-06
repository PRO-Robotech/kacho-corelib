// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package drainer_test

// Shared fixtures for drainer integration tests.
//
// Each test owns its own Postgres testcontainer (no shared state, parallel-safe).
// Schema is an inline copy of kacho-iam migration 0002_fga_outbox.sql —
// kacho-corelib doesn't own kacho-iam migrations; copying the DDL here keeps
// the drainer package self-contained for testing without a cross-repo dependency.
// If the schema changes in kacho-iam, this constant must be re-synced
// (intentional duplication, documented).

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// fgaOutboxSchema — exact copy of kacho-iam/internal/migrations/0002_fga_outbox.sql
// (only the fga_outbox table + trigger + index pieces; subject_change_outbox /
// fga_model_version / watch_cursors omitted — not used by drainer).
const fgaOutboxSchema = `
CREATE SCHEMA IF NOT EXISTS kacho_iam;
SET search_path TO kacho_iam, public;

CREATE TABLE kacho_iam.fga_outbox (
    id            bigserial    PRIMARY KEY,
    event_type    text         NOT NULL,
    payload       jsonb        NOT NULL,
    created_at    timestamptz  NOT NULL DEFAULT now(),
    sent_at       timestamptz,
    last_error    text,
    attempt_count integer      NOT NULL DEFAULT 0,
    CONSTRAINT fga_outbox_event_type_check
        CHECK (event_type IN ('fga.tuple.write', 'fga.tuple.delete'))
);

CREATE INDEX fga_outbox_pending_idx
    ON kacho_iam.fga_outbox (created_at) WHERE sent_at IS NULL;

CREATE OR REPLACE FUNCTION kacho_iam.fga_outbox_notify() RETURNS trigger
LANGUAGE plpgsql AS $fn$
BEGIN
    PERFORM pg_notify('kacho_iam_fga_outbox', NEW.id::text);
    RETURN NEW;
END;
$fn$;

CREATE TRIGGER fga_outbox_notify_trigger
    AFTER INSERT ON kacho_iam.fga_outbox
    FOR EACH ROW EXECUTE FUNCTION kacho_iam.fga_outbox_notify();
`

// setupDrainerPG спинит контейнер Postgres, накатывает fga_outbox-schema
// и возвращает готовый pool + DSN (DSN нужен для тестов, которым важен
// LISTEN на отдельном connection / запуск второй реплики drainer-а).
func setupDrainerPG(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	if testing.Short() || os.Getenv("SKIP_INTEGRATION") == "1" {
		t.Skip("integration tests skipped (SKIP_INTEGRATION=1)")
	}

	// Use a fresh context per setup so cleanup runs even on test panic.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("kacho_iam_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		termCtx, termCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer termCancel()
		_ = ctr.Terminate(termCtx)
	})

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	_, err = pool.Exec(ctx, fgaOutboxSchema)
	require.NoError(t, err, "apply fgaOutboxSchema (inline copy of kacho-iam 0002)")

	return pool, dsn
}

// fakeApplier is a programmable in-memory applier used by all drainer tests.
// It records every invocation (eventType, payload-bytes, attempt-index)
// and supports per-payload-key behaviour injection (delay, error sequence).
type fakeApplier struct {
	mu sync.Mutex

	// calls is the ordered log of every applier invocation.
	calls []fakeApplierCall

	// callsCount is an atomic mirror of len(calls), safe to read without mu.
	callsCount atomic.Int64

	// errorSeq, keyed by some test-chosen string key (extracted from payload),
	// returns errors in order; nil means success. After the slice is exhausted,
	// fallback `defaultErr` (or nil) is returned.
	errorSeq map[string][]error

	// delayPerCall, keyed similarly, blocks for this duration inside Apply().
	delayPerCall map[string]time.Duration

	// keyFn extracts a stable key from (eventType, payload-bytes).
	// Tests set this to inspect payload JSON; default = eventType.
	keyFn func(eventType string, payload []byte) string

	// defaultErr is returned when no per-key sequence is set and is also the
	// fallback after a per-key sequence is exhausted.
	defaultErr error

	// onCallHook, if set, runs *before* the recorded call + error-return.
	// Useful to signal goroutines / inject test logic.
	onCallHook func(eventType string, payload []byte, attemptIdx int)
}

type fakeApplierCall struct {
	eventType  string
	payload    []byte
	at         time.Time
	attemptIdx int // 1-based per-key
}

func newFakeApplier() *fakeApplier {
	return &fakeApplier{
		errorSeq:     make(map[string][]error),
		delayPerCall: make(map[string]time.Duration),
		keyFn:        func(eventType string, _ []byte) string { return eventType },
	}
}

// setErrorSeq programs a per-key sequence of errors (nil = success).
// Returned by Apply() in order; after exhaustion → defaultErr (or nil).
func (f *fakeApplier) setErrorSeq(key string, seq ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errorSeq[key] = append([]error(nil), seq...)
}

// setDelay programs a per-key delay inside Apply().
func (f *fakeApplier) setDelay(key string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delayPerCall[key] = d
}

// setDefaultErr sets the post-sequence-exhaustion error.
func (f *fakeApplier) setDefaultErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defaultErr = err
}

// setKeyFn overrides the key extraction.
func (f *fakeApplier) setKeyFn(fn func(eventType string, payload []byte) string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keyFn = fn
}

// setOnCallHook installs a pre-call hook.
func (f *fakeApplier) setOnCallHook(fn func(eventType string, payload []byte, attemptIdx int)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onCallHook = fn
}

// Apply is the function the drainer is expected to invoke per row.
// Test wires this in as drainer.Applier[any] (or whatever T the impl uses).
//
// NOTE: this method signature INTENTIONALLY mirrors the drainer.Applier[T]
// contract (`func(ctx, eventType, payload) error`); the test wiring maps it to
// the concrete drainer.Applier type.
func (f *fakeApplier) Apply(ctx context.Context, eventType string, payload []byte) error {
	f.mu.Lock()
	key := f.keyFn(eventType, payload)
	attemptIdx := 1
	for _, c := range f.calls {
		if f.keyFn(c.eventType, c.payload) == key {
			attemptIdx++
		}
	}
	c := fakeApplierCall{
		eventType:  eventType,
		payload:    append([]byte(nil), payload...),
		at:         time.Now(),
		attemptIdx: attemptIdx,
	}
	f.calls = append(f.calls, c)
	f.callsCount.Store(int64(len(f.calls)))

	hook := f.onCallHook
	delay := f.delayPerCall[key]
	var retErr error
	if seq, ok := f.errorSeq[key]; ok && len(seq) > 0 {
		retErr = seq[0]
		f.errorSeq[key] = seq[1:]
	} else {
		retErr = f.defaultErr
	}
	f.mu.Unlock()

	if hook != nil {
		hook(eventType, payload, attemptIdx)
	}

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return retErr
}

// snapshotCalls returns a copy of the calls log (safe to inspect from test).
func (f *fakeApplier) snapshotCalls() []fakeApplierCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeApplierCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// countCalls returns total invocations (atomic, safe under concurrency).
func (f *fakeApplier) countCalls() int {
	return int(f.callsCount.Load())
}

// waitForCalls polls countCalls() until it reaches `want` or `timeout` elapses.
// Returns true on success, false on timeout.
func (f *fakeApplier) waitForCalls(want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f.countCalls() >= want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return f.countCalls() >= want
}

// waitStableInt64 is a bounded quiescence poll for exactly-once assertions.
//
// It polls sample() until it reaches `want`, then verifies the value STAYS
// exactly `want` for `stableFor`. Any overshoot (sample() > want) — a duplicate
// event — is detected and returned as ok=false the instant it happens (fail-fast),
// rather than being missed by a single fixed settle-sleep window. Returns the
// last observed value and ok=true only if `want` was reached and held stable.
//
// This is the sound replacement for `time.Sleep(settle); assert(count==want)`:
// proving the ABSENCE of a late duplicate by re-sampling across the window and
// failing on the first overshoot, instead of reading the counter once after an
// arbitrary delay (which a duplicate arriving after the sleep would escape).
func waitStableInt64(sample func() int64, want int64, stableFor, timeout time.Duration) (int64, bool) {
	deadline := time.Now().Add(timeout)
	// Step 1: reach `want`. An overshoot here already means a duplicate.
	for {
		v := sample()
		if v > want {
			return v, false
		}
		if v == want {
			break
		}
		if time.Now().After(deadline) {
			return v, false
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Step 2: quiescence — value must remain == want for the whole window.
	stableDeadline := time.Now().Add(stableFor)
	for time.Now().Before(stableDeadline) {
		if v := sample(); v != want {
			return v, false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return want, true
}

// insertOutboxRow inserts a single row into kacho_iam.fga_outbox.
// Returns the auto-assigned id.
func insertOutboxRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventType, payload string) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(ctx,
		`INSERT INTO kacho_iam.fga_outbox (event_type, payload)
		 VALUES ($1, $2::jsonb)
		 RETURNING id`,
		eventType, payload,
	).Scan(&id)
	require.NoError(t, err)
	return id
}

// outboxRow is a partial projection used by assertions.
type outboxRow struct {
	id           int64
	eventType    string
	sentAt       *time.Time
	lastError    *string
	attemptCount int
}

// readOutboxRow reads a single row by id.
func readOutboxRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id int64) outboxRow {
	t.Helper()
	var r outboxRow
	r.id = id
	err := pool.QueryRow(ctx,
		`SELECT event_type, sent_at, last_error, attempt_count
		   FROM kacho_iam.fga_outbox WHERE id = $1`,
		id,
	).Scan(&r.eventType, &r.sentAt, &r.lastError, &r.attemptCount)
	require.NoError(t, err)
	return r
}

// waitForRowSent polls until sent_at IS NOT NULL on the row or timeout.
func waitForRowSent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id int64, timeout time.Duration) outboxRow {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r := readOutboxRow(t, ctx, pool, id)
		if r.sentAt != nil {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	r := readOutboxRow(t, ctx, pool, id)
	require.NotNilf(t, r.sentAt, "row id=%d sent_at still NULL after %s (last_error=%v, attempt_count=%d)",
		id, timeout, derefStr(r.lastError), r.attemptCount)
	return r
}

// waitForAttemptCount polls until row.attempt_count >= want or timeout.
func waitForAttemptCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id int64, want int, timeout time.Duration) outboxRow {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r := readOutboxRow(t, ctx, pool, id)
		if r.attemptCount >= want {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	return readOutboxRow(t, ctx, pool, id)
}

func derefStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// sentinel used to assert "this is the kind of error we injected".
var errTestTransient = errors.New("test: simulated transient")
