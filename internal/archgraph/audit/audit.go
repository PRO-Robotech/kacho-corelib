// Package audit orchestrates archgraph's blocking architecture audit —
// the arch-audit subcommand's verdict.
//
// # What arch-audit is
//
// arch-audit is archgraph's CI-blocking gate. It runs the three
// architecture checks — C1 completeness, C2 dead-code, C3 freshness —
// over a repository and turns their combined verdict into a process
// exit code: zero when every check passes, non-zero when any one fails.
//
// # Run every check, always
//
// arch-audit never short-circuits. A failing C1 does not stop C2 or C3
// from running: a CI run must surface the *complete* set of architecture
// deviations in one pass, so an engineer fixes them all at once rather
// than rediscovering the next one on the next CI cycle. Run therefore
// invokes C1, C2 and C3 unconditionally and aggregates their Results.
//
// # Deterministic, CI-assertable output
//
// The rendered audit report is fully deterministic: findings are grouped
// by check in the fixed order C1 → C2 → C3, and within a check they keep
// the per-check Result's own deterministic sort (C1 by symbol, C2 by
// symbol, C3 by note path). Two runs over an unchanged repository
// produce byte-identical output, so a CI log diff is meaningful.
//
// Every finding line is self-contained — it names the offending object
// (a gRPC method FQN, an exported symbol with its file:line, a note
// path with its hash pair) and the reason — so there is never a bare
// "check failed" with no context.
//
// # One reachability build
//
// C2 (dead-code) and C3 (freshness) both need the SSA-lowered
// reachability graph. Run builds it exactly once — through
// reach.ComputeWithSymbols, which lowers the program to SSA, runs RTA
// and resolves the repository's exported symbols to canonical SSA names
// off that single build — and feeds the one graph to both checks. C1 is
// pure (inventory + notes) and needs no graph.
//
// # Layering
//
// audit depends only on archgraph's own check, reach, entrypoints and
// note packages plus the standard library and golang.org/x/tools. It
// imports no gRPC, persistence or transport code: it is a use-case
// orchestrator, and the cli package is the thin transport layer that
// calls Run and maps its verdict to an exit code.
package audit

import (
	"fmt"
	"io"
	"sort"

	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/check"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
)

// Report is the outcome of an arch-audit run: the combined pass/fail
// verdict and the three per-check Results in the fixed C1 → C2 → C3
// order. It is the value the cli package turns into an exit code and
// the audit text turns into CI-log lines.
type Report struct {
	// Passed is the aggregate verdict: true iff C1, C2 and C3 all
	// passed. A SKIP (a library repo's C2) carries Passed=true, so it
	// never makes the aggregate fail.
	Passed bool
	// C1, C2, C3 are the individual check verdicts, always populated —
	// arch-audit runs every check regardless of any earlier failure.
	C1 check.Result
	C2 check.Result
	C3 check.Result
}

// summaryLine is the single aggregate verdict line of an audit report,
// e.g. "arch-audit: PASS (C1 PASS, C2 PASS, C3 PASS)" or
// "arch-audit: FAIL (C1 PASS, C2 FAIL, C3 PASS)". It is the line CI
// asserts on; the per-check verdict tokens are derived from each
// Result.Passed, never parsed from a Result's own free-text Summary.
func (r Report) summaryLine() string {
	verdict := "PASS"
	if !r.Passed {
		verdict = "FAIL"
	}
	return fmt.Sprintf("arch-audit: %s (C1 %s, C2 %s, C3 %s)",
		verdict,
		passToken(r.C1.Passed),
		passToken(r.C2.Passed),
		passToken(r.C3.Passed))
}

// passToken renders a single check's verdict token for the aggregate
// summary line.
func passToken(passed bool) string {
	if passed {
		return "PASS"
	}
	return "FAIL"
}

