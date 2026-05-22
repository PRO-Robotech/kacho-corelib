package archtest

import "strings"

// arch-gen fixtures (group D): a gRPC service with a reachable call chain
// of exported functions, an L2 note anchoring the entry-point, and a
// domain package carrying exported types, fields and constants — the
// inputs arch-gen's L3 (per-functionality call-tree + signatures) and L4
// (per-repo type/field/const tables) artifacts are derived from.
//
// The fixtures opt the service out of the default handler (NoHandler) and
// supply the handler / domain packages verbatim through Files, so the
// reachable call chain and the L4 domain surface are hand-tailored.

// genHandlerSource is the NetworkService handler of the arch-gen
// fixtures: Create reaches the exported domain.ValidateSpec and the
// exported repo.Insert, so an L3 artifact built from the Create anchor
// must list both signatures.
const genHandlerSource = `// Package handler implements the NetworkService gRPC handler of the
// arch-gen fixture.
//
// The call chain arch-gen's L3 artifact must recover from the Create
// anchor:
//
//	(*Handler).Create -> domain.ValidateSpec -> repo.Insert -> repo.encodeRow
package handler

import (
	"example.com/genbasic/internal/domain"
	"example.com/genbasic/internal/repo"
)

// Handler implements pb.NetworkServiceServer.
type Handler struct {
	repo *repo.NetworkRepo
}

// New returns a fresh Handler wired to a repo.
func New() *Handler { return &Handler{repo: repo.New()} }

// Create handles the Create RPC. It is the rpc entry-point root.
func (h *Handler) Create() {
	domain.ValidateSpec(domain.Network{})
	h.repo.Insert()
}
`

// genDomainSource is the domain package of the arch-gen fixture: it
// carries the exported types, struct fields and constants arch-gen's L4
// artifact tabulates, plus the exported ValidateSpec reachable from the
// handler (an L3 signature).
const genDomainSource = `// Package domain holds the domain entities of the arch-gen fixture.
package domain

// State enumerates the lifecycle states of a Network.
type State string

const (
	// StateProvisioning is the initial state of a Network.
	StateProvisioning State = "PROVISIONING"
	// StateActive marks a fully provisioned Network.
	StateActive State = "ACTIVE"
)

// MaxSubnets is the upper bound on subnets per Network.
const MaxSubnets = 64

// Network is the domain entity for a VPC network.
type Network struct {
	// ID is the network identifier.
	ID string
	// Name is the human-readable network name.
	Name string
	// State is the lifecycle state.
	State State
}

// ValidateSpec validates a Network spec. Reachable from the Create RPC,
// so an L3 artifact built from the Create anchor lists its signature.
func ValidateSpec(n Network) error { return nil }
`

// genRepoSource is the persistence adapter of the arch-gen fixture:
// Insert is exported and reachable from Create (an L3 signature),
// encodeRow is a private transitive callee (a call-tree node).
const genRepoSource = `// Package repo is the persistence adapter of the arch-gen fixture.
package repo

// NetworkRepo persists Network rows.
type NetworkRepo struct{}

// New returns a fresh NetworkRepo.
func New() *NetworkRepo { return &NetworkRepo{} }

// Insert writes a Network row. Reachable from Create.
func (r *NetworkRepo) Insert() {
	r.encodeRow()
}

// encodeRow is a private helper reachable transitively from Insert.
func (r *NetworkRepo) encodeRow() {}
`

// SpecGenBasic is the standard arch-gen fixture (group D, scenarios
// D1/D2/D3/D4/D5/D6): one NetworkService with a Create RPC anchored by a
// single L2 note, a reachable chain of exported functions and a domain
// package with exported types, fields and constants. D3 mutates the
// handler via GenHandlerSourceWithEnrich to assert the artifact updates.
func SpecGenBasic() Spec {
	s := grpcServiceBase("example.com/genbasic", []string{"Create"})
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "genbasic",
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		SourceSHA:  FreshSHA,
		Body: "# Network lifecycle\n\n" +
			"This curated L2 note anchors the NetworkService Create RPC. Its\n" +
			"narrative body is human-authored and arch-gen must never rewrite it.\n",
	}}
	s.Files = map[string]string{
		"internal/handler/handler.go": genHandlerSource,
		"internal/domain/domain.go":   genDomainSource,
		"internal/repo/repo.go":       genRepoSource,
	}
	// grpcServiceBase points main at internal/handler; the domain and repo
	// imports are pulled in transitively by the handler package.
	return s
}

