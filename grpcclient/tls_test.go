package grpcclient_test

// tls_test.go — SEC-B unit tests for grpcclient TLSClient config struct + the
// TLSClientCreds helper (FD-1/FD-2/FD-6/FD-7). The end-to-end handshake (incl.
// server_name verification) is exercised in grpcsrv/tls_bufconn_test.go.

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

// --- SEC-B-01: TLSClient exists with the contract fields, loadable via config.
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

	type svcConfig struct {
		TLS grpcclient.TLSClient
	}
	t.Setenv("KACHO_COMPUTE_TLS_CLIENT_ENABLE", "true")
	t.Setenv("KACHO_COMPUTE_TLS_CLIENT_CERT_FILE", "/cli.crt")
	t.Setenv("KACHO_COMPUTE_TLS_CLIENT_KEY_FILE", "/cli.key")
	t.Setenv("KACHO_COMPUTE_TLS_CLIENT_CA_FILES", "/ca1.crt,/ca2.crt")
	t.Setenv("KACHO_COMPUTE_TLS_CLIENT_SERVER_NAME", "iam.kacho.svc")

	var c svcConfig
	require.NoError(t, config.Load(&c))
	require.True(t, c.TLS.Enable)
	require.Equal(t, "/cli.crt", c.TLS.CertFile)
	require.Equal(t, "/cli.key", c.TLS.KeyFile)
	require.Equal(t, []string{"/ca1.crt", "/ca2.crt"}, c.TLS.CAFiles)
	require.Equal(t, "iam.kacho.svc", c.TLS.ServerName)
}

// --- SEC-B-03/18: TLSClient.enable=false ⇒ insecure dial-option, no file read.
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

// --- SEC-B-04: zero-value TLSClient ⇒ insecure (backward-compat merge guard).
func TestSECB04_ZeroValueClient_Insecure(t *testing.T) {
	opt, err := grpcclient.TLSClientCreds(grpcclient.TLSClient{})
	require.NoError(t, err)
	require.NotNil(t, opt)
	require.False(t, grpcclient.TLSClient{}.Enable, "zero-value enable must be false (FD-1)")
}

// --- SEC-B-11: enable=true + unreadable cert/key/ca ⇒ error (fail-closed).
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
		require.Error(t, err, "enable=true with empty server_name must error (FD-2)")
	})
}

// --- SEC-B-18: enable=true + valid files ⇒ valid dial-option, no error.
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
