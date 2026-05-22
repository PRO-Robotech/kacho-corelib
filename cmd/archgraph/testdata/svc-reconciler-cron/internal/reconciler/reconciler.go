// Package reconciler holds the service's reconcile loop.
package reconciler

import "context"

// InstanceReconciler drives instances toward their desired state. It is
// an archgraph worker entry-point started via Run.
type InstanceReconciler struct{}

// NewInstanceReconciler returns a fresh InstanceReconciler.
func NewInstanceReconciler() *InstanceReconciler {
	return &InstanceReconciler{}
}

// Run is the reconciler's root.
func (r *InstanceReconciler) Run(ctx context.Context) {
	<-ctx.Done()
}
