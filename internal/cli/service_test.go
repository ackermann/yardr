package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// manager stands in for launchctl and systemctl: it records every command
// line and keeps which units are loaded, as the real one would.
type manager struct {
	calls  []string
	loaded map[string]bool
	// fail, when it holds for a command line, makes that command fail.
	fail func(line string) bool
}

func (m *manager) ctl(_ context.Context, name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	m.calls = append(m.calls, line)
	if m.fail != nil && m.fail(line) {
		return "refused", errors.New("exit status 5")
	}
	last := args[len(args)-1]
	unit := strings.TrimSuffix(filepath.Base(last), ".plist")
	if i := strings.LastIndex(last, "/"); i >= 0 && !strings.HasSuffix(last, ".plist") {
		unit = last[i+1:] // gui/<uid>/<label>
	}
	switch {
	case slices.Contains(args, "print"), slices.Contains(args, "is-enabled"):
		if !m.loaded[unit] {
			return "not found", errors.New("exit status 113")
		}
	case slices.Contains(args, "bootstrap"), slices.Contains(args, "enable"):
		m.loaded[unit] = true
	case slices.Contains(args, "bootout"), slices.Contains(args, "disable"):
		delete(m.loaded, unit)
	}
	return "", nil
}

// stubServices gives the test a service directory of its own, a manager
// that loads nothing, the system to write for and a fixed yardr to run. It
// returns the directory and the manager.
func stubServices(t *testing.T, system string) (string, *manager) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "services")
	m := &manager{loaded: map[string]bool{}}
	stub(t, &serviceDir, func() (string, error) { return dir, nil })
	stub(t, &serviceCtl, m.ctl)
	stub(t, &goos, system)
	stub(t, &executable, func() (string, error) { return "/opt/yardr/bin/yardr", nil })
	t.Setenv("PATH", "/opt/yardr/bin:/usr/bin:/bin")
	t.Setenv("HOME", "/Users/jo")
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_SESSION", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	return dir, m
}

// golden compares got with testdata/service/<name>; YARDR_UPDATE_GOLDEN=1
// writes it instead.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "service", name)
	if os.Getenv("YARDR_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs from its golden file:\n%s\nwant:\n%s", name, got, want)
	}
}

// plutilLint has macOS's own tool read a plist, where there is one.
func plutilLint(t *testing.T, plist string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	bin, err := exec.LookPath("plutil")
	if err != nil {
		return
	}
	path := filepath.Join(t.TempDir(), "x.plist")
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(bin, "-lint", path).CombinedOutput(); err != nil {
		t.Errorf("plutil -lint: %v: %s\n%s", err, out, plist)
	}
}

// An install makes the service's log with 0600 before the manager sees
// it, and chmods an existing one to it, on either manager: the manager
// writes to the file as it is, but makes a missing one with its own
// mode (yardr-6pvg). The service's file is 0600 too, of every kind: its
// environment can hold a proxy's password. One an older yardr wrote 0644
// is 0600 when it is written again, its words replaced.
func TestInstallMakesTheLogPrivate(t *testing.T) {
	for _, system := range []string{"darwin", "linux"} {
		home := newHomeDir(t)
		stubServices(t, system)
		stubLookPath(t, "git", "herdr", "systemctl")
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
		for _, c := range []struct {
			kind string
			args []string
		}{
			{svcDaemon, nil},
			{svcWeb, nil},
			{svcBDP, nil},
			{svcOTel, []string{"--endpoint", "http://127.0.0.1:4318"}},
		} {
			install := func() error {
				_, err := run(t, a().rootCmd(), append([]string{c.kind, "install"}, c.args...)...)
				return err
			}
			log := kindOf(c.kind).Log(config.At(home))
			if _, err := os.Lstat(log); !os.IsNotExist(err) {
				t.Fatalf("%s %s: %s is there before its install: %v", system, c.kind, log, err)
			}
			if err := install(); err != nil {
				t.Fatal(err)
			}
			if fi, err := os.Stat(log); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s %s: a made log is %v, %v; want 0600", system, c.kind, modeOf(fi), err)
			}
			// The service's file is 0600, and one that was 0644 is 0600
			// again once it is written, with the new words in it.
			_, _, file, err := unitFile(config.At(home), c.kind)
			if err != nil {
				t.Fatal(err)
			}
			if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s %s: a made service file is %v, %v; want 0600", system, c.kind, modeOf(fi), err)
			}
			if err := os.WriteFile(file, []byte("the words of an older install\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(file, 0o644); err != nil { // past the umask
				t.Fatal(err)
			}
			// An existing one is chmodded to it, its words kept.
			if err := os.WriteFile(log, []byte("what the service printed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(log, 0o644); err != nil { // past the umask
				t.Fatal(err)
			}
			if err := install(); err != nil {
				t.Fatal(err)
			}
			if fi, err := os.Stat(log); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s %s: a kept log is %v, %v; want 0600", system, c.kind, modeOf(fi), err)
			}
			if body, err := os.ReadFile(log); err != nil || string(body) != "what the service printed\n" {
				t.Errorf("%s %s: the install rewrote its log: %q, %v", system, c.kind, body, err)
			}
			if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s %s: a written again service file is %v, %v; want 0600", system, c.kind, modeOf(fi), err)
			}
			if body, err := os.ReadFile(file); err != nil || !strings.Contains(string(body), c.kind) || strings.Contains(string(body), "the words of an older install") {
				t.Errorf("%s %s: the install did not replace the service file: %q, %v", system, c.kind, body, err)
			}
		}
		// Where the log cannot be made the install fails, and says the
		// service is written.
		if err := os.Remove(filepath.Join(home, "daemon.log")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(home, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(home, 0o700) })
		_, err := run(t, a().rootCmd(), "daemon", "install")
		if err == nil || !strings.Contains(err.Error(), "daemon.log") || !strings.Contains(err.Error(), "written") {
			t.Errorf("%s: install into a locked home = %v", system, err)
		}
	}
}

// modeOf is fi's permission bits, for a failure's words.
func modeOf(fi os.FileInfo) os.FileMode {
	if fi == nil {
		return 0
	}
	return fi.Mode().Perm()
}

