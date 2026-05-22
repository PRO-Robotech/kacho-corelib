// Command svc is an archgraph C1 fixture: NetworkService exposes Create
// and Delete only, but the L2 note also anchors a Patch RPC that does not
// exist in the code — C1 must flag Patch as a stale anchor.
package main

import (
	"example.com/c1e3/grpcstub"
	"example.com/c1e3/internal/handler"
	"example.com/c1e3/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	pb.RegisterNetworkServiceServer(grpcServer, handler.New())
}
