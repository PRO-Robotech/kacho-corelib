package main_test

// Integration tests for the archgraph CLI skeleton.
//
// Each test maps 1:1 to an acceptance scenario from group A of the
// sub-phase 4.0 acceptance document (archgraph CLI skeleton). The
// scenario ID is encoded in the test name for traceability.
//
// The CLI is exercised as a real subprocess: the binary is built once
// per test run and invoked with controlled cwd / args, so exit codes
// and stdout/stderr are observed exactly as a CI pipeline would see
// them.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/archtest"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/check"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/reach"
)

// buildOnce builds the archgraph binary a single time for the whole
// test binary and returns its absolute path.
var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

func archgraphBin(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "archgraph-bin-")
		if err != nil {
			buildErr = err
			return
		}
		bin := filepath.Join(dir, "archgraph")
		// Resolve the package directory (this test file's dir).
		cmd := exec.Command("go", "build", "-o", bin, ".")
		cmd.Dir = mustPkgDir()
		out, err := cmd.CombinedOutput()
		if err != nil {
			buildErr = errWithOutput(err, out)
			return
		}
		binPath = bin
	})
	require.NoError(t, buildErr, "building archgraph binary")
	require.NotEmpty(t, binPath)
	return binPath
}

// mustPkgDir returns the directory containing the archgraph command
// package (the directory of this test file).
func mustPkgDir() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	return wd
}

func errWithOutput(err error, out []byte) error {
	return &buildFailure{err: err, out: string(out)}
}

type buildFailure struct {
	err error
	out string
}

func (b *buildFailure) Error() string {
	return b.err.Error() + "\n" + b.out
}

// runResult captures the observable outcome of one CLI invocation.
type runResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// run executes the archgraph binary with the given working directory
// and arguments.
func run(t *testing.T, workdir string, args ...string) runResult {
	t.Helper()
	bin := archgraphBin(t)
	cmd := exec.Command(bin, args...)
	cmd.Dir = workdir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if asExit(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("running archgraph: %v", err)
		}
	}
	return runResult{stdout: stdout.String(), stderr: stderr.String(), exitCode: code}
}

func asExit(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

// fixture builds a fixture Spec into a fresh temp module and returns the
// absolute path to its module root, for invoking the CLI against.
func fixture(t *testing.T, spec archtest.Spec) string {
	t.Helper()
	return archtest.BuildRepo(t, spec)
}

// fixtureFresh builds a fixture and then resolves every L2 note carrying
// the archtest.FreshSHA sentinel to a genuine C3 freshness hash. An
// arch-audit run evaluates C1, C2 and C3 together, so a fixture meant to
// pass arch-audit must have its notes recorded as C3-fresh — that is the
// second phase of the FreshSHA two-phase build.
func fixtureFresh(t *testing.T, spec archtest.Spec) string {
	t.Helper()
	root := archtest.BuildRepo(t, spec)
	resolveFreshNotesAt(t, root)
	return root
}

// resolveFreshNotesAt rewrites every FreshSHA-sentinel note under root
// with the genuine hash of its anchors' reachable-set. It reloads the
// repository's packages, recomputes the reachability graph and hashes
// via check.AnchorHash — the exact computation arch-audit's C3 will
// perform — so the recorded source_sha matches. It is called both after
// the initial build and again after any in-place mutation of a fixture
// whose reachable-set files changed (the F6 continuation).
func resolveFreshNotesAt(t *testing.T, root string) {
	t.Helper()
	g := graphAt(t, root)
	archtest.ResolveFreshNotes(t, root, func(notePath string) string {
		n, err := note.Parse(notePath)
		require.NoError(t, err, "parsing sentinel note %s", notePath)
		return check.AnchorHash(root, n, g)
	})
}

// graphAt loads the module at root and computes its reachability graph —
// the input check.AnchorHash needs to hash a note's reachable-set.
func graphAt(t *testing.T, root string) *reach.Graph {
	t.Helper()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports |
			packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax |
			packages.NeedTypesInfo,
		Dir:   root,
		Tests: false,
		Env:   append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	require.NoError(t, err, "loading fixture at %s", root)
	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err, "discovering entry-points at %s", root)
	g, err := reach.Compute(pkgs, inv)
	require.NoError(t, err, "computing reachability at %s", root)
	return g
}

