// Package entrypoints builds the entry-point inventory of a service
// repository: the set of roots from which archgraph's later
// reachability pass walks the call-graph.
//
// An entry-point is one of two kinds:
//
//   - KindRPC — a gRPC method, recognised by a RegisterXxxServer call
//     in a main package. Its canonical name is "<proto-FQN>/<method>",
//     where the proto FQN is read from the registered grpc.ServiceDesc
//     value's ServiceName string literal (never derived from the
//     RegisterXxxServer identifier);
//   - KindWorker — a background worker, reconciler or cron job,
//     recognised by the convention New<Name>(...) followed by a Run or
//     Start call on the result (including the "go" and chained forms).
//     Its canonical name is the worker type's name.
//
// Recognition is deliberately conservative: a worker started without
// the New<Name> + Run/Start convention is not inventoried, but a Hint
// is collected so arch-audit can flag it for a human.
//
// A repository with no runnable main package has no entry-points and is
// classified as a library (Inventory.IsLibraryRepo).
//
// This package depends only on the standard library and
// golang.org/x/tools/go/packages; it imports no gRPC runtime, no
// persistence and no transport code.
package entrypoints

import (
	"errors"
	"go/ast"
	"go/token"
	"go/types"
	"sort"

	"golang.org/x/tools/go/packages"
)

// Kind classifies an entry-point.
type Kind string

const (
	// KindRPC marks a gRPC method entry-point.
	KindRPC Kind = "rpc"
	// KindWorker marks a background worker / reconciler / cron entry-point.
	KindWorker Kind = "worker"
)

// workerStartMethods is the fixed set of method names that, called on a
// New<Name>(...) result, mark a worker entry-point. It is intentionally
// closed: archgraph's worker convention is exactly New + Run/Start.
var workerStartMethods = map[string]bool{
	"Run":   true,
	"Start": true,
}

// EntryPoint is one inventoried root of reachability.
type EntryPoint struct {
	// Kind is rpc or worker.
	Kind Kind
	// Name is the canonical name: "<proto-FQN>/<method>" for an rpc
	// entry-point, or the worker type's name for a worker.
	Name string
	// Root is the function or method the reachability pass (Task 4)
	// walks the call-graph from: the gRPC handler method for an rpc
	// entry-point, or the Run/Start method for a worker.
	Root *types.Func
}

// Hint is a non-fatal observation surfaced by arch-audit: a goroutine
// that looks like a worker entry-point but does not match the
// recognised convention, so it was left out of the inventory.
type Hint struct {
	// Message is the human-readable hint, including a file:line
	// position and a pointer to the entry-point convention.
	Message string
}

// Inventory is the result of Discover: the entry-points of a
// repository, plus any unrecognised-worker hints and the library flag.
type Inventory struct {
	// Entries are the recognised entry-points, ordered deterministically
	// (by kind, then name).
	Entries []EntryPoint
	// Hints are unrecognised-worker observations for arch-audit.
	Hints []Hint
	// IsLibraryRepo is true when the repository has no runnable main
	// package — it has no entry-points by construction.
	IsLibraryRepo bool
}

// ErrUnresolvedFQN is the sentinel wrapped when a RegisterXxxServer call
// cannot be resolved to a proto service FQN.
var ErrUnresolvedFQN = errors.New("cannot resolve service FQN")

// Discover builds the entry-point inventory from a loaded package set.
// pkgs must be loaded with at least NeedName, NeedSyntax, NeedTypes,
// NeedTypesInfo, NeedImports and NeedDeps so that main packages and
// their gRPC-stub dependencies are both available for AST/type walking.
//
// Discover fails only when a RegisterXxxServer call is found but its
// service FQN cannot be resolved (B7); a repository with no main
// package is not an error — it yields an empty, library-flagged
// inventory.
func Discover(pkgs []*packages.Package) (*Inventory, error) {
	idx := newIndex(pkgs)

	mains := mainPackages(pkgs)
	inv := &Inventory{IsLibraryRepo: len(mains) == 0}

	for _, mp := range mains {
		rpcs, err := discoverRPC(mp, idx)
		if err != nil {
			return nil, err
		}
		inv.Entries = append(inv.Entries, rpcs...)

		workers, hints := discoverWorkers(mp, idx)
		inv.Entries = append(inv.Entries, workers...)
		inv.Hints = append(inv.Hints, hints...)
	}

	sortEntries(inv.Entries)
	return inv, nil
}

// sortEntries orders entries deterministically: by kind, then name.
func sortEntries(es []EntryPoint) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Kind != es[j].Kind {
			return es[i].Kind < es[j].Kind
		}
		return es[i].Name < es[j].Name
	})
}

// mainPackages returns the runnable main packages of pkgs: packages
// named "main" that declare a parameterless, resultless func main.
func mainPackages(pkgs []*packages.Package) []*packages.Package {
	var out []*packages.Package
	for _, p := range pkgs {
		if p.Name != "main" {
			continue
		}
		if p.Types == nil || p.Types.Scope().Lookup("main") == nil {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PkgPath < out[j].PkgPath })
	return out
}

// index gives O(1) lookup from a *types.Func to its declaring AST and
// owning package — needed to follow a RegisterXxxServer call into its
// body, and a constructor / start-method back to its declaration.
type index struct {
	// funcDecls maps a function/method object to its *ast.FuncDecl.
	funcDecls map[types.Object]*ast.FuncDecl
	// funcPkg maps a function/method object to its owning package.
	funcPkg map[types.Object]*packages.Package
	// varSpecs maps a package-level var object to its *ast.ValueSpec
	// and the index of the var within a grouped declaration.
	varSpecs map[types.Object]varSite
}

// varSite locates a package-level var's initialiser expression.
type varSite struct {
	pkg  *packages.Package
	spec *ast.ValueSpec
	// idx is the position of the var name within the ValueSpec, so the
	// matching initialiser expression is spec.Values[idx].
	idx int
}

// newIndex walks every package's syntax once and records declarations.
func newIndex(pkgs []*packages.Package) *index {
	idx := &index{
		funcDecls: map[types.Object]*ast.FuncDecl{},
		funcPkg:   map[types.Object]*packages.Package{},
		varSpecs:  map[types.Object]varSite{},
	}
	seen := map[*packages.Package]bool{}
	var visit func(p *packages.Package)
	visit = func(p *packages.Package) {
		if p == nil || seen[p] {
			return
		}
		seen[p] = true
		idx.addPackage(p)
		for _, imp := range p.Imports {
			visit(imp)
		}
	}
	for _, p := range pkgs {
		visit(p)
	}
	return idx
}

// addPackage records the func and var declarations of a single package.
func (idx *index) addPackage(p *packages.Package) {
	if p.TypesInfo == nil {
		return
	}
	for _, file := range p.Syntax {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if obj := p.TypesInfo.Defs[d.Name]; obj != nil {
					idx.funcDecls[obj] = d
					idx.funcPkg[obj] = p
				}
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if obj := p.TypesInfo.Defs[name]; obj != nil {
							idx.varSpecs[obj] = varSite{pkg: p, spec: vs, idx: i}
						}
					}
				}
			}
		}
	}
}
