// Package serveryard tests the server-yard script: that one run sets a
// yard up as the server steps of docs/install.md say, that herdr's unit
// carries the proxy and CA variables of the script's environment, that a
// second run changes nothing, and that where a step cannot be taken the
// script stops with the way on. Each test builds yardr from the tree and
// runs the script with stand-ins for herdr (fake/, a socket and a CLI that
// answer what a setup asks), the harness, systemctl, loginctl and, on
// macOS, launchctl, with HOME, XDG_CONFIG_HOME and YARDR_HOME in a temp
// dir. The systemctl and launchctl stand-ins model the manager starting a
// unit: herdr's server and the daemon really start (the daemon late, or
// not at all, where a test says so); the web view's unit is only marked,
// since starting it would bind 127.0.0.1:8791.
package serveryard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
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
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/testload"
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
	initArgs string // what the shim saw of each yardr init's words
	home     string
	config   string
	yardhome string
	sock     string
	state    string
	env      []string
}

// goBuild builds the package pkg of the repository at root into out.
func goBuild(t *testing.T, root, out, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, out)
	}
}

// warm runs path once with args in env, where no fuse times it, and ignores
// the result: the first run of a file written a moment ago waits behind the
// machine's other first runs on macOS (yardr-c3nm).
func warm(path string, env []string, args ...string) {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	_ = cmd.Run()
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
		initLog: filepath.Join(d, "init-stdin.log"), initArgs: filepath.Join(d, "init-args.log")}
	shim := filepath.Join(d, "shim")
	y.path = shim + ":" + y.bin + ":/usr/bin:/bin"
	for _, dir := range []string{y.bin, shim, y.home, y.config, y.yardhome, filepath.Join(y.state, "herdr")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	goBuild(t, root, filepath.Join(y.bin, "yardr"), "yardr.dev/yardr")
	goBuild(t, root, filepath.Join(y.bin, "herdr"), "yardr.dev/yardr/contrib/server-yard/fake")
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
  echo "$@" >>"$SY_INIT_ARGS"
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
	// Everything above is a file the script runs under its waits: run each
	// once here, where no fuse times it (the managers ran in Install). The
	// stand-ins get only what their run needs: herdr with no state answers
	// nothing and logs nothing, and claude with no argument prints its
	// version.
	real := filepath.Join(y.bin, "yardr")
	warm(real, []string{"HOME=" + y.home}, "version")
	warm(filepath.Join(y.bin, "herdr"), []string{})
	warm(filepath.Join(y.bin, "claude"), []string{"HERDR_FAKE_STATE=" + filepath.Join(y.state, "herdr")})
	warm(filepath.Join(shim, "yardr"), []string{"HOME=" + y.home, "SY_REAL_YARDR=" + real}, "version")
	// The test's own environment stays out: a YARDR_HOME or a
	// HERDR_SOCKET_PATH inherited from it would reach the real yard.
	y.env = []string{
		"HOME=" + y.home,
		"XDG_CONFIG_HOME=" + y.config,
		"YARDR_HOME=" + y.yardhome,
		"PATH=" + y.path,
		"SY_REAL_YARDR=" + filepath.Join(y.bin, "yardr"),
		"SY_INIT_LOG=" + y.initLog,
		"SY_INIT_ARGS=" + y.initArgs,
		"HERDR_SOCKET_PATH=" + y.sock,
		"HERDR_FAKE_STATE=" + filepath.Join(y.state, "herdr"),
		"USER=sy-test",
		"SHELL=/bin/sh",
	}
	y.env = append(y.env, managerEnv...)
	// Of the test's environment only the name TestMain gave the binary comes
	// in: the server and the daemon the script starts, itself or through a
	// stand-in manager, inherit it and stop when the binary is gone. The wait
	// for a server to start comes in too, for the yardr the script runs
	// (init --start): it is the starter's, and the server does not get it.
	for _, name := range []string{config.TestOwnerEnv, config.TestHomeCheckEnv, config.TestStartWaitEnv} {
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
	return y.runSaying(t, env, "", nil, args...)
}

// runSaying is runWith, and when the script prints a line holding say it
// calls then, once: a test that must act on what the script is doing, not on
// how long it has taken, as when the daemon the script waits for is held
// back until the script says it is waiting.
func (y *yard) runSaying(t *testing.T, env []string, say string, then func(), args ...string) (string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "contrib/server-yard/server-yard"), args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader("y\n")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cmd.Stdout, cmd.Stderr = w, w // one pipe, as CombinedOutput has
	err = cmd.Start()
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for br := bufio.NewReader(r); ; {
		line, err := br.ReadString('\n')
		out.WriteString(line)
		if then != nil && strings.Contains(line, say) {
			then()
			then = nil
		}
		if err != nil {
			break
		}
	}
	err = cmd.Wait()
	if err != nil {
		if at := stoppedAt(out.String()); at != "" {
			err = fmt.Errorf("%w\n%s", err, at)
		}
	}
	return out.String(), err
}

