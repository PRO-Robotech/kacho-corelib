package archtest

import "strings"

// Audit fixtures (group I): whole-repository fixtures for arch-audit's
// combined C1+C2+C3 orchestration. Each one is shaped so a known subset
// of the three checks fails, letting the audit tests assert the
// aggregate verdict, the deterministic grouped output and the
// self-contained finding lines.
//
// A fixture meant to pass C3 carries the FreshSHA sentinel on its
// notes; the audit test resolves it to a genuine hash (the two-phase
// FreshSHA build) before running arch-audit.

// SpecAuditAllPass (I1) — a NetworkService with one Create RPC, anchored
// by a single fresh L2 note, every exported symbol reachable. C1, C2 and
// C3 all PASS, so arch-audit exits 0. It reuses the C2-F1 shape: that
// fixture is already a clean all-green repository.
func SpecAuditAllPass() Spec {
	s := SpecC2Pass()
	s.Module = "example.com/auditi1"
	s.Notes = []NoteSpec{oneCreateNoteFor("auditi1")}
	rebaseC2Imports(&s, "example.com/c2f1", "example.com/auditi1")
	return s
}

// SpecAuditC2Fails (I2) — a NetworkService with one Create RPC anchored
// by a fresh note (C1 PASS, C3 PASS) plus an exported, unreachable,
// unannotated LegacyMigrateAddresses (C2 FAIL). arch-audit must still
// run and report C1 and C3 — it does not stop at the C2 failure — and
// exit non-zero. It reuses the C2-F2 dead-code shape.
func SpecAuditC2Fails() Spec {
	s := SpecC2Unreachable()
	s.Module = "example.com/auditi2"
	s.Notes = []NoteSpec{oneCreateNoteFor("auditi2")}
	rebaseC2Imports(&s, "example.com/c2f2", "example.com/auditi2")
	return s
}

// SpecAuditC1Only (I4) — a NetworkService exposing Create and Update;
// the single L2 note anchors only Create, so Update is an undocumented
// entry-point and C1 FAILs. Both RPCs are registered, so every handler
// method is a reachable entry-point and C2 PASSes; the note carries the
// FreshSHA sentinel so C3 PASSes once resolved. Exactly one check (C1)
// fails — the I4 "C1 finding names the entry-point" case.
func SpecAuditC1Only() Spec {
	const module = "example.com/auditi4c1"
	s := grpcServiceBase(module, []string{"Create", "Update"})
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "auditi4c1",
		// Anchors Create only; Update is left undocumented on purpose.
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		SourceSHA:  FreshSHA,
		Body: "# Network lifecycle\n\n" +
			"This L2 note anchors only Create; Update is deliberately left\n" +
			"undocumented so C1 completeness fails.\n",
	}}
	s.Files = map[string]string{
		"internal/handler/handler.go": `// Package handler implements the NetworkService gRPC handler of the
// audit-I4-C1 fixture: every exported method is a reachable entry-point.
package handler

// Handler implements pb.NetworkServiceServer.
type Handler struct{}

// New returns a fresh Handler.
func New() *Handler { return &Handler{} }

// Create handles the Create RPC entry-point.
func (h *Handler) Create() {}

// Update handles the Update RPC entry-point.
func (h *Handler) Update() {}
`,
	}
	return s
}

// SpecAuditC3Only (I4) — a NetworkService with one Create RPC anchored
// by an L2 note whose source_sha is a deliberately stale literal. C1
// PASSes (Create is documented) and C2 PASSes (every handler symbol is
// reachable), but C3 FAILs the note as stale. Exactly one check (C3)
// fails — the I4 "C3 finding names the note path and hash pair" case.
// It reuses the C3-G2 stale-note shape.
func SpecAuditC3Only() Spec {
	s := SpecC3Stale()
	s.Module = "example.com/auditi4c3"
	rebaseC3Imports(&s, "example.com/c3g2", "example.com/auditi4c3")
	return s
}

// SpecAuditMultiViolation (I3/I4) — a NetworkService exposing Create,
// Update and Delete; the single L2 note anchors only Create, so Update
// and Delete are two undocumented entry-points (two C1 findings), and
// the handler carries two exported, unreachable, unannotated helpers
// (two C2 findings). With four findings across two checks the audit
// output exercises the deterministic C1→C2 grouping and the stable
// per-check sort. The note carries the FreshSHA sentinel so C3 passes
// once resolved — keeping the failure set to exactly C1 and C2.
func SpecAuditMultiViolation() Spec {
	const module = "example.com/auditi3"
	s := grpcServiceBase(module, []string{"Create", "Update", "Delete"})
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "auditi3",
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		SourceSHA:  FreshSHA,
		Body: "# Network lifecycle\n\n" +
			"This L2 note anchors only Create; Update and Delete are left\n" +
			"undocumented so C1 raises two findings.\n",
	}}
	s.Files = map[string]string{
		// The handler registers Create/Update/Delete (all entry-points)
		// and additionally carries two exported, unreachable helpers
		// LegacyAlpha and LegacyBeta — two C2 dead-code findings.
		"internal/handler/handler.go": `// Package handler implements the NetworkService gRPC handler of the
// audit-I3 multi-violation fixture.
package handler

// Handler implements pb.NetworkServiceServer.
type Handler struct{}

// New returns a fresh Handler.
func New() *Handler { return &Handler{} }

// Create handles the Create RPC entry-point.
func (h *Handler) Create() {}

// Update handles the Update RPC entry-point.
func (h *Handler) Update() {}

// Delete handles the Delete RPC entry-point.
func (h *Handler) Delete() {}

// LegacyAlpha is exported, unreachable and unannotated — C2 dead code.
func LegacyAlpha() {}

// LegacyBeta is exported, unreachable and unannotated — C2 dead code.
func LegacyBeta() {}
`,
	}
	return s
}

// oneCreateNoteFor is oneCreateNote with the repo field overridden — the
// audit fixtures reuse the C2 fixture bodies but under their own module
// and repo identity.
func oneCreateNoteFor(repo string) NoteSpec {
	n := oneCreateNote(repo)
	n.Repo = repo
	return n
}

// rebaseC2Imports rewrites the verbatim handler/repo/legacy Files of a
// reused C2 fixture so their internal import paths point at the audit
// fixture's own module instead of the original C2 module.
func rebaseC2Imports(s *Spec, oldModule, newModule string) {
	rebaseModulePaths(s, oldModule, newModule)
}

// rebaseC3Imports rewrites the verbatim handler/repo Files of a reused
// C3 fixture so their internal import paths point at the audit
// fixture's own module.
func rebaseC3Imports(s *Spec, oldModule, newModule string) {
	rebaseModulePaths(s, oldModule, newModule)
}

// rebaseModulePaths rewrites every occurrence of oldModule with
// newModule across a Spec's verbatim Files and its ExtraMainImports, so
// a fixture body reused under a fresh module path still compiles.
func rebaseModulePaths(s *Spec, oldModule, newModule string) {
	for i, imp := range s.ExtraMainImports {
		s.ExtraMainImports[i] = strings.ReplaceAll(imp, oldModule, newModule)
	}
	rebased := make(map[string]string, len(s.Files))
	for path, content := range s.Files {
		rebased[path] = strings.ReplaceAll(content, oldModule, newModule)
	}
	s.Files = rebased
}
