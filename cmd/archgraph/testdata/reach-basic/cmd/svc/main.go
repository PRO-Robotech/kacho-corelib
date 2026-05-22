// Command svc is an archgraph reach-basic fixture: a service whose main
// package registers one gRPC service. The reachability pass walks the
// call-graph from (*handler.NetworkHandler).Create.
package main

import (
	"example.com/reachbasic/grpcstub"
	"example.com/reachbasic/internal/handler"
	"example.com/reachbasic/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	networkHandler := handler.New()
	pb.RegisterNetworkServiceServer(grpcServer, networkHandler)
}
