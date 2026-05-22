// Command svc is an archgraph C2 fixture (scenario 4.0-F4): an
// unreachable exported symbol carries a bare `// archgraph:keep`
// annotation with no reason, which C2 rejects as an invalid annotation.
package main

import (
	"example.com/c2f4/grpcstub"
	"example.com/c2f4/internal/handler"
	"example.com/c2f4/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	pb.RegisterNetworkServiceServer(grpcServer, handler.New())
}
