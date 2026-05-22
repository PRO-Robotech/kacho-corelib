package gen

import (
	"go/ast"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/note"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/reach"
)

// undocumentedSynopsis is the placeholder synopsis archgraph renders for
// a repo-owned function, type, field, variable or constant that carries
// no Go doc-comment. It cross-references the C4 doc-coverage check,
// which fails CI for exactly such a symbol: the enriched artifact shows
// the gap, and C4 forces it closed.
const undocumentedSynopsis = "(undocumented — see C4)"

// repoFunc is one function or method defined in a repository-owned
// package — exported or not — with everything the L3/L4 enrichment
// needs: the *types.Func behind the declaration (the bridge to the
// SSA-name resolver) and its doc-comment synopsis.
type repoFunc struct {
	typesObj *types.Func
	synopsis string
}

// collectRepoFuncs walks the repository's own packages and returns
// every function and method they define — exported and unexported,
// the main package included — keyed by a synthetic stable key
// (package path + symbol name).
//
// Unlike collectExportedFuncs (which feeds L3's exported-signature
// table) this collector keeps unexported helpers too, because an L3
// call-tree lists every reachable repo function regardless of export,
// and each one is annotated with its doc-comment synopsis.
func collectRepoFuncs(pkgs []*packages.Package) map[string]repoFunc {
	out := map[string]repoFunc{}
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Name == nil {
					continue
				}
				obj, _ := pkg.TypesInfo.Defs[fd.Name].(*types.Func)
				if obj == nil {
					continue
				}
				out[pkg.PkgPath+"."+funcSymbolName(fd)] = repoFunc{
					typesObj: obj,
					synopsis: docSynopsis(fd.Doc),
				}
			}
		}
	}
	return out
}

// repoFuncsBySSA maps every repo-owned function to the canonical SSA
// name reach assigns it — the same string that populates
// reach.ReachableSet.Funcs — so a call-tree member (an SSA name) can be
// matched back to its repoFunc, and thus to its doc-comment synopsis.
// It covers unexported helpers too, unlike resolveSSANames (which is
// scoped to exported symbols for the L3 signature table).
//
// inv is the repository's entry-point inventory: reach.ComputeWithSymbols
// resolves symbols only when it actually lowers the program to SSA, and
// it lowers only when there is at least one reachability root. Passing
// the real inventory therefore guarantees the name resolution runs; a
// library repository (no entry-points) has no call-tree to annotate
// anyway. A reachability-analysis failure yields an empty map: the L3
// call-tree is then rendered without per-function synopses rather than
// failing the whole generation.
func repoFuncsBySSA(
	pkgs []*packages.Package,
	inv *entrypoints.Inventory,
	repoFuncs map[string]repoFunc,
) map[string]repoFunc {
	symbols := make([]*types.Func, 0, len(repoFuncs))
	byObj := make(map[*types.Func]repoFunc, len(repoFuncs))
	for _, rf := range repoFuncs {
		symbols = append(symbols, rf.typesObj)
		byObj[rf.typesObj] = rf
	}

	_, names, err := reach.ComputeWithSymbols(pkgs, inv, nil, symbols)
	if err != nil || names == nil {
		return map[string]repoFunc{}
	}

	bySSA := map[string]repoFunc{}
	for tf, ssaName := range names {
		if rf, ok := byObj[tf]; ok {
			bySSA[ssaName] = rf
		}
	}
	return bySSA
}

// docSynopsis returns the one-line synopsis of a doc-comment: the first
// sentence of its first paragraph, flattened to a single line. An
// absent or empty doc-comment yields undocumentedSynopsis, the
// placeholder that points the reader at the C4 doc-coverage check.
func docSynopsis(doc *ast.CommentGroup) string {
	if doc == nil {
		return undocumentedSynopsis
	}
	text := doc.Text() // go/ast strips the comment markers for us.
	text = strings.TrimSpace(text)
	if text == "" {
		return undocumentedSynopsis
	}
	// First paragraph: everything up to the first blank line.
	if i := strings.Index(text, "\n\n"); i >= 0 {
		text = text[:i]
	}
	text = strings.Join(strings.Fields(text), " ")
	// First sentence: up to the first ". " (or a trailing ".").
	if i := firstSentenceEnd(text); i >= 0 {
		text = text[:i+1]
	}
	return text
}

