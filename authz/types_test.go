// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package authz

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Permission поле — drop-in metadata для будущего fine-grained Check;
// interceptor его не читает, struct-literal без него должен оставаться
// валидным (backward compat для уже заполненных PermissionMap'ов).
func TestRPCEntry_PermissionZeroValue(t *testing.T) {
	e := RPCEntry{
		Relation: "viewer",
		Extract: StaticExtractor("vpc_network", func(req any) (string, error) {
			return "enp00000000000000000", nil
		}),
	}
	require.Equal(t, "", e.Permission,
		"zero-value Permission означает «пока не каталогизировано»")
}

func TestRPCEntry_PermissionSet(t *testing.T) {
	e := RPCEntry{
		Relation:   "editor",
		Permission: "loadbalancer.networkLoadBalancers.start",
		Extract: StaticExtractor("nlb_load_balancer", func(req any) (string, error) {
			return "nlb00000000000000000", nil
		}),
	}
	require.Equal(t, "loadbalancer.networkLoadBalancers.start", e.Permission)
	require.Equal(t, "editor", e.Relation,
		"Relation остается источником истины для текущего interceptor'а")
}

// TestFormatSubject_RejectsDelimiterInjection — FormatSubject не должен
// конкатенировать principalID, содержащий FGA-разделители (':', '#', '@',
// whitespace), в subject-строку: иначе 'usr_x#member' стал бы userset-ссылкой,
// 'usr_x:usr_y' сдвинул бы границу id. Такой id — вырожденный/attack-случай →
// мапится в неподбираемый sentinel 'unknown' (fail-closed), симметрично
// FormatObject, который отвергает те же символы.
func TestFormatSubject_RejectsDelimiterInjection(t *testing.T) {
	cases := []struct {
		name string
		typ  string
		id   string
	}{
		{"hash-userset", "user", "usr_alice#member"},
		{"colon-boundary", "user", "usr_x:usr_y"},
		{"space", "user", "usr_x y"},
		{"tab", "service_account", "sva_x\ty"},
		{"newline", "user", "usr_x\ny"},
		{"at", "user", "usr_x@evil"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatSubject(tc.typ, tc.id)
			require.NotContains(t, got, tc.id,
				"raw injectable id must not appear verbatim in the subject")
			require.Equal(t, tc.typ+":unknown", got,
				"malformed principal id must map to the fail-closed sentinel")
		})
	}
}

// TestFormatSubject_AcceptsValid — well-formed principal id проходит без изменений.
func TestFormatSubject_AcceptsValid(t *testing.T) {
	require.Equal(t, "user:usr_alice", FormatSubject("user", "usr_alice"))
	require.Equal(t, "service_account:sva_x", FormatSubject("service_account", "sva_x"))
	// Неизвестный type → fallback на user:, id валидный.
	require.Equal(t, "user:bootstrap", FormatSubject("system", "bootstrap"))
}

func TestRPCMap_LookupPreservesPermission(t *testing.T) {
	m := RPCMap{
		"/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Start": {
			Relation:   "editor",
			Permission: "loadbalancer.networkLoadBalancers.start",
		},
	}
	e, ok := m.Lookup("/kacho.cloud.loadbalancer.v1.NetworkLoadBalancerService/Start")
	require.True(t, ok)
	require.Equal(t, "loadbalancer.networkLoadBalancers.start", e.Permission)
}
