// Package sdk holds the client-SDK builder of the C2-F5 fixture.
//
// BuildClientSDK is `// archgraph:keep`-annotated and unreachable from
// every entry-point. It calls the private assembleSDKManifest, which in
// turn calls the exported EncodeManifest. Because C2 treats a kept
// symbol as an extra reachability root, neither assembleSDKManifest nor
// EncodeManifest is reported as dead code.
package sdk

// archgraph:keep public SDK surface, consumed by external clients
func BuildClientSDK() {
	assembleSDKManifest()
}

// assembleSDKManifest is private; reachable only via BuildClientSDK.
func assembleSDKManifest() {
	EncodeManifest()
}

// EncodeManifest is exported; reachable only transitively from the kept
// BuildClientSDK. Without keep-transitivity C2 would flag it as dead.
func EncodeManifest() {}