// firstSentenceEnd returns the index of the period that ends the first
// sentence of s, or -1 when s carries no sentence-ending period. A
// period is sentence-ending when it is followed by a space or ends the
// string — the heuristic go/doc itself uses, so "kacho.cloud" mid-word
// does not split.
func firstSentenceEnd(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '.' {
			continue
		}
		if i == len(s)-1 || s[i+1] == ' ' {
			return i
		}
	}
	return -1
}

// functionalityIndex maps a repo function's canonical SSA name to the
// sorted, de-duplicated list of L2 functionalities it belongs to — a
// functionality being the slug of an L2 note. A function reachable from
// the anchors of several notes belongs to all of them; a function
// reachable from none is absent from the map (the caller renders it as
// "(unreached)").
//
// The membership is read straight off the reachability graph: a
// function is in note N's functionality iff it is in the union of the
// reachable-sets of N's anchors.
func functionalityIndex(notes []*note.Note, g *reach.Graph) map[string][]string {
	membership := map[string]map[string]struct{}{}
	if g != nil {
		for _, n := range notes {
			slug := noteSlug(n)
			for _, sym := range anchorSymbols(n) {
				rs, ok := g.Sets[sym]
				if !ok {
					continue
				}
				for _, f := range rs.Funcs {
					if membership[f] == nil {
						membership[f] = map[string]struct{}{}
					}
					membership[f][slug] = struct{}{}
				}
			}
		}
	}
	out := make(map[string][]string, len(membership))
	for fn, slugs := range membership {
		out[fn] = sortedKeys(slugs)
	}
	return out
}

// buildFuncFunctionalityRows builds the L4 "function → functionality"
// table: one row per repository-owned function (by canonical SSA name),
// mapped to the sorted L2 functionalities whose anchors reach it. A
// function reached by no functionality carries an empty slice, which
// renderL4FuncFunctionality renders as "(unreached)".
//
// The row set is the keys of synopsisBySSA — every repo function the
// SSA-name resolver matched — restricted to functions defined in one of
// the repository's own packages (repoPkgs), so a synthetic or
// dependency name never leaks into the table. The rows are sorted by
// function name for a byte-stable artifact.
func buildFuncFunctionalityRows(
	synopsisBySSA map[string]repoFunc,
	notes []*note.Note,
	g *reach.Graph,
	repoPkgs map[string]struct{},
) []funcFunctionalityRow {
	index := functionalityIndex(notes, g)

	rows := make([]funcFunctionalityRow, 0, len(synopsisBySSA))
	for ssaName := range synopsisBySSA {
		if !isRepoFunc(ssaName, repoPkgs) {
			continue
		}
		rows = append(rows, funcFunctionalityRow{
			Func:            ssaName,
			Functionalities: index[ssaName],
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Func < rows[j].Func })
	return rows
}

// noteSlug returns the L2 functionality slug of a note: its file base
// name without the .md extension — the same slug l3FileName embeds, so
// an L4 "function → functionality" entry and the matching L3 artifact
// name line up.
func noteSlug(n *note.Note) string {
	base := n.Path()
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, ".md")
}

