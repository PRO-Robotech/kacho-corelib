// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package validate

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/ids"
)

// allIDPrefixes enumerates EVERY canonical 3-char resource/operation prefix
// live in the platform, so the guard test below can assert each is a member of
// `resourceIDPrefixes`. The family-agnostic `ResourceID` validator returns
// InvalidArgument for any unknown 3-char prefix; a prefix that is live in some
// service but missing from `resourceIDPrefixes` would make every well-formed id
// of that family 400 at the api-gateway authz edge.
//
//   - ids.Prefix* — enumerated from kacho-corelib/ids (compiler-checked: a new
//     prefix constant added there but not referenced here is invisible, but a
//     constant renamed/removed breaks this test, prompting a re-audit).
//   - IAM domain prefixes — mirrored as string literals from the IAM domain
//     constants (corelib is UPSTREAM of kacho-iam in the build graph and
//     `internal/` is not importable, so the IAM prefixes cannot be imported;
//     they are duplicated here as the single source-of-truth pointer).
//     cag/cond/soc/evt are deliberately excluded: they use the
//     `<prefix>_<17-char>` underscore format (not the 3-char ids.NewID shape)
//     and are not exposed on the public REST resource surface gated by authz.
var allIDPrefixes = func() map[string]string {
	m := map[string]string{
		// kacho-corelib/ids — resource prefixes
		"PrefixCloud":              ids.PrefixCloud,
		"PrefixFolder":             ids.PrefixFolder,
		"PrefixOrganization":       ids.PrefixOrganization,
		"PrefixNetwork":            ids.PrefixNetwork,
		"PrefixSubnet":             ids.PrefixSubnet,
		"PrefixAddress":            ids.PrefixAddress,
		"PrefixRouteTable":         ids.PrefixRouteTable,
		"PrefixSecurityGroup":      ids.PrefixSecurityGroup,
		"PrefixGateway":            ids.PrefixGateway,
		"PrefixNetworkInterface":   ids.PrefixNetworkInterface,
		"PrefixAddressPool":        ids.PrefixAddressPool,
		"PrefixAnycastPool":        ids.PrefixAnycastPool,
		"PrefixInstance":           ids.PrefixInstance,
		"PrefixDisk":               ids.PrefixDisk,
		"PrefixImage":              ids.PrefixImage,
		"PrefixSnapshot":           ids.PrefixSnapshot,
		"PrefixLoadBalancer":       ids.PrefixLoadBalancer,
		"PrefixListener":           ids.PrefixListener,
		"PrefixTargetGroup":        ids.PrefixTargetGroup,
		"PrefixGlobalLoadBalancer": ids.PrefixGlobalLoadBalancer,
		// kacho-corelib/ids — per-domain operation prefixes
		"PrefixOperationRM":      ids.PrefixOperationRM,
		"PrefixOperationVPC":     ids.PrefixOperationVPC,
		"PrefixOperationCompute": ids.PrefixOperationCompute,
		"PrefixOperationNLB":     ids.PrefixOperationNLB,
		// IAM domain constants (mirrored literals)
		"iam.PrefixAccount":        "acc",
		"iam.PrefixProject":        "prj",
		"iam.PrefixUser":           "usr",
		"iam.PrefixServiceAccount": "sva",
		"iam.PrefixGroup":          "grp",
		"iam.PrefixRole":           "rol",
		"iam.PrefixAccessBinding":  "acb",
		"iam.PrefixOperationIAM":   "iop",
	}
	return m
}()

// TestResourceID_GuardEveryLivePrefixIsKnown is the regression guard ensuring a
// future new resource/operation prefix that lands in kacho-corelib/ids (or a new
// IAM resource) is also registered in `resourceIDPrefixes`, otherwise every
// well-formed id of that family would be rejected with InvalidArgument (400) at
// the gateway authz edge instead of reaching the owner service.
func TestResourceID_GuardEveryLivePrefixIsKnown(t *testing.T) {
	for name, prefix := range allIDPrefixes {
		if len(prefix) != 3 {
			t.Fatalf("%s = %q: canonical resource prefix must be exactly 3 chars", name, prefix)
		}
		if _, ok := resourceIDPrefixes[prefix]; !ok {
			t.Errorf("%s = %q is a live platform prefix but missing from resourceIDPrefixes — "+
				"well-formed %q ids would 400 at the authz edge instead of routing to their owner service",
				name, prefix, prefix)
		}
	}
}

// TestResourceID_KnownPrefixesAcceptValid asserts a well-formed id with each
// newly added (and previously known) prefix passes ResourceID (returns nil) —
// family-agnostic acceptance.
func TestResourceID_KnownPrefixesAcceptValid(t *testing.T) {
	body := strings.Repeat("0", 17)
	cases := []struct {
		name   string
		prefix string
	}{
		// IAM
		{"account", "acc"},
		{"project", "prj"},
		{"user", "usr"},
		{"service account", "sva"},
		{"group", "grp"},
		{"role", "rol"},
		{"access binding", "acb"},
		{"iam operation", "iop"},
		// nlb
		{"load balancer", "nlb"},
		{"listener", "lst"},
		{"target group", "tgr"},
		{"global load balancer", "glb"},
		// previously known (regression)
		{"network", "net"},
		{"subnet", "sub"},
		{"vpc operation", "enp"},
		{"instance", "epd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.prefix + body
			if err := ResourceID(tc.name, tc.prefix, id); err != nil {
				t.Errorf("ResourceID(%q, _, %q) = %v, want nil (well-formed known-prefix id)", tc.name, id, err)
			}
		})
	}
}

// TestResourceID_MalformedRejected asserts an unknown 3-char prefix and a too
// short id both produce InvalidArgument with the flat-message contract.
func TestResourceID_MalformedRejected(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"unknown prefix", "zzz00000000000000000"},
		{"wrong-prefix word", "not-a-valid-acb-id-at-all-verylongstring"},
		{"too short", "ab"},
		{"two chars", "ac"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ResourceID("access binding", "acb", tc.id)
			if err == nil {
				t.Fatalf("ResourceID(_, _, %q) = nil, want InvalidArgument", tc.id)
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Fatalf("ResourceID(_, _, %q) code = %v, want InvalidArgument", tc.id, got)
			}
			if msg := status.Convert(err).Message(); !strings.Contains(msg, "invalid access binding id") {
				t.Errorf("ResourceID(_, _, %q) message = %q, want contains %q", tc.id, msg, "invalid access binding id")
			}
		})
	}
}

// TestResourceID_EmptyPasses — empty id is intentionally not rejected here
// (required-check / transcoding routing handled separately).
func TestResourceID_EmptyPasses(t *testing.T) {
	if err := ResourceID("access binding", "acb", ""); err != nil {
		t.Errorf("ResourceID(_, _, \"\") = %v, want nil", err)
	}
}
