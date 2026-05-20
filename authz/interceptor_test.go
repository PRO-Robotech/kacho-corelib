package authz_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/authz"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeReq struct {
	id string
}

func networkExtractor(req any) (string, error) {
	if r, ok := req.(*fakeReq); ok {
		return r.id, nil
	}
	return "", errors.New("bad req type")
}

func makeMap() authz.RPCMap {
	return authz.RPCMap{
		"/kacho.cloud.vpc.v1.NetworkService/Get": {
			Relation: "viewer",
			Extract:  authz.StaticExtractor("vpc_network", networkExtractor),
		},
		"/kacho.cloud.vpc.v1.NetworkService/Create": {
			Relation: "editor",
			Extract:  authz.StaticExtractor("project", func(req any) (string, error) {
				return req.(*fakeReq).id, nil
			}),
		},
		// KAC-127 #25: scope-filtered List RPC — interceptor skips the
		// per-RPC Check; the handler authorises at the data level.
		"/kacho.cloud.vpc.v1.NetworkService/List": {
			Relation:      "viewer",
			ScopeFiltered: true,
			Extract:       authz.StaticExtractor("project", networkExtractor),
		},
	}
}

func ctxWithPrincipal(t *testing.T, id, ptype string) context.Context {
	t.Helper()
	return operations.WithPrincipal(context.Background(), operations.Principal{
		Type: ptype, ID: id, DisplayName: id,
	})
}

func runUnary(intr *authz.Interceptor, ctx context.Context, fullMethod string, req any) (any, error) {
	info := &grpc.UnaryServerInfo{FullMethod: fullMethod}
	handler := func(ctx context.Context, req any) (any, error) {
		return "handled", nil
	}
	return intr.Unary()(ctx, req, info, handler)
}

func TestInterceptor_AllowedOnPositiveCheck(t *testing.T) {
	calls := 0
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		calls++
		if s == "user:usr_alice" && r == "viewer" && o == "vpc_network:enp_x" {
			return true, nil
		}
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		ServiceName: "kacho-vpc-test",
		Map:         makeMap(),
		Client:      stub,
	})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	resp, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	if err != nil {
		t.Fatalf("expected allowed, got err: %v", err)
	}
	if resp != "handled" {
		t.Fatalf("expected handler called")
	}
	if calls != 1 {
		t.Fatalf("expected 1 Check call, got %d", calls)
	}
}

func TestInterceptor_DeniedOnNegativeCheck(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:    makeMap(),
		Client: stub,
	})
	ctx := ctxWithPrincipal(t, "usr_bob", "user")
	_, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	if err == nil {
		t.Fatalf("expected denied")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestInterceptor_FailClosedOnCheckError(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		return false, errors.New("connection refused")
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:    makeMap(),
		Client: stub,
	})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	_, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	if err == nil {
		t.Fatalf("expected fail-closed deny")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (fail-closed), got %v", err)
	}
}

func TestInterceptor_BreakglassBypass(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check should NOT be called when Breakglass=true")
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:        makeMap(),
		Client:     stub,
		Breakglass: true,
	})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	resp, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	if err != nil {
		t.Fatalf("expected breakglass allow, got: %v", err)
	}
	if resp != "handled" {
		t.Fatalf("expected handler called")
	}
	m := intr.Metrics()
	if m.Breakglass != 1 {
		t.Fatalf("expected breakglass metric=1, got %d", m.Breakglass)
	}
}

func TestInterceptor_UnmappedRPCDeniedByDefault(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		return true, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:    makeMap(),
		Client: stub,
	})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	_, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.UnknownService/Foo", &fakeReq{id: "x"})
	if err == nil {
		t.Fatalf("expected denied for unmapped")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestInterceptor_InternalRPCBypassed(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check should NOT be called on Internal* RPC")
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:    makeMap(),
		Client: stub,
	})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	resp, err := runUnary(intr, ctx, "/kacho.cloud.iam.v1.InternalIAMService/Check", &fakeReq{id: "x"})
	if err != nil {
		t.Fatalf("expected internal RPC pass, got %v", err)
	}
	if resp != "handled" {
		t.Fatalf("expected handler called")
	}
}

