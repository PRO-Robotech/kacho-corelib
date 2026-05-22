package check

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

// CheckC4 runs the doc-coverage check: every function, method,
// package-level variable and package-level constant defined in the
// repository's own packages must carry a Go doc-comment.
//
// # Why it blocks CI
//
// archgraph's mandate is that the documentation auto-describes what
// every function does and what every variable is for, and links those
// descriptions back to the L2 functionality they serve. The "what"
// lives in the Go doc-comment — the code documents itself — and the
// L3/L4 generators extract those comments verbatim. A symbol with no
// doc-comment is therefore an undocumented hole the generators cannot
// fill: C4 fails the merge so the gap is closed in the code (an AI
// later writes the missing comment) before the artifacts are
// regenerated.
//
// # Scope — repo-owned declarations only
//
// C4 considers only declarations in the repository's own loaded
// packages — never the standard library or module-cache dependencies,
// which a repository neither owns nor may edit. Unlike C2 (dead-code),
// C4 does NOT skip the main package: a service's composition root is
// hand-written repository code and its exported helpers are documented
// like any other. Unlike C2, a library repository is NOT exempt either
// — a library's API still needs doc-comments; only generated files are
// always excluded (see below).
//
// The two special program functions `main` and `init` are exempt:
// `main` is covered by the package doc-comment convention and `init`
// cannot even be named, so neither conventionally carries its own
// doc-comment. Every other free function and method — exported or
// unexported — is in scope, as is every package-level `var` and
// `const`.
//
// # Generated files are always excluded
//
// A file is generated — and excluded from C4 — when its name ends in
// ".pb.go" or "_grpc.pb.go" (the protoc-gen-go / protoc-gen-go-grpc
// outputs) or when it carries the standard `// Code generated ... DO
// NOT EDIT.` line. Generated code is not hand-maintained, so demanding
// hand-written doc-comments on it is meaningless.
//
// # Verdict
//
// Each undocumented symbol is one Finding. A function/method finding
// reads "undocumented function: <pkg>.<Name> (<file>:<line>) — add a
// doc-comment"; a variable/constant finding reads "undocumented
// variable: <pkg>.<Name> (<file>:<line>) — add a doc-comment". The
// summary is "C4 doc-coverage: PASS (N/N functions and variables
// documented)" on success or "C4 doc-coverage: FAIL" on failure.
//
// CheckC4 derives its verdict solely from pkgs and repoRoot (used only
// to render positions repository-relative). Findings are sorted by
// Symbol, then Reason, so the Result is fully deterministic. CheckC4
// performs no I/O and never errors.
func CheckC4(repoRoot string, pkgs []*packages.Package) Result {
	var findings []Finding
	documented := 0
	total := 0

	for _, pkg := range pkgs {
		if pkg == nil || pkg.Fset == nil {
			continue
		}
		pkgLabel := c4PkgLabel(repoRoot, pkg)
		for _, file := range pkg.Syntax {
			if isGeneratedFile(pkg.Fset, file) {
				continue
			}
			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					ok, finding := c4CheckFunc(repoRoot, pkgLabel, pkg.Fset, d)
					if finding == nil && !ok {
						// Exempt declaration (main / init): not counted.
						continue
					}
					total++
					if ok {
						documented++
					} else {
						findings = append(findings, *finding)
					}
				case *ast.GenDecl:
					if d.Tok != token.VAR && d.Tok != token.CONST {
						continue
					}
					vs := c4CheckGenDecl(repoRoot, pkgLabel, pkg.Fset, d)
					for _, r := range vs {
						total++
						if r.finding == nil {
							documented++
						} else {
							findings = append(findings, *r.finding)
						}
					}
				}
			}
		}
	}

	sortFindings(findings)

	passed := len(findings) == 0
	return Result{
		Passed:   passed,
		Findings: findings,
		Summary:  c4Summary(passed, documented, total),
	}
}

// c4SymbolResult is the per-symbol outcome of a C4 variable/constant
// check: finding is nil when the symbol is documented, non-nil
// otherwise.
type c4SymbolResult struct {
	finding *Finding
}

// c4CheckFunc evaluates one function or method declaration. It returns
// ok=true when the declaration carries a non-empty doc-comment, a
// non-nil Finding when it does not, and (false, nil) when the
// declaration is exempt from C4 entirely (the special `main` / `init`
// functions, which conventionally carry no doc-comment).
func c4CheckFunc(
	repoRoot, pkgLabel string,
	fset *token.FileSet,
	fd *ast.FuncDecl,
) (ok bool, finding *Finding) {
	if fd.Name == nil {
		return true, nil
	}
	// `main` and `init` are special: `main` is covered by the package
	// doc-comment and `init` cannot be named, so neither conventionally
	// carries its own doc-comment. They are exempt, not counted.
	if fd.Recv == nil && (fd.Name.Name == "main" || fd.Name.Name == "init") {
		return false, nil
	}

	name := c4FuncName(fd)
	if c4HasDoc(fd.Doc) {
		return true, nil
	}
	return false, &Finding{
		Check:  C4,
		Symbol: pkgLabel + "." + name,
		Reason: fmt.Sprintf(
			"undocumented function: %s.%s (%s) — add a doc-comment",
			pkgLabel, name, relPos(repoRoot, fset, fd.Pos())),
	}
}

