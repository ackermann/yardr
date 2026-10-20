package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// bdp serve refuses an address off loopback before it touches the yard, as
// web serve does: no store is opened and no server started for a command
// that will not serve, and the refusal is the BDP server's own words.
func TestBDPServeRefusesNonLoopback(t *testing.T) {
	t.Parallel()
	env := testEnv(t, t.TempDir())
	for _, addr := range []string{":8765", "0.0.0.0:8765", "192.168.1.10:8765", "[::]:8765", "example.com:8765"} {
		a := &app{env: env}
		_, err := run(t, a.bdpCmd(), "serve", "--addr", addr)
		if err == nil {
			t.Errorf("--addr %s: served", addr)
			continue
		}
		for _, want := range []string{"--addr " + addr + " refused", "yardr bdp has no authentication", "--unsafe"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("--addr %s: the refusal lacks %q: %v", addr, want, err)
			}
		}
		if a.st != nil {
			t.Errorf("--addr %s: the store was opened", addr)
		}
	}
}

// The address may come from the environment or from the yard's bdp_addr,
// which takes any host:port: it is refused all the same once the yard is
// read. The addresses are TEST-NET ones, which no machine has, and the run is
// bounded: were the refusal missing, serve would fail to listen at once, not
// serve the yard to the network until the test ended.
func TestBDPServeRefusesNonLoopbackFromEnvironmentAndSetting(t *testing.T) {
	t.Parallel()
	home := newHomeDir(t)
	env := testEnv(t, home)
	st := storetest.New(t)
	if err := st.SetSetting(context.Background(), store.BDPAddrSetting, "203.0.113.7:8765", "Jo"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ from, env, addr string }{
		{"the setting", "", "203.0.113.7:8765"},
		{"the environment", "198.51.100.9:8766", "198.51.100.9:8766"},
	} {
		a := &app{st: st, cfg: &config.Config{Home: home, BDPAddr: c.env}, env: env}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := a.bdpCmd()
		cmd.SetContext(ctx)
		_, err := run(t, cmd, "serve")
		cancel()
		if err == nil || !strings.Contains(err.Error(), "--addr "+c.addr+" refused") || !strings.Contains(err.Error(), "--unsafe") {
			t.Errorf("serve on %s: %v, want %s refused", c.from, err, c.addr)
		}
	}
}

// --unsafe gets an address off loopback past the address check and on to the
// yard, and so does --host, with --unsafe and on loopback without it: what
// fails here is the store there is none of, not a refusal. The home is a
// short one, so that the command gets as far as the server under any TMPDIR
// (a socket path too long for the system ends it earlier).
func TestBDPServeUnsafeAndHostGetOnToTheYard(t *testing.T) {
	t.Parallel()
	home := newHomeDir(t)
	for _, args := range [][]string{{"--unsafe"}, {"--unsafe", "--addr", ":9000"}, {"--unsafe", "--addr", "192.168.1.10:9000"},
		{"--unsafe", "--host", "yard.example.net"}, {"--unsafe", "--host", "192.168.178.90"},
		{"--host", "yard.example.net"}, {"--host", "a.example,b.example", "--host", "C.example.", "--addr", "127.0.0.1:9000"}, {"--host", "127.0.0.1"}} {
		a := testApp(t, nil, home)
		_, err := run(t, a.bdpCmd(), append([]string{"serve"}, args...)...)
		if !errors.Is(err, errNoServer) {
			t.Errorf("%v: %v, want the store's error", args, err)
		}
	}
}

// --host names hosts: as a wildcard or with no name it is refused before the
// yard is touched, with the reason, with --unsafe and without. Without it an
// IP address off loopback is refused too, in the server's words.
func TestBDPServeRefusesHost(t *testing.T) {
	t.Parallel()
	env := testEnv(t, t.TempDir())
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--host", "*"}, "wildcard"},
		{[]string{"--host", "*.ts.net"}, "wildcard"},
		{[]string{"--host", ""}, "no name"},
		{[]string{"--host", "a.example", "--host", "b.example:8765"}, "without a scheme or a port"},
		{[]string{"--host", "192.168.178.90"}, "--unsafe"},
		{[]string{"--host", "yard.example.net,10.0.0.1", "--addr", "127.0.0.1:9000"}, "--unsafe"},
		{[]string{"--unsafe", "--host", "*"}, "wildcard"},
		{[]string{"--unsafe", "--host", "a.example,,b.example"}, "no name"},
	} {
		a := &app{env: env}
		_, err := run(t, a.bdpCmd(), append([]string{"serve"}, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), "--host") || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want a refusal that says %q", c.args, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), "the view") {
			t.Errorf("%v: the refusal speaks of the web view: %v", c.args, err)
		}
		if a.st != nil {
			t.Errorf("%v: the store was opened", c.args)
		}
	}
}

