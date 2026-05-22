package gen

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/reach"
)

// collectExportedFuncs walks the repository's own packages and returns
// every exported function and method, keyed by a synthetic, stable key
// (the package path plus the symbol name). The map carries the
// declaration and its file set so an L3 artifact can render the symbol's
// source signature, and the *types.Func behind the declaration is the
// bridge to the SSA-name resolver.
//
// The main package is excluded: it is the composition root, not a public
// API surface — the same exclusion C2 applies. Dependency and stdlib
// packages are excluded by considering only the packages explicitly
// loaded for the repository (pkgs), never their transitive imports.
func collectExportedFuncs(
	repoRoot string,
	pkgs []*packages.Package,
) map[string]exportedFunc {
	out := map[string]exportedFunc{}
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
				name := funcSymbolName(fd)
				key := pkg.PkgPath + "." + name
				out[key] = exportedFunc{
					obj:      fd,
					fset:     pkg.Fset,
					name:     name,
					typesObj: obj,
				}
			}
		}
	}
	return out
}

// resolveSSANames maps each exported function declaration to the
// canonical SSA name reach assigns it — the same string populating
// reach.ReachableSet.Funcs — so a reachable-set member can be matched
// back to its declaration. A declaration that does not lower to an SSA
// function (an un-instantiated generic, a declaration the SSA builder
// elided) is simply absent from the result.
//
// resolveSSANames shares reach's symbol resolver: reach.ComputeWithSymbols
// resolves a caller-supplied set of *types.Func off one SSA build. The
// returned reachability graph is discarded here — Generate already holds
// the graph reach.Compute produced; only the name map is needed.
func resolveSSANames(
	pkgs []*packages.Package,
	inv *entrypoints.Inventory,
	exported map[string]exportedFunc,
) map[*ast.FuncDecl]string {
	symbols := make([]*types.Func, 0, len(exported))
	bySymbol := make(map[*types.Func]*ast.FuncDecl, len(exported))
	for _, ef := range exported {
		symbols = append(symbols, ef.typesObj)
		bySymbol[ef.typesObj] = ef.obj
	}

	_, names, err := reach.ComputeWithSymbols(pkgs, inv, nil, symbols)
	if err != nil || names == nil {
		// A reachability-analysis failure leaves L3 without signatures
		// rather than failing the whole generation: the call-tree (from
		// the already-computed graph) is still produced.
		return map[*ast.FuncDecl]string{}
	}

	out := make(map[*ast.FuncDecl]string, len(names))
	for tf, ssaName := range names {
		if fd, ok := bySymbol[tf]; ok {
			out[fd] = ssaName
		}
	}
	return out
}

// funcSymbolName renders a func declaration's human-readable name: the
// bare function name for a free function, or "(<receiver>).<method>" for
// a method, with a leading "*" on a pointer receiver — the same shape
// C2's symbolName produces, so the two passes name a symbol identically.
func funcSymbolName(fd *ast.FuncDecl) string {
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

// relPath renders abs as a slash-separated path relative to repoRoot, so
// an artifact reads "internal/domain/domain.go" regardless of the
// repository's checkout location. A path that cannot be made relative
// falls back to its absolute, slash-separated form.
func relPath(repoRoot, abs string) string {
	rel, err := filepath.Rel(repoRoot, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}
