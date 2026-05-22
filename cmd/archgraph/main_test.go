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

// fixture returns the absolute path to a testdata fixture directory.
func fixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", name))
	require.NoError(t, err)
	return p
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
	repo := fixture(t, "validrepo")

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
	repo := fixture(t, "compileerror")

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
	res := run(t, fixture(t, "c1-e1"), "arch-audit")
	require.Equal(t, 0, res.exitCode,
		"a complete repo must pass C1; stderr=%s", res.stderr)
	require.Contains(t, res.stdout,
		"C1 completeness: PASS (3/3 entry-points anchored)")
}

// Test_4_0_E2_C1FailUndocumentedExitNonZero: an undocumented
// entry-point makes arch-audit print the C1 FAIL summary plus the
// undocumented-entry-point finding and exit non-zero.
func Test_4_0_E2_C1FailUndocumentedExitNonZero(t *testing.T) {
	res := run(t, fixture(t, "c1-e2"), "arch-audit")
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
	res := run(t, fixture(t, "c1-e3"), "arch-audit")
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
	res := run(t, fixture(t, "c1-e4"), "arch-audit")
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
	res := run(t, fixture(t, "c1-e5"), "arch-audit")
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
	res := run(t, fixture(t, "c1-e6"), "arch-audit")
	require.Equal(t, 0, res.exitCode,
		"an empty-anchors note must not fail C1; stderr=%s", res.stderr)
	require.Contains(t, res.stdout,
		"C1 completeness: PASS (2/2 entry-points anchored)")
}
