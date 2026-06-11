package grpcsrv_test

// cert_identity_test.go — SEC-B identity-extractor unit tests (FD-5: opaque SAN
// string; FD-4 trust-invariant helpers). The pure extractor works on a parsed
// *x509.Certificate; the interceptor wiring is covered in
// cert_identity_bufconn_test.go.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/operations"
)

func mustURIs(t *testing.T, raw ...string) []*url.URL {
	t.Helper()
	out := make([]*url.URL, 0, len(raw))
	for _, r := range raw {
		u, err := url.Parse(r)
		require.NoError(t, err)
		out = append(out, u)
	}
	return out
}

// --- SEC-B-12: extract spiffe SAN as the exact opaque string.
func TestSECB12_ExtractIdentity_SpiffeSAN(t *testing.T) {
	const san = "spiffe://kacho.cloud/ns/kacho-system/sa/kacho-compute"
	cert := &x509.Certificate{URIs: mustURIs(t, san)}
	got := grpcsrv.CertIdentity(cert)
	require.Equal(t, san, got, "extractor must return the SAN URI verbatim (FD-5, no parse/resolve)")
}

// --- SEC-B-13: client-cert without a spiffe URI-SAN ⇒ empty identity (deterministic).
func TestSECB13_ExtractIdentity_NoSpiffeSAN_Empty(t *testing.T) {
	t.Run("dns_san_only", func(t *testing.T) {
		cert := &x509.Certificate{DNSNames: []string{"client.kacho.svc"}}
		require.Equal(t, "", grpcsrv.CertIdentity(cert))
	})
	t.Run("cn_only", func(t *testing.T) {
		cert := &x509.Certificate{}
		cert.Subject.CommonName = "kacho-compute"
		require.Equal(t, "", grpcsrv.CertIdentity(cert))
	})
	t.Run("non_kacho_spiffe_uri", func(t *testing.T) {
		cert := &x509.Certificate{URIs: mustURIs(t, "spiffe://other.domain/ns/x/sa/y")}
		require.Equal(t, "", grpcsrv.CertIdentity(cert),
			"non-kacho spiffe URI must not be returned (no foreign-field leak)")
	})
	t.Run("nil_cert", func(t *testing.T) {
		require.Equal(t, "", grpcsrv.CertIdentity(nil), "nil cert must return empty, not panic")
	})
}

// --- SEC-B-14: multiple URI-SANs ⇒ deterministic first kacho-spiffe pick, stable.
func TestSECB14_ExtractIdentity_MultiSAN_Deterministic(t *testing.T) {
	cert := &x509.Certificate{URIs: mustURIs(t,
		"https://not-spiffe.example/x",
		"spiffe://kacho.cloud/ns/kacho-system/sa/kacho-vpc",
		"spiffe://kacho.cloud/ns/kacho-system/sa/kacho-compute",
	)}
	const want = "spiffe://kacho.cloud/ns/kacho-system/sa/kacho-vpc"
	for i := 0; i < 5; i++ {
		require.Equal(t, want, grpcsrv.CertIdentity(cert),
			"first kacho-spiffe SAN must be chosen, stable across calls (FD-5)")
	}
}

// --- SEC-B-16 (unit half): defense-in-depth — without a verified client-cert the
//     cert-identity context carrier must report not-mTLS-verified and empty identity,
//     so a principal-aware layer can drop untrusted principal-metadata (FD-4).
func TestSECB16Unit_NoVerifiedCert_NotTrusted(t *testing.T) {
	// fresh ctx, no cert-identity ever set (no mTLS peer)
	id, verified := grpcsrv.CertIdentityFromContext(context.Background())
	require.Equal(t, "", id)
	require.False(t, verified, "no verified client-cert ⇒ peer must not be mTLS-verified (FD-4)")
}

// --- SEC-B-15/12 (unit half): a ctx carrying a verified cert-identity reports it.
func TestSECB15Unit_VerifiedCert_Carried(t *testing.T) {
	const id = "spiffe://kacho.cloud/ns/kacho-system/sa/kacho-compute"
	ctx := grpcsrv.WithCertIdentity(context.Background(), id, true)
	got, verified := grpcsrv.CertIdentityFromContext(ctx)
	require.Equal(t, id, got)
	require.True(t, verified)
}

