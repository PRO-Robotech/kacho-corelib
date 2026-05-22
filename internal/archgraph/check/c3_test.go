package check_test

// Integration tests for the C3 freshness check.
//
// Each test maps 1:1 to an acceptance scenario from group G of the
// sub-phase 4.0 acceptance document (archgraph C3 — note freshness: a
// note's source_sha must match the hash of the reachable-set files
// under its anchors). The scenario ID is encoded in the test name for
// traceability.
//
// Fixtures are synthesised by archtest.BuildRepo into t.TempDir(),
// loaded with golang.org/x/tools/go/packages, run through entry-point
// discovery and reach.Compute, then asserted on the Result returned by
// check.CheckC3. No testcontainers, no network: every fixture is
// self-contained and pins go 1.21.
//
// A note that must be genuinely fresh carries the archtest.FreshSHA
// sentinel in its frontmatter; runC3 recognises it, computes the real
// hash with check.AnchorHash once the reachability graph is available,
// rewrites the note in place and reloads — the two-phase build the
// FreshSHA contract requires.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/archtest"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/check"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/reach"
)

// c3Fixture builds a C3 fixture, loads its packages, discovers the
// entry-point inventory and computes the reachability graph. It returns
// the module root and the graph — the inputs CheckC3 and AnchorHash
// both consume.
func c3Fixture(t *testing.T, spec archtest.Spec) (string, *reach.Graph) {
	t.Helper()
	root := archtest.BuildRepo(t, spec)
	pkgs := loadFixturePkgsAt(t, root)
	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err, "discovering entry-points of fixture %s", spec.Module)
	g, err := reach.Compute(pkgs, inv)
	require.NoError(t, err, "reach.Compute must not error on fixture %s", spec.Module)
	require.NotNil(t, g)
	return root, g
}

// resolveFreshNotes rewrites every note under root carrying the
// archtest.FreshSHA sentinel with the genuine hash of its anchors'
// reachable-set, computed via check.AnchorHash. This is the second
// phase of the FreshSHA two-phase build: the real hash can only be
// known once the fixture is on disk and its graph is computed.
func resolveFreshNotes(t *testing.T, root string, g *reach.Graph) {
	t.Helper()
	archtest.ResolveFreshNotes(t, root, func(notePath string) string {
		n, err := note.Parse(notePath)
		require.NoError(t, err, "parsing sentinel note %s", notePath)
		return check.AnchorHash(root, n, g)
	})
}

// runC3 builds a C3 fixture, resolves any FreshSHA notes to their
// genuine hash, reloads the notes and runs CheckC3.
func runC3(t *testing.T, spec archtest.Spec) check.Result {
	t.Helper()
	root, g := c3Fixture(t, spec)
	resolveFreshNotes(t, root, g)
	notes, err := note.Load(filepath.Join(root, "docs", "arch"))
	require.NoError(t, err, "reloading L2 notes of fixture at %s", root)
	return check.CheckC3(root, notes, g)
}

// Test_4_0_G1_C3_PassFreshNote: an L2 note whose source_sha matches the
// hash of its anchors' reachable-set passes C3 with no findings.
func Test_4_0_G1_C3_PassFreshNote(t *testing.T) {
	res := runC3(t, archtest.SpecC3Fresh())

	require.True(t, res.Passed,
		"C3 must pass when source_sha matches the reachable-set hash; findings=%v",
		findingTexts(res))
	require.Empty(t, res.Findings, "a passing C3 has no findings")
	require.Equal(t, "C3 freshness: PASS", res.Summary)
}

// Test_4_0_G2_C3_FailChangedCode: a note whose source_sha no longer
// matches the hash of its anchors' reachable-set fails C3 with a
// stale-note finding naming the note, the old SHA and the actual one.
func Test_4_0_G2_C3_FailChangedCode(t *testing.T) {
	root, g := c3Fixture(t, archtest.SpecC3Stale())
	notes, err := note.Load(filepath.Join(root, "docs", "arch"))
	require.NoError(t, err)
	require.Len(t, notes, 1)

	actual := check.AnchorHash(root, notes[0], g)
	require.NotEmpty(t, actual)
	require.NotEqual(t, "0000000stalesha0000000", actual,
		"the fixture's recorded source_sha must differ from the real hash")

	res := check.CheckC3(root, notes, g)

	require.False(t, res.Passed, "a changed-code note must fail C3")
	require.Equal(t, "C3 freshness: FAIL", res.Summary)
	require.True(t, containsLine(findingTexts(res),
		"stale note: docs/arch/network-lifecycle.md — code under anchors "+
			"changed (source_sha 0000000stalesha0000000 != actual "+actual+
			"); re-read the note and update source_sha"),
		"C3 must report the stale note with old and actual SHAs; got %v",
		findingTexts(res))
}

