// Package handler implements the NetworkService gRPC handler of the
// C2-F5 fixture.
package handler

// Handler implements pb.NetworkServiceServer.
type Handler struct{}

// New returns a fresh Handler.
func New() *Handler { return &Handler{} }

// Create handles the Create RPC. It is the rpc entry-point root.
func (h *Handler) Create() {}
