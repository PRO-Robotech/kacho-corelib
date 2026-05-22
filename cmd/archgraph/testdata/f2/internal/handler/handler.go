// Package handler implements the NetworkService gRPC handler of the
// C2-F2 fixture.
package handler

import "example.com/c2f2/internal/repo"

// Handler implements pb.NetworkServiceServer.
type Handler struct {
	repo *repo.Repo
}

// New returns a fresh Handler wired to a repo.
func New() *Handler { return &Handler{repo: repo.New()} }

// Create handles the Create RPC. It is the rpc entry-point root.
func (h *Handler) Create() {
	h.repo.Insert()
}