// Test_4_0_G3_C3_HashIsPerReachableSet: with two notes whose
// reachable-sets are disjoint, a stale note B fails C3 while a fresh
// note A still passes — the hash is scoped to a note's own
// reachable-set, not the whole repository.
func Test_4_0_G3_C3_HashIsPerReachableSet(t *testing.T) {
	res := runC3(t, archtest.SpecC3TwoNotes())

	require.False(t, res.Passed, "the stale note B must fail C3")
	require.Equal(t, "C3 freshness: FAIL", res.Summary)

	texts := findingTexts(res)
	require.True(t, containsLine(texts, "stale note: docs/arch/instance-lifecycle.md"),
		"C3 must report the stale note B; got %v", texts)
	require.False(t, containsLine(texts, "network-lifecycle.md"),
		"the fresh note A must not be reported — its reachable-set is unchanged; got %v",
		texts)
	require.Len(t, res.Findings, 1,
		"exactly one note (B) must be flagged; findings=%v", texts)
}

// Test_4_0_G4_C3_DeterministicHash: AnchorHash over an unchanged
// fixture is deterministic across runs and invariant to the repository
// checkout path — it hashes repo-relative paths plus content, never
// absolute paths or traversal order.
func Test_4_0_G4_C3_DeterministicHash(t *testing.T) {
	// Two independent builds of the SAME spec land in different temp
	// directories — different absolute paths. The hash must be identical.
	rootA, gA := c3Fixture(t, archtest.SpecC3Fresh())
	rootB, gB := c3Fixture(t, archtest.SpecC3Fresh())
	require.NotEqual(t, rootA, rootB,
		"the two fixture builds must live at different absolute paths")

	notesA, err := note.Load(filepath.Join(rootA, "docs", "arch"))
	require.NoError(t, err)
	require.Len(t, notesA, 1)
	notesB, err := note.Load(filepath.Join(rootB, "docs", "arch"))
	require.NoError(t, err)
	require.Len(t, notesB, 1)

	h1 := check.AnchorHash(rootA, notesA[0], gA)
	h2 := check.AnchorHash(rootA, notesA[0], gA)
	require.Equal(t, h1, h2,
		"AnchorHash must be deterministic across runs over the same fixture")

	hB := check.AnchorHash(rootB, notesB[0], gB)
	require.Equal(t, h1, hB,
		"AnchorHash must be invariant to the repository checkout path")
	require.NotEmpty(t, h1, "AnchorHash of an anchored note must be non-empty")
}

