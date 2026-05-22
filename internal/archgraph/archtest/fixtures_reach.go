package archtest

// Reach (group C) and C2 dead-code (group F) fixtures. These carry
// hand-tailored business-logic packages — reachability chains, dead code,
// keep annotations, reflection — so the service opts out of the default
// handler (NoHandler) and the handler / repo / sdk / registry packages are
// supplied verbatim through Files. The structural fields still emit the
// shared scaffold: go.mod, grpcstub, the pb stub and main.go wiring.

// oneCreateNote is the standard single-RPC L2 note shared by every C2
// fixture (so C1 passes and C2 can be exercised). Its source_sha is the
// FreshSHA sentinel: an arch-audit run also evaluates C3, so the note
// must be resolvable to a genuine freshness hash. C2's own unit test
// ignores source_sha.
func oneCreateNote(repo string) NoteSpec {
	return NoteSpec{
		File: "network-lifecycle.md", Repo: repo,
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		SourceSHA:  FreshSHA,
		Body: "# Network lifecycle\n\n" +
			"This L2 note anchors the single NetworkService Create RPC, so C1\n" +
			"completeness passes and C2 dead-code can be exercised.\n",
	}
}

// grpcServiceFiles returns a Spec base for a C2/reach fixture: one
// NetworkService with the given methods, NoHandler set, main.go wiring the
// handler. The caller adds the handler / business Files.
func grpcServiceBase(module string, methods []string) Spec {
	return Spec{
		Module: module,
		Services: []ServiceSpec{{
			FQN:         "kacho.cloud.vpc.v1.NetworkService",
			Methods:     methods,
			HandlerType: "Handler",
			NoHandler:   true,
		}},
		ExtraMainImports: []string{
			module + "/grpcstub",
			module + "/internal/handler",
			module + "/pb",
		},
		MainBody: `grpcServer := grpcstub.NewServer()
pb.RegisterNetworkServiceServer(grpcServer, handler.New())`,
	}
}

// --- reach fixtures (group C) ----------------------------------------------

// SpecReachBasic — handler.Create -> validateSpec and repo.Insert; an
// unusedHelper is dead. Scenario C1-graph.
func SpecReachBasic() Spec {
	s := grpcServiceBase("example.com/reachbasic", []string{"Create"})
	s.Files = map[string]string{
		"internal/handler/handler.go": `// Package handler implements the NetworkService gRPC handler of the
// reach-basic fixture.
//
// The call-graph the reachability pass must recover:
//
//	(*NetworkHandler).Create -> validateSpec -> (*repo.NetworkRepo).Insert
//
// unusedHelper is defined but never called from any entry-point.
package handler

import "example.com/reachbasic/internal/repo"

// NetworkHandler implements pb.NetworkServiceServer.
type NetworkHandler struct {
	repo *repo.NetworkRepo
}

// New returns a fresh NetworkHandler wired to a repo.
func New() *NetworkHandler { return &NetworkHandler{repo: repo.New()} }

// Create handles the Create RPC. It is the rpc entry-point root.
func (h *NetworkHandler) Create() {
	h.validateSpec()
	h.repo.Insert()
}

// validateSpec validates a Network spec. Reachable from Create.
func (h *NetworkHandler) validateSpec() {}

// unusedHelper is never called from any entry-point — dead code.
func (h *NetworkHandler) unusedHelper() {}
`,
		"internal/repo/repo.go": `// Package repo is the persistence adapter of the reach-basic fixture.
package repo

// NetworkRepo persists Network rows.
type NetworkRepo struct{}

// New returns a fresh NetworkRepo.
func New() *NetworkRepo { return &NetworkRepo{} }

// Insert writes a Network row. Reachable from Create's call chain.
func (r *NetworkRepo) Insert() {
	r.encodeRow()
}

// encodeRow is a private helper reachable transitively from Insert.
func (r *NetworkRepo) encodeRow() {}
`,
	}
	return s
}

