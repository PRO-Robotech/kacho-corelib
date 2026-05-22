// Package reach builds, for each inventoried entry-point, the set of
// functions and source files reachable from it.
//
// # Algorithm
//
// reach.Compute lowers the loaded packages to SSA
// (golang.org/x/tools/go/ssa via ssautil), then runs Rapid Type
// Analysis (golang.org/x/tools/go/callgraph/rta) over a root set
// consisting of:
//
//   - every entry-point's root function/method (the gRPC handler method
//     or the worker Run/Start method discovered by the entrypoints
//     package), and
//   - every main function of the analyzed repository.
//
// Including the main functions is deliberate: a gRPC handler method is
// registered, not called, from main, so RTA would never see it called
// — and, crucially, would never see the concrete adapter types
// constructed by main's wiring code. Without those construction sites
// RTA cannot resolve interface ("invoke"-mode) dispatch to a concrete
// implementation. Seeding main alongside the handler roots makes the
// composition root's type construction visible, so a handler that calls
// a port interface reaches the concrete implementation.
//
// The per-entry-point reachable-set is then a breadth-first walk of the
// single RTA call-graph starting at that entry-point's root node.
//
// # RTA precision caveat
//
// RTA is type-based, not value-based: it over-approximates interface
// dispatch by linking every "invoke" call site to every method of every
// runtime type compatible with the interface. Conversely, calls made
// purely through reflection are not modelled and may be under-reported.
// Heavy interface fan-out or reflection therefore makes the
// reachable-set imprecise in both directions. Downstream dead-code
// detection (Task 6, C2) must treat a "reachable" verdict as sound-ish
// but a "dead" verdict as advisory: a false dead-code finding caused by
// a reflection-only call is suppressed with an `// archgraph:keep`
// marker, not by widening this analysis.
//
// # Determinism
//
// Every exported slice (ReachableSet.Funcs, ReachableSet.Files) is
// sorted by symbol / path name, never left in call-graph traversal or
// map-iteration order. Two Compute runs over byte-identical source
// therefore yield byte-identical reachable-sets — the property Task 7
// (freshness hashing) and Task 8 (call-tree generation) depend on.
//
// This package depends only on the standard library,
// golang.org/x/tools/go/{packages,ssa,ssa/ssautil,callgraph,
// callgraph/rta} and internal/archgraph/entrypoints. It imports no gRPC
// runtime and no persistence code.
package reach

import (
	"fmt"
	"go/types"
	"sort"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
)

// KeptRootKey is the canonical Graph.Sets key under which the
// reachable-set of a single `// archgraph:keep`-annotated root is
// stored. It is a synthetic key — distinct from any entry-point name —
// so a caller can tell entry-point reachable-sets from keep-root ones,
// and so Graph.Union (which folds every set) automatically counts the
// keep roots' transitive closure as reachable. The key embeds the kept
// root's canonical SSA name to stay unique and deterministic.
func KeptRootKey(ssaName string) string { return "archgraph:keep " + ssaName }

// MainRootKey is the canonical Graph.Sets key under which the
// reachable-set of a single main function is stored. A service's main
// is a reachability root in its own right: the composition-root wiring
// it runs — gRPC server bootstrap, adapter construction, the
// RegisterXxxServer calls — is live code, not dead code, even though no
// gRPC *handler* method calls it. Folding main into Graph.Union is what
// keeps C2 from flagging a repository's bootstrap as dead.
func MainRootKey(pkgPath string) string { return "main:" + pkgPath }

// ReachableSet is the deterministically ordered set of functions and
// source files reachable from one root (an entry-point, or the union of
// all of them).
type ReachableSet struct {
	// Funcs holds the canonical names of the reachable functions, sorted
	// ascending by name. A canonical name is *ssa.Function.String():
	// "pkg/path.Func" for a free function, "(*pkg/path.Type).Method" for
	// a method.
	Funcs []string
	// Files holds the paths of the source files declaring the reachable
	// functions, sorted ascending and de-duplicated. Synthetic functions
	// (compiler-generated wrappers, with no source position) contribute
	// no file.
	Files []string
}

// Graph is the reachability result for a repository: one ReachableSet
// per entry-point, keyed by the entry-point's canonical name.
type Graph struct {
	// Sets maps a reachability root to the ReachableSet computed from it.
	// A root is one of three kinds, distinguished by its key:
	//
	//   - an entry-point — keyed by its canonical name
	//     (entrypoints.EntryPoint.Name);
	//   - the process main — keyed by MainRootKey(pkgpath); a service's
	//     composition root and the code it wires up (server bootstrap,
	//     adapter construction) is live by virtue of being run;
	//   - an `// archgraph:keep`-annotated root — keyed by
	//     KeptRootKey(ssaName).
	//
	// Sets is empty for a library repository (no main, no entry-points).
	Sets map[string]ReachableSet
}

