// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeClient — счетчик вызовов + программируемые ответы.
type fakeClient struct {
	calls atomic.Int64
	fn    func(ctx context.Context, req ListObjectsRequest) (ListObjectsResponse, error)
}

func (f *fakeClient) ListObjects(ctx context.Context, req ListObjectsRequest) (ListObjectsResponse, error) {
	f.calls.Add(1)
	if f.fn == nil {
		return ListObjectsResponse{}, nil
	}
	return f.fn(ctx, req)
}

func newSvc(t *testing.T, client ListObjectsClient) *ListObjectsService {
	t.Helper()
	return NewListObjectsService(client, ListObjectsConfig{
		TTL:             time.Second,
		MaxEntries:      100,
		MaxResults:      10000,
		FollowupTimeout: time.Second,
		AuthzModelID:    "m_test_v1",
		ServiceName:     "kacho-test",
	})
}

// Cache miss → call → cache hit on second call.
func TestListObjects_CacheMissThenHit(t *testing.T) {
	client := &fakeClient{
		fn: func(_ context.Context, _ ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{ResourceIDs: []string{"net-1", "net-2"}}, nil
		},
	}
	svc := newSvc(t, client)

	ids, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(ids) != 2 || ids[0] != "net-1" || ids[1] != "net-2" {
		t.Fatalf("ids = %v", ids)
	}
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("first call: client calls = %d, want 1", got)
	}

	// Second call within TTL — cache hit.
	ids2, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(ids2) != 2 {
		t.Fatalf("ids2 = %v", ids2)
	}
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("second call: client calls = %d, want 1 (cache hit)", got)
	}
}

// Client error → ErrUnavailable wrapped.
func TestListObjects_ClientErrorReturnsUnavailable(t *testing.T) {
	upstreamErr := errors.New("connection refused")
	client := &fakeClient{
		fn: func(_ context.Context, _ ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{}, upstreamErr
		},
	}
	svc := newSvc(t, client)

	ids, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, generic infra error must NOT wrap ErrPermissionDenied", err)
	}
	if ids != nil {
		t.Fatalf("ids = %v, want nil on err", ids)
	}
}

// PermissionDenied от kacho-iam НЕ должен wrap'аться в ErrUnavailable
// (caller получит 503 вместо 403 — UI/SDK не отличат "нет прав" от "сервис мертв",
// retry-логика сделает хуже). До fix'а listobjects.go блиндово оборачивал любой
// upstream error в ErrUnavailable через `fmt.Errorf("%w: %v", ErrUnavailable, err)`,
// gRPC-код терялся.
func TestListObjects_PermissionDeniedFromIAM_ReturnsPermissionDeniedSentinel(t *testing.T) {
	pdErr := status.Error(codes.PermissionDenied, "permission denied")
	client := &fakeClient{
		fn: func(_ context.Context, _ ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{}, pdErr
		},
	}
	svc := newSvc(t, client)

	ids, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{})
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied sentinel", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, PermissionDenied MUST NOT wrap ErrUnavailable (caller's 503-vs-403 split)", err)
	}
	if ids != nil {
		t.Fatalf("ids = %v, want nil on err", ids)
	}
}

// Empty grant → empty slice, NOT PermissionDenied.
func TestListObjects_EmptyGrant(t *testing.T) {
	client := &fakeClient{
		fn: func(_ context.Context, _ ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{ResourceIDs: nil}, nil
		},
	}
	svc := newSvc(t, client)

	ids, err := svc.ListAllowedIDs(context.Background(), "user:usr_bob", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("ids = %v, want empty", ids)
	}

	// Second call — cached (no extra client call).
	if _, err := svc.ListAllowedIDs(context.Background(), "user:usr_bob", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{}); err != nil {
		t.Fatalf("err2: %v", err)
	}
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("client calls = %d, want 1 (empty cached)", got)
	}
}

