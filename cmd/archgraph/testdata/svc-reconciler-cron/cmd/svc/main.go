// Command svc is an archgraph fixture: a main package starting a
// reconciler and a cron job through chained New<Name>(...).Run/Start.
package main

import (
	"context"

	"example.com/svcreconcilercron/internal/cron"
	"example.com/svcreconcilercron/internal/reconciler"
)

func main() {
	ctx := context.Background()

	// Chained construct-and-start: New<Name>(...).Run(ctx).
	go reconciler.NewInstanceReconciler().Run(ctx)
	go cron.NewSnapshotCronJob().Start(ctx)

	<-ctx.Done()
}
