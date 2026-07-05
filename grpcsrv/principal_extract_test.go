// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package grpcsrv_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/PRO-Robotech/kacho-corelib/grpcsrv"
	"github.com/PRO-Robotech/kacho-corelib/operations"
)

func TestPrincipalExtract_Headers_PropagateToCtx(t *testing.T) {
	md := metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-alice",
		grpcsrv.MDKeyPrincipalDisplay, "alice@example.com",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	called := false
	handler := func(ctx context.Context, _ any) (any, error) {
		called = true
		p := operations.PrincipalFromContext(ctx)
		assert.Equal(t, "user", p.Type)
		assert.Equal(t, "usr-alice", p.ID)
		assert.Equal(t, "alice@example.com", p.DisplayName)
		return nil, nil
	}
	_, err := grpcsrv.UnaryPrincipalExtract()(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	require.NoError(t, err)
	assert.True(t, called)
}

func TestPrincipalExtract_NoHeaders_FallbackSystem(t *testing.T) {
	ctx := context.Background()
	called := false
	handler := func(ctx context.Context, _ any) (any, error) {
		called = true
		p := operations.PrincipalFromContext(ctx)
		// SystemPrincipal по умолчанию
		assert.Equal(t, "system", p.Type)
		assert.Equal(t, "bootstrap", p.ID)
		return nil, nil
	}
	_, err := grpcsrv.UnaryPrincipalExtract()(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	require.NoError(t, err)
	assert.True(t, called)
}

// TestPrincipalExtract_DebugRedactsSensitiveMetadata — CWE-532: с включённым
// debug (через опцию, не env) dump incoming metadata НЕ должен выводить значение
// чувствительного заголовка (authorization/token/...) — только его маскированную
// форму. Не-секретные принципал-заголовки должны логироваться как есть.
func TestPrincipalExtract_DebugRedactsSensitiveMetadata(t *testing.T) {
	const secret = "Bearer super-secret-jwt-value-xyz"
	md := metadata.Pairs(
		grpcsrv.MDKeyPrincipalType, "user",
		grpcsrv.MDKeyPrincipalID, "usr-alice",
		"authorization", secret,
		"x-api-key", "key-abc-123",
	)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	handler := func(context.Context, any) (any, error) { return nil, nil }
	_, err := grpcsrv.UnaryPrincipalExtract(
		grpcsrv.WithPrincipalDebug(true),
		grpcsrv.WithPrincipalDebugLogger(logger),
	)(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	require.NoError(t, err)

	out := buf.String()
	assert.NotContains(t, out, "super-secret-jwt-value-xyz", "authorization value must be redacted from logs")
	assert.NotContains(t, out, "key-abc-123", "api-key value must be redacted from logs")
	assert.Contains(t, out, "<redacted>", "sensitive values must be masked")
	// Non-sensitive principal id remains visible for troubleshooting.
	assert.Contains(t, out, "usr-alice")
}

// TestPrincipalExtract_DebugOffByDefault — без опции/env debug-дампа быть не должно.
func TestPrincipalExtract_DebugOffByDefault(t *testing.T) {
	md := metadata.Pairs(grpcsrv.MDKeyPrincipalType, "user", grpcsrv.MDKeyPrincipalID, "usr-bob")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	handler := func(context.Context, any) (any, error) { return nil, nil }
	_, err := grpcsrv.UnaryPrincipalExtract(grpcsrv.WithPrincipalDebugLogger(logger))(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	require.NoError(t, err)
	assert.NotContains(t, strings.ToLower(buf.String()), "incoming metadata")
}

func TestPrincipalExtract_PartialHeaders_NoOp(t *testing.T) {
	// Only Type set without ID — treated as missing.
	md := metadata.Pairs(grpcsrv.MDKeyPrincipalType, "user")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	handler := func(ctx context.Context, _ any) (any, error) {
		p := operations.PrincipalFromContext(ctx)
		assert.Equal(t, "system", p.Type)
		return nil, nil
	}
	_, err := grpcsrv.UnaryPrincipalExtract()(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	require.NoError(t, err)
}
