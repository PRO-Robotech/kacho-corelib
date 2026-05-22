// Package repo is the persistence adapter of the reach-interface
// fixture: a concrete Postgres-backed implementation of the
// service-layer NetworkRepo port.
package repo

// pgNetworkRepo is the concrete, unexported Postgres implementation of
// the service-layer NetworkRepo port interface. RTA must place its
// Insert method in the reachable-set: a value of this type is
// constructed by New, which is called from a reachable code path, so
// the interface dispatch in the handler can land here.
type pgNetworkRepo struct{}

// New constructs the concrete repository and returns it. The return
// type is the unexported concrete type so the constructed type is
// visible to RTA at the construction site.
func New() *pgNetworkRepo { return &pgNetworkRepo{} }

// Insert writes a Network row. Reached only through the NetworkRepo
// interface dispatch in the handler.
func (r *pgNetworkRepo) Insert() {}
