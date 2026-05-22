// Package pb is a hand-written stand-in for a protoc-gen-go-grpc
// generated stub. It reproduces the structures archgraph's FQN resolver
// must walk: a RegisterXxxServer function whose body calls
// RegisterService with a package-level ServiceDesc value, and that
// ServiceDesc value's ServiceName string literal plus Methods/Streams.
package pb

import "example.com/svcgrpcbasic/grpcstub"

// NetworkServiceServer is the server interface for NetworkService.
type NetworkServiceServer interface {
	Create()
	Get()
	List()
	Delete()
}

// RegisterNetworkServiceServer registers srv as the NetworkService
// implementation. archgraph must NOT derive the FQN from this
// identifier; it must follow the call to RegisterService and read
// NetworkService_ServiceDesc.ServiceName.
func RegisterNetworkServiceServer(s grpcstub.ServiceRegistrar, srv NetworkServiceServer) {
	s.RegisterService(&NetworkService_ServiceDesc, srv)
}

func _NetworkService_Create_Handler() {}
func _NetworkService_Get_Handler()    {}
func _NetworkService_List_Handler()   {}
func _NetworkService_Delete_Handler() {}

// NetworkService_ServiceDesc is the gRPC service descriptor. The
// ServiceName string literal is the canonical proto FQN.
var NetworkService_ServiceDesc = grpcstub.ServiceDesc{
	ServiceName: "kacho.cloud.vpc.v1.NetworkService",
	HandlerType: (*NetworkServiceServer)(nil),
	Methods: []grpcstub.MethodDesc{
		{MethodName: "Create", Handler: _NetworkService_Create_Handler},
		{MethodName: "Get", Handler: _NetworkService_Get_Handler},
		{MethodName: "List", Handler: _NetworkService_List_Handler},
		{MethodName: "Delete", Handler: _NetworkService_Delete_Handler},
	},
	Streams:  []grpcstub.StreamDesc{},
	Metadata: "kacho/cloud/vpc/v1/network.proto",
}
