// Package check implements archgraph's blocking architecture checks —
// the verdicts arch-audit turns into a CI exit code.
//
// archgraph defines four checks:
//
//   - C1 — completeness: the set of a repository's entry-points must be
//     in bijection with the anchors of its L2 functionality notes;
//   - C2 — reachability (Task 6);
//   - C3 — freshness (Task 7);
//   - C4 — doc-coverage: every repo-owned function, method, package
//     variable and constant must carry a Go doc-comment.
//
// This file holds the cross-check vocabulary every check shares: a
// Finding (one machine-checked deviation, with deterministic text) and
// a Result (a check's verdict — passed/failed, its findings, any
// advisory hints and a one-line summary). C1 is implemented in c1.go;
// C2, C3 and C4 reuse Finding and Result unchanged.
//
// The package depends only on the standard library and archgraph's own
// entrypoints and note packages; it imports no gRPC, persistence or
// transport code.
package check

// Check identifies which architecture check produced a Finding. The
// values are the stable C1/C2/C3 labels arch-audit prints.
type Check string

const (
	// C1 marks a completeness finding.
	C1 Check = "C1"
	// C2 marks a reachability finding (Task 6).
	C2 Check = "C2"
	// C3 marks a freshness finding (Task 7).
	C3 Check = "C3"
	// C4 marks a doc-coverage finding — an undocumented function,
	// method, package variable or constant.
	C4 Check = "C4"
)

// Finding is one machine-detected architecture deviation. It is the
// shared currency of every check: C1 reports undocumented entry-points,
// stale anchors and duplicate anchors as Findings; C2, C3 and C4 reuse
// the type unchanged.
//
// Symbol is the offending entity — a gRPC method FQN, a worker type
// name, an entry-point name, or a package-qualified symbol — and is the
// deterministic sort key for a check's finding list. Reason is the
// full, self-contained human-readable explanation, exactly as it
// appears in the audit output. Check is structured metadata so a
// combined-verdict pass can group findings without parsing text.
type Finding struct {
	// Check is the check that produced the finding (C1/C2/C3/C4). It is
	// metadata for grouping; it is not part of the printed line.
	Check Check
	// Symbol is the offending entity; it is the sort key within a
	// check's finding list.
	Symbol string
	// Reason is the full explanation, deterministic and self-contained.
	Reason string
}

// String renders the finding as one audit-output line. archgraph prints
// the bare reason — deterministic and self-contained — so CI can assert
// on it directly; the Check label is conveyed by the surrounding
// summary line, not repeated per finding.
func (f Finding) String() string {
	return f.Reason
}

// Result is the verdict of a single check: whether it passed, the
// findings that explain a failure, any advisory hint lines (non-fatal
// observations that do not by themselves fail the check) and a one-line
// human-readable summary.
//
// Findings are ordered deterministically by their Symbol; Hints are
// ordered deterministically by text. A passing Result has no findings;
// it may still carry hints.
type Result struct {
	// Passed reports whether the check found no blocking deviation.
	Passed bool
	// Findings are the blocking deviations, sorted by Symbol.
	Findings []Finding
	// Hints are advisory, non-blocking observations, sorted by text.
	Hints []string
	// Summary is the one-line verdict, e.g.
	// "C1 completeness: PASS (3/3 entry-points anchored)".
	Summary string
}
