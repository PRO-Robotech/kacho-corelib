// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package grpcsrv_test

import (
	"context"
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
