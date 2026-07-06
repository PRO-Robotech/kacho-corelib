// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/authz"
	"github.com/PRO-Robotech/kacho-corelib/operations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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
			Extract: authz.StaticExtractor("project", func(req any) (string, error) {
				return req.(*fakeReq).id, nil
			}),
		},
		// scope-filtered List RPC — interceptor skips the per-RPC Check;
		// the handler authorises at the data level.
		"/kacho.cloud.vpc.v1.NetworkService/List": {
			Relation:      "viewer",
			ScopeFiltered: true,
			Extract:       authz.StaticExtractor("project", networkExtractor),
		},
		// stream-friendly RPC: extractor не зависит от req (stream-interceptor
		// подаёт nil как req до первого Recv), возвращает фиксированный object —
		// так authorize() доходит до Check на stream-пути.
		"/kacho.cloud.vpc.v1.NetworkService/Watch": {
			Relation: "viewer",
			Extract: authz.StaticExtractor("vpc_network", func(req any) (string, error) {
				return "enp00000000000000000", nil
			}),
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

// fakeServerStream — минимальный grpc.ServerStream, несущий заданный ctx
// (принципал). Достаточно для того, чтобы Stream-interceptor извлёк authz-decision
// из ss.Context(); тело stream'а не задействуется.
type fakeServerStream struct {
	ctx context.Context
}

func (s *fakeServerStream) Context() context.Context        { return s.ctx }
func (s *fakeServerStream) SetHeader(md metadata.MD) error  { return nil }
func (s *fakeServerStream) SendHeader(md metadata.MD) error { return nil }
func (s *fakeServerStream) SetTrailer(md metadata.MD)       {}
func (s *fakeServerStream) SendMsg(m any) error             { return nil }
func (s *fakeServerStream) RecvMsg(m any) error             { return nil }

// runStream прогоняет Stream-interceptor с ctx-несущим ServerStream и
// возвращает (handlerCalled, err).
func runStream(intr *authz.Interceptor, ctx context.Context, fullMethod string) (bool, error) {
	info := &grpc.StreamServerInfo{FullMethod: fullMethod}
	called := false
	handler := func(srv any, ss grpc.ServerStream) error {
		called = true
		return nil
	}
	err := intr.Stream()(nil, &fakeServerStream{ctx: ctx}, info, handler)
	return called, err
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

func TestInterceptor_HideExistenceMapsToNotFound(t *testing.T) {
	// Клиент сигналит existence-hiding: объект есть, но caller не вправе видеть.
	// Interceptor обязан вернуть NOT_FOUND (не PermissionDenied) и НЕ звать handler.
	handlerCalled := false
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		return false, authz.ErrHideExistence
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:    makeMap(),
		Client: stub,
	})
	ctx := ctxWithPrincipal(t, "usr_bob", "user")
	_, err := intr.Unary()(ctx, &fakeReq{id: "enp_x"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.vpc.v1.NetworkService/Get"},
		func(ctx context.Context, req any) (any, error) {
			handlerCalled = true
			return &fakeReq{}, nil
		})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound (existence-hiding), got %v", err)
	}
	if handlerCalled {
		t.Fatalf("handler must NOT be called on existence-hiding (would leak the object)")
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

// Unmapped RPC fails closed even when its proto-name contains "Internal".
// Internal RPC, требующий exempt, обязан быть явно в RPCMap (Relation для Check
// или Public=true) — name-based эвристика убрана как fail-open вектор: на
// internal-listener'е любой не-замапленный RPC исполнялся бы без authz-Check.
func TestInterceptor_UnmappedInternalRPCFailClosed(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check should NOT be called on an unmapped RPC")
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:    makeMap(),
		Client: stub,
	})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	_, err := runUnary(intr, ctx, "/kacho.cloud.iam.v1.InternalIAMService/Check", &fakeReq{id: "x"})
	if err == nil {
		t.Fatalf("expected fail-closed deny for unmapped Internal* RPC")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (fail-closed), got %v", err)
	}
}

