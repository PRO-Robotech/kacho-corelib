package archtest_test

// Self-tests for the archtest fixture builder: a built repo must be a
// real, compilable Go module on disk so the archgraph passes that load it
// through go/packages see a clean graph.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/archtest"
)

// loadBuilt loads every package of a freshly built fixture and fails the
// test if anything does not compile.
func loadBuilt(t *testing.T, spec archtest.Spec) []*packages.Package {
	t.Helper()
	root := archtest.BuildRepo(t, spec)
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports |
			packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax |
			packages.NeedTypesInfo,
		Dir: root,
		Env: append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "./...")
	require.NoError(t, err)
	for _, p := range pkgs {
		require.Empty(t, p.Errors, "package %s must compile", p.PkgPath)
	}
	return pkgs
}

func TestBuildRepo_GRPCService(t *testing.T) {
	loadBuilt(t, archtest.Spec{
		Module: "example.com/at-grpc",
		Services: []archtest.ServiceSpec{{
			FQN:         "kacho.cloud.vpc.v1.NetworkService",
			Methods:     []string{"Create", "Get"},
			HandlerType: "NetworkHandler",
		}},
	})
}

func TestBuildRepo_Worker(t *testing.T) {
	loadBuilt(t, archtest.Spec{
		Module: "example.com/at-worker",
		Workers: []archtest.WorkerSpec{
			{Package: "worker", TypeName: "NetworkOutboxWorker", Method: "Run"},
		},
	})
}

func TestBuildRepo_LibraryAndFiles(t *testing.T) {
	loadBuilt(t, archtest.Spec{
		Module: "example.com/at-lib",
		NoMain: true,
		Files: map[string]string{
			"stringutil/stringutil.go": "package stringutil\n\n" +
				"// Reverse reverses s.\nfunc Reverse(s string) string { return s }\n",
		},
	})
}

func TestBuildRepo_NotesWritten(t *testing.T) {
	root := archtest.BuildRepo(t, archtest.Spec{
		Module: "example.com/at-notes",
		Services: []archtest.ServiceSpec{{
			FQN:     "kacho.cloud.vpc.v1.NetworkService",
			Methods: []string{"Create"},
		}},
		Notes: []archtest.NoteSpec{{
			File: "network-lifecycle.md", Repo: "atnotes",
			RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		}},
	})
	data, err := os.ReadFile(filepath.Join(root, "docs", "arch", "network-lifecycle.md"))
	require.NoError(t, err)
	require.Contains(t, string(data), "level: functionality")
	require.Contains(t, string(data), "rpc: kacho.cloud.vpc.v1.NetworkService/Create")
}

// TestBuildRepo_ProtoModuleSibling: a Spec carrying ProtoFiles materialises
// a synthetic kacho-proto sibling module with the .proto files under
// proto/, and the fixture repo's go.mod replaces kacho-proto with that
// sibling — the layout archgraph's proto-contract extractor resolves.
func TestBuildRepo_ProtoModuleSibling(t *testing.T) {
	root := archtest.BuildRepo(t, archtest.Spec{
		Module: "example.com/at-proto",
		Services: []archtest.ServiceSpec{{
			FQN:     "kacho.cloud.vpc.v1.NetworkService",
			Methods: []string{"Create"},
		}},
		ProtoFiles: map[string]string{
			"kacho/cloud/vpc/v1/network_service.proto": "syntax = \"proto3\";\n" +
				"package kacho.cloud.vpc.v1;\n" +
				"service NetworkService {\n" +
				"  rpc Create (CreateNetworkRequest) returns (Operation);\n" +
				"}\n",
		},
	})

	// The fixture's go.mod must replace kacho-proto with the sibling.
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	require.NoError(t, err)
	require.Contains(t, string(goMod), "github.com/PRO-Robotech/kacho-proto",
		"a ProtoFiles fixture must require kacho-proto")
	require.Contains(t, string(goMod), "replace github.com/PRO-Robotech/kacho-proto",
		"a ProtoFiles fixture must replace kacho-proto with the synthetic sibling")

	// The synthetic kacho-proto sibling must carry the .proto file under
	// proto/, reachable from the fixture root via the replace target.
	protoDir, ok := archtest.ProtoModuleDir(root)
	require.True(t, ok, "the synthetic kacho-proto sibling must be resolvable")
	protoFile := filepath.Join(protoDir, "kacho", "cloud", "vpc", "v1", "network_service.proto")
	data, err := os.ReadFile(protoFile)
	require.NoError(t, err, "the synthetic .proto file must exist under proto/")
	require.Contains(t, string(data), "service NetworkService")
}
