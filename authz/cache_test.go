// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/authz"
)

func TestCache_GetMiss(t *testing.T) {
	c := authz.NewCache(5 * time.Second)
	_, ok := c.Get("user:usr_alice", "viewer", "vpc_network", "enp_x")
	if ok {
		t.Fatalf("expected miss on empty cache")
	}
}

func TestCache_SetAllowedGetHit(t *testing.T) {
	c := authz.NewCache(5 * time.Second)
	c.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_x")
	allowed, ok := c.Get("user:usr_alice", "viewer", "vpc_network", "enp_x")
	if !ok {
		t.Fatalf("expected hit")
	}
	if !allowed {
		t.Fatalf("expected allowed=true")
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	c := authz.NewCache(5 * time.Second)
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	c.SetNowFunc(func() time.Time { return now })
	c.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_x")

	// Advance now by 4s — still within TTL.
	now = now.Add(4 * time.Second)
	_, ok := c.Get("user:usr_alice", "viewer", "vpc_network", "enp_x")
	if !ok {
		t.Fatalf("expected hit at 4s")
	}

	// Advance by 2s more (total 6s) — expired.
	now = now.Add(2 * time.Second)
	_, ok = c.Get("user:usr_alice", "viewer", "vpc_network", "enp_x")
	if ok {
		t.Fatalf("expected miss after 6s")
	}
}

func TestCache_InvalidateBySubject(t *testing.T) {
	c := authz.NewCache(5 * time.Second)
	c.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_a")
	c.SetAllowed("user:usr_alice", "editor", "project", "prj_dev")
	c.SetAllowed("user:usr_bob", "viewer", "vpc_network", "enp_a")

	// Invalidate alice — should remove both her entries, keep bob.
	c.InvalidateBySubject("user:usr_alice")

	_, okAliceViewer := c.Get("user:usr_alice", "viewer", "vpc_network", "enp_a")
	_, okAliceEditor := c.Get("user:usr_alice", "editor", "project", "prj_dev")
	_, okBob := c.Get("user:usr_bob", "viewer", "vpc_network", "enp_a")

	if okAliceViewer || okAliceEditor {
		t.Fatalf("expected alice entries invalidated")
	}
	if !okBob {
		t.Fatalf("expected bob entry preserved")
	}
}

func TestCache_InvalidateAll(t *testing.T) {
	c := authz.NewCache(5 * time.Second)
	c.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_a")
	c.SetAllowed("user:usr_bob", "viewer", "vpc_network", "enp_a")
	c.InvalidateAll()

	subjects, entries := c.Size()
	if subjects != 0 || entries != 0 {
		t.Fatalf("expected empty cache; got subjects=%d entries=%d", subjects, entries)
	}
}

func TestCache_DefaultTTL(t *testing.T) {
	c := authz.NewCache(0) // → default 5s
	c.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_a")
	now := time.Now()
	c.SetNowFunc(func() time.Time { return now.Add(4 * time.Second) })
	_, ok := c.Get("user:usr_alice", "viewer", "vpc_network", "enp_a")
	if !ok {
		t.Fatalf("expected hit (default TTL must be ≥4s)")
	}
}

// TestCache_Concurrent — data-race guard: Get / SetAllowed / InvalidateBySubject
// одновременно из N goroutine на пересекающихся ключах. Прогоняется под -race;
// падает, если какой-либо путь доступа к store не защищён локом. Ключевой момент —
// lazy-eviction в Get (evictIfStale) конкурирует с SetAllowed на тех же ключах.
func TestCache_Concurrent(t *testing.T) {
	c := authz.NewCache(1 * time.Millisecond) // короткий TTL → частая lazy-eviction
	const goroutines = 32
	const iterations = 500
	subjects := []string{"user:usr_a", "user:usr_b", "user:usr_c"}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				subj := subjects[(g+i)%len(subjects)]
				obj := fmt.Sprintf("enp_%d", i%8)
				switch i % 4 {
				case 0:
					c.SetAllowed(subj, "viewer", "vpc_network", obj)
				case 1:
					c.Get(subj, "viewer", "vpc_network", obj)
				case 2:
					c.Get(subj, "viewer", "vpc_network", obj)
				case 3:
					c.InvalidateBySubject(subj)
				}
			}
		}(g)
	}
	wg.Wait()

	// Sanity: кэш всё ещё работоспособен после конкурентной нагрузки.
	c.SetAllowed("user:usr_final", "viewer", "vpc_network", "enp_final")
	if _, ok := c.Get("user:usr_final", "viewer", "vpc_network", "enp_final"); !ok {
		t.Fatalf("cache unusable after concurrent load")
	}
}

