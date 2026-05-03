package grpcsrv

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// NewServer создаёт gRPC-сервер с зарегистрированным Health-сервисом в состоянии SERVING.
func NewServer(opts ...grpc.ServerOption) *grpc.Server {
	s := grpc.NewServer(opts...)
	h := health.NewServer()
	h.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(s, h)
	return s
}
