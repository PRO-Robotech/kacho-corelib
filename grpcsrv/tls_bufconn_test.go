// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package grpcsrv_test

// tls_bufconn_test.go — mTLS handshake over a real TLS layer on top of an
// in-memory bufconn listener. The gRPC server uses grpcsrv.TLSServerCreds; the
// client dials with grpcclient.TLSClientCreds + WithContextDialer(bufDialer). The
// TLS handshake is mutual and real — only the transport is in-memory.

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/PRO-Robotech/kacho-corelib/grpcclient"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
)

const (
	testServerSAN  = "test.kacho.svc"
	testClientSAN  = "spiffe://kacho.cloud/ns/kacho-system/sa/kacho-compute"
	bufconnBufSize = 1024 * 1024
)

// serveBufconn starts grpcsrv.NewServer with the given server-option (TLS or none)
// on a bufconn listener, returns a dialer for the client side.
func serveBufconn(t *testing.T, srvOpts ...grpc.ServerOption) func(context.Context, string) (net.Conn, error) {
	t.Helper()
	lis := bufconn.Listen(bufconnBufSize)
	srv := grpcsrv.NewServer(srvOpts...)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	t.Cleanup(func() { _ = lis.Close() })
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
}

// dialBufconn dials the bufconn server with the given transport dial-option.
// `authority` becomes the server name presented over bufconn (must match cert SAN
// when server_name verification is on the dial-option path, but here server_name
// comes from TLSClient.server_name embedded in the creds).
func dialBufconn(t *testing.T, dialer func(context.Context, string) (net.Conn, error), credsOpt grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		credsOpt,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func callHealthCtx(conn *grpc.ClientConn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	return err
}

func mustServerCreds(t *testing.T, cfg grpcsrv.TLSServer) grpc.ServerOption {
	t.Helper()
	opt, err := grpcsrv.TLSServerCreds(cfg)
	require.NoError(t, err)
	return opt
}

func mustClientCreds(t *testing.T, cfg grpcclient.TLSClient) grpc.DialOption {
	t.Helper()
	opt, err := grpcclient.TLSClientCreds(cfg)
	require.NoError(t, err)
	return opt
}

// --- TLSServer.enable=false ⇒ insecure server, insecure client RPC OK.
func TestSECB02_InsecureServer_InsecureClient_RPCOK(t *testing.T) {
	dialer := serveBufconn(t, mustServerCreds(t, grpcsrv.TLSServer{Enable: false}))
	conn := dialBufconn(t, dialer, mustClientCreds(t, grpcclient.TLSClient{Enable: false}))
	require.NoError(t, callHealthCtx(conn), "insecure client→insecure server must work (backward-compat)")
}

// --- TLSClient.enable=false ⇒ insecure dial; RPC to insecure server OK.
func TestSECB03_InsecureClient_DialOK(t *testing.T) {
	dialer := serveBufconn(t, mustServerCreds(t, grpcsrv.TLSServer{Enable: false}))
	// also assert the dial-option equals the documented insecure transport creds path
	conn := dialBufconn(t, dialer, mustClientCreds(t, grpcclient.TLSClient{Enable: false}))
	require.NoError(t, callHealthCtx(conn))
	// extra: explicit insecure creds reaches the same server (control)
	conn2 := dialBufconn(t, dialer, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, callHealthCtx(conn2))
}

// --- both enable=true with valid certs ⇒ handshake OK, RPC passes.
func TestSECB05_MutualTLS_HappyPath(t *testing.T) {
	ca := newTestCA(t, "kacho-internal-ca")
	caPath := ca.caFile(t)
	srvCrt, srvKey := ca.issueLeaf(t, leafOpts{commonName: testServerSAN, dnsNames: []string{testServerSAN}, isServer: true})
	cliCrt, cliKey := ca.issueLeaf(t, leafOpts{commonName: "kacho-compute", uriSANs: []string{testClientSAN}})

	dialer := serveBufconn(t, mustServerCreds(t, grpcsrv.TLSServer{
		Enable:        true,
		CertFile:      srvCrt,
		KeyFile:       srvKey,
		ClientCAFiles: []string{caPath},
	}))
	conn := dialBufconn(t, dialer, mustClientCreds(t, grpcclient.TLSClient{
		Enable:     true,
		CertFile:   cliCrt,
		KeyFile:    cliKey,
		CAFiles:    []string{caPath},
		ServerName: testServerSAN,
	}))
	require.NoError(t, callHealthCtx(conn), "mTLS happy-path RPC must succeed")
}

// --- server requires client-cert (RequireAndVerifyClientCert).
func TestSECB06_ServerRequiresClientCert(t *testing.T) {
	ca := newTestCA(t, "kacho-internal-ca")
	caPath := ca.caFile(t)
	srvCrt, srvKey := ca.issueLeaf(t, leafOpts{commonName: testServerSAN, dnsNames: []string{testServerSAN}, isServer: true})

	dialer := serveBufconn(t, mustServerCreds(t, grpcsrv.TLSServer{
		Enable:        true,
		CertFile:      srvCrt,
		KeyFile:       srvKey,
		ClientCAFiles: []string{caPath},
	}))
	// client uses TLS but presents NO client-cert (one-way TLS: server-CA only).
	conn := dialBufconn(t, dialer, mustClientCreds(t, grpcclient.TLSClient{
		Enable:     true,
		CAFiles:    []string{caPath},
		ServerName: testServerSAN,
	}))
	err := callHealthCtx(conn)
	require.Error(t, err, "server must reject a client without a client-cert")
	require.Equal(t, codes.Unavailable, status.Code(err), "handshake-reject ⇒ Unavailable")
}

// --- server_name match passes; mismatch fails.
func TestSECB07_ServerNameMatchAndMismatch(t *testing.T) {
	ca := newTestCA(t, "kacho-internal-ca")
	caPath := ca.caFile(t)
	srvCrt, srvKey := ca.issueLeaf(t, leafOpts{commonName: testServerSAN, dnsNames: []string{testServerSAN}, isServer: true})
	cliCrt, cliKey := ca.issueLeaf(t, leafOpts{commonName: "kacho-compute", uriSANs: []string{testClientSAN}})

	dialer := serveBufconn(t, mustServerCreds(t, grpcsrv.TLSServer{
		Enable: true, CertFile: srvCrt, KeyFile: srvKey, ClientCAFiles: []string{caPath},
	}))

	// match
	okConn := dialBufconn(t, dialer, mustClientCreds(t, grpcclient.TLSClient{
		Enable: true, CertFile: cliCrt, KeyFile: cliKey, CAFiles: []string{caPath}, ServerName: testServerSAN,
	}))
	require.NoError(t, callHealthCtx(okConn), "server_name match ⇒ RPC OK")

	// mismatch
	badConn := dialBufconn(t, dialer, mustClientCreds(t, grpcclient.TLSClient{
		Enable: true, CertFile: cliCrt, KeyFile: cliKey, CAFiles: []string{caPath}, ServerName: "wrong.kacho.svc",
	}))
	err := callHealthCtx(badConn)
	require.Error(t, err, "server_name mismatch must fail handshake")
	require.Equal(t, codes.Unavailable, status.Code(err))
}

// --- client-cert signed by a foreign CA ⇒ handshake fail, Unavailable.
func TestSECB08_ClientCertWrongCA_Unavailable(t *testing.T) {
	internalCA := newTestCA(t, "kacho-internal-ca")
	foreignCA := newTestCA(t, "foreign-ca")
	internalCAPath := internalCA.caFile(t)
	foreignCAPath := foreignCA.caFile(t)

	srvCrt, srvKey := internalCA.issueLeaf(t, leafOpts{commonName: testServerSAN, dnsNames: []string{testServerSAN}, isServer: true})
	// client-cert signed by foreign CA; client still trusts internal CA for server.
	cliCrt, cliKey := foreignCA.issueLeaf(t, leafOpts{commonName: "rogue", uriSANs: []string{testClientSAN}})

	dialer := serveBufconn(t, mustServerCreds(t, grpcsrv.TLSServer{
		Enable: true, CertFile: srvCrt, KeyFile: srvKey, ClientCAFiles: []string{internalCAPath},
	}))
	conn := dialBufconn(t, dialer, mustClientCreds(t, grpcclient.TLSClient{
		Enable: true, CertFile: cliCrt, KeyFile: cliKey,
		CAFiles: []string{internalCAPath, foreignCAPath}, ServerName: testServerSAN,
	}))
	err := callHealthCtx(conn)
	require.Error(t, err, "server must reject client-cert from foreign CA")
	require.Equal(t, codes.Unavailable, status.Code(err), "fail-closed ⇒ Unavailable")
}

// --- server-cert not verifiable by client ⇒ handshake fail.
func TestSECB09_ServerCertWrongCA_Unavailable(t *testing.T) {
	internalCA := newTestCA(t, "kacho-internal-ca")
	foreignCA := newTestCA(t, "foreign-ca")
	internalCAPath := internalCA.caFile(t)

	// server-cert signed by foreign CA (unknown to client).
	srvCrt, srvKey := foreignCA.issueLeaf(t, leafOpts{commonName: testServerSAN, dnsNames: []string{testServerSAN}, isServer: true})
	cliCrt, cliKey := internalCA.issueLeaf(t, leafOpts{commonName: "kacho-compute", uriSANs: []string{testClientSAN}})

	dialer := serveBufconn(t, mustServerCreds(t, grpcsrv.TLSServer{
		Enable: true, CertFile: srvCrt, KeyFile: srvKey, ClientCAFiles: []string{internalCAPath},
	}))
	conn := dialBufconn(t, dialer, mustClientCreds(t, grpcclient.TLSClient{
		Enable: true, CertFile: cliCrt, KeyFile: cliKey, CAFiles: []string{internalCAPath}, ServerName: testServerSAN,
	}))
	err := callHealthCtx(conn)
	require.Error(t, err, "client must reject server-cert from unknown CA")
	require.Equal(t, codes.Unavailable, status.Code(err))
}

// --- enable mismatch both directions ⇒ Unavailable (no silent downgrade).
func TestSECB10_EnableMismatch_BothDirections(t *testing.T) {
	ca := newTestCA(t, "kacho-internal-ca")
	caPath := ca.caFile(t)
	srvCrt, srvKey := ca.issueLeaf(t, leafOpts{commonName: testServerSAN, dnsNames: []string{testServerSAN}, isServer: true})
	cliCrt, cliKey := ca.issueLeaf(t, leafOpts{commonName: "kacho-compute", uriSANs: []string{testClientSAN}})

	t.Run("mtls_server_insecure_client", func(t *testing.T) {
		dialer := serveBufconn(t, mustServerCreds(t, grpcsrv.TLSServer{
			Enable: true, CertFile: srvCrt, KeyFile: srvKey, ClientCAFiles: []string{caPath},
		}))
		conn := dialBufconn(t, dialer, mustClientCreds(t, grpcclient.TLSClient{Enable: false}))
		err := callHealthCtx(conn)
		require.Error(t, err, "insecure client to mTLS server must fail (no downgrade)")
		require.Equal(t, codes.Unavailable, status.Code(err))
	})

	t.Run("insecure_server_mtls_client", func(t *testing.T) {
		dialer := serveBufconn(t, mustServerCreds(t, grpcsrv.TLSServer{Enable: false}))
		conn := dialBufconn(t, dialer, mustClientCreds(t, grpcclient.TLSClient{
			Enable: true, CertFile: cliCrt, KeyFile: cliKey, CAFiles: []string{caPath}, ServerName: testServerSAN,
		}))
		err := callHealthCtx(conn)
		require.Error(t, err, "mTLS client to insecure server must fail (TLS over plaintext)")
		require.Equal(t, codes.Unavailable, status.Code(err))
	})
}
