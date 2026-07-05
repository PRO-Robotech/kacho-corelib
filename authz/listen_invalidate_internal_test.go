// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz

import (
	"context"
	"testing"
	"time"
)

// seedListObjects populates the ListObjects cache for subjectID via a fake
// client so InvalidateBySubject has something to drop.
func seedListObjects(t *testing.T, subjectID string) *ListObjectsService {
	t.Helper()
	svc := NewListObjectsService(
		ListObjectsClientFunc(func(context.Context, ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{ResourceIDs: []string{"enp_a", "enp_b"}}, nil
		}),
		ListObjectsConfig{TTL: 5 * time.Second, ServiceName: "test"},
	)
	if _, err := svc.ListAllowedIDs(context.Background(), subjectID, "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{}); err != nil {
		t.Fatalf("seed ListObjects: %v", err)
	}
	if s, _ := svc.Size(); s == 0 {
		t.Fatalf("ListObjects cache not seeded")
	}
	return svc
}

// TestListenInvalidator_InvalidateBySubject_DispatchesBoth — revoke propagation
// must hit BOTH the Check-cache and the ListObjects-cache for the subject; a
// dropped dispatch means a revoked grant keeps being honoured from a stale cache.
func TestListenInvalidator_InvalidateBySubject_DispatchesBoth(t *testing.T) {
	const subject = "user:usr_alice"
	cache := NewCache(5 * time.Second)
	cache.SetAllowed(subject, "viewer", "vpc_network", "enp_a")
	lo := seedListObjects(t, subject)

	li := &ListenInvalidator{Cache: cache, ListObjects: lo}
	li.invalidateBySubject(subject)

	if _, ok := cache.Get(subject, "viewer", "vpc_network", "enp_a"); ok {
		t.Fatalf("Check-cache entry survived revoke invalidation")
	}
	if s, _ := lo.Size(); s != 0 {
		t.Fatalf("ListObjects cache entry survived revoke invalidation (subjects=%d)", s)
	}
}

// TestListenInvalidator_InvalidateAll_DispatchesBoth — empty NOTIFY payload
// routes to invalidateAll on both caches.
func TestListenInvalidator_InvalidateAll_DispatchesBoth(t *testing.T) {
	cache := NewCache(5 * time.Second)
	cache.SetAllowed("user:usr_alice", "viewer", "vpc_network", "enp_a")
	cache.SetAllowed("user:usr_bob", "viewer", "vpc_network", "enp_b")
	lo := seedListObjects(t, "user:usr_alice")

	li := &ListenInvalidator{Cache: cache, ListObjects: lo}
	li.invalidateAll()

	if s, e := cache.Size(); s != 0 || e != 0 {
		t.Fatalf("Check-cache not fully cleared: subjects=%d entries=%d", s, e)
	}
	if s, _ := lo.Size(); s != 0 {
		t.Fatalf("ListObjects cache not fully cleared: subjects=%d", s)
	}
}

// TestListenInvalidator_NilSafe — dispatch helpers must not panic when only one
// (or neither) cache is wired.
func TestListenInvalidator_NilSafe(t *testing.T) {
	(&ListenInvalidator{}).invalidateBySubject("user:usr_x") // both nil — no panic
	(&ListenInvalidator{}).invalidateAll()

	cache := NewCache(5 * time.Second)
	cache.SetAllowed("user:usr_x", "viewer", "vpc_network", "enp_a")
	li := &ListenInvalidator{Cache: cache} // ListObjects nil
	li.invalidateBySubject("user:usr_x")
	if _, ok := cache.Get("user:usr_x", "viewer", "vpc_network", "enp_a"); ok {
		t.Fatalf("Cache-only invalidation did not drop entry")
	}
}

// TestListenInvalidator_Run_BothNilErrors — Run must refuse to start with no
// cache to invalidate (misconfiguration guard).
func TestListenInvalidator_Run_BothNilErrors(t *testing.T) {
	li := &ListenInvalidator{ConnString: "postgres://unused"}
	if err := li.Run(context.Background()); err == nil {
		t.Fatalf("expected error when both Cache and ListObjects are nil")
	}
}