// InvalidateBySubject — cache miss after invalidation.
func TestListObjects_InvalidateBySubject(t *testing.T) {
	client := &fakeClient{
		fn: func(_ context.Context, _ ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{ResourceIDs: []string{"net-1"}}, nil
		},
	}
	svc := newSvc(t, client)

	if _, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("after first call: %d", got)
	}

	svc.InvalidateBySubject("user:usr_alice")

	if _, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := client.calls.Load(); got != 2 {
		t.Fatalf("after invalidate: client calls = %d, want 2", got)
	}
}

// TTL expiry → next call re-fetches. Детерминированно: fake-clock вместо
// time.Sleep (см. listObjectsCache.setNowFunc) — не зависит от wall-clock, не
// флейкует на нагруженном CI-раннере.
func TestListObjects_TTLExpiry(t *testing.T) {
	client := &fakeClient{
		fn: func(_ context.Context, _ ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{ResourceIDs: []string{"net-x"}}, nil
		},
	}
	const ttl = 50 * time.Millisecond
	svc := NewListObjectsService(client, ListObjectsConfig{
		TTL:        ttl,
		MaxEntries: 100,
		MaxResults: 10000,
	})

	base := time.Unix(1_700_000_000, 0)
	cur := base
	svc.cache.setNowFunc(func() time.Time { return cur })

	if _, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("after first call: %d", got)
	}

	// В пределах TTL — cache hit, повторного client-вызова нет.
	cur = base.Add(ttl - time.Nanosecond)
	if _, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("within TTL: expected cache hit (calls=1), got %d", got)
	}

	// Перешагнули TTL — запись просрочена, client вызывается снова.
	cur = base.Add(ttl + time.Nanosecond)
	if _, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := client.calls.Load(); got != 2 {
		t.Fatalf("after expiry: client calls = %d, want 2", got)
	}
}

// Deterministic ordering preserved (we return what client returned).
func TestListObjects_ResultStable(t *testing.T) {
	client := &fakeClient{
		fn: func(_ context.Context, _ ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{ResourceIDs: []string{"net-c", "net-a", "net-b"}}, nil
		},
	}
	svc := newSvc(t, client)

	ids, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[0] != "net-c" || ids[1] != "net-a" || ids[2] != "net-b" {
		t.Fatalf("ordering mismatch: %v", ids)
	}
}

// Pagination — multiple FGA calls accumulated.
func TestListObjects_Pagination(t *testing.T) {
	page := 0
	client := &fakeClient{
		fn: func(_ context.Context, req ListObjectsRequest) (ListObjectsResponse, error) {
			switch page {
			case 0:
				page++
				return ListObjectsResponse{ResourceIDs: []string{"a", "b"}, NextPageToken: "tok-2"}, nil
			case 1:
				if req.PageToken != "tok-2" {
					t.Errorf("page 2: page_token = %q, want tok-2", req.PageToken)
				}
				page++
				return ListObjectsResponse{ResourceIDs: []string{"c", "d"}, NextPageToken: ""}, nil
			}
			return ListObjectsResponse{}, errors.New("unexpected page call")
		},
	}
	svc := newSvc(t, client)

	ids, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 4 {
		t.Fatalf("ids = %v, want 4 items", ids)
	}
}

// TestListObjects_PerPageDeadline — FollowupTimeout bounds ONE ListObjects RPC
// call (field doc), so it MUST be applied per page, not once across the whole
// pagination loop. A healthy but multi-page peer whose per-page work fits the
// budget must NOT be spuriously cut off. With a single shared deadline the 2nd
// page inherits a shrunken remaining budget and fails closed to ErrUnavailable
// even though the peer is fine.
//
// Deterministic: each page-call races its own per-page work against ctx.Done.
// Under a shared budget the 2nd call sees ctx already near-expired → ctx.Done
// wins → DeadlineExceeded. Under a per-call deadline each call gets the full
// budget → work wins → success.
func TestListObjects_PerPageDeadline(t *testing.T) {
	const followup = 300 * time.Millisecond
	const perPageWork = 200 * time.Millisecond // < followup, so each page fits its own budget

	page := 0
	client := &fakeClient{
		fn: func(ctx context.Context, req ListObjectsRequest) (ListObjectsResponse, error) {
			select {
			case <-time.After(perPageWork):
			case <-ctx.Done():
				return ListObjectsResponse{}, status.Error(codes.DeadlineExceeded, "ctx deadline exceeded mid-pagination")
			}
			switch page {
			case 0:
				page++
				return ListObjectsResponse{ResourceIDs: []string{"a", "b"}, NextPageToken: "tok-2"}, nil
			case 1:
				page++
				return ListObjectsResponse{ResourceIDs: []string{"c", "d"}, NextPageToken: ""}, nil
			}
			return ListObjectsResponse{}, errors.New("unexpected page call")
		},
	}
	svc := NewListObjectsService(client, ListObjectsConfig{
		TTL:             time.Second,
		MaxEntries:      100,
		MaxResults:      10000,
		FollowupTimeout: followup,
		AuthzModelID:    "m_test_v1",
		ServiceName:     "kacho-test",
	})

	ids, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{})
	if err != nil {
		t.Fatalf("multi-page list of a healthy peer failed closed: %v", err)
	}
	if len(ids) != 4 {
		t.Fatalf("ids = %v, want 4 items across 2 pages", ids)
	}
}

