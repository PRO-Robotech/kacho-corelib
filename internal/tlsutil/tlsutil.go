// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package tlsutil holds module-internal TLS helpers shared by the grpcsrv and
// grpcclient transport-credential builders. It is under internal/ so it is NOT
// part of the public corelib API surface — only kacho-corelib packages may
// import it.
package tlsutil

import (
	"crypto/x509"
	"fmt"
	"os"
)

// LoadCAPool reads PEM CA bundles into an x509.CertPool. An empty/garbage bundle
// (no parseable certificate) is an error — fail-closed. This is the single source
// of truth for CA-pool loading on both the server (client-CA verification) and
// client (server-CA verification) transport edges, so a future hardening of the
// CA policy applies to both edges at once (previously the two copies could
// silently diverge).
func LoadCAPool(files []string) (*x509.CertPool, error) {
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
