package authz

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClient — счётчик вызовов + программируемые ответы.
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

// P4.GWT-01: cache miss → call → cache hit on second call.
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

// P4.GWT-03: client error → ErrUnavailable wrapped.
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
	if ids != nil {
		t.Fatalf("ids = %v, want nil on err", ids)
	}
}

// P4.GWT-07: empty grant → empty slice, NOT PermissionDenied.
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

// P4.GWT-26: InvalidateBySubject — cache miss after invalidation.
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

// P4.GWT-14: TTL expiry → next call re-fetches.
func TestListObjects_TTLExpiry(t *testing.T) {
	client := &fakeClient{
		fn: func(_ context.Context, _ ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{ResourceIDs: []string{"net-x"}}, nil
		},
	}
	svc := NewListObjectsService(client, ListObjectsConfig{
		TTL:        50 * time.Millisecond,
		MaxEntries: 100,
		MaxResults: 10000,
	})

	if _, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("after first call: %d", got)
	}

	time.Sleep(80 * time.Millisecond)

	if _, err := svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := client.calls.Load(); got != 2 {
		t.Fatalf("after expiry: client calls = %d, want 2", got)
	}
}

// P4.GWT-05: deterministic ordering preserved (we return what client returned).
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

// P4.GWT-06: pagination — multiple FGA calls accumulated.
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

// P4.GWT-04a: scope hint produces different cache entries.
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

// Concurrent calls are safe (race detector).
func TestListObjects_Concurrent(t *testing.T) {
	client := &fakeClient{
		fn: func(_ context.Context, _ ListObjectsRequest) (ListObjectsResponse, error) {
			return ListObjectsResponse{ResourceIDs: []string{"net-1"}}, nil
		},
	}
	svc := newSvc(t, client)

	done := make(chan struct{}, 50)
	for i := 0; i < 50; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = svc.ListAllowedIDs(context.Background(), "user:usr_alice", "vpc_network", "vpc.networks.read", ListAllowedIDsOptions{})
		}()
	}
	for i := 0; i < 50; i++ {
		<-done
	}
}

// Validation: subject == "" → error.
func TestListObjects_ValidationEmptySubject(t *testing.T) {
	svc := newSvc(t, &fakeClient{})
	_, err := svc.ListAllowedIDs(context.Background(), "", "vpc_network", "act", ListAllowedIDsOptions{})
	if err == nil {
		t.Fatal("empty subject must error")
	}
}