// KAC-127 #25: a ScopeFiltered RPC must NOT trigger the per-RPC Check — the
// handler authorises at the data level (ListObjects-filtered result). A
// single-object Check would 403 the whole call before the scope-filter runs.
func TestInterceptor_ScopeFilteredRPCBypassesCheck(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check must NOT be called on a ScopeFiltered RPC")
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:    makeMap(),
		Client: stub,
	})
	// A non-member principal — the handler (not the interceptor) would
	// scope-filter the result to empty; the interceptor must let it through.
	ctx := ctxWithPrincipal(t, "usr_nonmember", "user")
	resp, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/List", &fakeReq{id: "prj_x"})
	if err != nil {
		t.Fatalf("expected scope-filtered RPC to pass the interceptor, got %v", err)
	}
	if resp != "handled" {
		t.Fatalf("expected handler called")
	}
}

func TestInterceptor_NoPrincipalDenied(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check should NOT be called when no Principal")
		return false, nil
	})
	// No principal in ctx — PrincipalFromContext returns system{ID:bootstrap}.
	// AllowSystemPrincipal=false (default) → it should reach Check (subject=user:bootstrap).
	// Switch test to assert that the empty path through SubjectExtractor returns ok=true
	// for system; but Check stub fails the test. To assert no-principal explicitly,
	// override SubjectExtractor.
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:    makeMap(),
		Client: stub,
		SubjectExtractor: func(ctx context.Context) (string, string, bool) {
			return "", "", false
		},
	})
	_, err := runUnary(intr, context.Background(), "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	if err == nil {
		t.Fatalf("expected denied without principal")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestInterceptor_CacheHitSkipsCheck(t *testing.T) {
	calls := 0
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		calls++
		return true, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:    makeMap(),
		Client: stub,
	})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	for i := 0; i < 5; i++ {
		_, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	if calls != 1 {
		t.Fatalf("expected 1 Check (rest cache hits), got %d", calls)
	}
	m := intr.Metrics()
	if m.CacheHits < 4 {
		t.Fatalf("expected ≥4 cache hits, got %d", m.CacheHits)
	}
}

func TestInterceptor_NegativeNotCached(t *testing.T) {
	calls := 0
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		calls++
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:    makeMap(),
		Client: stub,
	})
	ctx := ctxWithPrincipal(t, "usr_bob", "user")
	for i := 0; i < 3; i++ {
		_, _ = runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	}
	// Each call → Check (negative never cached) — D-9.
	if calls != 3 {
		t.Fatalf("expected 3 Check calls (negative not cached), got %d", calls)
	}
}

func TestInterceptor_RateLimitDeniedStorm(t *testing.T) {
	checkCount := 0
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		checkCount++
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:                 makeMap(),
		Client:              stub,
		DenyRateLimitPerSec: 5, // burst = 10
	})
	ctx := ctxWithPrincipal(t, "usr_eve", "user")
	rateLimitedCount := 0
	for i := 0; i < 30; i++ {
		_, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
		if status.Code(err) == codes.ResourceExhausted {
			rateLimitedCount++
		}
	}
	if rateLimitedCount == 0 {
		t.Fatalf("expected rate-limited requests; got 0 of 30")
	}
	// Не больше burst-size прошло до limit'а.
	if checkCount > 11 {
		t.Fatalf("expected ≤11 Check calls (burst 10 + 1 edge), got %d", checkCount)
	}
}

func TestInterceptor_AllowSystemPrincipal(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check must not be called for system principal when AllowSystemPrincipal=true")
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:                  makeMap(),
		Client:               stub,
		AllowSystemPrincipal: true,
	})
	ctx := ctxWithPrincipal(t, "bootstrap", "system")
	resp, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	if err != nil {
		t.Fatalf("expected allow for system, got %v", err)
	}
	if resp != "handled" {
		t.Fatalf("expected handler called")
	}
}

func TestInterceptor_CheckTimeoutHonored(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		// Сам стаб не зависит от ctx — проверяем, что Interceptor правильно cancel'ит.
		<-ctx.Done()
		return false, ctx.Err()
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:          makeMap(),
		Client:       stub,
		CheckTimeout: 50 * time.Millisecond,
	})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	start := time.Now()
	_, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected timeout-induced fail")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected fast fail (~50ms), got %v", elapsed)
	}
}
