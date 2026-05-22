package cli_test

// Integration tests for arch-gen's computed-status write-back pass.
//
// Each test maps 1:1 to an acceptance scenario from group H of the
// sub-phase 4.0 acceptance document (archgraph — computed status +
// write-back into L2 note frontmatter). The scenario ID is encoded in the
// test name for traceability.
//
//	4.0-H1 — all anchors resolve            → status: implemented
//	4.0-H2 — some anchors resolve           → status: partial
//	4.0-H3 — no anchors resolve / empty     → status: planned
//	4.0-H4 — a hand-typed wrong status is overwritten; git diff catches it
//
// Fixtures are synthesised by archtest.BuildRepo into t.TempDir(); arch-gen
// is driven through cli.Run("arch-gen", "--repo-root", <fixture>). No
// testcontainers, no network: every fixture is a self-contained Go module.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/archtest"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/cli"
)

// runArchGen drives arch-gen over the fixture rooted at root via cli.Run,
// asserting it exits successfully, and returns its stdout.
func runArchGen(t *testing.T, root string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{"arch-gen", "--repo-root", root}, &stdout, &stderr)
	require.Equal(t, cli.ExitOK, code,
		"arch-gen must succeed; stderr=%s", stderr.String())
	return stdout.String()
}

// curatedNotePath is the absolute path of a group-H fixture's single
// curated L2 note.
func curatedNotePath(root string) string {
	return filepath.Join(root, "docs", "arch", "network-lifecycle.md")
}

// readNote reads the curated L2 note of a group-H fixture.
func readNote(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(curatedNotePath(root))
	require.NoError(t, err, "reading curated L2 note")
	return string(data)
}

// Test_4_0_H1_StatusImplemented: a note whose every anchor resolves to a
// discoverable entry-point gets status: implemented written back. The
// write-back touches only the status scalar — every other frontmatter key
// and the narrative body stay byte-identical.
func Test_4_0_H1_StatusImplemented(t *testing.T) {
	root := archtest.BuildRepo(t, archtest.SpecStatusImplemented())
	before := readNote(t, root)
	require.Contains(t, before, "status: planned",
		"the fixture must be authored with the wrong status so the write-back is observable")

	runArchGen(t, root)

	after := readNote(t, root)
	require.Contains(t, after, "status: implemented",
		"arch-gen must write the computed status: implemented")
	require.NotContains(t, after, "status: planned",
		"the stale status: planned must be gone")

	// Only the status scalar changed: the narrative body and every other
	// frontmatter key survive byte-for-byte.
	require.Equal(t, frontmatterMinusStatus(before), frontmatterMinusStatus(after),
		"arch-gen must change only the status scalar of the frontmatter")
	require.Equal(t, bodyOf(before), bodyOf(after),
		"arch-gen must not touch the curated narrative body")
}

// Test_4_0_H2_StatusPartial: a note anchoring one existing and one absent
// entry-point gets status: partial written back.
func Test_4_0_H2_StatusPartial(t *testing.T) {
	root := archtest.BuildRepo(t, archtest.SpecStatusPartial())
	before := readNote(t, root)

	runArchGen(t, root)

	after := readNote(t, root)
	require.Contains(t, after, "status: partial",
		"a note with one resolved and one unresolved anchor is partial")
	require.NotContains(t, after, "status: planned")
	require.NotContains(t, after, "status: implemented")
	require.Equal(t, bodyOf(before), bodyOf(after),
		"arch-gen must not touch the curated narrative body")
}

// Test_4_0_H3_StatusPlanned: a note whose every anchor is absent from the
// code gets status: planned written back.
func Test_4_0_H3_StatusPlanned(t *testing.T) {
	root := archtest.BuildRepo(t, archtest.SpecStatusPlanned())
	before := readNote(t, root)
	require.Contains(t, before, "status: implemented",
		"the fixture must be authored with the wrong status")

	runArchGen(t, root)

	after := readNote(t, root)
	require.Contains(t, after, "status: planned",
		"a note whose anchors are all unresolved is planned")
	require.NotContains(t, after, "status: implemented")
}

// Test_4_0_H3_StatusPlannedEmptyAnchors: a note with an empty anchors list
// is planned regardless of the inventory.
func Test_4_0_H3_StatusPlannedEmptyAnchors(t *testing.T) {
	root := archtest.BuildRepo(t, archtest.SpecStatusEmptyAnchors())

	runArchGen(t, root)

	after := readNote(t, root)
	require.Contains(t, after, "status: planned",
		"a note with an empty anchors list is a planned functionality")
	require.NotContains(t, after, "status: implemented")
}

