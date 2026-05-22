package entrypoints

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// discoverWorkers finds the background-worker entry-points started by
// the main package mp. A worker is recognised by the convention
//
//	w := New<Name>(...) ; go w.Run(ctx)        // bound form
//	go New<Name>(...).Run(ctx)                 // chained form
//
// where the start method is Run or Start. Goroutines that look like a
// worker but do not match the convention are not inventoried; a Hint is
// returned for each so arch-audit can surface them.
func discoverWorkers(mp *packages.Package, idx *index) ([]EntryPoint, []Hint) {
	var (
		entries []EntryPoint
		hints   []Hint
		seen    = map[string]bool{} // dedupe by worker type name
	)

	for _, file := range mp.Syntax {
		// Per-file map of local variable -> the New<Name> constructor
		// whose result it was assigned. Built from the file's
		// short-variable declarations and plain assignments.
		ctors := constructorBindings(file, mp.TypesInfo)

		ast.Inspect(file, func(n ast.Node) bool {
			goStmt, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}
			ep, hint := classifyGoroutine(goStmt, mp, idx, ctors)
			switch {
			case ep != nil:
				if !seen[ep.Name] {
					seen[ep.Name] = true
					entries = append(entries, *ep)
				}
			case hint != nil:
				hints = append(hints, *hint)
			}
			return true
		})
	}
	return entries, hints
}

// classifyGoroutine inspects one "go" statement. It returns a worker
// EntryPoint when the goroutine matches the New<Name> + Run/Start
// convention, a Hint when the goroutine calls a method of a named type
// but does not match the convention, or two nils when the goroutine is
// not worker-shaped at all.
func classifyGoroutine(
	gs *ast.GoStmt,
	mp *packages.Package,
	idx *index,
	ctors map[types.Object]*types.Func,
) (*EntryPoint, *Hint) {
	sel, ok := gs.Call.Fun.(*ast.SelectorExpr)
	if !ok {
		// `go someFunc(...)` — not a method call, not worker-shaped.
		return nil, nil
	}
	method, recvType := methodSelection(sel, mp.TypesInfo)
	if method == nil || recvType == nil {
		return nil, nil
	}
	named := namedOf(recvType)
	if named == nil {
		return nil, nil
	}

	recognised := workerStartMethods[method.Name()] &&
		constructedByConvention(sel.X, named, mp.TypesInfo, ctors)

	if recognised {
		// The reachability root is the very method handed to the
		// goroutine — Run or Start — as Task 4 walks the call-graph
		// from there.
		return &EntryPoint{
			Kind: KindWorker,
			Name: named.Obj().Name(),
			Root: method,
		}, nil
	}

	// A goroutine that calls a method of a named type but did not match
	// the convention: surface it for a human, do not inventory it.
	pos := mp.Fset.Position(gs.Pos())
	return nil, &Hint{
		Message: fmt.Sprintf(
			"hint: possible unrecognized worker entry-point at %s:%d — "+
				"see archgraph entry-point convention",
			pos.Filename, pos.Line),
	}
}

// constructorBindings maps each local variable that was assigned the
// result of a New<Name>(...) call to that constructor function. It
// scans both `:=` short declarations and `=` assignments in file.
func constructorBindings(file *ast.File, info *types.Info) map[types.Object]*types.Func {
	out := map[types.Object]*types.Func{}
	ast.Inspect(file, func(n ast.Node) bool {
		asn, ok := n.(*ast.AssignStmt)
		if !ok || len(asn.Lhs) != len(asn.Rhs) {
			return true
		}
		for i, rhs := range asn.Rhs {
			ctor := constructorOf(rhs, info)
			if ctor == nil {
				continue
			}
			lhsID, ok := asn.Lhs[i].(*ast.Ident)
			if !ok {
				continue
			}
			// `:=` defines the object (Defs); `=` reuses it (Uses).
			obj := info.Defs[lhsID]
			if obj == nil {
				obj = info.Uses[lhsID]
			}
			if obj != nil {
				out[obj] = ctor
			}
		}
		return true
	})
	return out
}

// constructorOf returns the New<Name> function a call expression
// invokes, or nil when expr is not a call of a function whose name has
// the New<Name> shape.
func constructorOf(expr ast.Expr, info *types.Info) *types.Func {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil
	}
	fn := calleeFunc(call, info)
	if fn == nil || !isConstructorName(fn.Name()) {
		return nil
	}
	return fn
}

// isConstructorName reports whether name has the New<Name> shape with a
// non-empty suffix.
func isConstructorName(name string) bool {
	return strings.HasPrefix(name, "New") && len(name) > len("New")
}

// constructedByConvention reports whether the receiver expression of a
// Run/Start call denotes a worker built by the New<Name> convention —
// either directly (a chained New<Name>(...).Run call) or via a local
// variable bound to a New<Name> result. It also checks the constructor
// name matches the worker type ("New" + type name). info is the main
// package's type info, which records both forms of receiver.
func constructedByConvention(
	recv ast.Expr,
	named *types.Named,
	info *types.Info,
	ctors map[types.Object]*types.Func,
) bool {
	var ctor *types.Func
	switch r := recv.(type) {
	case *ast.CallExpr:
		// Chained form: New<Name>(...).Run(...).
		ctor = constructorOf(r, info)
	case *ast.Ident:
		// Bound form: w := New<Name>(...) ; go w.Run(...).
		if obj := info.Uses[r]; obj != nil {
			ctor = ctors[obj]
		}
	case *ast.ParenExpr:
		// Parenthesised receiver: (New<Name>(...)).Run(...).
		return constructedByConvention(r.X, named, info, ctors)
	}
	if ctor == nil {
		return false
	}
	return ctor.Name() == "New"+named.Obj().Name()
}

// methodSelection resolves a selector expression to the method it
// names and the receiver type the method is selected on. It returns
// two nils when the selector is not a method selection.
func methodSelection(sel *ast.SelectorExpr, info *types.Info) (*types.Func, types.Type) {
	if info == nil {
		return nil, nil
	}
	s := info.Selections[sel]
	if s == nil || s.Kind() != types.MethodVal {
		return nil, nil
	}
	fn, ok := s.Obj().(*types.Func)
	if !ok {
		return nil, nil
	}
	return fn, s.Recv()
}

// namedOf unwraps a possibly-pointer type to the underlying named type,
// or returns nil when the type is not (a pointer to) a named type.
func namedOf(t types.Type) *types.Named {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, _ := t.(*types.Named)
	return named
}
