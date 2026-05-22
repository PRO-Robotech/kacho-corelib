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
// then emits one rpc EntryPoint per method.
func discoverRPC(mp *packages.Package, idx *index) ([]EntryPoint, error) {
	var entries []EntryPoint

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
			eps, err := resolveRegisterCall(fn, idx)
			if err != nil {
				walkErr = err
				return false
			}
			entries = append(entries, eps...)
			return true
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	return entries, nil
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
func resolveRegisterCall(fn *types.Func, idx *index) ([]EntryPoint, error) {
	decl := idx.funcDecls[fn]
	if decl == nil || decl.Body == nil {
		return nil, fmt.Errorf("%w for %s: registration function body unavailable",
			ErrUnresolvedFQN, fn.Name())
	}
	pkg := idx.funcPkg[fn]

	descObj := findServiceDescObject(decl, pkg.TypesInfo)
	if descObj == nil {
		return nil, fmt.Errorf(
			"%w for %s: no RegisterService(&ServiceDesc, ...) call in body",
			ErrUnresolvedFQN, fn.Name())
	}

	site, ok := idx.varSpecs[descObj]
	if !ok || site.idx >= len(site.spec.Values) {
		return nil, fmt.Errorf("%w for %s: ServiceDesc %s declaration unavailable",
			ErrUnresolvedFQN, fn.Name(), descObj.Name())
	}
	lit, ok := site.spec.Values[site.idx].(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("%w for %s: ServiceDesc %s is not a composite literal",
			ErrUnresolvedFQN, fn.Name(), descObj.Name())
	}

	serviceName, methods, err := readServiceDesc(lit)
	if err != nil {
		return nil, fmt.Errorf("%w for %s: %s", ErrUnresolvedFQN, fn.Name(), err)
	}

	entries := make([]EntryPoint, 0, len(methods))
	for _, m := range methods {
		entries = append(entries, EntryPoint{
			Kind: KindRPC,
			Name: serviceName + "/" + m,
			// The reachability root of an rpc entry-point is the
			// RegisterXxxServer function: Task 4 walks from it into the
			// registered handler implementation. fn is stable and always
			// resolvable, unlike the per-method handler which the stub
			// references only as an unexported package-level symbol.
			Root: fn,
		})
	}
	return entries, nil
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
