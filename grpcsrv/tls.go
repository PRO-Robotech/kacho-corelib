// Package grpcsrv — tls.go (SEC-B): opt-in mTLS server-credentials helper.
//
// TLSServerCreds is the single source of truth for assembling server-side TLS
// transport credentials for inter-service gRPC (FD-7). It is a server-option
// builder by analogy with the keepalive-helper (KAC-244): NewServer takes the
// returned grpc.ServerOption.
//
// Behavior contract (acceptance SEC-B, FD-1/FD-2/FD-6):
//   - enable=false → insecure server-credentials (current plaintext behavior,
//     dev backward-compat); cert files are NOT read.
//   - enable=true  → mTLS: presents server-cert (cert_file/key_file), verifies
//     client-certs against client_ca_files with ClientAuth =
//     RequireAndVerifyClientCert (server-cert + client-CA).
//   - enable=true + unreadable/garbage cert / empty client-CA → error (fail-closed;
//     never a silent insecure fallback).
//
// SEC-B reads cert files once at startup; rotation = pod restart (epic §6.2,
// hot-reload deliberately out of scope, not a TODO).
package grpcsrv

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// TLSServer is the per-instance (per-edge, FD-3) server-side TLS config. It is a
// plain value struct with no process-wide TLS singleton: every grpcsrv.NewServer
// receives its own TLSServerCreds argument (architecture.md: no global singletons
// outside cmd/).
//
// Field names / env-tags are part of the SEC-B contract (acceptance SEC-B-01).
// The env-tags are full names following the Kachō naming-convention
// (KACHO_<DOMAIN>_<...>); a service embeds this struct under a config field and
// config.Load (envconfig with empty prefix) resolves these via the explicit-tag
// fallback. Distinct edges in one process use distinct env-prefixed config blocks
// (FD-3): one process may run a TLS server and an insecure client simultaneously.
type TLSServer struct {
	// Enable toggles mTLS for this server. Zero-value false ⇒ insecure (FD-1).
	Enable bool `envconfig:"KACHO_VPC_TLS_SERVER_ENABLE"`
	// CertFile is the PEM server-certificate presented to clients.
	CertFile string `envconfig:"KACHO_VPC_TLS_SERVER_CERT_FILE"`
	// KeyFile is the PEM private key for CertFile.
	KeyFile string `envconfig:"KACHO_VPC_TLS_SERVER_KEY_FILE"`
	// ClientCAFiles are PEM CA bundles used to verify presented client-certs.
	ClientCAFiles []string `envconfig:"KACHO_VPC_TLS_SERVER_CLIENT_CA_FILES"`
}

// TLSServerCreds returns the grpc.ServerOption carrying the transport credentials
// for this config. See package doc for the behavior contract.
func TLSServerCreds(cfg TLSServer) (grpc.ServerOption, error) {
	if !cfg.Enable {
		// FD-1: insecure server, cert files NOT read.
		return grpc.Creds(insecure.NewCredentials()), nil
	}

	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("grpcsrv: load server cert/key: %w", err)
	}

	if len(cfg.ClientCAFiles) == 0 {
		// FD-2: RequireAndVerifyClientCert needs a client-CA bundle to verify against.
		return nil, fmt.Errorf("grpcsrv: tls enabled but client_ca_files is empty (RequireAndVerifyClientCert needs a client CA)")
	}
	clientCAs, err := loadCAPool(cfg.ClientCAFiles)
	if err != nil {
		return nil, fmt.Errorf("grpcsrv: load client CA pool: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS12,
	}
	return grpc.Creds(credentials.NewTLS(tlsCfg)), nil
}

// loadCAPool reads PEM CA bundles into an x509.CertPool. An empty/garbage bundle
// (no parseable certificate) is an error — fail-closed (FD-6).
func loadCAPool(files []string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	for _, f := range files {
		pem, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read CA file %q: %w", f, err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no valid PEM certificate in CA file %q", f)
		}
	}
	return pool, nil
}