// snapshotTree records the relative paths of every file under root.
func snapshotTree(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	seen := map[string]struct{}{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		seen[rel] = struct{}{}
		return nil
	})
	require.NoError(t, err)
	return seen
}

// requireNoFSChange asserts the file tree under root is unchanged
// relative to a previously taken snapshot.
func requireNoFSChange(t *testing.T, root string, before map[string]struct{}) {
	t.Helper()
	after := snapshotTree(t, root)
	require.Equal(t, before, after, "archgraph must not write to the filesystem")
}

// Test_4_0_A1_BuildAndUsage: the binary builds, prints usage listing
// exactly the two subcommands when invoked without one (non-zero
// exit), and prints the same usage on --help with exit code 0.
func Test_4_0_A1_BuildAndUsage(t *testing.T) {
	// `go build ./cmd/archgraph` succeeds and produces an artifact.
	bin := archgraphBin(t)
	info, err := os.Stat(bin)
	require.NoError(t, err)
	require.False(t, info.IsDir())

	// No subcommand: usage + non-zero exit.
	noSub := run(t, mustPkgDir())
	require.NotEqual(t, 0, noSub.exitCode, "no subcommand must exit non-zero")
	usage := noSub.stdout + noSub.stderr
	require.Contains(t, usage, "arch-gen")
	require.Contains(t, usage, "arch-audit")
	require.Contains(t, strings.ToLower(usage), "usage")

	// --help: same usage, exit 0.
	help := run(t, mustPkgDir(), "--help")
	require.Equal(t, 0, help.exitCode, "--help must exit 0")
	helpText := help.stdout + help.stderr
	require.Contains(t, helpText, "arch-gen")
	require.Contains(t, helpText, "arch-audit")
	require.Contains(t, strings.ToLower(helpText), "usage")
}

// Test_4_0_A2_UnknownSubcommand: an unrecognised subcommand exits
// non-zero with a precise error on stderr and writes nothing to disk.
func Test_4_0_A2_UnknownSubcommand(t *testing.T) {
	workdir := t.TempDir()
	before := snapshotTree(t, workdir)

	res := run(t, workdir, "arch-frobnicate")
	require.NotEqual(t, 0, res.exitCode)
	require.Contains(t, res.stderr,
		`unknown subcommand "arch-frobnicate"; expected arch-gen or arch-audit`)

	requireNoFSChange(t, workdir, before)
}

// Test_4_0_A3_RunFromRepoRoot: invoked at the root of a valid Go
// module with no --repo-root flag, archgraph detects the module via
// go.mod and the subcommand succeeds.
func Test_4_0_A3_RunFromRepoRoot(t *testing.T) {
	repo := fixture(t, archtest.SpecValidRepo())

	res := run(t, repo, "arch-audit")
	require.Equal(t, 0, res.exitCode,
		"arch-audit on a valid repo must succeed; stderr=%s", res.stderr)
}

// Test_4_0_A4_RunOutsideGoModule: invoked where no go.mod exists in
// the cwd or any parent, archgraph fails fast with a clear message
// and generates no artifacts.
func Test_4_0_A4_RunOutsideGoModule(t *testing.T) {
	// A fresh temp dir is guaranteed to have no go.mod anywhere up the
	// tree within the test sandbox boundaries we control; the message
	// must name the cwd.
	workdir := t.TempDir()
	before := snapshotTree(t, workdir)

	res := run(t, workdir, "arch-audit")
	require.NotEqual(t, 0, res.exitCode)
	require.Contains(t, res.stderr,
		"not a Go module: no go.mod found in "+workdir+" or parent directories")

	requireNoFSChange(t, workdir, before)
}

