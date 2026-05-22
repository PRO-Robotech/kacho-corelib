package gen

import (
	"fmt"
	"go/ast"
	"go/token"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// domainSurface is the L4 domain inventory of a repository: the exported
// types (with their exported struct fields) and the exported
// package-level constants of the repository's own packages.
type domainSurface struct {
	// Types are the exported type declarations, sorted by qualified name.
	Types []domainType
	// Consts are the exported package-level constants, sorted by
	// qualified name.
	Consts []domainConst
}

// domainType is one exported type of the repository: its package path,
// its name, the underlying-kind keyword ("struct", "interface", a named
// underlying type, …) and, for a struct, its exported fields.
type domainType struct {
	Pkg    string
	Name   string
	Kind   string
	Fields []domainField
}

// qualified renders the type's package-qualified name, e.g.
// "internal/domain.Network", used as the deterministic sort key and the
// L4 table's identity column.
func (t domainType) qualified() string { return t.Pkg + "." + t.Name }

// domainField is one exported struct field: its name and its type as
// written in source.
type domainField struct {
	Name string
	Type string
}

// domainConst is one exported package-level constant: its package path,
// its name and its declared type (empty when the constant is untyped).
type domainConst struct {
	Pkg  string
	Name string
	Type string
}

// qualified renders the constant's package-qualified name.
func (c domainConst) qualified() string { return c.Pkg + "." + c.Name }

// collectDomainSurface walks the repository's own packages and returns
// its L4 domain surface: every exported type with its exported struct
// fields, and every exported package-level constant.
//
// The main package is excluded — it is the composition root, not a
// domain surface. Dependency and stdlib packages are excluded by
// considering only the explicitly loaded packages (pkgs), never their
// transitive imports. Package paths are rendered repository-relative
// where possible so the artifact is checkout-independent.
func collectDomainSurface(repoRoot string, pkgs []*packages.Package) domainSurface {
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
					s.Types = append(s.Types, exportedTypes(pkgLabel, pkg.Fset, gd)...)
				case token.CONST:
					s.Consts = append(s.Consts, exportedConsts(pkgLabel, pkg.Fset, gd)...)
				}
			}
		}
	}
	sort.Slice(s.Types, func(i, j int) bool {
		return s.Types[i].qualified() < s.Types[j].qualified()
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

// exportedTypes returns the exported type declarations of one GenDecl.
func exportedTypes(pkgLabel string, fset *token.FileSet, gd *ast.GenDecl) []domainType {
	var out []domainType
	for _, spec := range gd.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok || ts.Name == nil || !token.IsExported(ts.Name.Name) {
			continue
		}
		dt := domainType{
			Pkg:  pkgLabel,
			Name: ts.Name.Name,
			Kind: typeKind(fset, ts.Type),
		}
		if st, ok := ts.Type.(*ast.StructType); ok {
			dt.Fields = exportedFields(fset, st)
		}
		out = append(out, dt)
	}
	return out
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
// needed. An embedded field contributes its type name.
func exportedFields(fset *token.FileSet, st *ast.StructType) []domainField {
	var out []domainField
	if st.Fields == nil {
		return out
	}
	for _, f := range st.Fields.List {
		typeStr := printNode(fset, f.Type)
		if len(f.Names) == 0 {
			// Embedded field — its name is its (possibly qualified) type.
			name := embeddedName(typeStr)
			if token.IsExported(name) {
				out = append(out, domainField{Name: name, Type: typeStr})
			}
			continue
		}
		for _, n := range f.Names {
			if !token.IsExported(n.Name) {
				continue
			}
			out = append(out, domainField{Name: n.Name, Type: typeStr})
		}
	}
	return out
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

// exportedConsts returns the exported package-level constants of one
// GenDecl. A const block carries an iota-running type forward across
// specs; exportedConsts tracks the last explicit type so a value-only
// spec inherits it, matching Go's const-block semantics.
func exportedConsts(pkgLabel string, fset *token.FileSet, gd *ast.GenDecl) []domainConst {
	var out []domainConst
	lastType := ""
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		if vs.Type != nil {
			lastType = printNode(fset, vs.Type)
		}
		for _, n := range vs.Names {
			if !token.IsExported(n.Name) {
				continue
			}
			out = append(out, domainConst{
				Pkg:  pkgLabel,
				Name: n.Name,
				Type: lastType,
			})
		}
	}
	return out
}

// renderL4 builds the Markdown body of the repository's L4 artifact: the
// GENERATED-BY marker, a table of every exported type with its exported
// struct fields, and a table of every exported package-level constant.
// Every collection is pre-sorted by collectDomainSurface, so the output
// is byte-for-byte reproducible.
func renderL4(repo string, s domainSurface) string {
	var b strings.Builder
	b.WriteString(genMarker + "\n\n")
	fmt.Fprintf(&b, "# L4 — %s domain surface\n\n", repo)
	b.WriteString("Generated inventory of the repository's exported domain " +
		"types, fields and constants.\n\n")

	b.WriteString("## Types\n\n")
	if len(s.Types) == 0 {
		b.WriteString("_No exported types._\n\n")
	} else {
		b.WriteString("| Type | Kind | Fields |\n")
		b.WriteString("| --- | --- | --- |\n")
		for _, t := range s.Types {
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n",
				t.qualified(), t.Kind, fieldList(t.Fields))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Constants\n\n")
	if len(s.Consts) == 0 {
		b.WriteString("_No exported constants._\n")
	} else {
		b.WriteString("| Constant | Type |\n")
		b.WriteString("| --- | --- |\n")
		for _, c := range s.Consts {
			typeCol := c.Type
			if typeCol == "" {
				typeCol = "_untyped_"
			}
			fmt.Fprintf(&b, "| `%s` | %s |\n", c.qualified(), typeCol)
		}
	}
	return b.String()
}

// fieldList renders a type's exported fields as a single table cell:
// "name type" pairs joined by "; ", or an em dash when the type has no
// exported field (a non-struct type, or a struct with only private
// fields).
func fieldList(fields []domainField) string {
	if len(fields) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, fmt.Sprintf("`%s %s`", f.Name, f.Type))
	}
	return strings.Join(parts, "; ")
}
