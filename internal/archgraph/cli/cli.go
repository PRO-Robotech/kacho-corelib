// Package cli implements the command-line skeleton of archgraph: the
// archgraph code-architecture analyzer.
//
// archgraph exposes two subcommands:
//
//   - arch-gen   — generate architecture documentation;
//   - arch-audit — run blocking architecture checks.
//
// This package owns argument parsing, repository-root discovery, Go
// package loading (fail-fast on compile errors), check orchestration
// and exit-code policy. arch-audit runs the C1/C2/C3 architecture
// checks over the discovered entry-point inventory and the repository's
// L2 notes; any failure exits non-zero. arch-gen generates the L3 and L4
// architecture artifacts under docs/arch/generated/ and exits 0 on
// success.
//
// Exit codes (stable contract):
//
//	0 — success;
//	1 — an architecture check failed;
//	2 — a startup error (unknown subcommand, not a Go module, the
//	    analyzed repository fails to build).
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/audit"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/gen"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/reach"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/status"
)

// Exit codes form part of archgraph's stable CLI contract.
const (
	// ExitOK signals success.
	ExitOK = 0
	// ExitCheckFailed signals that an architecture check reported a
	// failure — arch-audit returns it when C1 (or, in later tasks,
	// C2/C3) finds a blocking deviation.
	ExitCheckFailed = 1
	// ExitStartupError signals a startup error: unknown subcommand,
	// the working directory is not inside a Go module, or the
	// analyzed repository does not compile.
	ExitStartupError = 2
)

// Subcommand names.
const (
	subGen   = "arch-gen"
	subAudit = "arch-audit"
)

// usageText is the help/usage message. It lists exactly the two
// supported subcommands and is intentionally deterministic so CI can
// assert on it.
const usageText = `archgraph — Kachō code-architecture analyzer

Usage:
  archgraph <subcommand> [flags]

Subcommands:
  arch-gen     Generate architecture documentation.
  arch-audit   Run blocking architecture checks.

Flags:
  --repo-root <dir>   Repository root to analyze (default: discovered
                      from the current directory by walking up to the
                      nearest go.mod).
  --help              Show this message.
`

// Run executes the archgraph CLI with the given arguments (excluding
// argv[0]) and returns a process exit code. It is the single entry
// point used by cmd/archgraph/main.go, kept thin and side-effect-free
// beyond writing to the supplied streams so it stays testable.
func Run(args []string, stdout, stderr io.Writer) int {
	// No subcommand: there is no default action — print usage to
	// stderr and fail.
	if len(args) == 0 {
		writeString(stderr, usageText)
		return ExitStartupError
	}

	// --help / -h in the leading position: usage to stdout, success.
	if args[0] == "--help" || args[0] == "-h" {
		writeString(stdout, usageText)
		return ExitOK
	}

	sub := args[0]
	rest := args[1:]
	if sub != subGen && sub != subAudit {
		writeString(stderr, fmt.Sprintf(
			"unknown subcommand %q; expected %s or %s\n",
			sub, subGen, subAudit))
		return ExitStartupError
	}

	repoRoot, err := resolveRepoRoot(rest)
	if err != nil {
		writeString(stderr, err.Error()+"\n")
		return ExitStartupError
	}

	pkgs, err := loadPackages(repoRoot)
	if err != nil {
		writeString(stderr, err.Error()+"\n")
		return ExitStartupError
	}

	// Build the entry-point inventory: the roots from which later tasks
	// walk the call-graph. A FQN-resolution failure (B7) is a startup
	// error — the repository is gRPC-shaped but unanalysable.
	inv, err := entrypoints.Discover(pkgs)
	if err != nil {
		writeString(stderr, err.Error()+"\n")
		return ExitStartupError
	}

	switch sub {
	case subGen:
		return runGen(repoRoot, pkgs, inv, stdout, stderr)
	case subAudit:
		return runAudit(repoRoot, pkgs, inv, stdout, stderr)
	}
	return ExitOK
}

