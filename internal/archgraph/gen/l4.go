package gen

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
)

// domainSurface is the L4 domain inventory of a repository: the exported
// types (with their exported struct fields), the exported package-level
// variables and the exported package-level constants of the
// repository's own packages.
type domainSurface struct {
	// Types are the exported type declarations, sorted by qualified name.
	Types []domainType
	// Vars are the exported package-level variables, sorted by qualified
	// name.
	Vars []domainValue
	// Consts are the exported package-level constants, sorted by
	// qualified name.
	Consts []domainValue
}

// domainType is one exported type of the repository: its package path,
// its name, the underlying-kind keyword ("struct", "interface", a named
// underlying type, …), its doc-comment synopsis, its exported struct
// fields and the repo functions whose signature mentions it.
type domainType struct {
	Pkg      string
	Name     string
	Kind     string
	Synopsis string
	Fields   []domainField
	// Users are the canonical names of the repo functions that name this
	// type in a signature, sorted — the L4 "type → functions" link.
	Users []string
}

// qualified renders the type's package-qualified name, e.g.
// "internal/domain.Network", used as the deterministic sort key and the
// L4 table's identity column.
func (t domainType) qualified() string { return t.Pkg + "." + t.Name }

// domainField is one exported struct field: its name, its type as
// written in source and its doc-comment synopsis.
type domainField struct {
	Name     string
	Type     string
	Synopsis string
}

// domainValue is one exported package-level variable or constant: its
// package path, its name, its declared type (empty when untyped), its
// doc-comment synopsis and the repo functions that read or write it.
type domainValue struct {
	Pkg      string
	Name     string
	Type     string
	Synopsis string
	// Users are the canonical names of the repo functions that read or
	// write this variable/constant, sorted — the L4 "variable →
	// functions" link.
	Users []string
}

// qualified renders the value's package-qualified name.
func (c domainValue) qualified() string { return c.Pkg + "." + c.Name }

// collectDomainSurface walks the repository's own packages and returns
// its L4 domain surface: every exported type with its exported struct
// fields, every exported package-level variable and every exported
// package-level constant — each annotated with its doc-comment synopsis
// and, for a value, the repo functions that use it.
//
// The main package is excluded — it is the composition root, not a
// domain surface. Dependency and stdlib packages are excluded by
// considering only the explicitly loaded packages (pkgs), never their
// transitive imports. Package paths are rendered repository-relative
// where possible so the artifact is checkout-independent.
//
// su carries the use-def index — which repo functions reference each
// package var/const and each named type — collected once by
// collectSymbolUsers and shared with this pass.
func collectDomainSurface(
	repoRoot string,
	pkgs []*packages.Package,
	su symbolUsers,
) domainSurface {
	var s domainSurface
	for _, pkg := range pkgs {
		if pkg.Name == "main" || pkg.Fset == nil {
			continue
		}
		pkgLabel := pkgLabel(repoRoot, pkg)
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				switch gd.Tok {
				case token.TYPE:
					s.Types = append(s.Types,
						exportedTypes(repoRoot, pkgLabel, pkg, gd, su)...)
				case token.VAR:
					s.Vars = append(s.Vars,
						exportedValues(repoRoot, pkgLabel, pkg, gd, su)...)
				case token.CONST:
					s.Consts = append(s.Consts,
						exportedValues(repoRoot, pkgLabel, pkg, gd, su)...)
				}
			}
		}
	}
	sort.Slice(s.Types, func(i, j int) bool {
		return s.Types[i].qualified() < s.Types[j].qualified()
	})
	sort.Slice(s.Vars, func(i, j int) bool {
		return s.Vars[i].qualified() < s.Vars[j].qualified()
	})
	sort.Slice(s.Consts, func(i, j int) bool {
		return s.Consts[i].qualified() < s.Consts[j].qualified()
	})
	return s
}

