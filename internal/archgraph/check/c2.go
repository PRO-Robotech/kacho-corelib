package check

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
)

// keepMarker is the exact comment prefix that suppresses a C2 dead-code
// finding. A comment line whose text, after the "// " lead, equals
// keepMarker or begins with keepMarker followed by whitespace is an
// archgraph:keep annotation. Anything after the marker is the reason.
const keepMarker = "archgraph:keep"

// CheckC2 runs the dead-code check: every exported function and method
// of the repository must be reachable from at least one entry-point, or
// be explicitly retained with an `// archgraph:keep <reason>`
// annotation.
//
// # Scope — functions and methods only
//
// C2 enumerates the repository's exported *functions and methods*. It
// does not independently track exported *types*, *vars* or *consts*: a
// type is exercised through the functions that construct and consume
// it, so a type referenced only by dead functions is dead by
// transitivity of its functions, and a type referenced by live code is
// kept alive by that code. Modelling type reachability precisely would
// require a value-flow analysis disproportionate to the check's intent
// — catching whole unused functions an AI left behind. This scoping is
// deliberate and fixed; the F1 fixture's exported types are all used by
// reachable functions, so the scoping changes none of its verdicts.
//
// # Library repositories
//
// A library repository (inv.IsLibraryRepo — no main package, hence no
// entry-point against which to evaluate reachability) makes C2 a no-op:
// the returned Result is a passing SKIP. C1 and C3 still run.
//
// # The archgraph:keep annotation
//
// A comment `// archgraph:keep <reason>` placed immediately above a
// declaration retains the symbol: it is not reported as dead, and — so
// the retention is honest — it becomes an additional reachability root,
// reviving everything transitively reachable from it (scenario 4.0-F5).
// A retained symbol is reported as an advisory Hint ("kept: <symbol>
// (reason: <reason>)"). A bare `// archgraph:keep` with no reason is
// rejected: it produces an invalid-annotation Finding, because an
// unexplained suppression is itself an architecture smell.
//
// # RTA imprecision
//
// Reachability is RTA-based and cannot model reflection: a method
// invoked only through reflect.Value.Call is reported dead (scenario
// 4.0-F6). The cure is an `// archgraph:keep reflection-invoked`
// annotation on the symbol, not a widening of the analysis.
//
// CheckC2 derives its verdict from pkgs (the repository's own loaded
// packages), inv and repoRoot (used only to render file positions
// repository-relative). repoRoot must be the repository's module root.
// Findings are sorted by Symbol and Hints by text, so the Result is
// fully deterministic. CheckC2 returns an error only on an internal
// reachability-analysis failure.
func CheckC2(
	repoRoot string,
	pkgs []*packages.Package,
	inv *entrypoints.Inventory,
) (Result, error) {
	// A library repo has no entry-point to evaluate reachability against
	// — C2 is skipped, not failed.
	if inv != nil && inv.IsLibraryRepo {
		return c2SkipResult(), nil
	}

	ar, err := BuildAuditReach(repoRoot, pkgs, inv)
	if err != nil {
		return Result{}, err
	}
	return CheckC2WithReach(ar), nil
}

// c2SkipResult is the passing SKIP verdict C2 returns for a library
// repository — no main package, hence no entry-point to evaluate
// reachability against. C1 and C3 still run.
func c2SkipResult() Result {
	return Result{
		Passed:  true,
		Summary: "C2 dead-code: SKIP (library repo: no main package)",
	}
}

