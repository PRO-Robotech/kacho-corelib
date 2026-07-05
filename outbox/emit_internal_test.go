// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package outbox

import "testing"

// TestSanitizeTable — identifier quoting/escaping for the table name that Emit
// interpolates into `INSERT INTO %s`. Even though the name is contractually a
// trusted literal, defense-in-depth requires it to be quoted via pgx.Identifier
// so a stray/malicious name can never become a statement-injection sink.
func TestSanitizeTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "vpc_outbox", want: `"vpc_outbox"`},
		{name: "schema-qualified", in: "kacho_iam.fga_outbox", want: `"kacho_iam"."fga_outbox"`},
		{name: "injection-attempt", in: `x(a) VALUES(1); DROP TABLE users; --`,
			want: `"x(a) VALUES(1); DROP TABLE users; --"`},
		{name: "embedded-quote", in: `a"b`, want: `"a""b"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeTable(tc.in); got != tc.want {
				t.Fatalf("SanitizeTable(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
