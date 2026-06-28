// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz_test

import (
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

func TestCache_DifferentRelationsIsolated(t *testing.T) {
	c := authz.NewCache(5 * time.Second)
	c.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_a")
	// editor wasn't set → must be miss
	_, ok := c.Get("user:usr_alice", "editor", "vpc_network", "enp_a")
	if ok {
		t.Fatalf("expected miss for different relation")
	}
}
