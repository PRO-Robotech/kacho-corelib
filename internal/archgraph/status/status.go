// Package status computes the lifecycle status of an archgraph L2
// functionality note from how well the note's anchors match the
// repository's discovered entry-point inventory.
//
// An L2 note's "status" field is a *computed* value, not a human-authored
// one: it records whether the functionality the note describes is actually
// wired into the code. arch-gen derives it on every run and writes it back
// into the note's frontmatter, so a hand-edited status is overwritten and
// the divergence is caught by `git diff` in CI.
//
// The status is one of three values:
//
//   - "implemented" — every anchor of the note resolves to a discovered
//     entry-point; the functionality is fully wired in;
//   - "partial"     — some anchors resolve and some do not; the
//     functionality is half-built or half-removed;
//   - "planned"     — the note has no anchors, or none of its anchors
//     resolves to an entry-point; the functionality is not yet in the
//     code.
//
// Anchor-to-entry-point matching is by canonical name: an RPC anchor
// carries the fully-qualified gRPC method name ("<service-FQN>/<method>")
// and a worker anchor the worker type's name — exactly the strings
// entrypoints.EntryPoint.Name holds.
//
// This package depends only on the standard library and archgraph's own
// note and entrypoints packages; it performs no I/O.
package status

import (
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
)

// The three computed status values written into an L2 note's frontmatter.
const (
	// Implemented marks a note whose every anchor resolves to a discovered
	// entry-point.
	Implemented = "implemented"
	// Partial marks a note with both resolved and unresolved anchors.
	Partial = "partial"
	// Planned marks a note with no anchors, or no resolved anchor.
	Planned = "planned"
)

// Compute returns the computed status of note n given the repository's
// entry-point inventory inv:
//
//   - every anchor resolves        → Implemented;
//   - some resolve, some do not    → Partial;
//   - no anchors, or none resolve  → Planned.
//
// An anchor resolves when its canonical name (the RPC method FQN, or the
// worker type name) is the Name of an entry-point in inv. An anchor that
// is neither an RPC nor a worker anchor never resolves. A nil inv is
// treated as a repository with no entry-points: every anchored note is
// then Planned.
func Compute(n *note.Note, inv *entrypoints.Inventory) string {
	if n == nil || len(n.Anchors) == 0 {
		return Planned
	}

	// names: the canonical name of every discovered entry-point.
	names := map[string]struct{}{}
	if inv != nil {
		for _, ep := range inv.Entries {
			names[ep.Name] = struct{}{}
		}
	}

	var resolved, total int
	for _, a := range n.Anchors {
		sym := anchorSymbol(a)
		if sym == "" {
			// An anchor that is neither RPC nor worker contributes to the
			// total but can never resolve — it pulls the verdict towards
			// partial/planned, never implemented.
			total++
			continue
		}
		total++
		if _, ok := names[sym]; ok {
			resolved++
		}
	}

	switch resolved {
	case 0:
		return Planned
	case total:
		return Implemented
	default:
		return Partial
	}
}

// anchorSymbol returns the canonical name an anchor points at: the gRPC
// method FQN for an RPC anchor, the worker type name for a worker anchor,
// or "" for an anchor that is neither.
func anchorSymbol(a note.Anchor) string {
	switch {
	case a.IsRPC():
		return a.RPC
	case a.IsWorker():
		return a.Worker
	default:
		return ""
	}
}