// genHandlerSourceWithEnrich is genHandlerSource with an additional
// reachable exported function call — domain.EnrichSpec — exercising the
// D3 "new reachable exported function appears in its L3 artifact"
// scenario.
const genHandlerSourceWithEnrich = `// Package handler implements the NetworkService gRPC handler of the
// arch-gen fixture.
package handler

import (
	"example.com/genbasic/internal/domain"
	"example.com/genbasic/internal/repo"
)

// Handler implements pb.NetworkServiceServer.
type Handler struct {
	repo *repo.NetworkRepo
}

// New returns a fresh Handler wired to a repo.
func New() *Handler { return &Handler{repo: repo.New()} }

// Create handles the Create RPC. It is the rpc entry-point root.
func (h *Handler) Create() {
	domain.ValidateSpec(domain.Network{})
	domain.EnrichSpec(domain.Network{})
	h.repo.Insert()
}
`

// genDomainSourceWithEnrich is genDomainSource with the additional
// exported EnrichSpec function the D3 scenario adds.
const genDomainSourceWithEnrich = `// Package domain holds the domain entities of the arch-gen fixture.
package domain

// State enumerates the lifecycle states of a Network.
type State string

const (
	// StateProvisioning is the initial state of a Network.
	StateProvisioning State = "PROVISIONING"
	// StateActive marks a fully provisioned Network.
	StateActive State = "ACTIVE"
)

// MaxSubnets is the upper bound on subnets per Network.
const MaxSubnets = 64

// Network is the domain entity for a VPC network.
type Network struct {
	// ID is the network identifier.
	ID string
	// Name is the human-readable network name.
	Name string
	// State is the lifecycle state.
	State State
}

// ValidateSpec validates a Network spec. Reachable from the Create RPC,
// so an L3 artifact built from the Create anchor lists its signature.
func ValidateSpec(n Network) error { return nil }

// EnrichSpec augments a Network spec. Added by the D3 scenario as a new
// reachable exported function; its signature must appear in the L3
// artifact after arch-gen re-runs.
func EnrichSpec(n Network) Network { return n }
`

// GenHandlerSourceWithEnrich exposes the D3 post-change handler source to
// the gen test suite: a handler whose Create additionally calls the new
// exported domain.EnrichSpec.
func GenHandlerSourceWithEnrich() string { return genHandlerSourceWithEnrich }

// GenDomainSourceWithEnrich exposes the D3 post-change domain source to
// the gen test suite: the domain package extended with the new exported
// EnrichSpec function.
func GenDomainSourceWithEnrich() string { return genDomainSourceWithEnrich }

// genStdlibHandlerSource is the NetworkService handler of the
// stdlib-scoping arch-gen fixture (group D, scenario D7): Create reaches
// the in-repo domain.ValidateSpec and repo.Insert AND genuinely calls
// into the standard library — fmt.Sprintf with a %v verb, which RTA
// over-approximates into the reflect / strconv / sync stdlib packages.
// The anchor's reachable-set therefore contains GOROOT-resident source
// files and stdlib SSA function names (archive/tar.*, fmt.*,
// (*context.* …) on a real repo) — exactly the shape the real kacho-iam
// repo exhibits, the one that ballooned a single L3 artifact to 1.4 MB.
//
// It exists so D7 can assert that arch-gen scopes an L3 artifact's
// call-tree (and reachable exported signatures) to functions defined
// UNDER repoRoot: a call-tree that dumped the whole transitive
// reachable-set would render the entire Go standard library as
// documentation noise.
const genStdlibHandlerSource = `// Package handler implements the NetworkService gRPC handler of the
// stdlib-scoping arch-gen fixture (D7). Create reaches the in-repo
// domain.ValidateSpec and repo.Insert AND the standard library (fmt),
// so its reachable-set spans GOROOT source files and stdlib functions.
package handler

import (
	"fmt"

	"example.com/genstdlib/internal/domain"
	"example.com/genstdlib/internal/repo"
)

// Handler implements pb.NetworkServiceServer.
type Handler struct {
	repo *repo.NetworkRepo
}

// New returns a fresh Handler wired to a repo.
func New() *Handler { return &Handler{repo: repo.New()} }

// Create handles the Create RPC. It calls fmt.Sprintf — pulling stdlib
// functions into its reachable-set — then reaches the in-repo
// domain.ValidateSpec and repo.Insert.
func (h *Handler) Create() {
	_ = fmt.Sprintf("network %v created", h.repo)
	domain.ValidateSpec(domain.Network{})
	h.repo.Insert()
}
`

