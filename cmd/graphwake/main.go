// Command graphwake sends a service's OpenAPI spec from a CI pipeline to Graphwake.
//
// It never fails a pipeline: the exit code is always 0 and problems are written as one warning line.
package main

import (
	"fmt"
	"os"

	"github.com/graphwake/agent/internal/cli"
)

// version is set at build time (-ldflags "-X main.version=1.2.3").
var version = "dev"

func main() {
	defer func() {
		// Last line of defence: a bug must not fail the pipeline either.
		if recover() != nil {
			fmt.Fprintln(os.Stdout, "graphwake: internal error, nothing sent")
			os.Exit(0)
		}
	}()
	cli.Main(os.Args[1:], cli.DefaultDeps(version, os.Stdout))
	os.Exit(0)
}