// Internal RPC, явно помеченный Public=true, освобожден от Check (exempt).
func TestInterceptor_MappedPublicRPCExempt(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check should NOT be called on a Public-exempt RPC")
		return false, nil
	})
	m := makeMap()
	m["/kacho.cloud.iam.v1.InternalIAMService/Check"] = authz.RPCEntry{Public: true}
	intr := authz.NewInterceptor(authz.InterceptorOptions{Map: m, Client: stub})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	resp, err := runUnary(intr, ctx, "/kacho.cloud.iam.v1.InternalIAMService/Check", &fakeReq{id: "x"})
	if err != nil {
		t.Fatalf("expected Public RPC to pass, got %v", err)
	}
	if resp != "handled" {
		t.Fatalf("expected handler called")
	}
}

// Breakglass обходит Check для аутентифицированных, но anonymous (нет Principal
// в ctx) обязан быть denied — иначе breakglass=true пускает кого угодно.
func TestInterceptor_BreakglassAnonymousDenied(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check must NOT be called under breakglass")
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:        makeMap(),
		Client:     stub,
		Breakglass: true,
	})
	// context.Background() — нет Principal'а (anonymous).
	_, err := runUnary(intr, context.Background(), "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	if err == nil {
		t.Fatalf("expected anonymous denied even under breakglass")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if m := intr.Metrics(); m.Breakglass != 0 {
		t.Fatalf("breakglass metric must NOT increment on anonymous deny, got %d", m.Breakglass)
	}
}

// Breakglass=true эмулирует "все аутентифицированные allowed", НО принципал,
// которого api-gateway впрыснул как anonymous/bootstrap (injectAnonymous /
// PrincipalFromContext fallback дают Principal{Type:"system", ID:"anonymous"|
// "bootstrap"}), обязан быть denied через isAnonymousSubject closed-list. Здесь
// ctx НЕСЁТ такой Principal (в отличие от TestInterceptor_BreakglassAnonymousDenied,
// который бьёт по более раннему ok==false branch с context.Background()).
func TestInterceptor_BreakglassInjectedAnonymousDenied(t *testing.T) {
	for _, id := range []string{"anonymous", "bootstrap"} {
		t.Run(id, func(t *testing.T) {
			stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
				t.Fatalf("Check must NOT be called under breakglass")
				return false, nil
			})
			intr := authz.NewInterceptor(authz.InterceptorOptions{
				Map:        makeMap(),
				Client:     stub,
				Breakglass: true,
			})
			// Principal{Type:"system", ID:"anonymous"|"bootstrap"} — ровно то, что
			// продуцирует gateway injectAnonymous / bootstrap fallback.
			ctx := ctxWithPrincipal(t, id, "system")
			_, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
			if err == nil {
				t.Fatalf("expected injected %q principal denied even under breakglass", id)
			}
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("expected PermissionDenied, got %v", err)
			}
			m := intr.Metrics()
			if m.Breakglass != 0 {
				t.Fatalf("breakglass metric must NOT increment on %q deny, got %d", id, m.Breakglass)
			}
			if m.Denied == 0 {
				t.Fatalf("expected denied metric incremented for %q, got 0", id)
			}
		})
	}
}

