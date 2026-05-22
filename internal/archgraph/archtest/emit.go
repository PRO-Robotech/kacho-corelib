package archtest

import (
	"fmt"
	"strings"
)

// pbSource renders the single pb/network_grpc.pb.go stub carrying every
// service of the Spec. The stub reproduces the structures archgraph's FQN
// resolver walks: a RegisterXxxServer function whose body calls
// RegisterService with a package-level ServiceDesc value, and that
// descriptor's ServiceName string literal plus Methods.
func pbSource(spec Spec) string {
	var b strings.Builder
	b.WriteString("// Package pb is a hand-written stand-in for a protoc-gen-go-grpc\n")
	b.WriteString("// generated stub. It reproduces the structures archgraph's FQN\n")
	b.WriteString("// resolver must walk: a RegisterXxxServer function whose body calls\n")
	b.WriteString("// RegisterService with a package-level ServiceDesc value, and that\n")
	b.WriteString("// ServiceDesc value's ServiceName plus Methods.\n")
	b.WriteString("package pb\n\n")
	fmt.Fprintf(&b, "import %q\n", spec.Module+"/grpcstub")

	for _, svc := range spec.Services {
		ident := serviceIdent(svc.FQN)
		b.WriteString("\n")

		if svc.NoServiceName {
			fmt.Fprintf(&b, "// serviceName for %s is computed, not a string literal — so\n", ident)
			b.WriteString("// archgraph cannot resolve the proto FQN and must fail.\n")
			fmt.Fprintf(&b, "func serviceName%s() string { return %q }\n\n", ident, svc.FQN)
		}

		fmt.Fprintf(&b, "// %sServer is the server interface for %s.\n", ident, ident)
		fmt.Fprintf(&b, "type %sServer interface {\n", ident)
		for _, m := range svc.Methods {
			fmt.Fprintf(&b, "\t%s()\n", m)
		}
		b.WriteString("}\n\n")

		fmt.Fprintf(&b, "// Register%sServer registers srv as the %s implementation.\n", ident, ident)
		fmt.Fprintf(&b, "func Register%sServer(s grpcstub.ServiceRegistrar, srv %sServer) {\n", ident, ident)
		fmt.Fprintf(&b, "\ts.RegisterService(&%s_ServiceDesc, srv)\n", ident)
		b.WriteString("}\n\n")

		for _, m := range svc.Methods {
			fmt.Fprintf(&b, "func _%s_%s_Handler() {}\n", ident, m)
		}
		b.WriteString("\n")

		fmt.Fprintf(&b, "// %s_ServiceDesc is the gRPC service descriptor.\n", ident)
		fmt.Fprintf(&b, "var %s_ServiceDesc = grpcstub.ServiceDesc{\n", ident)
		if svc.NoServiceName {
			fmt.Fprintf(&b, "\tServiceName: serviceName%s(),\n", ident)
		} else {
			fmt.Fprintf(&b, "\tServiceName: %q,\n", svc.FQN)
		}
		fmt.Fprintf(&b, "\tHandlerType: (*%sServer)(nil),\n", ident)
		b.WriteString("\tMethods: []grpcstub.MethodDesc{\n")
		for _, m := range svc.Methods {
			fmt.Fprintf(&b, "\t\t{MethodName: %q, Handler: _%s_%s_Handler},\n", m, ident, m)
		}
		b.WriteString("\t},\n")
		b.WriteString("\tStreams:  []grpcstub.StreamDesc{},\n")
		b.WriteString("\tMetadata: \"kacho/cloud/vpc/v1/network.proto\",\n")
		b.WriteString("}\n")
	}
	return b.String()
}

