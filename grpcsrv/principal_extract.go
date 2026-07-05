// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package grpcsrv — principal_extract.go.
//
// PrincipalExtractInterceptor читает три metadata-header'а, которые api-gateway
// auth-interceptor выставляет после успешной JWT-валидации:
//
//	x-kacho-principal-type         "user" | "service_account" | "system"
//	x-kacho-principal-id           "usr-..." | "sva-..." | "anonymous"
//	x-kacho-principal-display-name "alice@example.com" | "" | ...
//
// и кладет `operations.Principal` в ctx через `operations.WithPrincipal`.
// Backend use-case'ы вызывают `operations.PrincipalFromContext(ctx)` →
// `Repo.CreateWithPrincipal(ctx, op, p)` — реальный principal попадает в
// `operations.principal_*` колонки.
//
// Если headers отсутствуют (legacy-call'ы, прямой gRPC без api-gateway) —
// fallback на `SystemPrincipal()` (идентично `PrincipalFromContext` поведению без auth).
package grpcsrv

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

const (
	MDKeyPrincipalType    = "x-kacho-principal-type"
	MDKeyPrincipalID      = "x-kacho-principal-id"
	MDKeyPrincipalDisplay = "x-kacho-principal-display-name"
)

// envDebugPrincipal читает KACHO_DEBUG_PRINCIPAL лениво (не при package-init) и
// кеширует результат. Дефолтное значение debug-флага для extractor'ов, построенных
// без явной опции. Composition root может переопределить через WithPrincipalDebug.
var (
	envDebugOnce sync.Once
	envDebugFlag bool
)

func envDebugPrincipal() bool {
	envDebugOnce.Do(func() { envDebugFlag = os.Getenv("KACHO_DEBUG_PRINCIPAL") == "1" })
	return envDebugFlag
}

// debugConfig управляет опциональным debug-логированием principal-extract'а.
// Логи идут через инъектированный/дефолтный slog.Logger (а НЕ через package-global
// stdout JSON-sink), чтобы уважать structured-logging-конфиг composition-root'а.
type debugConfig struct {
	enabled bool
	logger  *slog.Logger
}

func (d debugConfig) log(msg string, args ...any) {
	if d.enabled && d.logger != nil {
		d.logger.Info(msg, args...)
	}
}

// defaultDebugConfig строит debug-конфиг из env-дефолта, направляя вывод через
// текущий slog.Default() (перехватывается на момент построения extractor'а).
func defaultDebugConfig() debugConfig {
	return debugConfig{enabled: envDebugPrincipal(), logger: slog.Default()}
}

// PrincipalExtractOption — функциональная опция UnaryPrincipalExtract /
// StreamPrincipalExtract.
type PrincipalExtractOption func(*principalExtractConfig)

type principalExtractConfig struct {
	dbg debugConfig
}

// WithPrincipalDebug включает/выключает debug-логирование extract-решений и dump'а
// incoming metadata (по умолчанию — из env KACHO_DEBUG_PRINCIPAL). Composition root
// решает, а не package-init.
func WithPrincipalDebug(enabled bool) PrincipalExtractOption {
	return func(c *principalExtractConfig) { c.dbg.enabled = enabled }
}

// WithPrincipalDebugLogger направляет debug-вывод в указанный логгер (вместо
// slog.Default()). nil игнорируется.
func WithPrincipalDebugLogger(l *slog.Logger) PrincipalExtractOption {
	return func(c *principalExtractConfig) {
		if l != nil {
			c.dbg.logger = l
		}
	}
}

func buildPrincipalExtractConfig(opts []PrincipalExtractOption) principalExtractConfig {
	cfg := principalExtractConfig{dbg: defaultDebugConfig()}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.dbg.logger == nil {
		cfg.dbg.logger = slog.Default()
	}
	return cfg
}

// warnTrustOnce — WARN о безусловном доверии к заголовкам выводится один раз на
// процесс (см. warnUnconditionalTrust).
var warnTrustOnce sync.Once

// warnUnconditionalTrust логирует один startup-WARN о том, что этот extractor
// доверяет x-kacho-principal-* без проверки транспорта/форвардера. Повышает шанс,
// что оператор не смонтирует его на peer-достижимый listener (defense-in-depth
// против impersonation, CWE-290/863).
func warnUnconditionalTrust(l *slog.Logger) {
	if l == nil {
		l = slog.Default()
	}
	warnTrustOnce.Do(func() {
		l.Warn("grpcsrv.UnaryPrincipalExtract trusts x-kacho-principal-* headers " +
			"UNCONDITIONALLY (no transport/forwarder verification); mount ONLY on a " +
			"listener no untrusted peer can reach. On mTLS listeners prefer the " +
			"trust-aware pair UnaryCertIdentityExtract + " +
			"UnaryTrustedPrincipalExtract(WithTrustedForwarders(...)).")
	})
}

