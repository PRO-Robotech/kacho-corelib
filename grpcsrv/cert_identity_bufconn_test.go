// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package grpcsrv_test

// cert_identity_bufconn_test.go — identity-extractor + trust-invariant over real
// mTLS (bufconn). Verifies SAN→identity downstream, principal trusted under
// verified cert, principal NOT trusted without verified cert (handshake-reject
// transitively), and insecure listener accepts principal as today.

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/PRO-Robotech/kacho-corelib/grpcclient"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// captured holds the server-side ctx state observed inside the handler chain,
// after the cert-identity + principal interceptors have run.
type captured struct {
	mu             sync.Mutex
	certIdentity   string
	certVerified   bool
	principal      operations.Principal
	principalTrust bool
}

func (c *captured) set(certID string, certVerified bool, p operations.Principal, trusted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.certIdentity, c.certVerified, c.principal, c.principalTrust = certID, certVerified, p, trusted
}

func (c *captured) get() (string, bool, operations.Principal, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.certIdentity, c.certVerified, c.principal, c.principalTrust
}

// captureInterceptor runs LAST in the chain: reads cert-identity + trusted
// principal off ctx and records them for assertions.
func captureInterceptor(cap *captured) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		certID, verified := grpcsrv.CertIdentityFromContext(ctx)
		p, trusted := grpcsrv.TrustedPrincipalFromContext(ctx)
		cap.set(certID, verified, p, trusted)
		return handler(ctx, req)
	}
}

// serveBufconnNet starts a TLS-or-insecure server with the interceptor
// chain: CertIdentity → trust-aware PrincipalExtract → capture.
func serveBufconnNet(t *testing.T, cap *captured, credsOpt grpc.ServerOption) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	lis := bufconn.Listen(bufconnBufSize)
	srv := grpcsrv.NewServer(
		credsOpt,
		grpc.ChainUnaryInterceptor(
			grpcsrv.UnaryCertIdentityExtract(),
			grpcsrv.UnaryTrustedPrincipalExtract(),
			captureInterceptor(cap),
		),
	)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	t.Cleanup(func() { _ = lis.Close() })
	return func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }
}

// callHealthWithMD dials with the given creds + optional principal metadata.
func callHealthWithMD(t *testing.T, dialer func(context.Context, string) (net.Conn, error), credsOpt grpc.DialOption, principalID string) error {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(dialer), credsOpt)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if principalID != "" {
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(
			grpcsrv.MDKeyPrincipalType, "user",
			grpcsrv.MDKeyPrincipalID, principalID,
			grpcsrv.MDKeyPrincipalDisplay, principalID+"@example.com",
		))
	}
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	return err
}

// --- verified client-cert ⇒ cert-identity extracted downstream
//
//	AND principal-metadata trusted (both available, orthogonal).
func TestSECB12_15_VerifiedCert_IdentityAndPrincipalTrusted(t *testing.T) {
	cap := &captured{}
	ca := newTestCA(t, "kacho-internal-ca")
	caPath := ca.caFile(t)
	srvCrt, srvKey := ca.issueLeaf(t, leafOpts{commonName: testServerSAN, dnsNames: []string{testServerSAN}, isServer: true})
	cliCrt, cliKey := ca.issueLeaf(t, leafOpts{commonName: "kacho-compute", uriSANs: []string{testClientSAN}})

	dialer := serveBufconnNet(t, cap,
		mustServerCreds(t, grpcsrv.TLSServer{Enable: true, CertFile: srvCrt, KeyFile: srvKey, ClientCAFiles: []string{caPath}}),
	)
	err := callHealthWithMD(t, dialer, mustClientCreds(t, grpcclient.TLSClient{
		Enable: true, CertFile: cliCrt, KeyFile: cliKey, CAFiles: []string{caPath}, ServerName: testServerSAN,
	}), "usr-alice")
	require.NoError(t, err)

	certID, verified, p, trusted := cap.get()
	require.Equal(t, testClientSAN, certID, "cert-identity must be the verified SAN")
	require.True(t, verified, "peer must be mTLS-verified")
	require.True(t, trusted, "principal-metadata trusted under verified cert")
	require.Equal(t, "usr-alice", p.ID, "principal carried downstream alongside cert-identity")
}

// --- without a verified client-cert on a require-and-verify listener the
//
//	connection is rejected transitively → Unavailable; principal never reaches authz.
func TestSECB16_NoClientCert_Rejected(t *testing.T) {
	cap := &captured{}
	ca := newTestCA(t, "kacho-internal-ca")
	caPath := ca.caFile(t)
	srvCrt, srvKey := ca.issueLeaf(t, leafOpts{commonName: testServerSAN, dnsNames: []string{testServerSAN}, isServer: true})

	dialer := serveBufconnNet(t, cap,
		mustServerCreds(t, grpcsrv.TLSServer{Enable: true, CertFile: srvCrt, KeyFile: srvKey, ClientCAFiles: []string{caPath}}),
	)
	err := callHealthWithMD(t, dialer, mustClientCreds(t, grpcclient.TLSClient{
		Enable: true, CAFiles: []string{caPath}, ServerName: testServerSAN,
	}), "usr-mallory")
	require.Error(t, err, "require-and-verify must reject the cert-less client")
	require.Equal(t, codes.Unavailable, status.Code(err))

	_, _, _, trusted := cap.get()
	require.False(t, trusted, "untrusted peer principal must never reach the handler")
}

// --- insecure listener accepts principal as today (dev backward-compat).
func TestSECB17_InsecureListener_AcceptsPrincipal(t *testing.T) {
	cap := &captured{}
	dialer := serveBufconnNet(t, cap, mustServerCreds(t, grpcsrv.TLSServer{Enable: false}))
	err := callHealthWithMD(t, dialer, mustClientCreds(t, grpcclient.TLSClient{Enable: false}), "usr-dev")
	require.NoError(t, err)

	certID, verified, p, trusted := cap.get()
	require.Equal(t, "", certID, "no cert on insecure listener")
	require.False(t, verified, "insecure listener has no mTLS verification")
	require.True(t, trusted, "insecure dev-listener accepts principal as today")
	require.Equal(t, "usr-dev", p.ID)
}
