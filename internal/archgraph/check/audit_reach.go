package check

import (
	"fmt"
	"go/types"

	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/reach"
)

// AuditReach is the single reachability build shared by the C2
// dead-code and C3 freshness checks of one arch-audit run.
//
// # Why it exists
//
// C2 and C3 both depend on the SSA-lowered reachability graph. Before
// this type each check lowered the program to SSA and ran RTA on its
// own — two full builds per audit run. AuditReach lowers the program
// exactly once: it collects C2's exported-symbol set, runs
// reach.ComputeWithSymbols (one SSA build, one RTA, kept-root seeding
// and symbol-name resolution off the same build) and caches the graph
// plus everything C2's verdict step needs.
//
// audit.Run builds one AuditReach and feeds it to CheckC2WithReach and
// to CheckC3 (via the Graph field). The graph carries the entry-point,
// process-main and kept-root reachable-sets; C3's anchor lookups key on
// entry-point symbols, which never collide with the synthetic kept-root
// keys, so the one graph is correct for both checks.
//
// AuditReach is a value object: build it with BuildAuditReach, then
// only read it.
type AuditReach struct {
	// Graph is the reachability graph — entry-point, process-main and
	// kept-root reachable-sets. It is the input to both C2 (its Union
	// is the live set) and C3 (its per-anchor sets are hashed).
	Graph *reach.Graph

	// isLibrary records that the repository is a library (no main, no
	// entry-points). C2 then yields a passing SKIP.
	isLibrary bool

	// c2Symbols are the repository's exported functions and methods —
	// C2's dead-code candidates.
	c2Symbols []exportedSymbol

	// c2Invalid are the bare-`archgraph:keep` invalid-annotation
	// findings discovered while collecting symbols.
	c2Invalid []Finding

	// symbolNames maps each exported symbol's *types.Func to its
	// canonical SSA name, resolved off the same SSA build as Graph, so
	// a symbol's liveness can be tested against Graph.Union directly.
	symbolNames map[*types.Func]string
}

// BuildAuditReach performs the single shared reachability build of an
// arch-audit run. It collects the repository's exported functions and
// methods, lowers the program to SSA once, runs RTA seeded with the
// entry-points, the process main and the `archgraph:keep` roots, and
// resolves every exported symbol to its canonical SSA name off that one
// build.
//
// A library repository (inv.IsLibraryRepo) needs no build: the returned
// AuditReach carries an empty graph and isLibrary=true, and CheckC2WithReach
// turns it into the passing SKIP verdict.
//
// repoRoot is the repository's module root; pkgs its loaded packages;
// inv its entry-point inventory. BuildAuditReach returns an error only
// on an internal reachability-analysis failure.
func BuildAuditReach(
	repoRoot string,
	pkgs []*packages.Package,
	inv *entrypoints.Inventory,
) (*AuditReach, error) {
	if inv != nil && inv.IsLibraryRepo {
		return &AuditReach{Graph: &reach.Graph{}, isLibrary: true}, nil
	}

	// Enumerate the repository's exported functions and methods and the
	// archgraph:keep annotations attached to them — C2's candidate set
	// and its extra reachability roots.
	syms, keptFuncs, invalid := collectExportedSymbols(repoRoot, pkgs)

	allFuncs := make([]*types.Func, 0, len(syms))
	for _, s := range syms {
		allFuncs = append(allFuncs, s.fn)
	}

	// One SSA lowering + one RTA: the graph (entry-points + main + kept
	// roots) and the symbol-name resolution come off the same build.
	g, names, err := reach.ComputeWithSymbols(pkgs, inv, keptFuncs, allFuncs)
	if err != nil {
		return nil, fmt.Errorf("C2 dead-code: reachability analysis failed: %w", err)
	}

	return &AuditReach{
		Graph:       g,
		c2Symbols:   syms,
		c2Invalid:   invalid,
		symbolNames: names,
	}, nil
}
