// Command archgraph is the Kachō code-architecture analyzer.
//
// It provides two subcommands:
//
//	archgraph arch-gen     — generate architecture documentation;
//	archgraph arch-audit   — run blocking architecture checks.
//
// This file is a thin entry point: all behaviour lives in
// internal/archgraph/cli so it can be exercised by tests without a
// process boundary.
package main

import (
	"os"

	"github.com/PRO-Robotech/kacho-corelib/internal/archgraph/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