// AllowSystemPrincipal=true пускает ТОЛЬКО явно установленный system-principal;
// anonymous (ctx без Principal'а, fallback на SystemPrincipal) обязан быть denied,
// иначе включение AllowSystemPrincipal на достижимом listener'е открывает обход.
func TestInterceptor_AllowSystemPrincipalAnonymousDenied(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check must NOT be called for anonymous under AllowSystemPrincipal")
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:                  makeMap(),
		Client:               stub,
		AllowSystemPrincipal: true,
	})
	// Нет Principal'а в ctx — раньше PrincipalFromContext отдавал SystemPrincipal
	// (bootstrap) → AllowSystemPrincipal пускал anonymous. Теперь — deny.
	_, err := runUnary(intr, context.Background(), "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	if err == nil {
		t.Fatalf("expected anonymous denied even with AllowSystemPrincipal=true")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// A ScopeFiltered RPC must NOT trigger the per-RPC Check — the handler
// authorises at the data level (ListObjects-filtered result). A single-object
// Check would 403 the whole call before the scope-filter runs.
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
	// Each call → Check (negative never cached).
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

// AllowSystemPrincipal must gate the blanket allow to the genuine system
// identity (Principal{Type:"system", ID:"bootstrap"}), never a client-supplied
// principal-id string. A caller that forges x-kacho-principal-id=bootstrap with
// any OTHER principal-type (e.g. "user") must NOT skip the per-RPC Check — it
// must fall through to the normal authorization path. Regression guard for the
// CWE-863 magic-string bypass.
func TestInterceptor_AllowSystemPrincipal_RejectsForgedBootstrapType(t *testing.T) {
	checkCalled := false
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		checkCalled = true
		return false, nil // deny — the forged principal has no access
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{
		Map:                  makeMap(),
		Client:               stub,
		AllowSystemPrincipal: true,
	})
	// Forged: principal-id == "bootstrap" but type == "user" (a client can set
	// x-kacho-principal-id/type headers). This is NOT the system identity.
	ctx := ctxWithPrincipal(t, "bootstrap", "user")
	_, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	if err == nil {
		t.Fatalf("expected forged bootstrap (type=user) to be denied, got allow")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
	if !checkCalled {
		t.Fatalf("expected the forged bootstrap to fall through to the per-RPC Check")
	}
}

// --- Stream interceptor coverage (finding: Stream() switch arms untested) ---

// TestInterceptorStream_DeniedOnNegativeCheck — stream authz-gate: отрицательный
// Check → PermissionDenied, handler НЕ вызывается (fail-closed).
func TestInterceptorStream_DeniedOnNegativeCheck(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{Map: makeMap(), Client: stub})
	ctx := ctxWithPrincipal(t, "usr_bob", "user")
	called, err := runStream(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Watch")
	if called {
		t.Fatalf("handler must NOT run on denied stream")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// TestInterceptorStream_UnavailableFailClosed — Check-error (не NoPath) →
// PermissionDenied на stream'е (DecisionUnavailable arm, fail-closed).
func TestInterceptorStream_UnavailableFailClosed(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		return false, errors.New("connection refused")
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{Map: makeMap(), Client: stub})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	called, err := runStream(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Watch")
	if called {
		t.Fatalf("handler must NOT run when Check unavailable")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (fail-closed), got %v", err)
	}
}

// TestInterceptorStream_UnmappedFailClosed — не-замапленный stream-RPC →
// PermissionDenied (DecisionUnmapped arm).
func TestInterceptorStream_UnmappedFailClosed(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check must NOT be called on an unmapped stream RPC")
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{Map: makeMap(), Client: stub})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	called, err := runStream(intr, ctx, "/kacho.cloud.vpc.v1.UnknownService/Subscribe")
	if called {
		t.Fatalf("handler must NOT run on unmapped stream RPC")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

// TestInterceptorStream_PublicAllowsHandler — Public=true stream-RPC (e.g.
// InternalResourceLifecycleService.Subscribe) → handler запускается без Check
// (DecisionInternal arm).
func TestInterceptorStream_PublicAllowsHandler(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check must NOT be called on a Public stream RPC")
		return false, nil
	})
	m := makeMap()
	m["/kacho.cloud.vpc.v1.InternalResourceLifecycleService/Subscribe"] = authz.RPCEntry{Public: true}
	intr := authz.NewInterceptor(authz.InterceptorOptions{Map: m, Client: stub})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	called, err := runStream(intr, ctx, "/kacho.cloud.vpc.v1.InternalResourceLifecycleService/Subscribe")
	if err != nil {
		t.Fatalf("expected Public stream to pass, got %v", err)
	}
	if !called {
		t.Fatalf("expected handler invoked for Public stream RPC")
	}
}

// TestInterceptorStream_NoPathAllowsHandler — ErrNoPath на stream'е →
// passthrough (DecisionNoPath arm запускает handler; NOT_FOUND отдаст сам stream).
func TestInterceptorStream_NoPathAllowsHandler(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		return false, authz.ErrNoPath
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{Map: makeMap(), Client: stub})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	called, err := runStream(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Watch")
	if err != nil {
		t.Fatalf("expected NoPath passthrough on stream, got %v", err)
	}
	if !called {
		t.Fatalf("expected handler invoked on NoPath passthrough")
	}
}

// --- NoPath passthrough (fail-OPEN) branch of authorize() (unary) ---

// TestInterceptor_NoPathPassthroughRunsHandler — ErrNoPath — единственная
// намеренно-разрешающая ветка: handler запускается (чтобы отдать NOT_FOUND из БД,
// а не маскировать его как 403). allowed-метрика инкрементится.
func TestInterceptor_NoPathPassthroughRunsHandler(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		return false, authz.ErrNoPath
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{Map: makeMap(), Client: stub})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	resp, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	if err != nil {
		t.Fatalf("expected NoPath passthrough (handler runs), got %v", err)
	}
	if resp != "handled" {
		t.Fatalf("expected handler invoked on NoPath")
	}
	if m := intr.Metrics(); m.Allowed == 0 {
		t.Fatalf("expected allowed metric incremented on NoPath passthrough")
	}
}

// TestInterceptor_NoPathBoundary_GenericErrorStillDenies — граница NoPath: ошибка
// Check, НЕ являющаяся ErrNoPath (даже обёрнутая), обязана оставаться fail-closed
// (Unavailable → PermissionDenied), чтобы passthrough не «расширился» молча.
func TestInterceptor_NoPathBoundary_GenericErrorStillDenies(t *testing.T) {
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		return false, fmt.Errorf("wrapped: %w", errors.New("dial timeout"))
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{Map: makeMap(), Client: stub})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	resp, err := runUnary(intr, ctx, "/kacho.cloud.vpc.v1.NetworkService/Get", &fakeReq{id: "enp_x"})
	if err == nil {
		t.Fatalf("expected fail-closed deny for a non-NoPath error, got resp=%v", resp)
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied (fail-closed), got %v", err)
	}
}