// Test_4_0_A5_CompileErrorFailFast: a fixture whose Go code does not
// compile causes both subcommands to fail fast with a load error
// (naming the package and a position) and no false audit failures.
func Test_4_0_A5_CompileErrorFailFast(t *testing.T) {
	repo := fixture(t, archtest.SpecCompileError())

	for _, sub := range []string{"arch-audit", "arch-gen"} {
		sub := sub
		t.Run(sub, func(t *testing.T) {
			res := run(t, repo, sub)
			require.NotEqual(t, 0, res.exitCode)
			require.Contains(t, res.stderr, "failed to load packages:")
			require.Contains(t, res.stderr, "compile errors")
			// The build error is reported on its own; no C1/C2/C3
			// audit verdicts are emitted over an unusable graph.
			require.NotContains(t, res.stdout, "C1")
			require.NotContains(t, res.stdout, "C2")
			require.NotContains(t, res.stdout, "C3")
		})
	}
}

// Test_4_0_E1_C1PassExitZero: arch-audit on a repo whose every
// entry-point is anchored exactly once prints the C1 PASS summary and
// exits 0.
func Test_4_0_E1_C1PassExitZero(t *testing.T) {
	res := run(t, fixtureFresh(t, archtest.SpecC1Pass()), "arch-audit")
	require.Equal(t, 0, res.exitCode,
		"a complete repo must pass C1; stderr=%s", res.stderr)
	require.Contains(t, res.stdout,
		"C1 completeness: PASS (3/3 entry-points anchored)")
	require.Contains(t, res.stdout, "C3 freshness: PASS")
}

// Test_4_0_E2_C1FailUndocumentedExitNonZero: an undocumented
// entry-point makes arch-audit print the C1 FAIL summary plus the
// undocumented-entry-point finding and exit non-zero.
func Test_4_0_E2_C1FailUndocumentedExitNonZero(t *testing.T) {
	res := run(t, fixture(t, archtest.SpecC1Undocumented()), "arch-audit")
	require.NotEqual(t, 0, res.exitCode, "an undocumented entry-point must fail C1")
	out := res.stdout + res.stderr
	require.Contains(t, out, "C1 completeness: FAIL")
	require.Contains(t, out,
		"undocumented entry-point: kacho.cloud.vpc.v1.NetworkService/Update — "+
			"declare it in an L2 note's anchors or remove it")
}

// Test_4_0_E3_C1FailStaleAnchorExitNonZero: a stale anchor makes
// arch-audit print the C1 FAIL summary plus the stale-anchor finding
// and exit non-zero.
func Test_4_0_E3_C1FailStaleAnchorExitNonZero(t *testing.T) {
	res := run(t, fixture(t, archtest.SpecC1StaleAnchor()), "arch-audit")
	require.NotEqual(t, 0, res.exitCode, "a stale anchor must fail C1")
	out := res.stdout + res.stderr
	require.Contains(t, out, "C1 completeness: FAIL")
	require.Contains(t, out,
		"stale anchor: kacho.cloud.vpc.v1.NetworkService/Patch in "+
			"docs/arch/network-lifecycle.md points to a non-existent entry-point")
}

// Test_4_0_E4_C1FailDuplicateAnchorExitNonZero: an entry-point anchored
// in two notes makes arch-audit print the C1 FAIL summary plus the
// duplicate finding (note names sorted) and exit non-zero.
func Test_4_0_E4_C1FailDuplicateAnchorExitNonZero(t *testing.T) {
	res := run(t, fixture(t, archtest.SpecC1DuplicateAnchor()), "arch-audit")
	require.NotEqual(t, 0, res.exitCode, "a duplicate anchor must fail C1")
	out := res.stdout + res.stderr
	require.Contains(t, out, "C1 completeness: FAIL")
	require.Contains(t, out,
		"entry-point kacho.cloud.vpc.v1.NetworkService/Create anchored in 2 notes "+
			"(network-bootstrap.md, network-lifecycle.md) — must be exactly one")
}