// TestListObjects_PaginationPageBound — defense-in-depth: an adversarial/buggy
// peer that returns a perpetual non-empty (ever-changing) NextPageToken with
// pages that never accumulate to maxResults must NOT spin the handler goroutine
// forever hammering FGA. The loop must be bounded by the number of pages we
// could ever need to fill maxResults.
//
// The fake has a hard safety stop so a REGRESSION (missing bound) terminates the
// test instead of hanging; the assertion is that the real code stopped WELL
// before that safety stop — i.e. at the maxResults page bound.
func TestListObjects_PaginationPageBound(t *testing.T) {
	const safetyStop = 50 // fake gives up here so a broken loop can't hang the suite
	client := &fakeClient{
		fn: func(_ context.Context, req ListObjectsRequest) (ListObjectsResponse, error) {
			n := 0
			_, _ = fmt.Sscanf(req.PageToken, "tok-%d", &n)
			if n >= safetyStop {
				return ListObjectsResponse{ResourceIDs: nil, NextPageToken: ""}, nil
			}
			// 0 new ids, but a fresh continuation token every time.
			return ListObjectsResponse{ResourceIDs: nil, NextPageToken: fmt.Sprintf("tok-%d", n+1)}, nil
		},
	}
	svc := newSvc(t, client)

	ids, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{MaxResults: 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("ids = %v, want empty", ids)
	}
	// With MaxResults=3 the loop must stop after at most 3 pages, never reaching
	// the fake's safety stop.
	if got := client.calls.Load(); got != 3 {
		t.Fatalf("ListObjects called %d times, want bounded at 3 (maxResults page bound)", got)
	}
}

// TestListObjects_PaginationUnchangedToken — a peer that returns the SAME
// continuation token it was just handed is stuck; the loop must break instead of
// re-requesting the identical page indefinitely.
func TestListObjects_PaginationUnchangedToken(t *testing.T) {
	client := &fakeClient{
		fn: func(_ context.Context, req ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{ResourceIDs: nil, NextPageToken: "stuck"}, nil
		},
	}
	svc := newSvc(t, client) // MaxResults default 10000 — only the unchanged-token guard can stop this

	ids, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{MaxResults: 10000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("ids = %v, want empty", ids)
	}
	if got := client.calls.Load(); got != 2 {
		t.Fatalf("ListObjects called %d times, want 2 (break once the token stops advancing)", got)
	}
}

// Scope hint produces different cache entries.
func TestListObjects_ScopeHintSeparateCache(t *testing.T) {
	client := &fakeClient{
		fn: func(_ context.Context, req ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{ResourceIDs: []string{"id-for-" + req.Action}}, nil
		},
	}
	svc := newSvc(t, client)

	_, _ = svc.ListAllowedIDs(context.Background(), "user:u", "vpc_network", "act", ListAllowedIDsOptions{ScopeHint: "prj_1"})
	_, _ = svc.ListAllowedIDs(context.Background(), "user:u", "vpc_network", "act", ListAllowedIDsOptions{ScopeHint: "prj_2"})

	if got := client.calls.Load(); got != 2 {
		t.Fatalf("different scopes should trigger 2 calls, got %d", got)
	}
}

