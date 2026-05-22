package repo

// legacy.go holds a one-off migration helper that is no longer wired
// into any entry-point of the C2-F2 fixture. archgraph C2 must report
// LegacyMigrateAddresses as dead code: it is exported, unreachable from
// every entry-point, and carries no archgraph:keep annotation.
//
// The declaration of LegacyMigrateAddresses is deliberately kept on
// line 14 so the C2 finding can assert the exact file:line position
// "internal/repo/legacy.go:14" (the line of the func keyword).

// LegacyMigrateAddresses rewrites pre-v1 address rows. Dead code: it is
// exported and unreachable from every entry-point.
func LegacyMigrateAddresses() {}
