// Command svc is an archgraph fixture: a main package registering two
// gRPC services — a public and an internal one — in the same process.
package main

import (
	"example.com/svcgrpcmulti/grpcstub"
	"example.com/svcgrpcmulti/internal/handler"
	"example.com/svcgrpcmulti/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()

	pb.RegisterNetworkServiceServer(grpcServer, handler.NewNetwork())
	pb.RegisterInternalNetworkServiceServer(grpcServer, handler.NewInternal())
}