// The files as written, for each manager and each service: the yardr that
// installed it, the home, the PATH, the log, started at login and started
// again only after a failure.
func TestServiceFilesGolden(t *testing.T) {
	t.Parallel()
	env := []string{"HOME=/Users/jo", "PATH=/Users/jo/go/bin:/opt/homebrew/bin:/usr/bin:/bin", "YARDR_HOME=/Users/jo/.yardr"}
	for _, c := range []struct {
		file, manager, kind, name string
		args                      []string
		log                       string
	}{
		{"daemon.plist", launchd, svcDaemon, "dev.yardr.daemon.yardr-0a1b2c3d", []string{"daemon"}, "daemon.log"},
		{"web.plist", launchd, svcWeb, "dev.yardr.web.yardr-0a1b2c3d", []string{"web", "serve", "--addr", "127.0.0.1:8791"}, "web.log"},
		{"daemon.service", systemd, svcDaemon, "yardr-daemon-yardr-0a1b2c3d.service", []string{"daemon"}, "daemon.log"},
		{"web.service", systemd, svcWeb, "yardr-web-yardr-0a1b2c3d.service", []string{"web", "serve", "--addr", "127.0.0.1:8791"}, "web.log"},
	} {
		u := &unit{Kind: c.kind, Manager: c.manager, Name: c.name, Home: "/Users/jo/.yardr", Program: "/Users/jo/go/bin/yardr",
			Args: c.args, Env: env, Log: "/Users/jo/.yardr/" + c.log}
		got := u.render()
		golden(t, c.file, got)
		if c.manager == launchd {
			plutilLint(t, got)
		}
		// A service is on loopback and never unsafe.
		if strings.Contains(got, "unsafe") || strings.Contains(got, "0.0.0.0") {
			t.Errorf("%s:\n%s", c.file, got)
		}
	}
}

// What a path or a value may hold is written so that the manager reads it
// back as it was: XML's characters in a plist, quotes, % and $ in a unit.
func TestServiceFilesEscape(t *testing.T) {
	t.Parallel()
	home := `/Users/jo/My Yards & <co>/50% "new" -- $x`
	u := &unit{Kind: svcDaemon, Manager: launchd, Name: "dev.yardr.daemon.x-00000000", Home: home, Program: home + "/bin/yardr",
		Args: []string{"daemon"}, Env: []string{"PATH=" + home + "/bin:/usr/bin", "YARDR_HOME=" + home}, Log: home + "/daemon.log"}
	plist := u.render()
	plutilLint(t, plist)
	wantText(t, plist, "<string>/Users/jo/My Yards &amp; &lt;co&gt;/50% &#34;new&#34; -- $x/bin/yardr</string>")
	u.Manager = systemd
	wantText(t, u.render(),
		`ExecStart="/Users/jo/My Yards & <co>/50%% \"new\" -- $$x/bin/yardr" "daemon"`,
		`Environment="YARDR_HOME=/Users/jo/My Yards & <co>/50%% \"new\" -- $x"`,
		`WorkingDirectory=/Users/jo/My Yards & <co>/50%% "new" -- $x`)
}

// Two homes are two services, also when their directories have one name.
func TestServiceNameCarriesTheHome(t *testing.T) {
	stubServices(t, "darwin")
	a, b := homeID("/Users/jo/.yardr"), homeID("/Users/jo/work/.yardr")
	if a == b || !strings.HasPrefix(a, "yardr-") || !strings.HasPrefix(b, "yardr-") {
		t.Fatalf("ids %q and %q", a, b)
	}
	_, name, path, err := unitFile(&config.Config{Home: "/Users/jo/.yardr"}, svcDaemon)
	if err != nil || name != "dev.yardr.daemon."+a || filepath.Base(path) != name+".plist" {
		t.Fatalf("%q %q %v", name, path, err)
	}
}

