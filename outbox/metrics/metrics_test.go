// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package metrics_test

// Unit + integration tests for the outbox metrics package.
//
// They verify that backlog_depth / oldest_pending_age / poisoned_total are
// exposed per outbox table.
//
// The metrics layer is split into:
//   - metrics.Recorder — a small interface (the prometheus client is wired by
//     the service at the composition root; corelib stays dependency-light and
//     testable via an in-memory recorder).
//   - metrics.MemRecorder — the in-memory test/default implementation.
//   - metrics.Collector — periodically scans an outbox table for backlog_depth
//     and oldest_pending_age_seconds and feeds them into the Recorder.
//
// The poisoned_total counter is incremented by the drainer when it poisons a
// row (verified end-to-end in the drainer integration tests); here we verify
// the Collector's DB-scan gauges + the Recorder contract.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/PRO-Robotech/kacho-corelib/outbox/metrics"
)

const schemaDDL = `
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
    attempt_count integer      NOT NULL DEFAULT 0
);
`

func setupPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() || os.Getenv("SKIP_INTEGRATION") == "1" {
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
	_, err = pool.Exec(ctx, schemaDDL)
	require.NoError(t, err)
	return pool
}

// Test_MemRecorder_Contract — the in-memory recorder records the three series
// keyed by table label.
func Test_MemRecorder_Contract(t *testing.T) {
	t.Parallel()
	const tbl = "kacho_apps.fga_register_outbox"
	rec := metrics.NewMemRecorder()

	rec.SetBacklogDepth(tbl, 3)
	rec.SetOldestPendingAgeSeconds(tbl, 12.5)
	rec.IncPoisoned(tbl)
	rec.IncPoisoned(tbl)

	assert.Equal(t, float64(3), rec.BacklogDepth(tbl))
	assert.Equal(t, 12.5, rec.OldestPendingAgeSeconds(tbl))
	assert.Equal(t, float64(2), rec.PoisonedTotal(tbl),
		"poisoned_total is a historic counter (monotonic)")
}

// Test_1_4_23_CollectorScan_BacklogAndOldest — Collector.Scan reports backlog
// depth and oldest-pending age over the outbox table.
//
// 3 pending intents + 1 already-sent + 1 poisoned-looking row → Collector.Scan
// reports backlog_depth==3 (only pending), oldest_pending_age_seconds>0.
func Test_1_4_23_CollectorScan_BacklogAndOldest(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupPG(t)
	const tbl = "kacho_apps.fga_register_outbox"

	// 3 pending (sent_at NULL), one created clearly in the past.
	_, err := pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type, created_at)
		 VALUES ('fga.register', now() - interval '30 seconds')`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type) VALUES ('fga.register'), ('fga.register')`)
	require.NoError(t, err)
	// 1 already sent — must NOT count toward backlog.
	_, err = pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type, sent_at) VALUES ('fga.register', now())`)
	require.NoError(t, err)

	rec := metrics.NewMemRecorder()
	col := metrics.NewCollector(pool, rec, metrics.CollectorConfig{
		Table:       tbl,
		MaxAttempts: 10,
	})
	require.NoError(t, col.Scan(ctx))

	assert.Equal(t, float64(3), rec.BacklogDepth(tbl),
		"backlog_depth counts only pending (sent_at NULL) rows")
	assert.Greater(t, rec.OldestPendingAgeSeconds(tbl), float64(20),
		"oldest_pending_age_seconds reflects the oldest pending row (~30s)")
}

// Test_1_4_23_CollectorScan_PoisonedCount — Collector also reports the count of
// poisoned (attempt_count >= MaxAttempts AND sent_at NULL) rows so an operator
// can alert without waiting for a fresh poison-increment.
func Test_1_4_23_CollectorScan_PoisonedCount(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool := setupPG(t)
	const tbl = "kacho_apps.fga_register_outbox"

	// 1 poisoned: attempt_count == MaxAttempts, sent_at NULL.
	_, err := pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type, attempt_count, last_error)
		 VALUES ('fga.register', 10, 'permanent')`)
	require.NoError(t, err)
	// 1 pending (not poisoned).
	_, err = pool.Exec(ctx,
		`INSERT INTO kacho_apps.fga_register_outbox (event_type) VALUES ('fga.register')`)
	require.NoError(t, err)

	rec := metrics.NewMemRecorder()
	col := metrics.NewCollector(pool, rec, metrics.CollectorConfig{Table: tbl, MaxAttempts: 10})
	require.NoError(t, col.Scan(ctx))

	assert.Equal(t, float64(1), rec.PoisonedCount(tbl),
		"collector sets the poisoned-count gauge to the current count of poisoned rows")
	assert.Equal(t, float64(2), rec.BacklogDepth(tbl),
		"poisoned rows are still pending → counted in backlog (sent_at NULL)")
}
