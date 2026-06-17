// Package reconciler is the backstop layer over the outbox/drainer
// (sub-phase 1.4 D-6 + D-6c). The drainer delivers intents at-least-once; the
// reconciler repairs the cases the drainer alone cannot:
//
//   - RedrivePoisoned — re-drives poisoned/exhausted rows (sent_at IS NULL AND
//     attempt_count >= MaxAttempts) back to claimable so the drainer retries
//     them (e.g. after the permanent cause was fixed, or it was misclassified).
//   - BackfillFromState — derive-from-state safety-net: a per-service
//     ResourceEnumerator lists resource rows that lack an applied owner-tuple
//     intent; the reconciler re-emits a project-hierarchy register-intent
//     through the SAME transactional register-outbox table (the CAS-claim path
//     governs delivery — never a direct FGA/IAM call). It synthesises ONLY the
//     project-hierarchy tuple (derivable from the stored project_id); the
//     owner-self-grant subject is NOT reconstructible from resource state
//     (D-4 / 1.4-06a) so it is never guessed.
//   - GCOrphans — inverse-orphan GC with the anti-race invariant (D-6c): emit
//     fga.unregister for a tuple whose resource is gone ONLY when (a) the
//     resource is not "intended-registered" (no register-intent that is the most
//     recent intent for the id) AND (b) the resource has been absent for a grace
//     window. A concurrent re-Create that co-commits a register-intent therefore
//     always wins — its durable intent blocks the unregister, so a legitimate
//     re-create never suffers self-inflicted owner access-loss (1.4-07b).
//
// The domain knowledge — how to enumerate resource rows / project_id / check
// existence / list registered tuples — is a per-service adapter
// (ResourceEnumerator + TupleRegistry), NOT corelib logic. corelib orchestrates
// the passes and the emit; the service injects the domain enumeration.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event-type literals on the register-outbox table (compute/nlb/apps SEC-D form).
const (
	eventRegister   = "fga.register"
	eventUnregister = "fga.unregister"
)

// ResourceRow is one live resource as seen by the per-service enumerator. Kind +
// ID identify the resource; ProjectID is the hierarchy parent ("" when there is
// no derivable project hierarchy — e.g. an account — in which case the
// reconciler will NOT backfill, D-4).
type ResourceRow struct {
	Kind      string
	ID        string
	ProjectID string
}

// RegisteredTuple is one currently-registered owner-tuple (Kind + resource ID)
// as seen by the per-service TupleRegistry — the candidate set for orphan GC.
type RegisteredTuple struct {
	Kind string
	ID   string
}

// ResourceEnumerator is the per-service adapter that knows the domain resource
// tables. It is injected by the service (NOT implemented in corelib).
type ResourceEnumerator interface {
	// ListResources enumerates the live resource rows (id + project hierarchy).
	ListResources(ctx context.Context) ([]ResourceRow, error)
	// ResourceExists reports whether (kind,id) still exists — used by GC to
	// confirm a registered tuple's resource is truly gone.
	ResourceExists(ctx context.Context, kind, id string) (bool, error)
}

// TupleRegistry is the per-service adapter that lists currently-registered
// owner-tuples (the orphan-GC candidate set). It is injected by the service.
type TupleRegistry interface {
	ListRegistered(ctx context.Context) ([]RegisteredTuple, error)
}

// Adapters bundles the per-service domain adapters.
type Adapters struct {
	Enumerator ResourceEnumerator
	Registry   TupleRegistry
}

// Config parameterises a Reconciler.
type Config struct {
	// Table — full register-outbox table name (`<schema>.<table>`). Must be the
	// SAME table the drainer drains (intents are re-emitted here so the CAS-claim
	// path governs delivery).
	Table string
	// Channel — LISTEN/NOTIFY channel of the table (for parity / future use).
	Channel string
	// MaxAttempts — poison threshold (default 10) used by RedrivePoisoned.
	MaxAttempts int
	// GraceWindow — a resource must be continuously absent (and not
	// intended-registered) for at least this long before GCOrphans emits an
	// unregister (anti-race deferral, D-6c). 0 → emit on the first confirmed
	// pass (no deferral). Production sets a window (e.g. a minute) so any
	// in-flight re-Create lands its durable register-intent first.
	GraceWindow time.Duration
}

func (c Config) withDefaults() Config {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 10
	}
	return c
}

// Reconciler orchestrates the backstop passes over one register-outbox table.
type Reconciler struct {
	pool *pgxpool.Pool
	cfg  Config
	ad   Adapters
	log  *slog.Logger

	// firstSeenAbsent tracks when each orphan candidate was first observed absent
	// (the GraceWindow tombstone). Reset when the resource reappears or becomes
	// intended-registered. Guarded by mu (GCOrphans may run concurrently).
	mu              sync.Mutex
	firstSeenAbsent map[string]time.Time
}