// A service's name is the same whether or not its home exists: a home
// reached through a symlink is installed under its resolved name, and a
// file written while the home is gone is still the home's once the home
// is back.
func TestServiceNameWithoutHome(t *testing.T) {
	real := t.TempDir()
	linked := filepath.Join(shortDir(t, "yl"), "link")
	if err := os.Symlink(real, linked); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(linked, "yard")
	stubServices(t, "darwin")
	// The home is not there: the service is written under its name all
	// the same.
	u, err := newUnit(os.Environ(), config.At(home), kindOf(svcDaemon), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(u.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := u.write(); err != nil {
		t.Fatal(err)
	}
	// The home appears, and uninstall removes the service, not "nothing".
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	a := testApp(t, storetest.New(t), home)
	a.actor = "me"
	out, err := run(t, a.rootCmd(), "daemon", "uninstall")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "removed "+u.Path)
	if _, err := os.Stat(u.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("still there: %v", err)
	}
}

// egressEnviron are the proxy and CA variables of a box behind a company's
// egress proxy, each spelled out: a list taken from the code would pass
// whatever the code says. The lower-case forms are there because curl, and
// git with it, read them and not the upper-case http_proxy.
var egressEnviron = []string{"HTTPS_PROXY=http://proxy.corp:3128", "https_proxy=http://proxy.corp:3128",
	"HTTP_PROXY=http://proxy.corp:3128", "http_proxy=http://proxy.corp:3128",
	"NO_PROXY=localhost,.corp", "no_proxy=localhost,.corp",
	"SSL_CERT_FILE=/etc/corp/ca.pem", "SSL_CERT_DIR=/etc/corp/certs", "NODE_EXTRA_CA_CERTS=/etc/corp/ca.pem"}

// The environment a service gets is the server's and where herdr is, and
// nothing else of the shell: no credential, no identity. The proxy and CA
// variables are the server's, so every kind gets them, in its service and
// when it is started in the background.
func TestServiceEnv(t *testing.T) {
	t.Parallel()
	got := serviceEnv(slices.Concat([]string{"PATH=/bin", "HOME=/Users/jo", "YARDR_HOME=/elsewhere", "YARDR_ACTOR=me", "YARDR_SESSION=s1",
		"GITHUB_TOKEN=secret", "YARDR_HOOK_ADDR=127.0.0.1:8790", "YARDR_INBOX_RETENTION=30d", "HERDR_SOCKET_PATH=/tmp/h.sock",
		"HERDR_ENV=1", "XDG_CONFIG_HOME=/Users/jo/.config", "YARDR_TEST_OWNER=4242 987654", "YARDR_TEST_HOME_CHECK=1s",
		"ALL_PROXY=socks5://proxy.corp:1080"}, egressEnviron), "/Users/jo/.yardr", kindOf(svcDaemon))
	// In the order a service's file has them: sorted, upper case first.
	want := []string{"HERDR_SOCKET_PATH=/tmp/h.sock", "HOME=/Users/jo", "HTTPS_PROXY=http://proxy.corp:3128",
		"HTTP_PROXY=http://proxy.corp:3128", "NODE_EXTRA_CA_CERTS=/etc/corp/ca.pem", "NO_PROXY=localhost,.corp",
		"PATH=/bin", "SSL_CERT_DIR=/etc/corp/certs", "SSL_CERT_FILE=/etc/corp/ca.pem", "XDG_CONFIG_HOME=/Users/jo/.config",
		"YARDR_HOME=/Users/jo/.yardr", "YARDR_HOOK_ADDR=127.0.0.1:8790", "YARDR_INBOX_RETENTION=30d",
		"http_proxy=http://proxy.corp:3128", "https_proxy=http://proxy.corp:3128", "no_proxy=localhost,.corp"}
	if !slices.Equal(got, want) {
		t.Fatalf("env = %q\nwant %q", got, want)
	}
	for _, k := range longRunnings {
		for what, env := range map[string][]string{"service": serviceEnv(egressEnviron, "/h", k), "start": k.env(egressEnviron, "/h")} {
			for _, kv := range egressEnviron {
				if !slices.Contains(env, kv) {
					t.Errorf("%s %s: no %s in %q", k.Name, what, kv, env)
				}
			}
		}
	}
	// A kind's own variables are its service's too, and no other kind's.
	environ := []string{"PATH=/bin", "OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer t", "OTEL_SERVICE_NAME=x", "GITHUB_TOKEN=secret"}
	for _, k := range longRunnings {
		got := serviceEnv(environ, "/h", k)
		if has := slices.Contains(got, environ[1]); has != (k.Name == svcOTel) || slices.Contains(got, environ[2]) || slices.Contains(got, environ[3]) {
			t.Errorf("%s: env = %q", k.Name, got)
		}
	}
}

// What an install prints of a unit's environment and flags: the password of
// a URL's userinfo as url.URL.Redacted hides it (also in an address with no
// scheme, which Go and curl read as http://), the export's request headers
// whole, and every other word as it is. The unit's own words are not
// changed: its file has them.
func TestShown(t *testing.T) {
	t.Parallel()
	same := ""
	for _, c := range []struct{ name, in, want string }{
		{"a proxy with a password", "HTTPS_PROXY=http://ada:secret@proxy.example:3128", "HTTPS_PROXY=http://ada:xxxxx@proxy.example:3128"},
		{"the lower-case name", "https_proxy=http://ada:secret@proxy.example:3128", "https_proxy=http://ada:xxxxx@proxy.example:3128"},
		{"an @ in the password", "HTTP_PROXY=socks5://ada:p@ss@proxy.example:1080", "HTTP_PROXY=socks5://ada:xxxxx@proxy.example:1080"},
		{"an escaped password", "HTTPS_PROXY=http://ada:p%40ss@proxy.example:3128/", "HTTPS_PROXY=http://ada:xxxxx@proxy.example:3128/"},
		{"no scheme", "HTTPS_PROXY=ada:secret@proxy.example:3128", "HTTPS_PROXY=ada:xxxxx@proxy.example:3128"},
		{"a user and no password", "HTTPS_PROXY=http://ada@proxy.example:3128", same},
		{"no userinfo", "HTTPS_PROXY=http://proxy.example:3128", same},
		{"no userinfo, no scheme", "HTTPS_PROXY=proxy.example:3128", same},
		{"hosts", "NO_PROXY=localhost,.corp", same},
		{"a PATH with an @ in a directory", "PATH=/opt/homebrew/opt/python@3.11/bin:/usr/bin", same},
		{"an address", "YARDR_HOOK_ADDR=127.0.0.1:8790", same},
		{"a value that has an =", "SSL_CERT_FILE=/etc/corp/ca=1.pem", same},
		{"a word that is no KEY=value", "STRAY", same},
		{"a collector with a password", "OTEL_EXPORTER_OTLP_ENDPOINT=https://ada:secret@collector.example:4318", "OTEL_EXPORTER_OTLP_ENDPOINT=https://ada:xxxxx@collector.example:4318"},
		{"the export's headers", "OTEL_EXPORTER_OTLP_HEADERS=Authorization=Bearer example-secret", "OTEL_EXPORTER_OTLP_HEADERS=(a value, not shown)"},
		{"a signal's headers", "OTEL_EXPORTER_OTLP_LOGS_HEADERS=k=v", "OTEL_EXPORTER_OTLP_LOGS_HEADERS=(a value, not shown)"},
		{"headers that are none", "OTEL_EXPORTER_OTLP_HEADERS=", same},
		{"another variable of the export", "OTEL_EXPORTER_OTLP_TIMEOUT=10", same},
		{"headers of another program", "HEADERS=Authorization=Bearer example-secret", same},
	} {
		in := []string{c.in}
		got := shownEnv(in)
		if want := or(c.want, c.in); !slices.Equal(got, []string{want}) {
			t.Errorf("%s: %q is shown as %q, want %q", c.name, c.in, got, want)
		}
		if in[0] != c.in {
			t.Errorf("%s: the unit's own word is now %q", c.name, in[0])
		}
	}

	args := []string{"otel", "export", "--endpoint", "https://ada:secret@collector.example:4318", "--header", "Authorization=Bearer example-secret",
		"--header", "x-tenant=ada", "--after", "5m", "--include-text=true"}
	own := slices.Clone(args)
	want := []string{"otel", "export", "--endpoint", "https://ada:xxxxx@collector.example:4318", "--header", "(a value, not shown)",
		"--header", "(a value, not shown)", "--after", "5m", "--include-text=true"}
	if got := shownArgs(args); !slices.Equal(got, want) {
		t.Errorf("flags are shown as %q, want %q", got, want)
	}
	if !slices.Equal(args, own) {
		t.Errorf("the unit's own flags are now %q", args)
	}
}

// yardr daemon install writes the agent into the directory, loads it, and
// doctor reports it; an install over it replaces it; uninstall unloads it
// and removes the file, and nothing else.
func TestDaemonInstallUninstall(t *testing.T) {
	home := newHomeDir(t)
	dir, m := stubServices(t, "darwin")
	stubLookPath(t, "git", "herdr")
	st := storetest.New(t)
	testYard(t, st, home)
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	rund := func(args ...string) (string, error) {
		t.Helper()
		return run(t, a().rootCmd(), args...)
	}
	if _, err := rund("init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	stub(t, &userHomeDir, func() (string, error) { return "/Users/jo", nil })
	if err := st.PutDepot(context.Background(), store.Depot{Name: "notes", Path: "/Users/jo/Documents/notes"}, "me"); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "com.example.other.plist")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := rund("daemon", "install")
	if err != nil {
		t.Fatal(err)
	}
	label := "dev.yardr.daemon." + homeID(home)
	file := filepath.Join(dir, label+".plist")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	plutilLint(t, string(body))
	wantText(t, string(body), "<string>"+label+"</string>", "<string>/opt/yardr/bin/yardr</string>\n\t\t<string>daemon</string>",
		"<key>YARDR_HOME</key>\n\t\t<string>"+home+"</string>", "<key>PATH</key>\n\t\t<string>/opt/yardr/bin:/usr/bin:/bin</string>",
		"<string>"+home+"/daemon.log</string>")
	wantText(t, out, "launchd agent "+label+" written: "+file, "launchd has started the daemon and starts it at every login",
		// The harness is not on the PATH the service gets, and one depot is
		// where a login daemon has no access.
		"Mind: claude is not on the PATH written into the service",
		"Mind: depot notes (/Users/jo/Documents/notes) is in a folder macOS protects", "Full Disk Access (add /opt/yardr/bin/yardr)",
		hereAt(home, "yardr daemon uninstall"))
	if strings.Contains(out, "herdr is not on the PATH") {
		t.Errorf("herdr is on PATH:\n%s", out)
	}
	dom := domain()
	if want := []string{"launchctl print " + dom + "/" + label, "launchctl bootstrap " + dom + " " + file}; !slices.Equal(m.calls, want) {
		t.Fatalf("calls = %q", m.calls)
	}
	out, _ = rund("doctor")
	wantText(t, out, "configured      login service: the daemon starts at login ("+file+")",
		"note            login service: depot notes (/Users/jo/Documents/notes) is in a folder macOS protects")
	out, _ = rund("daemon", "status")
	wantText(t, out, "daemon   not running", "service  installed and loaded: launchd starts it at login ("+file+")")

	// Again: the loaded one is taken out before the new one goes in.
	m.calls = nil
	if _, err := rund("daemon", "install"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"launchctl print " + dom + "/" + label, "launchctl bootout " + dom + "/" + label,
		"launchctl print " + dom + "/" + label, "launchctl bootstrap " + dom + " " + file}; !slices.Equal(m.calls, want) {
		t.Fatalf("calls = %q", m.calls)
	}

	m.calls = nil
	out, err = rund("daemon", "uninstall")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "removed "+file, "has stopped the daemon it ran", hereAt(home, "yardr init --start"))
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) || m.loaded[label] {
		t.Fatalf("still there: %v, loaded %v", err, m.loaded)
	}
	if b, _ := os.ReadFile(other); string(b) != "theirs" {
		t.Fatalf("another agent's file was touched: %q", b)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 {
		t.Fatalf("left in %s: %v", dir, left)
	}
	out, _ = rund("doctor")
	wantText(t, out, "not configured  login service: ", hereAt(home, "yardr daemon install starts it at login"))
	if strings.Contains(out, "macOS protects") {
		t.Errorf("no service, and still the note:\n%s", out)
	}

	// Nothing installed: uninstall says so and asks the manager nothing.
	m.calls = nil
	out, err = rund("daemon", "uninstall")
	if err != nil || !strings.Contains(out, "No daemon service is installed") || len(m.calls) != 0 {
		t.Fatalf("%v, calls %q\n%s", err, m.calls, out)
	}
}

