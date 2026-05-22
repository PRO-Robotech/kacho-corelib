// Command svc is an archgraph reach-interface fixture: a service whose
// handler calls its repository through a port interface.
package main

import (
	"example.com/reachinterface/grpcstub"
	"example.com/reachinterface/internal/handler"
	"example.com/reachinterface/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	networkHandler := handler.New()
	pb.RegisterNetworkServiceServer(grpcServer, networkHandler)
}