// pkgLabel renders a package's repository-relative directory path — e.g.
// "internal/domain" — by relativising the package's first source file's
// directory against repoRoot. It falls back to the package path when no
// source file is positioned, so the label is always non-empty.
func pkgLabel(repoRoot string, pkg *packages.Package) string {
	if len(pkg.GoFiles) > 0 {
		dir := pkg.GoFiles[0]
		if i := strings.LastIndexByte(dir, '/'); i >= 0 {
			dir = dir[:i]
		}
		return relPath(repoRoot, dir)
	}
	return pkg.PkgPath
}

// exportedTypes returns the exported type declarations of one GenDecl,
// each annotated with its doc-comment synopsis and its type-user list.
func exportedTypes(
	repoRoot, pkgLabel string,
	pkg *packages.Package,
	gd *ast.GenDecl,
	su symbolUsers,
) []domainType {
	var out []domainType
	for _, spec := range gd.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok || ts.Name == nil || !token.IsExported(ts.Name.Name) {
			continue
		}
		dt := domainType{
			Pkg:      pkgLabel,
			Name:     ts.Name.Name,
			Kind:     typeKind(pkg.Fset, ts.Type),
			Synopsis: docSynopsis(specDoc(gd, ts.Doc)),
			Users:    typeUsersOf(repoRoot, pkg, ts.Name, su),
		}
		if st, ok := ts.Type.(*ast.StructType); ok {
			dt.Fields = exportedFields(pkg.Fset, st)
		}
		out = append(out, dt)
	}
	return out
}

// specDoc returns the doc-comment that documents a single spec: the
// spec's own doc-comment when present, otherwise the GenDecl's
// doc-comment — Go's convention for a one-spec declaration block.
func specDoc(gd *ast.GenDecl, specDoc *ast.CommentGroup) *ast.CommentGroup {
	if specDoc != nil {
		return specDoc
	}
	return gd.Doc
}

// typeUsersOf returns the sorted repo-function users of the named type
// ident, looked up in the use-def index by the type's *types.Object.
func typeUsersOf(
	repoRoot string,
	pkg *packages.Package,
	ident *ast.Ident,
	su symbolUsers,
) []string {
	if pkg.TypesInfo == nil {
		return nil
	}
	obj := pkg.TypesInfo.Defs[ident]
	if obj == nil {
		return nil
	}
	return su.byType[obj]
}

// typeKind returns a short keyword describing a type's underlying form:
// "struct" / "interface" for composite types, otherwise the underlying
// type rendered as source ("string" for a defined string type, etc.).
func typeKind(fset *token.FileSet, expr ast.Expr) string {
	switch expr.(type) {
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	default:
		return printNode(fset, expr)
	}
}

// exportedFields returns the exported fields of a struct type, in source
// order — source order is itself deterministic, so no extra sort is
// needed. An embedded field contributes its type name. Each field
// carries its doc-comment synopsis.
func exportedFields(fset *token.FileSet, st *ast.StructType) []domainField {
	var out []domainField
	if st.Fields == nil {
		return out
	}
	for _, f := range st.Fields.List {
		typeStr := printNode(fset, f.Type)
		synopsis := docSynopsis(fieldDoc(f))
		if len(f.Names) == 0 {
			// Embedded field — its name is its (possibly qualified) type.
			name := embeddedName(typeStr)
			if token.IsExported(name) {
				out = append(out, domainField{
					Name: name, Type: typeStr, Synopsis: synopsis})
			}
			continue
		}
		for _, n := range f.Names {
			if !token.IsExported(n.Name) {
				continue
			}
			out = append(out, domainField{
				Name: n.Name, Type: typeStr, Synopsis: synopsis})
		}
	}
	return out
}

// fieldDoc returns the doc-comment of a struct field: the leading
// doc-comment when present, otherwise the trailing line comment — both
// are legitimate field documentation in Go source.
func fieldDoc(f *ast.Field) *ast.CommentGroup {
	if f.Doc != nil {
		return f.Doc
	}
	return f.Comment
}

