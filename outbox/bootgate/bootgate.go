// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package bootgate is the fail-closed boot gate for services that must have a
// working IAM-register delivery path before they accept mutating Creates.
//
// Without it, a service whose register-drainer / IAM-peer is down silently logs
// a Warn and keeps accepting Creates — every resource created in that window
// gets no owner-tuple (the exact "ресурс создан, tuple обвалился" failure mode).
// The gate turns that silent loss into an explicit refusal (fail-closed is safer
// than a grant-leak).
//
// Canonical mode: when --require-iam (KACHO_<SVC>_REQUIRE_IAM=true)
// is set and the IAM-connected drainer is not up, BOTH effects hold —
//   - GuardMutation() returns a refusal (UNAVAILABLE) so Create<Resource> fails,
//   - Ready() reports false so the k8s readiness probe goes NotReady (no mutating
//     traffic is routed to an instance without a working delivery path).
//
// Read RPCs are unaffected — the gate is consulted only on the mutating path.
//
// With --require-iam=false (dev back-compat, local fixtures only) the gate is a
// no-op: always Ready, Create always allowed.
package bootgate

import (
	"sync/atomic"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Config parameterises a Gate.
type Config struct {
	// RequireIAM mirrors the --require-iam flag (KACHO_<SVC>_REQUIRE_IAM). When
	// false the gate is disabled (dev back-compat): Ready()==true, GuardMutation
	// always nil. In production it MUST be true (fail-closed).
	RequireIAM bool
	// Service is the short service name (apps|vpc|compute|nlb|iam) used only for
	// the refusal message; optional.
	Service string
}

// Gate is the fail-closed boot gate. The composition root creates one Gate from
// the --require-iam flag, the drainer wiring calls SetConnected(true) once the
// IAM-connected drainer is up (and false if the peer is lost), and the readiness
// probe + the mutating-Create handler consult Ready()/GuardMutation().
//
// Gate is safe for concurrent use (the connected flag is atomic).
type Gate struct {
	requireIAM bool
	service    string
	connected  atomic.Bool
}

// New constructs a Gate. It starts NOT connected — a require-iam service is
// fail-closed (NotReady, Create refused) until the drainer wiring signals
// SetConnected(true).
func New(cfg Config) *Gate {
	return &Gate{requireIAM: cfg.RequireIAM, service: cfg.Service}
}

// SetConnected records whether the IAM-connected register-drainer/peer is up.
// Call SetConnected(true) once the drainer has established its IAM connection
// (or first successful delivery), and SetConnected(false) if it is lost.
func (g *Gate) SetConnected(connected bool) {
	g.connected.Store(connected)
}

// Ready reports readiness for the k8s readiness probe. When require-iam is off
// the gate is always ready (dev). When on, ready iff the drainer is connected.
func (g *Gate) Ready() bool {
	if !g.requireIAM {
		return true
	}
	return g.connected.Load()
}

// GuardMutation returns a refusal error if mutating Creates must be rejected
// (require-iam on AND drainer not connected), else nil. Call it first thing in
// every mutating Create/Update/Delete handler that records an owner-tuple intent.
//
// The refusal is UNAVAILABLE (the IAM-register dependency is unavailable — a
// retryable, fail-closed condition for cross-domain dependencies).
func (g *Gate) GuardMutation() error {
	if g.Ready() {
		return nil
	}
	svc := g.service
	if svc == "" {
		svc = "service"
	}
	return status.Errorf(codes.Unavailable,
		"%s refuses mutations: IAM-register delivery path is not connected (require-iam, fail-closed)",
		svc)
}
