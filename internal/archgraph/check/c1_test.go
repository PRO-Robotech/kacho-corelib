package check_test

// Integration tests for the C1 completeness check.
//
// Each test maps 1:1 to an acceptance scenario from group E of the
// sub-phase 4.0 acceptance document (archgraph C1 — entry-point /
// anchor completeness). The scenario ID is encoded in the test name for
// traceability.
//
// Fixtures are synthesised by archtest.BuildRepo into t.TempDir() — a
// main package under cmd/svc/ plus L2 notes under docs/arch/ — loaded
// with golang.org/x/tools/go/packages and note.Load, then asserted on the
// Result returned by check.CheckC1. No testcontainers, no network: every
// fixture is self-contained and pins go 1.21.

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/archtest"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/check"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
)

// buildFixture materialises spec into a fresh temp module and returns the
// module root.
func buildFixture(t *testing.T, spec archtest.Spec) string {
	t.Helper()
	return archtest.BuildRepo(t, spec)
}

// loadInventoryAt loads the fixture rooted at root and discovers its
// entry-point inventory.
func loadInventoryAt(t *testing.T, root string) *entrypoints.Inventory {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedImports |
			packages.NeedDeps |
			packages.NeedTypes |
			packages.NeedSyntax |
			packages.NeedTypesInfo,
		Dir:   root,
		Tests: false,
		Env:   append(os.Environ(), "GOTOOLCHAIN=local"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	require.NoError(t, err, "loading fixture at %s", root)
	for _, p := range pkgs {
		require.Empty(t, p.Errors, "fixture at %s package %s must compile", root, p.PkgPath)
	}
	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err, "discovering entry-points of fixture at %s", root)
	return inv
}

// loadNotesAt loads the L2 notes under the fixture's docs/arch directory.
func loadNotesAt(t *testing.T, root string) []*note.Note {
	t.Helper()
	notes, err := note.Load(filepath.Join(root, "docs", "arch"))
	require.NoError(t, err, "loading L2 notes of fixture at %s", root)
	return notes
}

// reasons returns the sorted Reason strings of every finding.
func reasons(r check.Result) []string {
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.String())
	}
	sort.Strings(out)
	return out
}

// Test_4_0_E1_C1Pass: a repo whose three entry-points (two RPCs plus a
// worker, FQN form) are each anchored by exactly one L2 note passes C1
// with a 3/3 summary and produces no findings.
func Test_4_0_E1_C1Pass(t *testing.T) {
	root := buildFixture(t, archtest.SpecC1Pass())
	inv := loadInventoryAt(t, root)
	notes := loadNotesAt(t, root)

	res := check.CheckC1(inv, notes)

	require.True(t, res.Passed, "C1 must pass when every entry-point is anchored exactly once")
	require.Empty(t, res.Findings, "a passing C1 has no findings")
	require.Empty(t, res.Hints, "a clean C1 has no hints")
	require.Equal(t, "C1 completeness: PASS (3/3 entry-points anchored)", res.Summary)
}

// Test_4_0_E2_C1FailUndocumentedEntryPoint: an entry-point present in
// the code but anchored by no L2 note fails C1 with an
// "undocumented entry-point" finding.
func Test_4_0_E2_C1FailUndocumentedEntryPoint(t *testing.T) {
	root := buildFixture(t, archtest.SpecC1Undocumented())
	inv := loadInventoryAt(t, root)
	notes := loadNotesAt(t, root)

	res := check.CheckC1(inv, notes)

	require.False(t, res.Passed, "C1 must fail on an undocumented entry-point")
	require.Equal(t, "C1 completeness: FAIL", res.Summary)
	require.Contains(t, reasons(res),
		"undocumented entry-point: kacho.cloud.vpc.v1.NetworkService/Update — "+
			"declare it in an L2 note's anchors or remove it")
}

// Test_4_0_E3_C1FailStaleAnchor: an L2 anchor pointing at a method that
// no longer exists in the code fails C1 with a "stale anchor" finding
// that names the offending note.
func Test_4_0_E3_C1FailStaleAnchor(t *testing.T) {
	root := buildFixture(t, archtest.SpecC1StaleAnchor())
	inv := loadInventoryAt(t, root)
	notes := loadNotesAt(t, root)

	res := check.CheckC1(inv, notes)

	require.False(t, res.Passed, "C1 must fail on a stale anchor")
	require.Equal(t, "C1 completeness: FAIL", res.Summary)
	require.Contains(t, reasons(res),
		"stale anchor: kacho.cloud.vpc.v1.NetworkService/Patch in "+
			"docs/arch/network-lifecycle.md points to a non-existent entry-point")
}

// Test_4_0_E4_C1FailDuplicateAnchor: an entry-point anchored by two L2
// notes fails C1 with a "must be exactly one" finding listing both note
// names in sorted order.
func Test_4_0_E4_C1FailDuplicateAnchor(t *testing.T) {
	root := buildFixture(t, archtest.SpecC1DuplicateAnchor())
	inv := loadInventoryAt(t, root)
	notes := loadNotesAt(t, root)

	res := check.CheckC1(inv, notes)

	require.False(t, res.Passed, "C1 must fail when an entry-point is anchored twice")
	require.Equal(t, "C1 completeness: FAIL", res.Summary)
	require.Contains(t, reasons(res),
		"entry-point kacho.cloud.vpc.v1.NetworkService/Create anchored in 2 notes "+
			"(network-bootstrap.md, network-lifecycle.md) — must be exactly one")
}

// Test_4_0_E5_C1NonConventionalWorkerHint: a worker started without the
// recognised convention is not an entry-point, so an L2 anchor for it
// reads as stale; because the inventory carries an unrecognised-worker
// hint, C1 augments the output with a clarifying hint line.
func Test_4_0_E5_C1NonConventionalWorkerHint(t *testing.T) {
	root := buildFixture(t, archtest.SpecC1NonConventionalWorker())
	inv := loadInventoryAt(t, root)
	notes := loadNotesAt(t, root)
	require.NotEmpty(t, inv.Hints, "fixture must carry an unrecognised-worker hint")

	res := check.CheckC1(inv, notes)

	require.False(t, res.Passed, "an anchored-but-undiscovered worker reads as a stale anchor")
	require.Equal(t, "C1 completeness: FAIL", res.Summary)
	require.Contains(t, reasons(res),
		"stale anchor: NetworkOutboxWorker in docs/arch/network-outbox.md "+
			"points to a non-existent entry-point")
	require.Contains(t, res.Hints,
		"hint: an anchored worker NetworkOutboxWorker was not discovered as an "+
			"entry-point — verify it follows the New<Name>+Run/Start convention")
}

// Test_4_0_E6_C1EmptyAnchorsIgnored: an L2 note with an empty anchors
// list (a planned functionality) is ignored by C1 — it neither
// contributes coverage nor produces a stale finding — so C1 still
// passes when every entry-point is anchored by the other notes.
func Test_4_0_E6_C1EmptyAnchorsIgnored(t *testing.T) {
	root := buildFixture(t, archtest.SpecC1EmptyAnchors())
	inv := loadInventoryAt(t, root)
	notes := loadNotesAt(t, root)

	res := check.CheckC1(inv, notes)

	require.True(t, res.Passed,
		"an empty-anchors note must not cause a completeness failure")
	require.Empty(t, res.Findings)
	require.Equal(t, "C1 completeness: PASS (2/2 entry-points anchored)", res.Summary)
}
