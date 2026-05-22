package archtest

// This file is the catalogue of named fixture Specs shared by every
// archgraph test suite (cmd/archgraph, entrypoints, reach, check). Each
// constructor returns the Spec for one acceptance scenario; the suites
// call BuildRepo on it. Keeping the Specs here — not duplicated per test
// package — means one fixture is described in exactly one place.

// --- group B / reach / C1 / C2: gRPC service fixtures ----------------------

// SpecGRPCBasic is a service registering one gRPC service (NetworkService,
// four methods) via RegisterNetworkServiceServer. Used by B1/B7.
func SpecGRPCBasic() Spec {
	return Spec{
		Module: "example.com/svcgrpcbasic",
		Services: []ServiceSpec{{
			FQN:         "kacho.cloud.vpc.v1.NetworkService",
			Methods:     []string{"Create", "Get", "List", "Delete"},
			HandlerType: "NetworkHandler",
		}},
	}
}

// SpecGRPCMulti registers two services — a public and an internal one —
// each backed by its own concrete handler. Used by B2.
func SpecGRPCMulti() Spec {
	return Spec{
		Module: "example.com/svcgrpcmulti",
		Services: []ServiceSpec{
			{
				FQN:         "kacho.cloud.vpc.v1.NetworkService",
				Methods:     []string{"Create", "Get"},
				HandlerType: "NetworkHandler",
				Constructor: "NewNetwork",
			},
			{
				FQN:         "kacho.cloud.vpc.v1.InternalNetworkService",
				Methods:     []string{"ReportState", "Sweep"},
				HandlerType: "InternalNetworkHandler",
				Constructor: "NewInternal",
			},
		},
	}
}

// SpecGRPCNoServiceName is a service whose ServiceDesc.ServiceName is
// computed at runtime, not a string literal — archgraph cannot resolve the
// FQN. The handler is inline in main so no default handler package is
// emitted. Used by B7.
func SpecGRPCNoServiceName() Spec {
	return Spec{
		Module: "example.com/svcgrpcnosvcname",
		Services: []ServiceSpec{{
			FQN:           "kacho.cloud.vpc.v1.NetworkService",
			Methods:       []string{"Create"},
			NoServiceName: true,
			NoHandler:     true,
		}},
		ExtraMainImports: []string{
			"example.com/svcgrpcnosvcname/grpcstub",
			"example.com/svcgrpcnosvcname/pb",
		},
		MainBody: `grpcServer := grpcstub.NewServer()
pb.RegisterNetworkServiceServer(grpcServer, networkHandler{})`,
		Files: map[string]string{
			// An inline handler type, kept in the main package, so the
			// fixture compiles without a handler package.
			"cmd/svc/handler.go": "package main\n\n" +
				"// networkHandler is an inline NetworkServiceServer impl.\n" +
				"type networkHandler struct{}\n\n" +
				"func (networkHandler) Create() {}\n",
		},
	}
}

// --- group B: worker fixtures ----------------------------------------------

// SpecWorker starts one background worker via the New<Name> + go Run(ctx)
// convention. Used by B3.
func SpecWorker() Spec {
	return Spec{
		Module: "example.com/svcworker",
		Workers: []WorkerSpec{
			{Package: "worker", TypeName: "NetworkOutboxWorker", Method: "Run"},
		},
	}
}

// SpecReconcilerCron starts a reconciler (Run) and a cron job (Start),
// each via the chained New<Name>(...).Run/Start convention. Used by B4.
func SpecReconcilerCron() Spec {
	return Spec{
		Module: "example.com/svcreconcilercron",
		Workers: []WorkerSpec{
			{Package: "reconciler", TypeName: "InstanceReconciler", Method: "Run"},
			{Package: "cron", TypeName: "SnapshotCronJob", Method: "Start"},
		},
	}
}

// SpecWorkerNonConventional starts a worker WITHOUT the recognised
// convention — a private type with a private run method, no constructor.
// archgraph must not inventory it but must emit a hint. Used by B5.
func SpecWorkerNonConventional() Spec {
	return Spec{
		Module: "example.com/svcworkernonconv",
		NoMain: true,
		Files:  map[string]string{"cmd/svc/main.go": nonConventionalMain("svcworkernonconv")},
	}
}

// nonConventionalMain renders a main package that starts a private worker
// type via a private run method — the shape B5/E5 exercise.
func nonConventionalMain(_ string) string {
	return `// Command svc starts a background worker WITHOUT the recognised
// New<Name> + Run/Start convention.
package main

import "context"

// networkOutboxWorker is a private worker type — deliberately not
// matching the New<Name> + Run/Start convention.
type networkOutboxWorker struct{}

// run is a private method — deliberately not Run/Start.
func (w *networkOutboxWorker) run(ctx context.Context) {
	<-ctx.Done()
}

func main() {
	ctx := context.Background()

	go (&networkOutboxWorker{}).run(ctx)

	<-ctx.Done()
}
`
}

// --- group B / C / F: library & valid repos --------------------------------

// SpecLibraryRepo is a pure library module — one stringutil package, no
// main. archgraph classifies it as a library. Used by B6, C, F7.
func SpecLibraryRepo() Spec {
	return Spec{
		Module: "example.com/libraryrepo",
		NoMain: true,
		Files: map[string]string{
			"stringutil/stringutil.go": `// Package stringutil is the sole package of a library repo: no main
// package, hence no entry-points. archgraph classifies such a repo as a
// library and reports zero entry-points discovered.
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

// SpecValidRepo is a minimal, well-formed module with one trivial main
// package. Used by the CLI A3 happy-path scenario.
func SpecValidRepo() Spec {
	return Spec{
		Module:           "example.com/validrepo",
		NoMain:           true,
		ExtraMainImports: nil,
		Files: map[string]string{
			"cmd/svc/main.go": `// Command svc is a minimal valid main package used as an archgraph fixture.
package main

import "fmt"

func main() {
	fmt.Println("validrepo svc")
}
`,
		},
	}
}

// SpecCompileError is a module whose main package references an undefined
// identifier, so it fails to type-check. Used by the CLI A5 fail-fast
// scenario.
func SpecCompileError() Spec {
	return Spec{
		Module: "example.com/compileerror",
		NoMain: true,
		Files: map[string]string{
			"cmd/svc/main.go": `// Command svc is an intentionally broken main package used as an
// archgraph fixture: it references an undefined identifier so the package
// fails to type-check.
package main

func main() {
	// thisIdentifierDoesNotExist is undefined on purpose.
	thisIdentifierDoesNotExist()
}
`,
		},
	}
}
