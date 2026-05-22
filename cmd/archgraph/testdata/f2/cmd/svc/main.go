// Command svc is an archgraph C2 fixture (scenario 4.0-F2): the repo
// carries an exported function LegacyMigrateAddresses that no
// entry-point reaches and that carries no archgraph:keep annotation, so
// C2 dead-code fails.
package main

import (
	"example.com/c2f2/grpcstub"
	"example.com/c2f2/internal/handler"
	"example.com/c2f2/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	pb.RegisterNetworkServiceServer(grpcServer, handler.New())
}
