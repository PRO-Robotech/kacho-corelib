package grpcsrv_test

// tls_test.go — SEC-B unit tests for grpcsrv TLSServer config struct + the
// TLSServerCreds helper (FD-1/FD-2/FD-6/FD-7). Handshake behavior is covered in
// tls_bufconn_test.go.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/config"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
)

// --- SEC-B-01: TLSServer exists with the contract fields, loadable via config.
func TestSECB01_TLSServer_Fields_AndConfigLoad(t *testing.T) {
	// field/type contract — compile-time assertion via struct literal.
	cfg := grpcsrv.TLSServer{
		Enable:        true,
		CertFile:      "/c.crt",
		KeyFile:       "/c.key",
		ClientCAFiles: []string{"/ca.crt"},
	}
	require.True(t, cfg.Enable)
	require.Equal(t, "/c.crt", cfg.CertFile)
	require.Equal(t, "/c.key", cfg.KeyFile)
	require.Equal(t, []string{"/ca.crt"}, cfg.ClientCAFiles)

	// loadable by the existing config mechanism (envconfig) with KACHO_<DOMAIN>_* tags.
	type svcConfig struct {
		TLS grpcsrv.TLSServer
	}
	t.Setenv("KACHO_VPC_TLS_SERVER_ENABLE", "true")
	t.Setenv("KACHO_VPC_TLS_SERVER_CERT_FILE", "/srv.crt")
	t.Setenv("KACHO_VPC_TLS_SERVER_KEY_FILE", "/srv.key")
	t.Setenv("KACHO_VPC_TLS_SERVER_CLIENT_CA_FILES", "/ca1.crt,/ca2.crt")

	var c svcConfig
	require.NoError(t, config.Load(&c))
	require.True(t, c.TLS.Enable)
	require.Equal(t, "/srv.crt", c.TLS.CertFile)
	require.Equal(t, "/srv.key", c.TLS.KeyFile)
	require.Equal(t, []string{"/ca1.crt", "/ca2.crt"}, c.TLS.ClientCAFiles)
}

// --- SEC-B-02/18: TLSServer.enable=false ⇒ insecure server-option, no error,
//     cert files NOT read (paths can point at nonexistent files).
func TestSECB02Unit_DisabledServer_Insecure_NoFileRead(t *testing.T) {
	opt, err := grpcsrv.TLSServerCreds(grpcsrv.TLSServer{
		Enable:        false,
		CertFile:      "/nonexistent.crt",
		KeyFile:       "/nonexistent.key",
		ClientCAFiles: []string{"/nonexistent-ca.crt"},
	})
	require.NoError(t, err, "enable=false must not read cert files / must not error")
	require.NotNil(t, opt)
}

// --- SEC-B-04: zero-value TLSServer ⇒ insecure (backward-compat merge guard).
func TestSECB04_ZeroValueServer_Insecure(t *testing.T) {
	opt, err := grpcsrv.TLSServerCreds(grpcsrv.TLSServer{})
	require.NoError(t, err)
	require.NotNil(t, opt)
	require.False(t, grpcsrv.TLSServer{}.Enable, "zero-value enable must be false (FD-1)")
}

// --- SEC-B-11: enable=true + unreadable cert ⇒ error (fail-closed, no fallback).
func TestSECB11_MisconfiguredServer_Error(t *testing.T) {
	t.Run("nonexistent_cert", func(t *testing.T) {
		_, err := grpcsrv.TLSServerCreds(grpcsrv.TLSServer{
			Enable:        true,
			CertFile:      "/nonexistent.crt",
			KeyFile:       "/nonexistent.key",
			ClientCAFiles: []string{"/nonexistent-ca.crt"},
		})
		require.Error(t, err, "unreadable cert with enable=true must error (no insecure fallback)")
	})

	t.Run("empty_client_ca", func(t *testing.T) {
		ca := newTestCA(t, "ca")
		srvCrt, srvKey := ca.issueLeaf(t, leafOpts{commonName: "s", dnsNames: []string{"s"}, isServer: true})
		_, err := grpcsrv.TLSServerCreds(grpcsrv.TLSServer{
			Enable:        true,
			CertFile:      srvCrt,
			KeyFile:       srvKey,
			ClientCAFiles: nil, // RequireAndVerifyClientCert needs a client-CA bundle
		})
		require.Error(t, err, "enable=true with empty client_ca_files must error (FD-2 RequireAndVerify)")
	})

	t.Run("unreadable_client_ca", func(t *testing.T) {
		ca := newTestCA(t, "ca")
		srvCrt, srvKey := ca.issueLeaf(t, leafOpts{commonName: "s", dnsNames: []string{"s"}, isServer: true})
		_, err := grpcsrv.TLSServerCreds(grpcsrv.TLSServer{
			Enable:        true,
			CertFile:      srvCrt,
			KeyFile:       srvKey,
			ClientCAFiles: []string{"/nonexistent-ca.crt"},
		})
		require.Error(t, err, "unreadable client-CA must error")
	})

	t.Run("garbage_ca_pem", func(t *testing.T) {
		ca := newTestCA(t, "ca")
		srvCrt, srvKey := ca.issueLeaf(t, leafOpts{commonName: "s", dnsNames: []string{"s"}, isServer: true})
		bad := filepath.Join(t.TempDir(), "garbage-ca.crt")
		require.NoError(t, os.WriteFile(bad, []byte("not a pem"), 0o600))
		_, err := grpcsrv.TLSServerCreds(grpcsrv.TLSServer{
			Enable:        true,
			CertFile:      srvCrt,
			KeyFile:       srvKey,
			ClientCAFiles: []string{bad},
		})
		require.Error(t, err, "non-PEM client-CA must error")
	})
}

// --- SEC-B-18: enable=true + valid files ⇒ valid server-option, no error.
func TestSECB18_ServerCreds_ValidFiles_OK(t *testing.T) {
	ca := newTestCA(t, "ca")
	caPath := ca.caFile(t)
	srvCrt, srvKey := ca.issueLeaf(t, leafOpts{commonName: "s", dnsNames: []string{"s"}, isServer: true})
	opt, err := grpcsrv.TLSServerCreds(grpcsrv.TLSServer{
		Enable: true, CertFile: srvCrt, KeyFile: srvKey, ClientCAFiles: []string{caPath},
	})
	require.NoError(t, err)
	require.NotNil(t, opt)
}