// bdp serve has the flags web serve has for the same purposes, and a start
// and an install take --host and not --unsafe.
func TestBDPFlags(t *testing.T) {
	t.Parallel()
	serve, _, err := (&app{}).bdpCmd().Find([]string{"serve"})
	if err != nil {
		t.Fatal(err)
	}
	for name, def := range map[string]string{"addr": "127.0.0.1:8765", "unsafe": "false", "host": "[]", "scope": ""} {
		f := serve.Flags().Lookup(name)
		if f == nil || f.DefValue != def {
			t.Errorf("bdp serve --%s: %+v, want default %q", name, f, def)
		}
	}
	// What a start and an install hand on, and take.
	for _, verb := range []string{"start", "install"} {
		c, _, err := (&app{}).bdpCmd().Find([]string{verb})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"addr", "scope", "host"} {
			if c.Flags().Lookup(name) == nil {
				t.Errorf("bdp %s has no --%s", verb, name)
			}
		}
		if c.Flags().Lookup("unsafe") != nil {
			t.Errorf("bdp %s has --unsafe: a background server stays on loopback", verb)
		}
	}
}

// bdp start and bdp install keep the server on loopback whatever serve
// allows, as the web view's do: an address off loopback is refused with the
// loopback line before anything is started or written, wherever it came
// from, and --host is handed on, to the server a start runs and to the file
// of the service an install writes.
func TestBDPStartAndInstallStayOnLoopback(t *testing.T) {
	home := newHomeDir(t)
	st := storetest.New(t)
	testYard(t, st, home)
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude", "--operator", "Jo"); err != nil {
		t.Fatal(err)
	}
	var started [][]string
	stub(t, &spawn, func(_ []string, _ *config.Config, k *longRunning, args []string) (int, <-chan error, func() error, error) {
		started = append(started, append(k.command(), args...))
		return 4242, nil, func() error { return nil }, nil
	})
	stub(t, &answers, func(string) bool { return len(started) > 0 })
	dir, m := stubServices(t, "linux")
	const never = "yardr bdp start and yardr bdp install never listen off loopback"
	refuse := func(why string, args ...string) {
		t.Helper()
		for _, verb := range [][]string{{"bdp", "start"}, {"bdp", "install", "--file-only"}} {
			_, err := run(t, a().rootCmd(), append(slices.Clone(verb), args...)...)
			if err == nil || !strings.Contains(err.Error(), why) {
				t.Errorf("%v %v: %v, want a refusal that says %q", verb, args, err, why)
			}
			// The flag to give too, in serve's words, is none of theirs.
			if err != nil && strings.Contains(err.Error(), "Give --unsafe") {
				t.Errorf("%v %v: the refusal offers --unsafe: %v", verb, args, err)
			}
		}
	}
	refuse("--addr 0.0.0.0:8765 refused: 0.0.0.0 is not a loopback address, and yardr bdp has no authentication yet", "--addr", "0.0.0.0:8765")
	refuse(never, "--addr", ":8765")
	refuse("wildcard", "--host", "*.ts.net")
	refuse("--host 10.0.0.1 refused: "+never, "--host", "10.0.0.1")
	refuse(never, "--host", "proxy.lan", "--addr", "192.168.1.5:9000")
	// The setting takes any host:port, and neither does what it names.
	if err := st.SetSetting(context.Background(), store.BDPAddrSetting, "0.0.0.0:8765", "Jo"); err != nil {
		t.Fatal(err)
	}
	refuse(never)
	if err := st.SetSetting(context.Background(), store.BDPAddrSetting, "", "Jo"); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(dir); len(started) != 0 || len(left) != 0 || len(m.calls) != 0 {
		t.Fatalf("a refusal started %q, wrote %v and asked the manager %q", started, left, m.calls)
	}

	out, err := run(t, a().rootCmd(), "bdp", "start", "--scope", "https://beads.example/team/", "--host", "proxy.lan,b.example", "--host", "c.example")
	want := []string{"bdp", "serve", "--addr", "127.0.0.1:8765", "--scope", "https://beads.example/team/", "--host", "proxy.lan", "--host", "b.example", "--host", "c.example"}
	if err != nil || len(started) != 1 || !slices.Equal(started[0], want) {
		t.Fatalf("start --host: %v, started %q, want %q", err, started, want)
	}
	wantText(t, out, "The BDP server: http://127.0.0.1:8765/")

	out, err = run(t, a().rootCmd(), "bdp", "install", "--file-only", "--host", "proxy.lan,b.example", "--host", "c.example")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "yardr-bdp-"+homeID(home)+".service"))
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, string(body), `"bdp" "serve" "--addr" "127.0.0.1:8765" "--host" "proxy.lan" "--host" "b.example" "--host" "c.example"`)
	if strings.Contains(string(body), "unsafe") {
		t.Errorf("the service is unsafe:\n%s", body)
	}
	wantText(t, out, "yardr bdp serve --addr 127.0.0.1:8765 --host proxy.lan --host b.example --host c.example", "Not loaded (--file-only)")
}
