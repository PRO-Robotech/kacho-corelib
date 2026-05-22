// Package handler implements the NetworkService gRPC handler of the
// reach-basic fixture.
//
// The call-graph the reachability pass must recover:
//
//	(*NetworkHandler).Create -> validateSpec -> (*repo.NetworkRepo).Insert
//
// unusedHelper is defined but never called from any entry-point; the
// reachability pass must leave it out of every reachable-set.
package handler

import "example.com/reachbasic/internal/repo"

// NetworkHandler implements pb.NetworkServiceServer.
type NetworkHandler struct {
	repo *repo.NetworkRepo
}

// New returns a fresh NetworkHandler wired to a repo.
func New() *NetworkHandler { return &NetworkHandler{repo: repo.New()} }

// Create handles the Create RPC. It is the rpc entry-point root.
func (h *NetworkHandler) Create() {
	h.validateSpec()
	h.repo.Insert()
}

// validateSpec validates a Network spec. Reachable from Create.
func (h *NetworkHandler) validateSpec() {}

// unusedHelper is never called from any entry-point — dead code.
func (h *NetworkHandler) unusedHelper() {}
