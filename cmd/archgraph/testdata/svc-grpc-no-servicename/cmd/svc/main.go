// Command svc is an archgraph fixture whose gRPC stub cannot be
// FQN-resolved (no ServiceName string literal).
package main

import (
	"example.com/svcgrpcnosvcname/grpcstub"
	"example.com/svcgrpcnosvcname/pb"
)

type networkHandler struct{}

func (networkHandler) Create() {}

func main() {
	grpcServer := grpcstub.NewServer()
	pb.RegisterNetworkServiceServer(grpcServer, networkHandler{})
}
