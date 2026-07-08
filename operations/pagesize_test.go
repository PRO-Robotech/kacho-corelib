// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package operations_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/operations"
)

// TestList_PageSizeConvention locks the api-conventions page_size discipline for
// OperationService.List: out-of-range page_size (negative or > MaxPageSize) must
// be a sync InvalidArgument — NOT a silent clamp to the default. Validation runs
// before any DB access, so a nil-pool repo exercises the rejection path without
// Postgres.
func TestList_PageSizeConvention(t *testing.T) {
	t.Parallel()

	repo := operations.NewRepo(nil, "public")

	cases := []struct {
		name        string
		pageSize    int64
		wantInvalid bool
	}{
		{name: "negative-rejected", pageSize: -1, wantInvalid: true},
		{name: "over-max-rejected", pageSize: 1001, wantInvalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := repo.List(context.Background(), operations.ListFilter{PageSize: tc.pageSize})
			require.Error(t, err, "out-of-range page_size must error, not clamp")
			assert.Equal(t, codes.InvalidArgument, status.Code(err),
				"out-of-range page_size must be InvalidArgument (garbage → InvalidArgument, not silent clamp)")
		})
	}
}
