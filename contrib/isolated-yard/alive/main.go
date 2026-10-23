// Command alive says whether the test binary that named itself to an
// isolated yard is still running (see ../isolated-yard, which builds it into
// D/bin and whose watchdog asks it once a second). The binary is the one
// YARDR_TEST_OWNER names, "<pid> <start>" (config.TestOwner). Exit status 0
// when it runs, 1 when it is gone, 2 when the environment names no owner a
// check can use. The watchdog reads 2 as gone too: a handoff that broke must
// take the yard down, where the yard would otherwise outlive its test for
// good. The check goes by pid and start time (peer.Alive), as the yard's
// servers do: a pid alone is another process's once it is reused.
package main

import (
	"fmt"
	"io"
	"os"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
)

func main() { os.Exit(run(os.Stderr)) }

// run is main: the status to exit with, and the reason for a 2 on stderr.
func run(stderr io.Writer) int {
	pid, start, ok := config.TestOwner()
	if !ok {
		fmt.Fprintf(stderr, "alive: %s is missing or malformed\n", config.Key(config.TestOwnerEnv))
		return 2
	}
	if !peer.Alive(pid, start) {
		return 1
	}
	return 0
}
