// Command svc is an archgraph C2 fixture (scenario 4.0-F5): an
// `// archgraph:keep`-annotated function transitively reaches further
// symbols (a private helper and an exported helper). C2 treats the kept
// function as an extra reachability root, so nothing transitively
// reachable from it is reported as dead code.
package main

import (
	"example.com/c2f5/grpcstub"
	"example.com/c2f5/internal/handler"
	"example.com/c2f5/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	pb.RegisterNetworkServiceServer(grpcServer, handler.New())
}
