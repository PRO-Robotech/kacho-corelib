package audit_test

// Integration tests for arch-audit's combined C1+C2+C3 orchestration.
//
// Each test maps 1:1 to an acceptance scenario from group I of the
// sub-phase 4.0 acceptance document (archgraph — arch-audit
// orchestration). The scenario ID is encoded in the test name for
// traceability.
//
//	4.0-I1 — exit 0 when every check passes
//	4.0-I2 — exit != 0 when one check fails; every check still runs
//	4.0-I3 — deterministic, sorted output across two runs
//	4.0-I4 — every FAIL line names its object and reason
//	4.0-I5 — arch-gen exits != 0 when it cannot write an artifact
//	4.0-I6 — the CI drift gate: arch-gen + `git diff --exit-code`
//
// Fixtures are synthesised by archtest.BuildRepo into t.TempDir() and
// driven either through audit.Run directly or end-to-end through
// cli.Run("arch-audit"/"arch-gen", ...). No testcontainers, no network:
// every fixture is a self-contained Go module. I6 uses a real git repo.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/archtest"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/audit"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/check"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/cli"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/reach"
)

// --- harness ---------------------------------------------------------------

// loadFixturePkgs loads every package of the fixture rooted at root.
func loadFixturePkgs(t *testing.T, root string) []*packages.Package {
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
		require.Empty(t, p.Errors, "fixture package %s must compile", p.PkgPath)
	}
	return pkgs
}

// buildAndResolve materialises spec, then performs the second phase of
// the FreshSHA two-phase build: it computes the reachability graph and
// rewrites every note carrying the FreshSHA sentinel with its genuine
// anchor hash, so a fixture meant to pass C3 actually does. It returns
// the module root.
func buildAndResolve(t *testing.T, spec archtest.Spec) string {
	t.Helper()
	root := archtest.BuildRepo(t, spec)

	pkgs := loadFixturePkgs(t, root)
	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err, "discovering entry-points of fixture %s", spec.Module)
	g, err := reach.Compute(pkgs, inv)
	require.NoError(t, err, "reach.Compute on fixture %s", spec.Module)

	archtest.ResolveFreshNotes(t, root, func(notePath string) string {
		n, perr := note.Parse(notePath)
		require.NoError(t, perr, "parsing sentinel note %s", notePath)
		return check.AnchorHash(root, n, g)
	})
	return root
}

// runAudit drives audit.Run over the fixture rooted at root and returns
// the Report plus its rendered text (the same layout arch-audit prints).
func runAudit(t *testing.T, root string) (audit.Report, string) {
	t.Helper()
	pkgs := loadFixturePkgs(t, root)
	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err, "discovering entry-points at %s", root)
	notes, err := note.Load(filepath.Join(root, "docs", "arch"))
	require.NoError(t, err, "loading L2 notes at %s", root)

	report, err := audit.Run(root, pkgs, inv, notes)
	require.NoError(t, err, "audit.Run must not fail on a sound fixture")

	var buf bytes.Buffer
	audit.WriteReport(&buf, report)
	return report, buf.String()
}

// runArchAuditCLI drives arch-audit end-to-end through cli.Run and
// returns its exit code and stdout.
func runArchAuditCLI(t *testing.T, root string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"arch-audit", "--repo-root", root}, &stdout, &stderr)
	require.Empty(t, stderr.String(), "arch-audit must not write to stderr on a sound fixture")
	return code, stdout.String()
}

// --- 4.0-I1 — exit 0 when every check passes -------------------------------

