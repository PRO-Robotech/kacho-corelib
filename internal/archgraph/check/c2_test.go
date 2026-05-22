package check_test

// Integration tests for the C2 dead-code check.
//
// Each test maps 1:1 to an acceptance scenario from group F of the
// sub-phase 4.0 acceptance document (archgraph C2 — dead-code detection
// with the `// archgraph:keep` annotation). The scenario ID is encoded
// in the test name for traceability.
//
// Fixtures are synthesised by archtest.BuildRepo into t.TempDir(), loaded
// with golang.org/x/tools/go/packages, run through entry-point discovery,
// then asserted on the Result returned by check.CheckC2. No
// testcontainers, no network: every fixture is self-contained and pins
// go 1.21.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/archtest"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/check"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
)

// loadFixturePkgsAt loads every package of the C2 fixture rooted at root.
func loadFixturePkgsAt(t *testing.T, root string) []*packages.Package {
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
		Env:   append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	require.NoError(t, err, "loading fixture at %s", root)
	for _, p := range pkgs {
		require.Empty(t, p.Errors, "fixture at %s package %s must compile", root, p.PkgPath)
	}
	return pkgs
}

// runC2 builds a C2 fixture, discovers its inventory and runs CheckC2.
func runC2(t *testing.T, spec archtest.Spec) check.Result {
	t.Helper()
	root := archtest.BuildRepo(t, spec)
	pkgs := loadFixturePkgsAt(t, root)
	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err, "discovering entry-points of fixture %s", spec.Module)
	res, err := check.CheckC2(root, pkgs, inv)
	require.NoError(t, err, "CheckC2 must not error on fixture %s", spec.Module)
	return res
}

// findingTexts returns the Reason strings of every finding.
func findingTexts(r check.Result) []string {
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.String())
	}
	return out
}

// containsLine reports whether any string in ss equals or contains sub.
func containsLine(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// Test_4_0_F1_C2_PassAllReachable: every exported function and method of
// the repo is reachable from the single entry-point, so C2 passes with
// no findings and a 0-unreachable summary.
func Test_4_0_F1_C2_PassAllReachable(t *testing.T) {
	res := runC2(t, archtest.SpecC2Pass())

	require.True(t, res.Passed,
		"C2 must pass when every exported symbol is reachable; findings=%v",
		findingTexts(res))
	require.Empty(t, res.Findings, "a passing C2 has no findings")
	require.Equal(t,
		"C2 dead-code: PASS (0 unreachable exported symbols)", res.Summary)
}

// Test_4_0_F2_C2_FailUnreachable: an exported function unreachable from
// every entry-point and carrying no annotation fails C2 with a finding
// naming the symbol and its file:line position.
func Test_4_0_F2_C2_FailUnreachable(t *testing.T) {
	res := runC2(t, archtest.SpecC2Unreachable())

	require.False(t, res.Passed, "an unreachable exported symbol must fail C2")
	require.Contains(t, res.Summary, "C2 dead-code: FAIL")
	require.True(t, containsLine(findingTexts(res),
		"dead code: exported symbol LegacyMigrateAddresses "+
			"(internal/repo/legacy.go:14) unreachable from any entry-point"),
		"C2 must report LegacyMigrateAddresses with its file:line; got %v",
		findingTexts(res))
}

// Test_4_0_F3_C2_KeepSuppressesWithReason: an unreachable exported
// function annotated with `// archgraph:keep <reason>` is not reported
// as dead; C2 passes and lists the symbol as kept with its reason.
func Test_4_0_F3_C2_KeepSuppressesWithReason(t *testing.T) {
	res := runC2(t, archtest.SpecC2Keep())

	require.True(t, res.Passed,
		"a kept symbol must not fail C2; findings=%v", findingTexts(res))
	require.False(t, containsLine(findingTexts(res), "BuildClientSDK"),
		"BuildClientSDK must not be a dead-code finding when kept")
	require.True(t, containsLine(res.Hints,
		"kept: BuildClientSDK (reason: public SDK surface, "+
			"consumed by external clients)"),
		"C2 must list BuildClientSDK as kept with its reason; hints=%v",
		res.Hints)
}

// Test_4_0_F4_C2_KeepWithoutReasonRejected: a bare `// archgraph:keep`
// with no reason over an unreachable exported symbol is rejected as an
// invalid annotation; C2 fails with an invalid-annotation finding.
func Test_4_0_F4_C2_KeepWithoutReasonRejected(t *testing.T) {
	res := runC2(t, archtest.SpecC2KeepBare())

	require.False(t, res.Passed, "a bare archgraph:keep must fail C2")
	require.Contains(t, res.Summary, "C2 dead-code: FAIL")
	require.True(t, containsLine(findingTexts(res),
		"invalid annotation: // archgraph:keep at internal/repo/x.go:9 "+
			"requires a non-empty reason"),
		"C2 must reject the bare keep annotation; got %v", findingTexts(res))
}

// Test_4_0_F5_C2_KeepIsTransitive: a kept function is an extra
// reachability root — a private helper and a further exported function
// transitively reachable from it are not reported as dead.
func Test_4_0_F5_C2_KeepIsTransitive(t *testing.T) {
	res := runC2(t, archtest.SpecC2KeepTransitive())

	require.True(t, res.Passed,
		"keep must extend reachability transitively; findings=%v",
		findingTexts(res))
	require.False(t, containsLine(findingTexts(res), "EncodeManifest"),
		"EncodeManifest, transitively reachable from a kept symbol, "+
			"must not be dead code; findings=%v", findingTexts(res))
}

// Test_4_0_F7_C2_LibraryRepoSkipped: a pure library repo (no main
// package) is classified as a library; C2 is skipped, reports a SKIP
// summary, passes (does not contribute to a failing exit) and produces
// no findings even though exported symbols are unused.
func Test_4_0_F7_C2_LibraryRepoSkipped(t *testing.T) {
	res := runC2(t, archtest.SpecC2Library())

	require.True(t, res.Passed, "a skipped C2 must not fail")
	require.Empty(t, res.Findings, "a skipped C2 produces no findings")
	require.Equal(t,
		"C2 dead-code: SKIP (library repo: no main package)", res.Summary)
}

// fixtureExists guards the F6 reflection scenario, exercised end-to-end
// (as a continuation: FAIL before annotation, PASS after) by the CLI
// test in cmd/archgraph; here we only assert the unit-level FAIL.
func Test_4_0_F6_C2_ReflectionOnlyIsDead(t *testing.T) {
	res := runC2(t, archtest.SpecC2Reflection())

	require.False(t, res.Passed,
		"a reflection-only exported method is invisible to RTA and must "+
			"be reported dead until annotated")
	require.True(t, containsLine(findingTexts(res), "ReflectInvoke"),
		"C2 must report the reflection-only ReflectInvoke as dead; got %v",
		findingTexts(res))

	_ = filepath.Separator
}