// runGen runs the arch-gen pass. It does two things:
//
//   - generates the L3 (per-functionality call-tree + reachable exported
//     signatures) and L4 (per-repository domain-type / field / constant
//     tables) artifacts under docs/arch/generated/;
//   - computes each curated L2 note's "status" field from how its anchors
//     match the discovered entry-point inventory and writes that value
//     back into the note's frontmatter.
//
// The status write-back is surgical — only the "status" scalar of a
// curated note is rewritten, never its narrative body or any other
// frontmatter key — and idempotent: a note whose status is already
// correct is left byte-identical. Together with `git diff --exit-code`
// in CI this catches a hand-edited status that no longer matches the
// code.
//
// arch-gen exits 0 on success and ExitStartupError on a reachability or
// filesystem failure — it raises no architecture verdict of its own.
func runGen(
	repoRoot string,
	pkgs []*packages.Package,
	inv *entrypoints.Inventory,
	stdout, stderr io.Writer,
) int {
	notes, err := loadArchNotes(repoRoot)
	if err != nil {
		writeString(stderr, err.Error()+"\n")
		return ExitStartupError
	}

	// The reachability graph is the source of every L3 artifact's
	// call-tree and reachable-signature set.
	g, err := reach.Compute(pkgs, inv)
	if err != nil {
		writeString(stderr, err.Error()+"\n")
		return ExitStartupError
	}

	if err := gen.Generate(repoRoot, pkgs, inv, g, notes); err != nil {
		writeString(stderr, err.Error()+"\n")
		return ExitStartupError
	}

	// Compute and write back the "status" of every curated L2 note. A
	// note whose computed status already matches its frontmatter is left
	// untouched (note.Write is idempotent for an unchanged scalar).
	if err := writeBackStatus(notes, inv); err != nil {
		writeString(stderr, err.Error()+"\n")
		return ExitStartupError
	}

	writeString(stdout, fmt.Sprintf(
		"arch-gen: generated L3/L4 artifacts under %s/docs/arch/generated\n",
		repoRoot))
	return ExitOK
}

// writeBackStatus computes the status of each L2 note from the entry-point
// inventory and persists it into the note's frontmatter. The note package
// rewrites only the "status" scalar, so the curated narrative body and
// every other frontmatter key are preserved; a note already carrying the
// computed status is written back byte-identically. A filesystem failure
// on any note aborts the pass.
func writeBackStatus(notes []*note.Note, inv *entrypoints.Inventory) error {
	for _, n := range notes {
		n.SetStatus(status.Compute(n, inv))
		if err := n.Write(); err != nil {
			return fmt.Errorf("arch-gen: write back status of %s: %w", n.Path(), err)
		}
	}
	return nil
}

// archNotesDir is the conventional location of a repository's L2
// functionality notes, relative to its root.
const archNotesDir = "docs/arch"

// runAudit runs the blocking architecture audit and returns the process
// exit code. The C1/C2/C3 orchestration — running every check
// unconditionally, sharing a single reachability build between C2 and
// C3, aggregating the verdict and rendering the deterministic report —
// lives in the audit package; runAudit is the thin transport layer that
// loads the notes, calls audit.Run, prints the report and maps its
// aggregate verdict to an exit code.
//
// arch-audit exits ExitCheckFailed if any check fails (a SKIP verdict
// carries Passed=true, so it never makes the run fail) and
// ExitStartupError on an internal analysis failure.
func runAudit(
	repoRoot string,
	pkgs []*packages.Package,
	inv *entrypoints.Inventory,
	stdout, stderr io.Writer,
) int {
	notes, err := loadArchNotes(repoRoot)
	if err != nil {
		writeString(stderr, err.Error()+"\n")
		return ExitStartupError
	}

	report, err := audit.Run(repoRoot, pkgs, inv, notes)
	if err != nil {
		writeString(stderr, err.Error()+"\n")
		return ExitStartupError
	}

	// Render the B5 entry-point hints and the C1/C2/C3 verdicts, ending
	// with the aggregate "arch-audit: PASS|FAIL (...)" line.
	audit.WriteReportWithHints(stdout, report, inv)

	if !report.Passed {
		return ExitCheckFailed
	}
	return ExitOK
}