// Test_4_0_H4_HandWrittenStatusOverwritten: a human typed
// status: implemented by hand, but the note's anchors are only partial.
// arch-gen overwrites the field with status: partial, and `git diff
// --exit-code` over the curated note is non-zero, showing implemented →
// partial — exactly the drift CI is meant to catch.
func Test_4_0_H4_HandWrittenStatusOverwritten(t *testing.T) {
	root := archtest.BuildRepo(t, archtest.SpecStatusHandWrongImplemented())

	// Commit the as-authored fixture (with the hand-typed wrong status) so
	// a later git diff has a baseline.
	gitInitAll(t, root)

	before := readNote(t, root)
	require.Contains(t, before, "status: implemented",
		"the fixture is authored with a hand-typed, wrong status: implemented")

	runArchGen(t, root)

	after := readNote(t, root)
	require.Contains(t, after, "status: partial",
		"arch-gen must overwrite the hand-typed status with the computed partial")
	require.NotContains(t, after, "status: implemented")

	// git diff --exit-code over the curated note must be non-zero: the
	// hand-typed status no longer matches the computed one.
	out, code := gitDiffExitCode(t, root, "docs/arch/network-lifecycle.md")
	require.NotEqual(t, 0, code,
		"git diff --exit-code must be non-zero after arch-gen rewrote the status")
	require.Contains(t, out, "-status: implemented",
		"the diff must show the removed hand-typed status")
	require.Contains(t, out, "+status: partial",
		"the diff must show the computed status")
}

// Test_4_0_H_Idempotent: a second arch-gen run over a note whose status is
// already correct rewrites nothing — git diff stays clean.
func Test_4_0_H_Idempotent(t *testing.T) {
	root := archtest.BuildRepo(t, archtest.SpecStatusImplemented())

	// First run computes and writes status: implemented.
	runArchGen(t, root)
	gitInitAll(t, root)

	// Second run over identical inputs must rewrite nothing.
	runArchGen(t, root)
	out, code := gitDiffExitCode(t, root, "docs/arch/network-lifecycle.md")
	require.Equal(t, 0, code,
		"a second arch-gen run over a correct note must leave git clean; diff=%s", out)
	require.Empty(t, strings.TrimSpace(out))
}

// --- helpers ---------------------------------------------------------------

// frontmatterMinusStatus returns the frontmatter block of a note with
// every "status:" line removed, so two notes can be compared for equality
// of everything but the status scalar.
func frontmatterMinusStatus(noteText string) string {
	front := frontmatterOf(noteText)
	var kept []string
	for _, ln := range strings.Split(front, "\n") {
		if strings.HasPrefix(strings.TrimLeft(ln, " \t"), "status:") {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

// frontmatterOf returns the YAML frontmatter block (between the two ---
// delimiters) of a note.
func frontmatterOf(noteText string) string {
	const delim = "---\n"
	if !strings.HasPrefix(noteText, delim) {
		return ""
	}
	rest := noteText[len(delim):]
	end := strings.Index(rest, "\n"+"---\n")
	if end == -1 {
		return ""
	}
	return rest[:end]
}

// bodyOf returns the Markdown body following the frontmatter of a note.
func bodyOf(noteText string) string {
	const delim = "---\n"
	if !strings.HasPrefix(noteText, delim) {
		return noteText
	}
	rest := noteText[len(delim):]
	closing := "\n" + "---\n"
	end := strings.Index(rest, closing)
	if end == -1 {
		return noteText
	}
	return rest[end+len(closing):]
}

// gitInitAll initialises a git repo at root and commits everything, so a
// later git diff has a baseline to compare against.
func gitInitAll(t *testing.T, root string) {
	t.Helper()
	mustGitCmd(t, root, "init", "-q")
	mustGitCmd(t, root, "config", "user.email", "archgraph@test.local")
	mustGitCmd(t, root, "config", "user.name", "archgraph test")
	mustGitCmd(t, root, "add", "-A")
	mustGitCmd(t, root, "commit", "-q", "-m", "baseline")
}

// mustGitCmd runs a git subcommand at root and fails the test on error.
func mustGitCmd(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
}

// gitDiffExitCode runs `git diff --exit-code -- <path>` at root and
// returns the combined output and the process exit code (0 = clean,
// non-zero = differences).
func gitDiffExitCode(t *testing.T, root, path string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", "diff", "--exit-code", "--", path)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr, "git diff must fail only with an exit code")
	return string(out), exitErr.ExitCode()
}
