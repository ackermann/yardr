package bdp_test

import (
	"strings"
	"testing"

	"yardr.dev/yardr/internal/bdp"
)

// An address off the loopback interface is refused, and the message says
// why: there is no authentication, and it is the BDP server's own words, not
// the web view's.
func TestRefusedAddress(t *testing.T) {
	for _, addr := range []string{":8765", "0.0.0.0:8765", "192.168.1.10:8765", "[::]:8765", "example.com:80", "[2001:db8::1]:80"} {
		err := bdp.CheckAddr("--addr", addr, false)
		if err == nil {
			t.Errorf("%s: accepted", addr)
			continue
		}
		for _, want := range []string{"--addr " + addr + " refused", "yardr bdp has no authentication", "loopback", "--unsafe"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal lacks %q: %v", addr, want, err)
			}
		}
		if strings.Contains(err.Error(), "yardr web") {
			t.Errorf("%s: the refusal speaks of the web view: %v", addr, err)
		}
		if ln, err := bdp.Listen("--addr", addr, false); err == nil {
			ln.Close()
			t.Errorf("%s: Listen opened it", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8765", "127.0.0.1:0", "[::1]:8765", "localhost:8765", "127.8.8.8:1"} {
		if err := bdp.CheckAddr("--addr", addr, false); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if err := bdp.CheckAddr("--addr", "127.0.0.1", false); err == nil || strings.Contains(err.Error(), "refused") {
		t.Errorf("an address with no port: %v, want the parse error", err)
	}
	ln, err := bdp.Listen("--addr", "127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

// --unsafe lets every address through, the ones on every interface too;
// what is no address is still an error. Listen is tried on loopback only:
// opening every interface of the machine running the tests is none of a
// test's business.
func TestUnsafeAddress(t *testing.T) {
	for _, addr := range []string{":9000", "0.0.0.0:9000", "[::]:9000", "192.168.178.90:8765", "127.0.0.1:8765", "example.com:80"} {
		if err := bdp.CheckAddr("--addr", addr, true); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}
	if err := bdp.CheckAddr("--addr", "0.0.0.0", true); err == nil {
		t.Error("an address with no port: accepted")
	}
	ln, err := bdp.Listen("--addr", "127.0.0.1:0", true)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}
