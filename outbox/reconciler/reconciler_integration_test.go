package reconciler_test

// Integration tests for the outbox reconciler (sub-phase 1.4, D-6 + D-6c).
//
// RED-phase (test-first, ban #12): `reconciler.*` does not yet exist. Acceptance
// scenarios:
//   1.4-06  re-drive poisoned + derive-from-state backfill (project-hierarchy)
//   1.4-06a owner-self-grant NOT backfillable (reconciler must not synthesise it)
//   1.4-07  inverse-orphan GC: tuple-for-absent-resource → Unregister
//   1.4-07b anti-race GC vs concurrent re-Create (MANDATORY -race): re-create's
//           register-intent wins, no self-inflicted access-loss
//
// The reconciler orchestrates passes + emits through the SAME transactional
// register-outbox table (CAS-claim path governs delivery); the domain
// resource-enumerate is a per-service adapter (ResourceEnumerator), NOT corelib
// logic. Tests provide a fake enumerator backed by an in-memory resource store.
//
// Run: go test ./outbox/... -race -p 1

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/PRO-Robotech/kacho-corelib/outbox/reconciler"
)

// register-outbox table mirrors the compute SEC-D shape (id BIGSERIAL PK +
// resource_kind/resource_id + event_type CHECK + payload + sent/attempt).
const reconcilerSchema = `
CREATE SCHEMA IF NOT EXISTS kacho_apps;
CREATE TABLE kacho_apps.fga_register_outbox (
    id            bigserial    PRIMARY KEY,
    event_type    text         NOT NULL,
    resource_kind text         NOT NULL DEFAULT '',
    resource_id   text         NOT NULL DEFAULT '',
    payload       jsonb        NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz  NOT NULL DEFAULT now(),
    sent_at       timestamptz,
    last_error    text,
    attempt_count integer      NOT NULL DEFAULT 0,
    CONSTRAINT fga_register_outbox_event_type_check
        CHECK (event_type IN ('fga.register', 'fga.unregister'))
);
CREATE INDEX fga_register_outbox_pending_idx
    ON kacho_apps.fga_register_outbox (id) WHERE sent_at IS NULL;
`

const tbl = "kacho_apps.fga_register_outbox"

func setupPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("SKIP_INTEGRATION") == "1" {
		t.Skip("integration tests skipped (SKIP_INTEGRATION=1)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("kacho_apps_test"),
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
	_, err = pool.Exec(ctx, reconcilerSchema)
	require.NoError(t, err)
	return pool
}

// fakeEnumerator is a per-service ResourceEnumerator adapter backed by an
// in-memory resource store {id -> projectID}. It implements the corelib
// reconciler.ResourceEnumerator port.
type fakeEnumerator struct {
	mu        sync.Mutex
	resources map[string]string // resourceID -> projectID
	kind      string
}

func newFakeEnumerator(kind string) *fakeEnumerator {
	return &fakeEnumerator{resources: map[string]string{}, kind: kind}
}

func (f *fakeEnumerator) put(id, projectID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resources[id] = projectID
}

func (f *fakeEnumerator) delete(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.resources, id)
}

// ListResources enumerates all live resource rows (id + project hierarchy
// payload) — the source of truth for derive-from-state backfill.
func (f *fakeEnumerator) ListResources(_ context.Context) ([]reconciler.ResourceRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]reconciler.ResourceRow, 0, len(f.resources))
	for id, prj := range f.resources {
		out = append(out, reconciler.ResourceRow{
			Kind:      f.kind,
			ID:        id,
			ProjectID: prj,
		})
	}
	return out, nil
}

// ResourceExists reports whether the resource id still exists (used by GC).
func (f *fakeEnumerator) ResourceExists(_ context.Context, _ string, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.resources[id]
	return ok, nil
}

// registeredTuples models the FGA-side owner-tuple set the GC scans for orphans.
type registeredTuples struct {
	mu     sync.Mutex
	tuples map[string]string // resourceID -> kind
}

func newRegisteredTuples() *registeredTuples {
	return &registeredTuples{tuples: map[string]string{}}
}

func (r *registeredTuples) put(kind, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tuples[id] = kind
}

// ListRegistered enumerates the currently-registered owner-tuples (kind+id) —
// the candidate set for inverse-orphan GC.
func (r *registeredTuples) ListRegistered(_ context.Context) ([]reconciler.RegisteredTuple, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]reconciler.RegisteredTuple, 0, len(r.tuples))
	for id, kind := range r.tuples {
		out = append(out, reconciler.RegisteredTuple{Kind: kind, ID: id})
	}
	return out, nil
}

