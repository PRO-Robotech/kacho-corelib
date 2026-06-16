// Package grpcsrv — cert_identity.go (SEC-B): client-cert identity extractor +
// the principal⟺mTLS trust invariant (FD-4, FD-5).
//
// Two orthogonal server-side identities coexist on a cluster-internal listener
// and are BOTH made available downstream (for audit, FD-4):
//
//   - cert-identity (the *module*): a verbatim, opaque SPIFFE-like SAN string
//     extracted from the verified client-cert presented over mTLS. SEC-B only
//     extracts the string; it does NOT parse the sva-id, validate it against IAM,
//     or resolve it to a ServiceAccount (that is SEC-C, FD-5).
//   - principal (the *user*): carried in x-kacho-principal-* metadata, set by the
//     api-gateway auth-interceptor after JWT validation (see principal_extract.go).
//
// Trust invariant (FD-4, epic design-review I2): on an mTLS listener, incoming
// principal-metadata is trusted ⟺ the peer passed mTLS client-cert verification
// from the internal CA. With no verified client-cert, principal-metadata from
// that peer is NOT trusted (and must be dropped by the authz layer). On an
// insecure listener (enable=false, dev-mode) the invariant is inapplicable —
// there is no client-cert at all and principal-metadata is accepted as today
// (backward-compat). The invariant activates only under mTLS.
//
// The SAN format is the SPIRE-compatible internal trust-domain form
// spiffe://kacho.cloud/ns/<ns>/sa/kacho-<svc> (epic §4.1.4); cert-manager issues
// string-SANs in this exact shape. Only URIs under the kacho.cloud trust domain
// are accepted; a foreign spiffe trust-domain yields empty (no foreign-field leak).
package grpcsrv

import (
	"context"
	"crypto/x509"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// kachoSpiffePrefix is the internal trust-domain prefix of a Kachō module
// identity SAN. Only URI-SANs starting with this prefix are recognized as a
// module identity (epic §4.1.4: spiffe://kacho.cloud/ns/<ns>/sa/kacho-<svc>).
const kachoSpiffePrefix = "spiffe://kacho.cloud/"

// CertIdentity extracts the module identity from a (verified) client-cert as the
// verbatim, opaque SPIFFE-like SAN string (FD-5). Selection rule (part of the
// extractor contract): the FIRST URI-SAN whose value starts with
// "spiffe://kacho.cloud/" is returned exactly as it appears in the cert; other
// URI-SANs are ignored and the result is stable across calls (SEC-B-14).
//
// Returns "" deterministically when cert is nil, has no URI-SANs, or has no
// URI-SAN under the kacho.cloud trust domain (SEC-B-13). It never parses or
// resolves the identity and never panics.
func CertIdentity(cert *x509.Certificate) string {
	if cert == nil {
		return ""
	}
	for _, u := range cert.URIs {
		if u == nil {
			continue
		}
		if s := u.String(); strings.HasPrefix(s, kachoSpiffePrefix) {
			return s
		}
	}
	return ""
}

// certIdentityCtxKey is a private context key carrying the extracted module
// identity and whether the peer was mTLS-verified.
type certIdentityCtxKey struct{}

type certIdentity struct {
	id       string
	verified bool
}

// WithCertIdentity stores the extracted cert-identity and the mTLS-verified flag
// in ctx. Exposed so the principal-aware layer (and tests) can assert the
// invariant deterministically without a live TLS peer.
func WithCertIdentity(ctx context.Context, id string, verified bool) context.Context {
	return context.WithValue(ctx, certIdentityCtxKey{}, certIdentity{id: id, verified: verified})
}

// CertIdentityFromContext returns the extracted module identity and whether the
// peer was mTLS-verified. A ctx that never carried a cert-identity (no mTLS peer)
// reports ("", false) — i.e. NOT mTLS-verified (FD-4 default-deny of trust).
func CertIdentityFromContext(ctx context.Context) (id string, verified bool) {
	if ctx == nil {
		return "", false
	}
	if v, ok := ctx.Value(certIdentityCtxKey{}).(certIdentity); ok {
		return v.id, v.verified
	}
	return "", false
}

// peerTLSState classifies the transport security of the incoming peer:
//   - tlsPresent: true when the connection is TLS (one-way or mutual), false on
//     an insecure (plaintext) listener.
//   - verifiedCert: the first verified client-cert leaf, or nil when the peer
//     presented no client-cert OR none verified against the client-CA.
func peerTLSState(ctx context.Context) (tlsPresent bool, verifiedCert *x509.Certificate) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.AuthInfo == nil {
		return false, nil
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return false, nil
	}
	// TLS is present. A verified client-cert appears as the leaf of the first
	// verified chain; absence ⇒ no verified client-cert reached us.
	if len(tlsInfo.State.VerifiedChains) > 0 && len(tlsInfo.State.VerifiedChains[0]) > 0 {
		return true, tlsInfo.State.VerifiedChains[0][0]
	}
	return true, nil
}

