# Fixture: validrepo

A minimal, well-formed Go module used by archgraph CLI tests.

- Has a `go.mod` (module `example.com/validrepo`).
- Has one entry-point package `cmd/svc/main.go` that compiles cleanly.

Used by scenarios 4.0-A3 (repo-root detection from cwd) and as the
"happy path" load target for `arch-gen` / `arch-audit`.