// SpecReachInterface — handler.Create calls Insert through a port
// interface; RTA resolves it to the concrete pgNetworkRepo. Scenario
// C2-graph.
func SpecReachInterface() Spec {
	s := grpcServiceBase("example.com/reachinterface", []string{"Create"})
	s.Files = map[string]string{
		"internal/handler/handler.go": `// Package handler implements the NetworkService gRPC handler of the
// reach-interface fixture.
//
// The handler depends on the NetworkRepo port interface, not on a concrete
// type. RTA must follow the interface dispatch
// (*NetworkHandler).Create -> NetworkRepo.Insert to the concrete
// (*repo.pgNetworkRepo).Insert.
package handler

import "example.com/reachinterface/internal/repo"

// NetworkRepo is the service-layer port interface for Network persistence.
type NetworkRepo interface {
	Insert()
}

// NetworkHandler implements pb.NetworkServiceServer.
type NetworkHandler struct {
	repo NetworkRepo
}

// New returns a fresh NetworkHandler wired to the concrete repo.
func New() *NetworkHandler { return &NetworkHandler{repo: repo.New()} }

// Create handles the Create RPC. It calls Insert through the NetworkRepo
// port interface — RTA resolves the dynamic call to the concrete type.
func (h *NetworkHandler) Create() {
	h.repo.Insert()
}
`,
		"internal/repo/repo.go": `// Package repo is the persistence adapter of the reach-interface fixture:
// a concrete implementation of the service-layer NetworkRepo port.
package repo

// pgNetworkRepo is the concrete, unexported implementation of the
// service-layer NetworkRepo port interface.
type pgNetworkRepo struct{}

// New constructs the concrete repository and returns it.
func New() *pgNetworkRepo { return &pgNetworkRepo{} }

// Insert writes a Network row. Reached only through the NetworkRepo
// interface dispatch in the handler.
func (r *pgNetworkRepo) Insert() {}
`,
	}
	return s
}

// --- C2 dead-code fixtures (group F) ---------------------------------------

// SpecC2Pass (F1) — every exported symbol is reachable from Create.
func SpecC2Pass() Spec {
	s := grpcServiceBase("example.com/c2f1", []string{"Create"})
	s.Notes = []NoteSpec{oneCreateNote("c2f1")}
	s.Files = map[string]string{
		"internal/handler/handler.go": `// Package handler implements the NetworkService gRPC handler of the
// C2-F1 fixture. Every exported symbol here is reachable from Create.
package handler

import "example.com/c2f1/internal/repo"

// Handler implements pb.NetworkServiceServer.
type Handler struct {
	repo *repo.Repo
}

// New returns a fresh Handler wired to a repo.
func New() *Handler { return &Handler{repo: repo.New()} }

// Create handles the Create RPC. It is the rpc entry-point root.
func (h *Handler) Create() {
	h.validateSpec()
	h.repo.Insert()
}

// validateSpec validates a Network spec. Reachable from Create.
func (h *Handler) validateSpec() {}
`,
		"internal/repo/repo.go": `// Package repo is the persistence adapter of the C2-F1 fixture.
package repo

// Repo persists Network rows.
type Repo struct{}

// New returns a fresh Repo.
func New() *Repo { return &Repo{} }

// Insert writes a Network row.
func (r *Repo) Insert() {
	r.encodeRow()
}

// encodeRow is a private helper reachable transitively from Insert.
func (r *Repo) encodeRow() {}
`,
	}
	return s
}

