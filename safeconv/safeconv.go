// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package safeconv provides bounds-checked numeric conversions.
//
// Go's plain conversion `int32(x)` silently wraps on overflow, which
// static analysers (gosec G115) flag as an integer-overflow hazard. The
// helpers here make the truncation explicit and saturate to the target
// type's range instead of wrapping — so a malformed/oversized request
// can never produce a negative or aliased value downstream.
//
// Used by every kacho service that maps int64 proto fields (page sizes,
// counts) onto narrower internal types, so it lives in corelib.
package safeconv

import "math"

// ClampInt32 saturates an int64 into the int32 range. Values below
// math.MinInt32 clamp to math.MinInt32; values above math.MaxInt32 clamp
// to math.MaxInt32. This is the safe replacement for a bare `int32(v)`.
func ClampInt32(v int64) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

// ClampNonNegInt32 saturates an int64 into the [0, math.MaxInt32] range.
// Intended for page sizes and counts that must never be negative.
func ClampNonNegInt32(v int64) int32 {
	if v < 0 {
		return 0
	}
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}

// IntToInt32 saturates a platform int into the int32 range.
func IntToInt32(v int) int32 {
	return ClampInt32(int64(v))
}

// IntToUint32 saturates a platform int into the [0, math.MaxUint32] range,
// flooring negatives at 0.
func IntToUint32(v int) uint32 {
	if v < 0 {
		return 0
	}
	if int64(v) > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(v)
}
