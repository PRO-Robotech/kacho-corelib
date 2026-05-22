package check_test

// Integration tests for the C4 doc-coverage check.
//
// Each test maps 1:1 to an acceptance scenario from group H of the
// archgraph sub-phase 4.0 acceptance document (archgraph C4 — every
// repo-owned function, method, package variable and constant must
// carry a Go doc-comment). The scenario ID is encoded in the test name
// for traceability.
//
// Fixtures are synthesised by archtest.BuildRepo into t.TempDir(),
// loaded with golang.org/x/tools/go/packages, then asserted on the
// Result returned by check.CheckC4. No testcontainers, no network:
// every fixture is self-contained and pins go 1.21.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/archtest"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/check"
)

// runC4 builds a C4 fixture, loads its packages and runs CheckC4.
func runC4(t *testing.T, spec archtest.Spec) check.Result {
	t.Helper()
	root := archtest.BuildRepo(t, spec)
	pkgs := loadFixturePkgsAt(t, root)
	return check.CheckC4(root, pkgs)
}

// Test_4_0_H1_C4_PassAllDocumented: every function, method, package
// variable and constant of every repo-owned package carries a
// doc-comment, so C4 passes with no findings and an N/N summary.
func Test_4_0_H1_C4_PassAllDocumented(t *testing.T) {
	res := runC4(t, archtest.SpecC4Pass())

	require.True(t, res.Passed,
		"C4 must pass when every symbol is documented; findings=%v",
		findingTexts(res))
	require.Empty(t, res.Findings, "a passing C4 has no findings")
	require.Contains(t, res.Summary, "C4 doc-coverage: PASS",
		"a passing C4 must carry the PASS summary; got %q", res.Summary)
	require.Contains(t, res.Summary, "functions and variables documented",
		"the PASS summary must carry the N/N documented count; got %q",
		res.Summary)
}

// Test_4_0_H2_C4_FailUndocumentedFunc: a repo-owned function with no
// doc-comment fails C4 with a finding naming the symbol and its
// file:line position, in the exact undocumented-function text.
func Test_4_0_H2_C4_FailUndocumentedFunc(t *testing.T) {
	res := runC4(t, archtest.SpecC4UndocumentedFunc())

	require.False(t, res.Passed,
		"an undocumented function must fail C4")
	require.Equal(t, "C4 doc-coverage: FAIL", res.Summary)
	require.True(t, containsLine(findingTexts(res),
		"undocumented function: internal/domain.ComputeChecksum "+
			"(internal/domain/domain.go:9) — add a doc-comment"),
		"C4 must report ComputeChecksum with its file:line; got %v",
		findingTexts(res))
}

// Test_4_0_H3_C4_FailUndocumentedVar: a repo-owned package-level
// variable with no doc-comment fails C4 with an undocumented-variable
// finding naming the symbol and its file:line position.
func Test_4_0_H3_C4_FailUndocumentedVar(t *testing.T) {
	res := runC4(t, archtest.SpecC4UndocumentedVar())

	require.False(t, res.Passed,
		"an undocumented package variable must fail C4")
	require.Equal(t, "C4 doc-coverage: FAIL", res.Summary)
	require.True(t, containsLine(findingTexts(res),
		"undocumented variable: internal/domain.apiEndpoint "+
			"(internal/domain/domain.go:8) — add a doc-comment"),
		"C4 must report apiEndpoint with its file:line; got %v",
		findingTexts(res))
}

// Test_4_0_H4_C4_GeneratedFileExcluded: a machine-generated file
// carrying the "// Code generated ... DO NOT EDIT." marker is excluded
// from C4 entirely — its undocumented declarations raise no finding —
// so the fixture passes.
func Test_4_0_H4_C4_GeneratedFileExcluded(t *testing.T) {
	res := runC4(t, archtest.SpecC4GeneratedExcluded())

	require.True(t, res.Passed,
		"a generated file must be excluded from C4; findings=%v",
		findingTexts(res))
	require.False(t, containsLine(findingTexts(res), "GeneratedMarshal"),
		"the generated function GeneratedMarshal must not be a C4 finding")
	require.False(t, containsLine(findingTexts(res), "generatedTable"),
		"the generated variable generatedTable must not be a C4 finding")
}
