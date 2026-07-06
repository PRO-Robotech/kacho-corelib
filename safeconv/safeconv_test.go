// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package safeconv

import (
	"math"
	"testing"
)

func TestClampInt32(t *testing.T) {
	cases := []struct {
		in   int64
		want int32
	}{
		{0, 0},
		{42, 42},
		{math.MaxInt32, math.MaxInt32},
		{math.MinInt32, math.MinInt32},
		{math.MaxInt32 + 1, math.MaxInt32},
		{math.MinInt32 - 1, math.MinInt32},
		{math.MaxInt64, math.MaxInt32},
		{math.MinInt64, math.MinInt32},
	}
	for _, c := range cases {
		if got := ClampInt32(c.in); got != c.want {
			t.Errorf("ClampInt32(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestClampNonNegInt32(t *testing.T) {
	cases := []struct {
		in   int64
		want int32
	}{
		{-1, 0},
		{math.MinInt64, 0},
		{0, 0},
		{1000, 1000},
		{math.MaxInt32, math.MaxInt32},
		{math.MaxInt64, math.MaxInt32},
	}
	for _, c := range cases {
		if got := ClampNonNegInt32(c.in); got != c.want {
			t.Errorf("ClampNonNegInt32(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestIntToInt32(t *testing.T) {
	cases := []struct {
		in   int
		want int32
	}{
		{0, 0},
		{42, 42},
		{-42, -42},
		{math.MaxInt32, math.MaxInt32},
		{math.MaxInt32 + 1, math.MaxInt32},
		{math.MinInt32, math.MinInt32},
		{math.MinInt32 - 1, math.MinInt32},
		{math.MaxInt64, math.MaxInt32},
		{math.MinInt64, math.MinInt32},
	}
	for _, c := range cases {
		if got := IntToInt32(c.in); got != c.want {
			t.Errorf("IntToInt32(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestIntToUint32(t *testing.T) {
	cases := []struct {
		in   int
		want uint32
	}{
		{-1, 0},
		{0, 0},
		{1000, 1000},
		{math.MaxInt32, math.MaxInt32},
		// Upper-saturation branch (the reason this helper exists over bare
		// uint32(v)): any int above math.MaxUint32 must clamp, never wrap.
		// On a 64-bit platform int easily exceeds MaxUint32.
		{math.MaxUint32, math.MaxUint32},     // boundary: exactly the max, no clamp
		{math.MaxUint32 + 1, math.MaxUint32}, // one past max → clamp (bare uint32(v) would wrap to 0)
		{math.MaxInt64, math.MaxUint32},      // far above → clamp
	}
	for _, c := range cases {
		if got := IntToUint32(c.in); got != c.want {
			t.Errorf("IntToUint32(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
