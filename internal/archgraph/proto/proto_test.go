package proto_test

// Unit tests for archgraph's proto-contract extractor.
//
// The extractor reads the .proto files of the kacho-proto module a service
// repository depends on and recovers, per gRPC rpc, the contract archgraph's
// L3 "RPC contract" table renders: the fully-qualified method name
// (<package>.<Service>/<Method>), the request and response message types,
// and — when the rpc carries a (google.api.http) annotation — its REST verb
// and path.
//
// Fixtures are synthetic .proto files written into t.TempDir(); no network,
// no checked-in proto, no go-protoparser dependency leaks into the test.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/proto"
)

// writeProto materialises one .proto file under dir at the given
// repo-relative slash path and returns dir for chaining.
func writeProto(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
	require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
}

// networkServiceProto is a synthetic kacho-proto service file: a public
// NetworkService with a REST-annotated async Create and sync Get, and an
// InternalNetworkService whose UpdateStatus carries no (google.api.http)
// annotation — the Internal-service shape that produces an empty REST cell.
const networkServiceProto = `syntax = "proto3";

package kacho.cloud.vpc.v1;

import "google/api/annotations.proto";

service NetworkService {
  rpc Create (CreateNetworkRequest) returns (operation.Operation) {
    option (google.api.http) = { post: "/vpc/v1/networks" body: "*" };
  }
  rpc Get (GetNetworkRequest) returns (Network) {
    option (google.api.http) = { get: "/vpc/v1/networks/{network_id}" };
  }
  rpc Update (UpdateNetworkRequest) returns (operation.Operation) {
    option (google.api.http) = {
      patch: "/vpc/v1/networks/{network_id}"
      body: "network"
    };
  }
}

service InternalNetworkService {
  rpc UpdateStatus (UpdateStatusRequest) returns (UpdateStatusResponse);
}
`

// Test_proto_ParseExtractsContract: the extractor recovers, per rpc, the
// fully-qualified method name, the request and response message types and
// the REST verb + path of every (google.api.http)-annotated method.
func Test_proto_ParseExtractsContract(t *testing.T) {
	dir := t.TempDir()
	writeProto(t, dir, "kacho/cloud/vpc/v1/network_service.proto", networkServiceProto)

	rpcs, err := proto.Extract(dir)
	require.NoError(t, err, "Extract over a well-formed proto tree must succeed")

	byFQN := map[string]proto.RPC{}
	for _, r := range rpcs {
		byFQN[r.FQN] = r
	}

	create, ok := byFQN["kacho.cloud.vpc.v1.NetworkService/Create"]
	require.True(t, ok, "Create rpc must be extracted; got %v", keys(byFQN))
	require.Equal(t, "CreateNetworkRequest", create.Request)
	require.Equal(t, "operation.Operation", create.Response)
	require.Equal(t, "POST", create.RESTVerb)
	require.Equal(t, "/vpc/v1/networks", create.RESTPath)

	get, ok := byFQN["kacho.cloud.vpc.v1.NetworkService/Get"]
	require.True(t, ok, "Get rpc must be extracted")
	require.Equal(t, "GetNetworkRequest", get.Request)
	require.Equal(t, "Network", get.Response)
	require.Equal(t, "GET", get.RESTVerb)
	require.Equal(t, "/vpc/v1/networks/{network_id}", get.RESTPath)

	upd, ok := byFQN["kacho.cloud.vpc.v1.NetworkService/Update"]
	require.True(t, ok, "Update rpc must be extracted")
	require.Equal(t, "PATCH", upd.RESTVerb, "a multi-line http option must still resolve the verb")
	require.Equal(t, "/vpc/v1/networks/{network_id}", upd.RESTPath)
}

// Test_proto_ParseRPCWithoutHTTPAnnotation: an rpc with no (google.api.http)
// option — the Internal-service shape — yields empty REST verb and path,
// never an error.
func Test_proto_ParseRPCWithoutHTTPAnnotation(t *testing.T) {
	dir := t.TempDir()
	writeProto(t, dir, "kacho/cloud/vpc/v1/network_service.proto", networkServiceProto)

	rpcs, err := proto.Extract(dir)
	require.NoError(t, err)

	var found bool
	for _, r := range rpcs {
		if r.FQN != "kacho.cloud.vpc.v1.InternalNetworkService/UpdateStatus" {
			continue
		}
		found = true
		require.Equal(t, "UpdateStatusRequest", r.Request)
		require.Equal(t, "UpdateStatusResponse", r.Response)
		require.Empty(t, r.RESTVerb,
			"an rpc with no (google.api.http) annotation must have an empty REST verb")
		require.Empty(t, r.RESTPath,
			"an rpc with no (google.api.http) annotation must have an empty REST path")
	}
	require.True(t, found, "InternalNetworkService/UpdateStatus must be extracted")
}

// Test_proto_ExtractEmptyOnMissingDir: Extract over a directory that does
// not exist (no proto module resolved) returns an empty result and no
// error — the graceful path for a repo without a kacho-proto dependency.
func Test_proto_ExtractEmptyOnMissingDir(t *testing.T) {
	rpcs, err := proto.Extract(filepath.Join(t.TempDir(), "does-not-exist"))
	require.NoError(t, err, "Extract over an absent proto root must not error")
	require.Empty(t, rpcs, "Extract over an absent proto root must yield no RPCs")
}

// Test_proto_ResolveProtoDir locates the kacho-proto module's proto/
// directory from a service repo's go.mod replace directive.
func Test_proto_ResolveProtoDir(t *testing.T) {
	root := t.TempDir()

	// A synthetic kacho-proto sibling carrying a proto/ tree.
	protoMod := filepath.Join(root, "kacho-proto")
	writeProto(t, protoMod, "proto/kacho/cloud/vpc/v1/network_service.proto", networkServiceProto)

	// The service repo, whose go.mod replaces kacho-proto with ../kacho-proto.
	svc := filepath.Join(root, "kacho-vpc")
	require.NoError(t, os.MkdirAll(svc, 0o755))
	goMod := "module github.com/PRO-Robotech/kacho-vpc\n\n" +
		"go 1.25\n\n" +
		"require github.com/PRO-Robotech/kacho-proto v0.0.0\n\n" +
		"replace github.com/PRO-Robotech/kacho-proto => ../kacho-proto\n"
	require.NoError(t, os.WriteFile(filepath.Join(svc, "go.mod"), []byte(goMod), 0o644))

	dir, ok := proto.ResolveProtoDir(svc)
	require.True(t, ok, "ResolveProtoDir must locate the replaced kacho-proto proto/ tree")

	rpcs, err := proto.Extract(dir)
	require.NoError(t, err)
	require.NotEmpty(t, rpcs, "the resolved proto/ tree must yield RPCs")
}

// Test_proto_ResolveProtoDirNoReplace: a service repo with no kacho-proto
// replace directive resolves no proto directory — the graceful "no proto
// module" path.
func Test_proto_ResolveProtoDirNoReplace(t *testing.T) {
	svc := t.TempDir()
	goMod := "module example.com/svc\n\ngo 1.25\n"
	require.NoError(t, os.WriteFile(filepath.Join(svc, "go.mod"), []byte(goMod), 0o644))

	_, ok := proto.ResolveProtoDir(svc)
	require.False(t, ok, "a repo without a kacho-proto replace must resolve no proto dir")
}

// keys returns the keys of an FQN map, for diagnostics.
func keys(m map[string]proto.RPC) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
