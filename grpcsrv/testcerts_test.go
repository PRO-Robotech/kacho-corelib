// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package grpcsrv_test

// testcerts_test.go — ephemeral test-CA + cert generation for mTLS bufconn tests.
// NOTHING here is committed cert material: every CA/leaf is generated in memory at
// test time (no secrets in git; test cert material is generated, not stored).
// Written to a per-test temp dir so the corelib TLS-helpers (which read files) can
// load them.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testCA — an ephemeral self-signed CA usable to sign leaf certs and to act as a
// trust anchor (written to disk as <name>-ca.crt).
type testCA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

func newTestCA(t *testing.T, commonName string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
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
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return &testCA{cert: cert, key: key, certPEM: certPEM}
}

// caFile writes the CA cert PEM to a temp file, returns the path.
func (ca *testCA) caFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.crt")
	require.NoError(t, os.WriteFile(p, ca.certPEM, 0o600))
	return p
}

// leafOpts describes a leaf cert to issue from a testCA.
type leafOpts struct {
	commonName string
	dnsNames   []string
	uriSANs    []string // raw URIs, e.g. spiffe://kacho.cloud/ns/...
	isServer   bool     // server-auth EKU vs client-auth EKU
}

// issueLeaf signs a leaf cert from the CA, returns (certFilePath, keyFilePath).
func (ca *testCA) issueLeaf(t *testing.T, o leafOpts) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	uris := make([]*url.URL, 0, len(o.uriSANs))
	for _, raw := range o.uriSANs {
		u, perr := url.Parse(raw)
		require.NoError(t, perr)
		uris = append(uris, u)
	}

	eku := x509.ExtKeyUsageClientAuth
	if o.isServer {
		eku = x509.ExtKeyUsageServerAuth
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: o.commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		DNSNames:     o.dnsNames,
		URIs:         uris,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)

	dir := t.TempDir()
	certFile = filepath.Join(dir, "leaf.crt")
	keyFile = filepath.Join(dir, "leaf.key")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	require.NoError(t, os.WriteFile(certFile, certPEM, 0o600))

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	require.NoError(t, os.WriteFile(keyFile, keyPEM, 0o600))

	return certFile, keyFile
}
