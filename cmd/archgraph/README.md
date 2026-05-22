# archgraph

`archgraph` is the Kachō code-architecture analyzer. It builds a static
call graph of a Go service repository, discovers its entry-points (gRPC
methods, workers, reconcilers, cron jobs), computes reachability, and
keeps the curated architecture notes under `docs/arch/` honest with the
code.

## Subcommands

| Subcommand   | What it does |
| ------------ | ------------ |
| `arch-gen`   | Generates the L3/L4 architecture artifacts under `docs/arch/generated/` and writes the computed `status` (`implemented` / `partial` / `planned`) back into curated L2-note frontmatter. Idempotent: a second run on unchanged code produces a zero diff. |
| `arch-audit` | Runs the three blocking architecture checks and exits non-zero on any violation: **C1** every entry-point is anchored by exactly one L2 note, **C2** no exported symbol is dead code, **C3** each note's `source_sha` matches the hash of its reachable-set. |

## Usage

```
archgraph <subcommand> [flags]

  --repo-root <dir>   Repository root to analyze (default: discovered by
                      walking up from the current directory to the
                      nearest go.mod).
  --help              Show usage.
```

Run from a service repo root:

```sh
archgraph arch-gen      # regenerate artifacts + write-back note status
archgraph arch-audit    # CI gate — exit != 0 blocks the build
```

A library module (no `main` package) is detected automatically: the
entry-point inventory is empty, C2 is skipped (the exported API is the
library contract), and the tool completes gracefully.

## Make targets

`kacho-corelib`'s `Makefile` wraps the tool and self-applies it — these
targets double as a template for service repos:

```sh
make build-archgraph   # build bin/archgraph
make arch-gen          # build + run arch-gen on this repo
make arch-audit        # build + run arch-audit on this repo
```

## Notes

- Analysis is offline and deterministic: packages are loaded with
  `GOTOOLCHAIN=local` and `GOWORK=off`, so the `go` toolchain on `PATH`
  must already satisfy the target repository's `go.mod` go directive.
- A dead exported symbol that is intentionally part of a public contract
  (SDK helper, reflection target) is suppressed with a
  `// archgraph:keep <reason>` comment — the reason is mandatory.

## CI drift gate

`arch-gen` followed by `git diff --exit-code` over `docs/arch/` is the
drift gate: if the generated artifacts or the written-back note status
differ from what is committed, the build fails and the author must
regenerate and commit.
