// Command gen writes the drawings of the starter pack's flows that the
// documentation shows, into the directory it is given: go generate runs it
// (internal/flowdraw).
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"yardr.dev/yardr/internal/flowdraw"
	"yardr.dev/yardr/packs"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: gen <directory>")
		os.Exit(2)
	}
	files, err := flowdraw.Embedded(packs.Starter)
	if err == nil {
		err = os.MkdirAll(os.Args[1], 0o755)
	}
	for name, svg := range files {
		if err == nil {
			err = os.WriteFile(filepath.Join(os.Args[1], name), svg, 0o644)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
