// Package note parses and writes the YAML frontmatter of archgraph's
// L2 "functionality" notes.
//
// An L2 note is a Markdown file (conventionally under docs/arch/*.md of a
// service repository) whose leading block, delimited by two "---" lines,
// is a YAML document describing one unit of functionality:
//
//	---
//	level: functionality
//	repo: kacho-vpc
//	anchors:
//	  - rpc: kacho.cloud.vpc.v1.NetworkService/Create
//	  - worker: NetworkOutboxWorker
//	status: implemented        # implemented | partial | planned
//	source_sha: a1b2c3d
//	---
//	<markdown body>
//
// This package is the foundation for later archgraph tasks: C1/C3 checks
// read Anchors and SourceSHA, and the status write-back pass updates
// Status in place. Write-back is round-trip faithful: it edits only the
// "status" scalar inside the original raw bytes, so frontmatter key
// order, YAML comments, every other value and the Markdown body are all
// preserved byte-for-byte. Writes are atomic (temp file + rename), so a
// failure never leaves a note half-written.
//
// It depends only on the standard library and gopkg.in/yaml.v3; it
// imports no transport, persistence or RPC packages.
package note

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrMalformedNote is the sentinel returned when a file cannot be
// interpreted as an L2 note because its YAML frontmatter is absent or
// syntactically invalid. Errors from Parse and Load wrap it, so callers
// may match with errors.Is.
var ErrMalformedNote = errors.New("invalid L2 note")

// LevelFunctionality is the value of the "level" key that marks a note
// as an L2 functionality note. Load considers only notes at this level.
const LevelFunctionality = "functionality"

// generatedDir is the subdirectory of docs/arch/ holding archgraph's own
// generated output; Load skips it so generated notes are never treated
// as authored input.
const generatedDir = "generated"

// frontmatterDelim is the line that opens and closes the YAML
// frontmatter block of a note.
const frontmatterDelim = "---"

// Anchor is one entry-point of a functionality. It is a oneof: exactly
// one of RPC or Worker is set. RPC holds a fully-qualified gRPC method
// name ("<package>.<Service>/<Method>"); Worker holds a background
// worker's Go type name.
type Anchor struct {
	// RPC is the fully-qualified gRPC method name, or "" for a worker
	// anchor.
	RPC string
	// Worker is the worker type name, or "" for an RPC anchor.
	Worker string
}

// IsRPC reports whether the anchor refers to a gRPC method.
func (a Anchor) IsRPC() bool { return a.RPC != "" }

// IsWorker reports whether the anchor refers to a background worker.
func (a Anchor) IsWorker() bool { return a.Worker != "" }

// Note is a parsed L2 functionality note: its typed frontmatter, its
// Markdown body and the path it was read from. The exact bytes of the
// frontmatter block are retained so write-back can edit a single scalar
// without disturbing layout or comments.
type Note struct {
	// Level is the "level" key; an L2 note has level "functionality".
	Level string
	// Repo is the owning service repository, e.g. "kacho-vpc".
	Repo string
	// Anchors are the functionality's entry-points.
	Anchors []Anchor
	// Status is implemented | partial | planned, computed by archgraph.
	Status string
	// SourceSHA is the code SHA the note was last verified against.
	SourceSHA string

	// path is the file the note was parsed from.
	path string
	// front holds the raw frontmatter bytes between the delimiters
	// (the closing newline included), preserved verbatim apart from an
	// in-place status edit.
	front []byte
	// body is the Markdown content following the closing delimiter,
	// preserved verbatim across write-back.
	body string
}

// Path returns the file path the note was parsed from.
func (n *Note) Path() string { return n.path }

// Body returns the Markdown body following the frontmatter.
func (n *Note) Body() string { return n.body }

// SetStatus updates the note's "status" field. The change is applied
// surgically to the retained raw frontmatter — only the status scalar's
// text is rewritten — so a subsequent Write keeps every other key,
// value, comment and the body byte-identical. SetStatus is idempotent:
// setting the current value rewrites the bytes to the same content.
func (n *Note) SetStatus(status string) {
	n.front = replaceStatusScalar(n.front, status)
	n.Status = status
}