// New constructs a Reconciler. pool + Table + both adapters are required.
func New(pool *pgxpool.Pool, cfg Config, ad Adapters, logger *slog.Logger) (*Reconciler, error) {
	if pool == nil {
		return nil, errors.New("reconciler.New: pool is nil")
	}
	if cfg.Table == "" {
		return nil, errors.New("reconciler.New: Config.Table required")
	}
	if ad.Enumerator == nil {
		return nil, errors.New("reconciler.New: Adapters.Enumerator required")
	}
	if ad.Registry == nil {
		return nil, errors.New("reconciler.New: Adapters.Registry required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{
		pool:            pool,
		cfg:             cfg.withDefaults(),
		ad:              ad,
		log:             logger.With(slog.String("component", "outbox_reconciler"), slog.String("table", cfg.Table)),
		firstSeenAbsent: map[string]time.Time{},
	}, nil
}

// RedrivePoisoned resets poisoned/exhausted rows (sent_at IS NULL AND
// attempt_count >= MaxAttempts) back to claimable (attempt_count = 0, last_error
// = NULL) so the drainer retries them. Returns the number re-driven.
func (r *Reconciler) RedrivePoisoned(ctx context.Context) (int, error) {
	q := fmt.Sprintf(
		`UPDATE %s
		    SET attempt_count = 0, last_error = NULL
		  WHERE sent_at IS NULL AND attempt_count >= $1`,
		r.cfg.Table,
	)
	tag, err := r.pool.Exec(ctx, q, r.cfg.MaxAttempts)
	if err != nil {
		return 0, fmt.Errorf("reconciler.RedrivePoisoned %s: %w", r.cfg.Table, err)
	}
	return int(tag.RowsAffected()), nil
}

// BackfillFromState synthesises a project-hierarchy register-intent for each live
// resource that is NOT currently intended-registered (no register-intent that is
// the latest intent for the id) AND has a derivable project_id. It emits through
// the same register-outbox table (CAS-claim path delivers). Returns the count
// emitted. Resources without a project_id are skipped (owner-self-grant is not
// backfillable, D-4 / 1.4-06a).
func (r *Reconciler) BackfillFromState(ctx context.Context) (int, error) {
	rows, err := r.ad.Enumerator.ListResources(ctx)
	if err != nil {
		return 0, fmt.Errorf("reconciler.BackfillFromState list: %w", err)
	}
	emitted := 0
	for _, res := range rows {
		if res.ProjectID == "" {
			// No derivable project-hierarchy → not backfillable (D-4).
			continue
		}
		n, err := r.backfillOne(ctx, res)
		if err != nil {
			return emitted, err
		}
		emitted += n
	}
	return emitted, nil
}

// backfillOne emits a register-intent for one resource iff it is not already
// intended-registered. The check + insert run in one advisory-locked tx so a
// concurrent path serialises on the resource id (no duplicate backfill).
func (r *Reconciler) backfillOne(ctx context.Context, res ResourceRow) (int, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("reconciler.backfillOne begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockResource(ctx, tx, res.ID); err != nil {
		return 0, err
	}
	registered, err := intendedRegistered(ctx, tx, r.cfg.Table, res.ID)
	if err != nil {
		return 0, err
	}
	if registered {
		return 0, nil // already has a live register-intent → no backfill
	}

	payload := fmt.Sprintf(`{"project_id":%q}`, res.ProjectID)
	if err := insertIntent(ctx, tx, r.cfg.Table, eventRegister, res.Kind, res.ID, payload); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("reconciler.backfillOne commit: %w", err)
	}
	return 1, nil
}

// GCOrphans emits fga.unregister for each registered tuple whose resource is gone
// and whose absence has been confirmed for >= GraceWindow with NO live
// register-intent (anti-race D-6c). Returns the count of unregister-intents
// emitted. Safe to call concurrently.
func (r *Reconciler) GCOrphans(ctx context.Context) (int, error) {
	tuples, err := r.ad.Registry.ListRegistered(ctx)
	if err != nil {
		return 0, fmt.Errorf("reconciler.GCOrphans list: %w", err)
	}

	emitted := 0
	live := map[string]struct{}{}
	for _, tup := range tuples {
		exists, err := r.ad.Enumerator.ResourceExists(ctx, tup.Kind, tup.ID)
		if err != nil {
			return emitted, fmt.Errorf("reconciler.GCOrphans exists %s: %w", tup.ID, err)
		}
		if exists {
			live[tup.ID] = struct{}{}
			r.clearTombstone(tup.ID)
			continue
		}
		ok, err := r.gcOne(ctx, tup)
		if err != nil {
			return emitted, err
		}
		if ok {
			emitted++
		}
	}
	return emitted, nil
}