// herdrLoginLineAt is the line doctor prints for the login service of
// herdr's server, for the yard in home.
func herdrLoginLineAt(home string) string {
	return herdrLoginPart(&config.Config{Home: home}).line()
}

// yardr daemon install ends with the same one line doctor prints for
// herdr's server, after its own output; yardr web install says nothing of
// it, and --json stays one object.
func TestDaemonInstallNamesHerdrLogin(t *testing.T) {
	home := newHomeDir(t)
	stubServices(t, "darwin")
	stubLookPath(t, "git", "herdr")
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
	out, err := run(t, a().rootCmd(), "daemon", "install")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, herdrLoginLineAt(home), "herdr's server does not start at login", hereAt(home, "yardr up --login"), "Keep it running after a reboot")
	if strings.Index(out, herdrLoginLineAt(home)) < strings.Index(out, "To remove it") {
		t.Errorf("the herdr line comes before the install's own output:\n%s", out)
	}
	out, err = run(t, a().rootCmd(), "web", "install")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, herdrLoginLineAt(home)) || strings.Contains(out, "herdr's server") {
		t.Errorf("the web view's install names herdr:\n%s", out)
	}
	out, err = run(t, a().rootCmd(), "daemon", "install", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res serviceResult
	if jerr := json.Unmarshal([]byte(out), &res); jerr != nil || !res.Loaded {
		t.Fatalf("json install: %v\n%s", jerr, out)
	}
}

