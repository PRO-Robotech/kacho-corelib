// Command svc is an archgraph C1 fixture: a service registering one gRPC
// service (NetworkService, two methods) and starting one background
// worker. Its three entry-points are fully and uniquely anchored by a
// single L2 note, so C1 completeness passes.
package main

import (
	"context"

	"example.com/c1e1/grpcstub"
	"example.com/c1e1/internal/handler"
	"example.com/c1e1/internal/worker"
	"example.com/c1e1/pb"
)

func main() {
	ctx := context.Background()

	grpcServer := grpcstub.NewServer()
	pb.RegisterNetworkServiceServer(grpcServer, handler.New())

	w := worker.NewNetworkOutboxWorker()
	go w.Run(ctx)

	<-ctx.Done()
}
