// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package drainer_test

// Unit tests for the error-classification function.
//
// The classifier is the single, testable decision point that decides whether an
// apply-error poisons a row (permanent) or retries unbounded with backoff
// (transient). Transient gRPC Unavailable / DeadlineExceeded / connection-refused
// / timeout must classify as ClassTransient (never poison); only ErrPermanent
// (and decode-fail, handled separately) classifies as ClassPermanent.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/PRO-Robotech/kacho-corelib/outbox/drainer"
)

func TestClassify_SuccessAndAlreadyApplied(t *testing.T) {
	t.Parallel()
	assert.Equal(t, drainer.ClassSuccess, drainer.Classify(nil),
		"nil error → success")
	assert.Equal(t, drainer.ClassAlreadyApplied,
		drainer.Classify(drainer.ErrAlreadyApplied),
		"ErrAlreadyApplied → idempotent success (FGA-409)")
	assert.Equal(t, drainer.ClassAlreadyApplied,
		drainer.Classify(fmt.Errorf("wrapped: %w", drainer.ErrAlreadyApplied)),
		"wrapped ErrAlreadyApplied still idempotent success")
}

func TestClassify_PermanentOnlyForErrPermanent(t *testing.T) {
	t.Parallel()
	assert.Equal(t, drainer.ClassPermanent,
		drainer.Classify(drainer.ErrPermanent),
		"ErrPermanent → permanent")
	assert.Equal(t, drainer.ClassPermanent,
		drainer.Classify(errors.Join(drainer.ErrPermanent, errors.New("bad payload"))),
		"joined ErrPermanent → permanent")
	// gRPC InvalidArgument is the canonical permanent class on the applier side
	// (compute/nlb wrap it in ErrPermanent), but a RAW InvalidArgument that was
	// NOT wrapped must classify permanent too so a malformed intent poisons.
	assert.Equal(t, drainer.ClassPermanent,
		drainer.Classify(status.Error(codes.InvalidArgument, "malformed")),
		"raw gRPC InvalidArgument → permanent (4xx-non-409 family)")
}

func TestClassify_TransientNeverPoisons(t *testing.T) {
	t.Parallel()
	transient := []error{
		status.Error(codes.Unavailable, "iam down"),
		status.Error(codes.DeadlineExceeded, "timeout"),
		context.DeadlineExceeded,
		fmt.Errorf("dial tcp 10.0.0.1:9091: connect: connection refused"),
		&net.OpError{Op: "dial", Err: errors.New("connection refused")},
		errors.New("test: simulated transient"),
		// PermissionDenied is RAW transient per canonical applier classification
		// (compute/nlb: PermissionDenied/Unavailable → raw transient, retry, NOT
		// poison). Only InvalidArgument is permanent.
		status.Error(codes.PermissionDenied, "no relation"),
	}
	for _, err := range transient {
		assert.Equalf(t, drainer.ClassTransient, drainer.Classify(err),
			"%v must classify transient (never poison)", err)
	}
}