// handlerSource renders internal/handler/handler.go with one concrete
// handler type per service that did not opt out via NoHandler. Returns ""
// when no service needs a default handler.
func handlerSource(spec Spec) string {
	var svcs []ServiceSpec
	for _, s := range spec.Services {
		if !s.NoHandler {
			svcs = append(svcs, s)
		}
	}
	if len(svcs) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("// Package handler holds the gRPC service handlers of the fixture.\n")
	b.WriteString("package handler\n")
	for _, svc := range svcs {
		typ := handlerType(svc)
		ctor := handlerCtor(svc)
		ident := serviceIdent(svc.FQN)
		b.WriteString("\n")
		fmt.Fprintf(&b, "// %s implements pb.%sServer.\n", typ, ident)
		fmt.Fprintf(&b, "type %s struct{}\n\n", typ)
		fmt.Fprintf(&b, "// %s returns a fresh %s.\n", ctor, typ)
		fmt.Fprintf(&b, "func %s() *%s { return &%s{} }\n", ctor, typ, typ)
		for _, m := range svc.Methods {
			fmt.Fprintf(&b, "\n// %s handles the %s RPC.\n", m, m)
			fmt.Fprintf(&b, "func (h *%s) %s() {}\n", typ, m)
		}
	}
	return b.String()
}

// workerPackage returns the internal package directory of a worker.
func workerPackage(w WorkerSpec) string {
	if w.Package != "" {
		return w.Package
	}
	return "worker"
}

// workerMethod returns the goroutine root method name of a worker.
func workerMethod(w WorkerSpec) string {
	if w.Method != "" {
		return w.Method
	}
	return "Run"
}

// workerSource renders a worker package: a struct type, its New<Type>
// constructor and the Run/Start goroutine root, matching the archgraph
// New<Name> + Run/Start entry-point convention.
func workerSource(w WorkerSpec) string {
	pkg := workerPackage(w)
	method := workerMethod(w)
	var b strings.Builder
	fmt.Fprintf(&b, "// Package %s holds a background worker of the fixture.\n", pkg)
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("import \"context\"\n\n")
	fmt.Fprintf(&b, "// %s is an archgraph worker entry-point: constructed by\n", w.TypeName)
	fmt.Fprintf(&b, "// New%s and started via %s.\n", w.TypeName, method)
	fmt.Fprintf(&b, "type %s struct{}\n\n", w.TypeName)
	fmt.Fprintf(&b, "// New%s returns a fresh %s.\n", w.TypeName, w.TypeName)
	fmt.Fprintf(&b, "func New%s() *%s {\n\treturn &%s{}\n}\n\n", w.TypeName, w.TypeName, w.TypeName)
	fmt.Fprintf(&b, "// %s is the worker's goroutine root.\n", method)
	fmt.Fprintf(&b, "func (w *%s) %s(ctx context.Context) {\n\t<-ctx.Done()\n}\n", w.TypeName, method)
	return b.String()
}

