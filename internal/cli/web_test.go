package cli

import (
	"errors"
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
		if !strings.Contains(err.Error(), "--unsafe") {
			t.Errorf("--addr %s: the refusal does not name --unsafe: %v", addr, err)
		}
		if a.st != nil {
			t.Errorf("--addr %s: the store was opened", addr)
		}
	}
}

// --unsafe alone listens on every interface; an --addr given stands, and
// without the flag the default stays on loopback.
func TestWebAddr(t *testing.T) {
	def := (&app{}).webCmd().Commands()[0].Flags().Lookup("addr").DefValue
	if def != "127.0.0.1:8791" {
		t.Fatalf("the default --addr is %s", def)
	}
	for _, c := range []struct {
		addr          string
		given, unsafe bool
		want          string
	}{
		{def, false, false, "127.0.0.1:8791"},
		{def, false, true, "0.0.0.0:8791"},
		{":9000", true, true, ":9000"},
		{"127.0.0.1:8791", true, true, "127.0.0.1:8791"},
		{"192.168.1.10:9000", true, false, "192.168.1.10:9000"},
	} {
		if got := webAddr(c.addr, c.given, c.unsafe); got != c.want {
			t.Errorf("webAddr(%s, %v, %v) = %s, want %s", c.addr, c.given, c.unsafe, got, c.want)
		}
	}
}

// --unsafe gets an address off loopback past the address check and on to
// the yard: what fails here is the store there is none of, not a refusal.
// The home is a short one, so that the command gets as far as the server
// under any TMPDIR (a socket path too long for the system ends it earlier).
func TestWebServeUnsafeAcceptsAnyAddress(t *testing.T) {
	newHome(t)
	for _, args := range [][]string{{"--unsafe"}, {"--unsafe", "--addr", ":9000"}, {"--unsafe", "--addr", "0.0.0.0:9000"}, {"--unsafe", "--addr", "192.168.1.10:9000"}} {
		a := &app{}
		_, err := run(t, a.webCmd(), append([]string{"serve"}, args...)...)
		if !errors.Is(err, errNoServer) {
			t.Errorf("%v: %v, want the store's error", args, err)
		}
	}
}
