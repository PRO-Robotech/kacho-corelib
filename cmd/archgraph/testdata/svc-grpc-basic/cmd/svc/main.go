// Command svc is an archgraph fixture: a service whose main package
// registers one gRPC service via RegisterNetworkServiceServer.
package main

import (
	"example.com/svcgrpcbasic/grpcstub"
	"example.com/svcgrpcbasic/internal/handler"
	"example.com/svcgrpcbasic/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	networkHandler := handler.New()
	pb.RegisterNetworkServiceServer(grpcServer, networkHandler)
}
