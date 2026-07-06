// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz_test

// Integration test for authz.ListenInvalidator — the revoke-propagation path.
//
// An IAM binding-revoke fires `pg_notify('kacho_iam_subjects', <subject_id>)`;
// ListenInvalidator must turn that NOTIFY into a cache eviction so a revoked
// grant stops being honoured before its 5s TTL. This test drives a real
// Postgres testcontainer, seeds a positive Check-cache entry, issues the NOTIFY,
// and asserts the entry disappears.
//
// testcontainers-go Postgres, one container per test (no shared state).

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/PRO-Robotech/kacho-corelib/authz"
)

func startPG(t *testing.T) (pool *pgxpool.Pool, dsn string) {
	t.Helper()
	// Единый с остальными testcontainer-хелперами gate: под -short / SKIP_INTEGRATION
	// Postgres+Docker-зависимый тест скипается (быстрый ci-джоб build-vet-test), а
	// без флага — гоняется (integration-джоб). Иначе на раннере без Docker `go test
	// -short ./...` падал бы вместо пропуска (см. operations/operations_test.go и др.).
	if testing.Short() || os.Getenv("SKIP_INTEGRATION") == "1" {
		t.Skip("integration tests skipped (SKIP_INTEGRATION=1)")
	}
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

	dsn, err = ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err = pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool, dsn
}

// notifyUntil re-issues pg_notify on channel with payload until check() returns
// true or the deadline elapses. Re-issuing covers the race where the LISTEN
// session is not yet established when the first NOTIFY commits (such a NOTIFY is
// simply not delivered — only sessions that ran LISTEN before commit receive it).
func notifyUntil(t *testing.T, pool *pgxpool.Pool, channel, payload string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, err := pool.Exec(context.Background(), "SELECT pg_notify($1, $2)", channel, payload)
		require.NoError(t, err)
		time.Sleep(100 * time.Millisecond)
		if check() {
			return
		}
	}
	t.Fatalf("condition not met within deadline after NOTIFY %q payload=%q", channel, payload)
}

func TestListenInvalidator_NotifyEvictsSubject(t *testing.T) {
	pool, dsn := startPG(t)
	_ = pool

	const subject = "user:usr_alice"
	cache := authz.NewCache(60 * time.Second) // long TTL → only NOTIFY can evict
	cache.SetAllowed(subject, "viewer", "vpc_network", "enp_a")
	cache.SetAllowed("user:usr_bob", "viewer", "vpc_network", "enp_b")

	li := &authz.ListenInvalidator{
		ConnString: dsn,
		Channel:    "kacho_iam_subjects",
		Cache:      cache,
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- li.Run(runCtx) }()

	notifyUntil(t, pool, "kacho_iam_subjects", subject, func() bool {
		_, ok := cache.Get(subject, "viewer", "vpc_network", "enp_a")
		return !ok
	})

	// Non-targeted subject must remain cached.
	if _, ok := cache.Get("user:usr_bob", "viewer", "vpc_network", "enp_b"); !ok {
		t.Fatalf("unrelated subject entry was evicted")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}

func TestListenInvalidator_EmptyPayloadEvictsAll(t *testing.T) {
	pool, dsn := startPG(t)

	cache := authz.NewCache(60 * time.Second)
	for i := 0; i < 5; i++ {
		cache.SetAllowed(fmt.Sprintf("user:usr_%d", i), "viewer", "vpc_network", "enp_a")
	}

	li := &authz.ListenInvalidator{
		ConnString: dsn,
		Channel:    "kacho_iam_subjects",
		Cache:      cache,
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- li.Run(runCtx) }()

	notifyUntil(t, pool, "kacho_iam_subjects", "", func() bool {
		s, e := cache.Size()
		return s == 0 && e == 0
	})

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Run did not return after ctx cancel")
	}
}
