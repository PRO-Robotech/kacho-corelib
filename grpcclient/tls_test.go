// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package grpcclient_test

// tls_test.go — unit tests for grpcclient TLSClient config struct + the
// TLSClientCreds helper. The end-to-end handshake (incl. server_name
// verification) is exercised in grpcsrv/tls_bufconn_test.go.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/config"
	"github.com/PRO-Robotech/kacho-corelib/grpcclient"
)

// issueCA writes a self-signed CA cert PEM, returns its path.
func issueCA(t *testing.T) (caPath string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	caPath = filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	return caPath, cert, key
}

// issueClientLeaf signs a client-cert from the CA, returns (certPath, keyPath).
func issueClientLeaf(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "kacho-compute"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	require.NoError(t, err)
	dir := t.TempDir()
	certPath = filepath.Join(dir, "client.crt")
	keyPath = filepath.Join(dir, "client.key")
	require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certPath, keyPath
}

// TLSClient exists with the contract fields, and is a HORIZONTAL
// value-struct WITHOUT absolute envconfig tags. A service composes it under
// its own per-edge field; the parent field name supplies the env prefix segment.
// Two distinct dial-edges in one process (e.g. compute→iam and compute→vpc) MUST
// resolve to two INDEPENDENT, per-edge-prefixed env sets — true per-edge
// prefixing, not a single shared absolute env name.
func TestSECB01_TLSClient_Fields_AndConfigLoad(t *testing.T) {
	cfg := grpcclient.TLSClient{
		Enable:     true,
		CertFile:   "/c.crt",
		KeyFile:    "/c.key",
		CAFiles:    []string{"/ca.crt"},
		ServerName: "peer.kacho.svc",
	}
	require.True(t, cfg.Enable)
	require.Equal(t, "/c.crt", cfg.CertFile)
	require.Equal(t, "/c.key", cfg.KeyFile)
	require.Equal(t, []string{"/ca.crt"}, cfg.CAFiles)
	require.Equal(t, "peer.kacho.svc", cfg.ServerName)

	// Two dial-edges under one service, each its own parent field. An absolute
	// tag (KACHO_COMPUTE_TLS_CLIENT_*) would collapse both edges onto the same
	// env names; per-edge prefixing keeps them distinct.
	type svcConfig struct {
		IAM     grpcclient.TLSClient
		VPCPeer grpcclient.TLSClient
	}
	t.Setenv("KACHO_COMPUTE_IAM_ENABLE", "true")
	t.Setenv("KACHO_COMPUTE_IAM_CERTFILE", "/iam-cli.crt")
	t.Setenv("KACHO_COMPUTE_IAM_KEYFILE", "/iam-cli.key")
	t.Setenv("KACHO_COMPUTE_IAM_CAFILES", "/iam-ca1.crt,/iam-ca2.crt")
	t.Setenv("KACHO_COMPUTE_IAM_SERVERNAME", "iam.kacho.svc")
	t.Setenv("KACHO_COMPUTE_VPCPEER_ENABLE", "false")
	t.Setenv("KACHO_COMPUTE_VPCPEER_CERTFILE", "/vpc-cli.crt")
	t.Setenv("KACHO_COMPUTE_VPCPEER_KEYFILE", "/vpc-cli.key")
	t.Setenv("KACHO_COMPUTE_VPCPEER_CAFILES", "/vpc-ca.crt")
	t.Setenv("KACHO_COMPUTE_VPCPEER_SERVERNAME", "vpc.kacho.svc")

	var c svcConfig
	require.NoError(t, config.LoadPrefixed("KACHO_COMPUTE", &c))

	// IAM dial-edge resolved from its own prefix.
	require.True(t, c.IAM.Enable)
	require.Equal(t, "/iam-cli.crt", c.IAM.CertFile)
	require.Equal(t, "/iam-cli.key", c.IAM.KeyFile)
	require.Equal(t, []string{"/iam-ca1.crt", "/iam-ca2.crt"}, c.IAM.CAFiles)
	require.Equal(t, "iam.kacho.svc", c.IAM.ServerName)

	// VPC dial-edge resolved INDEPENDENTLY — proves per-edge prefixing.
	require.False(t, c.VPCPeer.Enable)
	require.Equal(t, "/vpc-cli.crt", c.VPCPeer.CertFile)
	require.Equal(t, "/vpc-cli.key", c.VPCPeer.KeyFile)
	require.Equal(t, []string{"/vpc-ca.crt"}, c.VPCPeer.CAFiles)
	require.Equal(t, "vpc.kacho.svc", c.VPCPeer.ServerName)
}

// --- TLSClient.enable=false ⇒ insecure dial-option, no file read.
func TestSECB03Unit_DisabledClient_Insecure_NoFileRead(t *testing.T) {
	opt, err := grpcclient.TLSClientCreds(grpcclient.TLSClient{
		Enable:     false,
		CertFile:   "/nonexistent.crt",
		KeyFile:    "/nonexistent.key",
		CAFiles:    []string{"/nonexistent-ca.crt"},
		ServerName: "x",
	})
	require.NoError(t, err, "enable=false must not read cert files / must not error")
	require.NotNil(t, opt)
}