// CheckC2WithReach is CheckC2's verdict step, computed from an
// AuditReach the caller has already built. arch-audit (the audit
// package) builds one AuditReach — a single SSA lowering + RTA + symbol
// resolution — and feeds it to both C2 (here) and C3, so the program is
// lowered exactly once per audit run.
//
// A library-repo AuditReach (IsLibraryRepo) yields the passing SKIP
// verdict, identical to CheckC2's own library-repo path.
func CheckC2WithReach(ar *AuditReach) Result {
	if ar == nil || ar.isLibrary {
		return c2SkipResult()
	}

	syms := ar.c2Symbols
	invalidFindings := ar.c2Invalid
	names := ar.symbolNames

	reachable := make(map[string]struct{})
	for _, f := range ar.Graph.Union().Funcs {
		reachable[f] = struct{}{}
	}

	var findings []Finding
	findings = append(findings, invalidFindings...)
	var hints []string

	for _, s := range syms {
		// A symbol explicitly retained with a valid keep annotation is
		// never dead; surface it as a kept hint instead.
		if s.keptReason != "" {
			hints = append(hints, fmt.Sprintf(
				"kept: %s (reason: %s)", s.name, s.keptReason))
			continue
		}
		// A symbol with a bare keep already has an invalid-annotation
		// finding; it is not also reported as dead code.
		if s.bareKeep {
			continue
		}
		// A symbol that did not lower to an SSA function (an unusual
		// build edge case — e.g. an un-instantiated generic) is treated
		// conservatively as reachable: C2 must not raise a false dead-code
		// finding it cannot substantiate.
		ssaName, ok := names[s.fn]
		if !ok {
			continue
		}
		if _, live := reachable[ssaName]; live {
			continue
		}
		findings = append(findings, Finding{
			Check:  C2,
			Symbol: s.name,
			Reason: fmt.Sprintf(
				"dead code: exported symbol %s (%s) unreachable from any "+
					"entry-point", s.name, s.pos),
		})
	}

	sortFindings(findings)
	sort.Strings(hints)

	passed := len(findings) == 0
	return Result{
		Passed:   passed,
		Findings: findings,
		Hints:    hints,
		Summary:  c2Summary(passed, len(findings)),
	}
}

// exportedSymbol is one exported function or method of the repository: a
// dead-code candidate. fn is the symbol's types object (the key into the
// SSA-name resolver); name is its human-readable canonical name; pos is
// its repository-relative "file:line" position. keptReason is non-empty
// iff the symbol carries a valid `// archgraph:keep <reason>`. bareKeep
// is true iff the symbol carries a reason-less `// archgraph:keep`,
// already reported as an invalid-annotation Finding — such a symbol is
// not additionally reported as dead code (the author's clear intent was
// to retain it; the fix is to supply a reason, not to delete it).
type exportedSymbol struct {
	fn         *types.Func
	name       string
	pos        string
	keptReason string
	bareKeep   bool
}

// collectExportedSymbols walks the repository's own packages and returns
// every exported function and method as an exportedSymbol, the
// *types.Func of each validly kept symbol (the extra reachability roots
// C2 feeds to reach), and an invalid-annotation Finding for every bare
// `// archgraph:keep` with no reason.
//
// The main package is excluded: it is the composition root, not a public
// API surface, and its entry-point wiring is the very thing every other
// symbol's reachability is measured against.
func collectExportedSymbols(
	repoRoot string,
	pkgs []*packages.Package,
) (syms []exportedSymbol, keptFuncs []*types.Func, invalid []Finding) {
	for _, pkg := range pkgs {
		if pkg.Name == "main" || pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Name == nil || !token.IsExported(fd.Name.Name) {
					continue
				}
				obj, _ := pkg.TypesInfo.Defs[fd.Name].(*types.Func)
				if obj == nil {
					continue
				}

				sym := exportedSymbol{
					fn:   obj,
					name: symbolName(fd),
					pos:  relPos(repoRoot, pkg.Fset, fd.Pos()),
				}

				reason, hasMarker, bare := parseKeep(fd.Doc)
				switch {
				case bare:
					// A keep with no reason is rejected with its own
					// invalid-annotation finding; the symbol is not also
					// reported as dead code — the author plainly meant to
					// retain it, the fix is to add a reason.
					sym.bareKeep = true
					invalid = append(invalid, Finding{
						Check:  C2,
						Symbol: sym.name,
						Reason: fmt.Sprintf(
							"invalid annotation: // %s at %s requires a "+
								"non-empty reason",
							keepMarker, keepPos(repoRoot, pkg.Fset, fd.Doc)),
					})
				case hasMarker:
					sym.keptReason = reason
					keptFuncs = append(keptFuncs, obj)
				}
				syms = append(syms, sym)
			}
		}
	}
	return syms, keptFuncs, invalid
}

