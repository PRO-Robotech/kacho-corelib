package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// C3 fixtures (group G): a gRPC service plus L2 notes under docs/arch,
// exercising the freshness check — does a note's source_sha still match
// the hash of the reachable-set files under its anchors.
//
// A C3 fixture's note carries one of three kinds of source_sha:
//
//   - a literal SHA (e.g. "deadbeef") — a deliberately stale note,
//     whose recorded SHA cannot match the freshly computed hash;
//   - the empty string — a note with anchors but no source_sha at all
//     (scenario G5);
//   - the FreshSHA sentinel — the C3 test, after building the repo and
//     computing the reachability graph, rewrites the note in place with
//     the genuine hash of its anchors' reachable-set, so the note is
//     correctly fresh (scenarios G1, G3-A, G4).
//
// The FreshSHA two-phase rewrite lives in the C3 test, because the
// genuine hash can only be computed once the fixture is on disk and its
// packages are loaded.

// FreshSHA is the NoteSpec.SourceSHA sentinel marking a note whose
// source_sha must be the genuine hash of its anchors' reachable-set.
// archtest writes it verbatim into the fixture's frontmatter; the C3
// test recognises it and rewrites the note with the real hash after the
// reachability graph is computed. It is intentionally not a valid hex
// SHA so a fixture that forgets the rewrite fails loudly rather than
// silently passing.
const FreshSHA = "<archtest:fresh>"

// EmptySHA is the NoteSpec.SourceSHA sentinel forcing an empty
// source_sha ("") on a note that is NOT planned — an anchored note that
// deliberately records no SHA (scenario G5's missing-source_sha case).
// It is needed because a bare SourceSHA "" on a non-planned note is
// otherwise filled with the default "a1b2c3d" by the builder.
const EmptySHA = "<archtest:empty>"

// SpecC3Fresh (G1) — one L2 note anchoring NetworkService/Create whose
// source_sha is the genuine hash of its reachable-set. C3 PASSes.
func SpecC3Fresh() Spec {
	s := grpcServiceBase("example.com/c3g1", []string{"Create"})
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "c3g1",
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		SourceSHA:  FreshSHA,
		Body: "# Network lifecycle\n\n" +
			"This L2 note anchors NetworkService/Create. Its source_sha is the\n" +
			"genuine hash of the reachable-set, so C3 freshness passes.\n",
	}}
	s.Files = map[string]string{
		"internal/handler/handler.go": c3HandlerSource("example.com/c3g1", "C3-G1"),
		"internal/repo/repo.go":       c3RepoSource("c3g1", "C3-G1"),
	}
	return s
}

// SpecC3Stale (G2) — the note's source_sha is a literal value that does
// NOT match the hash of its reachable-set files; C3 FAILs the note as
// stale. The note file is docs/arch/network-lifecycle.md so the G2
// finding can assert that exact path.
func SpecC3Stale() Spec {
	s := grpcServiceBase("example.com/c3g2", []string{"Create"})
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "c3g2",
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		// A literal, deliberately wrong SHA — the recorded value is
		// stale relative to the fixture's actual code.
		SourceSHA: "0000000stalesha0000000",
		Body: "# Network lifecycle\n\n" +
			"This L2 note's source_sha is stale: it no longer matches the hash\n" +
			"of the code under its anchors, so C3 freshness fails.\n",
	}}
	s.Files = map[string]string{
		"internal/handler/handler.go": c3HandlerSource("example.com/c3g2", "C3-G2"),
		"internal/repo/repo.go":       c3RepoSource("c3g2", "C3-G2"),
	}
	return s
}