// genStdlibDomainSource is genDomainSource re-homed under the genstdlib
// module — the D7 fixture's domain package.
const genStdlibDomainSource = `// Package domain holds the domain entities of the stdlib-scoping
// arch-gen fixture (D7).
package domain

// State enumerates the lifecycle states of a Network.
type State string

const (
	// StateProvisioning is the initial state of a Network.
	StateProvisioning State = "PROVISIONING"
	// StateActive marks a fully provisioned Network.
	StateActive State = "ACTIVE"
)

// MaxSubnets is the upper bound on subnets per Network.
const MaxSubnets = 64

// Network is the domain entity for a VPC network.
type Network struct {
	// ID is the network identifier.
	ID string
	// Name is the human-readable network name.
	Name string
	// State is the lifecycle state.
	State State
}

// ValidateSpec validates a Network spec. Reachable from the Create RPC,
// so an L3 artifact built from the Create anchor lists its signature.
func ValidateSpec(n Network) error { return nil }
`

// genStdlibRepoSource is genRepoSource re-homed under the genstdlib
// module — the D7 fixture's persistence adapter.
const genStdlibRepoSource = `// Package repo is the persistence adapter of the stdlib-scoping
// arch-gen fixture (D7).
package repo

// NetworkRepo persists Network rows.
type NetworkRepo struct{}

// New returns a fresh NetworkRepo.
func New() *NetworkRepo { return &NetworkRepo{} }

// Insert writes a Network row. Reachable from Create.
func (r *NetworkRepo) Insert() {
	r.encodeRow()
}

// encodeRow is a private helper reachable transitively from Insert.
func (r *NetworkRepo) encodeRow() {}
`

// SpecGenStdlibReach is the stdlib-scoping arch-gen fixture (group D,
// scenario D7): a NetworkService whose Create handler reaches the
// in-repo domain / repo packages AND genuinely calls into the standard
// library (fmt.Sprintf with a %v verb). The anchor's reachable-set
// therefore spans GOROOT-resident stdlib functions, so the D7 test can
// assert arch-gen scopes the L3 call-tree to repo-defined functions only
// — never dumping the standard library into the artifact.
func SpecGenStdlibReach() Spec {
	s := grpcServiceBase("example.com/genstdlib", []string{"Create"})
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "genstdlib",
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		SourceSHA:  FreshSHA,
		Body: "# Network lifecycle\n\n" +
			"This curated L2 note anchors NetworkService/Create, whose handler\n" +
			"calls into the standard library — its reachable-set spans GOROOT\n" +
			"files. arch-gen's L3 call-tree must still list in-repo functions only.\n",
	}}
	s.Files = map[string]string{
		"internal/handler/handler.go": genStdlibHandlerSource,
		"internal/domain/domain.go":   genStdlibDomainSource,
		"internal/repo/repo.go":       genStdlibRepoSource,
	}
	return s
}

