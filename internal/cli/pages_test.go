package cli

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// The sandbox origin's flags default to their own loopback port and no
// public name; the default address parses, as the run links build on it.
func TestPagesFlagsDefault(t *testing.T) {
	t.Parallel()
	flags := (&app{}).webCmd().Commands()[0].Flags()
	if def := flags.Lookup("pages-addr").DefValue; def != "127.0.0.1:8789" {
		t.Fatalf("the default --pages-addr is %s", def)
	}
	if def := flags.Lookup("pages-host").DefValue; def != "" {
		t.Fatalf("the default --pages-host is %q", def)
	}
	if _, _, err := net.SplitHostPort(store.DefaultWebPagesAddr); err != nil {
		t.Fatalf("the default pages address does not parse: %v", err)
	}
}

// web serve refuses a sandbox address, and a local-read address, off
// loopback before it touches the yard, naming the flag that gave it.
// --unsafe lets the sandbox origin's address go off loopback with the view's,
// and never the local-read listener's, which has no token: its refusal does
// not offer --unsafe.
func TestWebServeRefusesPagesAndLocalReadAddr(t *testing.T) {
	t.Parallel()
	env := testEnv(t, t.TempDir())
	for _, c := range []struct {
		args []string
		flag string
		// want is what the refusal says besides the flag.
		want string
	}{
		{[]string{"--pages-addr", ":8789"}, "--pages-addr", "refused"},
		{[]string{"--pages-addr", "0.0.0.0:8789"}, "--pages-addr", "refused"},
		{[]string{"--pages-addr", "192.168.1.10:8789"}, "--pages-addr", "refused"},
		{[]string{"--local-read", ":8793"}, "--local-read", "an address with no host listens on every interface"},
		{[]string{"--local-read", "0.0.0.0:8793"}, "--local-read", "0.0.0.0 is not a loopback address"},
		{[]string{"--local-read", "192.168.1.10:8793"}, "--local-read", "192.168.1.10 is not a loopback address"},
		{[]string{"--local-read", "yard.example.net:8793"}, "--local-read", "yard.example.net is not a loopback address"},
		{[]string{"--unsafe", "--local-read", ":8793"}, "--local-read", "whatever --unsafe says of the view"},
		{[]string{"--unsafe", "--local-read", "0.0.0.0:8793"}, "--local-read", "whatever --unsafe says of the view"},
		{[]string{"--unsafe", "--local-read", "192.168.1.10:8793"}, "--local-read", "whatever --unsafe says of the view"},
		{[]string{"--unsafe", "--addr", "0.0.0.0:8791", "--local-read", "0.0.0.0:8793"}, "--local-read", "refused"},
		{[]string{"--local-read", "127.0.0.1"}, "--local-read", "missing port"},
		{[]string{"--local-read", ""}, "--local-read", "missing port"},
	} {
		a := &app{env: env}
		_, err := run(t, a.webCmd(), append([]string{"serve"}, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), c.flag) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want a refusal that names %s and says %q", c.args, err, c.flag, c.want)
		}
		if err != nil && c.flag == "--local-read" && strings.Contains(err.Error(), "unless --unsafe is given") {
			t.Errorf("%v: the refusal offers --unsafe, which the local-read listener never has: %v", c.args, err)
		}
		if a.st != nil {
			t.Errorf("%v: the store was opened", c.args)
		}
	}
}

// web serve refuses a public name that is no host name before it touches
// the yard, naming the flag that gave it.
func TestWebServeRefusesPagesHost(t *testing.T) {
	t.Parallel()
	env := testEnv(t, t.TempDir())
	for _, c := range []struct {
		host string
		want string
	}{
		{"*", "wildcard"},
		{"", "no name"},
		{"a.example:8789", "without a scheme or a port"},
		{"not a name", "no host name"},
	} {
		a := &app{env: env}
		_, err := run(t, a.webCmd(), "serve", "--pages-host", c.host)
		if err == nil || !strings.Contains(err.Error(), "--pages-host") || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("--pages-host %q: %v, want a refusal that says %q", c.host, err, c.want)
		}
		if a.st != nil {
			t.Errorf("--pages-host %q: the store was opened", c.host)
		}
	}
}