// c4CheckGenDecl evaluates one package-level var or const block. Every
// name it declares is a symbol C4 tracks: a doc-comment may sit on the
// GenDecl itself (a block comment shared by every spec) or on the
// individual ValueSpec; either suffices. A var and a const are both
// reported with the noun "variable", so the finding text is uniform
// regardless of the declaration keyword. The returned slice carries one
// c4SymbolResult per declared name.
func c4CheckGenDecl(
	repoRoot, pkgLabel string,
	fset *token.FileSet,
	gd *ast.GenDecl,
) []c4SymbolResult {
	blockDoc := c4HasDoc(gd.Doc)

	var out []c4SymbolResult
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		specDoc := blockDoc || c4HasDoc(vs.Doc)
		for _, ident := range vs.Names {
			if ident == nil || ident.Name == "_" {
				continue
			}
			if specDoc {
				out = append(out, c4SymbolResult{})
				continue
			}
			out = append(out, c4SymbolResult{finding: &Finding{
				Check:  C4,
				Symbol: pkgLabel + "." + ident.Name,
				Reason: fmt.Sprintf(
					"undocumented variable: %s.%s (%s) — add a doc-comment",
					pkgLabel, ident.Name,
					relPos(repoRoot, fset, ident.Pos())),
			}})
		}
	}
	return out
}

// c4HasDoc reports whether a doc-comment group is present and carries
// at least one non-blank line of text.
func c4HasDoc(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	for _, c := range doc.List {
		if strings.TrimSpace(commentText(c.Text)) != "" {
			return true
		}
	}
	return false
}

// c4FuncName renders a function declaration's name as C4 reports it:
// the bare function name for a free function, or "(<receiver>).<method>"
// for a method — the same shape C2's symbolName produces.
func c4FuncName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	return "(" + recvTypeString(fd.Recv.List[0].Type) + ")." + fd.Name.Name
}

// c4PkgLabel renders a package's C4 label: its repository-relative
// directory path (e.g. "internal/domain"), derived from the package's
// first source file. A checkout-independent, repository-relative label
// keeps every C4 finding's Symbol stable regardless of where the
// repository is checked out or what its module path is. It falls back
// to the package's import path, then its name, when no source file is
// positioned.
func c4PkgLabel(repoRoot string, pkg *packages.Package) string {
	if len(pkg.GoFiles) > 0 {
		dir := filepath.ToSlash(pkg.GoFiles[0])
		if i := strings.LastIndexByte(dir, '/'); i >= 0 {
			dir = dir[:i]
		}
		if rel, err := filepath.Rel(repoRoot, filepath.FromSlash(dir)); err == nil &&
			!strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	if pkg.PkgPath != "" {
		return pkg.PkgPath
	}
	return pkg.Name
}

// isGeneratedFile reports whether file is machine-generated and must be
// excluded from C4. A file is generated when its name ends in ".pb.go"
// (protoc-gen-go) or "_grpc.pb.go" (protoc-gen-go-grpc), or when one of
// its leading comments is the standard `// Code generated ... DO NOT
// EDIT.` marker.
func isGeneratedFile(fset *token.FileSet, file *ast.File) bool {
	if file == nil {
		return false
	}
	name := fset.Position(file.Pos()).Filename
	if strings.HasSuffix(name, ".pb.go") || strings.HasSuffix(name, "_grpc.pb.go") {
		return true
	}
	for _, cg := range file.Comments {
		// A "Code generated" marker only counts when it precedes the
		// package clause — that is where go's own convention places it.
		if cg.Pos() > file.Package {
			break
		}
		for _, c := range cg.List {
			if isGeneratedComment(c.Text) {
				return true
			}
		}
	}
	return false
}

// isGeneratedComment reports whether a raw comment line is the standard
// generated-code marker: "Code generated <generator> DO NOT EDIT." —
// the convention go vet and tooling recognise.
func isGeneratedComment(raw string) bool {
	text := commentText(raw)
	return strings.HasPrefix(text, "Code generated") &&
		strings.Contains(text, "DO NOT EDIT")
}

// c4Summary builds the one-line C4 verdict. On success it reports the
// N/N documented count; on failure it is the bare FAIL line — the
// Findings carry the detail.
func c4Summary(passed bool, documented, total int) string {
	if passed {
		return fmt.Sprintf(
			"C4 doc-coverage: PASS (%d/%d functions and variables documented)",
			documented, total)
	}
	return "C4 doc-coverage: FAIL"
}
