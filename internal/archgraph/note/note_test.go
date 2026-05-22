package note_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
)

// copyFixture copies a testdata fixture into a fresh temp file so that
// write-back tests never mutate the checked-in fixtures.
func copyFixture(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("testdata", "notes", name)
	data, err := os.ReadFile(src)
	require.NoError(t, err, "read fixture %s", name)
	dst := filepath.Join(t.TempDir(), filepath.Base(name))
	require.NoError(t, os.WriteFile(dst, data, 0o644))
	return dst
}

// --- Parse: happy path -----------------------------------------------------

func TestParse_ValidWithComments(t *testing.T) {
	n, err := note.Parse(filepath.Join("testdata", "notes", "valid_with_comments.md"))
	require.NoError(t, err)

	require.Equal(t, "functionality", n.Level)
	require.Equal(t, "kacho-vpc", n.Repo)
	require.Equal(t, "implemented", n.Status)
	require.Equal(t, "a1b2c3d", n.SourceSHA)

	require.Len(t, n.Anchors, 3)

	require.True(t, n.Anchors[0].IsRPC())
	require.False(t, n.Anchors[0].IsWorker())
	require.Equal(t, "kacho.cloud.vpc.v1.NetworkService/Create", n.Anchors[0].RPC)

	require.True(t, n.Anchors[1].IsRPC())
	require.Equal(t, "kacho.cloud.vpc.v1.NetworkService/Delete", n.Anchors[1].RPC)

	require.True(t, n.Anchors[2].IsWorker())
	require.False(t, n.Anchors[2].IsRPC())
	require.Equal(t, "NetworkOutboxWorker", n.Anchors[2].Worker)

	require.Contains(t, n.Body(), "# Network lifecycle")
	require.Contains(t, n.Body(), "survive a write-back untouched")
}

func TestParse_ValidMinimal(t *testing.T) {
	n, err := note.Parse(filepath.Join("testdata", "notes", "valid_minimal.md"))
	require.NoError(t, err)

	require.Equal(t, "functionality", n.Level)
	require.Equal(t, "kacho-compute", n.Repo)
	require.Equal(t, "planned", n.Status)
	require.Equal(t, "", n.SourceSHA)
	require.Len(t, n.Anchors, 1)
	require.True(t, n.Anchors[0].IsRPC())
}

func TestParse_PathRecorded(t *testing.T) {
	p := filepath.Join("testdata", "notes", "valid_minimal.md")
	n, err := note.Parse(p)
	require.NoError(t, err)
	require.Equal(t, p, n.Path())
}

// --- Parse: 4.0-H6 — malformed / missing frontmatter -----------------------

func TestParse_MalformedYAML_4_0_H6(t *testing.T) {
	p := filepath.Join("testdata", "badnotes", "malformed_yaml.md")
	n, err := note.Parse(p)
	require.Error(t, err)
	require.Nil(t, n)
	require.ErrorContains(t, err, "invalid L2 note")
	require.ErrorContains(t, err, "malformed or missing YAML frontmatter")
	require.ErrorContains(t, err, p)
}

func TestParse_NoFrontmatter_4_0_H6(t *testing.T) {
	p := filepath.Join("testdata", "badnotes", "no_frontmatter.md")
	n, err := note.Parse(p)
	require.Error(t, err)
	require.Nil(t, n)
	require.ErrorContains(t, err, "invalid L2 note")
	require.ErrorContains(t, err, "malformed or missing YAML frontmatter")
	require.ErrorContains(t, err, p)
}

func TestParse_MissingFile(t *testing.T) {
	n, err := note.Parse(filepath.Join("testdata", "notes", "does_not_exist.md"))
	require.Error(t, err)
	require.Nil(t, n)
}

// --- Write-back: 4.0-H5 — formatting / order / comments preserved ----------

func TestWriteBack_PreservesKeyOrderAndComments_4_0_H5(t *testing.T) {
	path := copyFixture(t, "valid_with_comments.md")
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	n, err := note.Parse(path)
	require.NoError(t, err)

	n.SetStatus("partial")
	require.NoError(t, n.Write())

	out, err := os.ReadFile(path)
	require.NoError(t, err)
	got := string(out)

	// status field was updated.
	require.Contains(t, got, "status: partial")
	require.NotContains(t, got, "status: implemented")

	// All other frontmatter keys retained, in original order.
	keyOrder := []string{"level:", "repo:", "anchors:", "status:", "source_sha:"}
	idx := -1
	for _, k := range keyOrder {
		at := strings.Index(got, k)
		require.NotEqual(t, -1, at, "key %q must be present after write-back", k)
		require.Greater(t, at, idx, "key %q must keep its original position", k)
		idx = at
	}

	// YAML comments survive.
	require.Contains(t, got, "# L2: a unit of functionality")
	require.Contains(t, got, "# anchors are the entry-points of this functionality")
	require.Contains(t, got, "# delete path")
	require.Contains(t, got, "# computed by archgraph")

	// Other values untouched.
	require.Contains(t, got, "repo: kacho-vpc")
	require.Contains(t, got, "source_sha: a1b2c3d")
	require.Contains(t, got, "kacho.cloud.vpc.v1.NetworkService/Create")
	require.Contains(t, got, "worker: NetworkOutboxWorker")

	// Body untouched, exactly once, with proper --- delimiters.
	require.Contains(t, got, "# Network lifecycle")
	require.Contains(t, got, "survive a write-back untouched")
	require.Equal(t, 2, strings.Count(got, "---\n"),
		"frontmatter must remain delimited by exactly two --- lines")

	// Re-parse the written file: it stays a valid L2 note.
	reparsed, err := note.Parse(path)
	require.NoError(t, err)
	require.Equal(t, "partial", reparsed.Status)
	require.Len(t, reparsed.Anchors, 3)
	require.Equal(t, "a1b2c3d", reparsed.SourceSHA)

	// The body byte-region must be unchanged.
	require.Equal(t, bodyOf(t, before), bodyOf(t, out))
}