// --file-only writes and removes the file and leaves the manager alone. The
// file is its owner's alone, a 0644 one included once it is written again,
// and holds the proxy's address as the starter has it, password and all;
// what the install prints, as text and as JSON, has the password hidden.
func TestDaemonInstallFileOnly(t *testing.T) {
	home := newHomeDir(t)
	dir, m := stubServices(t, "linux")
	const proxy, redacted = "http://ada:secret@proxy.example:3128", "http://ada:xxxxx@proxy.example:3128"
	st := storetest.New(t)
	testYard(t, st, home, "HTTPS_PROXY="+proxy)
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	out, err := run(t, a().rootCmd(), "daemon", "install", "--file-only")
	if err != nil {
		t.Fatal(err)
	}
	name := "yardr-daemon-" + homeID(home) + ".service"
	file := filepath.Join(dir, name)
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, string(body), `ExecStart="/opt/yardr/bin/yardr" "daemon"`, `Environment="YARDR_HOME=`+home+`"`, "WantedBy=default.target",
		`Environment="HTTPS_PROXY=`+proxy+`"`)
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the service file is %v, %v; want 0600", modeOf(fi), err)
	}
	wantText(t, out, "systemd user unit "+name+" written", "Not loaded (--file-only)",
		"systemctl --user daemon-reload && systemctl --user enable "+name+" && systemctl --user restart "+name, "HTTPS_PROXY="+redacted+"\n")
	if strings.Contains(out, "secret") {
		t.Errorf("the install prints the proxy's password:\n%s", out)
	}
	if len(m.calls) != 0 {
		t.Fatalf("calls = %q", m.calls)
	}

	// An install over a file an older yardr wrote 0644 makes it 0600.
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a().rootCmd(), "daemon", "install", "--file-only", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res serviceResult
	if jerr := json.Unmarshal([]byte(out), &res); jerr != nil || !slices.Contains(res.Env, "HTTPS_PROXY="+redacted) || strings.Contains(out, "secret") {
		t.Errorf("the install's JSON (%v) shows the proxy's password, or not its address:\n%s", jerr, out)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the service file written again is %v, %v; want 0600", modeOf(fi), err)
	}
	if body, err := os.ReadFile(file); err != nil || !strings.Contains(string(body), `Environment="HTTPS_PROXY=`+proxy+`"`) {
		t.Errorf("the file written again lost the proxy's address: %q, %v", body, err)
	}

	out, err = run(t, a().rootCmd(), "daemon", "uninstall", "--file-only")
	if err != nil || !strings.Contains(out, "Not unloaded (--file-only)") || len(m.calls) != 0 {
		t.Fatalf("%v, calls %q\n%s", err, m.calls, out)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("still there: %v", err)
	}
}

// On Linux the unit is loaded with systemctl, and taken out with it.
func TestDaemonInstallSystemd(t *testing.T) {
	home := newHomeDir(t)
	_, m := stubServices(t, "linux")
	stubLookPath(t, "git", "herdr", "systemctl")
	a := testApp(t, storetest.New(t), home)
	a.actor = "me"
	out, err := run(t, a.rootCmd(), "daemon", "install")
	if err != nil {
		t.Fatal(err)
	}
	name := "yardr-daemon-" + homeID(home) + ".service"
	if want := []string{"systemctl --user is-enabled " + name, "systemctl --user daemon-reload", "systemctl --user enable " + name,
		"systemctl --user restart " + name}; !slices.Equal(m.calls, want) {
		t.Fatalf("calls = %q", m.calls)
	}
	wantText(t, out, "systemd has started the daemon and starts it at every login", "loginctl enable-linger")
	m.calls = nil
	if _, err := run(t, a.rootCmd(), "daemon", "uninstall"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"systemctl --user is-enabled " + name, "systemctl --user disable --now " + name,
		"systemctl --user daemon-reload"}; !slices.Equal(m.calls, want) {
		t.Fatalf("calls = %q", m.calls)
	}

	// No systemd: nothing is written.
	stubLookPath(t, "git", "herdr")
	_, err = run(t, a.rootCmd(), "daemon", "install")
	if err == nil || !strings.Contains(err.Error(), "systemctl is not on PATH") || serviceInstalled(&config.Config{Home: home}, svcDaemon) {
		t.Fatalf("err = %v", err)
	}
}

// A system yardr knows no service manager of: it says so and how to run the
// daemon by hand, and writes nothing.
func TestServiceNotSupported(t *testing.T) {
	dir, m := stubServices(t, "freebsd")
	a := testApp(t, storetest.New(t), newHomeDir(t))
	a.actor = "me"
	for _, args := range [][]string{{"daemon", "install"}, {"daemon", "uninstall"}} {
		_, err := run(t, a.rootCmd(), args...)
		if err == nil || !strings.Contains(err.Error(), "a login service is not supported on freebsd") ||
			!strings.Contains(err.Error(), "`yardr daemon` in a terminal of its own") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) || len(m.calls) != 0 {
		t.Fatalf("written or asked: %v, %q", err, m.calls)
	}
	out, err := run(t, a.rootCmd(), "daemon", "status")
	if err != nil || !strings.Contains(out, "service  not supported on freebsd") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// holdDaemon takes the home's daemon lock as a running daemon holds it, and
// makes a stop signal release it. It returns the pids signalled.
func holdDaemon(t *testing.T, home string) *[]int {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockDaemon(filepath.Join(home, "daemon.lock"), 0)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	stub(t, &signalStop, func(pid int) error {
		pids = append(pids, pid)
		unlock()
		return nil
	})
	return &pids
}

// yardr daemon stop stops the daemon that holds the lock and waits for it;
// with none running it says so. status reports the pid.
func TestDaemonStopAndStatus(t *testing.T) {
	home := newHomeDir(t)
	stubServices(t, "darwin")
	a := testApp(t, storetest.New(t), home)
	a.actor = "me"
	out, err := run(t, a.rootCmd(), "daemon", "stop")
	if err != nil || !strings.Contains(out, "no daemon is running") {
		t.Fatalf("%v\n%s", err, out)
	}
	pids := holdDaemon(t, home)
	me := os.Getpid()
	out, err = run(t, a.rootCmd(), "daemon", "status", "--json")
	var status struct {
		Running bool
		PID     int
		Service serviceState
	}
	if jerr := json.Unmarshal([]byte(out), &status); err != nil || jerr != nil || !status.Running || status.PID != me ||
		!status.Service.Supported || status.Service.Installed {
		t.Fatalf("%v %v\n%s", err, jerr, out)
	}
	out, err = run(t, a.rootCmd(), "daemon", "status")
	if err != nil || !strings.Contains(out, "service  "+hereAt(home, "not installed (yardr daemon install)")) {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err = run(t, a.rootCmd(), "daemon", "stop")
	if err != nil || !slices.Equal(*pids, []int{me}) || !strings.Contains(out, "daemon stopped (pid ") || config.DaemonRunning(filepath.Join(home, "daemon.lock")) {
		t.Fatalf("%v, signalled %v\n%s", err, *pids, out)
	}
}

// An install stops the daemon that runs before the service's is started:
// the yard has one, and the service's would end on the other's lock.
func TestDaemonInstallStopsTheRunningDaemon(t *testing.T) {
	home := newHomeDir(t)
	_, m := stubServices(t, "darwin")
	pids := holdDaemon(t, home)
	stopped := -1
	signal := signalStop
	stub(t, &signalStop, func(pid int) error { stopped = len(m.calls); return signal(pid) })
	a := testApp(t, storetest.New(t), home)
	a.actor = "me"
	out, err := run(t, a.rootCmd(), "daemon", "install")
	if err != nil {
		t.Fatal(err)
	}
	// Signalled after the question whether it is loaded, before the bootstrap.
	if len(*pids) != 1 || stopped != 1 || len(m.calls) != 2 || !strings.Contains(m.calls[1], "bootstrap") {
		t.Fatalf("signalled %v at %d of %q", *pids, stopped, m.calls)
	}
	wantText(t, out, "The daemon that was running (pid ", "the service runs the yard's one daemon now")

	// A manager that refuses leaves the file, and says that it is not loaded.
	m.fail = func(line string) bool { return strings.Contains(line, "bootstrap") }
	_, err = run(t, a.rootCmd(), "daemon", "install")
	if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), "written") || !strings.Contains(err.Error(), "not loaded") {
		t.Fatalf("err = %v", err)
	}
}

