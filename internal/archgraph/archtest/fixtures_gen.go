package archtest

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
// D1/D2/D4/D5/D6): one NetworkService with a Create RPC anchored by a
// single L2 note, a reachable chain of exported functions and a domain
// package with exported types, fields and constants.
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
