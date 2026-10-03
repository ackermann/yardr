package cli

import (
	"strings"
	"testing"
)

// web serve refuses an address off loopback before it touches the yard: no
// store is opened and no server started for a command that will not serve.
func TestWebServeRefusesNonLoopback(t *testing.T) {
	t.Setenv("YARDR_HOME", t.TempDir())
	for _, addr := range []string{":8791", "0.0.0.0:8791", "192.168.1.10:8791"} {
		a := &app{}
		_, err := run(t, a.webCmd(), "serve", "--addr", addr)
		if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "no authentication") {
			t.Errorf("--addr %s: %v, want a refusal that says why", addr, err)
		}
		if a.st != nil {
			t.Errorf("--addr %s: the store was opened", addr)
		}
	}
}