// gcOne attempts to unregister one orphan candidate under the anti-race
// invariant. Returns true if an unregister-intent was emitted.
func (r *Reconciler) gcOne(ctx context.Context, tup RegisteredTuple) (bool, error) {
	// GraceWindow deferral: first sighting only records the tombstone (a
	// concurrent re-Create gets a full window to land its durable register-intent
	// which then blocks GC). Eligible once absent for >= GraceWindow.
	if !r.graceElapsed(tup.ID) {
		return false, nil
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("reconciler.gcOne begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialise register-vs-unregister emits on the resource id.
	if err := lockResource(ctx, tx, tup.ID); err != nil {
		return false, err
	}

	// Anti-race condition (a): a live register-intent (the latest intent is a
	// register) means the resource is intended-registered → a concurrent
	// re-Create won; NEVER unregister.
	registered, err := intendedRegistered(ctx, tx, r.cfg.Table, tup.ID)
	if err != nil {
		return false, err
	}
	if registered {
		r.clearTombstone(tup.ID)
		return false, nil
	}

	if err := insertIntent(ctx, tx, r.cfg.Table, eventUnregister, tup.Kind, tup.ID, "{}"); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("reconciler.gcOne commit: %w", err)
	}
	r.clearTombstone(tup.ID)
	return true, nil
}

// graceElapsed reports whether the candidate has been continuously absent for at
// least GraceWindow. The first call records the tombstone and (for GraceWindow>0)
// returns false; GraceWindow==0 → eligible on the first confirmed pass.
func (r *Reconciler) graceElapsed(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	first, seen := r.firstSeenAbsent[id]
	now := time.Now()
	if !seen {
		r.firstSeenAbsent[id] = now
		return r.cfg.GraceWindow == 0
	}
	return now.Sub(first) >= r.cfg.GraceWindow
}

func (r *Reconciler) clearTombstone(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.firstSeenAbsent, id)
}

// EmitRegister co-commits an fga.register intent in the caller's writer-tx (the
// same tx that inserts the resource — atomic, ban #10). It takes the per-resource
// advisory lock so it serialises against GCOrphans' unregister-emit on the same
// id: this is the anti-race contract (D-6c) — a re-Create that co-commits its
// register-intent always wins over a concurrent GC. Services call this from
// their resource Create writer-tx (greenfield SEC-D) or the reconciler uses it
// for backfill.
//
// table/kind are trusted literals; payload is a JSON object string (owner-tuple
// data incl. project_id).
func EmitRegister(ctx context.Context, tx pgx.Tx, table, kind, id, payload string) error {
	if err := lockResource(ctx, tx, id); err != nil {
		return err
	}
	return insertIntent(ctx, tx, table, eventRegister, kind, id, payload)
}

// EmitUnregister co-commits an fga.unregister intent in the caller's writer-tx
// (the same tx that deletes the resource). Like EmitRegister it takes the
// per-resource advisory lock so register/unregister emits for the same id
// serialise.
func EmitUnregister(ctx context.Context, tx pgx.Tx, table, kind, id, payload string) error {
	if err := lockResource(ctx, tx, id); err != nil {
		return err
	}
	return insertIntent(ctx, tx, table, eventUnregister, kind, id, payload)
}

// lockResource takes a transaction-scoped advisory lock keyed by the resource id
// so register-emit and unregister-emit for the same id serialise.
func lockResource(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, id); err != nil {
		return fmt.Errorf("reconciler: advisory lock %s: %w", id, err)
	}
	return nil
}

// intendedRegistered reports whether the resource id is currently intended to be
// registered: there exists a register-intent that is more recent than any
// unregister-intent for the id (covers both pending and already-sent rows — a
// re-Create's durable register-intent blocks GC even after delivery).
func intendedRegistered(ctx context.Context, tx pgx.Tx, table, id string) (bool, error) {
	q := fmt.Sprintf(`
		SELECT COALESCE(
		    (SELECT event_type FROM %s
		      WHERE resource_id = $1 AND event_type IN ($2, $3)
		      ORDER BY id DESC LIMIT 1),
		    '') = $2
	`, table)
	var registered bool
	if err := tx.QueryRow(ctx, q, id, eventRegister, eventUnregister).Scan(&registered); err != nil {
		return false, fmt.Errorf("reconciler: intended-registered %s: %w", id, err)
	}
	return registered, nil
}

// insertIntent inserts one intent row into the register-outbox table (the NOTIFY
// trigger wakes the drainer). table/eventType are trusted literals.
func insertIntent(ctx context.Context, tx pgx.Tx, table, eventType, kind, id, payload string) error {
	q := fmt.Sprintf(
		`INSERT INTO %s (event_type, resource_kind, resource_id, payload)
		 VALUES ($1, $2, $3, $4::jsonb)`,
		table,
	)
	if _, err := tx.Exec(ctx, q, eventType, kind, id, payload); err != nil {
		return fmt.Errorf("reconciler: insert %s intent %s: %w", eventType, id, err)
	}
	return nil
}