// UnaryPrincipalExtract — gRPC unary interceptor для backend-сервисов.
// Должен стоять РАНЬШЕ бизнес-handler'ов в цепочке interceptor'ов.
//
// ВНИМАНИЕ (trust): этот extractor читает x-kacho-principal-* БЕЗУСЛОВНО, без
// проверки транспорта/cert-identity форвардера — он доверяет, что заголовки
// проставил только api-gateway. Монтировать ТОЛЬКО на listener'е, куда не может
// дозвониться неконтролируемый peer. Для cluster-internal mTLS-листенеров
// предпочтительна trust-aware связка UnaryCertIdentityExtract +
// UnaryTrustedPrincipalExtract(WithTrustedForwarders(<gateway-SAN>)): она снимает
// principal на недоверенном/не-форвардер peer'е (защита от confused-deputy).
// Для новых mTLS-листенеров предпочитайте именно trust-aware связку. При
// построении этот extractor выводит один startup-WARN об этом свойстве.
func UnaryPrincipalExtract(opts ...PrincipalExtractOption) grpc.UnaryServerInterceptor {
	cfg := buildPrincipalExtractConfig(opts)
	warnUnconditionalTrust(cfg.dbg.logger)
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx = extractPrincipal(ctx, cfg.dbg)
		return handler(ctx, req)
	}
}

// StreamPrincipalExtract — то же для stream RPC.
func StreamPrincipalExtract(opts ...PrincipalExtractOption) grpc.StreamServerInterceptor {
	cfg := buildPrincipalExtractConfig(opts)
	warnUnconditionalTrust(cfg.dbg.logger)
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := extractPrincipal(ss.Context(), cfg.dbg)
		return handler(srv, &principalStream{ServerStream: ss, ctx: ctx})
	}
}

type principalStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *principalStream) Context() context.Context { return s.ctx }

func extractPrincipal(ctx context.Context, dbg debugConfig) context.Context {
	p, ok := principalFromIncomingMetadata(ctx, dbg)
	if !ok {
		return ctx
	}
	dbg.log("principal_extract: principal set", "type", p.Type, "id", p.ID)
	return operations.WithPrincipal(ctx, p)
}

// principalFromIncomingMetadata parses the x-kacho-principal-* headers from the
// incoming metadata into an operations.Principal. ok is false when there is no
// incoming metadata or the required type/id headers are absent (legacy / direct
// gRPC calls). Shared by extractPrincipal (UnaryPrincipalExtract) and the
// trust-aware UnaryTrustedPrincipalExtract (cert_identity.go).
func principalFromIncomingMetadata(ctx context.Context, dbg debugConfig) (operations.Principal, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		dbg.log("principal_extract: no incoming metadata")
		return operations.Principal{}, false
	}
	if dbg.enabled {
		// Dump metadata keys for debugging, redacting values of sensitive headers
		// (authorization/token/cookie/...) so a credential is never written to logs.
		keys := make([]string, 0, len(md))
		for k := range md {
			keys = append(keys, k+"="+redactMetadataValue(k, md.Get(k)))
		}
		dbg.log("principal_extract: incoming metadata", "keys", strings.Join(keys, "; "))
	}
	pType := first(md.Get(MDKeyPrincipalType))
	pID := first(md.Get(MDKeyPrincipalID))
	if pType == "" || pID == "" {
		dbg.log("principal_extract: missing principal headers", "type", pType, "id", pID)
		return operations.Principal{}, false
	}
	return operations.Principal{
		Type:        pType,
		ID:          pID,
		DisplayName: first(md.Get(MDKeyPrincipalDisplay)),
	}, true
}

// sensitiveMetadataSubstrings — подстроки (lowercase) в имени metadata-ключа,
// маркирующие потенциально-секретное значение, которое НЕ должно попадать в логи.
var sensitiveMetadataSubstrings = []string{
	"authorization", "auth", "cookie", "token", "secret", "password",
	"passwd", "apikey", "api-key", "credential", "bearer", "session",
	"jwt", "assertion", "private",
}

// isSensitiveMetadataKey сообщает, следует ли редактировать значение под ключом key.
func isSensitiveMetadataKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range sensitiveMetadataSubstrings {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// redactMetadataValue возвращает значение ключа для debug-дампа, маскируя значения
// чувствительных ключей.
func redactMetadataValue(key string, vals []string) string {
	if isSensitiveMetadataKey(key) {
		return "<redacted>"
	}
	return strings.Join(vals, ",")
}

func first(vs []string) string {
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}
