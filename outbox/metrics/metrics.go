// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package metrics exposes the outbox-delivery observability surface: backlog
// depth, oldest-pending age and poison count per outbox table/channel. It makes
// a stuck/lost owner-tuple delivery observable (alertable) instead of a silent
// Warn-log.
//
// Dependency boundary (Clean Architecture): corelib stays dependency-light — the
// concrete Prometheus client is NOT imported here. Instead the package defines a
// small Recorder interface; the service wires a Prometheus-backed Recorder at
// its composition root (mirroring kacho-iam internal/observability/metrics), and
// tests use the in-memory MemRecorder. The Collector periodically scans an
// outbox table (DB-side) and feeds the gauges into whatever Recorder it is given.
//
// Three series, all labelled by the outbox table name (so a service running two
// outbox families — audit `_outbox` vs register `_fga_register_outbox` — keeps
// them separate and never conflates poison/backlog):
//
//	outbox_backlog_depth{table}              gauge  — pending (sent_at IS NULL) rows
//	outbox_oldest_pending_age_seconds{table} gauge  — age of the oldest pending row
//	outbox_poisoned_total{table}             counter — monotonic poison events
//	(the Collector also reports the current poisoned-row gauge via PoisonedCount)
package metrics

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Recorder is the metrics sink the outbox layer writes to. Implement it with a
// Prometheus registry at the service composition root; corelib provides the
// in-memory MemRecorder for tests and a no-op via a nil-safe wrapper.
//
//   - SetBacklogDepth / SetOldestPendingAgeSeconds / SetPoisonedCount — gauges
//     set by the Collector on each scan.
//   - IncPoisoned — the monotonic counter, incremented by the drainer's poison
//     observer (see drainer.WithPoisonObserver).
type Recorder interface {
	SetBacklogDepth(table string, depth float64)
	SetOldestPendingAgeSeconds(table string, age float64)
	SetPoisonedCount(table string, count float64)
	IncPoisoned(table string)
}

// MemRecorder is an in-memory Recorder for tests and as a safe default. It is
// concurrency-safe.
type MemRecorder struct {
	mu            sync.Mutex
	backlog       map[string]float64
	oldest        map[string]float64
	poisonedCount map[string]float64 // current count gauge (Collector)
	poisonedTotal map[string]float64 // monotonic counter (drainer)
}

// NewMemRecorder constructs an empty in-memory recorder.
func NewMemRecorder() *MemRecorder {
	return &MemRecorder{
		backlog:       map[string]float64{},
		oldest:        map[string]float64{},
		poisonedCount: map[string]float64{},
		poisonedTotal: map[string]float64{},
	}
}

// SetBacklogDepth records the pending-row gauge for a table.
func (m *MemRecorder) SetBacklogDepth(table string, depth float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backlog[table] = depth
}

// SetOldestPendingAgeSeconds records the oldest-pending-age gauge for a table.
func (m *MemRecorder) SetOldestPendingAgeSeconds(table string, age float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.oldest[table] = age
}

// SetPoisonedCount records the current poisoned-row gauge for a table.
func (m *MemRecorder) SetPoisonedCount(table string, count float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.poisonedCount[table] = count
}

// IncPoisoned increments the monotonic poison counter for a table.
func (m *MemRecorder) IncPoisoned(table string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.poisonedTotal[table]++
}

// BacklogDepth returns the last-recorded backlog gauge (test accessor).
func (m *MemRecorder) BacklogDepth(table string) float64 { return m.read(m.backlog, table) }

// OldestPendingAgeSeconds returns the last-recorded oldest-age gauge (test accessor).
func (m *MemRecorder) OldestPendingAgeSeconds(table string) float64 { return m.read(m.oldest, table) }

// PoisonedCount returns the last-recorded poisoned-row gauge (test accessor).
func (m *MemRecorder) PoisonedCount(table string) float64 { return m.read(m.poisonedCount, table) }

// PoisonedTotal returns the monotonic poison counter (test accessor).
func (m *MemRecorder) PoisonedTotal(table string) float64 { return m.read(m.poisonedTotal, table) }

func (m *MemRecorder) read(src map[string]float64, table string) float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return src[table]
}

var _ Recorder = (*MemRecorder)(nil)

// CollectorConfig parameterises a Collector.
type CollectorConfig struct {
	// Table — full outbox table name (`<schema>.<table>`), used both for the
	// scan query and as the metric `table` label.
	Table string
	// MaxAttempts — poison threshold; a pending row with attempt_count >=
	// MaxAttempts is counted as poisoned. Default 10 (matches drainer default).
	MaxAttempts int
	// Interval — how often Run scans (default 15s). Scan can also be called
	// directly (tests / on-demand).
	Interval time.Duration
}

func (c CollectorConfig) withDefaults() CollectorConfig {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 10
	}
	if c.Interval <= 0 {
		c.Interval = 15 * time.Second
	}
	return c
}

// Collector scans one outbox table and feeds the gauges into a Recorder.
type Collector struct {
	pool *pgxpool.Pool
	rec  Recorder
	cfg  CollectorConfig
}

// NewCollector constructs a Collector. pool/rec must be non-nil; Table required.
func NewCollector(pool *pgxpool.Pool, rec Recorder, cfg CollectorConfig) *Collector {
	return &Collector{pool: pool, rec: rec, cfg: cfg.withDefaults()}
}

// Scan runs one observation pass: it reads backlog depth, oldest pending age and
// the current poisoned-row count from the outbox table and records them. It does
// NOT mutate the table. The table name is a trusted literal supplied by the
// composition root (same contract as drainer.Config.Table) — not user input.
func (c *Collector) Scan(ctx context.Context) error {
	if c.pool == nil || c.rec == nil {
		return errors.New("metrics.Collector.Scan: pool and recorder required")
	}
	if c.cfg.Table == "" {
		return errors.New("metrics.Collector.Scan: Table required")
	}

	// One round-trip: pending count, oldest-pending age (seconds), poisoned count.
	q := fmt.Sprintf(`
		SELECT
		    count(*) FILTER (WHERE sent_at IS NULL)                                              AS backlog,
		    COALESCE(EXTRACT(EPOCH FROM (now() - min(created_at) FILTER (WHERE sent_at IS NULL))), 0) AS oldest_age,
		    count(*) FILTER (WHERE sent_at IS NULL AND attempt_count >= $1)                      AS poisoned
		FROM %s
	`, c.cfg.Table)

	var backlog, poisoned int64
	var oldestAge float64
	if err := c.pool.QueryRow(ctx, q, c.cfg.MaxAttempts).Scan(&backlog, &oldestAge, &poisoned); err != nil {
		return fmt.Errorf("metrics.Collector.Scan %s: %w", c.cfg.Table, err)
	}

	c.rec.SetBacklogDepth(c.cfg.Table, float64(backlog))
	c.rec.SetOldestPendingAgeSeconds(c.cfg.Table, oldestAge)
	c.rec.SetPoisonedCount(c.cfg.Table, float64(poisoned))
	return nil
}

// Run scans on c.cfg.Interval until ctx is cancelled. Scan errors are returned
// to the caller's logger via the optional onErr callback (nil → swallowed; the
// loop never dies on a transient scan error). Run blocks until ctx.Done().
func (c *Collector) Run(ctx context.Context, onErr func(error)) {
	tick := time.NewTicker(c.cfg.Interval)
	defer tick.Stop()
	// Immediate first scan so metrics are populated before the first tick.
	if err := c.Scan(ctx); err != nil && onErr != nil {
		onErr(err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := c.Scan(ctx); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}