// embeddedName extracts the bare field name of an embedded field from
// its type text — the last identifier of "pkg.Type" or the text itself.
func embeddedName(typeStr string) string {
	s := strings.TrimPrefix(typeStr, "*")
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// exportedValues returns the exported package-level variables or
// constants of one GenDecl, each annotated with its doc-comment
// synopsis and the repo functions that read or write it. A const block
// carries an iota-running type forward across specs; exportedValues
// tracks the last explicit type so a value-only spec inherits it,
// matching Go's const-block semantics.
func exportedValues(
	repoRoot, pkgLabel string,
	pkg *packages.Package,
	gd *ast.GenDecl,
	su symbolUsers,
) []domainValue {
	var out []domainValue
	lastType := ""
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		if vs.Type != nil {
			lastType = printNode(pkg.Fset, vs.Type)
		}
		synopsis := docSynopsis(specDoc(gd, vs.Doc))
		for _, n := range vs.Names {
			if !token.IsExported(n.Name) {
				continue
			}
			out = append(out, domainValue{
				Pkg:      pkgLabel,
				Name:     n.Name,
				Type:     lastType,
				Synopsis: synopsis,
				Users:    valueUsersOf(pkg, n, su),
			})
		}
	}
	return out
}

// valueUsersOf returns the sorted repo-function users of the package
// var/const named ident, looked up in the use-def index by the value's
// *types.Object.
func valueUsersOf(pkg *packages.Package, ident *ast.Ident, su symbolUsers) []string {
	if pkg.TypesInfo == nil {
		return nil
	}
	obj := pkg.TypesInfo.Defs[ident]
	if obj == nil {
		return nil
	}
	return su.byVar[obj]
}

// renderL4 builds the Markdown body of the repository's L4 artifact: the
// GENERATED-BY marker, an Obsidian wikilink index of the repository's L3
// functionality artifacts, a table of every exported type (with its
// synopsis, exported struct fields and type-user functions), a table of
// every exported package-level variable and constant (with its
// synopsis and the functions that use it) and the function →
// functionality membership table. Every collection is pre-sorted, so
// the output is byte-for-byte reproducible.
func renderL4(
	repo string,
	s domainSurface,
	funcFunctionality []funcFunctionalityRow,
	notes []*note.Note,
) string {
	var b strings.Builder
	b.WriteString(genMarker + "\n\n")
	fmt.Fprintf(&b, "# L4 — %s domain surface\n\n", repo)
	b.WriteString("Generated inventory of the repository's exported domain " +
		"types, fields, variables and constants — each with its doc-comment " +
		"synopsis and its structural links to the code.\n\n")

	renderL4Functionalities(&b, notes)
	renderL4Types(&b, s.Types)
	renderL4Values(&b, "Variables", s.Vars)
	renderL4Values(&b, "Constants", s.Consts)
	renderL4FuncFunctionality(&b, funcFunctionality)
	return b.String()
}

// renderL4Functionalities writes the `## Функциональности` section: an
// Obsidian wikilink to the L3 artifact of every anchored L2
// functionality note of the repository. The L3 artifact of a note is
// `l3-<note-base>.md` (l3FileName); the wikilink is by base name without
// the .md extension, so Obsidian resolves it globally. A planned
// functionality (no anchors, hence no L3 artifact) carries no link. The
// links are sorted for a byte-stable section.
func renderL4Functionalities(b *strings.Builder, notes []*note.Note) {
	b.WriteString("## Функциональности\n\n")
	links := make([]string, 0, len(notes))
	for _, n := range notes {
		if len(n.Anchors) == 0 {
			continue
		}
		base := l3FileName(n) // "l3-<slug>.md"
		links = append(links, strings.TrimSuffix(base, ".md"))
	}
	if len(links) == 0 {
		b.WriteString("_No anchored functionalities._\n\n")
		return
	}
	sort.Strings(links)
	for _, l := range links {
		fmt.Fprintf(b, "- [[%s]]\n", l)
	}
	b.WriteString("\n")
}

