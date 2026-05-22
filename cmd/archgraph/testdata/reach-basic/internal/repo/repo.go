// Package repo is the persistence adapter of the reach-basic fixture.
package repo

// NetworkRepo persists Network rows.
type NetworkRepo struct{}

// New returns a fresh NetworkRepo.
func New() *NetworkRepo { return &NetworkRepo{} }

// Insert writes a Network row. It is reachable: the handler's Create
// method calls it through validateSpec's caller chain.
func (r *NetworkRepo) Insert() {
	r.encodeRow()
}

// encodeRow is a private helper reachable transitively from Insert.
func (r *NetworkRepo) encodeRow() {}
