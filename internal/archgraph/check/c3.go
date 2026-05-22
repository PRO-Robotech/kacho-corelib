package check

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/reach"
)

// CheckC3 runs the freshness check: every L2 functionality note with a
// non-empty anchors list must record a source_sha equal to the hash of
// the reachable-set files under its anchors.
//
// The check exists so an AI cannot merge a code change reaching under a
// note without a human having re-read the note: the moment a file in a
// note's reachable-set changes, the recomputed hash diverges from the
// recorded source_sha and C3 fails the merge — re-reading the note and
// updating source_sha is the only way to clear the failure.
//
// The check has two failure modes, each reported as a Finding:
//
//   - stale note — the note records a non-empty source_sha that does
//     not match the hash of its anchors' reachable-set; the code under
//     the note changed and the note was not re-verified;
//   - missing source_sha — the note has anchors but no source_sha at
//     all; freshness was never recorded.
//
// A note whose anchors list is empty (a planned functionality) is
// skipped entirely: with no anchors there is no reachable-set to hash,
// so it neither passes nor fails — and notably is *not* reported as a
// missing source_sha. An anchor that resolves to no entry-point (a
// stale anchor, already C1's concern) contributes nothing to the
// reachable-set rather than failing C3.
//
// # Hash granularity
//
// source_sha hashes the *whole files* of a note's reachable-set, not
// the bodies of individual reachable functions. Editing any function
// in a reachable-set file — even one not itself reachable from the
// note's anchors — stales the note. This conservative over-trigger is
// deliberate (sub-phase 4.0 §12.4): a coarser, file-granular hash is
// cheap and certain, and a false "re-read the note" is a far smaller
// cost than a missed one.
//
// CheckC3 derives its verdict from notes, the reachability graph g and
// repoRoot (used to render note paths and the hash's file paths
// repository-relative — see AnchorHash). Findings are sorted by Symbol
// (the note's repository-relative path), so the Result is fully
// deterministic. CheckC3 performs file I/O — it reads the reachable-set
// files to hash them — but never fails on it: an unreadable file simply
// contributes empty content to the hash.
func CheckC3(repoRoot string, notes []*note.Note, g *reach.Graph) Result {
	var findings []Finding

	for _, n := range notes {
		// A planned functionality (empty anchors) has no reachable-set
		// — C3 skips it; it is not a missing-source_sha failure.
		if len(n.Anchors) == 0 {
			continue
		}

		notePath := relNotePath(repoRoot, n)

		// A note with anchors but no source_sha never recorded its
		// freshness — a reviewer must set it.
		if n.SourceSHA == "" {
			findings = append(findings, Finding{
				Check:  C3,
				Symbol: notePath,
				Reason: fmt.Sprintf(
					"missing source_sha: %s has anchors but no source_sha "+
						"— review the code and set it", notePath),
			})
			continue
		}

		actual := AnchorHash(repoRoot, n, g)
		if actual != n.SourceSHA {
			findings = append(findings, Finding{
				Check:  C3,
				Symbol: notePath,
				Reason: fmt.Sprintf(
					"stale note: %s — code under anchors changed "+
						"(source_sha %s != actual %s); re-read the note and "+
						"update source_sha", notePath, n.SourceSHA, actual),
			})
		}
	}

	sortFindings(findings)

	passed := len(findings) == 0
	return Result{
		Passed:   passed,
		Findings: findings,
		Summary:  c3Summary(passed),
	}
}

