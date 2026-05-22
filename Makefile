.PHONY: lint test cover build-archgraph arch-gen arch-audit

lint:
	golangci-lint run ./...

test:
	go test ./... -race -cover

cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out

# build-archgraph compiles the archgraph CLI into bin/archgraph.
build-archgraph:
	CGO_ENABLED=0 go build -o bin/archgraph ./cmd/archgraph

# archgraph loads the target repository with GOTOOLCHAIN=local (offline,
# deterministic analysis — see internal/archgraph/cli.loadPackages). The
# `go` on PATH must therefore already satisfy the target go.mod directive
# (kacho-corelib requires go 1.25). If the default `go` is older but a
# `go1.25.x` SDK shim is installed (go install golang.org/dl/go1.25.x),
# arch-gen/arch-audit pick its GOROOT/bin up automatically.
GO125 := $(shell ls $$HOME/go/bin/go1.25* 2>/dev/null | head -1)
ARCHGRAPH_PATH := $(if $(GO125),$(shell $(GO125) env GOROOT)/bin:$(PATH),$(PATH))

# arch-gen regenerates the L3/L4 architecture artifacts for this repo.
# kacho-corelib is a library module (no main package), so arch-gen
# completes gracefully with an empty entry-point inventory — this target
# doubles as a self-test of the tool and a template for service repos.
arch-gen: build-archgraph
	PATH="$(ARCHGRAPH_PATH)" ./bin/archgraph arch-gen

# arch-audit runs the C1/C2/C3 architecture checks and blocks via exit
# code on any violation. For a library module C2 is skipped; C1/C3 run.
arch-audit: build-archgraph
	PATH="$(ARCHGRAPH_PATH)" ./bin/archgraph arch-audit