// Test_4_0_I1_AllPassExitZero: a repository where C1, C2 and C3 all pass
// makes arch-audit exit 0 and print the all-PASS aggregate line.
func Test_4_0_I1_AllPassExitZero(t *testing.T) {
	root := buildAndResolve(t, archtest.SpecAuditAllPass())

	report, out := runAudit(t, root)
	require.True(t, report.Passed, "every check passes, so the audit passes; output:\n%s", out)
	require.True(t, report.C1.Passed && report.C2.Passed && report.C3.Passed && report.C4.Passed)
	require.Contains(t, out, "arch-audit: PASS (C1 PASS, C2 PASS, C3 PASS, C4 PASS)",
		"the aggregate line must report PASS for every check")

	code, cliOut := runArchAuditCLI(t, root)
	require.Equal(t, cli.ExitOK, code, "arch-audit must exit 0; output:\n%s", cliOut)
	require.Contains(t, cliOut, "arch-audit: PASS (C1 PASS, C2 PASS, C3 PASS, C4 PASS)")
}

// --- 4.0-I2 — one check fails; every check still runs ----------------------

// Test_4_0_I2_OneFailsExitNonZero: when C2 fails but C1 and C3 pass,
// arch-audit exits non-zero, the aggregate line shows C2 FAIL with C1 and
// C3 still PASS — proof arch-audit ran every check and did not stop at the
// first failure.
func Test_4_0_I2_OneFailsExitNonZero(t *testing.T) {
	root := buildAndResolve(t, archtest.SpecAuditC2Fails())

	report, out := runAudit(t, root)
	require.False(t, report.Passed, "a C2 failure fails the audit; output:\n%s", out)
	require.True(t, report.C1.Passed, "C1 must still have run and passed")
	require.False(t, report.C2.Passed, "C2 must have failed")
	require.True(t, report.C3.Passed, "C3 must still have run and passed")
	require.True(t, report.C4.Passed, "C4 must still have run and passed")
	require.Contains(t, out, "arch-audit: FAIL (C1 PASS, C2 FAIL, C3 PASS, C4 PASS)",
		"the aggregate line must report all four verdicts, C1/C3/C4 still PASS")

	code, cliOut := runArchAuditCLI(t, root)
	require.Equal(t, cli.ExitCheckFailed, code,
		"arch-audit must exit non-zero on a C2 failure; output:\n%s", cliOut)
	require.NotEqual(t, cli.ExitOK, code)
	require.Contains(t, cliOut, "arch-audit: FAIL (C1 PASS, C2 FAIL, C3 PASS, C4 PASS)")
}

// Test_4_0_I2_C4FailsExitNonZero: when C4 fails (an undocumented
// function) but C1, C2 and C3 pass, arch-audit exits non-zero, the
// aggregate line shows C4 FAIL with C1/C2/C3 still PASS — proof
// arch-audit ran every check and C4 is a blocking check.
func Test_4_0_I2_C4FailsExitNonZero(t *testing.T) {
	root := buildAndResolve(t, archtest.SpecAuditC4Fails())

	report, out := runAudit(t, root)
	require.False(t, report.Passed, "a C4 failure fails the audit; output:\n%s", out)
	require.True(t, report.C1.Passed, "C1 must still have run and passed")
	require.True(t, report.C2.Passed, "C2 must still have run and passed")
	require.True(t, report.C3.Passed, "C3 must still have run and passed")
	require.False(t, report.C4.Passed, "C4 must have failed on the undocumented helper")
	require.Contains(t, out, "arch-audit: FAIL (C1 PASS, C2 PASS, C3 PASS, C4 FAIL)",
		"the aggregate line must report C4 FAIL with C1/C2/C3 still PASS")
	require.Contains(t, out, "undocumented function:",
		"the C4 finding must appear in the audit output")

	code, cliOut := runArchAuditCLI(t, root)
	require.Equal(t, cli.ExitCheckFailed, code,
		"arch-audit must exit non-zero on a C4 failure; output:\n%s", cliOut)
	require.Contains(t, cliOut, "arch-audit: FAIL (C1 PASS, C2 PASS, C3 PASS, C4 FAIL)")
}

// --- 4.0-I3 — deterministic, sorted output ---------------------------------

