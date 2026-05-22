# Fixture: nogomod

A directory tree that is **not** a Go module: no `go.mod` in this
directory nor in any parent inside the fixture.

Used by scenario 4.0-A4 (running archgraph outside a Go module must
fail fast with a clear error and produce no artifacts).

The `sub/` subdirectory exists so tests can verify the walk-up search
also fails when started from a nested directory.
