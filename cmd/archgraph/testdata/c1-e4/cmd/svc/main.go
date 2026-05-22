// Command svc is an archgraph C1 fixture: NetworkService exposes Create
// and Delete; two L2 notes both anchor Create, so C1 must flag Create as
// anchored in more than one note.
package main

import (
	"example.com/c1e4/grpcstub"
	"example.com/c1e4/internal/handler"
	"example.com/c1e4/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	pb.RegisterNetworkServiceServer(grpcServer, handler.New())
}
