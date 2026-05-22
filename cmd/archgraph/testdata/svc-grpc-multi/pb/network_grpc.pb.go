// Package pb is a hand-written stand-in for protoc-gen-go-grpc output
// carrying two services: the public NetworkService and the
// InternalNetworkService. archgraph does not distinguish public from
// internal — both must reach the inventory, each FQN-resolved from its
// own ServiceDesc.
package pb

import "example.com/svcgrpcmulti/grpcstub"

// NetworkServiceServer is the public NetworkService server interface.
type NetworkServiceServer interface {
	Create()
	Get()
}

// RegisterNetworkServiceServer registers the public NetworkService.
func RegisterNetworkServiceServer(s grpcstub.ServiceRegistrar, srv NetworkServiceServer) {
	s.RegisterService(&NetworkService_ServiceDesc, srv)
}

func _NetworkService_Create_Handler() {}
func _NetworkService_Get_Handler()    {}

// NetworkService_ServiceDesc is the public NetworkService descriptor.
var NetworkService_ServiceDesc = grpcstub.ServiceDesc{
	ServiceName: "kacho.cloud.vpc.v1.NetworkService",
	HandlerType: (*NetworkServiceServer)(nil),
	Methods: []grpcstub.MethodDesc{
		{MethodName: "Create", Handler: _NetworkService_Create_Handler},
		{MethodName: "Get", Handler: _NetworkService_Get_Handler},
	},
	Metadata: "kacho/cloud/vpc/v1/network.proto",
}

// InternalNetworkServiceServer is the internal NetworkService interface.
type InternalNetworkServiceServer interface {
	ReportState()
	Sweep()
}

// RegisterInternalNetworkServiceServer registers the internal service.
func RegisterInternalNetworkServiceServer(s grpcstub.ServiceRegistrar, srv InternalNetworkServiceServer) {
	s.RegisterService(&InternalNetworkService_ServiceDesc, srv)
}

func _InternalNetworkService_ReportState_Handler() {}
func _InternalNetworkService_Sweep_Handler()       {}

// InternalNetworkService_ServiceDesc is the internal service descriptor.
var InternalNetworkService_ServiceDesc = grpcstub.ServiceDesc{
	ServiceName: "kacho.cloud.vpc.v1.InternalNetworkService",
	HandlerType: (*InternalNetworkServiceServer)(nil),
	Methods: []grpcstub.MethodDesc{
		{MethodName: "ReportState", Handler: _InternalNetworkService_ReportState_Handler},
		{MethodName: "Sweep", Handler: _InternalNetworkService_Sweep_Handler},
	},
	Metadata: "kacho/cloud/vpc/v1/network_internal.proto",
}