// Union returns the reachable-set that is the union of every root's
// reachable-set — entry-points, the process main and any kept roots: a
// function/file is in the union iff it is reachable from at least one
// of them. The result is deterministically sorted. For a library
// repository the union is empty. Union is the input to dead-code
// detection (Task 6, C2): an exported symbol absent from the union is a
// dead-code candidate.
func (g *Graph) Union() ReachableSet {
	funcs := map[string]struct{}{}
	files := map[string]struct{}{}
	for _, rs := range g.Sets {
		for _, f := range rs.Funcs {
			funcs[f] = struct{}{}
		}
		for _, f := range rs.Files {
			files[f] = struct{}{}
		}
	}
	return ReachableSet{Funcs: sortedKeys(funcs), Files: sortedKeys(files)}
}

// Compute lowers pkgs to SSA, runs RTA from the inventory's entry-points
// (plus the repository's main functions) and returns the per-entry-point
// reachability graph.
//
// A library repository — inv.IsLibraryRepo true, no entries — yields an
// empty Graph and a nil error: absence of entry-points is not a failure.
//
// Compute fails only on an internal SSA-mapping error: an inventoried
// entry-point root that cannot be lowered to an *ssa.Function (which
// would indicate a bug upstream in entry-point discovery, not a
// property of the analyzed code).
//
// Compute is the no-extra-roots case of ComputeWithRoots.
func Compute(pkgs []*packages.Package, inv *entrypoints.Inventory) (*Graph, error) {
	return ComputeWithRoots(pkgs, inv, nil)
}

// ComputeWithRoots is Compute extended with an additional set of
// reachability roots — the `// archgraph:keep`-annotated symbols the C2
// dead-code check (Task 6) treats as live entry-points in their own
// right.
//
// Each kept root is seeded into RTA alongside the entry-point roots and
// the repository's main functions, and its own BFS reachable-set is
// added to the returned Graph under the synthetic KeptRootKey(name)
// key. Because Graph.Union folds every set, a symbol transitively
// reachable from a kept root is automatically part of the union — that
// is how C2 keep-transitivity (scenario 4.0-F5) is realised: marking a
// function kept revives its whole reachable subtree.
//
// extra holds the *types.Func of each kept root. A nil or empty extra
// makes ComputeWithRoots behave exactly like Compute. A kept func that
// cannot be lowered to an *ssa.Function (a rare SSA-mapping edge case)
// is skipped silently: a keep annotation must never make the analysis
// itself fail.
func ComputeWithRoots(
	pkgs []*packages.Package,
	inv *entrypoints.Inventory,
	extra []*types.Func,
) (*Graph, error) {
	g, _, err := ComputeWithSymbols(pkgs, inv, extra, nil)
	return g, err
}

