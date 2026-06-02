package grpcclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestKeepaliveParams_Defaults — FD-1: Time==10s, Timeout==Time/3 (KA-05/KA-H2).
func TestKeepaliveParams_Defaults(t *testing.T) {
	for _, permit := range []bool{true, false} {
		p := KeepaliveParams(permit)
		require.Equal(t, 10*time.Second, p.Time, "Time must be 10s (FD-1)")
		require.Equal(t, 10*time.Second/3, p.Timeout, "Timeout must be Time/3 (FD-1)")
		require.Equal(t, permit, p.PermitWithoutStream,
			"PermitWithoutStream must propagate the argument (FD-2)")
	}
}

// TestKeepaliveParams_TimeoutIsThirdOfTime — инвариант «timeout = треть интервала».
func TestKeepaliveParams_TimeoutIsThirdOfTime(t *testing.T) {
	p := KeepaliveParams(false)
	require.Equal(t, p.Time/3, p.Timeout)
}

// TestKeepaliveDialOption_NotNil — helper отдаёт валидный grpc.DialOption.
func TestKeepaliveDialOption_NotNil(t *testing.T) {
	require.NotNil(t, KeepaliveDialOption(true))
	require.NotNil(t, KeepaliveDialOption(false))
}