// Test_4_0_G4_C3_HashScopedToRepoFiles: the freshness hash must be
// computed over files of the analysed repository ONLY. With a fixture
// whose anchor genuinely reaches into the standard library — its
// handler calls fmt.Sprintf, so RTA pulls GOROOT-resident source files
// into the reachable-set — AnchorHash must:
//
//   - feed only files under repoRoot into the digest (no absolute path,
//     no GOROOT/GOMODCACHE-resident file), and
//   - still be deterministic and checkout-path-invariant.
//
// Hashing the stdlib / dependency files would tie source_sha to the
// toolchain install path and Go version: a Go-minor bump would stale
// every note on a real repo at once. The minimal empty-handler C3
// fixtures never reach stdlib, so the original G4 test could not catch
// this — this fixture deliberately does.
func Test_4_0_G4_C3_HashScopedToRepoFiles(t *testing.T) {
	rootA, gA := c3Fixture(t, archtest.SpecC3StdlibReach())
	rootB, gB := c3Fixture(t, archtest.SpecC3StdlibReach())
	require.NotEqual(t, rootA, rootB,
		"the two fixture builds must live at different absolute paths")

	notesA, err := note.Load(filepath.Join(rootA, "docs", "arch"))
	require.NoError(t, err)
	require.Len(t, notesA, 1)
	notesB, err := note.Load(filepath.Join(rootB, "docs", "arch"))
	require.NoError(t, err)
	require.Len(t, notesB, 1)

	// The fixture's handler calls fmt.Sprintf — confirm the anchor's
	// reachable-set genuinely spans stdlib files, so this test exercises
	// the defect rather than a no-op. Without stdlib in the set the
	// assertion below would pass vacuously.
	stdlibSeen := false
	for _, rs := range gA.Sets {
		for _, f := range rs.Files {
			if isStdlibOrDepFile(t, rootA, f) {
				stdlibSeen = true
			}
		}
	}
	require.True(t, stdlibSeen,
		"fixture precondition: the anchor's reachable-set must span stdlib/dep "+
			"files, otherwise the repo-scoping assertion is vacuous")

	// (a) every file fed into the digest must be repository-relative and
	// resolve under repoRoot — no absolute path, no GOROOT/GOMODCACHE leak.
	hashFilesA := check.AnchorHashFiles(rootA, notesA[0], gA)
	require.NotEmpty(t, hashFilesA,
		"the anchored note must hash at least its own handler/repo files")
	for _, rel := range hashFilesA {
		require.False(t, filepath.IsAbs(filepath.FromSlash(rel)),
			"AnchorHash must not feed absolute paths into the digest; got %q", rel)
		require.False(t, strings.HasPrefix(rel, "../") || rel == "..",
			"AnchorHash must not feed out-of-repo (.. prefixed) paths into the "+
				"digest; got %q", rel)
		abs := filepath.Join(rootA, filepath.FromSlash(rel))
		require.False(t, isStdlibOrDepFile(t, rootA, abs),
			"AnchorHash must scope the digest to in-repo files only — %q is a "+
				"stdlib/dependency file outside repoRoot", rel)
	}

	// (b) the digest stays deterministic and checkout-path-invariant even
	// though the reachable-set spans stdlib files at different absolute
	// GOROOT paths on the two builds.
	h1 := check.AnchorHash(rootA, notesA[0], gA)
	h2 := check.AnchorHash(rootA, notesA[0], gA)
	require.Equal(t, h1, h2,
		"AnchorHash must be deterministic across runs over the same fixture")

	hB := check.AnchorHash(rootB, notesB[0], gB)
	require.Equal(t, h1, hB,
		"AnchorHash must be invariant to the checkout path even when the "+
			"reachable-set spans stdlib files at different GOROOT paths")
	require.NotEmpty(t, h1, "AnchorHash of an anchored note must be non-empty")
}

// isStdlibOrDepFile reports whether abs is a source file outside the
// repository rooted at repoRoot — a GOROOT-resident stdlib file or a
// GOMODCACHE-resident dependency file. It is the test-side oracle for
// "this file is outside C3's mandate": a path that cannot be made
// repoRoot-relative without a ".." prefix is, by definition, outside the
// repo.
func isStdlibOrDepFile(t *testing.T, repoRoot, abs string) bool {
	t.Helper()
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil {
		return true
	}
	return strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".."
}

// Test_4_0_G5_C3_FailMissingSourceSHA: a note with a non-empty anchors
// list but an empty source_sha fails C3 with a missing-source_sha
// finding.
func Test_4_0_G5_C3_FailMissingSourceSHA(t *testing.T) {
	res := runC3(t, archtest.SpecC3MissingSHA())

	require.False(t, res.Passed, "a note with anchors but no source_sha must fail C3")
	require.Equal(t, "C3 freshness: FAIL", res.Summary)
	require.True(t, containsLine(findingTexts(res),
		"missing source_sha: docs/arch/network-lifecycle.md has anchors "+
			"but no source_sha — review the code and set it"),
		"C3 must report the missing source_sha; got %v", findingTexts(res))
}

// Test_4_0_G6_C3_PlannedNoteSkipped: a note with an empty anchors list
// (a planned functionality, no source_sha) is skipped by C3 — it
// produces no missing-source_sha finding — so C3 still passes when the
// other notes are fresh.
func Test_4_0_G6_C3_PlannedNoteSkipped(t *testing.T) {
	res := runC3(t, archtest.SpecC3PlannedSkipped())

	require.True(t, res.Passed,
		"a planned (empty-anchors) note must not fail C3; findings=%v",
		findingTexts(res))
	require.Empty(t, res.Findings, "a passing C3 has no findings")
	require.False(t, containsLine(findingTexts(res), "network-planned.md"),
		"the planned note must not be reported by C3")
	require.Equal(t, "C3 freshness: PASS", res.Summary)
}