func TestWriteBack_NoChange_Idempotent(t *testing.T) {
	path := copyFixture(t, "valid_with_comments.md")
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	n, err := note.Parse(path)
	require.NoError(t, err)
	n.SetStatus(n.Status) // same value
	require.NoError(t, n.Write())

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after),
		"writing the same status back must be a byte-identical no-op")
}

// --- Atomic write ----------------------------------------------------------

func TestWrite_AtomicNoTempLeftover(t *testing.T) {
	path := copyFixture(t, "valid_minimal.md")
	n, err := note.Parse(path)
	require.NoError(t, err)
	n.SetStatus("implemented")
	require.NoError(t, n.Write())

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	for _, e := range entries {
		require.Equal(t, filepath.Base(path), e.Name(),
			"atomic write must not leave temp files behind")
	}
}

func TestWrite_PreservesFileMode(t *testing.T) {
	// A note checked in with mode 0644 must keep 0644 after a write-back:
	// atomicWrite creates its temp file 0600, so without an explicit
	// Chmod the rename would silently narrow the destination's mode.
	path := copyFixture(t, "valid_minimal.md")
	require.NoError(t, os.Chmod(path, 0o644))

	n, err := note.Parse(path)
	require.NoError(t, err)
	n.SetStatus("implemented")
	require.NoError(t, n.Write())

	fi, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), fi.Mode().Perm(),
		"write-back must preserve the original file mode")
}

func TestWriteTo_NewFileDefaultMode(t *testing.T) {
	// WriteTo to a path that does not yet exist must produce a 0644
	// file, not the 0600 that os.CreateTemp would leave.
	n, err := note.Parse(filepath.Join("testdata", "notes", "valid_minimal.md"))
	require.NoError(t, err)
	n.SetStatus("partial")

	dst := filepath.Join(t.TempDir(), "new.md")
	require.NoError(t, n.WriteTo(dst))

	fi, err := os.Stat(dst)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), fi.Mode().Perm(),
		"a freshly written note must default to mode 0644")
}

func TestWriteTo_DoesNotTouchOriginal(t *testing.T) {
	src := filepath.Join("testdata", "notes", "valid_minimal.md")
	orig, err := os.ReadFile(src)
	require.NoError(t, err)

	n, err := note.Parse(src)
	require.NoError(t, err)
	n.SetStatus("partial")

	dst := filepath.Join(t.TempDir(), "out.md")
	require.NoError(t, n.WriteTo(dst))

	// Original fixture untouched.
	now, err := os.ReadFile(src)
	require.NoError(t, err)
	require.Equal(t, string(orig), string(now))

	// Destination has the new status.
	out, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Contains(t, string(out), "status: partial")
}

// --- Load ------------------------------------------------------------------

func TestLoad_PicksOnlyL2NotesAndSkipsGenerated(t *testing.T) {
	notes, err := note.Load("testdata/notes")
	require.NoError(t, err)

	repos := map[string]bool{}
	for _, n := range notes {
		require.Equal(t, "functionality", n.Level)
		repos[filepath.Base(n.Path())] = true
	}

	require.True(t, repos["valid_with_comments.md"], "valid L2 note must be loaded")
	require.True(t, repos["valid_minimal.md"], "valid L2 note must be loaded")
	require.False(t, repos["not_l2.md"], "non-L2 note must be skipped")
	require.False(t, repos["skip_me.md"], "generated/ note must be skipped")
	require.False(t, repos["malformed_yaml.md"], "malformed note must not appear")
	require.False(t, repos["no_frontmatter.md"], "frontmatter-less note must not appear")
}

func TestLoad_PropagatesMalformedError(t *testing.T) {
	// A directory containing only a malformed note must surface the
	// 4.0-H6 error rather than silently dropping the file.
	dir := t.TempDir()
	bad, err := os.ReadFile(filepath.Join("testdata", "badnotes", "malformed_yaml.md"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.md"), bad, 0o644))

	_, err = note.Load(dir)
	require.Error(t, err)
	require.ErrorContains(t, err, "malformed or missing YAML frontmatter")
}

// bodyOf returns the Markdown body region (after the closing ---) of a
// raw note file, used to assert the body survives write-back untouched.
func bodyOf(t *testing.T, raw []byte) string {
	t.Helper()
	s := string(raw)
	require.True(t, strings.HasPrefix(s, "---\n"), "fixture must start with frontmatter")
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	require.NotEqual(t, -1, end, "fixture must have a closing --- delimiter")
	return rest[end+len("\n---\n"):]
}
