package archtest_test

// Scenario 4.0-J1 — acceptance ↔ test traceability.
//
// The sub-phase 4.0 acceptance document enumerates 51 Given-When-Then
// scenarios across ten groups (A1–A5, B1–B7, C1-graph–C3-graph, D1–D6,
// E1–E6, F1–F7, G1–G6, H1–H6, I1–I6) plus the J1 meta-scenario itself.
//
// J1 demands two-way traceability: every scenario ID is reachable from a
// Go test name, and every ID-bearing test name maps back to a scenario.
// Test_4_0_J1_Traceability is the machine guard: it parses every *_test.go
// file under internal/ and cmd/, collects the scenario IDs embedded in
// test-function names, and asserts the set equals the expected catalogue.
//
// A new scenario added to the acceptance doc without a test, or a test
// renamed so its ID drops out, fails this test immediately — keeping the
// doc and the suite from drifting apart silently.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// expectedScenarioIDs is the catalogue of every acceptance scenario in
// sub-phase 4.0 that must be covered by at least one Go test whose name
// contains the ID. Sourced verbatim from
// docs/specs/sub-phase-4.0-archgraph-acceptance.md §1–§10. C1-graph..
// C3-graph are encoded as C1/C2/C3 in test names (the "-graph" suffix is
// not identifier-safe). J1 is the meta-scenario and is satisfied by this
// very test (Test_4_0_J1_Traceability).
var expectedScenarioIDs = []string{
	"A1", "A2", "A3", "A4", "A5",
	"B1", "B2", "B3", "B4", "B5", "B6", "B7",
	"C1", "C2", "C3",
	"D1", "D2", "D3", "D4", "D5", "D6",
	"E1", "E2", "E3", "E4", "E5", "E6",
	"F1", "F2", "F3", "F4", "F5", "F6", "F7",
	"G1", "G2", "G3", "G4", "G5", "G6",
	"H1", "H2", "H3", "H4", "H5", "H6",
	"I1", "I2", "I3", "I4", "I5", "I6",
	"J1",
}

// idInTestName extracts a scenario ID from a test-function name. The
// project encodes IDs two ways: the canonical `Test_4_0_<ID>_...` prefix
// and a `_4_0_<ID>` infix used by a handful of note-package tests
// (TestParse_MalformedYAML_4_0_H6). Both are matched.
var idInTestName = regexp.MustCompile(`_4_0_([A-Z][0-9]+)`)

func Test_4_0_J1_Traceability(t *testing.T) {
	repoRoot := traceabilityRepoRoot(t)

	covered := map[string][]string{} // scenario ID -> test func names
	for _, dir := range []string{"internal", "cmd"} {
		root := filepath.Join(repoRoot, dir)
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
				return nil
			}
			for _, name := range testFuncNames(t, path) {
				if m := idInTestName.FindStringSubmatch(name); m != nil {
					covered[m[1]] = append(covered[m[1]], name)
				}
			}
			return nil
		})
		require.NoError(t, err, "walk %s", root)
	}

	// Forward direction: every catalogued scenario has a test.
	var missing []string
	for _, id := range expectedScenarioIDs {
		if len(covered[id]) == 0 {
			missing = append(missing, id)
		}
	}
	require.Empty(t, missing,
		"acceptance scenarios with no Go test carrying their ID: %v", missing)

	// Reverse direction: every ID found in a test name is a real
	// catalogued scenario — no test claims a scenario the doc lacks.
	expected := map[string]bool{}
	for _, id := range expectedScenarioIDs {
		expected[id] = true
	}
	var orphan []string
	for id := range covered {
		if !expected[id] {
			orphan = append(orphan, id)
		}
	}
	sort.Strings(orphan)
	require.Empty(t, orphan,
		"tests reference scenario IDs absent from the acceptance catalogue: %v", orphan)

	// J1 itself must be satisfied by exactly this test.
	require.Contains(t, covered["J1"], "Test_4_0_J1_Traceability",
		"J1 must be covered by Test_4_0_J1_Traceability")

	t.Logf("J1: all %d acceptance scenarios traced to tests", len(expectedScenarioIDs))
}

// testFuncNames parses one Go file and returns its top-level
// `func Test...` declarations.
func testFuncNames(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err, "parse %s", path)

	var names []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if strings.HasPrefix(fn.Name.Name, "Test") {
			names = append(names, fn.Name.Name)
		}
	}
	return names
}

// traceabilityRepoRoot walks up from this source file to the module root
// (the directory holding go.mod).
func traceabilityRepoRoot(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	dir := filepath.Dir(self)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, parent, dir, "go.mod not found walking up from %s", self)
		dir = parent
	}
}
