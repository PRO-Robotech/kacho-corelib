// Package cron holds the service's scheduled jobs.
package cron

import "context"

// SnapshotCronJob takes periodic snapshots. It is an archgraph worker
// entry-point started via Start.
type SnapshotCronJob struct{}

// NewSnapshotCronJob returns a fresh SnapshotCronJob.
func NewSnapshotCronJob() *SnapshotCronJob {
	return &SnapshotCronJob{}
}

// Start is the cron job's root.
func (j *SnapshotCronJob) Start(ctx context.Context) {
	<-ctx.Done()
}
