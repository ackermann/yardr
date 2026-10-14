package cli

import (
	"context"
	"errors"
	"slices"
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

// --unsafe alone listens on every interface, on the port of the yard's
// web_addr where it has one; an --addr given stands, and without the flag
// the default stays on loopback.
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
		// The yard's web_addr, which is addr when --addr was not given:
		// every interface on its port.
		{"127.0.0.1:9100", false, false, "127.0.0.1:9100"},
		{"127.0.0.1:9100", false, true, "0.0.0.0:9100"},
		{"[::1]:9100", false, true, "0.0.0.0:9100"},
		{"invalid", false, true, "invalid"},
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

// --host names hosts: as a wildcard or with no name it is refused before
// the yard is touched, with the reason, with --unsafe and without. Without
// it an IP address off loopback is refused too, and the refusal names the
// flag that answers to one.
func TestWebServeRefusesHost(t *testing.T) {
	t.Setenv("YARDR_HOME", t.TempDir())
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--host", "*"}, "wildcard"},
		{[]string{"--host", "*.ts.net"}, "wildcard"},
		{[]string{"--host", ""}, "no name"},
		{[]string{"--host", "a.example,,b.example"}, "no name"},
		{[]string{"--host", "a.example", "--host", "b.example:8791"}, "without a scheme or a port"},
		{[]string{"--host", "192.168.178.90"}, "--unsafe"},
		{[]string{"--host", "yard.example.net,10.0.0.1", "--addr", "127.0.0.1:9000"}, "--unsafe"},
		{[]string{"--unsafe", "--host", "*"}, "wildcard"},
		{[]string{"--unsafe", "--host", "*.ts.net"}, "wildcard"},
		{[]string{"--unsafe", "--host", ""}, "no name"},
		{[]string{"--unsafe", "--host", "a.example,,b.example"}, "no name"},
		{[]string{"--unsafe", "--host", "a.example", "--host", "b.example:8791"}, "without a scheme or a port"},
	} {
		a := &app{}
		_, err := run(t, a.webCmd(), append([]string{"serve"}, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), "--host") || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want a refusal that says %q", c.args, err, c.want)
		}
		if a.st != nil {
			t.Errorf("%v: the store was opened", c.args)
		}
	}
}

// --host gets past the checks and on to the yard, with --unsafe and on
// loopback without it, given once, twice or as a comma list, and each name
// is kept as it is compared.
func TestWebHosts(t *testing.T) {
	newHome(t)
	for _, args := range [][]string{{"--unsafe", "--host", "yard.example.net"}, {"--unsafe", "--host", "a.example,b.example", "--host", "C.example."},
		{"--unsafe", "--host", "192.168.178.90"},
		{"--host", "yard.example.net"}, {"--host", "a.example,b.example", "--host", "C.example.", "--addr", "127.0.0.1:9000"}, {"--host", "127.0.0.1"}} {
		a := &app{}
		_, err := run(t, a.webCmd(), append([]string{"serve"}, args...)...)
		if !errors.Is(err, errNoServer) {
			t.Errorf("%v: %v, want the store's error", args, err)
		}
	}
	got, err := webHosts([]string{"a.example", " B.Example ", "C.example."}, true, true)
	if want := []string{"a.example", "b.example", "c.example"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("webHosts = %v, %v, want %v", got, err, want)
	}
	if got, err := webHosts([]string{"a.example", " B.Example ", "C.example."}, true, false); err != nil || !slices.Equal(got, []string{"a.example", "b.example", "c.example"}) {
		t.Errorf("webHosts without --unsafe = %v, %v", got, err)
	}
	if got, err := webHosts(nil, false, false); err != nil || got != nil {
		t.Errorf("no --host: %v, %v", got, err)
	}
}

// --as with a name no user of the yard has is refused, naming the flag
// and where the users are; with a user's it gets on to serving.
func TestWebServeAsRefused(t *testing.T) {
	_, st, a := statusYard(t, "abcdef123456")
	_, err := run(t, a().webCmd(), "serve", "--as", "nosuch", "--addr", "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "--as") || !strings.Contains(err.Error(), "refused") ||
		!strings.Contains(err.Error(), "no user") || !strings.Contains(err.Error(), "yardr user list") {
		t.Fatalf("--as nosuch: %v, want a refusal that names the flag and the users", err)
	}
	if _, err := st.GetUser(context.Background(), "Jo"); err != nil {
		t.Fatalf("the yard's user: %v", err)
	}
}

func TestWebAnswersTo(t *testing.T) {
	for _, c := range []struct {
		unsafe bool
		names  []string
		want   string
	}{
		{false, nil, ""},
		{false, []string{"yard.example.net", "yard.lan"}, "a loopback address, localhost, yard.example.net, yard.lan"},
		{true, nil, "an IP address, localhost"},
		{true, []string{"adventuretime", "adventuretime.local", "yard.example.net"}, "an IP address, localhost, adventuretime, adventuretime.local, yard.example.net"},
	} {
		if got := webAnswersTo(c.unsafe, c.names); got != c.want {
			t.Errorf("webAnswersTo(%v, %v) = %q, want %q", c.unsafe, c.names, got, c.want)
		}
	}
}
