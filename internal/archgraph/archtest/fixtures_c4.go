package archtest

// C4 doc-coverage fixtures (group H): a gRPC service whose business
// packages exercise the doc-coverage check — every function, method,
// package variable and constant of a repo-owned package must carry a
// Go doc-comment.
//
// The fixtures opt the service out of the default handler (NoHandler)
// and supply the handler / business Files verbatim, so the documented
// vs. undocumented shape is hand-tailored. They reuse oneCreateNote so
// C1 also passes when the fixture is run through arch-audit.

// c4DocumentedHandler is the NetworkService handler of the C4 PASS
// fixture: every function and method carries a doc-comment.
const c4DocumentedHandler = `// Package handler implements the NetworkService gRPC handler of the
// C4 doc-coverage fixture. Every declaration here is documented.
package handler

// Handler implements pb.NetworkServiceServer.
type Handler struct{}

// New returns a fresh Handler.
func New() *Handler { return &Handler{} }

// Create handles the Create RPC. It is the rpc entry-point root.
func (h *Handler) Create() {
	h.validateSpec()
}

// validateSpec validates a Network spec before persistence.
func (h *Handler) validateSpec() {}
`

// c4DocumentedDomain is the domain package of the C4 PASS fixture:
// every type, function, package var and package const is documented.
const c4DocumentedDomain = `// Package domain holds the documented domain surface of the C4
// doc-coverage fixture.
package domain

// State enumerates the lifecycle states of a Network.
type State string

// StateActive marks a fully provisioned Network.
const StateActive State = "ACTIVE"

// DefaultRegion is the region a Network defaults to.
var DefaultRegion = "ru-central1"

// Validate reports whether the State is a recognised lifecycle value.
func Validate(s State) bool { return s == StateActive }
`

// SpecC4Pass (H1) — every function, method, package var and package
// const of every repo-owned package carries a doc-comment, so C4
// PASSes with a zero-finding, N/N summary.
func SpecC4Pass() Spec {
	s := grpcServiceBase("example.com/c4h1", []string{"Create"})
	s.Notes = []NoteSpec{oneCreateNote("c4h1")}
	s.Files = map[string]string{
		"internal/handler/handler.go": c4DocumentedHandler,
		"internal/domain/domain.go":   c4DocumentedDomain,
	}
	return s
}

// c4UndocumentedFuncDomain is the domain package of the C4 function
// FAIL fixture: ComputeChecksum carries no doc-comment. Its func
// keyword is deliberately kept on line 9 so the C4 finding can assert
// the exact "internal/domain/domain.go:9" position.
const c4UndocumentedFuncDomain = `// Package domain holds the domain surface of the C4 undocumented-func
// fixture. ComputeChecksum below is deliberately left without a
// doc-comment so C4 raises an undocumented-function finding. Its func
// keyword sits on line 9 — counted from this header's first line — so
// the finding's "internal/domain/domain.go:9" file:line is assertable
// and pinned.
package domain

func ComputeChecksum() string { return "" }

// HumanReadableName returns a documented helper, present so the
// fixture also has a documented function for the N/N count.
func HumanReadableName() string { return "" }
`

// SpecC4UndocumentedFunc (H2) — ComputeChecksum is a repo-owned
// function with no doc-comment. C4 FAILs with an undocumented-function
// finding naming the symbol and its file:line position.
func SpecC4UndocumentedFunc() Spec {
	s := grpcServiceBase("example.com/c4h2", []string{"Create"})
	s.Notes = []NoteSpec{oneCreateNote("c4h2")}
	s.Files = map[string]string{
		"internal/handler/handler.go": c4DocumentedHandler,
		"internal/domain/domain.go":   c4UndocumentedFuncDomain,
	}
	return s
}

// c4UndocumentedVarDomain is the domain package of the C4 variable
// FAIL fixture: the package-level var apiEndpoint carries no
// doc-comment. The var keyword sits on line 8 so the C4 finding can
// assert the exact "internal/domain/domain.go:8" position.
const c4UndocumentedVarDomain = `// Package domain holds the domain surface of the C4 undocumented-var
// fixture. apiEndpoint below has no doc-comment, so C4 raises an
// undocumented-variable finding. Its declaration sits on line 8 —
// counted from this header's first line — so the finding's file:line
// position is pinned and assertable by the H3 test.
package domain

var apiEndpoint = "https://api.kacho.local"

// Validate is a documented function, present so the fixture also has a
// documented declaration for the N/N count.
func Validate() bool { return apiEndpoint != "" }
`

// SpecC4UndocumentedVar (H3) — apiEndpoint is a repo-owned
// package-level variable with no doc-comment. C4 FAILs with an
// undocumented-variable finding naming the symbol and its file:line.
func SpecC4UndocumentedVar() Spec {
	s := grpcServiceBase("example.com/c4h3", []string{"Create"})
	s.Notes = []NoteSpec{oneCreateNote("c4h3")}
	s.Files = map[string]string{
		"internal/handler/handler.go": c4DocumentedHandler,
		"internal/domain/domain.go":   c4UndocumentedVarDomain,
	}
	return s
}

// c4GeneratedFile is a machine-generated file: it carries the standard
// "// Code generated ... DO NOT EDIT." marker above its package clause
// and an undocumented function. C4 must skip it entirely — generated
// code is not hand-maintained, so demanding hand-written doc-comments
// on it is meaningless.
const c4GeneratedFile = `// Code generated by kacho-fixture-gen. DO NOT EDIT.

package domain

func GeneratedMarshal() []byte { return nil }

var generatedTable = map[string]int{}
`

// SpecC4GeneratedExcluded (H4) — the domain package carries a
// machine-generated file whose undocumented GeneratedMarshal /
// generatedTable would fail C4 if scanned. C4 must exclude the
// generated file by its "// Code generated ... DO NOT EDIT." header,
// so the fixture PASSes.
func SpecC4GeneratedExcluded() Spec {
	s := grpcServiceBase("example.com/c4h4", []string{"Create"})
	s.Notes = []NoteSpec{oneCreateNote("c4h4")}
	s.Files = map[string]string{
		"internal/handler/handler.go":   c4DocumentedHandler,
		"internal/domain/domain.go":     c4DocumentedDomain,
		"internal/domain/domain_gen.go": c4GeneratedFile,
	}
	return s
}
