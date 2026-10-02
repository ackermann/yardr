// Command yardr routes beads of work to whoever works them.
package main

import (
	"os"

	"yardr.dev/yardr/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
