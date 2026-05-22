// Package grpcstub is a self-contained, offline stand-in for
// google.golang.org/grpc. It carries exactly the surface a
// protoc-gen-go-grpc stub touches: ServiceRegistrar, ServiceDesc,
// MethodDesc and StreamDesc. archgraph fixtures import this instead of
// the real module so they load under GOTOOLCHAIN=local with no network.
package grpcstub

// ServiceRegistrar is the registration sink passed to RegisterXxxServer.
type ServiceRegistrar interface {
	RegisterService(desc *ServiceDesc, impl any)
}

// MethodDesc describes one unary gRPC method.
type MethodDesc struct {
	MethodName string
	Handler    any
}

// StreamDesc describes one streaming gRPC method.
type StreamDesc struct {
	StreamName    string
	Handler       any
	ServerStreams bool
	ClientStreams bool
}

// ServiceDesc is the per-service registration descriptor. Its shape
// mirrors google.golang.org/grpc.ServiceDesc closely enough that
// archgraph's FQN resolver exercises the real walk.
type ServiceDesc struct {
	ServiceName string
	HandlerType any
	Methods     []MethodDesc
	Streams     []StreamDesc
	Metadata    any
}

// Server is a trivial ServiceRegistrar implementation.
type Server struct{}

// NewServer returns a fresh Server.
func NewServer() *Server { return &Server{} }

// RegisterService records a service registration (no-op for the fixture).
func (s *Server) RegisterService(_ *ServiceDesc, _ any) {}
