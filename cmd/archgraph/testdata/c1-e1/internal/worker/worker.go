// Package worker holds the service's background worker.
package worker

import "context"

// NetworkOutboxWorker drains the outbox table and emits events. It is an
// archgraph worker entry-point: constructed by NewNetworkOutboxWorker and
// started via Run.
type NetworkOutboxWorker struct{}

// NewNetworkOutboxWorker returns a fresh NetworkOutboxWorker.
func NewNetworkOutboxWorker() *NetworkOutboxWorker {
	return &NetworkOutboxWorker{}
}

// Run is the worker's root.
func (w *NetworkOutboxWorker) Run(ctx context.Context) {
	<-ctx.Done()
}