// noteSource renders an L2 architecture note: YAML frontmatter delimited by
// --- lines followed by a Markdown body.
func noteSource(n NoteSpec) string {
	status := n.Status
	if status == "" {
		status = "implemented"
	}
	sha := n.SourceSHA
	if sha == "" && n.Status != "planned" {
		sha = "a1b2c3d"
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("level: functionality\n")
	fmt.Fprintf(&b, "repo: %s\n", n.Repo)
	if len(n.RPCAnchors) == 0 && len(n.WorkerAnchors) == 0 {
		b.WriteString("anchors: []\n")
	} else {
		b.WriteString("anchors:\n")
		for _, a := range n.RPCAnchors {
			fmt.Fprintf(&b, "  - rpc: %s\n", a)
		}
		for _, a := range n.WorkerAnchors {
			fmt.Fprintf(&b, "  - worker: %s\n", a)
		}
	}
	fmt.Fprintf(&b, "status: %s\n", status)
	if sha == "" {
		b.WriteString("source_sha: \"\"\n")
	} else {
		fmt.Fprintf(&b, "source_sha: %s\n", sha)
	}
	b.WriteString("---\n")

	if n.Body != "" {
		b.WriteString(n.Body)
		if !strings.HasSuffix(n.Body, "\n") {
			b.WriteString("\n")
		}
	} else {
		title := strings.TrimSuffix(n.File, ".md")
		fmt.Fprintf(&b, "# %s\n\nL2 architecture note for the %s fixture.\n", title, n.Repo)
	}
	return b.String()
}

// mainSource renders cmd/svc/main.go. When MainBody is set it wraps that
// body verbatim; otherwise it auto-wires every non-opted-out service
// registration and every worker goroutine.
func mainSource(spec Spec) string {
	var b strings.Builder
	b.WriteString("// Command svc is an archgraph fixture entry-point package.\n")
	b.WriteString("package main\n")

	imports := mainImports(spec)
	if len(imports) > 0 {
		b.WriteString("\nimport (\n")
		for _, imp := range imports {
			fmt.Fprintf(&b, "\t%q\n", imp)
		}
		b.WriteString(")\n")
	}

	b.WriteString("\nfunc main() {\n")
	if spec.MainBody != "" {
		body := strings.TrimRight(spec.MainBody, "\n")
		for _, line := range strings.Split(body, "\n") {
			if line == "" {
				b.WriteString("\n")
			} else {
				fmt.Fprintf(&b, "\t%s\n", line)
			}
		}
		b.WriteString("}\n")
		return b.String()
	}

	needsCtx := len(spec.Workers) > 0
	if needsCtx {
		b.WriteString("\tctx := context.Background()\n")
	}

	registered := registeredServices(spec)
	if len(registered) > 0 {
		b.WriteString("\n\tgrpcServer := grpcstub.NewServer()\n")
		for _, svc := range registered {
			ident := serviceIdent(svc.FQN)
			fmt.Fprintf(&b, "\tpb.Register%sServer(grpcServer, handler.%s())\n",
				ident, handlerCtor(svc))
		}
	}

	if len(spec.Workers) > 0 {
		b.WriteString("\n")
		for _, w := range spec.Workers {
			fmt.Fprintf(&b, "\tgo %s.New%s().%s(ctx)\n",
				workerPackage(w), w.TypeName, workerMethod(w))
		}
		b.WriteString("\n\t<-ctx.Done()\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// registeredServices returns the services main.go must register: those
// with a default handler (NoHandler unset).
func registeredServices(spec Spec) []ServiceSpec {
	var out []ServiceSpec
	for _, s := range spec.Services {
		if !s.NoHandler {
			out = append(out, s)
		}
	}
	return out
}

// mainImports computes the sorted, deduplicated import block of main.go for
// the auto-wired (non-MainBody) and MainBody cases alike.
func mainImports(spec Spec) []string {
	set := map[string]struct{}{}

	if spec.MainBody == "" {
		if len(spec.Workers) > 0 {
			set["context"] = struct{}{}
		}
		reg := registeredServices(spec)
		if len(reg) > 0 {
			set[spec.Module+"/grpcstub"] = struct{}{}
			set[spec.Module+"/handler-marker"] = struct{}{}
			set[spec.Module+"/pb"] = struct{}{}
		}
		for _, w := range spec.Workers {
			set[spec.Module+"/internal/"+workerPackage(w)] = struct{}{}
		}
	}
	for _, imp := range spec.ExtraMainImports {
		set[imp] = struct{}{}
	}

	// The handler package lives under internal/handler; the marker keeps
	// it deduplicated against an explicit ExtraMainImports entry.
	if _, ok := set[spec.Module+"/handler-marker"]; ok {
		delete(set, spec.Module+"/handler-marker")
		set[spec.Module+"/internal/handler"] = struct{}{}
	}

	out := make([]string, 0, len(set))
	for imp := range set {
		out = append(out, imp)
	}
	sortImports(out)
	return out
}

// sortImports orders stdlib imports (no dot) before module imports, each
// group sorted lexically — matching gofmt's grouping closely enough for
// the fixture to compile and read cleanly.
func sortImports(imps []string) {
	isStd := func(s string) bool { return !strings.Contains(s, ".") }
	less := func(i, j int) bool {
		si, sj := isStd(imps[i]), isStd(imps[j])
		if si != sj {
			return si
		}
		return imps[i] < imps[j]
	}
	for i := 1; i < len(imps); i++ {
		for j := i; j > 0 && less(j, j-1); j-- {
			imps[j], imps[j-1] = imps[j-1], imps[j]
		}
	}
}