// Parse reads the file at path and decodes its L2 frontmatter. If the
// file has no "---"-delimited frontmatter block, or that block is not
// valid YAML, it returns an error wrapping ErrMalformedNote and a nil
// Note — no partial result is produced.
func Parse(path string) (*Note, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read note %s: %w", path, err)
	}

	front, body, ok := splitFrontmatter(raw)
	if !ok {
		return nil, malformed(path)
	}

	var fm frontmatter
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return nil, malformed(path)
	}
	// A frontmatter block that is valid YAML but not a mapping (e.g.
	// only a comment, or a bare scalar) carries no note fields.
	if !isYAMLMapping(front) {
		return nil, malformed(path)
	}

	n := &Note{
		Level:     fm.Level,
		Repo:      fm.Repo,
		Status:    fm.Status,
		SourceSHA: fm.SourceSHA,
		Anchors:   make([]Anchor, 0, len(fm.Anchors)),
		path:      path,
		front:     front,
		body:      body,
	}
	for _, a := range fm.Anchors {
		n.Anchors = append(n.Anchors, Anchor(a))
	}
	return n, nil
}

// Load collects every L2 functionality note in dir, recursively. Files
// whose "level" is not "functionality" are skipped, as is everything
// under a "generated" subdirectory (archgraph's own output). A note with
// malformed frontmatter aborts the load with an error wrapping
// ErrMalformedNote. Results are sorted by path for determinism.
func Load(dir string) ([]*Note, error) {
	var paths []string
	walkErr := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == generatedDir && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(p), ".md") {
			paths = append(paths, p)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("scan arch notes in %s: %w", dir, walkErr)
	}
	sort.Strings(paths)

	notes := make([]*Note, 0, len(paths))
	for _, p := range paths {
		n, err := classify(p)
		if err != nil {
			return nil, err
		}
		if n != nil {
			notes = append(notes, n)
		}
	}
	return notes, nil
}

// classify parses p and returns the note if it is an L2 functionality
// note. A non-L2 note (e.g. a service-level note) yields (nil, nil): it
// is skipped, not an error. A note with malformed frontmatter yields the
// 4.0-H6 error.
func classify(p string) (*Note, error) {
	n, err := Parse(p)
	if err != nil {
		return nil, err
	}
	if n.Level != LevelFunctionality {
		return nil, nil
	}
	return n, nil
}

// Write persists the note back to the path it was parsed from.
func (n *Note) Write() error { return n.WriteTo(n.path) }

// WriteTo serialises the note to path: the retained frontmatter bytes
// (with at most the status scalar edited), wrapped in the "---"
// delimiters, then the original body verbatim. The write is atomic — the
// content is written to a temp file in the same directory and renamed
// over the destination — so a failure never leaves a half-written note.
func (n *Note) WriteTo(path string) error {
	var out []byte
	out = append(out, frontmatterDelim+"\n"...)
	out = append(out, n.front...)
	out = append(out, frontmatterDelim+"\n"...)
	out = append(out, n.body...)

	if err := atomicWrite(path, out); err != nil {
		return fmt.Errorf("write note %s: %w", path, err)
	}
	return nil
}

// atomicWrite writes data to path atomically: it creates a temp file in
// the destination directory, writes and fsyncs it, then renames it over
// path. On any failure before the rename the temp file is removed, so no
// partial output is observable at path.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".archgraph-note-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// frontmatter is the typed decode target for an L2 note's YAML block.
type frontmatter struct {
	Level     string       `yaml:"level"`
	Repo      string       `yaml:"repo"`
	Anchors   []anchorYAML `yaml:"anchors"`
	Status    string       `yaml:"status"`
	SourceSHA string       `yaml:"source_sha"`
}

// anchorYAML decodes a single anchor list entry; exactly one of its keys
// is expected to be present.
type anchorYAML struct {
	RPC    string `yaml:"rpc"`
	Worker string `yaml:"worker"`
}

