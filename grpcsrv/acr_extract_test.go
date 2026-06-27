// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package grpcsrv_test

// acr_extract_test.go — acr-carrying trusted-principal extract.
//
// The api-gateway forwards the validated JWT `acr` claim as the trusted metadata
// key x-kacho-token-acr on the mTLS-verified gateway→iam re-dial (alongside the
// existing x-kacho-principal-*). UnaryTrustedPrincipalExtract carries that acr
// into ctx ONLY under the same trust invariant as the principal: trusted ⟺
// the peer is mTLS-verified. On an unverified TLS peer the forwarded acr is
// DROPPED with the principal (anti-spoof — plumbing half).
//
// These are pure-ctx unit tests (no live TLS handshake) mirroring the existing
// cert_identity_test.go style; the leaf==nil / TLSInfo branch is driven directly.

import (
	"context"
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
)

// --- ACR ranking helper (shared with gateway StepUpGate / iam floor).
func TestACRRank_Ordering(t *testing.T) {
	require.Equal(t, 0, grpcsrv.ACRRank(""), "empty ⇒ rank 0")
	require.Equal(t, 0, grpcsrv.ACRRank("0"))
	require.Equal(t, 1, grpcsrv.ACRRank("1"))
	require.Equal(t, 2, grpcsrv.ACRRank("2"))
	require.Equal(t, 3, grpcsrv.ACRRank("3"))
	require.Equal(t, 0, grpcsrv.ACRRank("garbage"), "unknown ⇒ rank 0 (fail-closed)")
}

// --- ACRSatisfies — required=="" / "0" is a no-op (always satisfied),
//
//	matching the public StepUpGate.Check RequiredACRMin=="" semantics.
func TestACRSatisfies(t *testing.T) {
	t.Run("no_requirement_always_ok", func(t *testing.T) {
		require.True(t, grpcsrv.ACRSatisfies("", ""))
		require.True(t, grpcsrv.ACRSatisfies("0", ""))
		require.True(t, grpcsrv.ACRSatisfies("", "0"), `required "0" ⇒ no requirement`)
	})
	t.Run("met", func(t *testing.T) {
		require.True(t, grpcsrv.ACRSatisfies("2", "2"))
		require.True(t, grpcsrv.ACRSatisfies("3", "2"))
	})
	t.Run("not_met", func(t *testing.T) {
		require.False(t, grpcsrv.ACRSatisfies("1", "2"))
		require.False(t, grpcsrv.ACRSatisfies("", "2"), "absent acr vs required ⇒ fail-closed")
		require.False(t, grpcsrv.ACRSatisfies("garbage", "2"))
	})
}

// --- trusted peer carries acr into ctx.
func TestTrustedACR_VerifiedPeer_Carried(t *testing.T) {
	// mTLS-verified peer: ctx carries a verified cert-identity (set by
	// UnaryCertIdentityExtract on a verified peer). With principal+acr metadata,
	// the trust-aware extract must expose the acr downstream.
	ctx := grpcsrv.WithCertIdentity(context.Background(),
		"spiffe://kacho.cloud/ns/kacho/sa/kacho-api-gateway", true)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-alice",
		grpcsrv.MDKeyTokenACR, "2",
	))

	var (
		gotACR     = "<unset>"
		gotTrusted bool
	)
	final := func(c context.Context, _ any) (any, error) {
		gotACR, gotTrusted = grpcsrv.TrustedACRFromContext(c)
		return nil, nil
	}
	chained := chainUnary(grpcsrv.UnaryTrustedPrincipalExtract())
	_, err := chained(ctx, nil, nil, final)
	require.NoError(t, err)
	require.True(t, gotTrusted, "verified peer ⇒ acr trusted")
	require.Equal(t, "2", gotACR, "trusted acr must be carried from x-kacho-token-acr")
}

// --- unverified TLS peer ⇒ acr dropped (not trusted).
func TestTrustedACR_UnverifiedPeer_Dropped(t *testing.T) {
	// TLS peer with NO verified client-cert: a spoofing peer sends a high acr.
	tlsPeer := &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{}}}
	ctx := peer.NewContext(context.Background(), tlsPeer)
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-mallory",
		grpcsrv.MDKeyTokenACR, "3", // spoofed
	))

	var (
		gotACR     = "<unset>"
		gotTrusted = true
	)
	final := func(c context.Context, _ any) (any, error) {
		gotACR, gotTrusted = grpcsrv.TrustedACRFromContext(c)
		return nil, nil
	}
	chained := chainUnary(
		grpcsrv.UnaryCertIdentityExtract(),
		grpcsrv.UnaryTrustedPrincipalExtract(),
	)
	_, err := chained(ctx, nil, nil, final)
	require.NoError(t, err)
	require.False(t, gotTrusted, "unverified TLS peer ⇒ acr NOT trusted")
	require.Equal(t, "", gotACR, "spoofed acr from unverified peer must be dropped")
}

// --- insecure (dev) listener ⇒ acr accepted as today (back-compat),
//
//	so dev/newman behaviour is byte-identical (no mTLS, principal trusted).
func TestTrustedACR_InsecureListener_Accepted(t *testing.T) {
	// No peer / no TLS at all ⇒ insecure dev listener: principal (and acr)
	// accepted as today.
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-alice",
		grpcsrv.MDKeyTokenACR, "1",
	))
	var (
		gotACR     = "<unset>"
		gotTrusted bool
	)
	final := func(c context.Context, _ any) (any, error) {
		gotACR, gotTrusted = grpcsrv.TrustedACRFromContext(c)
		return nil, nil
	}
	chained := chainUnary(grpcsrv.UnaryTrustedPrincipalExtract())
	_, err := chained(ctx, nil, nil, final)
	require.NoError(t, err)
	require.True(t, gotTrusted, "insecure dev listener ⇒ acr accepted (back-compat)")
	require.Equal(t, "1", gotACR)
}

// --- a ctx that never went through the extract reports no acr.
func TestTrustedACR_NoCtx(t *testing.T) {
	acr, trusted := grpcsrv.TrustedACRFromContext(context.Background())
	require.Equal(t, "", acr)
	require.False(t, trusted)
}

// --- WithTrustedACR test-support helper round-trips acr+trusted.
func TestWithTrustedACR_RoundTrip(t *testing.T) {
	ctx := grpcsrv.WithTrustedACR(context.Background(), "2", true)
	acr, trusted := grpcsrv.TrustedACRFromContext(ctx)
	require.Equal(t, "2", acr)
	require.True(t, trusted)

	// untrusted variant
	ctx2 := grpcsrv.WithTrustedACR(context.Background(), "", false)
	acr2, trusted2 := grpcsrv.TrustedACRFromContext(ctx2)
	require.Equal(t, "", acr2)
	require.False(t, trusted2)
}