// nil-client wiring — service returns ErrUnavailable.
func TestListObjects_NilClientFailClosed(t *testing.T) {
	svc := NewListObjectsService(nil, ListObjectsConfig{})
	_, err := svc.ListAllowedIDs(context.Background(), "user:u", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

// SkipCache bypasses cache.
func TestListObjects_SkipCacheBypass(t *testing.T) {
	client := &fakeClient{
		fn: func(_ context.Context, _ ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{ResourceIDs: []string{"x"}}, nil
		},
	}
	svc := newSvc(t, client)

	_, _ = svc.ListAllowedIDs(context.Background(), "user:u", "vpc_network", "act", ListAllowedIDsOptions{})
	_, _ = svc.ListAllowedIDs(context.Background(), "user:u", "vpc_network", "act", ListAllowedIDsOptions{SkipCache: true})

	if got := client.calls.Load(); got != 2 {
		t.Fatalf("SkipCache should bypass cache, got %d calls", got)
	}
}

// TestListObjects_Concurrent — data-race guard для listObjectsCache: одновременно
// из N goroutine прогоняем put (cache-miss на МНОГО distinct subject/scope-ключей,
// заставляя evictIfNeededLocked срабатывать при переполнении maxSize), read-hit
// (тот же ключ), и invalidateBySubject / invalidateAll. Прогоняется под -race;
// падает, если любой путь к store (в т.ч. LRU-eviction или two-level-delete) не
// защищён локом. Раньше тест бил ТОЛЬКО read-hit по одному общему ключу и не
// пересекал put/evict/invalidate — зеркалим более сильный authz.TestCache_Concurrent.
func TestListObjects_Concurrent(t *testing.T) {
	client := &fakeClient{
		fn: func(_ context.Context, _ ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{ResourceIDs: []string{"net-1"}}, nil
		},
	}
	// MaxEntries:100 (newSvc) против сотен distinct ключей → eviction под нагрузкой.
	svc := newSvc(t, client)

	const goroutines = 32
	const iterations = 300
	subjects := []string{"user:usr_a", "user:usr_b", "user:usr_c", "user:usr_d"}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				subj := subjects[(g+i)%len(subjects)]
				// Distinct ScopeHint → distinct cache key → put + рост store →
				// evictIfNeededLocked при превышении maxSize.
				scope := fmt.Sprintf("prj_%d", i%64)
				switch i % 4 {
				case 0, 1:
					_, _ = svc.ListAllowedIDs(context.Background(), subj, "vpc_network", "vpc.networks.read",
						ListAllowedIDsOptions{ScopeHint: scope})
				case 2:
					svc.InvalidateBySubject(subj)
				case 3:
					svc.InvalidateAll()
				}
			}
		}(g)
	}
	wg.Wait()

	// Sanity: кэш работоспособен после конкурентной нагрузки.
	ids, err := svc.ListAllowedIDs(context.Background(), "user:usr_final", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{})
	if err != nil {
		t.Fatalf("cache unusable after concurrent load: %v", err)
	}
	if len(ids) != 1 || ids[0] != "net-1" {
		t.Fatalf("ids = %v, want [net-1]", ids)
	}
}

// Validation: subject == "" → validation error, а НЕ fail-closed ErrUnavailable.
// Пустой subject — это caller-баг (не выставил principal), не недоступность FGA;
// doc-контракт ListAllowedIDs обязан их различать (caller мапит 400 vs 503).
func TestListObjects_ValidationEmptySubject(t *testing.T) {
	svc := newSvc(t, &fakeClient{})
	_, err := svc.ListAllowedIDs(context.Background(), "", "vpc_network", "act", ListAllowedIDsOptions{})
	if err == nil {
		t.Fatal("empty subject must error")
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, empty subject must be a validation error, NOT the ErrUnavailable sentinel", err)
	}
}

