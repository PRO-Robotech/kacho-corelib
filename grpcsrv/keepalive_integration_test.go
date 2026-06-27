// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package grpcsrv

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
)

// dialKeepaliveClient — клиент с keepalive-пингами (PermitWithoutStream=true,
// pingTime). Зеркалит idle-prone inter-service conn (authz / subject-drainer).
func dialKeepaliveClient(t *testing.T, addr string, pingTime time.Duration) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                pingTime,
			Timeout:             pingTime / 3,
			PermitWithoutStream: true,
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// callHealth — один unary RPC.
func callHealth(conn *grpc.ClientConn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hc := healthpb.NewHealthClient(conn)
	_, err := hc.Check(ctx, &healthpb.HealthCheckRequest{})
	return err
}

// serveNewServer — поднимает grpcsrv.NewServer (с built-in health SERVING +
// DefaultKeepaliveEnforcement) на новом loopback-listener'е, возвращает addr.
func serveNewServer(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := NewServer()
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)
	return lis.Addr().String()
}

// TestNewServer_AcceptsIdleKeepalive — idle keepalive behavioral test.
//
// Фабрика NewServer ставит DefaultKeepaliveEnforcement (MinTime=5s,
// PermitWithoutStream=true), который НЕ строже клиентского keepalive Time (>=6s).
// Idle-conn с keepalive-пингами держится теплым весь idle-период (> 2
// ping-интервалов): conn остается READY, GOAWAY too_many_pings не приходит,
// RPC после простоя проходит немедленно. idle-prone conn (authz/drainer)
// переживает простой без half-open-столла.
func TestNewServer_AcceptsIdleKeepalive(t *testing.T) {
	if testing.Short() {
		t.Skip("idle keepalive behavioral test — skipped in -short")
	}
	addr := serveNewServer(t)
	conn := dialKeepaliveClient(t, addr, 6*time.Second)

	require.NoError(t, callHealth(conn))
	require.Equal(t, connectivity.Ready, conn.GetState())

	time.Sleep(16 * time.Second) // > 2 ping-интервала idle, без RPC/стримов

	require.Equal(t, connectivity.Ready, conn.GetState(),
		"conn must stay READY through idle — DefaultKeepaliveEnforcement accepts pings")
	require.NoError(t, callHealth(conn),
		"RPC after idle must succeed immediately — no GOAWAY too_many_pings (KA-04b)")
}

// TestNewServer_IdleStreamSurvives — вариант с активным idle-стримом.
//
// Долгоживущий streaming-RPC (health Watch) держится открытым весь idle-период
// при keepalive-пингах: сервер из NewServer не банит пинги (нет GOAWAY,
// оборвавшего бы стрим). Зеркалит реальный idle-prone push/drainer-стрим.
func TestNewServer_IdleStreamSurvives(t *testing.T) {
	if testing.Short() {
		t.Skip("idle keepalive behavioral test — skipped in -short")
	}
	addr := serveNewServer(t)
	conn := dialKeepaliveClient(t, addr, 6*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := healthpb.NewHealthClient(conn).Watch(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	_, err = stream.Recv() // первый статус
	require.NoError(t, err)

	recvErr := make(chan error, 1)
	go func() { _, e := stream.Recv(); recvErr <- e }() // блок: апдейт или обрыв

	select {
	case e := <-recvErr:
		t.Fatalf("idle stream aborted — NewServer must accept idle pings, got: %v", e)
	case <-time.After(16 * time.Second):
		// стрим жив весь idle-период — пинги приняты сервером
	}
	require.Equal(t, connectivity.Ready, conn.GetState())
}
