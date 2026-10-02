// Command yard routes beads of work to whoever works them.
package main

import (
	"os"

	"github.com/mckean/yard/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