// stoppedAt says where a run of the script that failed had got to, in lines
// to lead its failure with. The gate's flaky log keeps the first six lines of
// a failed test, and the step that failed is a hundred lines down, under the
// output of the steps that went well: a strike of this suite was filed as
// ending at the linger line, which is only where the log stopped keeping lines.
// The script ends with doctor, so its exit status is doctor's, and a FAULT
// line of the report it prints after "what is still missing" is the step (init
// prints fault lines of its own when the yardmaster does not come up, which
// are not). Without one the script stopped itself, and its last lines say why.
// "" for a run that printed nothing.
func stoppedAt(out string) string {
	var last []string
	doctor := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		doctor = doctor || strings.HasPrefix(line, "server-yard: what is still missing")
		if doctor && strings.HasPrefix(line, "FAULT ") {
			return "doctor, the script's last step, named a fault:\n" + line
		}
		last = append(last, line)
	}
	if len(last) == 0 {
		return ""
	}
	return "the script's last lines:\n" + strings.Join(last[max(len(last)-3, 0):], "\n")
}

// cli runs the yard's yardr with args.
func (y *yard) cli(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join(y.bin, "yardr"), args...)
	cmd.Env = y.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// running reports whether pid is a live process. A zombie is not running:
// kill -0 still answers for one, so where it says alive the state is read
// with ps and a zombie counts as dead. ps listing nothing means the process
// died between the checks.
func running(pid int) bool {
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	return !zombie(strings.TrimSpace(string(out)))
}

// zombie reports whether a ps STAT word names a process that is not
// running: a zombie, or nothing at all where ps lists no such process.
func zombie(stat string) bool { return stat == "" || strings.Contains(stat, "Z") }

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

// gone waits up to 10s, on an idle box, for pid to end.
func gone(pid int) bool {
	for deadline := time.Now().Add(testload.Scale(10 * time.Second)); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !running(pid) {
			return true
		}
	}
	return false
}

// TestZombie pins what running counts as dead: a zombie's STAT word, in
// either ps's spelling, and nothing listed at all. A real zombie is not
// kept instead: Go reaps adopted children on GC timing.
func TestZombie(t *testing.T) {
	for _, c := range []struct {
		stat string
		want bool
	}{
		{"Z", true},
		{"Z+", true},
		{"Ss", false},
		{"R", false},
		{"", true}, // ps lists nothing: dead between the checks
	} {
		if got := zombie(c.stat); got != c.want {
			t.Errorf("zombie(%q) = %v, want %v", c.stat, got, c.want)
		}
	}
}

