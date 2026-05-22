// Package pb is a hand-written stand-in for a protoc-gen-go-grpc
// generated stub: a RegisterXxxServer function whose body calls
// RegisterService with a package-level ServiceDesc value carrying the
// proto service FQN as a ServiceName string literal.
package pb

import "example.com/reachbasic/grpcstub"

// NetworkServiceServer is the server interface for NetworkService.
type NetworkServiceServer interface {
	Create()
}

// RegisterNetworkServiceServer registers srv as the NetworkService
// implementation.
func RegisterNetworkServiceServer(s grpcstub.ServiceRegistrar, srv NetworkServiceServer) {
	s.RegisterService(&NetworkService_ServiceDesc, srv)
}

func _NetworkService_Create_Handler() {}

// NetworkService_ServiceDesc is the gRPC service descriptor.
var NetworkService_ServiceDesc = grpcstub.ServiceDesc{
	ServiceName: "kacho.cloud.vpc.v1.NetworkService",
	HandlerType: (*NetworkServiceServer)(nil),
	Methods: []grpcstub.MethodDesc{
		{MethodName: "Create", Handler: _NetworkService_Create_Handler},
	},
	Streams:  []grpcstub.StreamDesc{},
	Metadata: "kacho/cloud/vpc/v1/network.proto",
}