// SpecC3TwoNotes (G3) — two services, two L2 notes A and B whose
// reachable-sets do not overlap. Note A (network-lifecycle.md) is fresh;
// note B (instance-lifecycle.md) is stale. C3 must fail B only and pass
// A. Note A carries the FreshSHA sentinel; note B a literal stale SHA.
func SpecC3TwoNotes() Spec {
	module := "example.com/c3g3"
	s := Spec{
		Module: module,
		Services: []ServiceSpec{
			{
				FQN:         "kacho.cloud.vpc.v1.NetworkService",
				Methods:     []string{"Create"},
				HandlerType: "NetworkHandler",
				Constructor: "NewNetwork",
				NoHandler:   true,
			},
			{
				FQN:         "kacho.cloud.compute.v1.InstanceService",
				Methods:     []string{"Create"},
				HandlerType: "InstanceHandler",
				Constructor: "NewInstance",
				NoHandler:   true,
			},
		},
		ExtraMainImports: []string{
			module + "/grpcstub",
			module + "/internal/instancehandler",
			module + "/internal/networkhandler",
			module + "/pb",
		},
		MainBody: `grpcServer := grpcstub.NewServer()
pb.RegisterNetworkServiceServer(grpcServer, networkhandler.New())
pb.RegisterInstanceServiceServer(grpcServer, instancehandler.New())`,
		Notes: []NoteSpec{
			{
				File: "network-lifecycle.md", Repo: "c3g3",
				RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
				SourceSHA:  FreshSHA,
				Body: "# Network lifecycle\n\n" +
					"Note A — anchors NetworkService/Create. Fresh: its reachable-set\n" +
					"(networkhandler + networkrepo) is unchanged.\n",
			},
			{
				File: "instance-lifecycle.md", Repo: "c3g3",
				RPCAnchors: []string{"kacho.cloud.compute.v1.InstanceService/Create"},
				SourceSHA:  "0000000stalesha0000000",
				Body: "# Instance lifecycle\n\n" +
					"Note B — anchors InstanceService/Create. Stale: its recorded\n" +
					"source_sha no longer matches the code under its anchors.\n",
			},
		},
		Files: map[string]string{
			"internal/networkhandler/handler.go": `// Package networkhandler implements the NetworkService gRPC handler of
// the C3-G3 fixture. Its reachable-set is disjoint from the instance
// handler's, so a change to one note's anchors cannot stale the other.
package networkhandler

import "example.com/c3g3/internal/networkrepo"

// Handler implements pb.NetworkServiceServer.
type Handler struct {
	repo *networkrepo.Repo
}

// New returns a fresh Handler wired to a networkrepo.
func New() *Handler { return &Handler{repo: networkrepo.New()} }

// Create handles the Create RPC. It is the rpc entry-point root.
func (h *Handler) Create() {
	h.repo.Insert()
}
`,
			"internal/networkrepo/repo.go": `// Package networkrepo is the persistence adapter of the C3-G3 network
// side.
package networkrepo

// Repo persists Network rows.
type Repo struct{}

// New returns a fresh Repo.
func New() *Repo { return &Repo{} }

// Insert writes a Network row.
func (r *Repo) Insert() {}
`,
			"internal/instancehandler/handler.go": `// Package instancehandler implements the InstanceService gRPC handler
// of the C3-G3 fixture.
package instancehandler

import "example.com/c3g3/internal/instancerepo"

// Handler implements pb.InstanceServiceServer.
type Handler struct {
	repo *instancerepo.Repo
}

// New returns a fresh Handler wired to an instancerepo.
func New() *Handler { return &Handler{repo: instancerepo.New()} }

// Create handles the Create RPC. It is the rpc entry-point root.
func (h *Handler) Create() {
	h.repo.Insert()
}
`,
			"internal/instancerepo/repo.go": `// Package instancerepo is the persistence adapter of the C3-G3 instance
// side.
package instancerepo

// Repo persists Instance rows.
type Repo struct{}

// New returns a fresh Repo.
func New() *Repo { return &Repo{} }

// Insert writes an Instance row.
func (r *Repo) Insert() {}
`,
		},
	}
	return s
}

