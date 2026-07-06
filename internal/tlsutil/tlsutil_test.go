// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package tlsutil_test

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

	"github.com/PRO-Robotech/kacho-corelib/internal/tlsutil"
)

// writeCAPEM генерирует ephemeral self-signed CA-серт и пишет его PEM во временный
// файл, возвращая путь.
func writeCAPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kacho-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write pem: %v", err)
	}
	return path
}

// TestLoadCAPool_ValidPEM — валидный PEM-bundle → непустой pool, без ошибки.
func TestLoadCAPool_ValidPEM(t *testing.T) {
	pool, err := tlsutil.LoadCAPool([]string{writeCAPEM(t)})
	if err != nil {
		t.Fatalf("LoadCAPool: unexpected err: %v", err)
	}
	if pool == nil {
		t.Fatal("LoadCAPool returned nil pool for a valid bundle")
	}
}

// TestLoadCAPool_GarbageIsFailClosed — файл без валидного PEM-серта → ошибка
// (fail-closed, не тихий пустой pool).
func TestLoadCAPool_GarbageIsFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.pem")
	if err := os.WriteFile(path, []byte("not a pem certificate"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tlsutil.LoadCAPool([]string{path}); err == nil {
		t.Fatal("garbage CA bundle must fail-closed (want error, got nil)")
	}
}

// TestLoadCAPool_MissingFileIsFailClosed — несуществующий путь → ошибка чтения.
func TestLoadCAPool_MissingFileIsFailClosed(t *testing.T) {
	if _, err := tlsutil.LoadCAPool([]string{filepath.Join(t.TempDir(), "nope.pem")}); err == nil {
		t.Fatal("missing CA file must fail-closed (want error, got nil)")
	}
}