// yardr web install keeps the view on loopback: an address off it is
// refused and there is no --unsafe, and the file names the address, which
// doctor then reports.
func TestWebInstall(t *testing.T) {
	home := newHomeDir(t)
	dir, m := stubServices(t, "darwin")
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
	for _, args := range [][]string{{"--addr", "0.0.0.0:8791"}, {"--addr", ":8791"}, {"--addr", "192.168.1.5:8791"}, {"--unsafe"}} {
		if _, err := run(t, a().rootCmd(), append([]string{"web", "install"}, args...)...); err == nil {
			t.Fatalf("%v was accepted", args)
		}
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 || len(m.calls) != 0 {
		t.Fatalf("written or asked: %v, %q", left, m.calls)
	}
	out, err := run(t, a().rootCmd(), "web", "install", "--addr", "127.0.0.1:9000")
	if err != nil {
		t.Fatal(err)
	}
	label := "dev.yardr.web." + homeID(home)
	file := filepath.Join(dir, label+".plist")
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	plutilLint(t, string(body))
	wantText(t, string(body), "<string>web</string>\n\t\t<string>serve</string>\n\t\t<string>--addr</string>\n\t\t<string>127.0.0.1:9000</string>",
		"<string>"+home+"/web.log</string>")
	if strings.Contains(string(body), "unsafe") {
		t.Errorf("the service is unsafe:\n%s", body)
	}
	wantText(t, out, "The web view: http://127.0.0.1:9000/", "launchd has started the web view")
	out, _ = run(t, a().rootCmd(), "doctor")
	wantText(t, out, "configured      web view: kept running as a login service at http://127.0.0.1:9000/ ("+file+")",
		"not configured  login service: ")
	if _, err := run(t, a().rootCmd(), "web", "uninstall"); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("left: %v", left)
	}
	out, _ = run(t, a().rootCmd(), "doctor")
	wantText(t, out, "not configured  web view: ", hereAt(home, "yardr web install keeps it running from login on"))

	// The same from a systemd unit.
	stub(t, &goos, "linux")
	stubLookPath(t, "systemctl")
	if _, err := run(t, a().rootCmd(), "web", "install", "--addr", "[::1]:9001"); err != nil {
		t.Fatal(err)
	}
	if got := webServiceAddr(&config.Config{Home: home}); got != "[::1]:9001" {
		t.Fatalf("address read back: %q", got)
	}
}

// yardr web install --host writes the names into the service, each as its
// own --host whether it came alone, in a comma list or from a flag given
// again, after the address, which is still read back from the file. A name
// yardr web serve would refuse on loopback is refused before a file is
// written, and an address off loopback is refused with the loopback line
// whatever --host says.
func TestWebInstallHost(t *testing.T) {
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
	for _, c := range []struct {
		system, file string
		want         []string
	}{
		{"darwin", "dev.yardr.web." + homeID(home) + ".plist", []string{"<string>serve</string>\n\t\t<string>--addr</string>\n\t\t<string>127.0.0.1:8791</string>\n" +
			"\t\t<string>--host</string>\n\t\t<string>some.name</string>\n\t\t<string>--host</string>\n\t\t<string>b.example</string>\n" +
			"\t\t<string>--host</string>\n\t\t<string>c.example</string>"}},
		{"linux", "yardr-web-" + homeID(home) + ".service", []string{`"web" "serve" "--addr" "127.0.0.1:8791" "--host" "some.name" "--host" "b.example" "--host" "c.example"`}},
	} {
		dir, m := stubServices(t, c.system)
		for _, refused := range []struct {
			args []string
			want string
		}{
			{[]string{"--host", "*"}, "wildcard"},
			{[]string{"--host", ""}, "no name"},
			{[]string{"--host", "some.name:8791"}, "without a scheme or a port"},
			{[]string{"--host", "some.name,192.168.1.5"}, "--host 192.168.1.5 refused: yardr web start and yardr web install never listen off loopback"},
			{[]string{"--host", "some.name", "--addr", "0.0.0.0:8791"}, "yardr web start and yardr web install never listen off loopback"},
		} {
			_, err := run(t, a().rootCmd(), append([]string{"web", "install", "--file-only"}, refused.args...)...)
			if err == nil || !strings.Contains(err.Error(), refused.want) {
				t.Errorf("%s %v: %v, want a refusal that says %q", c.system, refused.args, err, refused.want)
			}
			// The flag to give too, in serve's words, is none of install's.
			if err != nil && strings.Contains(err.Error(), "Give --unsafe") {
				t.Errorf("%s %v: the refusal offers --unsafe: %v", c.system, refused.args, err)
			}
		}
		if left, _ := os.ReadDir(dir); len(left) != 0 {
			t.Fatalf("%s: a refused install wrote %v", c.system, left)
		}
		out, err := run(t, a().rootCmd(), "web", "install", "--host", "some.name,b.example", "--host", "c.example", "--file-only")
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(dir, c.file))
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, string(body), c.want...)
		if strings.Contains(string(body), "unsafe") {
			t.Errorf("the service is unsafe:\n%s", body)
		}
		wantText(t, out, "yardr web serve --addr 127.0.0.1:8791 --host some.name --host b.example --host c.example", "Not loaded (--file-only)")
		if got := webServiceAddr(&config.Config{Home: home}); got != "127.0.0.1:8791" {
			t.Errorf("%s: address read back: %q", c.system, got)
		}
		if len(m.calls) != 0 {
			t.Errorf("%s: --file-only asked the manager: %q", c.system, m.calls)
		}
	}
}