// --- zero-value TLSClient ⇒ insecure (backward-compat merge guard).
func TestSECB04_ZeroValueClient_Insecure(t *testing.T) {
	opt, err := grpcclient.TLSClientCreds(grpcclient.TLSClient{})
	require.NoError(t, err)
	require.NotNil(t, opt)
	require.False(t, grpcclient.TLSClient{}.Enable, "zero-value enable must be false")
}

// --- enable=true + unreadable cert/key/ca ⇒ error (fail-closed).
func TestSECB11_MisconfiguredClient_Error(t *testing.T) {
	caPath, caCert, caKey := issueCA(t)
	cliCrt, cliKey := issueClientLeaf(t, caCert, caKey)

	t.Run("nonexistent_cert", func(t *testing.T) {
		_, err := grpcclient.TLSClientCreds(grpcclient.TLSClient{
			Enable: true, CertFile: "/nope.crt", KeyFile: "/nope.key",
			CAFiles: []string{caPath}, ServerName: "s",
		})
		require.Error(t, err)
	})

	t.Run("unreadable_ca", func(t *testing.T) {
		_, err := grpcclient.TLSClientCreds(grpcclient.TLSClient{
			Enable: true, CertFile: cliCrt, KeyFile: cliKey,
			CAFiles: []string{"/nonexistent-ca.crt"}, ServerName: "s",
		})
		require.Error(t, err)
	})

	t.Run("empty_ca", func(t *testing.T) {
		_, err := grpcclient.TLSClientCreds(grpcclient.TLSClient{
			Enable: true, CertFile: cliCrt, KeyFile: cliKey,
			CAFiles: nil, ServerName: "s",
		})
		require.Error(t, err, "enable=true with empty ca_files must error (server-CA required)")
	})

	t.Run("empty_server_name", func(t *testing.T) {
		_, err := grpcclient.TLSClientCreds(grpcclient.TLSClient{
			Enable: true, CertFile: cliCrt, KeyFile: cliKey,
			CAFiles: []string{caPath}, ServerName: "",
		})
		require.Error(t, err, "enable=true with empty server_name must error")
	})
}

// --- enable=true + valid files ⇒ valid dial-option, no error.
func TestSECB18_ClientCreds_ValidFiles_OK(t *testing.T) {
	caPath, caCert, caKey := issueCA(t)
	cliCrt, cliKey := issueClientLeaf(t, caCert, caKey)
	opt, err := grpcclient.TLSClientCreds(grpcclient.TLSClient{
		Enable: true, CertFile: cliCrt, KeyFile: cliKey,
		CAFiles: []string{caPath}, ServerName: "peer.kacho.svc",
	})
	require.NoError(t, err)
	require.NotNil(t, opt)
}

// --- TLSClientTransportCreds is the raw-credentials building block that
// TLSClientCreds wraps — single source of truth for the behavior contract.
// Callers dialing through a builder that takes credentials.TransportCredentials
// (rather than a grpc.DialOption) use it directly (compute→vpc). It must
// honor the same disabled⇒insecure (no file read) / fail-closed contract.
func TestSECM_TLSClientTransportCreds(t *testing.T) {
	t.Run("disabled_insecure_no_file_read", func(t *testing.T) {
		creds, err := grpcclient.TLSClientTransportCreds(grpcclient.TLSClient{
			Enable:   false,
			CertFile: "/nonexistent.crt", KeyFile: "/nonexistent.key",
			CAFiles: []string{"/nonexistent-ca.crt"}, ServerName: "x",
		})
		require.NoError(t, err, "enable=false must not read cert files / must not error")
		require.NotNil(t, creds)
	})

	t.Run("valid_files_ok", func(t *testing.T) {
		caPath, caCert, caKey := issueCA(t)
		cliCrt, cliKey := issueClientLeaf(t, caCert, caKey)
		creds, err := grpcclient.TLSClientTransportCreds(grpcclient.TLSClient{
			Enable: true, CertFile: cliCrt, KeyFile: cliKey,
			CAFiles: []string{caPath}, ServerName: "peer.kacho.svc",
		})
		require.NoError(t, err)
		require.NotNil(t, creds)
	})

	t.Run("enabled_empty_server_name_error", func(t *testing.T) {
		caPath, caCert, caKey := issueCA(t)
		cliCrt, cliKey := issueClientLeaf(t, caCert, caKey)
		_, err := grpcclient.TLSClientTransportCreds(grpcclient.TLSClient{
			Enable: true, CertFile: cliCrt, KeyFile: cliKey,
			CAFiles: []string{caPath}, ServerName: "",
		})
		require.Error(t, err, "enable=true with empty server_name must error")
	})
}