// isYAMLMapping reports whether front is a YAML document whose top-level
// node is a mapping. A frontmatter block that decodes cleanly but is not
// a mapping (a bare scalar, a sequence, or only comments) is not a valid
// note frontmatter.
func isYAMLMapping(front []byte) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal(front, &doc); err != nil {
		return false
	}
	return len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode
}

// splitFrontmatter separates a note file into its YAML frontmatter and
// its Markdown body. It reports ok=false when the file does not begin
// with a "---" line or has no closing "---" line — both of which mean
// "malformed or missing frontmatter" for 4.0-H6. The returned front
// includes the newline before the closing delimiter.
func splitFrontmatter(raw []byte) (front []byte, body string, ok bool) {
	s := string(raw)
	open := frontmatterDelim + "\n"
	if !strings.HasPrefix(s, open) {
		return nil, "", false
	}
	rest := s[len(open):]
	closing := "\n" + frontmatterDelim + "\n"
	end := strings.Index(rest, closing)
	if end == -1 {
		return nil, "", false
	}
	front = []byte(rest[:end+1]) // include the newline before the closing delim
	body = rest[end+len(closing):]
	return front, body, true
}

// replaceStatusScalar rewrites the value of the top-level "status:" key
// in raw frontmatter bytes. It edits only the value token: the key, its
// leading separator, any inline "#" comment and the exact whitespace
// around them are kept byte-for-byte, so an unchanged value is a no-op
// and a changed one disturbs nothing else. If no "status:" key is found
// a new "status: <value>" line is appended.
func replaceStatusScalar(front []byte, value string) []byte {
	lines := strings.SplitAfter(string(front), "\n")
	for i, ln := range lines {
		// Match a top-level (column-0) "status:" key. Indented
		// occurrences belong to nested mappings and are left alone.
		trimmed := strings.TrimRight(ln, "\n")
		if !strings.HasPrefix(trimmed, "status:") {
			continue
		}
		nl := ""
		if strings.HasSuffix(ln, "\n") {
			nl = "\n"
		}
		rest := trimmed[len("status:"):]

		// Leading whitespace between the colon and the value token.
		lead := rest[:len(rest)-len(strings.TrimLeft(rest, " \t"))]
		afterLead := rest[len(lead):]

		// An inline comment, if any, with the original whitespace
		// before its '#' preserved.
		comment := ""
		valuePart := afterLead
		if hash := indexUnquotedHash(afterLead); hash != -1 {
			comment = afterLead[hash:]
			valuePart = afterLead[:hash]
		}
		// Whitespace between the value token and the comment (or EOL).
		trail := valuePart[len(strings.TrimRight(valuePart, " \t")):]

		if lead == "" {
			lead = " "
		}
		lines[i] = "status:" + lead + yamlScalar(value) + trail + comment + nl
		return []byte(strings.Join(lines, ""))
	}
	// No status key: append one, keeping a trailing newline.
	appended := string(front)
	if !strings.HasSuffix(appended, "\n") {
		appended += "\n"
	}
	appended += "status: " + yamlScalar(value) + "\n"
	return []byte(appended)
}

// indexUnquotedHash returns the index of the first '#' in s that starts
// an inline YAML comment — i.e. a '#' preceded by whitespace and not
// inside a quoted string. It returns -1 when there is no inline comment.
func indexUnquotedHash(s string) int {
	var quote rune
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '#' && i > 0 && (s[i-1] == ' ' || s[i-1] == '\t'):
			return i
		}
	}
	return -1
}

// yamlScalar renders a string as a YAML scalar value for the status
// field. The empty string is emitted quoted ("") so it round-trips as a
// string rather than YAML null; other status values (implemented /
// partial / planned) are plain identifiers needing no quoting.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	return s
}

// malformed builds the 4.0-H6 error for an unparseable note.
func malformed(path string) error {
	return fmt.Errorf("%w: %s — malformed or missing YAML frontmatter",
		ErrMalformedNote, path)
}