// yardr web start --host hands the names on to yardr web serve as install
// writes them, and refuses what serve would on loopback before anything is
// started.
func TestWebStartHost(t *testing.T) {
	st := storetest.New(t)
	testYard(t, st, newHomeDir(t))
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	var started [][]string
	stub(t, &spawn, func(_ []string, _ *config.Config, k *longRunning, args []string) (int, <-chan error, func() error, error) {
		started = append(started, append(k.command(), args...))
		return 4242, nil, func() error { return nil }, nil
	})
	stub(t, &answers, func(string) bool { return len(started) > 0 })
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--host", "*.ts.net"}, "wildcard"},
		{[]string{"--host", "10.0.0.1"}, "--host 10.0.0.1 refused: yardr web start and yardr web install never listen off loopback"},
		{[]string{"--host", "some.name", "--addr", "192.168.1.5:9000"}, "yardr web start and yardr web install never listen off loopback"},
	} {
		if _, err := run(t, a().rootCmd(), append([]string{"web", "start"}, c.args...)...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want a refusal that says %q", c.args, err, c.want)
		}
	}
	if len(started) != 0 {
		t.Fatalf("a refused start started %q", started)
	}
	for _, c := range []struct{ args, want []string }{
		{[]string{"--host", "some.name"}, []string{"--host", "some.name"}},
		{[]string{"--host", "some.name", "--host", "b.example"}, []string{"--host", "some.name", "--host", "b.example"}},
		{[]string{"--host", "some.name,b.example", "--host", "c.example"}, []string{"--host", "some.name", "--host", "b.example", "--host", "c.example"}},
	} {
		started = nil
		out, err := run(t, a().rootCmd(), append([]string{"web", "start", "--addr", "127.0.0.1:9000"}, c.args...)...)
		want := append([]string{"web", "serve", "--addr", "127.0.0.1:9000"}, c.want...)
		if err != nil || len(started) != 1 || !slices.Equal(started[0], want) {
			t.Fatalf("%v: %v, started %q, want %q", c.args, err, started, want)
		}
		wantText(t, out, "The web view: http://127.0.0.1:9000/")
	}
}

// yardr web install --as writes the user into the service, and the view
// it starts acts as them.
func TestWebInstallAs(t *testing.T) {
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
	for _, c := range []struct {
		system, file string
		want         []string
	}{
		{"darwin", "dev.yardr.web." + homeID(home) + ".plist", []string{"<string>--as</string>\n\t\t<string>Jo</string>"}},
		{"linux", "yardr-web-" + homeID(home) + ".service", []string{`"--as" "Jo"`}},
	} {
		dir, _ := stubServices(t, c.system)
		out, err := run(t, a().rootCmd(), "web", "install", "--as", "Jo", "--file-only")
		if err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(filepath.Join(dir, c.file))
		if err != nil {
			t.Fatal(err)
		}
		wantText(t, string(body), c.want...)
		wantText(t, out, "--as Jo")
	}
}

// yardr web start --as hands the user on to yardr web serve.
func TestWebStartAs(t *testing.T) {
	st := storetest.New(t)
	testYard(t, st, newHomeDir(t))
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
	out, err := run(t, a().rootCmd(), "web", "start", "--addr", "127.0.0.1:9000", "--as", "Jo")
	want := []string{"web", "serve", "--addr", "127.0.0.1:9000", "--as", "Jo"}
	if err != nil || len(started) != 1 || !slices.Equal(started[0], want) {
		t.Fatalf("%v, started %q, want %q", err, started, want)
	}
	wantText(t, out, "The web view: http://127.0.0.1:9000/")
}

// stubWeb replaces the web view's start: it records the addresses started,
// and the view listens from then on.
func stubWeb(t *testing.T) *[]string {
	t.Helper()
	var started []string
	stub(t, &spawn, func(_ []string, _ *config.Config, k *longRunning, args []string) (int, <-chan error, func() error, error) {
		if k.Name != svcWeb || len(args) != 2 || args[0] != "--addr" {
			t.Errorf("started %s %q", k.Name, args)
		}
		started = append(started, args[1])
		return 4242, nil, func() error { return nil }, nil
	})
	stub(t, &answers, func(addr string) bool { return slices.Contains(started, addr) })
	return &started
}

// yardr web start starts the view in the background and prints its address;
// an address something listens on already is not started on, and one off
// loopback is refused.
func TestWebStart(t *testing.T) {
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
	started := stubWeb(t)
	out, err := run(t, a().rootCmd(), "web", "start")
	if err != nil || !slices.Equal(*started, []string{"127.0.0.1:8791"}) {
		t.Fatalf("%v, started %v", err, *started)
	}
	wantText(t, out, "The web view: http://127.0.0.1:8791/", "(pid 4242)", home+"/web.log", "To stop it: yardr --home "+home+" web stop")
	_, err = run(t, a().rootCmd(), "web", "start")
	if err == nil || !strings.Contains(err.Error(), "something listens on 127.0.0.1:8791 already") || len(*started) != 1 {
		t.Fatalf("err = %v, started %v", err, *started)
	}
	if _, err := run(t, a().rootCmd(), "web", "start", "--addr", "0.0.0.0:9000"); err == nil || len(*started) != 1 {
		t.Fatalf("off loopback: %v, started %v", err, *started)
	}

	// One that ends at once (its port taken between the look and the start).
	gone := make(chan error, 1)
	gone <- errors.New("exit status 1")
	stub(t, &spawn, func([]string, *config.Config, *longRunning, []string) (int, <-chan error, func() error, error) {
		return 1, gone, func() error { return nil }, nil
	})
	stub(t, &answers, func(string) bool { return false })
	if _, err := run(t, a().rootCmd(), "web", "start"); err == nil || !strings.Contains(err.Error(), "ended right after it started") {
		t.Fatalf("err = %v", err)
	}

	// A child that never binds the address is stopped before start returns.
	stopped := false
	stub(t, &startWait, 10*time.Millisecond)
	stub(t, &spawn, func([]string, *config.Config, *longRunning, []string) (int, <-chan error, func() error, error) {
		return 7, nil, func() error { stopped = true; return nil }, nil
	})
	if _, err := run(t, a().rootCmd(), "web", "start"); err == nil || !strings.Contains(err.Error(), "stopped it") || !stopped {
		t.Fatalf("err = %v, stopped = %v", err, stopped)
	}
}