// genRPCContractProto is the synthetic kacho-proto NetworkService file for
// the RPC-contract arch-gen fixture: a REST-annotated Create whose verb /
// path / request / response shape the L3 "RPC contract" table must render.
const genRPCContractProto = `syntax = "proto3";

package kacho.cloud.vpc.v1;

import "google/api/annotations.proto";

// NetworkService is the public VPC network API.
service NetworkService {
  // Create provisions a network. Async — returns an Operation.
  rpc Create (CreateNetworkRequest) returns (operation.Operation) {
    option (google.api.http) = { post: "/vpc/v1/networks" body: "*" };
  }
  // Get reads a network. Sync.
  rpc Get (GetNetworkRequest) returns (Network) {
    option (google.api.http) = { get: "/vpc/v1/networks/{network_id}" };
  }
}
`

// SpecGenRPCContract is the RPC-contract arch-gen fixture: the standard
// NetworkService/Create functionality plus a synthetic kacho-proto module
// carrying the service's .proto. arch-gen's L3 artifact must add a
// `## RPC contract` section rowing the anchored Create RPC with its gRPC
// request/response shape and its REST verb + path.
func SpecGenRPCContract() Spec {
	s := grpcServiceBase("example.com/genrpccontract", []string{"Create"})
	s.Files = map[string]string{
		"internal/handler/handler.go": rehome(genHandlerSource, "genbasic", "genrpccontract"),
		"internal/domain/domain.go":   genDomainSource,
		"internal/repo/repo.go":       genRepoSource,
	}
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "genrpccontract",
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		SourceSHA:  FreshSHA,
		Body: "# Network lifecycle\n\n" +
			"This curated L2 note anchors NetworkService/Create. arch-gen must\n" +
			"add an RPC contract section recovered from the repo's kacho-proto.\n",
	}}
	s.ProtoFiles = map[string]string{
		"kacho/cloud/vpc/v1/network_service.proto": genRPCContractProto,
	}
	return s
}

// genRPCContractInternalProto is the synthetic kacho-proto file for the
// Internal-service RPC-contract fixture: an InternalNetworkService whose
// UpdateStatus carries no (google.api.http) annotation, so the L3 RPC
// contract table renders an em-dash REST cell for it.
const genRPCContractInternalProto = `syntax = "proto3";

package kacho.cloud.vpc.v1;

// InternalNetworkService is the cluster-internal VPC network API.
service InternalNetworkService {
  // UpdateStatus writes a network's status. No REST annotation — internal.
  rpc UpdateStatus (UpdateStatusRequest) returns (UpdateStatusResponse);
}
`

// genInternalHandlerSource is the InternalNetworkService handler of the
// RPC-contract-internal fixture: an UpdateStatus RPC root reaching the
// in-repo domain.ValidateSpec and repo.Insert.
const genInternalHandlerSource = `// Package handler implements the InternalNetworkService gRPC handler of
// the RPC-contract-internal arch-gen fixture.
package handler

import (
	"example.com/geninternal/internal/domain"
	"example.com/geninternal/internal/repo"
)

// Handler implements pb.InternalNetworkServiceServer.
type Handler struct {
	repo *repo.NetworkRepo
}

// New returns a fresh Handler wired to a repo.
func New() *Handler { return &Handler{repo: repo.New()} }

// UpdateStatus handles the UpdateStatus RPC. It is the rpc entry-point root.
func (h *Handler) UpdateStatus() {
	domain.ValidateSpec(domain.Network{})
	h.repo.Insert()
}
`

// SpecGenRPCContractInternal is the Internal-service RPC-contract arch-gen
// fixture: an InternalNetworkService/UpdateStatus functionality whose
// .proto carries no (google.api.http) annotation. arch-gen's L3 artifact
// must row the RPC with an em-dash REST cell.
func SpecGenRPCContractInternal() Spec {
	s := grpcServiceBase("example.com/geninternal", []string{"UpdateStatus"})
	// The synthetic kacho-proto names the service InternalNetworkService;
	// the in-repo gRPC stub must register the same FQN so the anchor and
	// the proto RPC line up.
	s.Services[0].FQN = "kacho.cloud.vpc.v1.InternalNetworkService"
	s.MainBody = "grpcServer := grpcstub.NewServer()\n" +
		"pb.RegisterInternalNetworkServiceServer(grpcServer, handler.New())"
	s.Files = map[string]string{
		"internal/handler/handler.go": genInternalHandlerSource,
		"internal/domain/domain.go":   rehome(genDomainSource, "", ""),
		"internal/repo/repo.go":       genRepoSource,
	}
	s.Notes = []NoteSpec{{
		File: "internal-lifecycle.md", Repo: "geninternal",
		RPCAnchors: []string{"kacho.cloud.vpc.v1.InternalNetworkService/UpdateStatus"},
		SourceSHA:  FreshSHA,
		Body: "# Internal network lifecycle\n\n" +
			"This curated L2 note anchors InternalNetworkService/UpdateStatus,\n" +
			"an Internal RPC carrying no google.api.http annotation.\n",
	}}
	s.ProtoFiles = map[string]string{
		"kacho/cloud/vpc/v1/internal_network_service.proto": genRPCContractInternalProto,
	}
	return s
}

