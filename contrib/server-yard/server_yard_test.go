// Package serveryard tests the server-yard script: that one run sets a
// yard up as the server steps of docs/install.md say, that herdr's unit
// carries the proxy and CA variables of the script's environment, that a
// second run changes nothing, and that where a step cannot be taken the
// script stops with the way on. Each test builds yardr from the tree and
// runs the script with stand-ins for herdr (fake/, a socket and a CLI that
// answer what a setup asks), the harness, systemctl, loginctl and, on
// macOS, launchctl, with HOME, XDG_CONFIG_HOME and YARDR_HOME in a temp
// dir. The systemctl and launchctl stand-ins model the manager starting a
// unit: herdr's server and the daemon really start; the web view's unit is
// only marked, since starting it would bind 127.0.0.1:8791.
package serveryard

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/contrib/server-yard/managers"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/storetest"
)

// TestMain names this test binary to the servers its yards start: a binary
// killed or timed out runs no Cleanup, y.stop is only that, and the server
// and the daemon of a yard would outlive it (storetest.NameTestBinary).
func TestMain(m *testing.M) {
	storetest.NameTestBinary()
	os.Exit(m.Run())
}

// yard holds one test's directories: D is short (/tmp/sy.XXXXXX), since a
// unix socket path has a length limit.
type yard struct {
	d        string
	bin      string
	path     string // the PATH the script runs with: the shim, then bin
	initLog  string // what the shim saw of each yardr init's stdin
	home     string
	config   string
	yardhome string
	sock     string
	state    string
	env      []string
}

// build makes D: yardr from the tree, the fake herdr, the shell stand-ins,
// and the directories the script writes to.
func build(t *testing.T) *yard {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	d, err := os.MkdirTemp("/tmp", "sy.")
	if err != nil {
		t.Fatal(err)
	}
	y := &yard{d: d, bin: filepath.Join(d, "bin"), home: filepath.Join(d, "home"),
		config: filepath.Join(d, "config"), yardhome: filepath.Join(d, "yard"),
		sock: filepath.Join(d, "herdr.sock"), state: filepath.Join(d, "state"),
		initLog: filepath.Join(d, "init-stdin.log")}
	shim := filepath.Join(d, "shim")
	y.path = shim + ":" + y.bin + ":/usr/bin:/bin"
	for _, dir := range []string{y.bin, shim, y.home, y.config, y.yardhome, filepath.Join(y.state, "herdr")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	goBuild := func(out, pkg string) {
		t.Helper()
		cmd := exec.Command("go", "build", "-o", out, pkg)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go build %s: %v\n%s", pkg, err, out)
		}
	}
	goBuild(filepath.Join(y.bin, "yardr"), "yardr.dev/yardr")
	goBuild(filepath.Join(y.bin, "herdr"), "yardr.dev/yardr/contrib/server-yard/fake")
	b, err := os.ReadFile(filepath.Join(root, "contrib/server-yard/fake", "claude"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(y.bin, "claude"), b, 0o755); err != nil {
		t.Fatal(err)
	}
	// A terminal on init's stdin makes it stop to ask whether to make a real
	// call to the harness, which the script must not wait for. The test has
	// no terminal, so init could not be seen to ask: the shim in front of
	// yardr says instead whether the stdin it was given has anything on it
	// (run gives the script a line, as a person at a keyboard would).
	const shimBody = `#!/bin/sh
if [ "$1" = init ]; then
  if IFS= read -r line; then echo "init: stdin has a line"; else echo "init: stdin is closed"; fi >>"$SY_INIT_LOG"
fi
exec "$SY_REAL_YARDR" "$@"
`
	if err := os.WriteFile(filepath.Join(shim, "yardr"), []byte(shimBody), 0o755); err != nil {
		t.Fatal(err)
	}
	managerEnv, err := managers.Install(root, y.bin, y.state)
	if err != nil {
		t.Fatal(err)
	}
	// The test's own environment stays out: a YARDR_HOME or a
	// HERDR_SOCKET_PATH inherited from it would reach the real yard.
	y.env = []string{
		"HOME=" + y.home,
		"XDG_CONFIG_HOME=" + y.config,
		"YARDR_HOME=" + y.yardhome,
		"PATH=" + y.path,
		"SY_REAL_YARDR=" + filepath.Join(y.bin, "yardr"),
		"SY_INIT_LOG=" + y.initLog,
		"HERDR_SOCKET_PATH=" + y.sock,
		"HERDR_FAKE_STATE=" + filepath.Join(y.state, "herdr"),
		"USER=sy-test",
		"SHELL=/bin/sh",
	}
	y.env = append(y.env, managerEnv...)
	// Of the test's environment only the name TestMain gave the binary comes
	// in: the server and the daemon the script starts, itself or through a
	// stand-in manager, inherit it and stop when the binary is gone.
	for _, name := range []string{config.TestOwnerEnv, config.TestHomeCheckEnv} {
		y.env = append(y.env, config.Env(name, config.Getenv(name))...)
	}
	t.Cleanup(y.stop)
	return y
}

// run runs the script with args and returns what it printed.
func (y *yard) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return y.runWith(t, y.env, args...)
}

