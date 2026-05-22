# Fixture: compileerror

A Go module whose entry-point package does **not** compile: `cmd/svc/main.go`
calls an undefined identifier.

Used by scenario 4.0-A5 (fail-fast): archgraph must report the build
failure with file/position, exit non-zero, and must **not** emit false
C1/C2/C3 audit failures on top of an unusable graph.
