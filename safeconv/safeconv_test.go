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

func TestIntToUint(t *testing.T) {
	if got := IntToUint(-5); got != 0 {
		t.Errorf("IntToUint(-5) = %d, want 0", got)
	}
	if got := IntToUint(7); got != 7 {
		t.Errorf("IntToUint(7) = %d, want 7", got)
	}
}