// loadArchNotes loads the L2 functionality notes under repoRoot's
// docs/arch directory. A repository with no docs/arch directory simply
// has no notes — that is not an error here (C1 will then report every
// entry-point as undocumented). A malformed note is a hard error.
func loadArchNotes(repoRoot string) ([]*note.Note, error) {
	dir := filepath.Join(repoRoot, archNotesDir)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	return note.Load(dir)
}

// writeString writes s to w. A failure to write to the process's
// stdout/stderr is unrecoverable and intentionally ignored: there is
// no second channel to report it on.
func writeString(w io.Writer, s string) {
	_, _ = io.WriteString(w, s)
}

// resolveRepoRoot determines the repository root to analyze. A
// --repo-root <dir> flag, when present, takes precedence; otherwise
// the root is discovered by walking up from the current working
// directory to the nearest go.mod.
func resolveRepoRoot(args []string) (string, error) {
	explicit, err := parseRepoRootFlag(args)
	if err != nil {
		return "", err
	}

	start := explicit
	if start == "" {
		wd, werr := os.Getwd()
		if werr != nil {
			return "", fmt.Errorf("cannot determine working directory: %w", werr)
		}
		start = wd
	}

	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path %q: %w", start, err)
	}

	root, found := findModuleRoot(abs)
	if !found {
		return "", fmt.Errorf(
			"not a Go module: no go.mod found in %s or parent directories",
			abs)
	}
	return root, nil
}

// parseRepoRootFlag extracts an optional --repo-root value from args.
// It accepts both "--repo-root <dir>" and "--repo-root=<dir>" forms.
// Unknown flags are ignored at the skeleton stage (later tasks own
// the full flag set).
func parseRepoRootFlag(args []string) (string, error) {
	const flag = "--repo-root"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == flag:
			if i+1 >= len(args) {
				return "", errors.New("--repo-root requires a directory argument")
			}
			return args[i+1], nil
		case len(a) > len(flag)+1 && a[:len(flag)+1] == flag+"=":
			return a[len(flag)+1:], nil
		}
	}
	return "", nil
}

// findModuleRoot walks up from dir looking for a directory containing
// a go.mod file. It returns that directory and true, or "" and false
// if the filesystem root is reached without a match.
func findModuleRoot(dir string) (string, bool) {
	for {
		if isFile(filepath.Join(dir, "go.mod")) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// isFile reports whether path exists and is a regular file.
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// loadPackages loads every package of the repository rooted at
// repoRoot and fails fast if any package has build or type-check
// errors. This guarantees later analysis runs only over a sound
// graph: a compile failure is reported on its own, before any
// architecture check. On success it returns the loaded packages so
// downstream passes (entry-point discovery, the call-graph) reuse the
// single load.
func loadPackages(repoRoot string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedImports |
			packages.NeedDeps |
			packages.NeedTypes |
			packages.NeedSyntax |
			packages.NeedTypesInfo,
		Dir:   repoRoot,
		Tests: false,
		// Pin the toolchain to the locally installed one and disable
		// go.work: archgraph must analyze code offline and
		// deterministically. GOTOOLCHAIN=local stops the loader pausing
		// to download a Go toolchain referenced by the target
		// repository's go.mod. GOWORK=off stops an ambient go.work of a
		// surrounding multi-module checkout (the kacho repos use
		// `replace ../` in go.mod and a workspace go.work) pinning a
		// stale go directive or pulling sibling modules into the load —
		// archgraph's analysis must depend only on the repository under
		// --repo-root, not on the directory it happens to be run from.
		Env: append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("failed to load packages: %w", err)
	}

	// Collect the first compile error deterministically: packages and
	// their errors are reported in a stable order by go/packages.
	for _, pkg := range pkgs {
		if len(pkg.Errors) > 0 {
			e := pkg.Errors[0]
			pos := e.Pos
			if pos == "" {
				pos = "unknown position"
			}
			return nil, fmt.Errorf(
				"failed to load packages: %s has compile errors: %s: %s",
				pkg.PkgPath, pos, e.Msg)
		}
	}
	return pkgs, nil
}
