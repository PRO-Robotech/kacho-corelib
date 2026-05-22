package check

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
)

// CheckC1 runs the completeness check: the set of a repository's
// entry-points must be in bijection with the anchors of its L2
// functionality notes.
//
// The check has three failure modes, each reported as a Finding:
//
//   - undocumented entry-point — an entry-point present in the code is
//     anchored by no note;
//   - stale anchor — an anchor points at a name that is not an
//     entry-point of the code;
//   - duplicate anchor — an entry-point is anchored by two or more
//     notes (an anchor must be unique).
//
// A note whose anchors list is empty (a planned functionality) is
// ignored entirely: it neither contributes coverage nor yields a stale
// finding. When a stale anchor names a worker and the inventory carries
// an unrecognised-worker hint, CheckC1 adds an advisory Hint pointing at
// the New<Name>+Run/Start convention — the stale finding is most likely
// a false positive caused by a worker started off-convention.
//
// CheckC1 is pure: it derives its verdict solely from inv and notes and
// performs no I/O. Findings are sorted by Symbol and Hints by text, so
// the Result is fully deterministic.
func CheckC1(inv *entrypoints.Inventory, notes []*note.Note) Result {
	// entryPoints: the canonical name of every discovered entry-point.
	entryPoints := make(map[string]struct{}, len(inv.Entries))
	for _, ep := range inv.Entries {
		entryPoints[ep.Name] = struct{}{}
	}

	// anchoredBy: for each anchor symbol, the basenames of the notes
	// that anchor it. A note with an empty anchors list contributes
	// nothing. anchorIsWorker records whether an anchor symbol was
	// declared as a worker anchor, so a stale worker anchor can be
	// correlated with an unrecognised-worker hint.
	anchoredBy := map[string][]string{}
	anchorIsWorker := map[string]bool{}
	for _, n := range notes {
		if len(n.Anchors) == 0 {
			continue
		}
		base := filepath.Base(n.Path())
		for _, a := range n.Anchors {
			sym := anchorSymbol(a)
			if sym == "" {
				continue
			}
			anchoredBy[sym] = append(anchoredBy[sym], base)
			if a.IsWorker() {
				anchorIsWorker[sym] = true
			}
		}
	}

	var findings []Finding
	var hints []string

	// Undocumented: an entry-point with no anchoring note.
	for name := range entryPoints {
		if _, ok := anchoredBy[name]; !ok {
			findings = append(findings, Finding{
				Check:  C1,
				Symbol: name,
				Reason: fmt.Sprintf(
					"undocumented entry-point: %s — declare it in an "+
						"L2 note's anchors or remove it", name),
			})
		}
	}

	// Stale / duplicate: walk the anchors.
	for sym, notesForSym := range anchoredBy {
		_, isEntryPoint := entryPoints[sym]
		switch {
		case !isEntryPoint:
			// Stale: the anchor points at a non-existent entry-point.
			// A single note carries the anchor (a name anchored by
			// several notes that all happen to be stale is still each
			// reported once, deterministically, by note basename).
			for _, nb := range dedupSorted(notesForSym) {
				findings = append(findings, Finding{
					Check:  C1,
					Symbol: sym,
					Reason: fmt.Sprintf(
						"stale anchor: %s in docs/arch/%s points to a "+
							"non-existent entry-point", sym, nb),
				})
			}
			// A stale worker anchor next to an unrecognised-worker hint
			// is most likely an off-convention worker, not a real
			// mistake — surface a clarifying hint.
			if anchorIsWorker[sym] && len(inv.Hints) > 0 {
				hints = append(hints, fmt.Sprintf(
					"hint: an anchored worker %s was not discovered as an "+
						"entry-point — verify it follows the "+
						"New<Name>+Run/Start convention", sym))
			}
		case len(notesForSym) > 1:
			// Duplicate: the entry-point is anchored by several notes.
			sorted := dedupSorted(notesForSym)
			findings = append(findings, Finding{
				Check:  C1,
				Symbol: sym,
				Reason: fmt.Sprintf(
					"entry-point %s anchored in %d notes (%s) — "+
						"must be exactly one",
					sym, len(sorted), strings.Join(sorted, ", ")),
			})
		}
	}

	sortFindings(findings)
	sort.Strings(hints)

	passed := len(findings) == 0
	return Result{
		Passed:   passed,
		Findings: findings,
		Hints:    hints,
		Summary:  c1Summary(passed, len(entryPoints)),
	}
}

// anchorSymbol returns the canonical name an anchor points at: the gRPC
// method FQN for an RPC anchor, the worker type name for a worker
// anchor, or "" for an anchor that is neither.
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

// dedupSorted returns the distinct elements of in, sorted. The duplicate
// and stale findings name notes in a stable, sorted order.
func dedupSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// sortFindings orders findings deterministically: by Symbol, then by
// Reason so two findings on the same symbol still have a stable order.
func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Symbol != fs[j].Symbol {
			return fs[i].Symbol < fs[j].Symbol
		}
		return fs[i].Reason < fs[j].Reason
	})
}

// c1Summary builds the one-line C1 verdict. On success it reports the
// N/N entry-point/anchor count; on failure it is the bare FAIL line —
// the Findings carry the detail.
func c1Summary(passed bool, total int) string {
	if passed {
		return fmt.Sprintf(
			"C1 completeness: PASS (%d/%d entry-points anchored)", total, total)
	}
	return "C1 completeness: FAIL"
}
