// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz

import (
	"testing"
	"time"
)

// TestCache_EvictIfStale_GuardsFreshEntry — регрессия на clobber-race в
// lazy-eviction: Get отпускает RLock, увидев expired-entry, затем берёт write
// lock и удаляет ключ. Между RUnlock и Lock конкурентный SetAllowed мог
// записать свежий entry — безусловный delete выкидывал бы его (потеря валидного
// positive-результата → лишний Check round-trip в kacho-iam).
//
// evictIfStale должен удалять запись ТОЛЬКО если сохранённый expiresAt всё ещё
// равен наблюдённому (stale) значению. Здесь мы эмулируем interleave: кладём
// свежий entry, затем зовём evictIfStale со СТАРЫМ (observed-stale) expiresAt —
// свежая запись обязана уцелеть.
func TestCache_EvictIfStale_GuardsFreshEntry(t *testing.T) {
	base := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	c := NewCache(5 * time.Second)
	c.SetNowFunc(func() time.Time { return base })

	key := entryKey{"viewer", "vpc_network", "enp_x"}
	// Свежий entry: expiresAt = base+5s.
	c.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_x")

	// Наблюдённый (в Get) expired-entry имел старый expiresAt — эмулируем, что
	// параллельный SetAllowed уже перезаписал его свежим значением.
	observedStale := base.Add(-1 * time.Second)
	c.evictIfStale("user:usr_alice", key, observedStale)

	if _, ok := c.Get("user:usr_alice", "viewer", "vpc_network", "enp_x"); !ok {
		t.Fatalf("fresh entry was clobbered by stale eviction; expected it to survive")
	}
}

// TestCache_EvictIfStale_RemovesMatchingStale — если сохранённый expiresAt
// совпадает с наблюдённым stale-значением, запись удаляется (нормальная
// lazy-eviction).
func TestCache_EvictIfStale_RemovesMatchingStale(t *testing.T) {
	base := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	c := NewCache(5 * time.Second)
	c.SetNowFunc(func() time.Time { return base })

	key := entryKey{"viewer", "vpc_network", "enp_x"}
	c.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_x")

	// Читаем реальный сохранённый expiresAt.
	c.mu.RLock()
	stored := c.store["user:usr_alice"][key].expiresAt
	c.mu.RUnlock()

	c.evictIfStale("user:usr_alice", key, stored)

	c.mu.RLock()
	_, still := c.store["user:usr_alice"]
	c.mu.RUnlock()
	if still {
		t.Fatalf("matching-stale entry must be evicted (subject map should be empty and dropped)")
	}
}
