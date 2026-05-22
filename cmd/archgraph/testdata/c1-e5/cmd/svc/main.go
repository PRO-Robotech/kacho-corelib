// Command svc is an archgraph C1 fixture: it starts a background worker
// WITHOUT the New<Name> + Run/Start convention, so entry-point discovery
// does not inventory it but emits an unrecognised-worker hint. An L2 note
// nonetheless anchors worker: NetworkOutboxWorker. C1 sees a stale anchor
// (no matching entry-point) AND, because inv.Hints carries the
// unrecognised worker, augments the output with a clarifying hint.
package main

import "context"

// networkOutboxWorker is a private worker type — deliberately not
// matching the New<Name> + Run/Start convention.
type networkOutboxWorker struct{}

// run is a private method — deliberately not Run/Start.
func (w *networkOutboxWorker) run(ctx context.Context) {
	<-ctx.Done()
}

func main() {
	ctx := context.Background()

	go (&networkOutboxWorker{}).run(ctx)

	<-ctx.Done()
}
