// Package pb is a hand-written protoc-gen-go-grpc stub stand-in.
// NetworkService here exposes two methods (Create, Delete); the C1
// fixture's L2 note additionally anchors a non-existent Patch RPC.
package pb

import "example.com/c1e3/grpcstub"

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
