package operations

import "context"

// principalCtxKey — приватный тип-ключ для context.WithValue, чтобы исключить
// коллизии с другими пакетами (Go anti-pattern: string-key в ctx).
type principalCtxKey struct{}

// WithPrincipal кладёт Principal в context. Используется auth-interceptor'ом
// api-gateway (E2): после валидации JWT и резолва subject через kacho-iam
// interceptor вызывает WithPrincipal и пробрасывает ctx дальше в handler.
//
// На E0 (без auth) ctx остаётся пустым и PrincipalFromContext возвращает
// SystemPrincipal().
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// PrincipalFromContext извлекает Principal из ctx. Если ctx пустой (нет
// auth-interceptor'а, фоновый job, тест) — возвращает SystemPrincipal().
//
// Use-case вызывает PrincipalFromContext в начале обработки мутации и
// передаёт результат в repo.CreateWithPrincipal:
//
//	p := operations.PrincipalFromContext(ctx)
//	if err := repo.CreateWithPrincipal(ctx, op, p); err != nil { ... }
func PrincipalFromContext(ctx context.Context) Principal {
	if ctx == nil {
		return SystemPrincipal()
	}
	if v, ok := ctx.Value(principalCtxKey{}).(Principal); ok {
		return v
	}
	return SystemPrincipal()
}
