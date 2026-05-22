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