// TestListObjectsCache_EvictionBoundsSize — CWE-770-style guard: под churn
// сильно выше maxSize (много distinct cache-key), evictIfNeededLocked обязан
// удержать общий размер ≤ maxSize. Зеркалит authz.TestCache_MaxEntriesBound
// (cache_test.go), применённый к двухуровневому listObjectsCache.
func TestListObjectsCache_EvictionBoundsSize(t *testing.T) {
	const maxSize = 100
	c := newListObjectsCache(maxSize, time.Hour)

	for i := 0; i < maxSize*20; i++ {
		subj := fmt.Sprintf("user:usr_%d", i)
		key := cacheKeyFor(subj, "vpc_network", "act", "", "")
		c.put(key, subj, []string{"id"})
	}

	_, entries := c.size()
	if entries > maxSize {
		t.Fatalf("cache exceeded maxSize: got %d, want <= %d", entries, maxSize)
	}
}

// TestListObjectsCache_EvictionPurgesExpiredFirst — при достижении потолка
// evictIfNeededLocked обязан сперва вычистить просроченные entries; если их
// достаточно, свежая запись выживает без произвольной эвикции. Зеркалит
// authz.TestCache_MaxEntriesEvictsExpiredFirst.
func TestListObjectsCache_EvictionPurgesExpiredFirst(t *testing.T) {
	const maxSize = 10
	base := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	now := base
	c := newListObjectsCache(maxSize, 5*time.Second)
	c.setNowFunc(func() time.Time { return now })

	for i := 0; i < maxSize; i++ {
		subj := fmt.Sprintf("user:usr_stale_%d", i)
		key := cacheKeyFor(subj, "vpc_network", "act", "", "")
		c.put(key, subj, []string{"old"})
	}
	// Просрочиваем всё.
	now = base.Add(6 * time.Second)
	// Новый insert → потолок достигнут, но все старые просрочены → чистятся
	// (фаза 1 expired-sweep), свежая запись выживает без произвольной эвикции.
	freshKey := cacheKeyFor("user:usr_fresh", "vpc_network", "act", "", "")
	c.put(freshKey, "user:usr_fresh", []string{"new"})

	_, entries := c.size()
	if entries > maxSize {
		t.Fatalf("cache exceeded maxSize after expiry sweep: got %d, want <= %d", entries, maxSize)
	}
	if _, ok := c.get(freshKey); !ok {
		t.Fatalf("fresh entry must survive; expired entries should have been reclaimed first")
	}
}

// TestListObjectsCache_EvictionIsLinearNotQuadratic — regression guard against
// the O(N^2) full insertion-sort eviction that used to run under c.mu on every
// put() past maxSize (authz/listobjects.go finding, round-7 audit). With
// maxSize=10000 churned by 10x puts, eviction fires roughly every maxSize/10
// puts; the old insertion-sort re-sorted ALL ~10000 live entries each time
// (~2.5e7 swaps/eviction, ~90 evictions across this run) — multi-second wall
// time. The O(N) expired-sweep + arbitrary-eviction-to-low-water replacement
// (mirrors authz.Cache.evictLocked) does the same churn in well under a
// second. The elapsed-time budget below is deliberately generous (10x+
// margin over observed O(N) runtime) so it only trips on an algorithmic
// regression, not machine noise.
func TestListObjectsCache_EvictionIsLinearNotQuadratic(t *testing.T) {
	if testing.Short() {
		t.Skip("large-N perf regression guard skipped in -short")
	}
	const maxSize = 10000
	const totalPuts = 100000
	c := newListObjectsCache(maxSize, time.Hour)

	start := time.Now()
	for i := 0; i < totalPuts; i++ {
		subj := fmt.Sprintf("user:usr_%d", i)
		key := cacheKeyFor(subj, "vpc_network", "act", "", "")
		c.put(key, subj, []string{"id"})
	}
	elapsed := time.Since(start)

	_, entries := c.size()
	if entries > maxSize {
		t.Fatalf("cache exceeded maxSize after churn: got %d, want <= %d", entries, maxSize)
	}
	const budget = 3 * time.Second
	if elapsed > budget {
		t.Fatalf("put() with %d-entry eviction churn took %v, want < %v (O(N^2) insertion-sort regression?)", totalPuts, elapsed, budget)
	}
}
