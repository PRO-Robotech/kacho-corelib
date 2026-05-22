package entrypoints_test

// Integration tests for the entry-point inventory.
//
// Each test maps 1:1 to an acceptance scenario from group B of the
// sub-phase 4.0 acceptance document (archgraph entry-point inventory).
// The scenario ID is encoded in the test name for traceability.
//
// Fixtures are synthesised on the fly by archtest.BuildRepo into
// t.TempDir() and loaded with golang.org/x/tools/go/packages; tests
// assert on the Inventory returned by Discover. No testcontainers, no
// network: every fixture is self-contained and pins go 1.21 so it loads
// under GOTOOLCHAIN=local.

import (
	"go/types"
	"os"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/archtest"
	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/entrypoints"
)

// loadFixture builds the given fixture Spec into a fresh temp module and
// loads every package, asserting the module compiles cleanly.
func loadFixture(t *testing.T, spec archtest.Spec) []*packages.Package {
	t.Helper()

	root := archtest.BuildRepo(t, spec)

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
	require.NoError(t, err, "loading fixture %s", spec.Module)
	for _, p := range pkgs {
		require.Empty(t, p.Errors, "fixture %s package %s must compile", spec.Module, p.PkgPath)
	}
	return pkgs
}

// names returns the sorted canonical names of the inventory entries
// matching kind.
func names(inv *entrypoints.Inventory, kind entrypoints.Kind) []string {
	var out []string
	for _, ep := range inv.Entries {
		if ep.Kind == kind {
			out = append(out, ep.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Test_4_0_B1_GRPCMethodsByRegister: a main package with one
// RegisterNetworkServiceServer call yields exactly the four FQN-named
// rpc entry-points, each canonically "<proto-FQN>/<method>".
func Test_4_0_B1_GRPCMethodsByRegister(t *testing.T) {
	pkgs := loadFixture(t, archtest.SpecGRPCBasic())

	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err)
	require.False(t, inv.IsLibraryRepo, "a repo with a main package is not a library")

	require.Equal(t, []string{
		"kacho.cloud.vpc.v1.NetworkService/Create",
		"kacho.cloud.vpc.v1.NetworkService/Delete",
		"kacho.cloud.vpc.v1.NetworkService/Get",
		"kacho.cloud.vpc.v1.NetworkService/List",
	}, names(inv, entrypoints.KindRPC))

	// No workers, no unrecognised-worker hints in a pure-gRPC repo.
	require.Empty(t, names(inv, entrypoints.KindWorker))
	require.Empty(t, inv.Hints)

	// Every rpc entry-point carries a non-nil root *types.Func — the
	// reachability pass (Task 4) walks the call-graph from it.
	for _, ep := range inv.Entries {
		require.NotNil(t, ep.Root, "entry-point %s must carry a root func", ep.Name)
	}
}

// rpcRoot returns the root *types.Func of the rpc entry-point with the
// given canonical name, failing the test when no such entry exists.
func rpcRoot(t *testing.T, inv *entrypoints.Inventory, name string) *types.Func {
	t.Helper()
	for _, ep := range inv.Entries {
		if ep.Kind == entrypoints.KindRPC && ep.Name == name {
			require.NotNil(t, ep.Root, "rpc entry-point %s must carry a root", name)
			return ep.Root
		}
	}
	t.Fatalf("no rpc entry-point named %q in inventory", name)
	return nil
}

// receiverTypeName returns the bare name of fn's receiver named type
// ("NetworkHandler" for `func (h *NetworkHandler) Create()`), or "" when
// fn is not a method.
func receiverTypeName(fn *types.Func) string {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return ""
	}
	t := sig.Recv().Type()
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	return ""
}

// Test_4_0_B1_RPCRootIsHandlerMethod: the reachability root of an rpc
// entry-point must be the concrete handler type's method (e.g.
// (*NetworkHandler).Create), NOT the RegisterXxxServer plumbing
// function. Task 4 walks the call-graph from Root for dead-code (C2)
// analysis — rooting at RegisterNetworkServiceServer would reach
// registration plumbing instead of the handler's business logic.
func Test_4_0_B1_RPCRootIsHandlerMethod(t *testing.T) {
	pkgs := loadFixture(t, archtest.SpecGRPCBasic())

	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err)

	for _, method := range []string{"Create", "Get", "List", "Delete"} {
		root := rpcRoot(t, inv,
			"kacho.cloud.vpc.v1.NetworkService/"+method)
		require.Equal(t, method, root.Name(),
			"Root must be the handler method %q, not RegisterNetworkServiceServer", method)
		require.Equal(t, "NetworkHandler", receiverTypeName(root),
			"Root must be a method of the concrete handler type, "+
				"not the package-level RegisterXxxServer function")
	}
}

// Test_4_0_B2_RPCRootIsHandlerMethodMulti: with two RegisterXxxServer
// calls handing two distinct concrete handlers, each rpc entry-point's
// Root resolves to a method of its own handler type.
func Test_4_0_B2_RPCRootIsHandlerMethodMulti(t *testing.T) {
	pkgs := loadFixture(t, archtest.SpecGRPCMulti())

	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err)

	cases := map[string]struct{ method, recv string }{
		"kacho.cloud.vpc.v1.NetworkService/Create":              {"Create", "NetworkHandler"},
		"kacho.cloud.vpc.v1.NetworkService/Get":                 {"Get", "NetworkHandler"},
		"kacho.cloud.vpc.v1.InternalNetworkService/ReportState": {"ReportState", "InternalNetworkHandler"},
		"kacho.cloud.vpc.v1.InternalNetworkService/Sweep":       {"Sweep", "InternalNetworkHandler"},
	}
	for name, want := range cases {
		root := rpcRoot(t, inv, name)
		require.Equal(t, want.method, root.Name(), "Root method for %s", name)
		require.Equal(t, want.recv, receiverTypeName(root),
			"Root receiver type for %s", name)
	}
}

