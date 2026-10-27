// Package isolatedyard tests the isolated-yard script: that `down` stops
// what runs in a yard and nothing else. The tests use bare yards (no herdr
// server), so they run where herdr is not installed.
//
// The exception is TestYardShapes (matrix_test.go), the yard-shape matrix.
// It brings up yards with a herdr server and a stand-in agent of their own
// (isolated-yard up), so it needs herdr, and it takes minutes. It therefore
// skips unless YARDR_QUIET_RUN is set, which only the daemon's quiet run
// sets (docs/testing.md R6): `go test ./...`, a builder's gate and a
// landing do not run it. By hand:
//
//	YARDR_QUIET_RUN=1 go test -run TestYardShapes ./contrib/isolated-yard
package isolatedyard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/testload"
)

// script runs isolated-yard from the repository's root, where `bare`
// builds yardr from.
func script(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return scriptEnv(t, nil, args...)
}

// scriptEnv is script in the environment env, this binary's own when nil.
func scriptEnv(t *testing.T, env []string, args ...string) (string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "contrib/isolated-yard/isolated-yard"), args...)
	cmd.Dir = root
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// bare makes a bare yard and returns its directory. Should the test fail
// before its own `down`, the yard is taken down when the test ends.
func bare(t *testing.T) string {
	t.Helper()
	// A test with a bare yard shares nothing with another's (a directory of
	// its own under /tmp, matched by that directory), so it runs beside
	// them from here on.
	t.Parallel()
	return bareEnv(t, nil)
}

// bareEnv is bare for a yard made in the environment env (this binary's own
// when nil). It does not call t.Parallel: a test that makes a second yard
// has called it already, with bare, and may not again.
func bareEnv(t *testing.T, env []string) string {
	t.Helper()
	out, err := scriptEnv(t, env, "bare")
	if err != nil {
		t.Fatalf("isolated-yard bare: %v\n%s", err, out)
	}
	lines := strings.Split(out, "\n")
	d := lines[len(lines)-1]
	if !strings.HasPrefix(d, "/tmp/iy.") {
		t.Fatalf("isolated-yard bare printed %q", out)
	}
	downAtEnd(t, d)
	return d
}

// up makes a yard with a herdr server of its own (isolated-yard up), the
// kind that may start agents, and returns its directory. A test that starts
// agents must not go on in a yard that is not isolated, so it checks what
// up printed: a pane of that herdr finds the stand-in as `claude`, and the
// agent list is empty. Unlike bare it leaves t.Parallel to its caller.
func up(t *testing.T) string {
	t.Helper()
	out, err := script(t, "up")
	if err != nil {
		t.Fatalf("isolated-yard up: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(?m)^isolated yard: (/tmp/iy\.\S+)\s`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("isolated-yard up named no yard:\n%s", out)
	}
	d := m[1]
	downAtEnd(t, d)
	for _, proof := range []string{"command -v claude, in a pane: " + d + "/bin/claude", `"agents":[]`} {
		if !strings.Contains(out, proof) {
			t.Fatalf("isolated-yard up did not show the yard isolated: no %q in\n%s", proof, out)
		}
	}
	return d
}

// downAtEnd takes the yard in d down when the test ends, should the test
// fail before its own `down`. It goes by the script, which goes by the
// yard's directory: a pid the test read earlier may be someone else's by
// then. The yard's watchdog may remove it between the look and the script's
// start, which the script answers with "no yard at" (status 2): the yard is
// gone, as the cleanup wanted, so that answer and no other is let pass.
func downAtEnd(t *testing.T, d string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := os.Stat(d); err == nil {
			if out, err := script(t, "down", d); err != nil && !strings.Contains(out, "isolated-yard: no yard at "+d) {
				t.Errorf("cleanup: isolated-yard down %s: %v\n%s", d, err, out)
			}
		}
	})
}

