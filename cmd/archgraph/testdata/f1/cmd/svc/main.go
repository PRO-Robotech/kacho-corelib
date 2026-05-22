// Command svc is an archgraph C2 fixture (scenario 4.0-F1): every
// exported function and method of the repo is reachable from the single
// gRPC entry-point, so C2 dead-code passes.
package main

import (
	"example.com/c2f1/grpcstub"
	"example.com/c2f1/internal/handler"
	"example.com/c2f1/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	pb.RegisterNetworkServiceServer(grpcServer, handler.New())
}