// Test_4_0_B2_MultipleRegisterInOneMain: two RegisterXxxServer calls in
// one main yield entry-points for both services, FQN-prefixed
// independently, with no merge or loss.
func Test_4_0_B2_MultipleRegisterInOneMain(t *testing.T) {
	pkgs := loadFixture(t, archtest.SpecGRPCMulti())

	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err)
	require.False(t, inv.IsLibraryRepo)

	require.Equal(t, []string{
		"kacho.cloud.vpc.v1.InternalNetworkService/ReportState",
		"kacho.cloud.vpc.v1.InternalNetworkService/Sweep",
		"kacho.cloud.vpc.v1.NetworkService/Create",
		"kacho.cloud.vpc.v1.NetworkService/Get",
	}, names(inv, entrypoints.KindRPC))
}

// Test_4_0_B3_WorkerByConstructorConvention: New<Name>(...) + go
// w.Run(ctx) is inventoried as a worker named after the worker type.
func Test_4_0_B3_WorkerByConstructorConvention(t *testing.T) {
	pkgs := loadFixture(t, archtest.SpecWorker())

	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err)
	require.False(t, inv.IsLibraryRepo)

	require.Equal(t, []string{"NetworkOutboxWorker"}, names(inv, entrypoints.KindWorker))
	require.Empty(t, names(inv, entrypoints.KindRPC))

	// The worker's root is the goroutine method (Run).
	var found bool
	for _, ep := range inv.Entries {
		if ep.Kind == entrypoints.KindWorker && ep.Name == "NetworkOutboxWorker" {
			found = true
			require.NotNil(t, ep.Root)
			require.Equal(t, "Run", ep.Root.Name())
		}
	}
	require.True(t, found)
}

// Test_4_0_B4_ReconcilerAndCron: a reconciler and a cron job, both via
// the chained New<Name>(...).Run/Start convention, are both
// inventoried as workers, each rooted at the method handed to the
// goroutine.
func Test_4_0_B4_ReconcilerAndCron(t *testing.T) {
	pkgs := loadFixture(t, archtest.SpecReconcilerCron())

	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err)

	require.Equal(t, []string{"InstanceReconciler", "SnapshotCronJob"},
		names(inv, entrypoints.KindWorker))

	roots := map[string]string{} // worker name -> root method name
	for _, ep := range inv.Entries {
		if ep.Kind == entrypoints.KindWorker {
			require.NotNil(t, ep.Root, "worker %s must carry a root", ep.Name)
			roots[ep.Name] = ep.Root.Name()
		}
	}
	require.Equal(t, "Run", roots["InstanceReconciler"])
	require.Equal(t, "Start", roots["SnapshotCronJob"])
}

// Test_4_0_B5_NonConventionalWorkerNotRecognised: a worker started
// without the New<Name> + Run/Start convention is NOT inventoried, but
// a hint is collected pointing at the goroutine site.
func Test_4_0_B5_NonConventionalWorkerNotRecognised(t *testing.T) {
	pkgs := loadFixture(t, archtest.SpecWorkerNonConventional())

	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err)

	require.Empty(t, inv.Entries, "non-conventional worker must not be inventoried")
	require.False(t, inv.IsLibraryRepo, "a repo with a main package is not a library")

	require.Len(t, inv.Hints, 1, "exactly one unrecognised-worker hint expected")
	h := inv.Hints[0]
	require.Contains(t, h.Message, "possible unrecognized worker entry-point")
	require.Contains(t, h.Message, "archgraph entry-point convention")
	// The hint must name a file:line position.
	require.Contains(t, h.Message, "main.go:")
}

// Test_4_0_B6_RepoWithoutEntryPoints: a library repo (no main package)
// yields an empty inventory flagged as a library.
func Test_4_0_B6_RepoWithoutEntryPoints(t *testing.T) {
	pkgs := loadFixture(t, archtest.SpecLibraryRepo())

	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err)

	require.Empty(t, inv.Entries)
	require.Empty(t, inv.Hints)
	require.True(t, inv.IsLibraryRepo, "a repo with no main package is a library")
}

// Test_4_0_B7_FQNResolutionFailure: a gRPC stub whose ServiceDesc has
// no ServiceName string literal cannot be FQN-resolved; Discover fails
// with the precise B7 error.
func Test_4_0_B7_FQNResolutionFailure(t *testing.T) {
	pkgs := loadFixture(t, archtest.SpecGRPCNoServiceName())

	inv, err := entrypoints.Discover(pkgs)
	require.Error(t, err)
	require.Nil(t, inv)
	require.ErrorContains(t, err,
		"cannot resolve service FQN for RegisterNetworkServiceServer: "+
			"no ServiceName literal in ServiceDesc")
}

// Test_4_0_B7_FQNViaServiceDesc: a positive check that the FQN comes
// from ServiceDesc.ServiceName, not from the RegisterXxxServer
// identifier — the basic fixture's identifier ("NetworkService") would
// give a bare name; only the descriptor yields the dotted proto FQN.
func Test_4_0_B7_FQNViaServiceDesc(t *testing.T) {
	pkgs := loadFixture(t, archtest.SpecGRPCBasic())

	inv, err := entrypoints.Discover(pkgs)
	require.NoError(t, err)

	for _, ep := range inv.Entries {
		require.Contains(t, ep.Name, "kacho.cloud.vpc.v1.NetworkService/",
			"FQN must be the dotted proto service name from ServiceDesc")
	}
}