// renderL4Types writes the `## Types` section: one row per exported
// type with its kind, synopsis, exported fields and the repo functions
// that mention it in a signature.
func renderL4Types(b *strings.Builder, types []domainType) {
	b.WriteString("## Types\n\n")
	if len(types) == 0 {
		b.WriteString("_No exported types._\n\n")
		return
	}
	b.WriteString("| Type | Kind | Synopsis | Fields | Used by |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, t := range types {
		fmt.Fprintf(b, "| `%s` | %s | %s | %s | %s |\n",
			t.qualified(), t.Kind, mdCell(t.Synopsis),
			fieldList(t.Fields), funcRefList(t.Users))
	}
	b.WriteString("\n")
}

// renderL4Values writes a `## Variables` or `## Constants` section: one
// row per exported package-level value with its type, synopsis and the
// repo functions that read or write it.
func renderL4Values(b *strings.Builder, heading string, values []domainValue) {
	fmt.Fprintf(b, "## %s\n\n", heading)
	if len(values) == 0 {
		fmt.Fprintf(b, "_No exported %s._\n\n", strings.ToLower(heading))
		return
	}
	b.WriteString("| Name | Type | Synopsis | Used by |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, v := range values {
		typeCol := v.Type
		if typeCol == "" {
			typeCol = "_untyped_"
		}
		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n",
			v.qualified(), typeCol, mdCell(v.Synopsis), funcRefList(v.Users))
	}
	b.WriteString("\n")
}

// funcFunctionalityRow is one row of the L4 function → functionality
// table: a repo function and the L2 functionalities it belongs to.
type funcFunctionalityRow struct {
	// Func is the canonical name of the repo function.
	Func string
	// Functionalities are the slugs of the L2 notes whose anchors reach
	// this function, sorted. Empty means the function is unreached.
	Functionalities []string
}

// renderL4FuncFunctionality writes the `## Functions → functionality`
// section: every repo function mapped to the L2 functionality (or
// functionalities) it serves. A function reached from no functionality
// is rendered "(unreached)".
func renderL4FuncFunctionality(b *strings.Builder, rows []funcFunctionalityRow) {
	b.WriteString("## Functions → functionality\n\n")
	if len(rows) == 0 {
		b.WriteString("_No repository functions._\n")
		return
	}
	b.WriteString("| Function | Functionality |\n")
	b.WriteString("| --- | --- |\n")
	for _, r := range rows {
		cell := "(unreached)"
		if len(r.Functionalities) > 0 {
			parts := make([]string, 0, len(r.Functionalities))
			for _, f := range r.Functionalities {
				parts = append(parts, "`"+f+"`")
			}
			cell = strings.Join(parts, ", ")
		}
		fmt.Fprintf(b, "| `%s` | %s |\n", r.Func, cell)
	}
}

// fieldList renders a type's exported fields as a single table cell:
// "name type — synopsis" entries joined by "; ", or an em dash when the
// type has no exported field.
func fieldList(fields []domainField) string {
	if len(fields) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		entry := fmt.Sprintf("`%s %s`", f.Name, f.Type)
		if f.Synopsis != "" && f.Synopsis != undocumentedSynopsis {
			entry += " — " + mdCell(f.Synopsis)
		}
		parts = append(parts, entry)
	}
	return strings.Join(parts, "; ")
}

// funcRefList renders a sorted list of repo-function names as a single
// table cell — back-quoted names joined by "; " — or an em dash when no
// function references the symbol.
func funcRefList(funcs []string) string {
	if len(funcs) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(funcs))
	for _, f := range funcs {
		parts = append(parts, "`"+f+"`")
	}
	return strings.Join(parts, "; ")
}

// mdCell escapes a free-text string for safe rendering inside a
// Markdown table cell: a literal "|" would otherwise be read as a
// column separator, and a newline would break the row. Both are
// neutralised; the text is otherwise preserved.
func mdCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
