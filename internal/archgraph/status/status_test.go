package status_test

// Unit tests for status.Compute — the function that derives an L2 note's
// computed status (implemented | partial | planned) from how well the
// note's anchors match the repository's discovered entry-point inventory.
//
// These tests pin the pure decision table; the arch-gen write-back wiring
// is exercised end-to-end by the integration suite (group H, H1-H4) in
// internal/archgraph/cli.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/status"
)

// inv builds an inventory whose Entries carry the given canonical names.
// Kind is irrelevant to the name-based match Compute performs, so every
// synthetic entry is recorded as an rpc entry-point.
func inv(names ...string) *entrypoints.Inventory {
	out := &entrypoints.Inventory{}
	for _, n := range names {
		out.Entries = append(out.Entries, entrypoints.EntryPoint{
			Kind: entrypoints.KindRPC,
			Name: n,
		})
	}
	return out
}

// rpcNote builds an L2 note carrying the given RPC anchors.
func rpcNote(anchors ...string) *note.Note {
	n := &note.Note{Level: note.LevelFunctionality}
	for _, a := range anchors {
		n.Anchors = append(n.Anchors, note.Anchor{RPC: a})
	}
	return n
}

// workerNote builds an L2 note carrying the given worker anchors.
func workerNote(anchors ...string) *note.Note {
	n := &note.Note{Level: note.LevelFunctionality}
	for _, a := range anchors {
		n.Anchors = append(n.Anchors, note.Anchor{Worker: a})
	}
	return n
}

// Test_Compute_AllAnchorsPresent_Implemented: every anchor of the note
// resolves to an entry-point — the functionality is fully implemented.
func Test_Compute_AllAnchorsPresent_Implemented(t *testing.T) {
	n := rpcNote(
		"kacho.cloud.vpc.v1.NetworkService/Create",
		"kacho.cloud.vpc.v1.NetworkService/Delete",
	)
	in := inv(
		"kacho.cloud.vpc.v1.NetworkService/Create",
		"kacho.cloud.vpc.v1.NetworkService/Delete",
	)
	require.Equal(t, "implemented", status.Compute(n, in))
}

// Test_Compute_SomeAnchorsMissing_Partial: one anchor resolves, one does
// not — the functionality is partially implemented.
func Test_Compute_SomeAnchorsMissing_Partial(t *testing.T) {
	n := rpcNote(
		"kacho.cloud.vpc.v1.NetworkService/Create",
		"kacho.cloud.vpc.v1.NetworkService/Migrate", // not in the inventory
	)
	in := inv("kacho.cloud.vpc.v1.NetworkService/Create")
	require.Equal(t, "partial", status.Compute(n, in))
}

// Test_Compute_NoAnchorsPresent_Planned: the note carries anchors but not
// one of them resolves to an entry-point — the functionality is planned.
func Test_Compute_NoAnchorsPresent_Planned(t *testing.T) {
	n := rpcNote(
		"kacho.cloud.vpc.v1.NetworkService/Migrate",
		"kacho.cloud.vpc.v1.NetworkService/Archive",
	)
	in := inv("kacho.cloud.vpc.v1.NetworkService/Create")
	require.Equal(t, "planned", status.Compute(n, in))
}

// Test_Compute_EmptyAnchors_Planned: a note with an empty anchors list is
// a planned functionality.
func Test_Compute_EmptyAnchors_Planned(t *testing.T) {
	n := rpcNote()
	require.Equal(t, "planned", status.Compute(n, inv("kacho.cloud.vpc.v1.NetworkService/Create")))
}

// Test_Compute_EmptyInventory_Planned: a note with anchors against a
// repository that has no entry-points at all is planned.
func Test_Compute_EmptyInventory_Planned(t *testing.T) {
	n := rpcNote("kacho.cloud.vpc.v1.NetworkService/Create")
	require.Equal(t, "planned", status.Compute(n, inv()))
}

// Test_Compute_WorkerAnchorByTypeName: a worker anchor matches an
// entry-point by the worker type name.
func Test_Compute_WorkerAnchorByTypeName(t *testing.T) {
	n := workerNote("NetworkOutboxWorker")
	require.Equal(t, "implemented", status.Compute(n, inv("NetworkOutboxWorker")))
	require.Equal(t, "planned", status.Compute(n, inv("OtherWorker")))
}

// Test_Compute_MixedAnchors_AllPresent: an rpc anchor and a worker anchor,
// both resolving, yield implemented.
func Test_Compute_MixedAnchors_AllPresent(t *testing.T) {
	n := &note.Note{
		Level: note.LevelFunctionality,
		Anchors: []note.Anchor{
			{RPC: "kacho.cloud.vpc.v1.NetworkService/Create"},
			{Worker: "NetworkOutboxWorker"},
		},
	}
	in := inv("kacho.cloud.vpc.v1.NetworkService/Create", "NetworkOutboxWorker")
	require.Equal(t, "implemented", status.Compute(n, in))
}

// Test_Compute_NilInventory_Planned: a nil inventory is treated as a
// repository with no entry-points — every anchored note is planned.
func Test_Compute_NilInventory_Planned(t *testing.T) {
	n := rpcNote("kacho.cloud.vpc.v1.NetworkService/Create")
	require.Equal(t, "planned", status.Compute(n, nil))
}
