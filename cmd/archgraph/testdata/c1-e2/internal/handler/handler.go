// Package handler holds the gRPC NetworkService implementation.
package handler

// Handler implements pb.NetworkServiceServer.
type Handler struct{}

// New returns a fresh Handler.
func New() *Handler { return &Handler{} }

// Create implements the NetworkService Create RPC.
func (h *Handler) Create() {}

// Update implements the NetworkService Update RPC.
func (h *Handler) Update() {}

// Delete implements the NetworkService Delete RPC.
func (h *Handler) Delete() {}