// A failed sandbox listener must release the view listener it bound first.
func TestWebServeClosesViewIfPagesAddrIsInUse(t *testing.T) {
	// The setting variables are blank in the process already (TestMain).
	home, st, _ := statusYard(t, "abcdef123456")
	pages, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pages.Close()
	viewAddr := freeAddr(t)
	a := &app{st: st, cfg: &config.Config{Home: home}, actor: "me"}
	if _, err := run(t, a.webCmd(), "serve", "--addr", viewAddr, "--pages-addr", pages.Addr().String()); err == nil {
		t.Fatal("web serve started with its pages address already in use")
	}
	view, err := net.Listen("tcp", viewAddr)
	if err != nil {
		t.Fatalf("web serve left its view listener bound after the pages listener failed: %v", err)
	}
	_ = view.Close()
}

// A start and an install hand the sandbox origin's flags and --local-read
// on when they are given, and check what the yard's settings and the
// environment resolve them to: a start with neither hands on only the
// view's address, and the serve it starts resolves the sandbox itself. The
// local-read flag has no setting and no environment: without it the service
// has no local-read listener to keep, and with it the install writes it
// into the service, so a login restart opens it again.
func TestStartAndInstallTakeThePagesFlags(t *testing.T) {
	// The setting variables are blank in the process already (TestMain).
	home := newHomeDir(t)
	dir, _ := stubServices(t, "darwin")
	st := storetest.New(t)
	// The runs' own environment, shared so a value set below reaches
	// every run; the setting variables are set on the process too,
	// where the setting reader finds them (design 3.2's list: a test
	// that sets one to a value stays serial).
	runEnv := testEnv(t, home)
	a := func() *app { return &app{st: st, actor: "me", env: runEnv} }
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	var started [][]string
	stub(t, &spawn, func(_ []string, _ *config.Config, k *longRunning, args []string) (int, <-chan error, func() error, error) {
		started = append(started, append(k.command(), args...))
		return 4242, nil, func() error { return nil }, nil
	})
	stub(t, &answers, func(addr string) bool { return len(started) > 0 && slices.Contains(started[len(started)-1], addr) })
	starts := func(args []string, want []string) {
		t.Helper()
		started = nil
		if _, err := run(t, a().rootCmd(), append([]string{"web", "start"}, args...)...); err != nil {
			t.Fatalf("web start %v: %v", args, err)
		}
		if len(started) != 1 || !slices.Equal(started[0], want) {
			t.Fatalf("web start %v started %q, want %q", args, started, want)
		}
	}
	starts(nil, []string{"web", "serve", "--addr", "127.0.0.1:8791"})
	starts([]string{"--pages-addr", "127.0.0.1:9001", "--pages-host", "pages.example.net"},
		[]string{"web", "serve", "--addr", "127.0.0.1:8791", "--pages-addr", "127.0.0.1:9001", "--pages-host", "pages.example.net"})
	starts([]string{"--pages-host", "pages.example.net"},
		[]string{"web", "serve", "--addr", "127.0.0.1:8791", "--pages-host", "pages.example.net"})
	starts([]string{"--local-read", "127.0.0.1:8793"},
		[]string{"web", "serve", "--addr", "127.0.0.1:8791", "--local-read", "127.0.0.1:8793"})

	if _, err := run(t, a().rootCmd(), "web", "install", "--pages-addr", "127.0.0.1:9001", "--pages-host", "pages.example.net"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "dev.yardr.web."+homeID(home)+".plist"))
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, string(body), "<string>--pages-addr</string>\n\t\t<string>127.0.0.1:9001</string>",
		"<string>--pages-host</string>\n\t\t<string>pages.example.net</string>")
	if strings.Contains(string(body), "--local-read") {
		t.Errorf("an install without --local-read wrote it into the service:\n%s", body)
	}
	if _, err := run(t, a().rootCmd(), "web", "uninstall"); err != nil {
		t.Fatal(err)
	}
	// With the flag the service keeps it across a login restart.
	if _, err := run(t, a().rootCmd(), "web", "install", "--local-read", "127.0.0.1:8793"); err != nil {
		t.Fatal(err)
	}
	if body, err = os.ReadFile(filepath.Join(dir, "dev.yardr.web."+homeID(home)+".plist")); err != nil {
		t.Fatal(err)
	}
	wantText(t, string(body), "<string>--local-read</string>\n\t\t<string>127.0.0.1:8793</string>")
	if _, err := run(t, a().rootCmd(), "web", "uninstall"); err != nil {
		t.Fatal(err)
	}
	// Loopback under every flag: a start or an install that would open it
	// elsewhere is refused before anything runs, and nothing is written.
	for _, verb := range []string{"start", "install"} {
		for addr, want := range map[string]string{
			"0.0.0.0:8793": "loopback", "192.168.1.5:8793": "loopback", ":8793": "loopback",
			"yard.example.net:8793": "loopback", "": "missing port",
		} {
			started = nil
			_, err := run(t, a().rootCmd(), "web", verb, "--local-read", addr)
			if err == nil || !strings.Contains(err.Error(), "--local-read") || !strings.Contains(err.Error(), want) {
				t.Errorf("web %s --local-read %q = %v, want a refusal that names the flag and says %q", verb, addr, err, want)
			}
			if len(started) != 0 {
				t.Errorf("web %s --local-read %q started %q", verb, addr, started)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "dev.yardr.web."+homeID(home)+".plist")); !os.IsNotExist(err) {
		t.Errorf("a refused install left a service file behind: %v", err)
	}

	// The yard's settings resolve in the serve a start starts: a start
	// hands on no pages flag for them, but refuses what they resolve
	// to when it is refused.
	if _, err := run(t, a().rootCmd(), "settings", "set", "web_pages_addr", "127.0.0.1:9100"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a().rootCmd(), "settings", "set", "web_pages_host", "pages.example.net"); err != nil {
		t.Fatal(err)
	}
	starts(nil, []string{"web", "serve", "--addr", "127.0.0.1:8791"})
	if got, err := run(t, a().rootCmd(), "settings", "get", "web_pages_addr"); err != nil || got != "127.0.0.1:9100\n" {
		t.Fatalf("settings get web_pages_addr = %q, %v", got, err)
	}

	// The environment overrides the stored settings, and what it
	// resolves to is refused before anything is started.
	t.Setenv("YARDR_WEB_PAGES_ADDR", "192.168.1.5:9100")
	runEnv["YARDR_WEB_PAGES_ADDR"] = "192.168.1.5:9100"
	if _, err := run(t, a().rootCmd(), "web", "start"); err == nil || !strings.Contains(err.Error(), "never listen off loopback") {
		t.Fatalf("web start under non-loopback YARDR_WEB_PAGES_ADDR = %v", err)
	}
	t.Setenv("YARDR_WEB_PAGES_ADDR", "")
	runEnv["YARDR_WEB_PAGES_ADDR"] = ""
	t.Setenv("YARDR_WEB_PAGES_HOST", "*")
	runEnv["YARDR_WEB_PAGES_HOST"] = "*"
	if _, err := run(t, a().rootCmd(), "web", "start"); err == nil || !strings.Contains(err.Error(), "--pages-host") {
		t.Fatalf("web start under YARDR_WEB_PAGES_HOST=* = %v", err)
	}
	t.Setenv("YARDR_WEB_PAGES_HOST", "")
	runEnv["YARDR_WEB_PAGES_HOST"] = ""
}

