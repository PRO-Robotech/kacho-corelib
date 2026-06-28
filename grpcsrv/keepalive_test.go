// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package grpcsrv

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// grpcDefaultMinTime / grpcDefaultPermitWithoutStream — дефолтная серверная
// keepalive.EnforcementPolicy в gRPC-go (internal/transport: MinTime=5m,
// PermitWithoutStream=false). Именно она забанила бы idle-prone клиента с
// keepalive Time=10s, PermitWithoutStream=true → GOAWAY too_many_pings. Это
// корень бага со стороны сервера.
const (
	grpcDefaultMinTime             = 5 * time.Minute
	grpcDefaultPermitWithoutStream = false
	clientKeepaliveTime            = 10 * time.Second
)

// TestDefaultKeepaliveEnforcement — сервер допускает idle keepalive-пинги
// (MinTime <= клиентский Time=10s, PermitWithoutStream=true) — иначе GOAWAY too_many_pings.
func TestDefaultKeepaliveEnforcement(t *testing.T) {
	p := DefaultKeepaliveEnforcement()
	require.LessOrEqual(t, p.MinTime, clientKeepaliveTime,
		"server MinTime must be <= client keepalive Time (10s)")
	require.Equal(t, 5*time.Second, p.MinTime)
	require.True(t, p.PermitWithoutStream,
		"server must permit pings without active streams (idle-prone conns)")
}

// TestDefaultEnforcement_FixesGrpcDefault — детерминированный policy-level тест.
// Доказывает, что DefaultKeepaliveEnforcement ОТЛИЧАЕТСЯ от gRPC-дефолта
// в обе стороны, нужные для приема idle-пингов стандартного клиента (Time=10s):
//
//   - gRPC-дефолт (MinTime=5m > 10s) забанил бы пинги → наш MinTime ДОЛЖЕН быть
//     <= client Time (иначе too_many_pings);
//   - gRPC-дефолт (PermitWithoutStream=false) забанил бы idle-пинги без стримов →
//     наш ДОЛЖЕН быть true.
//
// Тест красный, если DefaultKeepaliveEnforcement вернет gRPC-дефолт (= нет фикса).
func TestDefaultEnforcement_FixesGrpcDefault(t *testing.T) {
	// Контроль: gRPC-дефолт несовместим со стандартным idle-клиентом.
	require.Greater(t, grpcDefaultMinTime, clientKeepaliveTime,
		"sanity: gRPC default MinTime (5m) IS stricter than client Time (10s) — that's the bug")
	require.False(t, grpcDefaultPermitWithoutStream,
		"sanity: gRPC default forbids pings without stream — that's the bug")

	// Фикс: наша policy совместима.
	p := DefaultKeepaliveEnforcement()
	require.NotEqual(t, grpcDefaultMinTime, p.MinTime,
		"fix must NOT keep gRPC default MinTime")
	require.LessOrEqual(t, p.MinTime, clientKeepaliveTime,
		"fix MinTime must accept client Time=10s pings")
	require.NotEqual(t, grpcDefaultPermitWithoutStream, p.PermitWithoutStream,
		"fix must permit pings without stream (idle-prone conns)")
}
