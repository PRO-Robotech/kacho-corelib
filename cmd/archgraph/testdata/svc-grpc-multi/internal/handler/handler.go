// Package handler implements both NetworkService handlers.
package handler

// NetworkHandler implements pb.NetworkServiceServer.
type NetworkHandler struct{}

// NewNetwork returns a fresh public NetworkHandler.
func NewNetwork() *NetworkHandler { return &NetworkHandler{} }

// Create handles the Create RPC.
func (h *NetworkHandler) Create() {}

// Get handles the Get RPC.
func (h *NetworkHandler) Get() {}

// InternalNetworkHandler implements pb.InternalNetworkServiceServer.
type InternalNetworkHandler struct{}

// NewInternal returns a fresh InternalNetworkHandler.
func NewInternal() *InternalNetworkHandler { return &InternalNetworkHandler{} }

// ReportState handles the ReportState RPC.
func (h *InternalNetworkHandler) ReportState() {}

// Sweep handles the Sweep RPC.
func (h *InternalNetworkHandler) Sweep() {}
