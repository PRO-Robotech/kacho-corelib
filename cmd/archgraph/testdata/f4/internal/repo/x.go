// Package repo holds an unreachable exported symbol of the C2-F4
// fixture. The `// archgraph:keep` comment on line 9 is bare — it
// carries no reason — so C2 rejects it as an invalid annotation rather
// than honouring it as a suppression.
package repo

// LegacyHelper is exported and unreachable from every entry-point.

// archgraph:keep
func LegacyHelper() {}