// in runs a command inside the yard, through its wrapper.
func in(t *testing.T, d string, args ...string) string {
	t.Helper()
	out, err := exec.Command(filepath.Join(d, "e"), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s/e %s: %v\n%s", d, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// approve approves the ask seq on bead as the operator mckean, through the
// yard's socket, and returns what the yard ran printed: the line of its
// result, or the words of its refusal with the error. It says that its
// person saw the card's weight (weighed): yardr bead answer asks a weighty
// card for its number at a terminal and declines without one, and these
// yards have no terminal to give, so the approval is made as a client with
// a gate of its own would make it. The tests that use it are about what the
// yard does once a card is approved, not about how it is approved.
func approve(d, bead string, seq int) (string, error) {
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(d, "y", "yard.sock"))
	}}}
	defer hc.CloseIdleConnections()
	body := fmt.Sprintf(`{"bead":%q,"ask":%d,"actor":"mckean","weighed":true}`, bead, seq)
	resp, err := hc.Post("http://yard/call/ApproveAsk", "application/json", strings.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var reply struct {
		Result struct {
			Result string `json:"result"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return string(raw), err
	}
	if reply.Error != nil {
		return reply.Error.Message, fmt.Errorf("approving ask %d of %s: %s", seq, bead, reply.Error.Message)
	}
	return reply.Result.Result, nil
}

// pidIn reads the pid a server or daemon left in file, waiting for it.
func pidIn(t *testing.T, file string) int {
	t.Helper()
	for deadline := time.Now().Add(testload.Scale(10 * time.Second)); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		b, _ := os.ReadFile(file)
		line, _, _ := strings.Cut(string(b), "\n")
		pid, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		if n, err := strconv.Atoi(pid); err == nil && n > 0 {
			return n
		}
	}
	t.Fatalf("no pid in %s", file)
	return 0
}

// running reports whether pid is a live process (one that ended and was
// not reaped yet is not).
func running(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	s := strings.TrimSpace(string(out))
	return s != "" && !strings.HasPrefix(s, "Z")
}

// down runs `isolated-yard down d` and checks that it removed d and that
// none of pids is left.
func down(t *testing.T, d string, pids ...int) {
	t.Helper()
	out, err := script(t, "down", d)
	if err != nil {
		t.Fatalf("isolated-yard down: %v\n%s", err, out)
	}
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Errorf("%s is still there (%v)", d, err)
	}
	for _, pid := range pids {
		if running(pid) {
			state, _ := exec.Command("ps", "-o", "pid=,ppid=,pgid=,stat=,command=", "-p", strconv.Itoa(pid)).CombinedOutput()
			t.Errorf("pid %d outlived down:\n%s\n%s", pid, out, state)
		}
	}
}

// startDaemon starts the yard's daemon the way init --start does: in the
// background, in a session of its own.
func startDaemon(t *testing.T, d string) int {
	t.Helper()
	cmd := exec.Command(filepath.Join(d, "e"), "yardr", "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	return pidIn(t, filepath.Join(d, "y", "daemon.lock"))
}

// yardr-npdr: after `go build -o D/bin/yardr`, the yard's server and daemon
// run a file that D/bin no longer names, and a down that went by the files
// in D/bin said "stopped and removed" with both still running.
func TestDownAfterTheBinaryWasRebuilt(t *testing.T) {
	d := bare(t)
	in(t, d, "yardr", "server", "start")
	server := pidIn(t, filepath.Join(d, "y", "server.pid"))
	daemon := startDaemon(t, d)
	// Go's module cache deliberately makes downloaded files read-only. down
	// must still remove a yard after a test populated that cache.
	mod := filepath.Join(d, "h", "go", "pkg", "mod", "example.invalid", "module@v1")
	if err := os.MkdirAll(mod, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mod, "source.go"), []byte("package module\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mod, 0o555); err != nil {
		t.Fatal(err)
	}

	// Rebuilt the way go build does it and the way an install does it: the
	// file written anew, and a new file renamed over the name.
	bin := filepath.Join(d, "bin", "yardr")
	if out, err := exec.Command("go", "build", "-o", bin, "yardr.dev/yardr").CombinedOutput(); err != nil {
		t.Fatalf("rebuild: %v\n%s", err, out)
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+".new", b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bin+".new", bin); err != nil {
		t.Fatal(err)
	}
	if !running(server) || !running(daemon) {
		t.Fatalf("server running=%v daemon running=%v before down", running(server), running(daemon))
	}
	down(t, d, server, daemon)
}

// yardr-npdr: a server started by another yardr (the installed one) with
// its home in the yard runs no file of D/bin at all.
func TestDownStopsAServerStartedFromAnotherBinary(t *testing.T) {
	d := bare(t)
	other := filepath.Join(t.TempDir(), "yardr")
	if out, err := exec.Command("go", "build", "-o", other, "yardr.dev/yardr").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := filepath.Join(d, "y")
	cmd := exec.Command(other, "server", "start")
	cmd.Env = []string{"HOME=" + filepath.Join(d, "h"), "PATH=/usr/bin:/bin", "YARDR_HOME=" + home}
	// Not started through D/e, but a server of the yard all the same: named
	// to this binary like the rest, or a kill during the test leaves it, and
	// given the wait for it to start that the rest are given.
	for _, name := range []string{config.TestOwnerEnv, config.TestHomeCheckEnv, config.TestStartWaitEnv} {
		cmd.Env = append(cmd.Env, config.Env(name, config.Getenv(name))...)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("server start: %v\n%s", err, out)
	}
	server := pidIn(t, filepath.Join(home, "server.pid"))
	if !running(server) {
		t.Fatal("no server before down")
	}
	down(t, d, server)
}

// execSessionFuse is how long the daemon gets to claim a bead and start
// its exec script: a fuse against a hang, sized to the machine by waitFor.
const execSessionFuse = time.Minute

// yardr-1819: macOS's ps shows no environment for the system's own
// binaries, so an exec group's running script that starts `sleep 600` is
// found by neither the ps leg (no environment shown) nor the lsof leg
// (it runs no file of D/bin): pids lists it from its session handle,
// and down stops its process group, leaving none behind. On Linux the
// ps leg sees the script, so the test passes there with or without the
// fix; it guards the Mac.
func TestDownStopsAnExecGroupsRunningScript(t *testing.T) {
	d := bare(t)
	in(t, d, "yardr", "init", "--harness", "claude", "--operator", "jo")
	in(t, d, "yardr", "server", "start")
	server := pidIn(t, filepath.Join(d, "y", "server.pid"))
	cmd := `trap 'exit 0' TERM; sh -c 'trap "" TERM; echo $$ > "$YARDR_HOME/exec-child.pid"; exec sleep 600' & wait`
	in(t, d, "yardr", "--actor", "jo", "group", "add", "sleepers", "--runner", "exec", "--set", "cmd="+strconv.Quote(cmd))
	in(t, d, "yardr", "--actor", "jo", "route", "add", "--stage", "new", "--group", "sleepers")
	bead := strings.TrimSpace(in(t, d, "yardr", "--actor", "jo", "bead", "create", "sleep a while"))
	daemon := startDaemon(t, d)
	// The daemon claims the bead and starts its script; its session
	// names the script's pid, which is what pids must list.
	var execPid int
	if !waitFor(execSessionFuse, func() bool {
		out, err := runIn(d, "yardr", "--actor", "jo", "session", "list", "--running", "--json")
		if err != nil {
			return false
		}
		var ss []struct {
			Runner string         `json:"runner"`
			Handle map[string]any `json:"handle"`
		}
		if json.Unmarshal([]byte(out), &ss) != nil {
			return false
		}
		for _, s := range ss {
			if s.Runner != "exec" {
				continue
			}
			if pid, ok := s.Handle["pid"].(float64); ok && pid > 0 {
				execPid = int(pid)
				return true
			}
		}
		return false
	}) {
		out, _ := runIn(d, "yardr", "--actor", "jo", "session", "list")
		t.Fatalf("no running exec session for %s:\n%s", bead, out)
	}
	childPath := filepath.Join(d, "y", "exec-child.pid")
	var childPid int
	if !waitFor(execSessionFuse, func() bool {
		b, err := os.ReadFile(childPath)
		if err != nil {
			return false
		}
		childPid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return childPid > 0
	}) {
		t.Fatalf("exec script did not start its child (session pid %d)", execPid)
	}
	if !running(childPid) {
		t.Fatalf("exec child %d did not stay running", childPid)
	}
	pgid, err := exec.Command("ps", "-o", "pgid=", "-p", strconv.Itoa(childPid)).Output()
	if err != nil || strings.TrimSpace(string(pgid)) != strconv.Itoa(execPid) {
		t.Fatalf("exec child %d has process group %q, want leader %d (err %v)", childPid, strings.TrimSpace(string(pgid)), execPid, err)
	}
	// The child ignores TERM, so down must retain the process group after its
	// shell leader exits and send KILL to the same group.
	t.Cleanup(func() {
		for _, pid := range []int{execPid, childPid} {
			if running(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	out, err := script(t, "pids", d)
	if err != nil {
		t.Fatalf("isolated-yard pids: %v\n%s", err, out)
	}
	if !slices.Contains(strings.Fields(out), strconv.Itoa(execPid)) {
		t.Errorf("pids lacks the exec script %d:\n%s", execPid, out)
	}
	down(t, d, server, daemon, execPid, childPid)
}

// down stops processes by the directory it is given, so it takes none that
// `up` did not make: not a directory that only has the name of one, and not
// one that carries another yard's mark.
func TestDownRefusesWhatUpDidNotMake(t *testing.T) {
	t.Parallel()
	plain, err := os.MkdirTemp("/tmp", "iy.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(plain) })
	wrapper := func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "e"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wrapper(plain)
	marked, err := os.MkdirTemp("/tmp", "iy.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(marked) })
	wrapper(marked)
	if err := os.WriteFile(filepath.Join(marked, ".isolated-yard"), []byte(plain+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{plain, marked, t.TempDir()} {
		out, err := script(t, "down", dir)
		if err == nil || !strings.Contains(out, "isolated-yard: ") {
			t.Errorf("down %s: err %v, output %q; want a refusal", dir, err, out)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("down %s removed it: %v", dir, err)
		}
	}
}

// match runs `isolated-yard match home` over lines of "<pid> <environment>"
// and returns what it printed and whether it failed.
func match(t *testing.T, home string, lines ...string) (string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "contrib/isolated-yard/isolated-yard"), "match", home)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return stderr.String(), err
	}
	return strings.Join(strings.Fields(string(out)), " "), nil
}

// yardr-6gyb: the home was escaped into a regular expression by a sed
// expression GNU sed refuses, so on Linux the pattern was empty and every
// process with any YARDR_HOME was one of the yard. The home is compared as
// a string: what a pattern would read as an operator is a character here.
func TestMatchComparesTheHomeAsAString(t *testing.T) {
	t.Parallel()
	for _, home := range []string{
		"/tmp/iy.Ab12Cd",
		"/tmp/iy.a[b]",
		"/tmp/iy.[.x.]",
		"/tmp/iy.a*b+c?",
		"/tmp/iy.with space",
		`/tmp/iy.back\slash$^(x|y)`,
	} {
		got, err := match(t, home,
			"11 PATH=/bin YARDR_HOME="+home+"/y TERM=x",
			"12 YARDR_HOME="+home,
			"13 YARDR_HOME=/private"+home+"/y",
			// Not the yard's: a longer name, another yard, a real yard,
			// no home, the home under another name, and a line of no process.
			"21 YARDR_HOME="+home+"x/y",
			"22 YARDR_HOME=/tmp/iy.other/y",
			"23 YARDR_HOME=/Users/someone/.yardr",
			"24 PATH=/bin",
			"25 NOT_YARDR_HOME="+home+"/y",
			"26 YARDR_HOME=/elsewhere"+home+"/y",
			"x YARDR_HOME="+home+"/y",
		)
		if err != nil || got != "11 12 13" {
			t.Errorf("match %q: %q, %v; want 11 12 13", home, got, err)
		}
	}
	// A home that is all pattern matches itself only.
	if got, err := match(t, "/.*", "1 YARDR_HOME=/tmp/iy.a/y", "2 YARDR_HOME=/.*/y"); err != nil || got != "2" {
		t.Errorf("match /.*: %q, %v; want 2", got, err)
	}
}

// yardr-6gyb: nothing may match every process. No home, "/" and what is no
// absolute path are refused, with a message and no pid.
func TestMatchRefusesAHomeThatWouldMatchEverything(t *testing.T) {
	t.Parallel()
	for _, home := range []string{"", "/", "//", "/tmp/", "tmp/iy.a", "."} {
		out, err := match(t, home, "1 YARDR_HOME=/tmp/iy.a/y", "2 YARDR_HOME=/y", "3 YARDR_HOME=")
		if err == nil || !strings.Contains(out, "isolated-yard: ") {
			t.Errorf("match %q: %q, %v; want a refusal", home, out, err)
		}
	}
}

// The list of a yard's processes is refused, not empty and not everyone's,
// for what is not an isolated yard.
func TestPidsRefusesWhatIsNoYard(t *testing.T) {
	t.Parallel()
	unmarked, err := os.MkdirTemp("/tmp", "iy.")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(unmarked) })
	home, _ := os.UserHomeDir()
	for _, dir := range []string{"", "/", "/tmp", "/tmp/iy.", unmarked, unmarked + "/y", "/tmp/iy.nosuchyard", filepath.Join(home, ".yardr"), t.TempDir()} {
		out, err := script(t, "pids", dir)
		if err == nil || !strings.HasPrefix(out, "isolated-yard: ") || strings.Contains(out, "\n") {
			t.Errorf("pids %q: err %v, output %q; want a refusal and nothing else", dir, err, out)
		}
	}
}

// A failed process listing must not look like an empty yard and let down
// remove it without knowing whether any of its processes are still alive.
func TestDownRefusesWhenPsFails(t *testing.T) {
	d := bare(t)
	bin := t.TempDir()
	ps := filepath.Join(bin, "ps")
	if err := os.WriteFile(ps, []byte("#!/bin/sh\necho simulated ps failure >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "contrib/isolated-yard/isolated-yard"), "down", d)
	cmd.Dir = root
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "PATH=") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "cannot tell the processes") || !strings.Contains(string(out), "ps failed") {
		t.Fatalf("down with failing ps: err %v, output %q; want a refusal", err, out)
	}
	if _, err := os.Stat(d); err != nil {
		t.Fatalf("down removed the yard after ps failed: %v", err)
	}
}

// yardr-b6x7: two removers can meet in one yard, the watchdog's down and the
// cleanup of a test that gave up waiting for it, and the later one must not
// fail for what the other did. chmod is where it finds the yard gone or going,
// so the chmod on its PATH stands for the other remover, as far along as each
// case says. Timing cannot pin this (the other remover is a few milliseconds
// ahead or it is not), so each case fixes how far ahead it is. A failure that
// is no one's race stays one: in the third case rm refuses as well. The yard
// is a stub, the mark and a D/e: all down does with it is look for its
// processes and remove it.
func TestDownMeetsAnotherRemover(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		// The bodies of the chmod and rm stubs ($3 is the yard, as in chmod
		// -R u+w D); an empty rm is the real one. gone is a yard that is
		// removed before down begins.
		chmod, rm string
		gone      bool
		status    int
		out       string // with the yard for its %s
		removed   bool
	}{
		{name: "the yard is gone when chmod runs", chmod: `/bin/rm -rf "$3"; echo "chmod: $3: No such file or directory" >&2; exit 1`,
			status: 0, out: "stopped and removed %s", removed: true},
		{name: "a file goes while chmod walks", chmod: `echo "chmod: $3/m46: No such file or directory" >&2; exit 1`,
			status: 0, out: "stopped and removed %s", removed: true},
		{name: "chmod fails and rm refuses with the yard there", chmod: `echo "chmod: $3: Operation not permitted" >&2; exit 1`, rm: `echo "rm: $3: Permission denied" >&2; exit 1`,
			status: 1, out: "Permission denied"},
		// What downAtEnd lets pass, and no other answer.
		{name: "the yard is gone before down begins", gone: true,
			status: 2, out: "isolated-yard: no yard at %s", removed: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d, err := os.MkdirTemp("/tmp", "iy.")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(d) })
			bin := t.TempDir()
			files := map[string]string{
				filepath.Join(d, ".isolated-yard"): d + "\n",
				filepath.Join(d, "e"):              "#!/bin/sh\nexit 0\n",
			}
			for name, body := range map[string]string{"chmod": c.chmod, "rm": c.rm} {
				if body != "" {
					files[filepath.Join(bin, name)] = "#!/bin/sh\n" + body + "\n"
				}
			}
			for path, body := range files {
				if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if c.gone {
				os.RemoveAll(d)
			}

			cmd := exec.Command(scriptPath(t), "down", d)
			cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "PATH=") }),
				"PATH="+bin+":"+os.Getenv("PATH"))
			out, _ := cmd.CombinedOutput()
			if got := cmd.ProcessState.ExitCode(); got != c.status {
				t.Errorf("down exited %d, want %d:\n%s", got, c.status, out)
			}
			if want := strings.Replace(c.out, "%s", d, 1); !strings.Contains(string(out), want) {
				t.Errorf("down printed:\n%s\nwant %q", out, want)
			}
			if _, err := os.Stat(d); os.IsNotExist(err) != c.removed {
				t.Errorf("after down, %s: %v; want it removed: %v", d, err, c.removed)
			}
		})
	}
}

// noSuchPid is a pid no system has, past every pid_max: a signal sent to it
// reaches no one, and the process a listing names by it never ends.
const noSuchPid = 2147483000

// yardr-st1s: two downs can converge on one yard (the watchdog's and a
// test's cleanup), and the later one must not fail for what the earlier did
// after its check of the mark. A stub stands for the other remover; the yard
// is a stub too, the mark and a D/e: all down does with it is look for its
// processes and remove it. The first two cases strike between the listings:
// the stub ps removes the yard at its first axeww listing, then runs the real
// ps. In the second every listing also names a process of the home that no
// signal ends (another user's, in life), so the check before the removal
// finds it still running with the yard already gone, however many listings
// come before that check. The third strikes at the first read of the mark
// after down's own: the stub cat takes the yard first, so a down that asks
// for the mark again (as its first listing would, were the check not
// remembered) refuses the yard.
func TestDownWhenAnotherRemoverTakesTheYardBetweenListings(t *testing.T) {
	t.Parallel()
	realPs, err := exec.LookPath("ps")
	if err != nil {
		t.Fatal(err)
	}
	realCat, err := exec.LookPath("cat")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		// atMark is the stub that strikes at a read of the mark (cat), not at
		// a listing (ps). stuck lists a process of the yard that no signal
		// ends.
		atMark, stuck bool
		status        int
		// out names what down prints, with the yard for its %s; absent names
		// what it must not print.
		out    []string
		absent []string
	}{
		{name: "between the listings, no process of the yard is left", status: 0,
			out: []string{"stopped and removed %s"}},
		{name: "between the listings, a process of the yard is still running", stuck: true, status: 1,
			out:    []string{"still running", "%s already removed: " + strconv.Itoa(noSuchPid)},
			absent: []string{"kept"}},
		{name: "at the first read of the mark after the check", atMark: true, status: 0,
			out:    []string{"stopped and removed %s"},
			absent: []string{"was not made by"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			d, err := os.MkdirTemp("/tmp", "iy.")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(d) })
			bin := t.TempDir()
			files := map[string]string{
				filepath.Join(d, ".isolated-yard"): d + "\n",
				filepath.Join(d, "e"):              "#!/bin/sh\nexit 0\n",
			}
			if c.atMark {
				// Down reads the mark once of itself, at its start.
				files[filepath.Join(bin, "cat")] = `#!/bin/sh
if [ -f "` + filepath.Join(bin, "cat.read") + `" ]; then
  /bin/rm -rf "` + d + `"
fi
: > "` + filepath.Join(bin, "cat.read") + `"
exec "` + realCat + `" "$@"
`
			} else {
				stuck := ""
				if c.stuck {
					stuck = `echo "` + strconv.Itoa(noSuchPid) + ` sleep YARDR_HOME=` + d + `/y"`
				}
				// The first listing takes the yard after down's read of the
				// mark for it (home_pids reads the mark before it runs ps); the
				// later ones find it gone.
				files[filepath.Join(bin, "ps")] = `#!/bin/sh
case " $* " in
*" axeww "*) ;;
*) exec "` + realPs + `" "$@" ;;
esac
/bin/rm -rf "` + d + `"
"` + realPs + `" "$@" || exit 1
` + stuck + `
`
			}
			for path, body := range files {
				if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			cmd := exec.Command(scriptPath(t), "down", d)
			cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "PATH=") }),
				"PATH="+bin+":"+os.Getenv("PATH"))
			out, _ := cmd.CombinedOutput()
			if got := cmd.ProcessState.ExitCode(); got != c.status {
				t.Errorf("down exited %d, want %d:\n%s", got, c.status, out)
			}
			for _, want := range c.out {
				if want = strings.Replace(want, "%s", d, 1); !strings.Contains(string(out), want) {
					t.Errorf("down printed:\n%s\nwant %q", out, want)
				}
			}
			for _, want := range c.absent {
				if strings.Contains(string(out), want) {
					t.Errorf("down printed:\n%s\nwant no %q", out, want)
				}
			}
			if _, err := os.Stat(d); !os.IsNotExist(err) {
				t.Errorf("after down, %s: %v; want it removed", d, err)
			}
		})
	}
}

// yardr-jd3w: a version probe (`claude --version`, what yardr doctor runs)
// and a one-shot call (`claude -p`, what yardr doctor --try runs) start no
// agent, so `down` leaves those starts out of what it reports: a trial's
// last line reads as proof of how many agents ran.
func TestDownLeavesProbesOutOfTheStandInCount(t *testing.T) {
	d := bare(t)
	in(t, d, "yardr", "init", "--harness", "claude", "--operator", "jo")
	// The probe is the one doctor runs; it is started directly, as the
	// one-shot call is, so the test does not depend on doctor.
	in(t, d, "claude", "--version")
	in(t, d, "claude", "-p", "say ok")
	// One real start: the stand-in with no probe args, its stdin closed
	// so it exits.
	in(t, d, "claude")
	log, err := os.ReadFile(filepath.Join(d, "agents.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []string{`args=["--version"]`, `args=["-p" "say ok"]`} {
		if !strings.Contains(string(log), probe) {
			t.Errorf("agents.log has no start with %s:\n%s", probe, log)
		}
	}
	out, err := script(t, "down", d)
	if err != nil {
		t.Fatalf("isolated-yard down: %v\n%s", err, out)
	}
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Errorf("%s is still there (%v)", d, err)
	}
	if !strings.Contains(out, "stand-in agents started: 1") {
		t.Errorf("down printed %q; want it to report one agent started", out)
	}
}

// As a sleeper (see there) the test binary only waits to be stopped, by its
// owner's end too if it has one. Otherwise it names itself to the servers its
// yards start: D/e hands the name on (isolated-yard's build), so a run killed
// before its cleanups leaves no server or daemon of a yard behind.
func TestMain(m *testing.M) {
	if os.Getenv("ISOLATED_TEST_SLEEPER") != "" {
		// A sleeper stands for a process of another yard, which no down of
		// this binary's yards stops. The binary that started it runs no
		// cleanup when it is killed, so a sleeper with an owner goes with it,
		// as a server does; the five minutes are the cap of one with none.
		if pid, start, ok := config.TestOwner(); ok {
			go func() {
				<-config.WatchOwner(context.Background(), time.Second, pid, start)
				os.Exit(0)
			}()
		}
		time.Sleep(5 * time.Minute)
		return
	}
	storetest.NameTestBinary()
	os.Exit(m.Run())
}

// sleeper starts a process with the given YARDR_HOME that does nothing, and
// ends it with the test. It is this test binary, not sleep(1): macOS's ps
// shows no environment for the system's own binaries, so down does not see
// one of those by its home. It is named to this binary, so it ends with it.
func sleeper(t *testing.T, home string) int {
	t.Helper()
	return startSleeper(t, home, config.Getenv(config.TestOwnerEnv))
}

// startSleeper is sleeper named to the owner, YARDR_TEST_OWNER's value, or to
// none for "".
func startSleeper(t *testing.T, home, owner string) int {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "ISOLATED_TEST_SLEEPER=1", "YARDR_HOME=" + home}
	if owner != "" {
		cmd.Env = append(cmd.Env, config.Env(config.TestOwnerEnv, owner)...)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Reaped as they die: `down` waits on kill -0, which answers for a
	// sleeper it just stopped until the test ends, burning the whole 5 s
	// wait every time.
	go cmd.Wait()
	t.Cleanup(func() {
		cmd.Process.Kill() // the wait above reaps it
	})
	return cmd.Process.Pid
}

// ownerGoneFuse gives a loaded test process time to start its one-second
// watch, and keeps a broken watch from leaving the sleeper's five-minute cap
// behind. For the tests that wait on a whole down it is not the time the down
// may take but the time it may stand still (waitWhileDown): a full down (herdr
// stop, the daemon's and the server's stops, the sweeps with their five-second
// waits, the removal) costs what the machine makes it, and a limit on the whole
// of it gave out in a landing at 15 s and again at a minute, leaving the test's
// cleanup to race the down it gave up on (yardr-b6x7). watchdogStartFuse is a
// minute for another reason: the first run of a program built a moment ago can
// wait behind the machine's other first runs. watchdogRunFuse bounds the status
// test, which intentionally retries after two one-second answers, as
// ownerGoneFuse bounds a down. All three are written for an idle box: waitFor
// and waitWhileDown size them to the machine (testload.Scale), so none is
// raised by hand when a landing's load outgrows it.
const (
	ownerGoneFuse     = time.Minute
	watchdogStartFuse = time.Minute
	watchdogRunFuse   = time.Minute
)

// deadOwner is the name of a test binary that has ended, as NameTestBinary
// gives one (YARDR_TEST_OWNER's value: the pid and the start time): a sleep,
// described while it ran, then killed and reaped. Its pid may be another
// process's by now, which the start time is there to tell.
func deadOwner(t *testing.T) string {
	t.Helper()
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	var start uint64
	if st, err := peer.Describe(sleep.Process.Pid); err == nil {
		start = st.Start
	}
	sleep.Process.Kill()
	sleep.Wait()
	return strconv.Itoa(sleep.Process.Pid) + " " + strconv.FormatUint(start, 10)
}

// yardr-q4bk: a sleeper named to a binary that is gone ends by itself, at the
// next look of its watch, where a test binary killed before its cleanups left
// it waiting for its five minutes. Another test pins the other half: the
// sleepers of TestDownLeavesTheProcessesOfOtherYards, named to this binary,
// are still running when its down is over.
func TestSleeperEndsWhenItsOwnerIsGone(t *testing.T) {
	t.Parallel()
	pid := startSleeper(t, filepath.Join(t.TempDir(), "home"), deadOwner(t))
	if !waitFor(ownerGoneFuse, func() bool { return !running(pid) }) {
		t.Errorf("the sleeper (pid %d) is still running", pid)
	}
}

// yardr-6gyb: down on Linux stopped the processes of every yard on the
// machine, the servers of the tests running beside it among them. A process
// with its home in another yard, in a real one or in a directory that only
// begins like this yard's outlives down.
func TestDownLeavesTheProcessesOfOtherYards(t *testing.T) {
	d := bare(t)
	mine := []int{sleeper(t, d+"/y"), sleeper(t, d)}
	others := []int{
		sleeper(t, d+"x/y"),
		sleeper(t, "/tmp/iy.another/y"),
		sleeper(t, filepath.Join(t.TempDir(), "home")),
		sleeper(t, ""),
		sleeper(t, "/"),
	}
	out, err := script(t, "pids", d)
	if err != nil {
		t.Fatalf("isolated-yard pids: %v\n%s", err, out)
	}
	listed := map[string]bool{}
	for _, f := range strings.Fields(out) {
		listed[f] = true
	}
	for _, pid := range mine {
		if !listed[strconv.Itoa(pid)] {
			t.Errorf("pids lacks %d of the yard: %q", pid, out)
		}
	}
	for _, pid := range others {
		if listed[strconv.Itoa(pid)] {
			t.Errorf("pids has %d, which is not of the yard: %q", pid, out)
		}
	}
	if t.Failed() {
		t.FailNow() // down would signal what pids listed
	}
	down(t, d, mine...)
	for _, pid := range others {
		if !running(pid) {
			t.Errorf("down stopped pid %d, which is not of the yard", pid)
		}
	}
}

// The login shell of a yard finds what its PATH has and no more: yardr asks
// it for a program PATH lacks, and yardr up starts the herdr it finds. With
// the system's profile alone a bare yard's login shell had the machine's own
// directories on PATH, and yardr up there started the machine's herdr, with
// the real agent on its panes' PATH (yardr-0pk6).
func TestLoginShellFindsNothingOfTheMachine(t *testing.T) {
	d := bare(t)
	want := d + "/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	if got := strings.TrimSpace(in(t, d, "/bin/sh", "-l", "-c", "echo $PATH")); got != want {
		t.Fatalf("the login shell's PATH is %s, want %s", got, want)
	}
	if got := strings.TrimSpace(in(t, d, "/bin/sh", "-l", "-c", "command -v herdr || echo none")); got != "none" {
		t.Fatalf("the login shell of a bare yard finds herdr: %s", got)
	}
}

// TestWrapperEnvironment pins what D/e passes through from the environment
// that made the yard. Without __CF_USER_TEXT_ENCODING a newly built binary's
// first run under `codex sandbox` stalls in dyld's initializers, before
// main, in _CFStringGetUserDefaultEncoding -> getpwuid; a test binary's name
// lets the yard's server and daemon stop when that binary is killed, and its
// long wait for a server to start keeps a loaded machine from failing the
// yardr that starts one.
func TestWrapperEnvironment(t *testing.T) {
	d := bare(t)
	out, err := exec.Command(filepath.Join(d, "e"), "printenv", "__CF_USER_TEXT_ENCODING").CombinedOutput()
	if runtime.GOOS == "darwin" {
		if err != nil {
			t.Fatalf("D/e printenv __CF_USER_TEXT_ENCODING: %v\n%s", err, out)
		}
		got := strings.TrimSpace(string(out))
		if got == "" {
			t.Fatal("D/e leaves __CF_USER_TEXT_ENCODING empty")
		}
		if want := os.Getenv("__CF_USER_TEXT_ENCODING"); want != "" && got != want {
			t.Fatalf("D/e __CF_USER_TEXT_ENCODING = %q, want the caller's %q", got, want)
		}
	} else if err == nil {
		t.Fatalf("D/e sets __CF_USER_TEXT_ENCODING to %q off macOS; want it unset", strings.TrimSpace(string(out)))
	}

	for _, name := range []string{config.TestOwnerEnv, config.TestHomeCheckEnv, config.TestStartWaitEnv} {
		want := config.Getenv(name)
		if want == "" {
			t.Fatalf("%s is not set in this binary: TestMain names it (storetest.NameTestBinary)", config.Key(name))
		}
		got, err := runIn(d, "printenv", config.Key(name))
		if got = strings.TrimSpace(got); err != nil || got != want {
			t.Errorf("D/e hands on %s=%q (%v), want this binary's %q", config.Key(name), got, err, want)
		}
	}

	hand := bareEnv(t, slices.DeleteFunc(os.Environ(), func(kv string) bool {
		key, _, _ := strings.Cut(kv, "=")
		return config.IsVar(key, config.TestOwnerEnv, config.TestHomeCheckEnv, config.TestStartWaitEnv)
	}))
	if got := in(t, hand, "env"); strings.Contains(got, "YARDR_TEST_") {
		t.Errorf("D/e of a yard made without a name for a test binary hands one on:\n%s", got)
	}
	// Such a yard has no binary to watch for: its watchdog says so and ends at
	// once, and the yard is as it was before yards had a watchdog.
	if !waitFor(watchdogStartFuse, func() bool {
		log, _ := os.ReadFile(filepath.Join(hand, "watchdog.log"))
		return strings.Contains(string(log), "no test binary named this yard")
	}) {
		t.Error("the watchdog of the yard made by hand did not say it has nothing to watch")
	}
	if !waitFor(ownerGoneFuse, func() bool { return len(watchdogsOf(t, hand)) == 0 }) {
		t.Error("the watchdog of the yard made by hand is still running")
	}
}
