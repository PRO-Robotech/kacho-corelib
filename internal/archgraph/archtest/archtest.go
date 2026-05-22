// Package archtest is the fixture builder for archgraph's test suites.
//
// archgraph analyses a Go repository whole, through golang.org/x/tools/go/
// packages, so every test fixture must be a real, self-contained Go module
// on disk. Before this package each fixture was a checked-in directory
// under cmd/archgraph/testdata; the boilerplate scaffold (an offline
// grpcstub package, a go.mod, a protoc-gen-go-grpc stub, a main.go and a
// handler) was copy-pasted across ~24 of them.
//
// BuildRepo writes a complete minimal module into t.TempDir() from a
// declarative Spec. The duplicated scaffold — the grpcstub package, the
// go.mod, the gRPC stub and the main-package wiring — is emitted from a
// single source here, so it lives in exactly one place. Scenario-specific
// files (business-logic reach chains, compile errors, reflection cases)
// are supplied verbatim through Spec.Files.
//
// archtest depends only on the standard library and the testing package;
// it is a testing helper, so t.Fatalf on a malformed Spec is appropriate.
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Spec declares a synthetic fixture repository.
//
// The builder emits the duplicated scaffold (go.mod, the grpcstub package,
// per-service gRPC stubs, the main package and a default handler) from the
// structural fields, and writes Files verbatim for everything that does
// not fit a structural pattern (compile-error fixtures, hand-tailored
// reachability chains, reflection cases, library packages).
type Spec struct {
	// Module is the Go module path written into go.mod, e.g.
	// "example.com/svcgrpcbasic". Required.
	Module string

	// Services declares gRPC services. For each one the builder emits a
	// protoc-gen-go-grpc-shaped stub under pb/ and, unless the service
	// opts out with NoHandler, a default handler type plus the
	// RegisterXxxServer wiring in main.go.
	Services []ServiceSpec

	// Workers declares background workers started from main.go through
	// the New<Type>(...) + go (Run|Start) convention.
	Workers []WorkerSpec

	// Notes declares L2 architecture notes written under docs/arch/.
	Notes []NoteSpec

	// MainBody, when non-empty, replaces the auto-generated body of
	// main(). The builder still emits the package clause, the import
	// block (deduced from Services/Workers plus ExtraMainImports) and the
	// func main() { ... } wrapper. Use it for fixtures whose main wires
	// things in a non-standard way (e.g. a reflection call).
	MainBody string

	// ExtraMainImports are import paths added to main.go's import block,
	// needed when MainBody references packages the builder cannot deduce.
	ExtraMainImports []string

	// NoMain suppresses the main package entirely, producing a library
	// repo. A library Spec typically carries only Files.
	NoMain bool

	// Files are written verbatim, keyed by repo-relative slash path. They
	// are emitted after the structural scaffold and may add packages the
	// structural fields do not model. A Files entry never overwrites a
	// structural file silently — a collision fails the test.
	Files map[string]string
}

// ServiceSpec declares one gRPC service exposed by the fixture.
type ServiceSpec struct {
	// FQN is the dotted proto service name, e.g.
	// "kacho.cloud.vpc.v1.NetworkService". It becomes the
	// ServiceDesc.ServiceName string literal archgraph's FQN resolver
	// reads. When NoServiceName is set the FQN is still used to name the
	// stub identifiers but is NOT emitted as a literal.
	FQN string

	// Methods are the RPC method names, e.g. {"Create", "Get"}.
	Methods []string

	// HandlerType is the concrete handler type name emitted in package
	// handler, e.g. "NetworkHandler". Defaults to "Handler".
	HandlerType string

	// Constructor is the handler constructor name in package handler,
	// e.g. "New" or "NewNetwork". Defaults to "New".
	Constructor string

	// NoHandler suppresses both the default handler package and the
	// RegisterXxxServer call in main.go for this service. Use it when the
	// handler is supplied through Files (the f1..f6 / reach fixtures) or
	// when main.go uses an inline handler type (MainBody).
	NoHandler bool

	// NoServiceName emits a ServiceDesc whose ServiceName is computed by
	// a helper func rather than a string literal, so archgraph cannot
	// resolve the FQN. Exercises the B7 failure path.
	NoServiceName bool
}

// WorkerSpec declares a background worker entry-point.
type WorkerSpec struct {
	// Package is the internal package the worker type lives in, e.g.
	// "worker", "reconciler", "cron". The builder writes
	// internal/<Package>/<Package>.go.
	Package string

	// TypeName is the worker struct type, e.g. "NetworkOutboxWorker".
	TypeName string

	// Method is the goroutine root method, "Run" or "Start". Defaults to
	// "Run". The constructor is always New<TypeName>, matching the
	// archgraph New<Name> + Run/Start convention.
	Method string
}

// NoteSpec declares an L2 architecture note written under docs/arch/.
type NoteSpec struct {
	// File is the note's base name, e.g. "network-lifecycle.md".
	File string

	// Repo is the note's frontmatter repo field.
	Repo string

	// RPCAnchors are "<service-FQN>/<method>" anchor strings.
	RPCAnchors []string

	// WorkerAnchors are worker-type-name anchor strings.
	WorkerAnchors []string

	// Status is the note's frontmatter status, e.g. "implemented" or
	// "planned". Defaults to "implemented".
	Status string

	// SourceSHA is the note's frontmatter source_sha. A bare "" on a
	// non-planned note defaults to "a1b2c3d"; "" on a planned note
	// stays empty. Two sentinels override that: FreshSHA marks a note
	// whose source_sha the C3 test rewrites with the genuine
	// reachable-set hash; EmptySHA forces an empty source_sha on an
	// anchored, non-planned note (the G5 missing-source_sha case).
	SourceSHA string

	// Body, when non-empty, replaces the default Markdown body emitted
	// after the frontmatter.
	Body string
}