// symbolUsers maps a repository-owned package-level var/const or type
// to the sorted list of repo functions that reference it.
//
// For a var/const the reference is a read or write of the value — any
// *ast.Ident in a function body resolving (through go/types Uses) to
// the var's *types.Object. For a type the reference is structural —
// the type named anywhere in a function's signature (parameters or
// results). The result is the L4 "variable → functions" / "type →
// functions" link the enrichment renders.
type symbolUsers struct {
	// byVar maps a package-level var/const object to its repo-function
	// users, keyed by the object, valued by a sorted function-name list.
	byVar map[types.Object][]string
	// byType maps an exported named type to the repo functions whose
	// signature mentions it, keyed by the type's object.
	byType map[types.Object][]string
}

// collectSymbolUsers builds the use-def index L4's enrichment needs: for
// every repository-owned package-level var/const it records which repo
// functions read or write it, and for every repository-owned named type
// it records which repo functions name it in a signature.
//
// It walks the repository's own packages only — a use in a dependency
// is outside the artifact's mandate — and resolves identifiers through
// go/types' Uses and Defs maps, so a shadowed local of the same spelling
// is never mistaken for the package-level symbol.
func collectSymbolUsers(pkgs []*packages.Package) symbolUsers {
	su := symbolUsers{
		byVar:  map[types.Object][]string{},
		byType: map[types.Object][]string{},
	}

	// pkgVars: every package-level var/const object of a repo package.
	// pkgTypes: every named type object of a repo package.
	pkgVars := map[types.Object]struct{}{}
	pkgTypes := map[types.Object]struct{}{}
	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			switch obj := scope.Lookup(name).(type) {
			case *types.Var, *types.Const:
				pkgVars[obj] = struct{}{}
			case *types.TypeName:
				pkgTypes[obj] = struct{}{}
			}
		}
	}

	varUsers := map[types.Object]map[string]struct{}{}
	typeUsers := map[types.Object]map[string]struct{}{}

	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Name == nil {
					continue
				}
				fnName := pkg.PkgPath + "." + funcSymbolName(fd)

				// var/const reads & writes — every identifier in the whole
				// declaration (body and signature) resolving to a tracked
				// package-level var/const.
				ast.Inspect(fd, func(node ast.Node) bool {
					id, ok := node.(*ast.Ident)
					if !ok {
						return true
					}
					obj := pkg.TypesInfo.Uses[id]
					if obj == nil {
						return true
					}
					if _, tracked := pkgVars[obj]; tracked {
						if varUsers[obj] == nil {
							varUsers[obj] = map[string]struct{}{}
						}
						varUsers[obj][fnName] = struct{}{}
					}
					return true
				})

				// type usage — every named type mentioned in the function's
				// signature (receiver, parameters, results).
				for _, tObj := range signatureTypes(pkg.TypesInfo, fd) {
					if _, tracked := pkgTypes[tObj]; !tracked {
						continue
					}
					if typeUsers[tObj] == nil {
						typeUsers[tObj] = map[string]struct{}{}
					}
					typeUsers[tObj][fnName] = struct{}{}
				}
			}
		}
	}

	for obj, set := range varUsers {
		su.byVar[obj] = sortedKeys(set)
	}
	for obj, set := range typeUsers {
		su.byType[obj] = sortedKeys(set)
	}
	return su
}

// signatureTypes returns the *types.Object of every named type
// mentioned in fd's signature — the receiver, the parameter types and
// the result types. It resolves identifiers through info.Uses so a type
// is matched by identity, not by spelling.
func signatureTypes(info *types.Info, fd *ast.FuncDecl) []types.Object {
	seen := map[types.Object]struct{}{}
	var out []types.Object
	collect := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			ast.Inspect(f.Type, func(node ast.Node) bool {
				id, ok := node.(*ast.Ident)
				if !ok {
					return true
				}
				if obj, ok := info.Uses[id].(*types.TypeName); ok {
					if _, dup := seen[obj]; !dup {
						seen[obj] = struct{}{}
						out = append(out, obj)
					}
				}
				return true
			})
		}
	}
	collect(fd.Recv)
	if fd.Type != nil {
		collect(fd.Type.Params)
		collect(fd.Type.Results)
	}
	return out
}

