// Command svc is an archgraph C1 fixture: NetworkService exposes three
// RPCs (Create, Update, Delete) but the L2 note anchors only two — the
// Update entry-point is undocumented, so C1 completeness must FAIL.
package main

import (
	"example.com/c1e2/grpcstub"
	"example.com/c1e2/internal/handler"
	"example.com/c1e2/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	pb.RegisterNetworkServiceServer(grpcServer, handler.New())
}
