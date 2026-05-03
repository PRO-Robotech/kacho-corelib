package grpcsrv

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// NewServer создаёт gRPC-сервер с зарегистрированными Health-сервисом в состоянии SERVING
// и server-reflection (для grpcurl, debug, dev-tooling).
func NewServer(opts ...grpc.ServerOption) *grpc.Server {
	s := grpc.NewServer(opts...)
	h := health.NewServer()
	h.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s, h)
	reflection.Register(s)
	return s
}