// settings set refuses for web_pages_addr what --pages-addr refuses,
// in the sentence the flag gives, and for web_pages_host what
// --pages-host refuses; a start refuses the same before anything runs.
func TestPagesSettingsAreCheckedAsTheFlags(t *testing.T) {
	home := newHomeDir(t)
	st := storetest.New(t)
	testYard(t, st, home)
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	stubWeb(t)
	for _, addr := range []string{"0.0.0.0:8789", ":8789", "192.168.1.5:8789", "example.net:8789", "8789"} {
		flag := web.CheckAddr("--pages-addr", addr, false)
		if flag == nil {
			t.Fatalf("--pages-addr %s is taken", addr)
		}
		if _, err := run(t, a().rootCmd(), "web", "start", "--pages-addr", addr); err == nil || !strings.Contains(err.Error(), flag.Error()) {
			t.Fatalf("web start --pages-addr %s = %v", addr, err)
		}
		if _, err := run(t, a().rootCmd(), "settings", "set", "web_pages_addr", addr); err == nil || !strings.Contains(err.Error(), flag.Error()) {
			t.Errorf("settings set web_pages_addr %s = %v, want the flag's: %v", addr, err, flag)
		}
	}
	for _, addr := range []string{"127.0.0.1:9100", "[::1]:9100", "localhost:9100"} {
		if _, err := run(t, a().rootCmd(), "settings", "set", "web_pages_addr", addr); err != nil {
			t.Errorf("settings set web_pages_addr %s: %v", addr, err)
		}
	}
	for _, host := range []string{"*", "pages.example.net:443", "not a name"} {
		_, flag := store.CheckWebHost("--pages-host", host)
		if flag == nil {
			t.Fatalf("--pages-host %s is taken", host)
		}
		if _, err := run(t, a().rootCmd(), "web", "start", "--pages-host", host); err == nil || !strings.Contains(err.Error(), flag.Error()) {
			t.Fatalf("web start --pages-host %s = %v", host, err)
		}
		if _, err := run(t, a().rootCmd(), "settings", "set", "web_pages_host", host); err == nil || !strings.Contains(err.Error(), flag.Error()) {
			t.Errorf("settings set web_pages_host %s = %v, want the flag's: %v", host, err, flag)
		}
	}
	if _, err := run(t, a().rootCmd(), "settings", "set", "web_pages_host", "Pages.Example."); err != nil {
		t.Errorf("settings set web_pages_host Pages.Example.: %v", err)
	}
}