// Test_4_0_I3_DeterministicOutput: a fixture with several C1 and C2
// violations at once. Two arch-audit runs over the unchanged repository
// produce byte-identical output, and the findings are grouped by check
// in the fixed C1 → C2 order — so a CI-log diff of two runs is empty.
func Test_4_0_I3_DeterministicOutput(t *testing.T) {
	root := buildAndResolve(t, archtest.SpecAuditMultiViolation())

	_, out1 := runAudit(t, root)
	_, out2 := runAudit(t, root)
	require.Equal(t, out1, out2,
		"two audit runs over an unchanged repository must be byte-identical")

	// The C1 summary must precede every C2 summary — findings are grouped
	// by check, C1 before C2.
	c1Idx := strings.Index(out1, "C1 completeness:")
	c2Idx := strings.Index(out1, "C2 dead-code:")
	c3Idx := strings.Index(out1, "C3 freshness:")
	require.True(t, c1Idx >= 0 && c2Idx >= 0 && c3Idx >= 0,
		"the report must carry all three check sections")
	require.Less(t, c1Idx, c2Idx, "C1 section must precede C2")
	require.Less(t, c2Idx, c3Idx, "C2 section must precede C3")

	// Within C1 the two undocumented-RPC findings are sorted by symbol:
	// Delete before Update.
	delIdx := strings.Index(out1, "NetworkService/Delete")
	updIdx := strings.Index(out1, "NetworkService/Update")
	require.True(t, delIdx >= 0 && updIdx >= 0, "both C1 findings must appear")
	require.Less(t, delIdx, updIdx, "C1 findings must be sorted by symbol")

	// Within C2 the two dead-code findings are sorted by symbol: Alpha
	// before Beta.
	alphaIdx := strings.Index(out1, "LegacyAlpha")
	betaIdx := strings.Index(out1, "LegacyBeta")
	require.True(t, alphaIdx >= 0 && betaIdx >= 0, "both C2 findings must appear")
	require.Less(t, alphaIdx, betaIdx, "C2 findings must be sorted by symbol")

	// And every C2 finding sits after the C2 summary, every C1 finding
	// after the C1 summary — confirming the grouping.
	require.Greater(t, delIdx, c1Idx, "a C1 finding belongs under the C1 summary")
	require.Less(t, updIdx, c2Idx, "C1 findings precede the C2 section")
	require.Greater(t, alphaIdx, c2Idx, "a C2 finding belongs under the C2 summary")
}

// --- 4.0-I4 — every FAIL line names its object and reason ------------------

// Test_4_0_I4_C1FindingNamesEntryPoint: a single C1 violation — an
// undocumented entry-point — produces a finding line carrying the
// offending entry-point's FQN and a reason. No bare "check failed".
func Test_4_0_I4_C1FindingNamesEntryPoint(t *testing.T) {
	root := buildAndResolve(t, archtest.SpecAuditC1Only())

	report, out := runAudit(t, root)
	require.False(t, report.C1.Passed, "C1 must fail on the undocumented Update")
	require.True(t, report.C2.Passed, "C2 must pass")
	require.True(t, report.C3.Passed, "C3 must pass")
	require.Len(t, report.C1.Findings, 1, "exactly one C1 finding")

	f := report.C1.Findings[0]
	require.Contains(t, f.Reason, "kacho.cloud.vpc.v1.NetworkService/Update",
		"the C1 finding must name the offending entry-point")
	require.Contains(t, f.Reason, "undocumented entry-point",
		"the C1 finding must state the reason")
	require.Contains(t, out, f.Reason, "the finding must appear verbatim in the report")
	require.NotContains(t, out, "check failed", "no bare check-failed line")
}

// Test_4_0_I4_C2FindingNamesSymbolAndPosition: a single C2 violation —
// dead code — produces a finding naming the exported symbol and its
// file:line position.
func Test_4_0_I4_C2FindingNamesSymbolAndPosition(t *testing.T) {
	root := buildAndResolve(t, archtest.SpecAuditC2Fails())

	report, out := runAudit(t, root)
	require.False(t, report.C2.Passed, "C2 must fail on the dead LegacyMigrateAddresses")
	require.Len(t, report.C2.Findings, 1, "exactly one C2 finding")

	f := report.C2.Findings[0]
	require.Contains(t, f.Reason, "LegacyMigrateAddresses",
		"the C2 finding must name the offending symbol")
	require.Contains(t, f.Reason, "internal/repo/legacy.go:",
		"the C2 finding must carry a repository-relative file:line position")
	require.Regexp(t, `internal/repo/legacy\.go:\d+`, f.Reason,
		"the position must be file:line")
	require.Contains(t, out, f.Reason)
}

