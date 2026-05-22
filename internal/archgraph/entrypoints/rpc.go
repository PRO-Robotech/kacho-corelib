package entrypoints

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// discoverRPC finds every gRPC entry-point registered by the main
// package mp. For each RegisterXxxServer call it resolves the proto
// service FQN and method list through the registered grpc.ServiceDesc,
// then emits one rpc EntryPoint per method, rooted at the concrete
// handler type's method (see resolveRegisterCall).
func discoverRPC(mp *packages.Package, idx *index) ([]EntryPoint, []Hint, error) {
	var (
		entries []EntryPoint
		hints   []Hint
	)

	for _, file := range mp.Syntax {
		var walkErr error
		ast.Inspect(file, func(n ast.Node) bool {
			if walkErr != nil {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn := calleeFunc(call, mp.TypesInfo)
			if fn == nil || !isRegisterServerName(fn.Name()) {
				return true
			}
			eps, hs, err := resolveRegisterCall(fn, call, mp, idx)
			if err != nil {
				walkErr = err
				return false
			}
			entries = append(entries, eps...)
			hints = append(hints, hs...)
			return true
		})
		if walkErr != nil {
			return nil, nil, walkErr
		}
	}
	return entries, hints, nil
}

// isRegisterServerName reports whether name has the protoc-gen-go-grpc
// registration shape "Register<Service>Server".
func isRegisterServerName(name string) bool {
	return strings.HasPrefix(name, "Register") &&
		strings.HasSuffix(name, "Server") &&
		len(name) > len("Register")+len("Server")
}

// calleeFunc resolves the function a call expression invokes, or nil if
// the callee is not a plain function/method reference.
func calleeFunc(call *ast.CallExpr, info *types.Info) *types.Func {
	if info == nil {
		return nil
	}
	var id *ast.Ident
	switch c := call.Fun.(type) {
	case *ast.Ident:
		id = c
	case *ast.SelectorExpr:
		id = c.Sel
	default:
		return nil
	}
	fn, _ := info.Uses[id].(*types.Func)
	return fn
}

// resolveRegisterCall follows a RegisterXxxServer function into its
// body, finds the grpc.ServiceDesc it registers, and emits one rpc
// EntryPoint per method declared on that descriptor.
//
// call is the RegisterXxxServer(server, handler) call site in the main
// package mp. Its second argument is the concrete handler value: its
// static type is the handler implementation, and each rpc EntryPoint is
// rooted at that type's method — the real business logic Task 4 must
// walk for reachability (C2 dead-code). When the handler type cannot be
// resolved to a concrete type carrying the method, the EntryPoint falls
// back to the RegisterXxxServer function as Root and a Hint is emitted.
func resolveRegisterCall(
	fn *types.Func,
	call *ast.CallExpr,
	mp *packages.Package,
	idx *index,
) ([]EntryPoint, []Hint, error) {
	decl := idx.funcDecls[fn]
	if decl == nil || decl.Body == nil {
		return nil, nil, fmt.Errorf("%w for %s: registration function body unavailable",
			ErrUnresolvedFQN, fn.Name())
	}
	pkg := idx.funcPkg[fn]

	descObj := findServiceDescObject(decl, pkg.TypesInfo)
	if descObj == nil {
		return nil, nil, fmt.Errorf(
			"%w for %s: no RegisterService(&ServiceDesc, ...) call in body",
			ErrUnresolvedFQN, fn.Name())
	}

	site, ok := idx.varSpecs[descObj]
	if !ok || site.idx >= len(site.spec.Values) {
		return nil, nil, fmt.Errorf("%w for %s: ServiceDesc %s declaration unavailable",
			ErrUnresolvedFQN, fn.Name(), descObj.Name())
	}
	lit, ok := site.spec.Values[site.idx].(*ast.CompositeLit)
	if !ok {
		return nil, nil, fmt.Errorf("%w for %s: ServiceDesc %s is not a composite literal",
			ErrUnresolvedFQN, fn.Name(), descObj.Name())
	}

	serviceName, methods, err := readServiceDesc(lit)
	if err != nil {
		return nil, nil, fmt.Errorf("%w for %s: %w", ErrUnresolvedFQN, fn.Name(), err)
	}

	// Resolve the concrete handler type from the call's second argument.
	// handlerType is the named type whose method set carries the
	// per-method handler implementations, and handlerPkg its owning
	// package; handlerType is nil when the handler is passed behind an
	// interface or constructed unresolvably.
	handlerType, handlerPkg := handlerArgType(call, mp.TypesInfo)

	var hints []Hint
	entries := make([]EntryPoint, 0, len(methods))
	for _, m := range methods {
		root, ok := lookupHandlerMethod(handlerType, handlerPkg, m)
		if !ok {
			// The handler type did not statically resolve to a concrete
			// type carrying method m. Fall back to the RegisterXxxServer
			// function as Root and surface a hint for arch-audit so the
			// unresolved root can be reviewed by a human.
			root = fn
			pos := mp.Fset.Position(call.Pos())
			hints = append(hints, Hint{
				Message: fmt.Sprintf(
					"hint: rpc handler root unresolved for %s/%s at %s:%d — "+
						"reachability rooted at %s registration plumbing instead",
					serviceName, m, pos.Filename, pos.Line, fn.Name()),
			})
		}
		entries = append(entries, EntryPoint{
			Kind: KindRPC,
			Name: serviceName + "/" + m,
			// The reachability root is the concrete handler type's
			// method — the real RPC business logic Task 4 walks for
			// dead-code analysis (or, on the fallback path above, the
			// RegisterXxxServer function).
			Root: root,
		})
	}
	return entries, hints, nil
}

// handlerArgType resolves the static type of the handler value passed
// as the second argument of a RegisterXxxServer(server, handler) call.
// It returns the named type carrying the handler's method set and the
// package that named type belongs to, or (nil, nil) when the handler
// argument is absent or its type is not a (pointer to a) named type
// (e.g. an interface value or an unresolvable inline construction).
func handlerArgType(call *ast.CallExpr, info *types.Info) (*types.Named, *types.Package) {
	if info == nil || len(call.Args) < 2 {
		return nil, nil
	}
	tv, ok := info.Types[call.Args[1]]
	if !ok || tv.Type == nil {
		return nil, nil
	}
	t := tv.Type
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return nil, nil
	}
	return named, named.Obj().Pkg()
}