// SEC-B-16 (unit defense-in-depth): a TLS peer that REACHES the interceptor chain
// WITHOUT a verified client-cert (credentials.TLSInfo present but empty
// VerifiedChains) must NOT be trusted. RequireAndVerifyClientCert normally rejects
// such a peer at the handshake (covered by the bufconn half), but the interceptor
// is the last line of defense: if a cert-less TLS peer ever reaches it,
// principalIsTrusted / TrustedPrincipalFromContext must report false so
// principal-metadata from that peer is dropped (FD-4). This drives the production
// leaf==nil branch directly, without a live handshake.
func TestSECB16Unit_TLSPeerNoVerifiedCert_PrincipalNotTrusted(t *testing.T) {
	// Peer over TLS but with NO verified client-cert: TLSInfo present, empty
	// VerifiedChains (this is exactly what a cert-less / unverified peer looks like).
	tlsPeer := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{}}}
	require.Empty(t, tlsPeer.AuthInfo.(credentials.TLSInfo).State.VerifiedChains,
		"precondition: no verified chains (no verified client-cert)")

	// Incoming principal-metadata as a malicious/unverified peer would send.
	ctx := peer.NewContext(context.Background(), tlsPeer)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-mallory",
		grpcsrv.MDKeyPrincipalDisplay, "mallory@example.com",
	))

	// Run the real interceptor chain (cert-identity → trust-aware principal),
	// capturing the downstream ctx state the handler would see.
	var (
		gotCertID         string
		gotVerified       bool
		gotTrusted        = true
		gotCarrierPrincID = "<unset>"
	)
	final := func(c context.Context, _ any) (any, error) {
		gotCertID, gotVerified = grpcsrv.CertIdentityFromContext(c)
		_, gotTrusted = grpcsrv.TrustedPrincipalFromContext(c)
		// The standard carrier that use-cases consume must NOT be contaminated by
		// an untrusted principal — it must still fall back to SystemPrincipal.
		gotCarrierPrincID = operations.PrincipalFromContext(c).ID
		return nil, nil
	}
	chained := chainUnary(
		grpcsrv.UnaryCertIdentityExtract(),
		grpcsrv.UnaryTrustedPrincipalExtract(),
	)
	_, err := chained(ctx, nil, nil, final)
	require.NoError(t, err)

	// cert-identity layer: TLS present but unverified ⇒ empty id, not verified.
	require.Equal(t, "", gotCertID, "no verified cert ⇒ empty cert-identity")
	require.False(t, gotVerified, "TLS peer with empty VerifiedChains ⇒ NOT mTLS-verified (FD-4)")

	// trust layer: principal-metadata from an unverified mTLS peer MUST be dropped.
	require.False(t, gotTrusted,
		"principal from unverified TLS peer must NOT be trusted (SEC-B-16 defense-in-depth)")
	// The untrusted principal must never reach the carrier use-cases read from —
	// it stays the system fallback, not the metadata-supplied usr-mallory.
	require.Equal(t, operations.SystemPrincipal().ID, gotCarrierPrincID,
		"untrusted principal must not populate operations.PrincipalFromContext (FD-4)")
	require.NotEqual(t, "usr-mallory", gotCarrierPrincID,
		"the metadata principal-id must not leak into the use-case principal carrier")
}

// chainUnary composes unary server interceptors left-to-right around a final
// handler, mirroring grpc.ChainUnaryInterceptor semantics, so unit tests can
// exercise the real interceptor chain without a server.
func chainUnary(interceptors ...grpc.UnaryServerInterceptor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		chained := handler
		for i := len(interceptors) - 1; i >= 0; i-- {
			ic := interceptors[i]
			next := chained
			chained = func(c context.Context, r any) (any, error) { return ic(c, r, info, next) }
		}
		return chained(ctx, req)
	}
}
