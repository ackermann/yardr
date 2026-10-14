// Command gen writes the rpc client's mechanical store.API wrappers into
// client_gen.go: go generate runs it (internal/rpc).
package main

import (
	"fmt"
	"os"

	"yardr.dev/yardr/internal/rpc/clientgen"
)

func main() {
	out, err := clientgen.Generate("../store/api.go")
	if err == nil {
		err = os.WriteFile("client_gen.go", out, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}
