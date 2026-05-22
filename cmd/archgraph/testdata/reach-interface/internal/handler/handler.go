// Package handler implements the NetworkService gRPC handler of the
// reach-interface fixture.
//
// The handler depends on the NetworkRepo port interface, not on a
// concrete type. The reachability pass — running RTA — must follow the
// interface dispatch (*NetworkHandler).Create -> NetworkRepo.Insert to
// the concrete (*repo.pgNetworkRepo).Insert, because a value of that
// concrete type is constructed in reachable code (handler.New).
package handler

import "example.com/reachinterface/internal/repo"

// NetworkRepo is the service-layer port interface for Network
// persistence. The handler is wired against this port; the concrete
// implementation lives in package repo.
type NetworkRepo interface {
	Insert()
}

// NetworkHandler implements pb.NetworkServiceServer.
type NetworkHandler struct {
	repo NetworkRepo
}

// New returns a fresh NetworkHandler wired to the concrete repo. The
// concrete *repo.pgNetworkRepo value is constructed here, in code
// reachable from main, so RTA can resolve the interface dispatch.
func New() *NetworkHandler { return &NetworkHandler{repo: repo.New()} }

// Create handles the Create RPC. It calls Insert through the NetworkRepo
// port interface — RTA resolves the dynamic call to the concrete type.
func (h *NetworkHandler) Create() {
	h.repo.Insert()
}