// UnaryCertIdentityExtract is a server interceptor that classifies the peer's
// transport security and, for an mTLS-verified peer, extracts its module
// identity (FD-5) into ctx. It MUST run before UnaryTrustedPrincipalExtract.
//
//   - mTLS-verified peer → WithCertIdentity(ctx, CertIdentity(leaf), true).
//   - TLS peer without a verified client-cert → WithCertIdentity(ctx, "", false)
//     (defense-in-depth: marks the peer not-verified so principal is dropped).
//   - insecure (plaintext) peer → ctx untouched (no cert-identity ever set);
//     CertIdentityFromContext then reports ("", false) and the principal layer
//     treats the insecure listener as dev backward-compat (FD-1).
func UnaryCertIdentityExtract() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx = withCertIdentityFromPeer(ctx)
		return handler(ctx, req)
	}
}

// StreamCertIdentityExtract is the stream analogue of UnaryCertIdentityExtract.
func StreamCertIdentityExtract() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := withCertIdentityFromPeer(ss.Context())
		return handler(srv, &certIdentityStream{ServerStream: ss, ctx: ctx})
	}
}

type certIdentityStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *certIdentityStream) Context() context.Context { return s.ctx }

func withCertIdentityFromPeer(ctx context.Context) context.Context {
	tlsPresent, leaf := peerTLSState(ctx)
	if !tlsPresent {
		// Insecure listener: no client-cert at all. Leave ctx untouched so the
		// principal layer applies dev backward-compat (FD-1/FD-4 N/A).
		return ctx
	}
	if leaf == nil {
		// TLS but no verified client-cert (defense-in-depth). RequireAndVerify
		// normally rejects this at handshake; if it ever reaches here, mark the
		// peer not-verified so principal-metadata is dropped (FD-4).
		return WithCertIdentity(ctx, "", false)
	}
	return WithCertIdentity(ctx, CertIdentity(leaf), true)
}

// UnaryTrustedPrincipalExtract reads x-kacho-principal-* metadata and exposes it
// downstream ONLY when it is trustworthy under the FD-4 invariant. It MUST run
// after UnaryCertIdentityExtract.
//
// Trust decision:
//   - mTLS-verified peer (CertIdentityFromContext verified=true) → principal
//     trusted (SEC-B-15).
//   - insecure listener (no cert-identity ever set on ctx) → principal trusted as
//     today, dev backward-compat (SEC-B-17). Distinguished from an unverified TLS
//     peer by peerTLSState: insecure ⇒ no TLS transport at all.
//   - TLS peer without a verified client-cert → principal NOT trusted; dropped
//     (SEC-B-16 defense-in-depth, FD-4).
//
// The decision is recorded via withTrustedPrincipal so TrustedPrincipalFromContext
// returns (principal, trusted). cert-identity and principal are orthogonal and
// both remain available downstream for audit (FD-4) — neither substitutes the other.
func UnaryTrustedPrincipalExtract() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx = withTrustedPrincipal(ctx)
		return handler(ctx, req)
	}
}

