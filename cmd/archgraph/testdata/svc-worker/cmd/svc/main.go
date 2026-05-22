// Command svc is an archgraph fixture: a main package that starts one
// background worker via the New<Name>(...) + go Run(ctx) convention.
package main

import (
	"context"

	"example.com/svcworker/internal/worker"
)

func main() {
	ctx := context.Background()

	w := worker.NewNetworkOutboxWorker()
	go w.Run(ctx)

	<-ctx.Done()
}
