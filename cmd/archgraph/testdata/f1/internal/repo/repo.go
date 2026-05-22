// Package repo is the persistence adapter of the C2-F1 fixture.
package repo

// Repo persists Network rows.
type Repo struct{}

// New returns a fresh Repo. Reachable: called by handler.New.
func New() *Repo { return &Repo{} }

// Insert writes a Network row. Reachable: called by (*Handler).Create.
func (r *Repo) Insert() {
	r.encodeRow()
}

// encodeRow is a private helper reachable transitively from Insert.
func (r *Repo) encodeRow() {}
