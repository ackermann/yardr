// Package serveryard tests the server-yard script: that one run sets a
// yard up as the server steps of docs/install.md say, and that a second
// run changes nothing. Each test builds yardr from the tree and runs the
// script with stand-ins for herdr (fake/, a socket and a CLI that answer
// what a setup asks), the harness, systemctl, loginctl and, on macOS,
// launchctl, with HOME, XDG_CONFIG_HOME and YARDR_HOME in a temp dir. The
// systemctl and launchctl stand-ins model the manager starting a unit:
// herdr's server and the daemon really start; the web view's unit is only
// marked, since starting it would bind 127.0.0.1:8791.
package serveryard

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/contrib/server-yard/managers"
)

// yard holds one test's directories: D is short (/tmp/sy.XXXXXX), since a
// unix socket path has a length limit.
type yard struct {
	d        string
	bin      string
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
		sock: filepath.Join(d, "herdr.sock"), state: filepath.Join(d, "state")}
	for _, dir := range []string{y.bin, y.home, y.config, y.yardhome, filepath.Join(y.state, "herdr")} {
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
		"PATH=" + y.bin + ":/usr/bin:/bin",
		"HERDR_SOCKET_PATH=" + y.sock,
		"HERDR_FAKE_STATE=" + filepath.Join(y.state, "herdr"),
		"USER=sy-test",
		"SHELL=/bin/sh",
	}
	y.env = append(y.env, managerEnv...)
	t.Cleanup(y.stop)
	return y
}

// run runs the script with args and returns what it printed.
func (y *yard) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "contrib/server-yard/server-yard"), args...)
	cmd.Env = y.env
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

// TestServerYardSetsUpAYard runs the script once: herdr's unit is written
// and its server runs, linger is on, the yard is created with its
// yardmaster up, the daemon and the web view are installed as login
// services, and doctor ends the run with no fault.
func TestServerYardSetsUpAYard(t *testing.T) {
	y := build(t)
	out, err := y.run(t, "--harness", "claude", "--operator", "jo")
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
		`Environment="PATH=`+y.bin+`:/usr/bin:/bin"`,
		"Restart=on-failure", "WantedBy=default.target")
	syscalls := strings.Join(y.calls(t, "systemctl"), "\n")
	if i, j := strings.Index(syscalls, "daemon-reload"), strings.Index(syscalls, "enable --now herdr"); i < 0 || j < 0 || i > j {
		t.Errorf("systemctl calls lack daemon-reload before enable --now herdr:\n%s", syscalls)
	}
	mustContain(t, strings.Join(y.calls(t, "loginctl"), "\n"), "enable-linger sy-test")
	mustContain(t, out, "herdr's server runs", "linger is on for sy-test")
	if strings.Contains(out, "already:") {
		t.Errorf("a first run finds nothing already done:\n%s", out)
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

	// A second run changes nothing: the files are byte for byte what
	// they were, no agent starts, the daemon and the server are the same
	// processes, and the managers are only asked, never told.
	before := map[string]string{}
	for _, f := range []string{unit, y.serviceFile(t, "daemon"), y.serviceFile(t, "web")} {
		before[f] = string(mustRead(t, f))
	}
	marks := map[string]int{}
	for _, who := range []string{"systemctl", "loginctl", "launchctl", "herdr"} {
		marks[who] = len(y.calls(t, who))
	}
	again, err := y.run(t, "--harness", "claude", "--operator", "jo")
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
	probes := map[string][]string{
		"systemctl": {"systemctl --user is-enabled "},
		"loginctl":  {"loginctl show-user "},
		"launchctl": {"launchctl print "},
	}
	for who, allowed := range probes {
		for _, line := range y.calls(t, who)[marks[who]:] {
			ok := false
			for _, p := range allowed {
				ok = ok || strings.HasPrefix(line, p)
			}
			if !ok {
				t.Errorf("second run told %s: %q", who, line)
			}
		}
	}
	mustContain(t, again, "already:")
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
	if _, err := os.Stat(filepath.Join(y.config, "systemd", "user", "herdr.service")); err != nil {
		t.Errorf("herdr.service: %v", err)
	}
	mustRead(t, y.serviceFile(t, "daemon"))
	mustRead(t, y.serviceFile(t, "web"))
	mustContain(t, out, "The yard")
	daemon := y.waitDaemon(t)
	if !running(daemon) {
		t.Errorf("no daemon runs after the script")
	}
}
