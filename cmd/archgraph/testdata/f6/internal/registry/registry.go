// Package registry holds a dispatcher whose ReflectInvoke method is
// called only through reflect.Value.Call in main. archgraph's RTA pass
// does not model reflection, so ReflectInvoke is absent from the
// reachable-set and C2 reports it as dead code until an
// `// archgraph:keep reflection-invoked` annotation is added.
package registry

// Dispatcher routes reflection-invoked calls.
type Dispatcher struct{}

// New returns a fresh Dispatcher.
func New() *Dispatcher { return &Dispatcher{} }

// ReflectInvoke is exported and invoked only via reflect.Value.Call.
func (d *Dispatcher) ReflectInvoke() {}