// Test_4_0_E5_C1NonConventionalWorkerHint: an anchored worker that was
// not discovered as an entry-point reads as a stale anchor; arch-audit
// fails C1 and also prints the convention-clarifying hint line.
func Test_4_0_E5_C1NonConventionalWorkerHint(t *testing.T) {
	res := run(t, fixture(t, archtest.SpecC1NonConventionalWorker()), "arch-audit")
	require.NotEqual(t, 0, res.exitCode)
	out := res.stdout + res.stderr
	require.Contains(t, out, "C1 completeness: FAIL")
	require.Contains(t, out,
		"stale anchor: NetworkOutboxWorker in docs/arch/network-outbox.md "+
			"points to a non-existent entry-point")
	require.Contains(t, out,
		"hint: an anchored worker NetworkOutboxWorker was not discovered as an "+
			"entry-point — verify it follows the New<Name>+Run/Start convention")
}

// Test_4_0_E6_C1EmptyAnchorsIgnored: an L2 note with an empty anchors
// list does not break C1 — arch-audit still passes and exits 0 when the
// other notes cover every entry-point.
func Test_4_0_E6_C1EmptyAnchorsIgnored(t *testing.T) {
	res := run(t, fixtureFresh(t, archtest.SpecC1EmptyAnchors()), "arch-audit")
	require.Equal(t, 0, res.exitCode,
		"an empty-anchors note must not fail C1; stderr=%s", res.stderr)
	require.Contains(t, res.stdout,
		"C1 completeness: PASS (2/2 entry-points anchored)")
	require.Contains(t, res.stdout, "C3 freshness: PASS")
}

// Test_4_0_F1_C2PassExitZero: arch-audit on a repo whose every exported
// symbol is reachable prints the C2 PASS summary and exits 0.
func Test_4_0_F1_C2PassExitZero(t *testing.T) {
	res := run(t, fixtureFresh(t, archtest.SpecC2Pass()), "arch-audit")
	require.Equal(t, 0, res.exitCode,
		"a repo with no dead code must pass C2; stderr=%s", res.stderr)
	require.Contains(t, res.stdout,
		"C2 dead-code: PASS (0 unreachable exported symbols)")
	require.Contains(t, res.stdout, "C3 freshness: PASS")
}

// Test_4_0_F2_C2FailUnreachableExitNonZero: an unreachable exported
// symbol makes arch-audit print the C2 FAIL summary plus the dead-code
// finding (symbol + file:line) and exit non-zero.
func Test_4_0_F2_C2FailUnreachableExitNonZero(t *testing.T) {
	res := run(t, fixture(t, archtest.SpecC2Unreachable()), "arch-audit")
	require.NotEqual(t, 0, res.exitCode, "an unreachable exported symbol must fail")
	out := res.stdout + res.stderr
	require.Contains(t, out, "C2 dead-code: FAIL")
	require.Contains(t, out,
		"dead code: exported symbol LegacyMigrateAddresses "+
			"(internal/repo/legacy.go:14) unreachable from any entry-point")
}

// Test_4_0_F3_C2KeepSuppresses: a `// archgraph:keep <reason>`
// annotation over an unreachable exported symbol keeps C2 passing and
// makes arch-audit print the kept-symbol line; C2 does not contribute a
// non-zero exit.
func Test_4_0_F3_C2KeepSuppresses(t *testing.T) {
	res := run(t, fixtureFresh(t, archtest.SpecC2Keep()), "arch-audit")
	require.Equal(t, 0, res.exitCode,
		"a kept symbol must not fail the audit; stderr=%s", res.stderr)
	require.Contains(t, res.stdout, "C2 dead-code: PASS")
	require.Contains(t, res.stdout,
		"kept: BuildClientSDK (reason: public SDK surface, "+
			"consumed by external clients)")
}

