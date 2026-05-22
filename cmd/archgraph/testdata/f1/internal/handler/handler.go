// Package handler implements the NetworkService gRPC handler of the
// C2-F1 fixture. Every exported symbol here is reachable from Create.
package handler

import "example.com/c2f1/internal/repo"

// Handler implements pb.NetworkServiceServer.
type Handler struct {
	repo *repo.Repo
}

// New returns a fresh Handler wired to a repo. Reachable: called by main.
func New() *Handler { return &Handler{repo: repo.New()} }

// Create handles the Create RPC. It is the rpc entry-point root.
func (h *Handler) Create() {
	h.validateSpec()
	h.repo.Insert()
}

// validateSpec validates a Network spec. Private, reachable from Create.
func (h *Handler) validateSpec() {}
