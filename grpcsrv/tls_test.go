// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package grpcsrv_test

// tls_test.go — unit tests for grpcsrv TLSServer config struct + the
// TLSServerCreds helper. Handshake behavior is covered in tls_bufconn_test.go.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/config"
	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
)

// TLSServer exists with the contract fields, and is a HORIZONTAL value-struct
// WITHOUT absolute envconfig tags. A service composes it under its own per-edge
// field, and the parent field name supplies the env prefix segment — so the same
// struct embedded under two different parent fields resolves to two independent,
// per-edge-prefixed sets of env vars following the KACHO_<DOMAIN>_<EDGE>_<NAME>
// convention.
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

	// True per-edge prefixing: the service owns the env name by choosing the
	// parent field + an empty-prefix config.Load. Two server-edges in one process
	// (e.g. a public listener and an admin listener) MUST be independent — an
	// absolute tag would collapse every embedding onto the same
	// KACHO_VPC_TLS_SERVER_* names.
	type svcConfig struct {
		Public grpcsrv.TLSServer
		Admin  grpcsrv.TLSServer
	}
	// Distinct env-prefixed blocks per edge; KACHO_<DOMAIN>_<EDGE>_<NAME>.
	t.Setenv("KACHO_VPC_PUBLIC_ENABLE", "true")
	t.Setenv("KACHO_VPC_PUBLIC_CERTFILE", "/public.crt")
	t.Setenv("KACHO_VPC_PUBLIC_KEYFILE", "/public.key")
	t.Setenv("KACHO_VPC_PUBLIC_CLIENTCAFILES", "/pub-ca1.crt,/pub-ca2.crt")
	t.Setenv("KACHO_VPC_ADMIN_ENABLE", "false")
	t.Setenv("KACHO_VPC_ADMIN_CERTFILE", "/admin.crt")
	t.Setenv("KACHO_VPC_ADMIN_KEYFILE", "/admin.key")
	t.Setenv("KACHO_VPC_ADMIN_CLIENTCAFILES", "/adm-ca.crt")

	var c svcConfig
	require.NoError(t, config.LoadPrefixed("KACHO_VPC", &c))

	// Public edge resolved from its own prefix.
	require.True(t, c.Public.Enable)
	require.Equal(t, "/public.crt", c.Public.CertFile)
	require.Equal(t, "/public.key", c.Public.KeyFile)
	require.Equal(t, []string{"/pub-ca1.crt", "/pub-ca2.crt"}, c.Public.ClientCAFiles)

	// Admin edge resolved INDEPENDENTLY — proves per-edge prefixing, not a shared
	// absolute env name.
	require.False(t, c.Admin.Enable)
	require.Equal(t, "/admin.crt", c.Admin.CertFile)
	require.Equal(t, "/admin.key", c.Admin.KeyFile)
	require.Equal(t, []string{"/adm-ca.crt"}, c.Admin.ClientCAFiles)
}

// --- TLSServer.enable=false ⇒ insecure server-option, no error,
//
//	cert files NOT read (paths can point at nonexistent files).
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

// --- zero-value TLSServer ⇒ insecure (backward-compat merge guard).
func TestSECB04_ZeroValueServer_Insecure(t *testing.T) {
	opt, err := grpcsrv.TLSServerCreds(grpcsrv.TLSServer{})
	require.NoError(t, err)
	require.NotNil(t, opt)
	require.False(t, grpcsrv.TLSServer{}.Enable, "zero-value enable must be false")
}

// --- enable=true + unreadable cert ⇒ error (fail-closed, no fallback).
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
		require.Error(t, err, "enable=true with empty client_ca_files must error")
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

// --- enable=true + valid files ⇒ valid server-option, no error.
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
