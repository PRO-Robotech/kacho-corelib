// Package sdk holds the client-SDK builder of the C2-F3 fixture.
//
// BuildClientSDK is exported and unreachable from every entry-point, so
// without annotation C2 would flag it as dead code. The
// `// archgraph:keep` comment directly above its declaration carries a
// non-empty reason, so C2 treats it as intentionally kept, not dead.
package sdk

// archgraph:keep public SDK surface, consumed by external clients
func BuildClientSDK() {}