// SpecC3MissingSHA (G5) — a note with a non-empty anchors list but an
// empty source_sha. C3 FAILs the note with a missing-source_sha
// finding.
func SpecC3MissingSHA() Spec {
	s := grpcServiceBase("example.com/c3g5", []string{"Create"})
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "c3g5",
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		// EmptySHA forces an empty source_sha on this anchored,
		// non-planned note — the missing-source_sha case C3 must flag.
		SourceSHA: EmptySHA,
		Status:    "implemented",
		Body: "# Network lifecycle\n\n" +
			"This L2 note has anchors but no source_sha — C3 must flag it as a\n" +
			"missing source_sha that a reviewer has to set.\n",
	}}
	s.Files = map[string]string{
		"internal/handler/handler.go": c3HandlerSource("example.com/c3g5", "C3-G5"),
		"internal/repo/repo.go":       c3RepoSource("c3g5", "C3-G5"),
	}
	return s
}

// SpecC3PlannedSkipped (G6) — a note with an empty anchors list (a
// planned functionality, no source_sha). C3 must skip it entirely: no
// reachable-set, hence no freshness verdict, and no missing-source_sha
// failure. A second, fully fresh note keeps the rest of C3 passing.
func SpecC3PlannedSkipped() Spec {
	s := grpcServiceBase("example.com/c3g6", []string{"Create"})
	s.Notes = []NoteSpec{
		{
			File: "network-lifecycle.md", Repo: "c3g6",
			RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
			SourceSHA:  FreshSHA,
			Body: "# Network lifecycle\n\n" +
				"A fresh L2 note — keeps C3 passing alongside the planned note.\n",
		},
		{
			File: "network-planned.md", Repo: "c3g6",
			// No anchors, planned status, no source_sha — C3 skips it.
			Status:    "planned",
			SourceSHA: "",
			Body: "# Network planned\n\n" +
				"A planned functionality: empty anchors, no source_sha. C3 skips\n" +
				"it — no anchors means no reachable-set to hash.\n",
		},
	}
	s.Files = map[string]string{
		"internal/handler/handler.go": c3HandlerSource("example.com/c3g6", "C3-G6"),
		"internal/repo/repo.go":       c3RepoSource("c3g6", "C3-G6"),
	}
	return s
}

// SpecC3StdlibReach (G4, stdlib-scoping case) — a NetworkService whose
// Create handler genuinely calls into the standard library
// (fmt.Sprintf with a %v verb, which RTA over-approximates into the
// reflect / strconv / sync stdlib packages). The anchor's reachable-set
// therefore contains source files OUTSIDE the fixture repository —
// under GOROOT/src — exactly the shape the real kacho-vpc repo
// exhibits and the minimal empty-handler C3 fixtures never reproduce.
//
// It exists so the G4 determinism test can assert that AnchorHash
// scopes its digest to files UNDER repoRoot: a hash that fed the
// GOROOT-resident stdlib files in would depend on the toolchain
// install path and Go version, breaking G4's machine-independence
// guarantee. The note carries the FreshSHA sentinel so an arch-audit
// run can resolve it to a genuine hash.
func SpecC3StdlibReach() Spec {
	s := grpcServiceBase("example.com/c3g4std", []string{"Create"})
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "c3g4std",
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		SourceSHA:  FreshSHA,
		Body: "# Network lifecycle\n\n" +
			"This L2 note anchors NetworkService/Create, whose handler calls\n" +
			"into the standard library — its reachable-set spans GOROOT files.\n" +
			"C3's freshness hash must still scope to in-repo files only.\n",
	}}
	s.Files = map[string]string{
		"internal/handler/handler.go": c3StdlibHandlerSource("example.com/c3g4std"),
		"internal/repo/repo.go":       c3RepoSource("c3g4std", "C3-G4-STD"),
	}
	return s
}