// --- Extract / FormatObject failure deny branches of authorize() ---

// TestInterceptor_ObjectExtractErrorDenies — Extract(req) error → PermissionDenied,
// handler НЕ вызывается (malformed request нельзя пускать к handler'у).
func TestInterceptor_ObjectExtractErrorDenies(t *testing.T) {
	handlerCalled := false
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check must NOT be called when object extract fails")
		return false, nil
	})
	intr := authz.NewInterceptor(authz.InterceptorOptions{Map: makeMap(), Client: stub})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	// networkExtractor возвращает error для req НЕ-*fakeReq типа.
	_, err := intr.Unary()(ctx, "not-a-fakeReq",
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.vpc.v1.NetworkService/Get"},
		func(ctx context.Context, req any) (any, error) { handlerCalled = true; return nil, nil })
	if handlerCalled {
		t.Fatalf("handler must NOT run when object extract fails")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied on extract error, got %v", err)
	}
}

// TestInterceptor_FormatObjectErrorDenies — extractor вернул пустой object id →
// FormatObject error → PermissionDenied, handler НЕ вызывается.
func TestInterceptor_FormatObjectErrorDenies(t *testing.T) {
	handlerCalled := false
	stub := authz.CheckClientFunc(func(ctx context.Context, s, r, o string) (bool, error) {
		t.Fatalf("Check must NOT be called when object id is empty")
		return false, nil
	})
	m := makeMap()
	// Extractor возвращает ПУСТОЙ id (без ошибки) → FormatObject отбьёт "empty object id".
	m["/kacho.cloud.vpc.v1.NetworkService/Get"] = authz.RPCEntry{
		Relation: "viewer",
		Extract:  authz.StaticExtractor("vpc_network", func(req any) (string, error) { return "", nil }),
	}
	intr := authz.NewInterceptor(authz.InterceptorOptions{Map: m, Client: stub})
	ctx := ctxWithPrincipal(t, "usr_alice", "user")
	_, err := intr.Unary()(ctx, &fakeReq{id: "x"},
		&grpc.UnaryServerInfo{FullMethod: "/kacho.cloud.vpc.v1.NetworkService/Get"},
		func(ctx context.Context, req any) (any, error) { handlerCalled = true; return nil, nil })
	if handlerCalled {
		t.Fatalf("handler must NOT run when FormatObject fails")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied on FormatObject error, got %v", err)
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
