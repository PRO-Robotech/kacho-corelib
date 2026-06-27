// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package grpcsrv_test

// tls_guard_test.go — anti-regression guard: inter-service gRPC TLS-credentials
// are built ONLY through the corelib helpers (grpcsrv.TLSServerCreds /
// grpcclient.TLSClientCreds). A bare credentials.NewTLS(...) or a hand-rolled
// tls.Config anywhere in corelib production code (outside the two helper files)
// fails this guard.
//
// Scope: production code only (*_test.go excluded — test-CA/cert generation is
// allowed to use crypto/tls / x509 directly).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// allowedTLSHelperFiles — the single source of truth for TLS-credential assembly.
// These two files are permitted to import crypto/tls and call credentials.NewTLS.
var allowedTLSHelperFiles = map[string]bool{
	"tls.go": true, // grpcsrv/tls.go and grpcclient/tls.go (basename match)
}

// bannedTLSTokens — direct credential-assembly that must not appear outside helpers.
var bannedTLSTokens = []string{
	"credentials.NewTLS(",
	"tls.Config{",
}

func TestSECB19_NoDirectTLSConfigOutsideHelper(t *testing.T) {
	// corelib root = grpcsrv/.. (this test lives in grpcsrv).
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	var violations []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			// skip VCS / vendored / hidden dirs
			base := info.Name()
			if base == ".git" || base == "vendor" || strings.HasPrefix(base, ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil // test code may generate certs / tls.Config freely (allow-list)
		}
		if allowedTLSHelperFiles[filepath.Base(path)] {
			return nil // the helper files themselves
		}
		data, rErr := os.ReadFile(path)
		if rErr != nil {
			return rErr
		}
		content := string(data)
		for _, tok := range bannedTLSTokens {
			if strings.Contains(content, tok) {
				rel, _ := filepath.Rel(root, path)
				violations = append(violations, rel+" contains "+tok)
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.Empty(t, violations,
		"TLS-credentials must be built only via grpcsrv.TLSServerCreds / grpcclient.TLSClientCreds; offenders: %v",
		violations)
}
