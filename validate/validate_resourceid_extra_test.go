// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package validate

import "testing"

// parseResourceIDPrefixes normalises a comma-separated env value into the set of
// meaningful (exactly-3-char) id prefixes; malformed tokens are dropped.
func TestParseResourceIDPrefixes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
		{"single", "xyz", []string{"xyz"}},
		{"multi with spaces", " xyz , abc ", []string{"xyz", "abc"}},
		{"drops non-3-char", "xyz,ab,abcd,", []string{"xyz"}},
		{"lowercases", "XYZ", []string{"xyz"}},
		{"dedup ignored (set semantics upstream)", "xyz,xyz", []string{"xyz", "xyz"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseResourceIDPrefixes(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("parseResourceIDPrefixes(%q) = %v, want %v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("parseResourceIDPrefixes(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
				}
			}
		})
	}
}

// buildResourceIDPrefixes must include the base platform prefixes AND any extra
// prefixes supplied via config — so a new domain can be routed at the authz edge
// without a corelib release (only an env/config change on the gateway).
func TestBuildResourceIDPrefixes_MergesExtras(t *testing.T) {
	m := buildResourceIDPrefixes("xyz,qqq")

	// Base prefix still present.
	if _, ok := m["net"]; !ok {
		t.Errorf("base prefix 'net' missing from built set")
	}
	// Extras merged.
	for _, p := range []string{"xyz", "qqq"} {
		if _, ok := m[p]; !ok {
			t.Errorf("extra prefix %q not merged into built set", p)
		}
	}
}

// A resource id whose prefix was supplied only via the extra-config path must be
// accepted by the family-agnostic check that ResourceID relies on.
func TestBuildResourceIDPrefixes_ExtraPrefixAccepted(t *testing.T) {
	m := buildResourceIDPrefixes("zzz")
	id := "zzz_deadbeef"
	if _, ok := m[id[:3]]; !ok {
		t.Errorf("id %q with config-supplied prefix must be recognised", id)
	}
}

// Empty config leaves exactly the base set (no accidental widening).
func TestBuildResourceIDPrefixes_EmptyConfigIsBaseOnly(t *testing.T) {
	base := buildResourceIDPrefixes("")
	if _, ok := base["zzz"]; ok {
		t.Errorf("unexpected prefix 'zzz' present with empty config")
	}
	if _, ok := base["net"]; !ok {
		t.Errorf("base prefix 'net' missing with empty config")
	}
}