func pendingByResource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventType, id string) int {
	t.Helper()
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM kacho_apps.fga_register_outbox
		   WHERE event_type=$1 AND resource_id=$2 AND sent_at IS NULL`,
		eventType, id,
	).Scan(&n)
	require.NoError(t, err)
	return n
}

func markAllSent(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx,
		`UPDATE kacho_apps.fga_register_outbox SET sent_at = now() WHERE sent_at IS NULL`)
	require.NoError(t, err)
}

func newReconciler(t *testing.T, pool *pgxpool.Pool, enum reconciler.ResourceEnumerator, reg reconciler.TupleRegistry, grace time.Duration) *reconciler.Reconciler {
	t.Helper()
	r, err := reconciler.New(pool, reconciler.Config{
		Table:       tbl,
		Channel:     "kacho_apps_fga_register_outbox",
		MaxAttempts: 10,
		GraceWindow: grace,
	}, reconciler.Adapters{Enumerator: enum, Registry: reg}, nil)
	require.NoError(t, err)
	return r
}

// Test_1_4_06_RedrivePoisoned_And_Backfill — acceptance 1.4-06.
func Test_1_4_06_RedrivePoisoned_And_Backfill(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupPG(t)

	// (a) one poisoned intent (attempt_count == MaxAttempts, sent_at NULL).
	_, err := pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox
		   (event_type, resource_kind, resource_id, attempt_count, last_error)
		 VALUES ('fga.register','apps_application','app-poison',10,'was permanent')`)
	require.NoError(t, err)

	// (b) one resource row WITHOUT an applied intent (legacy never-enqueued).
	enum := newFakeEnumerator("apps_application")
	enum.put("app-legacy", "prj-X")
	reg := newRegisteredTuples()

	r := newReconciler(t, pool, enum, reg, 0)

	// (a) Re-drive: poisoned row reset to claimable.
	n, err := r.RedrivePoisoned(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "exactly one poisoned row re-driven")
	var attempt int
	var lastErr *string
	err = pool.QueryRow(ctx,
		`SELECT attempt_count, last_error FROM kacho_apps.fga_register_outbox
		   WHERE resource_id='app-poison'`).Scan(&attempt, &lastErr)
	require.NoError(t, err)
	assert.Less(t, attempt, 10, "re-driven row attempt_count reset below MaxAttempts (claimable)")
	assert.Nil(t, lastErr, "last_error cleared on re-drive")

	// (b) Backfill: derive-from-state synthesises a project-hierarchy register
	// intent for app-legacy (which had none) through the SAME outbox table.
	emitted, err := r.BackfillFromState(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, emitted, "one legacy resource gets a backfill intent")
	assert.Equal(t, 1, pendingByResource(t, ctx, pool, "fga.register", "app-legacy"),
		"backfill writes a pending fga.register intent for the legacy resource")

	// Backfill is idempotent: a resource that now HAS a pending intent is not
	// re-emitted on the next pass.
	emitted2, err := r.BackfillFromState(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, emitted2, "no duplicate backfill while an intent is pending")
}

// Test_1_4_06a_OwnerSelfGrant_NotBackfillable — acceptance 1.4-06a / D-4.
//
// The reconciler only synthesises project-hierarchy intents (derivable from the
// stored project_id). A resource with NO project_id (owner-self-grant only,
// subject unknowable from state) must NOT get any synthesised intent — the
// reconciler must not guess the principal.
func Test_1_4_06a_OwnerSelfGrant_NotBackfillable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupPG(t)

	enum := newFakeEnumerator("apps_application")
	enum.put("acc-noproj", "") // no project hierarchy derivable
	reg := newRegisteredTuples()
	r := newReconciler(t, pool, enum, reg, 0)

	emitted, err := r.BackfillFromState(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, emitted,
		"reconciler must NOT synthesise an intent when no project-hierarchy is derivable")
	assert.Equal(t, 0, pendingByResource(t, ctx, pool, "fga.register", "acc-noproj"))
}

