// Package grpcsrv — principal_extract.go (KAC-107 E2).
//
// PrincipalExtractInterceptor читает три metadata-header'а, которые api-gateway
// auth-interceptor выставляет после успешной JWT-валидации:
//
//	x-kacho-principal-type         "user" | "service_account" | "system"
//	x-kacho-principal-id           "usr-..." | "sva-..." | "anonymous"
//	x-kacho-principal-display-name "alice@example.com" | "" | ...
//
// и кладёт `operations.Principal` в ctx через `operations.WithPrincipal`.
// Backend use-case'ы вызывают `operations.PrincipalFromContext(ctx)` →
// `Repo.CreateWithPrincipal(ctx, op, p)` — реальный principal попадает в
// `operations.principal_*` колонки (запрет #11 DoD #5 acceptance §6).
//
// Если headers отсутствуют (legacy-call'ы, прямой gRPC без api-gateway) —
// fallback на `SystemPrincipal()` (как до E2; идентично `PrincipalFromContext`
// поведению без auth).
package grpcsrv

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

const (
	MDKeyPrincipalType    = "x-kacho-principal-type"
	MDKeyPrincipalID      = "x-kacho-principal-id"
	MDKeyPrincipalDisplay = "x-kacho-principal-display-name"
)

// UnaryPrincipalExtract — gRPC unary interceptor для backend-сервисов.
// Должен стоять РАНЬШЕ бизнес-handler'ов в цепочке interceptor'ов.
func UnaryPrincipalExtract() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx = extractPrincipal(ctx)
		return handler(ctx, req)
	}
}

// StreamPrincipalExtract — то же для stream RPC.
func StreamPrincipalExtract() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := extractPrincipal(ss.Context())
		return handler(srv, &principalStream{ServerStream: ss, ctx: ctx})
	}
}

type principalStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *principalStream) Context() context.Context { return s.ctx }

func extractPrincipal(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	pType := first(md.Get(MDKeyPrincipalType))
	pID := first(md.Get(MDKeyPrincipalID))
	if pType == "" || pID == "" {
		return ctx
	}
	p := operations.Principal{
		Type:        pType,
		ID:          pID,
		DisplayName: first(md.Get(MDKeyPrincipalDisplay)),
	}
	return operations.WithPrincipal(ctx, p)
}

func first(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}
