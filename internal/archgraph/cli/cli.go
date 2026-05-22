// Package cli implements the command-line skeleton of archgraph: the
// archgraph code-architecture analyzer.
//
// archgraph exposes two subcommands:
//
//   - arch-gen   — generate architecture documentation;
//   - arch-audit — run blocking architecture checks.
//
// This package owns argument parsing, repository-root discovery, Go
// package loading (fail-fast on compile errors) and exit-code policy.
// The actual generation / audit logic is added by later tasks; for
// now both subcommands are success stubs once package loading
// succeeds.
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
)

// Exit codes form part of archgraph's stable CLI contract.
const (
	// ExitOK signals success.
	ExitOK = 0
	// ExitCheckFailed signals that an architecture check reported a
	// failure (used by later tasks; the skeleton never returns it).
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

	if err := loadPackages(repoRoot); err != nil {
		writeString(stderr, err.Error()+"\n")
		return ExitStartupError
	}

	// Skeleton stubs: package loading succeeded, so the subcommand is
	// reported as a no-op success. Real generation / auditing arrives
	// in later tasks.
	switch sub {
	case subGen:
		writeString(stdout, fmt.Sprintf(
			"arch-gen: loaded repository %s (no documentation generated yet)\n", repoRoot))
	case subAudit:
		writeString(stdout, fmt.Sprintf(
			"arch-audit: loaded repository %s (no checks run yet)\n", repoRoot))
	}
	return ExitOK
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
// architecture check.
func loadPackages(repoRoot string) error {
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
		// Pin the toolchain to the locally installed one: archgraph
		// must analyze code offline and deterministically, never
		// pause to download a Go toolchain referenced by the target
		// repository's go.mod.
		Env: append(os.Environ(), "GOTOOLCHAIN=local"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return fmt.Errorf("failed to load packages: %w", err)
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
			return fmt.Errorf(
				"failed to load packages: %s has compile errors: %s: %s",
				pkg.PkgPath, pos, e.Msg)
		}
	}
	return nil
}