// symbolName renders a func declaration's human-readable name: the bare
// function name for a free function, or "(<receiver>).<method>" for a
// method, where <receiver> is the receiver type written as in source
// (with a leading "*" for a pointer receiver).
func symbolName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	return "(" + recvTypeString(fd.Recv.List[0].Type) + ")." + fd.Name.Name
}

// recvTypeString renders a method receiver's type expression: "*T" for a
// pointer receiver, "T" for a value receiver, dropping any type
// parameters of a generic receiver.
func recvTypeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "*" + recvTypeString(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver T[P]
		return recvTypeString(t.X)
	case *ast.IndexListExpr: // generic receiver T[P, Q]
		return recvTypeString(t.X)
	default:
		return "?"
	}
}

// parseKeep inspects a declaration's doc comment group for an
// `archgraph:keep` annotation on its own line. It reports the reason
// (the trimmed remainder of the line), whether a marker was found at
// all, and whether the marker was bare (present but with no reason).
func parseKeep(doc *ast.CommentGroup) (reason string, hasMarker, bare bool) {
	if doc == nil {
		return "", false, false
	}
	for _, c := range doc.List {
		text := commentText(c.Text)
		if text == keepMarker {
			return "", true, true
		}
		if rest, ok := strings.CutPrefix(text, keepMarker); ok {
			r := strings.TrimSpace(rest)
			if r == "" {
				return "", true, true
			}
			return r, true, false
		}
	}
	return "", false, false
}

// commentText strips the comment lead ("//" or "/*"…"*/") and surrounding
// whitespace from a raw comment, yielding the bare comment text.
func commentText(raw string) string {
	s := raw
	switch {
	case strings.HasPrefix(s, "//"):
		s = s[2:]
	case strings.HasPrefix(s, "/*"):
		s = strings.TrimSuffix(strings.TrimPrefix(s, "/*"), "*/")
	}
	return strings.TrimSpace(s)
}

// keepPos returns the repository-relative "file:line" of the
// archgraph:keep comment within doc — the line C2's invalid-annotation
// finding points at.
func keepPos(repoRoot string, fset *token.FileSet, doc *ast.CommentGroup) string {
	for _, c := range doc.List {
		if strings.Contains(commentText(c.Text), keepMarker) {
			return relPos(repoRoot, fset, c.Pos())
		}
	}
	// Unreachable: keepPos is only called once a marker was found.
	return relPos(repoRoot, fset, doc.Pos())
}

// relPos renders pos as a "file:line" string with the file path made
// relative to repoRoot, so a finding reads "internal/repo/legacy.go:14"
// regardless of where the repository is checked out. A path that cannot
// be made relative (a different volume) falls back to its absolute form.
func relPos(repoRoot string, fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	file := p.Filename
	if rel, err := filepath.Rel(repoRoot, file); err == nil &&
		!strings.HasPrefix(rel, "..") {
		file = filepath.ToSlash(rel)
	}
	return fmt.Sprintf("%s:%d", file, p.Line)
}

// c2Summary builds the one-line C2 verdict. On success it reports the
// zero-unreachable count; on failure it names how many dead-code /
// invalid-annotation findings were raised.
func c2Summary(passed bool, n int) string {
	if passed {
		return "C2 dead-code: PASS (0 unreachable exported symbols)"
	}
	noun := "findings"
	if n == 1 {
		noun = "finding"
	}
	return fmt.Sprintf("C2 dead-code: FAIL (%d %s)", n, noun)
}
