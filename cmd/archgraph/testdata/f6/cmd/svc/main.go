// Command svc is an archgraph C2 fixture (scenario 4.0-F6): an exported
// method is invoked exclusively through reflect.Value.Call. RTA does not
// model reflection, so C2 reports the method as dead code — an expected
// imprecision cured by an `// archgraph:keep` annotation, which the F6
// test injects in its second (continuation) phase.
package main

import (
	"reflect"

	"example.com/c2f6/grpcstub"
	"example.com/c2f6/internal/handler"
	"example.com/c2f6/internal/registry"
	"example.com/c2f6/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	pb.RegisterNetworkServiceServer(grpcServer, handler.New())

	// ReflectInvoke is called only through reflection — RTA cannot see
	// this edge, so C2 reports it as dead until annotated.
	d := registry.New()
	reflect.ValueOf(d).MethodByName("ReflectInvoke").Call(nil)
}
