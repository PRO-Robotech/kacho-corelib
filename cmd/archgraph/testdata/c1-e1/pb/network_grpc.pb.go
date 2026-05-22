// Package pb is a hand-written stand-in for a protoc-gen-go-grpc
// generated stub. NetworkService here exposes exactly two methods —
// Create and Delete — so the C1 fixture's entry-point set is small and
// fully matched by its L2 note.
package pb

import "example.com/c1e1/grpcstub"

// NetworkServiceServer is the server interface for NetworkService.
type NetworkServiceServer interface {
	Create()
	Delete()
}

// RegisterNetworkServiceServer registers srv as the NetworkService
// implementation.
func RegisterNetworkServiceServer(s grpcstub.ServiceRegistrar, srv NetworkServiceServer) {
	s.RegisterService(&NetworkService_ServiceDesc, srv)
}

func _NetworkService_Create_Handler() {}
func _NetworkService_Delete_Handler() {}

// NetworkService_ServiceDesc is the gRPC service descriptor.
var NetworkService_ServiceDesc = grpcstub.ServiceDesc{
	ServiceName: "kacho.cloud.vpc.v1.NetworkService",
	HandlerType: (*NetworkServiceServer)(nil),
	Methods: []grpcstub.MethodDesc{
		{MethodName: "Create", Handler: _NetworkService_Create_Handler},
		{MethodName: "Delete", Handler: _NetworkService_Delete_Handler},
	},
	Streams:  []grpcstub.StreamDesc{},
	Metadata: "kacho/cloud/vpc/v1/network.proto",
}