// Run executes the arch-audit pass: it runs C1 completeness, C2
// dead-code and C3 freshness over the repository, aggregates their
// verdicts into a Report and returns it.
//
// All three checks run unconditionally — Run never short-circuits on an
// earlier failure, so the Report always carries a complete C1/C2/C3
// picture (scenario 4.0-I2).
//
// repoRoot is the repository's module root (used by C2/C3 to render
// positions repository-relative); pkgs the repository's loaded
// packages; inv its entry-point inventory; notes its L2 functionality
// notes. C2 and C3 share a single reachability build computed here.
//
// Run returns an error only on an internal analysis failure (a
// reachability build that cannot be completed) — never on an
// architecture deviation, which is reported through the Report.
func Run(
	repoRoot string,
	pkgs []*packages.Package,
	inv *entrypoints.Inventory,
	notes []*note.Note,
) (Report, error) {
	// C1 completeness is pure — inventory against note anchors — and
	// needs no reachability graph.
	c1 := check.CheckC1(inv, notes)

	// C2 and C3 both consume the reachability graph. Build it once,
	// here: BuildAuditReach lowers the program to SSA a single time,
	// runs RTA and resolves C2's exported symbols off the same build.
	// The one AuditReach feeds C2's verdict and the one *reach.Graph
	// feeds C3 — the program is never lowered twice per audit run
	// (the Task 7 review item).
	ar, err := check.BuildAuditReach(repoRoot, pkgs, inv)
	if err != nil {
		return Report{}, fmt.Errorf("arch-audit: %w", err)
	}

	// C2 dead-code over the shared build.
	c2 := check.CheckC2WithReach(ar)

	// C3 freshness over the same reachability graph.
	c3 := check.CheckC3(repoRoot, notes, ar.Graph)

	return Report{
		Passed: c1.Passed && c2.Passed && c3.Passed,
		C1:     c1,
		C2:     c2,
		C3:     c3,
	}, nil
}

// WriteReport renders report to w in archgraph's deterministic,
// CI-assertable layout:
//
//	<C1 summary line>
//	  <C1 finding>            (zero or more, sorted)
//	  <C1 hint>               (zero or more, sorted)
//	<C2 summary line>
//	  ...
//	<C3 summary line>
//	  ...
//	arch-audit: PASS|FAIL (C1 ..., C2 ..., C3 ...)
//
// Findings are grouped strictly by check in the fixed order C1 → C2 →
// C3; within a check the per-check Result's own deterministic order is
// preserved. The trailing aggregate line is the CI gate's verdict.
// Two runs over an unchanged repository emit byte-identical output.
func WriteReport(w io.Writer, report Report) {
	for _, res := range []check.Result{report.C1, report.C2, report.C3} {
		writeCheck(w, res)
	}
	writeLine(w, report.summaryLine())
}

// writeCheck renders one check's verdict: its summary line, then every
// finding, then every advisory hint, each detail line indented two
// spaces. The Result's Findings and Hints are already sorted
// deterministically by the check that produced them.
func writeCheck(w io.Writer, res check.Result) {
	writeLine(w, res.Summary)
	for _, f := range res.Findings {
		writeLine(w, "  "+f.String())
	}
	for _, h := range res.Hints {
		writeLine(w, "  "+h)
	}
}

// hintLines returns the entry-point discovery hints (B5 unrecognised
// workers) as deterministic, indented audit-output lines. The
// inventory's hints are sorted by message so two runs render them in an
// identical order. A nil inventory or one with no hints yields nil.
func hintLines(inv *entrypoints.Inventory) []string {
	if inv == nil || len(inv.Hints) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(inv.Hints))
	for _, h := range inv.Hints {
		msgs = append(msgs, h.Message)
	}
	sort.Strings(msgs)
	lines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		lines = append(lines, "  "+m)
	}
	return lines
}

// writeInventoryHints renders the B5 unrecognised-worker hints of inv,
// each on its own indented line. Nothing is written when there are no
// hints.
func writeInventoryHints(w io.Writer, inv *entrypoints.Inventory) {
	for _, line := range hintLines(inv) {
		writeLine(w, line)
	}
}

// WriteReportWithHints renders the audit report preceded by the
// entry-point discovery hints (B5): the inventory's unrecognised-worker
// observations, then the C1/C2/C3 verdicts, then the aggregate line.
// The hints are advisory — they do not change the verdict — but they
// belong in the CI log next to the verdict they contextualise.
func WriteReportWithHints(
	w io.Writer,
	report Report,
	inv *entrypoints.Inventory,
) {
	writeInventoryHints(w, inv)
	WriteReport(w, report)
}

// writeLine writes s followed by a newline. A failure writing to the
// process's own output stream is unrecoverable and intentionally
// ignored — there is no second channel to report it on.
func writeLine(w io.Writer, s string) {
	_, _ = io.WriteString(w, s+"\n")
}