// BuildRepo materialises spec into a fresh t.TempDir() and returns the
// absolute path to the module root. The directory is removed automatically
// when the test finishes.
func BuildRepo(t *testing.T, spec Spec) string {
	t.Helper()
	if spec.Module == "" {
		t.Fatalf("archtest: Spec.Module is required")
	}

	root := t.TempDir()
	files := map[string]string{}

	put := func(path, content string) {
		if _, dup := files[path]; dup {
			t.Fatalf("archtest: file %q emitted twice for module %s", path, spec.Module)
		}
		files[path] = content
	}

	// go.mod — emitted once, from a single template.
	put("go.mod", goMod(spec.Module))

	usesGRPC := len(spec.Services) > 0

	// The offline grpcstub package — emitted from one constant, only
	// when the fixture actually has a gRPC service.
	if usesGRPC {
		put("grpcstub/grpcstub.go", grpcStubSource)
	}

	// All services of a fixture share one pb package file, emitted as a
	// whole from a single source.
	if usesGRPC {
		put("pb/network_grpc.pb.go", pbSource(spec))
	}

	// Default handler package, unless every service opts out.
	if h := handlerSource(spec); h != "" {
		put("internal/handler/handler.go", h)
	}

	// Worker packages.
	for _, w := range spec.Workers {
		pkg := workerPackage(w)
		put("internal/"+pkg+"/"+pkg+".go", workerSource(w))
	}

	// Notes.
	for _, n := range spec.Notes {
		put("docs/arch/"+n.File, noteSource(n))
	}

	// The main package.
	if !spec.NoMain {
		put("cmd/svc/main.go", mainSource(spec))
	}

	// Verbatim Files last — they may add packages the structural fields
	// do not model, but must not collide with a structural file.
	paths := make([]string, 0, len(spec.Files))
	for p := range spec.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		put(filepath.ToSlash(p), spec.Files[p])
	}

	writeAll(t, root, files)
	return root
}

// writeAll materialises files (keyed by slash path) under root.
func writeAll(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("archtest: mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("archtest: write %s: %v", rel, err)
		}
	}
}

// goMod renders the go.mod of a fixture module. The go directive is pinned
// at 1.21 so the fixture loads under GOTOOLCHAIN=local on any Go >= 1.21.
func goMod(module string) string {
	return fmt.Sprintf(`module %s

// archgraph fixture: go-directive kept at 1.21 so the fixture loads under
// GOTOOLCHAIN=local on any Go >= 1.21 — do not bump.
go 1.21
`, module)
}

// grpcStubSource is the single source of the offline google.golang.org/grpc
// stand-in. Every gRPC fixture embeds exactly this package.
const grpcStubSource = `// Package grpcstub is a self-contained, offline stand-in for
// google.golang.org/grpc, carrying exactly the surface a
// protoc-gen-go-grpc stub touches: ServiceRegistrar, ServiceDesc,
// MethodDesc and StreamDesc. archgraph fixtures import this instead of the
// real module so they load under GOTOOLCHAIN=local with no network.
package grpcstub

// ServiceRegistrar is the registration sink passed to RegisterXxxServer.
type ServiceRegistrar interface {
	RegisterService(desc *ServiceDesc, impl any)
}

// MethodDesc describes one unary gRPC method.
type MethodDesc struct {
	MethodName string
	Handler    any
}

// StreamDesc describes one streaming gRPC method.
type StreamDesc struct {
	StreamName    string
	Handler       any
	ServerStreams bool
	ClientStreams bool
}

// ServiceDesc is the per-service registration descriptor. Its shape
// mirrors google.golang.org/grpc.ServiceDesc closely enough that
// archgraph's FQN resolver exercises the real walk.
type ServiceDesc struct {
	ServiceName string
	HandlerType any
	Methods     []MethodDesc
	Streams     []StreamDesc
	Metadata    any
}

// Server is a trivial ServiceRegistrar implementation.
type Server struct{}

// NewServer returns a fresh Server.
func NewServer() *Server { return &Server{} }

// RegisterService records a service registration (no-op for the fixture).
func (s *Server) RegisterService(_ *ServiceDesc, _ any) {}
`

// serviceIdent is the bare Go identifier of a service FQN — the last
// dotted segment, e.g. "NetworkService" for
// "kacho.cloud.vpc.v1.NetworkService".
func serviceIdent(fqn string) string {
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		return fqn[i+1:]
	}
	return fqn
}

// handlerType returns the concrete handler type name of svc.
func handlerType(svc ServiceSpec) string {
	if svc.HandlerType != "" {
		return svc.HandlerType
	}
	return "Handler"
}

// handlerCtor returns the handler constructor name of svc.
func handlerCtor(svc ServiceSpec) string {
	if svc.Constructor != "" {
		return svc.Constructor
	}
	return "New"
}
