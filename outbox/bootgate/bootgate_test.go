package bootgate_test

// Unit tests for the fail-closed boot gate (sub-phase 1.4, D-8).
//
// RED-phase (test-first, ban #12): `bootgate.*` does not yet exist. Acceptance
// 1.4-08 — a service configured with --require-iam that has no IAM-connected
// drainer must (a) refuse mutating Create (UNAVAILABLE/FAILED_PRECONDITION) and
// (b) report readiness NotReady. Both effects, single canonical mode (N5).
//
// The gate is transport-agnostic corelib glue: a Gate object the composition
// root creates from the --require-iam flag; the drainer wiring marks it
// Connected once the IAM-peer/drainer is up; the readiness probe and the
// mutating-Create guard both consult it.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/outbox/bootgate"
)

// Test_1_4_08_RequireIAM_NoPeer_RefusesMutation_NotReady.
func Test_1_4_08_RequireIAM_NoPeer_RefusesMutation_NotReady(t *testing.T) {
	t.Parallel()
	g := bootgate.New(bootgate.Config{RequireIAM: true, Service: "apps"})

	// Before the drainer connects: NotReady + Create refused.
	assert.False(t, g.Ready(), "require-iam + no peer → NotReady")
	err := g.GuardMutation()
	require.Error(t, err, "mutating Create must be refused")
	code := status.Code(err)
	assert.Truef(t, code == codes.Unavailable || code == codes.FailedPrecondition,
		"refusal must be UNAVAILABLE or FAILED_PRECONDITION, got %s", code)
	assert.Contains(t, status.Convert(err).Message(), "IAM",
		"refusal message names the unavailable IAM-register dependency")

	// After the drainer connects: Ready + Create allowed.
	g.SetConnected(true)
	assert.True(t, g.Ready(), "peer connected → Ready")
	assert.NoError(t, g.GuardMutation(), "Create allowed once connected")

	// Peer drops again: back to fail-closed.
	g.SetConnected(false)
	assert.False(t, g.Ready())
	assert.Error(t, g.GuardMutation())
}

// Test_1_4_08_RequireIAMFalse_DevBackCompat — contrast: --require-iam=false →
// old Warn-mode, Create passes, probe Ready (local fixtures only).
func Test_1_4_08_RequireIAMFalse_DevBackCompat(t *testing.T) {
	t.Parallel()
	g := bootgate.New(bootgate.Config{RequireIAM: false, Service: "apps"})

	assert.True(t, g.Ready(), "require-iam=false → always Ready (dev back-compat)")
	assert.NoError(t, g.GuardMutation(), "require-iam=false → Create passes even without peer")

	// Connection state is irrelevant when the gate is disabled.
	g.SetConnected(false)
	assert.True(t, g.Ready())
	assert.NoError(t, g.GuardMutation())
}