// genEnrichDomain is the domain package of the L3/L4 enrichment
// fixture: it carries a documented package-level variable
// (DefaultRegion) read by two documented functions, so L4's
// "variable → functions" link has a non-trivial, sorted user list to
// render. Every declaration is documented so the synopsis columns are
// populated rather than placeholdered.
const genEnrichDomain = `// Package domain holds the domain surface of the L3/L4 enrichment
// fixture.
package domain

// DefaultRegion is the region a Network defaults to when none is set.
// It is read by RegionOf and by Normalize below.
var DefaultRegion = "ru-central1"

// RegionOf returns the effective region of a Network: its own region
// when set, otherwise the package default.
func RegionOf(region string) string {
	if region == "" {
		return DefaultRegion
	}
	return region
}

// Normalize rewrites an empty region to the package default in place.
func Normalize(region *string) {
	if *region == "" {
		*region = DefaultRegion
	}
}

// ValidateSpec validates a Network spec. Reachable from the Create RPC,
// so its synopsis appears in the L3 call-tree.
func ValidateSpec() error { return nil }
`

// genEnrichHandler is the handler of the L3/L4 enrichment fixture: its
// Create RPC reaches the documented domain.ValidateSpec, so the L3
// call-tree carries that function's synopsis and the L4 function →
// functionality table maps it to the anchored functionality.
const genEnrichHandler = `// Package handler implements the NetworkService gRPC handler of the
// L3/L4 enrichment fixture.
package handler

import "example.com/genenrich/internal/domain"

// Handler implements pb.NetworkServiceServer.
type Handler struct{}

// New returns a fresh Handler.
func New() *Handler { return &Handler{} }

// Create handles the Create RPC. It validates the spec before
// persisting it.
func (h *Handler) Create() {
	_ = domain.ValidateSpec()
}
`

// SpecGenEnrich is the L3/L4 enrichment fixture: a NetworkService whose
// Create RPC reaches a documented domain function, and a domain package
// carrying a package-level variable read by two documented functions.
// It lets the enrichment tests assert that L3 call-tree bullets carry
// doc-comment synopses, that L4 links a variable to its repo-function
// users, and that the L4 function → functionality table maps a
// reachable function to its L2 functionality.
func SpecGenEnrich() Spec {
	s := grpcServiceBase("example.com/genenrich", []string{"Create"})
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "genenrich",
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		SourceSHA:  FreshSHA,
		Body: "# Network lifecycle\n\n" +
			"This curated L2 note anchors NetworkService/Create. arch-gen must\n" +
			"enrich the L3 call-tree and the L4 tables from the code.\n",
	}}
	s.Files = map[string]string{
		"internal/handler/handler.go": genEnrichHandler,
		"internal/domain/domain.go":   genEnrichDomain,
	}
	return s
}

// rehome rewrites every occurrence of an old module path segment with a new
// one in fixture source. genHandlerSource imports "example.com/genbasic/…";
// a fixture re-homed under a different module needs those import paths
// rewritten. A bare ("","") request is a no-op passthrough used where the
// source carries no module-qualified import (genDomainSource).
func rehome(src, oldMod, newMod string) string {
	if oldMod == "" || newMod == "" {
		return src
	}
	return strings.ReplaceAll(src, oldMod, newMod)
}