// SpecC2Unreachable (F2) — LegacyMigrateAddresses is exported, unreachable
// and unannotated. C2 FAILs. The func keyword sits on line 14 so the
// finding can assert "internal/repo/legacy.go:14".
func SpecC2Unreachable() Spec {
	s := grpcServiceBase("example.com/c2f2", []string{"Create"})
	s.Notes = []NoteSpec{oneCreateNote("c2f2")}
	s.Files = map[string]string{
		"internal/handler/handler.go": `// Package handler implements the NetworkService gRPC handler of the
// C2-F2 fixture.
package handler

import "example.com/c2f2/internal/repo"

// Handler implements pb.NetworkServiceServer.
type Handler struct {
	repo *repo.Repo
}

// New returns a fresh Handler wired to a repo.
func New() *Handler { return &Handler{repo: repo.New()} }

// Create handles the Create RPC. It is the rpc entry-point root.
func (h *Handler) Create() {
	h.repo.Insert()
}
`,
		"internal/repo/repo.go": `// Package repo is the persistence adapter of the C2-F2 fixture.
package repo

// Repo persists Network rows.
type Repo struct{}

// New returns a fresh Repo.
func New() *Repo { return &Repo{} }

// Insert writes a Network row.
func (r *Repo) Insert() {}
`,
		// legacy.go: LegacyMigrateAddresses' func keyword must land on
		// line 14 so the F2 finding can assert the exact file:line.
		"internal/repo/legacy.go": `package repo

// legacy.go holds a one-off migration helper that is no longer wired
// into any entry-point of the C2-F2 fixture. archgraph C2 must report
// LegacyMigrateAddresses as dead code: it is exported, unreachable from
// every entry-point, and carries no archgraph:keep annotation.
//
// The declaration of LegacyMigrateAddresses is deliberately kept on
// line 14 so the C2 finding can assert the exact file:line position
// "internal/repo/legacy.go:14" (the line of the func keyword).

// LegacyMigrateAddresses rewrites pre-v1 address rows. Dead code: it is
// exported and unreachable from every entry-point.
func LegacyMigrateAddresses() {}
`,
	}
	return s
}

// SpecC2Keep (F3) — BuildClientSDK is unreachable but annotated with
// `// archgraph:keep <reason>`; C2 passes and lists it as kept.
func SpecC2Keep() Spec {
	s := grpcServiceBase("example.com/c2f3", []string{"Create"})
	s.Notes = []NoteSpec{oneCreateNote("c2f3")}
	s.Files = map[string]string{
		"internal/handler/handler.go": minimalHandler("C2-F3"),
		"internal/sdk/sdk.go": `// Package sdk holds the client-SDK builder of the C2-F3 fixture.
//
// BuildClientSDK is exported and unreachable, but the // archgraph:keep
// comment with a non-empty reason marks it intentionally kept.
package sdk

// archgraph:keep public SDK surface, consumed by external clients
func BuildClientSDK() {}
`,
	}
	return s
}

// SpecC2KeepBare (F4) — a bare `// archgraph:keep` with no reason over an
// unreachable symbol. C2 rejects it; the keep comment must sit on line 9.
func SpecC2KeepBare() Spec {
	s := grpcServiceBase("example.com/c2f4", []string{"Create"})
	s.Notes = []NoteSpec{oneCreateNote("c2f4")}
	s.Files = map[string]string{
		"internal/handler/handler.go": minimalHandler("C2-F4"),
		// The bare keep comment must land on line 9 so the F4 finding can
		// assert "internal/repo/x.go:9".
		"internal/repo/x.go": `// Package repo holds an unreachable exported symbol of the C2-F4
// fixture. The // archgraph:keep comment on line 9 is bare — it
// carries no reason — so C2 rejects it as an invalid annotation rather
// than honouring it as a suppression.
package repo

// LegacyHelper is exported and unreachable from every entry-point.

// archgraph:keep
func LegacyHelper() {}
`,
	}
	return s
}