// onboardTo runs the onboarding to its end with the answers given, on a
// yard whose yardmaster comes up. It returns the output and the keys asked.
func onboardTo(t *testing.T, st store.API, answers map[string]any) (string, []string) {
	t.Helper()
	setInteractive(t, true)
	stubLookPath(t, "claude", "git", "herdr")
	f := &former{t: t, answers: answers, goes: []string{goYardmaster}}
	stubAsk(t, f.ask)
	stubStart(t, st, yardmasterHandle())
	stubPane(t, answeredPane)
	stubFocus(t)
	out, err := run(t, appOn(st).rootCmd())
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, k := range f.asked {
		if k != "" {
			keys = append(keys, k)
		}
	}
	return out, keys
}

// The onboarding asks both once the yard runs, each starting at no: left at
// that, nothing is installed, loaded or started.
func TestOnboardingAsksForTheServices(t *testing.T) {
	home := newHomeDir(t)
	dir, m := stubServices(t, "darwin")
	started := stubWeb(t)
	st := storetest.New(t)
	// The run acts as the login name, as it does on a person's machine.
	testYard(t, st, home, "USER="+os.Getenv("USER"))
	out, keys := onboardTo(t, st, nil)
	if !slices.Equal(keys, []string{"style", "about", "try", "login", "web", "link", "go"}) {
		t.Fatalf("asked %q\n%s", keys, out)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 || len(m.calls) != 0 || len(*started) != 0 {
		t.Fatalf("written %v, asked %q, started %v", left, m.calls, *started)
	}
	if strings.Contains(out, hereAt(home, "yardr daemon install")) || strings.Contains(out, "The web view") {
		t.Errorf("output:\n%s", out)
	}
}

// Yes to the daemon at login installs the service; yes to the web view and
// no to "and at login?" starts it for now and prints its address.
func TestOnboardingInstallsTheDaemonAndStartsTheWebView(t *testing.T) {
	home := newHomeDir(t)
	dir, m := stubServices(t, "darwin")
	started := stubWeb(t)
	st := storetest.New(t)
	// The run acts as the login name, as it does on a person's machine.
	testYard(t, st, home, "USER="+os.Getenv("USER"))
	out, keys := onboardTo(t, st, map[string]any{"login": true, "web": true})
	if !slices.Equal(keys, []string{"style", "about", "try", "login", "web", "web_login", "link", "go"}) {
		t.Fatalf("asked %q\n%s", keys, out)
	}
	label := "dev.yardr.daemon." + homeID(home)
	if _, err := os.Stat(filepath.Join(dir, label+".plist")); err != nil || !m.loaded[label] {
		t.Fatalf("the daemon's service: %v, loaded %v\n%s", err, m.loaded, out)
	}
	if left, _ := os.ReadDir(dir); len(left) != 1 || !slices.Equal(*started, []string{"127.0.0.1:8791"}) {
		t.Fatalf("written %v, started %v", left, *started)
	}
	wantText(t, out, hereAt(home, "yardr daemon install"), "launchd has started the daemon and starts it at every login",
		hereAt(home, "yardr web start"), "The web view: http://127.0.0.1:8791/", "yardmaster is running, in herdr")
}

// Yes to "and at login?" installs the web view as a service instead, which
// starts it: it is not started a second time.
func TestOnboardingInstallsTheWebView(t *testing.T) {
	home := newHomeDir(t)
	dir, m := stubServices(t, "linux")
	stubLookPath(t, "claude", "git", "herdr", "systemctl")
	started := stubWeb(t)
	setInteractive(t, true)
	f := &former{t: t, answers: map[string]any{"web": true, "web_login": true}, goes: []string{goYardmaster}}
	stubAsk(t, f.ask)
	st := storetest.New(t)
	// The run acts as the login name, as it does on a person's machine.
	testYard(t, st, home, "USER="+os.Getenv("USER"))
	stubStart(t, st, yardmasterHandle())
	stubPane(t, answeredPane)
	stubFocus(t)
	out, err := run(t, appOn(st).rootCmd())
	if err != nil {
		t.Fatal(err)
	}
	name := "yardr-web-" + homeID(home) + ".service"
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil || !m.loaded[name] || len(*started) != 0 {
		t.Fatalf("%v, loaded %v, started %v\n%s", err, m.loaded, *started, out)
	}
	wantText(t, out, hereAt(home, "yardr web install"), "The web view: http://127.0.0.1:8791/")
}

// Where yardr writes no service, the login questions are not asked; the web
// view is still offered, for now. A start that fails does not end the
// onboarding.
func TestOnboardingWithoutAServiceManager(t *testing.T) {
	st := storetest.New(t)
	stubServices(t, "freebsd")
	// The run acts as the login name, as it does on a person's machine.
	testYard(t, st, newHomeDir(t), "USER="+os.Getenv("USER"))
	out, keys := onboardTo(t, st, map[string]any{"web": true})
	if !slices.Equal(keys, []string{"style", "about", "try", "web", "link", "go"}) {
		t.Fatalf("asked %q\n%s", keys, out)
	}
	wantText(t, out, "yardr: tests start nothing in the background", "The yard runs without it", "yardmaster is running, in herdr")
}