// lookupHandlerMethod finds the *types.Func for method name on the
// concrete handler type. It probes both the value and pointer method
// sets via types.LookupFieldOrMethod. It returns (nil, false) when the
// handler type is nil or carries no such method.
func lookupHandlerMethod(
	handlerType *types.Named,
	handlerPkg *types.Package,
	name string,
) (*types.Func, bool) {
	if handlerType == nil {
		return nil, false
	}
	// addressable=true so pointer-receiver methods are included; gRPC
	// handlers conventionally use pointer receivers.
	obj, _, _ := types.LookupFieldOrMethod(handlerType, true, handlerPkg, name)
	fn, ok := obj.(*types.Func)
	if !ok {
		return nil, false
	}
	return fn, true
}

// findServiceDescObject scans a RegisterXxxServer body for a
// RegisterService(&XxxDesc, ...) call and returns the package-level var
// object XxxDesc, or nil if no such call is present.
func findServiceDescObject(decl *ast.FuncDecl, info *types.Info) types.Object {
	var found types.Object
	ast.Inspect(decl.Body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "RegisterService" || len(call.Args) == 0 {
			return true
		}
		// The first argument is &XxxDesc — a unary address-of on the
		// package-level ServiceDesc var.
		unary, ok := call.Args[0].(*ast.UnaryExpr)
		if !ok {
			return true
		}
		if obj := descVarObject(unary.X, info); obj != nil {
			found = obj
			return false
		}
		return true
	})
	return found
}

// descVarObject resolves an expression that should denote a
// package-level var (a bare identifier or pkg.Ident selector) to its
// object.
func descVarObject(expr ast.Expr, info *types.Info) types.Object {
	switch e := expr.(type) {
	case *ast.Ident:
		if v, ok := info.Uses[e].(*types.Var); ok {
			return v
		}
	case *ast.SelectorExpr:
		if v, ok := info.Uses[e.Sel].(*types.Var); ok {
			return v
		}
	}
	return nil
}

// readServiceDesc extracts the proto service FQN and the ordered method
// list from a grpc.ServiceDesc composite literal. The FQN is the
// ServiceName field's string literal; the method list is the
// MethodName / StreamName fields of the Methods and Streams slices.
func readServiceDesc(lit *ast.CompositeLit) (serviceName string, methods []string, err error) {
	var (
		haveName bool
		unary    []string
		stream   []string
	)
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "ServiceName":
			s, ok := stringLiteral(kv.Value)
			if !ok {
				return "", nil, fmt.Errorf("no ServiceName literal in ServiceDesc")
			}
			serviceName, haveName = s, true
		case "Methods":
			unary = methodNames(kv.Value, "MethodName")
		case "Streams":
			stream = methodNames(kv.Value, "StreamName")
		}
	}
	if !haveName {
		return "", nil, fmt.Errorf("no ServiceName literal in ServiceDesc")
	}
	methods = append(methods, unary...)
	methods = append(methods, stream...)
	sort.Strings(methods)
	return serviceName, methods, nil
}

// methodNames extracts the values of the named field (MethodName or
// StreamName) from every composite-literal element of a
// []grpc.MethodDesc / []grpc.StreamDesc slice literal.
func methodNames(expr ast.Expr, field string) []string {
	slice, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	var out []string
	for _, el := range slice.Elts {
		entry, ok := el.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, fe := range entry.Elts {
			kv, ok := fe.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, ok := kv.Key.(*ast.Ident)
			if !ok || k.Name != field {
				continue
			}
			if s, ok := stringLiteral(kv.Value); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// stringLiteral returns the Go-unquoted value of a basic string literal
// expression, or ok=false for any other expression (an identifier, a
// call, a concatenation — none of which archgraph can resolve to a
// proto FQN).
func stringLiteral(expr ast.Expr) (string, bool) {
	bl, ok := expr.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(bl.Value)
	if err != nil {
		return "", false
	}
	return s, true
}