// SpecC2KeepTransitive (F5) — a kept BuildClientSDK is an extra root; the
// private assembleSDKManifest and exported EncodeManifest it reaches are
// not dead.
func SpecC2KeepTransitive() Spec {
	s := grpcServiceBase("example.com/c2f5", []string{"Create"})
	s.Notes = []NoteSpec{oneCreateNote("c2f5")}
	s.Files = map[string]string{
		"internal/handler/handler.go": minimalHandler("C2-F5"),
		"internal/sdk/sdk.go": `// Package sdk holds the client-SDK builder of the C2-F5 fixture.
//
// BuildClientSDK is // archgraph:keep-annotated and unreachable. It calls
// assembleSDKManifest, which calls EncodeManifest. A kept symbol is an
// extra reachability root, so neither is reported as dead.
package sdk

// archgraph:keep public SDK surface, consumed by external clients
func BuildClientSDK() {
	assembleSDKManifest()
}

// assembleSDKManifest is private; reachable only via BuildClientSDK.
func assembleSDKManifest() {
	EncodeManifest()
}

// EncodeManifest is exported; reachable only transitively from the kept
// BuildClientSDK.
func EncodeManifest() {}
`,
	}
	return s
}

// SpecC2Reflection (F6) — Dispatcher.ReflectInvoke is invoked only through
// reflect.Value.Call; RTA cannot see the edge so C2 reports it dead until
// annotated. The CLI F6 test injects the keep annotation as a continuation.
func SpecC2Reflection() Spec {
	module := "example.com/c2f6"
	s := Spec{
		Module: module,
		Services: []ServiceSpec{{
			FQN:       "kacho.cloud.vpc.v1.NetworkService",
			Methods:   []string{"Create"},
			NoHandler: true,
		}},
		Notes: []NoteSpec{oneCreateNote("c2f6")},
		ExtraMainImports: []string{
			"reflect",
			module + "/grpcstub",
			module + "/internal/handler",
			module + "/internal/registry",
			module + "/pb",
		},
		MainBody: `grpcServer := grpcstub.NewServer()
pb.RegisterNetworkServiceServer(grpcServer, handler.New())

// ReflectInvoke is called only through reflection — RTA cannot see this
// edge, so C2 reports it as dead until annotated.
d := registry.New()
reflect.ValueOf(d).MethodByName("ReflectInvoke").Call(nil)`,
		Files: map[string]string{
			"internal/handler/handler.go": minimalHandler("C2-F6"),
			"internal/registry/registry.go": `// Package registry holds a dispatcher whose ReflectInvoke method is
// called only through reflect.Value.Call in main. RTA does not model
// reflection, so ReflectInvoke is reported dead until an
// // archgraph:keep reflection-invoked annotation is added.
package registry

// Dispatcher routes reflection-invoked calls.
type Dispatcher struct{}

// New returns a fresh Dispatcher.
func New() *Dispatcher { return &Dispatcher{} }

// ReflectInvoke is exported and invoked only via reflect.Value.Call.
func (d *Dispatcher) ReflectInvoke() {}
`,
		},
	}
	return s
}

// minimalHandler renders a no-dependency NetworkService handler with a
// single Create method, used by C2 fixtures whose dead code lives in a
// separate package.
func minimalHandler(fixture string) string {
	return "// Package handler implements the NetworkService gRPC handler of the\n" +
		"// " + fixture + " fixture.\n" +
		"package handler\n\n" +
		"// Handler implements pb.NetworkServiceServer.\n" +
		"type Handler struct{}\n\n" +
		"// New returns a fresh Handler.\n" +
		"func New() *Handler { return &Handler{} }\n\n" +
		"// Create handles the Create RPC. It is the rpc entry-point root.\n" +
		"func (h *Handler) Create() {}\n"
}

// SpecC2Library (F7) — a pure library repo; C2 is skipped.
func SpecC2Library() Spec {
	return Spec{
		Module: "example.com/c2f7",
		NoMain: true,
		Files: map[string]string{
			"stringutil/stringutil.go": `// Package stringutil is the sole package of the C2-F7 fixture: a pure
// library repo with no main package, hence no entry-points. archgraph
// classifies it as a library and skips C2 dead-code entirely. Reverse and
// Title are exported and never called, yet C2 must not report them.
package stringutil

// Reverse returns s with its runes in reverse order.
func Reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

// Title returns s unchanged (fixture stub).
func Title(s string) string { return s }
`,
		},
	}
}