// Test_4_0_F4_C2KeepWithoutReasonRejected: a bare `// archgraph:keep`
// with no reason makes arch-audit print the C2 FAIL summary plus the
// invalid-annotation finding and exit non-zero.
func Test_4_0_F4_C2KeepWithoutReasonRejected(t *testing.T) {
	res := run(t, fixture(t, archtest.SpecC2KeepBare()), "arch-audit")
	require.NotEqual(t, 0, res.exitCode, "a bare archgraph:keep must fail")
	out := res.stdout + res.stderr
	require.Contains(t, out, "C2 dead-code: FAIL")
	require.Contains(t, out,
		"invalid annotation: // archgraph:keep at internal/repo/x.go:9 "+
			"requires a non-empty reason")
}

// Test_4_0_F5_C2KeepIsTransitive: a kept function is an extra
// reachability root — arch-audit passes C2 because nothing transitively
// reachable from the kept BuildClientSDK is reported as dead.
func Test_4_0_F5_C2KeepIsTransitive(t *testing.T) {
	res := run(t, fixtureFresh(t, archtest.SpecC2KeepTransitive()), "arch-audit")
	require.Equal(t, 0, res.exitCode,
		"keep transitivity must keep C2 passing; stderr=%s", res.stderr)
	require.Contains(t, res.stdout, "C2 dead-code: PASS")
	require.NotContains(t, res.stdout, "EncodeManifest")
}

// Test_4_0_F6_C2ReflectionKeepContinuation: a reflection-only exported
// method is reported dead by C2 (an expected RTA imprecision). The test
// runs as a continuation: first arch-audit on a freshly built f6 fixture
// FAILS; after injecting `// archgraph:keep reflection-invoked` above the
// method, a second arch-audit PASSES.
func Test_4_0_F6_C2ReflectionKeepContinuation(t *testing.T) {
	// The fixture is built into its own t.TempDir(), so phase 2 may
	// mutate it in place — nothing checked into the repo is touched.
	work := fixture(t, archtest.SpecC2Reflection())

	// Phase 1 — before annotation: C2 fails on the reflection-only method.
	before := run(t, work, "arch-audit")
	require.NotEqual(t, 0, before.exitCode,
		"a reflection-only method must fail C2 before annotation")
	out := before.stdout + before.stderr
	require.Contains(t, out, "C2 dead-code: FAIL")
	require.Contains(t, out, "ReflectInvoke")

	// Inject the keep annotation directly above the method declaration.
	regFile := filepath.Join(work, "internal", "registry", "registry.go")
	src, err := os.ReadFile(regFile)
	require.NoError(t, err)
	annotated := strings.Replace(string(src),
		"// ReflectInvoke is exported and invoked only via reflect.Value.Call.\n",
		"// ReflectInvoke is exported and invoked only via reflect.Value.Call.\n"+
			"// archgraph:keep reflection-invoked\n",
		1)
	require.NotEqual(t, string(src), annotated, "annotation must be injected")
	require.NoError(t, os.WriteFile(regFile, []byte(annotated), 0o644))

	// The annotation revives registry.go into the keep-root reachable-set,
	// so the L2 note's freshness hash changes. Re-resolve its FreshSHA
	// sentinel against the mutated tree before phase 2, so C3 — which
	// arch-audit now also evaluates — does not spuriously fail.
	resolveFreshNotesAt(t, work)

	// Phase 2 — after annotation: C2 (and C3) pass.
	after := run(t, work, "arch-audit")
	require.Equal(t, 0, after.exitCode,
		"after the keep annotation C2 must pass; stderr=%s", after.stderr)
	require.Contains(t, after.stdout, "C2 dead-code: PASS")
}

// Test_4_0_F7_C2LibrarySkip: a pure library repo (no main package) makes
// arch-audit print the C2 SKIP summary; C2 does not contribute to the
// exit code while C1 still runs.
func Test_4_0_F7_C2LibrarySkip(t *testing.T) {
	res := run(t, fixture(t, archtest.SpecC2Library()), "arch-audit")
	require.Equal(t, 0, res.exitCode,
		"a library repo must not fail the audit; stderr=%s", res.stderr)
	require.Contains(t, res.stdout,
		"C2 dead-code: SKIP (library repo: no main package)")
}
