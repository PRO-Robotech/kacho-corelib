package archtest

// Computed-status fixtures (group H): a gRPC service exposing exactly the
// NetworkService/Create RPC, with L2 notes whose anchors are deliberately
// aligned, partially aligned or fully misaligned with that single
// entry-point. arch-gen's status pass computes each note's status from how
// its anchors match the discovered entry-point inventory and writes the
// result back into the note's frontmatter.
//
// Every fixture uses the default handler scaffold (a NetworkService with a
// Create method, registered from main.go), so the repository has exactly
// one discoverable entry-point: kacho.cloud.vpc.v1.NetworkService/Create.

// statusServiceBase returns a Spec exposing a single NetworkService whose
// only method is Create — the lone entry-point the group-H notes are
// matched against. The default handler scaffold is emitted (NoHandler
// unset), so main.go registers the service and Create is discoverable.
func statusServiceBase(module string) Spec {
	return Spec{
		Module: module,
		Services: []ServiceSpec{{
			FQN:         "kacho.cloud.vpc.v1.NetworkService",
			Methods:     []string{"Create"},
			HandlerType: "NetworkHandler",
		}},
	}
}

// statusNoteBody is the curated narrative body shared by the group-H
// notes. arch-gen's status write-back must leave it byte-identical — only
// the frontmatter status scalar may change.
const statusNoteBody = "# Network lifecycle\n\n" +
	"This curated L2 note carries a human-authored narrative body. arch-gen\n" +
	"computes the `status` frontmatter field and writes it back, but must\n" +
	"never touch this body or any other frontmatter key.\n"

// SpecStatusImplemented is the H1 fixture: a note whose single anchor —
// NetworkService/Create — resolves to the repository's only entry-point.
// arch-gen must compute status: implemented. The note is authored with
// status: planned so the write-back is observable.
func SpecStatusImplemented() Spec {
	s := statusServiceBase("example.com/statusimplemented")
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "statusimplemented",
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Create"},
		Status:     "planned",
		SourceSHA:  "a1b2c3d",
		Body:       statusNoteBody,
	}}
	return s
}

// SpecStatusPartial is the H2 fixture: a note anchoring both the existing
// NetworkService/Create and the absent NetworkService/Migrate. arch-gen
// must compute status: partial. Authored as planned so the write-back is
// observable.
func SpecStatusPartial() Spec {
	s := statusServiceBase("example.com/statuspartial")
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "statuspartial",
		RPCAnchors: []string{
			"kacho.cloud.vpc.v1.NetworkService/Create",
			"kacho.cloud.vpc.v1.NetworkService/Migrate",
		},
		Status:    "planned",
		SourceSHA: "a1b2c3d",
		Body:      statusNoteBody,
	}}
	return s
}

// SpecStatusPlanned is the H3 fixture: a note whose every anchor —
// NetworkService/Migrate — is absent from the code. arch-gen must compute
// status: planned. Authored as implemented so the write-back is
// observable.
func SpecStatusPlanned() Spec {
	s := statusServiceBase("example.com/statusplanned")
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "statusplanned",
		RPCAnchors: []string{"kacho.cloud.vpc.v1.NetworkService/Migrate"},
		Status:     "implemented",
		SourceSHA:  "a1b2c3d",
		Body:       statusNoteBody,
	}}
	return s
}

// SpecStatusEmptyAnchors is the H3 companion fixture: a note with an empty
// anchors list. arch-gen must compute status: planned.
func SpecStatusEmptyAnchors() Spec {
	s := statusServiceBase("example.com/statusemptyanchors")
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "statusemptyanchors",
		Status: "implemented",
		Body:   statusNoteBody,
	}}
	return s
}

// SpecStatusHandWrongImplemented is the H4 fixture: a human has typed
// status: implemented by hand, but the note's anchors are only partially
// aligned with the code (Create exists, Migrate does not). arch-gen must
// overwrite the field with the computed status: partial, and a subsequent
// git diff over the curated note must show the implemented → partial
// change.
func SpecStatusHandWrongImplemented() Spec {
	s := statusServiceBase("example.com/statushandwrong")
	s.Notes = []NoteSpec{{
		File: "network-lifecycle.md", Repo: "statushandwrong",
		RPCAnchors: []string{
			"kacho.cloud.vpc.v1.NetworkService/Create",
			"kacho.cloud.vpc.v1.NetworkService/Migrate",
		},
		// Hand-typed, and wrong: the anchors are only partial.
		Status:    "implemented",
		SourceSHA: "a1b2c3d",
		Body:      statusNoteBody,
	}}
	return s
}