// Test_1_4_07_InverseOrphanGC_HappyPath — acceptance 1.4-07.
//
// A registered owner-tuple whose resource no longer exists → GC emits an
// fga.unregister intent. A tuple whose resource still exists is untouched.
func Test_1_4_07_InverseOrphanGC_HappyPath(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupPG(t)

	enum := newFakeEnumerator("apps_application")
	enum.put("app-live", "prj-X") // still exists
	reg := newRegisteredTuples()
	reg.put("apps_application", "app-live")
	reg.put("apps_application", "app-orphan") // resource gone, tuple lingers

	r := newReconciler(t, pool, enum, reg, 0)

	gc, err := r.GCOrphans(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, gc, "exactly one orphan unregistered")
	assert.Equal(t, 1, pendingByResource(t, ctx, pool, "fga.unregister", "app-orphan"),
		"orphan tuple gets an fga.unregister intent")
	assert.Equal(t, 0, pendingByResource(t, ctx, pool, "fga.unregister", "app-live"),
		"live resource's tuple must NOT be GC'd")
}

// Test_1_4_07b_GC_AntiRace_ConcurrentRecreateWins — acceptance 1.4-07b (MANDATORY -race).
//
// id X deleted (orphan tuple). GC pass runs concurrently with a re-Create that
// co-commits an fga.register intent for X BEFORE the GC's unregister lands. The
// anti-race invariant (D-6c): a pending register-intent for X blocks the
// unregister. Final state: register-intent for X PRESENT (re-create wins), and
// NO unregister-intent for X (no self-inflicted access-loss).
func Test_1_4_07b_GC_AntiRace_ConcurrentRecreateWins(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupPG(t)

	enum := newFakeEnumerator("apps_application")
	reg := newRegisteredTuples()
	reg.put("apps_application", "X") // orphan candidate (resource absent in enum)

	// GraceWindow > 0: GC must observe X continuously absent (and NOT
	// intended-registered) for the window before it may unregister — the
	// anti-race deferral (D-6c). A concurrent re-Create co-commits its durable
	// register-intent (via the same per-resource advisory lock used by GC), so
	// the register-intent always wins: GC sees X intended-registered and never
	// emits an unregister. The window is comfortably longer than the per-round
	// Create latency so the race is decided deterministically in the
	// re-Create's favour, while -race still exercises the concurrent paths.
	r := newReconciler(t, pool, enum, reg, 500*time.Millisecond)

	// The re-Create co-commits an fga.register intent for X via the corelib
	// anti-race contract (EmitRegister takes pg_advisory_xact_lock on the id, the
	// same lock GCOrphans takes) and marks the resource live again — mirrors a
	// real ApplicationService.Create writing resource + intent atomically.
	reCreate := func() {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
		require.NoError(t, err)
		require.NoError(t, reconciler.EmitRegister(ctx, tx, tbl, "apps_application", "X", `{"project_id":"prj-X"}`))
		require.NoError(t, tx.Commit(ctx))
		enum.put("X", "prj-X")
	}

	// Run GC and the re-Create concurrently many times to surface the race.
	const iters = 30
	var wg sync.WaitGroup
	for i := 0; i < iters; i++ {
		// Start of round: X is an orphan-candidate (resource absent, tuple
		// registered) — the only thing that saves it is the concurrent
		// register-intent from re-Create plus the grace deferral.
		enum.delete("X")
		reg.put("apps_application", "X")

		wg.Add(2)
		go func() { defer wg.Done(); _, _ = r.GCOrphans(ctx) }()
		go func() { defer wg.Done(); reCreate() }()
		wg.Wait()
		// One more GC pass after both finished — by now the durable
		// register-intent is committed, so even an eligible candidate is blocked.
		_, _ = r.GCOrphans(ctx)

		// Final state: a register intent for X exists (re-create wins) and there
		// is NO unregister intent for X — the tuple is NEVER removed while a
		// register-intent is the latest intent (no self-inflicted access-loss).
		assert.GreaterOrEqualf(t, pendingByResource(t, ctx, pool, "fga.register", "X"), 1,
			"round %d: re-create's register-intent must be present", i)
		assert.Equalf(t, 0, pendingByResource(t, ctx, pool, "fga.unregister", "X"),
			"round %d: anti-race — NO unregister emitted (no access-loss)", i)

		// Reset for next round: drain pending intents (simulate delivery) and
		// clear the table so the next round re-tests the race from scratch.
		markAllSent(t, ctx, pool)
		_, err := pool.Exec(ctx, `DELETE FROM kacho_apps.fga_register_outbox`)
		require.NoError(t, err)
	}
}