// Test_4_0_I4_C3FindingNamesNoteAndHashes: a single C3 violation — a
// stale note — produces a finding naming the note path and the
// recorded-vs-actual hash pair.
func Test_4_0_I4_C3FindingNamesNoteAndHashes(t *testing.T) {
	// SpecAuditC3Only carries a deliberately stale literal source_sha, so
	// it is built without FreshSHA resolution.
	root := archtest.BuildRepo(t, archtest.SpecAuditC3Only())

	report, out := runAudit(t, root)
	require.True(t, report.C1.Passed, "C1 must pass")
	require.True(t, report.C2.Passed, "C2 must pass")
	require.False(t, report.C3.Passed, "C3 must fail on the stale note")
	require.Len(t, report.C3.Findings, 1, "exactly one C3 finding")

	f := report.C3.Findings[0]
	require.Contains(t, f.Reason, "docs/arch/network-lifecycle.md",
		"the C3 finding must name the offending note path")
	require.Contains(t, f.Reason, "0000000stalesha0000000",
		"the C3 finding must carry the recorded (stale) source_sha")
	require.Contains(t, f.Reason, "actual",
		"the C3 finding must carry the actual hash for comparison")
	require.Contains(t, out, f.Reason)
}

// --- 4.0-I5 — arch-gen exits != 0 when it cannot write ---------------------

// Test_4_0_I5_ArchGenWriteFailure: when docs/arch/generated/ is
// read-only, arch-gen cannot write its artifacts. It must exit non-zero
// with a stderr line "failed to write generated artifact: <path>: <os
// error>", and the temp-file+rename discipline must leave no
// half-written artifact behind.
func Test_4_0_I5_ArchGenWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only-directory write failure is POSIX-specific")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	root := buildAndResolve(t, archtest.SpecGenBasic())

	// Pre-create docs/arch/generated/ and make it read-only so arch-gen's
	// artifact write fails inside an existing directory.
	genDir := filepath.Join(root, "docs", "arch", "generated")
	require.NoError(t, os.MkdirAll(genDir, 0o755))
	require.NoError(t, os.Chmod(genDir, 0o555),
		"making docs/arch/generated/ read-only")
	t.Cleanup(func() { _ = os.Chmod(genDir, 0o755) })

	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"arch-gen", "--repo-root", root}, &stdout, &stderr)
	require.NotEqual(t, cli.ExitOK, code,
		"arch-gen must exit non-zero when it cannot write an artifact")

	errOut := stderr.String()
	require.Contains(t, errOut, "failed to write generated artifact:",
		"stderr must carry the stable I5 error prefix")
	require.Contains(t, errOut, genDir,
		"the error must name the artifact destination path")

	// Atomicity: a failed run leaves no half-written .md artifact and no
	// stray temp file in the read-only directory.
	entries, rerr := os.ReadDir(genDir)
	require.NoError(t, rerr)
	for _, e := range entries {
		require.False(t, strings.HasSuffix(e.Name(), ".md"),
			"a failed arch-gen run must leave no .md artifact: found %s", e.Name())
		require.False(t, strings.HasPrefix(e.Name(), ".archgraph-gen-"),
			"a failed arch-gen run must leave no temp file: found %s", e.Name())
	}
}

// --- 4.0-I6 — the CI drift gate --------------------------------------------

