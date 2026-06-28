// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

package resourcelifecycle_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/PRO-Robotech/kacho-corelib/resourcelifecycle"
)

type fakeSource struct {
	batches [][]resourcelifecycle.Event
	idx     int
}

func (f *fakeSource) ReadFrom(ctx context.Context, kinds []string, cursor string, limit int) ([]resourcelifecycle.Event, error) {
	if f.idx >= len(f.batches) {
		return nil, nil
	}
	b := f.batches[f.idx]
	f.idx++
	return b, nil
}

func (f *fakeSource) WaitForChange(ctx context.Context, timeout time.Duration) error {
	// При исчерпании batch'ей завершаем тест cancel'ом ctx.
	if f.idx >= len(f.batches) {
		return context.Canceled
	}
	return nil
}

type fakeSink struct {
	sent []resourcelifecycle.Event
	err  error
}

func (s *fakeSink) Send(ev resourcelifecycle.Event) error {
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, ev)
	return nil
}

func TestStream_SendsAllBatchesUntilSourceExhausted(t *testing.T) {
	src := &fakeSource{
		batches: [][]resourcelifecycle.Event{
			{{EventID: "1", Kind: "network", ResourceID: "enp_x", Op: "Create"}},
			{
				{EventID: "2", Kind: "subnet", ResourceID: "sub_y", Op: "Create"},
				{EventID: "3", Kind: "network", ResourceID: "enp_z", Op: "Delete"},
			},
		},
	}
	sink := &fakeSink{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = resourcelifecycle.Stream(ctx, src, sink, nil, "", resourcelifecycle.StreamOptions{
		BatchLimit: 100,
		IdleSleep:  100 * time.Millisecond,
	})
	if len(sink.sent) != 3 {
		t.Fatalf("expected 3 events forwarded, got %d", len(sink.sent))
	}
	if sink.sent[0].EventID != "1" || sink.sent[2].EventID != "3" {
		t.Fatalf("unexpected order: %+v", sink.sent)
	}
}

func TestStream_StopsOnSinkErr(t *testing.T) {
	src := &fakeSource{
		batches: [][]resourcelifecycle.Event{
			{{EventID: "1", Kind: "network", ResourceID: "enp_x", Op: "Create"}},
		},
	}
	wantErr := errors.New("sink dead")
	sink := &fakeSink{err: wantErr}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := resourcelifecycle.Stream(ctx, src, sink, nil, "", resourcelifecycle.StreamOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wantErr, got %v", err)
	}
}