// AnchorHash computes the freshness hash of an L2 note: a digest of the
// whole source files of the reachable-set of every anchor of the note.
//
// The algorithm, fixed so the result is reproducible across machines,
// checkouts and runs:
//
//  1. For each anchor of n, look up the reachable-set keyed by the
//     anchor's canonical symbol (the gRPC method FQN or worker type
//     name) in g.Sets; collect the union of all those sets' Files. An
//     anchor whose symbol is not a graph key (a stale anchor, or a
//     non-RPC/non-worker anchor) contributes nothing.
//  2. Drop every file that does not lie under repoRoot. reach records
//     whole-program reachable-sets — RTA transitively descends into the
//     standard library and module-cache dependencies — so the raw set
//     contains GOROOT- and GOMODCACHE-resident files. C3's freshness
//     hash answers "did the code of *this* repository change", so
//     dependency and toolchain sources are outside its mandate; hashing
//     them would also tie source_sha to the toolchain install path and
//     Go version. Then convert each surviving path to a slash-separated
//     path relative to repoRoot and sort. Hashing repo-relative paths
//     makes the digest independent of where the repository is checked
//     out; sorting makes it independent of traversal and map order.
//  3. Feed each (relative-path, file-content) pair into a single SHA-256
//     digest, each component length-prefixed so no two distinct file
//     sets can collide by concatenation. A file that cannot be read
//     contributes empty content rather than aborting the hash.
//
// Step 2's file selection is exposed standalone as AnchorHashFiles.
//
// The returned digest is the lower-case hex SHA-256. A note with no
// reachable-set files at all (every anchor stale, or anchors empty)
// still yields a well-defined, stable hash of the empty file set.
//
// AnchorHash is the single source of the freshness algorithm: CheckC3
// calls it to obtain the actual hash, and archgraph's note generator
// (and test fixtures) call it to record a correct source_sha — neither
// reimplements the digest.
func AnchorHash(repoRoot string, n *note.Note, g *reach.Graph) string {
	rels := AnchorHashFiles(repoRoot, n, g)

	h := sha256.New()
	for _, rel := range rels {
		// Re-resolve the file from repoRoot: the sort decoupled rels from
		// the original absolute slice, and reading via repoRoot+rel keeps
		// the hash a pure function of (relative-path, content).
		content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			content = nil
		}
		writeHashChunk(h, []byte(rel))
		writeHashChunk(h, content)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// AnchorHashFiles returns the exact, deterministically sorted list of
// repository-relative (slash-separated) file paths AnchorHash feeds
// into its digest for note n — the introspection counterpart of
// AnchorHash, exposed so a caller (and the C3 freshness tests) can see
// *which* files a note's source_sha is computed over without recomputing
// the digest.
//
// Every returned path is repository-relative and lies under repoRoot:
// the reachable-set reach records is whole-program — RTA transitively
// descends into the standard library and module-cache dependencies, so
// reach.ReachableSet.Files legitimately contains GOROOT- and
// GOMODCACHE-resident files. Those are deliberately dropped here: C3's
// freshness hash answers "did the code of *this* repository under the
// note's anchors change", and the source of a dependency or the Go
// toolchain is outside that mandate. Hashing them would also make the
// digest depend on the toolchain install path and the Go version,
// breaking the machine-independence guarantee (sub-phase 4.0 §12.4 / G4).
func AnchorHashFiles(repoRoot string, n *note.Note, g *reach.Graph) []string {
	files := anchorReachableFiles(n, g)

	rels := make([]string, 0, len(files))
	for _, abs := range files {
		rel, inRepo := repoRelPath(repoRoot, abs)
		if !inRepo {
			// A GOROOT- / GOMODCACHE-resident file: stdlib or a
			// module-cache dependency RTA transitively descended into.
			// It is outside C3's mandate (the freshness hash is about
			// *this* repository's code) and, if hashed, would tie
			// source_sha to the toolchain install path and Go version.
			// Drop it.
			continue
		}
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	return rels
}

// anchorReachableFiles returns the union of the reachable-set files of
// every anchor of n, as the absolute paths reach.Compute recorded. An
// anchor whose canonical symbol is not a key of g.Sets (a stale anchor)
// contributes nothing — C3 must not fail on a nil set, and the stale
// anchor is already C1's finding.
func anchorReachableFiles(n *note.Note, g *reach.Graph) []string {
	set := map[string]struct{}{}
	if g != nil {
		for _, a := range n.Anchors {
			sym := anchorSymbol(a)
			if sym == "" {
				continue
			}
			rs, ok := g.Sets[sym]
			if !ok {
				continue
			}
			for _, f := range rs.Files {
				set[f] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// writeHashChunk feeds one length-prefixed chunk into h. The 8-byte
// big-endian length prefix makes the digest injective over a sequence
// of chunks: ("ab","c") and ("a","bc") hash differently, so no two
// distinct (path,content) sequences can collide by concatenation.
func writeHashChunk(h interface{ Write([]byte) (int, error) }, chunk []byte) {
	var lp [8]byte
	binary.BigEndian.PutUint64(lp[:], uint64(len(chunk)))
	_, _ = h.Write(lp[:])
	_, _ = h.Write(chunk)
}

// relNotePath renders a note's path repository-relative and
// slash-separated, so a C3 finding reads "docs/arch/network-lifecycle.md"
// regardless of the repository's checkout location. A note always lives
// inside the repository it documents, so the in-repo case is the only
// one expected; the absolute-path fallback exists solely so a
// degenerate input cannot panic a finding's display string.
func relNotePath(repoRoot string, n *note.Note) string {
	if rel, inRepo := repoRelPath(repoRoot, n.Path()); inRepo {
		return rel
	}
	return filepath.ToSlash(n.Path())
}

// repoRelPath reports whether abs lies inside the repository rooted at
// repoRoot and, if so, returns its slash-separated repository-relative
// form.
//
// A file is "in repo" iff filepath.Rel(repoRoot, abs) succeeds and the
// result has no ".." prefix — i.e. abs does not escape repoRoot (and is
// not on a different volume). reach.Compute records absolute paths
// spanning the whole analysed program — including GOROOT-resident
// stdlib files and GOMODCACHE-resident dependency files — so this
// predicate is the single choke-point that separates "code of this
// repository" (which C3's freshness hash is about) from everything
// outside its mandate. AnchorHashFiles drops every abs for which this
// returns false, which is why the freshness digest never depends on the
// toolchain install path or Go version.
func repoRelPath(repoRoot, abs string) (string, bool) {
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// c3Summary builds the one-line C3 verdict.
func c3Summary(passed bool) string {
	if passed {
		return "C3 freshness: PASS"
	}
	return "C3 freshness: FAIL"
}
