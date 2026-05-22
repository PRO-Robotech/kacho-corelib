// Command svc is an archgraph C1 fixture: NetworkService exposes Create
// and Delete, both anchored by one L2 note. A second L2 note has an empty
// anchors list (status: planned) and must be ignored by C1 — its
// emptiness must not cause a completeness failure.
package main

import (
	"example.com/c1e6/grpcstub"
	"example.com/c1e6/internal/handler"
	"example.com/c1e6/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	pb.RegisterNetworkServiceServer(grpcServer, handler.New())
}
