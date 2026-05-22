// Package pb is an archgraph fixture stub whose ServiceDesc has NO
// ServiceName string literal: the field is initialised from a value
// computed at runtime, so archgraph cannot resolve the proto FQN and
// must fail with a precise error.
package pb

import "example.com/svcgrpcnosvcname/grpcstub"

// serviceName is computed, not a literal in the composite literal — so
// the ServiceName field of the descriptor is not a string literal.
func serviceName() string { return "kacho.cloud.vpc.v1.NetworkService" }

// NetworkServiceServer is the server interface for NetworkService.
type NetworkServiceServer interface {
	Create()
}

// RegisterNetworkServiceServer registers the NetworkService.
func RegisterNetworkServiceServer(s grpcstub.ServiceRegistrar, srv NetworkServiceServer) {
	s.RegisterService(&NetworkService_ServiceDesc, srv)
}

func _NetworkService_Create_Handler() {}

// NetworkService_ServiceDesc lacks a ServiceName string literal.
var NetworkService_ServiceDesc = grpcstub.ServiceDesc{
	ServiceName: serviceName(),
	HandlerType: (*NetworkServiceServer)(nil),
	Methods: []grpcstub.MethodDesc{
		{MethodName: "Create", Handler: _NetworkService_Create_Handler},
	},
	Metadata: "kacho/cloud/vpc/v1/network.proto",
}
