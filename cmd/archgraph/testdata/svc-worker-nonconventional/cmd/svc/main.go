// Command svc is an archgraph fixture: a main package that starts a
// background worker WITHOUT the recognised convention — a private type
// with a private run method and no New<Name> constructor. archgraph
// must NOT inventory it, but arch-audit emits an unrecognised-worker
// hint for the goroutine.
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
