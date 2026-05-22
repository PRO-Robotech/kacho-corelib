// Command svc is an archgraph C2 fixture (scenario 4.0-F3): the repo
// carries an exported function BuildClientSDK that no entry-point
// reaches, but it is annotated with `// archgraph:keep <reason>`, so C2
// dead-code passes and reports BuildClientSDK as kept.
package main

import (
	"example.com/c2f3/grpcstub"
	"example.com/c2f3/internal/handler"
	"example.com/c2f3/pb"
)

func main() {
	grpcServer := grpcstub.NewServer()
	pb.RegisterNetworkServiceServer(grpcServer, handler.New())
}
