// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package grpcclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestKeepaliveParams_Defaults — Time==10s, Timeout==Time/3.
func TestKeepaliveParams_Defaults(t *testing.T) {
	for _, permit := range []bool{true, false} {
		p := KeepaliveParams(permit)
		require.Equal(t, 10*time.Second, p.Time, "Time must be 10s")
		require.Equal(t, 10*time.Second/3, p.Timeout, "Timeout must be Time/3")
		require.Equal(t, permit, p.PermitWithoutStream,
			"PermitWithoutStream must propagate the argument")
	}
}

// TestKeepaliveParams_TimeoutIsThirdOfTime — инвариант «timeout = треть интервала».
func TestKeepaliveParams_TimeoutIsThirdOfTime(t *testing.T) {
	p := KeepaliveParams(false)
	require.Equal(t, p.Time/3, p.Timeout)
}

// TestKeepaliveDialOption_NotNil — helper отдает валидный grpc.DialOption.
func TestKeepaliveDialOption_NotNil(t *testing.T) {
	require.NotNil(t, KeepaliveDialOption(true))
	require.NotNil(t, KeepaliveDialOption(false))
}
