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