// ComputeWithSymbols is the full reachability entry point: it computes
// the graph exactly as ComputeWithRoots does, and additionally resolves
// a caller-supplied set of symbols to their canonical SSA names off the
// same single SSA build.
//
// symbols holds the *types.Func the caller wants canonical names for —
// the repository's exported functions and methods, for the C2 dead-code
// check. The returned map keys are exactly the resolvable entries of
// symbols; a *types.Func that does not lower to an *ssa.Function (an
// interface method, or a declaration the SSA builder elided) is absent
// from the map, so the caller can tell it apart from a resolved-but-dead
// symbol. The canonical name is *ssa.Function.String() — identical to
// the names populating ReachableSet.Funcs, so membership can be tested
// directly.
//
// Sharing one SSA build between the call-graph and the name resolver
// keeps the C2 check from lowering the program twice.
func ComputeWithSymbols(
	pkgs []*packages.Package,
	inv *entrypoints.Inventory,
	extra []*types.Func,
	symbols []*types.Func,
) (*Graph, map[*types.Func]string, error) {
	g := &Graph{Sets: map[string]ReachableSet{}}
	names := map[*types.Func]string{}
	if (inv == nil || len(inv.Entries) == 0) && len(extra) == 0 {
		// No reachability roots: empty graph. Symbols are still resolved
		// below only if a program is built — with no roots there is
		// nothing to analyze, so the names map stays empty too. This is
		// the library-repo path; C2 skips before reaching here.
		return g, names, nil
	}

	// Lower every initial package plus dependencies to SSA, then build
	// the bodies — RTA needs built function bodies to discover call
	// sites and constructed runtime types.
	prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	// Map each entry-point's *types.Func root to its *ssa.Function.
	var roots []*ssa.Function
	epRoots := map[string]*ssa.Function{}
	if inv != nil {
		epRoots = make(map[string]*ssa.Function, len(inv.Entries))
		for _, ep := range inv.Entries {
			if ep.Root == nil {
				return nil, nil, fmt.Errorf("reach: entry-point %q has a nil root func", ep.Name)
			}
			fn := prog.FuncValue(ep.Root)
			if fn == nil {
				return nil, nil, fmt.Errorf(
					"reach: cannot map entry-point %q root %s to an SSA function "+
						"(interface method or package not built)",
					ep.Name, ep.Root.FullName())
			}
			epRoots[ep.Name] = fn
			roots = append(roots, fn)
		}
	}

	// Map each kept root's *types.Func to its *ssa.Function. A keep
	// annotation must never break the analysis, so an unmappable kept
	// func is skipped rather than erroring.
	keptRoots := map[string]*ssa.Function{}
	for _, kf := range extra {
		if kf == nil {
			continue
		}
		fn := prog.FuncValue(kf)
		if fn == nil {
			continue
		}
		keptRoots[KeptRootKey(fn.String())] = fn
		roots = append(roots, fn)
	}

	// Also seed every main function so RTA sees the composition root's
	// wiring code — the construction sites of concrete adapter types
	// that make interface dispatch resolvable. mainFuncs is appended in
	// a deterministic order; RTA's result does not depend on root order,
	// but a stable order keeps debugging reproducible.
	mains := mainFuncs(prog)
	roots = append(roots, mains...)

	res := rta.Analyze(roots, true)

	// For each entry-point, each main function and each kept root, BFS
	// the RTA call-graph from its root node. main is a root of its own:
	// the bootstrap code it runs is live, not dead (so C2's union counts
	// it). epRoots and keptRoots cannot collide with the synthetic main
	// keys, so a plain map assignment is unambiguous.
	for name, fn := range epRoots {
		g.Sets[name] = reachableFrom(res.CallGraph, fn)
	}
	for _, fn := range mains {
		g.Sets[MainRootKey(fn.Pkg.Pkg.Path())] = reachableFrom(res.CallGraph, fn)
	}
	for key, fn := range keptRoots {
		g.Sets[key] = reachableFrom(res.CallGraph, fn)
	}

	// Resolve the caller's symbols to canonical SSA names off the same
	// build. An unmappable symbol is left out of the map.
	for _, sym := range symbols {
		if sym == nil {
			continue
		}
		if fn := prog.FuncValue(sym); fn != nil {
			names[sym] = fn.String()
		}
	}
	return g, names, nil
}

// reachableFrom walks cg breadth-first from the node of root and returns
// the deterministically sorted set of reachable functions and their
// source files. root itself is included. A root with no node in the
// graph (RTA found it unreachable and pruned it) yields a singleton set
// of root alone.
func reachableFrom(cg *callgraph.Graph, root *ssa.Function) ReachableSet {
	visited := map[*ssa.Function]struct{}{root: {}}

	start := cg.Nodes[root]
	if start != nil {
		queue := []*callgraph.Node{start}
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			// n.Out is an unordered set; iteration order does not affect
			// the final result because the visited set is unordered and
			// the output is sorted below.
			for _, e := range n.Out {
				callee := e.Callee
				if callee == nil || callee.Func == nil {
					continue
				}
				if _, seen := visited[callee.Func]; seen {
					continue
				}
				visited[callee.Func] = struct{}{}
				queue = append(queue, callee)
			}
		}
	}

	funcs := map[string]struct{}{}
	files := map[string]struct{}{}
	for fn := range visited {
		funcs[fn.String()] = struct{}{}
		if f := fileOf(fn); f != "" {
			files[f] = struct{}{}
		}
	}
	return ReachableSet{Funcs: sortedKeys(funcs), Files: sortedKeys(files)}
}

// fileOf returns the absolute path of the source file declaring fn, or
// "" when fn has no real source position — a synthetic wrapper, a
// shared function (such as error.Error) or a builtin.
func fileOf(fn *ssa.Function) string {
	if fn.Synthetic != "" {
		return ""
	}
	if fn.Prog == nil || fn.Prog.Fset == nil {
		return ""
	}
	pos := fn.Pos()
	if !pos.IsValid() {
		return ""
	}
	return fn.Prog.Fset.Position(pos).Filename
}

// mainFuncs returns every "main.main" function in prog, sorted by
// package path for a deterministic root order.
func mainFuncs(prog *ssa.Program) []*ssa.Function {
	var out []*ssa.Function
	for _, pkg := range prog.AllPackages() {
		if pkg == nil || pkg.Pkg == nil || pkg.Pkg.Name() != "main" {
			continue
		}
		if fn := pkg.Func("main"); fn != nil {
			out = append(out, fn)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].String() < out[j].String()
	})
	return out
}

// sortedKeys returns the keys of set as an ascending-sorted slice. It is
// the single choke-point that turns unordered map membership into
// deterministic output.
func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
