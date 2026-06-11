package grpcsrv_test

// cert_identity_test.go — SEC-B identity-extractor unit tests (FD-5: opaque SAN
// string; FD-4 trust-invariant helpers). The pure extractor works on a parsed
// *x509.Certificate; the interceptor wiring is covered in
// cert_identity_bufconn_test.go.

import (
	"context"
	"crypto/x509"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
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
