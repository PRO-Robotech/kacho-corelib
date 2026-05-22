package reach_test

// Integration tests for the call-graph reachability pass.
//
// Each test maps 1:1 to an acceptance scenario from group C of the
// sub-phase 4.0 acceptance document (archgraph call-graph + reachability
// via RTA). The scenario ID is encoded in the test name for
// traceability.
//
// Tests load synthetic fixture repositories from cmd/archgraph/testdata
// with golang.org/x/tools/go/packages, run entry-point discovery, then
// assert on the Graph returned by reach.Compute. No testcontainers, no
// network: every fixture is self-contained and pins go 1.21 so it loads
// under GOTOOLCHAIN=local.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/reach"
)

// loadFixture loads every package of the named testdata fixture.
func loadFixture(t *testing.T, name string) []*packages.Package {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", "..", "..", "cmd", "archgraph", "testdata", name))
	require.NoError(t, err)
	info, err := os.Stat(root)
	require.NoError(t, err, "fixture %s must exist", name)
	require.True(t, info.IsDir())

	cfg := &packages.Config{
		Mode: packages.NeedName |
			packages.NeedFiles |
			packages.NeedImports |
			packages.NeedDeps |
			packages.NeedTypes |
			packages.NeedSyntax |
			packages.NeedTypesInfo,
		Dir:   root,
		Tests: false,
		Env:   append(os.Environ(), "GOTOOLCHAIN=local"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	require.NoError(t, err, "loading fixture %s", name)
	for _, p := range pkgs {
		require.Empty(t, p.Errors, "fixture %s package %s must compile", name, p.PkgPath)
	}
	return pkgs
}

// computeFixture loads a fixture, discovers entry-points and computes
// the reachability graph.
func computeFixture(t *testing.T, name string) (*entrypoints.Inventory, *reach.Graph) {
	t.Helper()
	pkgs := loadFixture(t, name)
	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err)
	g, err := reach.Compute(pkgs, inv)
	require.NoError(t, err)
	require.NotNil(t, g)
	return inv, g
}

// reachableSetFor returns the ReachableSet of the entry-point whose
// canonical name is name, failing the test when there is no such entry.
func reachableSetFor(t *testing.T, g *reach.Graph, name string) reach.ReachableSet {
	t.Helper()
	rs, ok := g.Sets[name]
	require.True(t, ok, "graph must carry a reachable-set for entry-point %q", name)
	return rs
}

// containsFuncSuffix reports whether any func canonical name in rs ends
// with suffix — a tolerant match independent of the fixture module path.
func containsFuncSuffix(rs reach.ReachableSet, suffix string) bool {
	for _, f := range rs.Funcs {
		if strings.HasSuffix(f, suffix) {
			return true
		}
	}
	return false
}

// Test_4_0_C1_graph_ReachabilityFromEntryPoint: the reachable-set of the
// Create rpc entry-point includes validateSpec and repo.Insert, while
// unusedHelper is in no entry-point's reachable-set, and the result is
// deterministic.
func Test_4_0_C1_graph_ReachabilityFromEntryPoint(t *testing.T) {
	_, g := computeFixture(t, "reach-basic")

	const ep = "kacho.cloud.vpc.v1.NetworkService/Create"
	rs := reachableSetFor(t, g, ep)

	require.True(t, containsFuncSuffix(rs, ".validateSpec"),
		"validateSpec must be reachable from Create; got %v", rs.Funcs)
	require.True(t, containsFuncSuffix(rs, ".Insert"),
		"repo.Insert must be reachable from Create; got %v", rs.Funcs)
	require.True(t, containsFuncSuffix(rs, ".Create"),
		"the entry-point root Create must be in its own reachable-set; got %v", rs.Funcs)

	// unusedHelper is dead code: in no entry-point's reachable-set, and
	// not in the union either.
	for name, set := range g.Sets {
		require.False(t, containsFuncSuffix(set, ".unusedHelper"),
			"unusedHelper must not be reachable from entry-point %q", name)
	}
	require.False(t, containsFuncSuffix(g.Union(), ".unusedHelper"),
		"unusedHelper must not be in the union reachable-set")

	// Determinism: a second Compute over the same code yields an
	// identical reachable-set.
	pkgs := loadFixture(t, "reach-basic")
	inv2, err := entrypoints.Discover(pkgs)
	require.NoError(t, err)
	g2, err := reach.Compute(pkgs, inv2)
	require.NoError(t, err)
	require.Equal(t, g.Sets[ep], g2.Sets[ep],
		"reachable-set must be deterministic across runs")
}

// Test_4_0_C2_graph_InterfaceDispatchInReachableSet: an entry-point that
// calls a method through a port interface reaches the concrete
// implementation's method, because RTA accounts for the concrete type
// being constructed in reachable code.
func Test_4_0_C2_graph_InterfaceDispatchInReachableSet(t *testing.T) {
	_, g := computeFixture(t, "reach-interface")

	const ep = "kacho.cloud.vpc.v1.NetworkService/Create"
	rs := reachableSetFor(t, g, ep)

	require.True(t, containsFuncSuffix(rs, ".Create"),
		"the entry-point root must be in its own reachable-set; got %v", rs.Funcs)
	require.True(t, containsFuncSuffix(rs, ".Insert"),
		"the concrete pgNetworkRepo.Insert must be reachable through the "+
			"NetworkRepo interface dispatch; got %v", rs.Funcs)

	// The concrete implementation method is named (*pgNetworkRepo).Insert
	// in the repo package — assert the concrete receiver, not merely a
	// bare ".Insert" suffix.
	require.True(t, containsFuncSuffix(rs, "pgNetworkRepo).Insert"),
		"the reachable Insert must be the concrete pgNetworkRepo method; got %v", rs.Funcs)
}

// Test_4_0_C3_graph_DeterministicSerialization: the reachable-set is
// deterministically ordered (funcs and files sorted by symbol name) and
// two independent Compute runs over the same code yield identical sets.
func Test_4_0_C3_graph_DeterministicSerialization(t *testing.T) {
	_, g1 := computeFixture(t, "reach-basic")
	_, g2 := computeFixture(t, "reach-basic")

	require.Equal(t, g1.Sets, g2.Sets, "Graph.Sets must be deterministic")
	require.Equal(t, g1.Union(), g2.Union(), "Graph.Union must be deterministic")

	for name, rs := range g1.Sets {
		require.True(t, isSorted(rs.Funcs),
			"reachable funcs of %q must be sorted by symbol name: %v", name, rs.Funcs)
		require.True(t, isSorted(rs.Files),
			"reachable files of %q must be sorted: %v", name, rs.Files)
	}

	u := g1.Union()
	require.True(t, isSorted(u.Funcs), "union funcs must be sorted: %v", u.Funcs)
	require.True(t, isSorted(u.Files), "union files must be sorted: %v", u.Files)
}

// isSorted reports whether ss is in non-decreasing order.
func isSorted(ss []string) bool {
	for i := 1; i < len(ss); i++ {
		if ss[i-1] > ss[i] {
			return false
		}
	}
	return true
}

// Test_4_0_C_LibraryRepoEmptyGraph: a library repo (no entry-points)
// yields an empty graph, not an error.
func Test_4_0_C_LibraryRepoEmptyGraph(t *testing.T) {
	pkgs := loadFixture(t, "library-repo")
	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err)
	require.True(t, inv.IsLibraryRepo)

	g, err := reach.Compute(pkgs, inv)
	require.NoError(t, err)
	require.NotNil(t, g)
	require.Empty(t, g.Sets, "a library repo has no reachable-sets")
	require.Empty(t, g.Union().Funcs, "a library repo union is empty")
	require.Empty(t, g.Union().Files, "a library repo union is empty")
}
