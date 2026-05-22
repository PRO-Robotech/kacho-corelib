// Package handler implements the NetworkService gRPC handler.
package handler

// NetworkHandler implements pb.NetworkServiceServer.
type NetworkHandler struct{}

// New returns a fresh NetworkHandler.
func New() *NetworkHandler { return &NetworkHandler{} }

// Create handles the Create RPC.
func (h *NetworkHandler) Create() {}

// Get handles the Get RPC.
func (h *NetworkHandler) Get() {}

// List handles the List RPC.
func (h *NetworkHandler) List() {}

// Delete handles the Delete RPC.
func (h *NetworkHandler) Delete() {}