// TestCache_LazyEvictionKeepsFreshEntry — публичный сценарий guard'а из
// evictIfStale: после того как Get увидел просроченную запись, конкурентная
// перезапись свежим SetAllowed не должна быть выкинута последующей lazy-eviction.
func TestCache_LazyEvictionKeepsFreshEntry(t *testing.T) {
	base := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	now := base
	c := authz.NewCache(5 * time.Second)
	c.SetNowFunc(func() time.Time { return now })

	c.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_x") // expiry base+5s

	// Продвигаем время за TTL — запись просрочена.
	now = base.Add(6 * time.Second)
	// Свежая перезапись (expiry now+5s) — эмулирует конкурентный писатель.
	c.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_x")

	// Get не должен уронить свежую запись (её expiresAt в будущем относительно now).
	if _, ok := c.Get("user:usr_alice", "viewer", "vpc_network", "enp_x"); !ok {
		t.Fatalf("fresh entry evicted after re-set past TTL")
	}
}

// TestCache_MaxEntriesBound — CWE-770: под enumeration-нагрузкой (много distinct
// object-id в пределах TTL) число entry не должно расти безгранично. При потолке
// insert нового ключа обязан удержать общий размер ≤ maxEntries.
func TestCache_MaxEntriesBound(t *testing.T) {
	const limit = 100
	c := authz.NewCacheWithLimit(5*time.Second, limit)
	// Вставляем на порядок больше distinct entry, чем потолок, в пределах TTL
	// (время не двигаем → ничего не просрочено; спасает только hard-cap эвикция).
	for i := 0; i < limit*20; i++ {
		c.SetAllowed("user:usr_alice", "viewer", "vpc_network", fmt.Sprintf("enp_%d", i))
	}
	_, entries := c.Size()
	if entries > limit {
		t.Fatalf("cache exceeded max entries: got %d, want <= %d", entries, limit)
	}
}

// TestCache_MaxEntriesEvictsExpiredFirst — при достижении потолка сперва вычищаются
// просроченные записи; если их достаточно, свежие сохраняются без произвольной
// эвикции.
func TestCache_MaxEntriesEvictsExpiredFirst(t *testing.T) {
	const limit = 10
	base := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	now := base
	c := authz.NewCacheWithLimit(5*time.Second, limit)
	c.SetNowFunc(func() time.Time { return now })

	// Заполняем ровно до потолка.
	for i := 0; i < limit; i++ {
		c.SetAllowed("user:usr_stale", "viewer", "vpc_network", fmt.Sprintf("old_%d", i))
	}
	// Просрочиваем всё.
	now = base.Add(6 * time.Second)
	// Новый insert → потолок достигнут, но все старые просрочены → чистятся.
	c.SetAllowed("user:usr_fresh", "viewer", "vpc_network", "new_0")

	_, entries := c.Size()
	if entries > limit {
		t.Fatalf("cache exceeded max entries after expiry sweep: got %d, want <= %d", entries, limit)
	}
	if _, ok := c.Get("user:usr_fresh", "viewer", "vpc_network", "new_0"); !ok {
		t.Fatalf("fresh entry must survive; expired entries should have been reclaimed first")
	}
}

func TestCache_DifferentRelationsIsolated(t *testing.T) {
	c := authz.NewCache(5 * time.Second)
	c.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_a")
	// editor wasn't set → must be miss
	_, ok := c.Get("user:usr_alice", "editor", "vpc_network", "enp_a")
	if ok {
		t.Fatalf("expected miss for different relation")
	}
}