// runWith is run in the environment env. Its stdin has a line on it, which
// nothing the script starts may read.
func (y *yard) runWith(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "contrib/server-yard/server-yard"), args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader("y\n")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// cli runs the yard's yardr with args.
func (y *yard) cli(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join(y.bin, "yardr"), args...)
	cmd.Env = y.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// running reports whether pid is a live process.
func running(pid int) bool { return pid > 0 && syscall.Kill(pid, 0) == nil }

// pidIn reads the pid a lock or pid file names, 0 when it names none.
func pidIn(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.Fields(string(b) + " 0")[0])
	return pid
}

// environOf is the environment pid runs with, as ps lists it after the
// command (isolated-yard finds the processes of a yard by it): NAME=value
// words on one line.
func environOf(t *testing.T, pid int) string {
	t.Helper()
	out, err := exec.Command("ps", "axeww", "-o", "pid=", "-o", "command=").Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), strconv.Itoa(pid)+" "); ok {
			return rest
		}
	}
	t.Fatalf("ps lists no process %d", pid)
	return ""
}

// gone waits up to 10s for pid to end.
func gone(pid int) bool {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !running(pid) {
			return true
		}
	}
	return false
}

// stop ends what the script started, by pid: the daemon, the server it
// started, and the fake herdr server the stand-in manager started. Then it
// removes D.
func (y *yard) stop() {
	daemon := pidIn(filepath.Join(y.yardhome, "daemon.lock"))
	server := pidIn(filepath.Join(y.yardhome, "server.pid"))
	herdr := pidIn(filepath.Join(y.state, "systemctl", "herdr.pid"))
	for _, pid := range []int{daemon, server, herdr} {
		if pid > 0 {
			syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	for _, pid := range []int{daemon, server, herdr} {
		if pid > 0 && !gone(pid) {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	os.RemoveAll(y.d)
}

// calls returns the lines of the stand-in's calls.log, "" when it logged none.
func (y *yard) calls(t *testing.T, who string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(y.state, who, "calls.log"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// marks is how many calls each stand-in has logged, to tell what a run adds.
func (y *yard) marks(t *testing.T) map[string]int {
	t.Helper()
	marks := map[string]int{}
	for _, who := range []string{"systemctl", "loginctl", "launchctl", "herdr"} {
		marks[who] = len(y.calls(t, who))
	}
	return marks
}

// told is what a run did to the service managers since marks, beyond asking:
// their calls that are not a look at their state.
func (y *yard) told(t *testing.T, marks map[string]int) []string {
	t.Helper()
	asks := map[string][]string{
		"systemctl": {"systemctl --user is-enabled ", "systemctl --user show-environment"},
		"loginctl":  {"loginctl show-user "},
		"launchctl": {"launchctl print "},
	}
	var told []string
	for _, who := range []string{"systemctl", "loginctl", "launchctl"} {
		for _, line := range y.calls(t, who)[marks[who]:] {
			if !slices.ContainsFunc(asks[who], func(p string) bool { return strings.HasPrefix(line, p) }) {
				told = append(told, line)
			}
		}
	}
	return told
}

// serviceFile is the daemon's or the web view's service file, as yardr
// wrote it on this system.
func (y *yard) serviceFile(t *testing.T, kind string) string {
	t.Helper()
	var pat string
	if runtime.GOOS == "darwin" {
		pat = filepath.Join(y.home, "Library", "LaunchAgents", "dev.yardr."+kind+".*.plist")
	} else {
		pat = filepath.Join(y.config, "systemd", "user", "yardr-"+kind+"-*.service")
	}
	m, err := filepath.Glob(pat)
	if err != nil || len(m) != 1 {
		t.Fatalf("%s service file: glob %s: %v, %q", kind, pat, err, m)
	}
	return m[0]
}

// waitDaemon waits for a daemon to hold the home's lock and returns its pid.
func (y *yard) waitDaemon(t *testing.T) int {
	t.Helper()
	lock := filepath.Join(y.yardhome, "daemon.lock")
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if pid := pidIn(lock); running(pid) {
			return pid
		}
	}
	out, _ := y.cli(t, "daemon", "status")
	t.Fatalf("no daemon on %s:\n%s", y.yardhome, out)
	return 0
}

func mustContain(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

// mustHaveEnvironment checks that the Environment lines of unit are want, in
// that order: the PATH, and the proxy and CA variables the script found.
func mustHaveEnvironment(t *testing.T, unit []byte, want ...string) {
	t.Helper()
	var got []string
	for _, line := range strings.Split(string(unit), "\n") {
		if strings.HasPrefix(line, "Environment=") {
			got = append(got, line)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("herdr.service holds these Environment lines:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// mustHaveMode checks the permission bits of path.
func mustHaveMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Errorf("%s is mode %04o, want %04o", path, got, want)
	}
}

// behindAProxy is the environment of a box behind an egress proxy:
// the nine variables yardr hands on, each with the line herdr's unit holds
// for it. Both are spelled out, since a list taken from the script would pass
// whatever the script says. The lower-case forms are there because curl, and
// git with it, read them and not the upper-case http_proxy. Three values are
// there for what they ask of the quoting: a percent-encoded credential, which
// systemd would read as a specifier, one that is set and empty, which goes on
// as it is, and one with a space, a quote and a backslash.
var behindAProxy = []struct{ env, line string }{
	{"HTTPS_PROXY=http://user:p%40ss@proxy.example:3128", `Environment="HTTPS_PROXY=http://user:p%%40ss@proxy.example:3128"`},
	{"https_proxy=http://user:p%40ss@proxy.example:3128", `Environment="https_proxy=http://user:p%%40ss@proxy.example:3128"`},
	{"HTTP_PROXY=http://proxy.example:3128", `Environment="HTTP_PROXY=http://proxy.example:3128"`},
	{"http_proxy=http://proxy.example:3128", `Environment="http_proxy=http://proxy.example:3128"`},
	{"NO_PROXY=localhost,.example", `Environment="NO_PROXY=localhost,.example"`},
	{"no_proxy=", `Environment="no_proxy="`},
	{"SSL_CERT_FILE=/etc/example/ca.pem", `Environment="SSL_CERT_FILE=/etc/example/ca.pem"`},
	{`SSL_CERT_DIR=/etc/example/ca "2026"\certs`, `Environment="SSL_CERT_DIR=/etc/example/ca \"2026\"\\certs"`},
	{"NODE_EXTRA_CA_CERTS=/etc/example/ca.pem", `Environment="NODE_EXTRA_CA_CERTS=/etc/example/ca.pem"`},
}

// notAmongTheNine are settings of the same kind that yardr is not asked to
// hand on, and the unit has no line for.
var notAmongTheNine = []string{"ALL_PROXY=socks5://proxy.example:1080", "CURL_CA_BUNDLE=/etc/example/ca.pem"}

// TestServerYardSetsUpAYard runs the script once: herdr's unit is written
// and its server runs, linger is on, the yard is created with its
// yardmaster up, the daemon and the web view are installed as login
// services, and doctor ends the run with no fault. The box is behind an
// egress proxy, so the unit carries the PATH and the nine variables of the
// script's environment, and a run in which one has changed writes it again.
func TestServerYardSetsUpAYard(t *testing.T) {
	y := build(t)
	var egress, egressLines []string
	for _, v := range behindAProxy {
		egress = append(egress, v.env)
		egressLines = append(egressLines, v.line)
	}
	proxied := withEnv(y.env, slices.Concat(egress, notAmongTheNine)...)
	out, err := y.runWith(t, proxied, "--harness", "claude", "--operator", "jo")
	if err != nil {
		t.Fatalf("server-yard: %v\n%s", err, out)
	}
	unit := filepath.Join(y.config, "systemd", "user", "herdr.service")
	body, err := os.ReadFile(unit)
	if err != nil {
		t.Fatalf("herdr.service: %v\n%s", err, out)
	}
	mustContain(t, string(body),
		`ExecStart="`+filepath.Join(y.bin, "herdr")+`" "server"`,
		`Environment="PATH=`+y.path+`"`,
		"Restart=on-failure", "WantedBy=default.target")
	// The PATH, then each of the nine in the script's order, and no line
	// for the settings that are not among them.
	wantLines := slices.Concat([]string{`Environment="PATH=` + y.path + `"`}, egressLines)
	mustHaveEnvironment(t, body, wantLines...)
	// A proxy's address can hold a password, so the unit is as closed as the
	// service files yardr writes.
	mustHaveMode(t, unit, 0o600)
	syscalls := strings.Join(y.calls(t, "systemctl"), "\n")
	if i, j := strings.Index(syscalls, "daemon-reload"), strings.Index(syscalls, "enable --now herdr"); i < 0 || j < 0 || i > j {
		t.Errorf("systemctl calls lack daemon-reload before enable --now herdr:\n%s", syscalls)
	}
	mustContain(t, strings.Join(y.calls(t, "loginctl"), "\n"), "enable-linger sy-test")
	mustContain(t, out, "herdr's server runs", "linger is on for sy-test")
	if strings.Contains(out, "already:") {
		t.Errorf("a first run finds nothing already done:\n%s", out)
	}

	// init was given no stdin, though the script had one: at a terminal it
	// would have stopped to ask about a real call to the harness.
	if got := strings.Split(strings.TrimSpace(string(mustRead(t, y.initLog))), "\n"); !slices.Equal(got, []string{"init: stdin is closed"}) {
		t.Errorf("yardr init saw %q; want its stdin closed", got)
	}

	// The yardmaster the daemon started is up, on the harness named.
	mustContain(t, out, "yardmaster is running")
	agents, _ := os.ReadFile(filepath.Join(y.state, "herdr", "agents.log"))
	mustContain(t, string(agents), "kind=claude")

	// The daemon install stopped the daemon init started, and the
	// stand-in manager started the service's own, which doctor sees.
	stopped := regexp.MustCompile(`\(pid ([0-9]+)\) is stopped`).FindStringSubmatch(out)
	if stopped == nil {
		t.Fatalf("daemon install stopped no daemon:\n%s", out)
	}
	pid, _ := strconv.Atoi(stopped[1])
	if running(pid) {
		t.Errorf("daemon %d outlived the install", pid)
	}
	daemon := y.waitDaemon(t)
	mustContain(t, string(mustRead(t, y.serviceFile(t, "daemon"))), filepath.Join(y.bin, "yardr"))
	mustRead(t, y.serviceFile(t, "web"))

	// Doctor ends the run, with no fault.
	mustContain(t, out, "The yard", "ok", "daemon", "running")
	if strings.Contains(out, "FAULT") {
		t.Errorf("doctor names a fault:\n%s", out)
	}
	doctor, err := y.cli(t, "doctor")
	if err != nil {
		t.Errorf("yardr doctor: %v\n%s", err, doctor)
	}
	mustContain(t, doctor, "is running (herdr agent")

	// The server init started is undisturbed.
	server := pidIn(filepath.Join(y.yardhome, "server.pid"))
	if !running(server) {
		t.Errorf("no server runs after the script")
	}

	// Both run with the name of this test binary, which is what stops them
	// when it is killed. The daemon is the stand-in manager's, so the name
	// came through the script and the stand-in, not only into the script.
	for _, p := range []struct {
		who string
		pid int
	}{{"server", server}, {"daemon", daemon}} {
		env := environOf(t, p.pid)
		for _, name := range []string{config.TestOwnerEnv, config.TestHomeCheckEnv} {
			val := config.Getenv(name)
			if val == "" {
				t.Fatalf("%s is not set in this binary: TestMain names it (storetest.NameTestBinary)", config.Key(name))
			}
			if !strings.Contains(env+" ", config.Key(name)+"="+val+" ") {
				t.Errorf("the %s (pid %d) does not run with %s=%s:\n%s", p.who, p.pid, config.Key(name), val, env)
			}
		}
	}

	// A second run in the same environment changes nothing: the files are
	// byte for byte what they were, the unit with its nine variables too, no
	// agent starts, the daemon and the server are the same processes, and the
	// managers are only asked, never told.
	before := map[string]string{}
	for _, f := range []string{unit, y.serviceFile(t, "daemon"), y.serviceFile(t, "web")} {
		before[f] = string(mustRead(t, f))
	}
	marks := y.marks(t)
	again, err := y.runWith(t, proxied, "--harness", "claude", "--operator", "jo")
	if err != nil {
		t.Fatalf("second run: %v\n%s", err, again)
	}
	for f, want := range before {
		if got := string(mustRead(t, f)); got != want {
			t.Errorf("second run rewrote %s", f)
		}
	}
	if got := pidIn(filepath.Join(y.yardhome, "daemon.lock")); got != daemon || !running(got) {
		t.Errorf("second run: daemon.pid is %d, want the running %d", got, daemon)
	}
	if got := pidIn(filepath.Join(y.yardhome, "server.pid")); got != server || !running(got) {
		t.Errorf("second run: server.pid is %d, want the running %d", got, server)
	}
	for _, line := range y.calls(t, "herdr")[marks["herdr"]:] {
		if strings.Contains(line, "agent start") {
			t.Errorf("second run started an agent: %q", line)
		}
	}
	if got := y.told(t, marks); len(got) > 0 {
		t.Errorf("second run told the managers %q", got)
	}
	mustContain(t, again, "already:", "already: "+unit+" is written")
	if got := strings.Count(string(mustRead(t, y.initLog)), "init: stdin is closed\n"); got != 2 {
		t.Errorf("after two runs the shim saw %d inits with a closed stdin, want 2", got)
	}

	// An earlier script could have written the same unit while it was 0644.
	// A same-environment run closes it without changing its contents or
	// telling systemd to reload it.
	if err := os.Chmod(unit, 0o644); err != nil {
		t.Fatal(err)
	}
	marks = y.marks(t)
	closed, err := y.runWith(t, proxied, "--harness", "claude", "--operator", "jo")
	if err != nil {
		t.Fatalf("same-environment run with an open unit: %v\n%s", err, closed)
	}
	mustContain(t, closed, "already: "+unit+" is written")
	if got := string(mustRead(t, unit)); got != before[unit] {
		t.Errorf("same-environment run changed the unit contents")
	}
	mustHaveMode(t, unit, 0o600)
	if got := y.told(t, marks); len(got) > 0 {
		t.Errorf("closing an unchanged unit told the managers %q", got)
	}

	// A changed variable is a changed PATH: the unit is not what the script
	// would write now, so it is written again and systemd is told, and nothing
	// else is. A variable the environment no longer has leaves the unit with
	// its line. The unit is open when this run finds it, as an earlier script
	// left it (0644), and is closed when it is written again.
	changed := withEnv(proxied, "NO_PROXY=localhost,.example,.example.com", "SSL_CERT_DIR")
	changedLines := slices.Clone(wantLines)
	changedLines[slices.Index(changedLines, `Environment="NO_PROXY=localhost,.example"`)] = `Environment="NO_PROXY=localhost,.example,.example.com"`
	changedLines = slices.DeleteFunc(changedLines, func(l string) bool { return strings.HasPrefix(l, `Environment="SSL_CERT_DIR=`) })
	if err := os.Chmod(unit, 0o644); err != nil {
		t.Fatal(err)
	}
	marks = y.marks(t)
	third, err := y.runWith(t, changed, "--harness", "claude", "--operator", "jo")
	if err != nil {
		t.Fatalf("third run: %v\n%s", err, third)
	}
	mustContain(t, third, "wrote "+unit)
	mustHaveEnvironment(t, mustRead(t, unit), changedLines...)
	mustHaveMode(t, unit, 0o600)
	if got := y.told(t, marks); !slices.Equal(got, []string{"systemctl --user daemon-reload"}) {
		t.Errorf("a changed variable told the managers %q, want a daemon-reload alone", got)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestServerYardWithoutTheHarnessLogin runs the script with a harness that
// refuses every prompt, as one without its login does: init --start cannot
// bring the yardmaster up, and the script goes on to the services and
// doctor all the same.
func TestServerYardWithoutTheHarnessLogin(t *testing.T) {
	y := build(t)
	if err := os.WriteFile(filepath.Join(y.state, "herdr", "logged-out"), []byte("the test logs the harness out\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := y.run(t, "--harness", "claude", "--operator", "jo", "--wait", "2s")
	if err != nil {
		t.Fatalf("server-yard: %v\n%s", err, out)
	}
	mustContain(t, out, "going on")
	mustContain(t, strings.Join(y.calls(t, "herdr"), "\n"), "prompt refused (logged-out)")
	// None of the nine variables is in this environment, so the unit holds
	// the PATH alone, as it did before it carried them, and is as closed as
	// when it carries them.
	unit := filepath.Join(y.config, "systemd", "user", "herdr.service")
	mustHaveEnvironment(t, mustRead(t, unit), `Environment="PATH=`+y.path+`"`)
	mustHaveMode(t, unit, 0o600)
	mustRead(t, y.serviceFile(t, "daemon"))
	mustRead(t, y.serviceFile(t, "web"))
	mustContain(t, out, "The yard")
	daemon := y.waitDaemon(t)
	if !running(daemon) {
		t.Errorf("no daemon runs after the script")
	}
}

// withEnv is env with the KEY=value pairs in set in place of what env has
// for those keys; a KEY alone takes that key out.
func withEnv(env []string, set ...string) []string {
	out := slices.Clone(env)
	for _, kv := range set {
		k, _, has := strings.Cut(kv, "=")
		out = slices.DeleteFunc(out, func(e string) bool { return strings.HasPrefix(e, k+"=") })
		if has {
			out = append(out, kv)
		}
	}
	return out
}

// TestServerYardStopsWithTheWayOn runs the script where a step cannot be
// taken, as three of them could not be on the Debian server of the README's
// run: the programs are in ~/.local/bin and the shell has no profile (ssh
// host cmd), the account has no systemd user session (sudo -u), and linger is
// refused (a server without polkit lets only root turn it on). Each stops
// the script with exit status 1 and the way on, the first two before it
// writes anything and the last before it creates the yard.
func TestServerYardStopsWithTheWayOn(t *testing.T) {
	y := build(t)
	unit := filepath.Join(y.config, "systemd", "user", "herdr.service")
	local := filepath.Join(y.home, ".local", "bin")
	touch := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("the test sets this\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	noSession := filepath.Join(y.state, "systemctl", "no-session")
	cases := []struct {
		name  string
		setup func(t *testing.T) []string // the environment the script runs in
		say   []string
		wrote bool // herdr's unit is written before the script stops
	}{
		{
			name: "programs in ~/.local/bin, a shell without a profile",
			setup: func(t *testing.T) []string {
				if err := os.MkdirAll(local, 0o755); err != nil {
					t.Fatal(err)
				}
				for _, p := range []string{"yardr", "herdr", "claude"} {
					if err := os.Symlink(filepath.Join(y.bin, p), filepath.Join(local, p)); err != nil {
						t.Fatal(err)
					}
				}
				return withEnv(y.env, "PATH=/usr/bin:/bin")
			},
			say: []string{"these are not on PATH:",
				"in " + local + ", which this shell's PATH lacks: yardr herdr claude",
				"run this from a login shell"},
		},
		{
			name: "no systemd user session",
			setup: func(t *testing.T) []string {
				touch(t, noSession)
				return y.env
			},
			say: []string{"reaches no systemd user session of sy-test", "sudo -iu sy-test", "loginctl enable-linger sy-test"},
		},
		{
			name: "linger refused",
			setup: func(t *testing.T) []string {
				if err := os.Remove(noSession); err != nil {
					t.Fatal(err)
				}
				touch(t, filepath.Join(y.state, "loginctl", "denied"))
				return y.env
			},
			say:   []string{"could not turn linger on for sy-test: as root, loginctl enable-linger sy-test, then run this again"},
			wrote: true,
		},
	}
	for _, c := range cases {
		env := c.setup(t)
		out, err := y.runWith(t, env, "--harness", "claude", "--operator", "jo")
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Errorf("%s: the script ended %v; want exit status 1\n%s", c.name, err, out)
		}
		mustContain(t, out, c.say...)
		if _, err := os.Stat(unit); (err == nil) != c.wrote {
			t.Errorf("%s: herdr.service written is %v, want %v\n%s", c.name, err == nil, c.wrote, out)
		}
		if _, err := os.Stat(filepath.Join(y.yardhome, "daemon.lock")); err == nil {
			t.Errorf("%s: a daemon started, so the yard was created\n%s", c.name, out)
		}
	}
}