// Test_4_0_I6_DriftGateCleanOnUnchangedCode: on unchanged code a
// re-run of arch-gen rewrites nothing, so `git diff --exit-code` over
// the committed tree stays clean. There is no --check flag — the gate is
// the arch-gen + git-diff pairing.
func Test_4_0_I6_DriftGateCleanOnUnchangedCode(t *testing.T) {
	root := buildAndResolve(t, archtest.SpecGenBasic())

	// First arch-gen run produces the artifacts; commit the whole tree as
	// the baseline.
	mustArchGen(t, root)
	gitInit(t, root)

	// A second run over identical code must rewrite nothing.
	mustArchGen(t, root)
	out, code := gitDiff(t, root)
	require.Equal(t, 0, code,
		"arch-gen over unchanged code must leave git clean; diff:\n%s", out)
	require.Empty(t, strings.TrimSpace(out))
}

// Test_4_0_I6_DriftGateDirtyWhenReachableSetChanges: when the reachable
// set changes (a new reachable exported function appears) without a
// regenerating run, the next arch-gen rewrites the affected artifact and
// `git diff --exit-code` becomes non-zero — exactly the drift CI catches.
func Test_4_0_I6_DriftGateDirtyWhenReachableSetChanges(t *testing.T) {
	root := buildAndResolve(t, archtest.SpecGenBasic())

	// Baseline: generate and commit.
	mustArchGen(t, root)
	gitInit(t, root)

	// Mutate the code so a new exported function becomes reachable, then
	// regenerate. The L3 artifact must change relative to the committed
	// baseline.
	addReachableExportedFunc(t, root)
	mustArchGen(t, root)

	out, code := gitDiff(t, root)
	require.NotEqual(t, 0, code,
		"a changed reachable-set must make git diff non-zero after arch-gen")
	require.Contains(t, out, "docs/arch/generated/",
		"the drift must be in a generated artifact")
}

// --- I6 helpers ------------------------------------------------------------

// mustArchGen drives arch-gen over root and asserts it succeeds.
func mustArchGen(t *testing.T, root string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"arch-gen", "--repo-root", root}, &stdout, &stderr)
	require.Equal(t, cli.ExitOK, code,
		"arch-gen must succeed; stderr=%s", stderr.String())
}

// gitInit initialises a git repo at root and commits the whole tree.
func gitInit(t *testing.T, root string) {
	t.Helper()
	mustGit(t, root, "init", "-q")
	mustGit(t, root, "config", "user.email", "archgraph@test.local")
	mustGit(t, root, "config", "user.name", "archgraph test")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "baseline")
}

// mustGit runs a git subcommand at root and fails the test on error.
func mustGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
}

// gitDiff runs `git diff --exit-code` at root and returns the combined
// output and the process exit code (0 = clean, non-zero = differences).
func gitDiff(t *testing.T, root string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", "diff", "--exit-code")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, "git diff must fail only with an exit code")
	return string(out), exitErr.ExitCode()
}

// addReachableExportedFunc mutates the genbasic fixture's repo package
// so a new exported method, NewlyReachable, becomes reachable from the
// handler's Create RPC (Insert now calls it). The reachable-set the L3
// artifact is generated from therefore changes, so a regenerating
// arch-gen run rewrites the artifact and git diff goes non-zero.
func addReachableExportedFunc(t *testing.T, root string) {
	t.Helper()
	repoPath := filepath.Join(root, "internal", "repo", "repo.go")
	raw, err := os.ReadFile(repoPath)
	require.NoError(t, err, "reading repo.go")

	src := string(raw)
	// Insert now also calls the new exported NewlyReachable method.
	src = strings.Replace(src,
		"func (r *NetworkRepo) Insert() {\n\tr.encodeRow()\n}",
		"func (r *NetworkRepo) Insert() {\n\tr.encodeRow()\n\tr.NewlyReachable()\n}",
		1)
	require.Contains(t, src, "r.NewlyReachable()",
		"the Insert mutation must take effect")
	// The new exported method, reachable transitively from Create.
	src += "\n// NewlyReachable is a freshly exported method reachable from Create.\n" +
		"func (r *NetworkRepo) NewlyReachable() {}\n"

	require.NoError(t, os.WriteFile(repoPath, []byte(src), 0o644),
		"writing the mutated repo.go")
}