// TestStoppedAt pins what leads the failure of a run of the script: doctor's
// first fault when doctor ended the run, else the script's last lines. The
// first output is the shape of a strike of TestServerYardSetsUpAYard, where
// the harness stand-in did not answer doctor's version probe in time.
func TestStoppedAt(t *testing.T) {
	const fault = "FAULT  claude      /tmp/sy.1/bin/claude, the harness of the yard's default, crew member yardmaster: on PATH, but it did not answer its version flag within 20s"
	for _, c := range []struct{ name, out, want string }{
		{"a fault of doctor, the script's last step", strings.Join([]string{
			"server-yard: linger is on for sy-test",
			"yard created in /tmp/sy.1/yard",
			"server-yard: waiting for the daemon to come up (up to 60 s)",
			"server-yard: what is still missing:",
			"Needed on this machine",
			"  ok     herdr       /tmp/sy.1/bin/herdr, its server is running",
			"  " + fault,
			"  FAULT  yardmaster  yardmaster is not running",
			"1 fault: something the yard needs is missing or not working (FAULT above).",
			"Next step: a depot.",
		}, "\n"), "doctor, the script's last step, named a fault:\n" + fault},
		{"the script stopped itself", strings.Join([]string{
			"server-yard: waiting for the daemon to come up (up to 2 s)",
			"server-yard: the daemon did not come up in 2 s",
			"server-yard: daemon   not running",
			"server-yard: see <home>/daemon.log and journalctl --user -u 'yardr-daemon-*'",
			"",
		}, "\n"), "the script's last lines:\n" +
			"server-yard: the daemon did not come up in 2 s\n" +
			"server-yard: daemon   not running\n" +
			"server-yard: see <home>/daemon.log and journalctl --user -u 'yardr-daemon-*'"},
		{"a fault that init printed is not doctor's", strings.Join([]string{
			"  FAULT  yardmaster  yardmaster is not running: the harness is not logged in",
			"server-yard: init --start did not see the yardmaster come up; going on",
			"server-yard: waiting for the daemon to come up (up to 60 s)",
			"server-yard: the daemon did not come up in 60 s",
			"server-yard: see <home>/daemon.log",
		}, "\n"), "the script's last lines:\n" +
			"server-yard: waiting for the daemon to come up (up to 60 s)\n" +
			"server-yard: the daemon did not come up in 60 s\n" +
			"server-yard: see <home>/daemon.log"},
		{"fewer lines than it would show", "usage: server-yard --harness <kind>\n", "the script's last lines:\nusage: server-yard --harness <kind>"},
		{"nothing printed", "\n", ""},
	} {
		if got := stoppedAt(c.out); got != c.want {
			t.Errorf("%s: stoppedAt = %q, want %q", c.name, got, c.want)
		}
	}
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

// holdDaemon has the stand-in managers hold the daemon back once they have
// loaded its unit, as a manager does that returns before its process runs:
// the daemon starts when releaseDaemon is called, and never if it is not.
// Which manager loads the unit depends on the system the test runs on
// (systemctl on Linux, launchctl on macOS, where yardr writes launchd
// agents), so both are told and the one that is called reads it.
func (y *yard) holdDaemon(t *testing.T) {
	t.Helper()
	for _, who := range []string{"systemctl", "launchctl"} {
		if err := os.WriteFile(filepath.Join(y.state, who, "start-hold"), []byte("the test holds the daemon back\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// releaseDaemon lets a daemon held back by holdDaemon start. It renames the
// hold to start-released, which is what the stand-ins start the daemon for:
// a hold that only goes, as it does when the yard's directory is removed at
// the end of a test, starts none.
func (y *yard) releaseDaemon(t *testing.T) {
	t.Helper()
	for _, who := range []string{"systemctl", "launchctl"} {
		dir := filepath.Join(y.state, who)
		if err := os.Rename(filepath.Join(dir, "start-hold"), filepath.Join(dir, "start-released")); err != nil {
			t.Fatal(err)
		}
	}
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
	for deadline := time.Now().Add(testload.Scale(15 * time.Second)); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
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
// services, and doctor ends the run with no fault, though the manager
// starts the daemon only after its install has returned. The box is behind
// an egress proxy, so the unit carries the PATH and the nine variables of
// the script's environment, and a run in which one has changed writes it
// again.
func TestServerYardSetsUpAYard(t *testing.T) {
	y := build(t)
	var egress, egressLines []string
	for _, v := range behindAProxy {
		egress = append(egress, v.env)
		egressLines = append(egressLines, v.line)
	}
	proxied := withEnv(y.env, slices.Concat(egress, notAmongTheNine)...)
	// A manager returns from loading a unit before the process runs. The
	// daemon the service starts is not up until the script says it is
	// waiting for it, so that a script that asked doctor at once would find
	// none, however fast or slow the machine is.
	y.holdDaemon(t)
	out, err := y.runSaying(t, proxied, "waiting for the daemon to come up", func() { y.releaseDaemon(t) },
		"--harness", "claude", "--operator", "jo")
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
	// stand-in manager started the service's own, late: the script waited
	// for it, so doctor sees it.
	mustContain(t, out, "waiting for the daemon to come up")
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

// TestServerYardPermissions runs the script with --permissions: the label
// goes to yardr init as it stands, and the yard is made with that preset
// (its setting, and the yardmaster's flags). Without the flag init is
// called exactly as before, which the same yard's second run shows: no
// --permissions in its words, and the yard init made stands.
func TestServerYardPermissions(t *testing.T) {
	y := build(t)
	out, err := y.run(t, "--harness", "claude", "--operator", "jo", "--permissions", "bypass")
	if err != nil {
		t.Fatalf("server-yard: %v\n%s", err, out)
	}
	mustContain(t, out, "yardmaster is running")
	if got := string(mustRead(t, y.initArgs)); !strings.Contains(got, "--permissions bypass") {
		t.Errorf("yardr init was given %q, want --permissions bypass in its words", got)
	}
	setting, err := y.cli(t, "settings", "get", "permissions_preset")
	if err != nil || strings.TrimSpace(setting) != "bypass" {
		t.Errorf("permissions_preset = %q, %v; want bypass", setting, err)
	}
	show, err := y.cli(t, "crew", "show", "yardmaster")
	if err != nil {
		t.Fatalf("crew show: %v\n%s", err, show)
	}
	mustContain(t, show, "--dangerously-skip-permissions")

	// Without the flag: the same call as before the switch existed, and
	// the yard stands as the first run made it.
	again, err := y.run(t, "--harness", "claude", "--operator", "jo")
	if err != nil {
		t.Fatalf("second run: %v\n%s", err, again)
	}
	lines := strings.Split(strings.TrimSpace(string(mustRead(t, y.initArgs))), "\n")
	if len(lines) != 2 || strings.Contains(lines[1], "--permissions") {
		t.Errorf("inits were given %q, want the second without --permissions", lines)
	}
	if setting, err := y.cli(t, "settings", "get", "permissions_preset"); err != nil ||
		strings.TrimSpace(setting) != "bypass" {
		t.Errorf("permissions_preset after a run without the flag = %q, %v", setting, err)
	}

	// An empty label is the usage, before anything is written.
	usage, err := y.run(t, "--harness", "claude", "--operator", "jo", "--permissions=")
	if err == nil || !strings.Contains(usage, "usage: server-yard") || !strings.Contains(usage, "--permissions <label>") {
		t.Errorf("--permissions= : %v\n%s", err, usage)
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
// refused (a server without polkit lets only root turn it on); and where a
// step is taken and does not hold, a daemon that its service manager loads
// and never starts. Each stops the script with exit status 1 and the way on
// and before doctor, the first two before it writes anything, the third
// before it creates the yard and the last after.
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
	// The daemon wait's bound is checked before the script looks at the
	// machine or writes anything. Exercise bad values in this same stop-path
	// test; none should get as far as a service manager.
	for _, bound := range []string{"soon", "5m", "-1", "1.5", "999999999999999999999999999999"} {
		env := withEnv(y.env, "SERVER_YARD_DAEMON_WAIT="+bound)
		out, err := y.runWith(t, env, "--harness", "claude", "--operator", "jo")
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Errorf("bound %q: the script ended %v; want exit status 2\n%s", bound, err, out)
		}
		mustContain(t, out, "SERVER_YARD_DAEMON_WAIT must be a whole number of seconds from 0 to 999999999")
		if len(y.calls(t, "systemctl")) != 0 || len(y.calls(t, "loginctl")) != 0 {
			t.Errorf("bound %q: the script reached a service manager\n%s", bound, out)
		}
	}
	noSession := filepath.Join(y.state, "systemctl", "no-session")
	denied := filepath.Join(y.state, "loginctl", "denied")
	cases := []struct {
		name  string
		setup func(t *testing.T) []string // the environment the script runs in
		say   []string
		wrote bool // herdr's unit is written before the script stops
		yard  bool // the yard is created, and a daemon started, before it stops
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
				touch(t, denied)
				return y.env
			},
			say:   []string{"could not turn linger on for sy-test: as root, loginctl enable-linger sy-test, then run this again"},
			wrote: true,
		},
		{
			// The service loads and the daemon never runs, as for a unit whose
			// process fails at once: the script says so after the bound, which
			// two seconds shortens from a minute, and does not go on to doctor.
			name: "a daemon the manager never starts",
			setup: func(t *testing.T) []string {
				if err := os.Remove(denied); err != nil {
					t.Fatal(err)
				}
				y.holdDaemon(t)
				return withEnv(y.env, "SERVER_YARD_DAEMON_WAIT=2")
			},
			say:   []string{"waiting for the daemon to come up (up to 2 s)", "the daemon did not come up in 2 s", "daemon   not running", "see <home>/daemon.log"},
			wrote: true,
			yard:  true,
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
		if err != nil {
			// The failure leads with the script's last lines, which hold the way on.
			mustContain(t, err.Error(), "the script's last lines:", c.say[len(c.say)-1])
		}
		if strings.Contains(out, "what is still missing") {
			t.Errorf("%s: the script went on to doctor\n%s", c.name, out)
		}
		if _, err := os.Stat(unit); (err == nil) != c.wrote {
			t.Errorf("%s: herdr.service written is %v, want %v\n%s", c.name, err == nil, c.wrote, out)
		}
		if _, err := os.Stat(filepath.Join(y.yardhome, "daemon.lock")); (err == nil) != c.yard {
			t.Errorf("%s: a daemon started is %v, want %v\n%s", c.name, err == nil, c.yard, out)
		}
	}
}

// owner starts a process to stand for the test binary that a server is named
// to, which no test can kill and survive. It returns the name as
// NameTestBinary gives one (YARDR_TEST_OWNER's value: the pid and the start
// time) and a function that ends the process.
func owner(t *testing.T) (named string, end func()) {
	t.Helper()
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	var start uint64
	if st, err := peer.Describe(sleep.Process.Pid); err == nil {
		start = st.Start
	}
	end = func() {
		sleep.Process.Kill()
		sleep.Wait()
	}
	t.Cleanup(end)
	return fmt.Sprintf("%d %d", sleep.Process.Pid, start), end
}

// yardr-q4bk: the herdr stand-in is started by a stand-in service manager,
// and only y.stop ends it, a Cleanup that a killed test binary never runs. So
// it stops by itself with the owner it is named to, as the yard's server and
// daemon do: it serves while the owner runs, and ends with status 0 once the
// owner has ended. (That it keeps serving for the length of a test whose
// owner lives is what TestServerYardSetsUpAYard needs of it throughout.)
func TestFakeHerdrStopsWithItsOwner(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// Short, since a unix socket path has a length limit.
	dir, err := os.MkdirTemp("/tmp", "sy.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	herdr := filepath.Join(dir, "herdr")
	goBuild(t, root, herdr, "yardr.dev/yardr/contrib/server-yard/fake")
	// Run it once before the serve the fuse below times: with no state it
	// answers nothing and logs nothing.
	warm(herdr, []string{})
	named, end := owner(t)
	sock := filepath.Join(dir, "herdr.sock")
	cmd := exec.Command(herdr, "server")
	cmd.Env = append([]string{"HERDR_FAKE_STATE=" + dir, "HERDR_SOCKET_PATH=" + sock}, config.Env(config.TestOwnerEnv, named)...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() }) // after the wait below has reaped it, a no-op
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// Up: it answers on its socket. The minute is a fuse: the first run of a
	// program built a moment ago queues behind the machine's other first runs.
	for deadline := time.Now().Add(testload.Scale(time.Minute)); ; time.Sleep(50 * time.Millisecond) {
		c, err := net.DialTimeout("unix", sock, time.Second)
		if err == nil {
			c.Close()
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("the stand-in ended before it served: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the stand-in did not serve on %s: %v", sock, err)
		}
	}

	end()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("the stand-in ended with %v after its owner ended, want status 0", err)
		}
	case <-time.After(testload.Scale(5 * time.Second)):
		t.Fatal("the stand-in kept running after its owner ended")
	}
}

// waitFor polls cond until it holds. The minute is a fuse against a stand-in
// that never gets there, not the measurement: the first run of a script
// written a moment ago queues behind the machine's other first runs. It is
// written for an idle box and sized to the machine (testload.Scale).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(testload.Scale(time.Minute)); !cond(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// A stand-in manager that holds the daemon back starts it for a release and
// for nothing else. The end of a test removes the yard's directory, which
// unlinks start-hold a moment before the state dir it lies in; a daemon
// started in that moment runs in a home that is being removed, and leaves
// the home behind. So the hold going is not the release: the test renames it
// to start-released (releaseDaemon), and a hold that only goes starts
// nothing. The stand-ins run by themselves here, with a yardr that records
// that it was started, since a yard's teardown cannot be made to hit that
// moment on demand.
func TestAHeldDaemonStartsOnlyForARelease(t *testing.T) {
	t.Parallel()
	fakes, err := filepath.Abs("fake")
	if err != nil {
		t.Fatal(err)
	}
	kinds := []struct {
		name, stateVar string
		load           []string // the call that loads the daemon's unit
	}{
		{"systemctl", "SYSTEMCTL_STATE", []string{"--user", "enable", "--now", "yardr-daemon-x.service"}},
		{"launchctl", "LAUNCHCTL_STATE", []string{"bootstrap", "gui/501", "/x/dev.yardr.daemon.x.plist"}},
	}
	cases := []struct {
		name   string
		held   bool
		lift   func(state string) error // what the test does to the hold, once the stand-in waits on it
		starts bool
	}{
		{"no hold", false, nil, true},
		{"released", true, func(state string) error {
			return os.Rename(filepath.Join(state, "start-hold"), filepath.Join(state, "start-released"))
		}, true},
		{"only gone", true, func(state string) error { return os.Remove(filepath.Join(state, "start-hold")) }, false},
	}
	for _, k := range kinds {
		for _, c := range cases {
			t.Run(k.name+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				bin, state, started := filepath.Join(dir, "bin"), filepath.Join(dir, "state"), filepath.Join(dir, "started")
				for _, d := range []string{bin, state} {
					if err := os.MkdirAll(d, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(bin, "yardr"), []byte("#!/bin/sh\necho \"$*\" >>\"$STARTED\"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				if c.held {
					if err := os.WriteFile(filepath.Join(state, "start-hold"), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				// The call returns at once and leaves the wait running; a
				// stand-in that kept the call's pipes would hold it for the
				// length of the wait. The wait, and the daemon it starts in
				// its place, hold the pipe given to the call as file 3 until
				// they end, so end-of-file on it is the moment when whether
				// the daemon started can be said: a stand-in that said it
				// would not, and then did, is caught.
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				defer r.Close()
				ctx, cancel := context.WithTimeout(context.Background(), testload.Scale(time.Minute))
				defer cancel()
				cmd := exec.CommandContext(ctx, filepath.Join(fakes, k.name), k.load...)
				cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", k.stateVar + "=" + state, "STARTED=" + started}
				cmd.ExtraFiles = []*os.File{w}
				out, err := cmd.CombinedOutput()
				w.Close()
				if err != nil {
					t.Fatalf("%s %v: %v\n%s", k.name, k.load, err, out)
				}
				// The hold is lifted once the stand-in waits on it: lifted
				// before its first look, it would be no hold at all.
				if c.held {
					waitFor(t, "the stand-in to hold the daemon back", func() bool {
						b, _ := os.ReadFile(filepath.Join(state, "daemon.log"))
						return strings.Contains(string(b), "held back")
					})
					if err := c.lift(state); err != nil {
						t.Fatal(err)
					}
				}
				if err := r.SetReadDeadline(time.Now().Add(testload.Scale(time.Minute))); err != nil {
					t.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, r); err != nil {
					t.Fatalf("the stand-in left a process running: %v", err)
				}
				if _, err := os.Stat(started); (err == nil) != c.starts {
					t.Errorf("the daemon started is %v, want %v", err == nil, c.starts)
				}
				if !c.starts {
					log, _ := os.ReadFile(filepath.Join(state, "daemon.log"))
					mustContain(t, string(log), "is not started")
				}
			})
		}
	}
}
