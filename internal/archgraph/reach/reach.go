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
	"sort"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
)

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
	// Sets maps an entry-point canonical name (entrypoints.EntryPoint.Name)
	// to the ReachableSet computed from that entry-point's root. It is
	// empty for a library repository (no entry-points).
	Sets map[string]ReachableSet
}

// Union returns the reachable-set that is the union of every
// entry-point's reachable-set: a function/file is in the union iff it
// is reachable from at least one entry-point. The result is
// deterministically sorted. For a library repository the union is
// empty. Union is the input to dead-code detection (Task 6, C2): an
// exported symbol absent from the union is a dead-code candidate.
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
func Compute(pkgs []*packages.Package, inv *entrypoints.Inventory) (*Graph, error) {
	g := &Graph{Sets: map[string]ReachableSet{}}
	if inv == nil || len(inv.Entries) == 0 {
		// Library repo (or nothing to analyze): empty graph, not an error.
		return g, nil
	}

	// Lower every initial package plus dependencies to SSA, then build
	// the bodies — RTA needs built function bodies to discover call
	// sites and constructed runtime types.
	prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	// Map each entry-point's *types.Func root to its *ssa.Function.
	roots := make([]*ssa.Function, 0, len(inv.Entries))
	epRoots := make(map[string]*ssa.Function, len(inv.Entries))
	for _, ep := range inv.Entries {
		if ep.Root == nil {
			return nil, fmt.Errorf("reach: entry-point %q has a nil root func", ep.Name)
		}
		fn := prog.FuncValue(ep.Root)
		if fn == nil {
			return nil, fmt.Errorf(
				"reach: cannot map entry-point %q root %s to an SSA function "+
					"(interface method or package not built)",
				ep.Name, ep.Root.FullName())
		}
		epRoots[ep.Name] = fn
		roots = append(roots, fn)
	}

	// Also seed every main function so RTA sees the composition root's
	// wiring code — the construction sites of concrete adapter types
	// that make interface dispatch resolvable. mainFuncs is appended in
	// a deterministic order; RTA's result does not depend on root order,
	// but a stable order keeps debugging reproducible.
	roots = append(roots, mainFuncs(prog)...)

	res := rta.Analyze(roots, true)

	// For each entry-point, BFS the RTA call-graph from its root node.
	for name, fn := range epRoots {
		g.Sets[name] = reachableFrom(res.CallGraph, fn)
	}
	return g, nil
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