// StreamTrustedPrincipalExtract is the stream analogue.
func StreamTrustedPrincipalExtract() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := withTrustedPrincipal(ss.Context())
		return handler(srv, &certIdentityStream{ServerStream: ss, ctx: ctx})
	}
}

type trustedPrincipalCtxKey struct{}

type trustedPrincipal struct {
	principal operations.Principal
	// acr — the forwarded JWT `acr` (sub-phase 5.4). Carried ONLY when trusted
	// (same FD-4 boundary as principal); empty on an untrusted/unverified peer.
	acr     string
	trusted bool
}

func withTrustedPrincipal(ctx context.Context) context.Context {
	trusted := principalIsTrusted(ctx)
	p := operations.SystemPrincipal()
	acr := ""
	if pp, ok := principalFromIncomingMetadata(ctx); ok {
		p = pp
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		acr = first(md.Get(MDKeyTokenACR))
	}
	if !trusted {
		// FD-4: on an unverified mTLS peer the forwarded principal-metadata is
		// dropped — and so is the acr (anti-spoof; a non-gateway peer cannot
		// elevate its acr by forging the header). 5.4-06 plumbing half.
		acr = ""
	}
	if trusted {
		// Make the trusted principal available to the standard operations carrier
		// so existing use-cases (operations.PrincipalFromContext) see it too.
		ctx = operations.WithPrincipal(ctx, p)
	}
	return context.WithValue(ctx, trustedPrincipalCtxKey{}, trustedPrincipal{principal: p, acr: acr, trusted: trusted})
}

// principalIsTrusted implements the FD-4 decision (see UnaryTrustedPrincipalExtract).
func principalIsTrusted(ctx context.Context) bool {
	tlsPresent, _ := peerTLSState(ctx)
	if !tlsPresent {
		// Insecure listener: dev backward-compat, principal accepted as today.
		return true
	}
	// TLS listener: trust ⟺ peer is mTLS client-cert verified.
	_, verified := CertIdentityFromContext(ctx)
	return verified
}

// TrustedPrincipalFromContext returns the principal and whether it is trusted
// under the FD-4 invariant. trusted=false means the principal-metadata came from
// an unverified peer on an mTLS listener and the authz layer must ignore it.
func TrustedPrincipalFromContext(ctx context.Context) (operations.Principal, bool) {
	if ctx == nil {
		return operations.SystemPrincipal(), false
	}
	if v, ok := ctx.Value(trustedPrincipalCtxKey{}).(trustedPrincipal); ok {
		return v.principal, v.trusted
	}
	return operations.SystemPrincipal(), false
}

// WithTrustedACR stores a forwarded JWT `acr` and the FD-4 trust flag directly in
// ctx (bypassing the metadata extract). Exposed so the iam acr-floor and tests
// can assert the floor deterministically without a live mTLS peer — the mirror of
// WithCertIdentity for the principal/acr layer. Note: this overwrites any existing
// trusted-principal carrier's acr/trusted with the given values while keeping the
// principal as previously recorded (or the system fallback).
func WithTrustedACR(ctx context.Context, acr string, trusted bool) context.Context {
	tp := trustedPrincipal{principal: operations.SystemPrincipal()}
	if v, ok := ctx.Value(trustedPrincipalCtxKey{}).(trustedPrincipal); ok {
		tp = v
	}
	tp.acr = acr
	tp.trusted = trusted
	return context.WithValue(ctx, trustedPrincipalCtxKey{}, tp)
}

// TrustedACRFromContext returns the forwarded JWT `acr` and whether it is trusted
// under the FD-4 invariant (sub-phase 5.4). trusted=false means the acr came from
// an unverified peer on an mTLS listener (or no acr was carried) and an acr-floor
// must treat it as absent (rank 0, fail-closed). On the insecure dev listener the
// acr is accepted as today (back-compat), consistent with the principal.
func TrustedACRFromContext(ctx context.Context) (acr string, trusted bool) {
	if ctx == nil {
		return "", false
	}
	if v, ok := ctx.Value(trustedPrincipalCtxKey{}).(trustedPrincipal); ok {
		return v.acr, v.trusted
	}
	return "", false
}
