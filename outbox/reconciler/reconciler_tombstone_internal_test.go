// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package reconciler

import (
	"context"
	"testing"
	"time"
)

// fakeEnumerator / fakeRegistry — минимальные адаптеры для GCOrphans-путей, не
// касающихся пула (grace не истёк / ресурс присутствует).
type fakeEnumerator struct {
	exists map[string]bool // key = id
}

func (f *fakeEnumerator) ListResources(context.Context) ([]ResourceRow, error) { return nil, nil }
func (f *fakeEnumerator) ResourceExists(_ context.Context, _, id string) (bool, error) {
	return f.exists[id], nil
}

// NOTE: table-name quoting (`sanitizeTable`) больше не дублируется в reconciler —
// единый source-of-truth outbox.SanitizeTable покрыт outbox.TestSanitizeTable
// (plain / schema-qualified / injection / embedded-quote).

type fakeRegistry struct{ tuples []RegisteredTuple }

func (f *fakeRegistry) ListRegistered(context.Context) ([]RegisteredTuple, error) {
	return f.tuples, nil
}

// TestGCOrphans_PrunesStaleTombstones — регресс на утечку firstSeenAbsent:
// tombstone для id, который покинул ListRegistered ЛЮБЫМ путём кроме corelib-GC
// (напр. out-of-band unregister), обязан быть вычищен на следующем проходе, а не
// висеть до конца жизни процесса (CWE-401).
func TestGCOrphans_PrunesStaleTombstones(t *testing.T) {
	r := &Reconciler{
		cfg:             Config{GraceWindow: time.Hour}.withDefaults(),
		ad:              Adapters{Enumerator: &fakeEnumerator{exists: map[string]bool{"A": false}}, Registry: &fakeRegistry{tuples: []RegisteredTuple{{Kind: "net", ID: "A"}}}},
		firstSeenAbsent: map[string]time.Time{},
	}
	// Стейл-tombstone для id "B", которого больше нет в ListRegistered.
	r.firstSeenAbsent["B"] = time.Now().Add(-2 * time.Hour)

	// "A" отсутствует как ресурс, grace=1h → gcOne вернёт рано (не тронет пул).
	if _, err := r.GCOrphans(context.Background()); err != nil {
		t.Fatalf("GCOrphans err: %v", err)
	}

	r.mu.Lock()
	_, hasB := r.firstSeenAbsent["B"]
	_, hasA := r.firstSeenAbsent["A"]
	n := len(r.firstSeenAbsent)
	r.mu.Unlock()

	if hasB {
		t.Fatalf("stale tombstone B must be pruned (leaked entry)")
	}
	if !hasA {
		t.Fatalf("tombstone A (absent candidate this pass) must be recorded")
	}
	if n != 1 {
		t.Fatalf("firstSeenAbsent must be bounded to current candidate set; got %d entries", n)
	}
}