// web serve takes the setting, environment or flag in precedence order
// for the sandbox origin beside the view: the origin answers blob
// bytes only under the scripted policy, no yard route, and the view's
// Host rules. Where none of them gives it an address, a view on a free
// port (port 0 from the flag, the environment or the setting) gets a
// free port for it too, and the serving line names both; the default's
// port is held meanwhile, as on a machine that runs its own view. A
// serve on the default itself is not run here, for that machine's view
// may hold it.
func TestServeTakesThePagesSetting(t *testing.T) {
	// The setting variables are blank in the process already (TestMain).
	home, st, _ := statusYard(t, "abcdef123456")
	cfg := &config.Config{Home: home}
	a := func() *app { return &app{st: st, cfg: cfg, actor: "me"} }
	// Held, a serve on a free port that needed the default origin's port
	// would end at the bind. A port some other process holds already is
	// as good.
	if held, err := net.Listen("tcp", store.DefaultWebPagesAddr); err == nil {
		t.Cleanup(func() { held.Close() })
	}
	// The serve logs where it listens to os.Stderr: its line is read back
	// through a pipe, as the daemon's is (homewatch_test.go).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	logged := &syncBuffer{}
	read := make(chan struct{})
	go func() { _, _ = io.Copy(logged, r); close(read) }()
	t.Cleanup(func() {
		os.Stderr = old
		w.Close()
		<-read
		r.Close()
	})
	// A port of 0 in a want is any free port on its host, which is not
	// the default origin's.
	bound := func(got, want string) bool {
		if !portZero(want) {
			return got == want
		}
		wantHost, _, _ := net.SplitHostPort(want)
		host, port, err := net.SplitHostPort(got)
		return err == nil && host == wantHost && port != "0" && got != store.DefaultWebPagesAddr
	}
	// serves runs web serve with args and --addr viewAddr, none for "":
	// the view then takes the address the environment or the setting gives
	// it, and is wanted on a free port of 127.0.0.1.
	serves := func(args []string, viewAddr, pagesAddr string) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cmd := a().rootCmd()
		argv := []string{"web", "serve"}
		wantView := "127.0.0.1:0"
		if viewAddr != "" {
			argv, wantView = append(argv, "--addr", viewAddr), viewAddr
		}
		cmd.SetArgs(append(argv, args...))
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		done := make(chan error, 1)
		go func() { done <- cmd.ExecuteContext(ctx) }()
		k := kindOf(svcWeb)
		var up []instance
		for deadline := time.Now().Add(10 * time.Second); len(up) == 0; time.Sleep(10 * time.Millisecond) {
			select {
			case err := <-done:
				t.Fatalf("web serve %v ended: %v", args, err)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("web serve %v recorded nothing", args)
			}
			up = k.running(cfg)
		}
		views := webViews(cfg)
		if len(views) != 1 {
			t.Fatalf("the record is %+v, want one view", views)
		}
		viewBound, pagesBound := up[0].Addr, views[0].PagesAddr
		if !bound(viewBound, wantView) {
			t.Errorf("web serve listens on %s, want %s", viewBound, wantView)
		}
		if !bound(pagesBound, pagesAddr) {
			t.Errorf("the record's sandbox origin is %s, want %s", pagesBound, pagesAddr)
		}
		// The serving line names both, as bound.
		line := "url=http://" + viewBound + "/ pages=http://" + pagesBound + "/"
		for deadline := time.Now().Add(10 * time.Second); !strings.Contains(logged.String(), line); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("web serve %v did not log %q:\n%s", args, line, logged.String())
			}
		}
		get := func(addr, target string, host string) (int, http.Header, string) {
			t.Helper()
			req, err := http.NewRequest("GET", "http://"+addr+target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if host != "" {
				req.Host = host
			}
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("nothing listens on %s: %v", addr, err)
			}
			defer r.Body.Close()
			b, _ := io.ReadAll(r.Body)
			return r.StatusCode, r.Header, string(b)
		}
		// The origin answers no yard route, and a missing blob
		// plainly, both under the scripted policy.
		if status, header, body := get(pagesBound, "/", ""); status != 404 || !strings.Contains(body, "404 page not found") {
			t.Errorf("the origin's /: status %d, want the mux's 404: %s", status, body)
		} else if csp := header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox allow-scripts") || !strings.Contains(csp, "webrtc 'block'") {
			t.Errorf("the origin's Content-Security-Policy %q, want the scripted policy", csp)
		}
		if status, _, body := get(pagesBound, "/blobs/"+strings.Repeat("0", 64), ""); status != 404 || !strings.Contains(body, "no such blob") {
			t.Errorf("the origin's missing blob: status %d, want a plain 404: %s", status, body)
		}
		// Its Host rules are the view's.
		if status, _, _ := get(pagesBound, "/", "evil.example.net"); status != http.StatusMisdirectedRequest {
			t.Errorf("Host evil.example.net: status %d, want 421", status)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("web serve did not stop")
		}
	}
	// Nothing gives the origin an address, and the view asks for a free
	// port: so does the origin.
	serves(nil, "127.0.0.1:0", "127.0.0.1:0")
	// The view's port of 0 may come from the environment or the setting.
	cfg.WebAddr = "127.0.0.1:0"
	serves(nil, "", "127.0.0.1:0")
	cfg.WebAddr = ""
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.WebAddrSetting, "127.0.0.1:0", "Jo"); err != nil {
		t.Fatal(err)
	}
	serves(nil, "", "127.0.0.1:0")
	if err := st.SetSetting(ctx, store.WebAddrSetting, "", "Jo"); err != nil {
		t.Fatal(err)
	}

	// An address from the setting, the environment or the flag is taken
	// as it stands, by a view on a free port as by any other.
	settingAddr := freeAddr(t)
	if err := st.SetSetting(ctx, store.WebPagesAddrSetting, settingAddr, "Jo"); err != nil {
		t.Fatal(err)
	}
	serves(nil, freeAddr(t), settingAddr)
	serves(nil, "127.0.0.1:0", settingAddr)
	envAddr := freeAddr(t)
	cfg.WebPagesAddr = envAddr
	serves(nil, freeAddr(t), envAddr)
	serves(nil, "127.0.0.1:0", envAddr)
	flagAddr := freeAddr(t)
	serves([]string{"--pages-addr", flagAddr}, freeAddr(t), flagAddr)
	serves([]string{"--pages-addr", flagAddr}, "127.0.0.1:0", flagAddr)
}
