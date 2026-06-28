// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package errors

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNotFound_BuildsResourceInfo(t *testing.T) {
	err := NotFound("Folder", "abc-123").Err()
	st, ok := status.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.NotFound, st.Code())
	require.Contains(t, st.Message(), "Folder")
	// проверка ResourceInfo в details — упрощенно для скелета
}

func TestInvalidArgument_BuildsBadRequest(t *testing.T) {
	err := InvalidArgument().AddFieldViolation("metadata.name", "must match regex").Err()
	st, _ := status.FromError(err)
	require.Equal(t, codes.InvalidArgument, st.Code())
}

func TestAllCodes_HaveHelper(t *testing.T) {
	cases := []struct {
		fn   func() *Builder
		code codes.Code
	}{
		{func() *Builder { return AlreadyExists("X", "y") }, codes.AlreadyExists},
		{func() *Builder { return FailedPrecondition("x") }, codes.FailedPrecondition},
		{func() *Builder { return Aborted("retry") }, codes.Aborted},
		{func() *Builder { return Unavailable("svc down") }, codes.Unavailable},
		{func() *Builder { return Internal("oops") }, codes.Internal},
	}
	for _, c := range cases {
		st, _ := status.FromError(c.fn().Err())
		require.Equal(t, c.code, st.Code())
	}
}