// c3StdlibHandlerSource renders a NetworkService handler whose Create
// reaches both repo.Insert AND the standard library: fmt.Sprintf with a
// %v verb forces RTA to over-approximate interface dispatch into the
// reflect / strconv stdlib packages, so the anchor's reachable-set
// contains GOROOT-resident source files. This is the fixture shape that
// reproduces the AnchorHash absolute-path defect on a real repo.
func c3StdlibHandlerSource(module string) string {
	return "// Package handler implements the NetworkService gRPC handler of the\n" +
		"// C3-G4-STD fixture. Create reaches repo.Insert AND the standard\n" +
		"// library (fmt), so its reachable-set spans GOROOT source files.\n" +
		"package handler\n\n" +
		"import (\n\t\"fmt\"\n\n\t\"" + module + "/internal/repo\"\n)\n\n" +
		"// Handler implements pb.NetworkServiceServer.\n" +
		"type Handler struct {\n\trepo *repo.Repo\n}\n\n" +
		"// New returns a fresh Handler wired to a repo.\n" +
		"func New() *Handler { return &Handler{repo: repo.New()} }\n\n" +
		"// Create handles the Create RPC. It calls fmt.Sprintf — pulling\n" +
		"// stdlib files into its reachable-set — then reaches repo.Insert.\n" +
		"func (h *Handler) Create() {\n" +
		"\t_ = fmt.Sprintf(\"network %v created\", h.repo)\n" +
		"\th.repo.Insert()\n}\n"
}

// c3HandlerSource renders a NetworkService handler whose Create reaches
// a repo.Insert — the reachable-set the C3 hash is computed over.
func c3HandlerSource(module, fixture string) string {
	return "// Package handler implements the NetworkService gRPC handler of the\n" +
		"// " + fixture + " fixture. Create reaches repo.Insert; both files form\n" +
		"// the reachable-set the C3 freshness hash is computed over.\n" +
		"package handler\n\n" +
		"import \"" + module + "/internal/repo\"\n\n" +
		"// Handler implements pb.NetworkServiceServer.\n" +
		"type Handler struct {\n\trepo *repo.Repo\n}\n\n" +
		"// New returns a fresh Handler wired to a repo.\n" +
		"func New() *Handler { return &Handler{repo: repo.New()} }\n\n" +
		"// Create handles the Create RPC. It is the rpc entry-point root.\n" +
		"func (h *Handler) Create() {\n\th.repo.Insert()\n}\n"
}

// c3RepoSource renders the persistence adapter reached from Create.
func c3RepoSource(_, fixture string) string {
	return "// Package repo is the persistence adapter of the " + fixture + " fixture.\n" +
		"package repo\n\n" +
		"// Repo persists Network rows.\n" +
		"type Repo struct{}\n\n" +
		"// New returns a fresh Repo.\n" +
		"func New() *Repo { return &Repo{} }\n\n" +
		"// Insert writes a Network row.\n" +
		"func (r *Repo) Insert() {}\n"
}

// ResolveFreshNotes is the second phase of the FreshSHA two-phase build:
// it rewrites every L2 note under root/docs/arch whose source_sha is the
// FreshSHA sentinel with a genuine hash, and returns the number of notes
// rewritten.
//
// The freshness hash is computed by the caller-supplied hashFor — it
// takes the absolute path of a sentinel-carrying note and returns the
// hash to record. Injecting the hash keeps archtest, the low-level
// fixture builder, free of any dependency on the check / reach packages:
// only the test that already imports them supplies check.AnchorHash.
//
// ResolveFreshNotes is idempotent over a tree with no remaining sentinel
// (it rewrites nothing and returns 0), so a fixture mutated after its
// first resolve can be re-resolved cleanly.
func ResolveFreshNotes(t *testing.T, root string, hashFor func(notePath string) string) int {
	t.Helper()
	archDir := filepath.Join(root, "docs", "arch")
	resolved := 0
	err := filepath.WalkDir(archDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		sentinel := "source_sha: " + FreshSHA
		if !strings.Contains(string(raw), sentinel) {
			return nil
		}
		hash := hashFor(p)
		if hash == "" {
			t.Fatalf("archtest: ResolveFreshNotes got an empty hash for note %s", p)
		}
		rewritten := strings.Replace(string(raw), sentinel, "source_sha: "+hash, 1)
		if werr := os.WriteFile(p, []byte(rewritten), 0o644); werr != nil {
			return werr
		}
		resolved++
		return nil
	})
	if err != nil {
		t.Fatalf("archtest: ResolveFreshNotes walking %s: %v", archDir, err)
	}
	return resolved
}
