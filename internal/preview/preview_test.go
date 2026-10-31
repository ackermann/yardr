package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// freePort reserves a loopback port and frees it for the preview.
func freePort(t *testing.T) int {
	return freePortAt(t, "127.0.0.1")
}

func freePortAt(t *testing.T, host string) int {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type yard struct {
	st   *store.Store
	home string
	sup  *Supervisor
}

func newYard(t *testing.T) *yard {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(context.Background(), store.OperatorSetting, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(context.Background()) })
	sup := New(st, home, nil)
	sup.Poll = 10 * time.Millisecond
	sup.BindWait = 5 * time.Second
	sup.Backoffs = []time.Duration{0, 10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}
	return &yard{st: st, home: home, sup: sup}
}

func (y *yard) run(t *testing.T) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go y.sup.Run(ctx)
	t.Cleanup(cancel)
	return cancel
}

func (y *yard) propose(t *testing.T, app, command string, port int, approve bool) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("hello preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	y.proposeIn(t, app, command, dir, port, approve)
}

// proposeIn proposes the preview to run in dir, and approves it when asked.
func (y *yard) proposeIn(t *testing.T, app, command, dir string, port int, approve bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := y.st.ProposePreview(ctx, store.NewPreview{App: app, Command: command, Dir: dir, Port: port}, "builder"); err != nil {
		t.Fatal(err)
	}
	if approve {
		if _, err := y.st.ApprovePreview(ctx, app, "ada"); err != nil {
			t.Fatal(err)
		}
	}
}

// gitTree is a directory with one commit, for a preview to read its depot
// tree from; the second result runs git in it.
func gitTree(t *testing.T) (string, func(args ...string) string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("config", "user.email", "a@b.c")
	run("config", "user.name", "a")
	run("commit", "-q", "--allow-empty", "-m", "one")
	return dir, run
}

func (y *yard) state(app string) store.Preview {
	p, err := y.st.GetPreview(context.Background(), app)
	if err != nil {
		return store.Preview{State: "gone"}
	}
	return *p
}

func TestApprovedPreviewStartsAndAnswersThroughEndpoint(t *testing.T) {
	if _, err := os.Stat(python3()); err != nil {
		t.Skip("python3 not available for the stand-in app")
	}
	y := newYard(t)
	y.run(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	busyPort := ln.Addr().(*net.TCPAddr).Port
	y.propose(t, "busy", "true", busyPort, true)
	waitFor(t, "busy preview failed without exposing the existing listener", func() bool {
		return y.state("busy").State == store.PreviewFailed
	})
	if got := y.state("busy"); !strings.Contains(got.StateDetail, "port already in use") {
		t.Fatalf("busy port detail = %q", got.StateDetail)
	}
	ln.Close()
	port := freePort(t)
	t.Setenv("PORT", "9000")
	t.Setenv("PREVIEW_APP", "wrong")
	t.Setenv("PREVIEW_PORT", "9001")
	for _, k := range []string{"SSH_AUTH_SOCK", "GPG_AGENT_SOCKET", "DB_PASSWORD", "GOOGLE_APPLICATION_CREDENTIALS", "GH_TOKEN"} {
		t.Setenv(k, "held")
	}
	env := previewEnv("demo", port)
	for _, kv := range env {
		if strings.HasSuffix(kv, "=held") {
			t.Errorf("preview environment keeps %s", kv)
		}
	}
	for key, want := range map[string]string{
		"PORT": strconv.Itoa(port), "PREVIEW_APP": "demo", "PREVIEW_PORT": strconv.Itoa(port),
	} {
		var values []string
		for _, kv := range env {
			k, v, _ := strings.Cut(kv, "=")
			if k == key {
				values = append(values, v)
			}
		}
		if len(values) != 1 || values[0] != want {
			t.Errorf("preview environment %s = %q, want one value %q", key, values, want)
		}
	}
	y.propose(t, "demo", fmt.Sprintf("python3 -m http.server %d --bind 127.0.0.1", port), port, true)

	waitFor(t, "preview running", func() bool { return y.state("demo").State == store.PreviewRunning })
	srv := httptest.NewServer(y.sup.Handler())
	defer srv.Close()
	var body string
	waitFor(t, "answer through the supervisor endpoint", func() bool {
		resp, err := http.Get(srv.URL + "/preview/demo/")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		body = string(raw)
		return resp.StatusCode == http.StatusOK && strings.Contains(body, "hello preview")
	})
	if got := y.state("demo"); got.LastRequestAt == nil {
		t.Fatalf("last request not recorded: %+v", got)
	}
	waitFor(t, "output in the preview log", func() bool {
		lines, err := LogTail(filepath.Join(y.home, "previews", "demo.log"), 100)
		return err == nil && len(lines) > 0
	})
	// A direct call to the app's loopback port answers too: the
	// supervisor's endpoint is a proxy in front of it, not a copy.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("direct loopback status = %d", resp.StatusCode)
	}

	// The yard-side endpoint removes edge credentials while keeping the
	// app's own cookies.
	headers := make(http.Header)
	headers.Set("X-Edge-Token", "token")
	headers.Set("X-Edge-JWT", "jwt")
	headers.Set("Edge-Yard", "yard")
	headers.Set("Cookie", "app=one; ory_kratos_session=kratos; edge_yard=yard; __Host-edge_preview=edge; theme=dark")
	stripEdgeCredentials(headers)
	for _, name := range edgeHeaders {
		if headers.Get(name) != "" {
			t.Errorf("edge header %s reached the app: %q", name, headers.Get(name))
		}
	}
	if got, want := headers.Get("Cookie"), "app=one; theme=dark"; got != want {
		t.Errorf("forwarded cookies = %q, want %q", got, want)
	}

	// IPv6 loopback is an allowed bind and must be reachable through the
	// same yard-side endpoint.
	t.Run("ipv6 loopback", func(t *testing.T) {
		probe, err := net.Listen("tcp", net.JoinHostPort("::1", "0"))
		if err != nil {
			t.Skipf("IPv6 loopback is unavailable: %v", err)
		}
		port6 := probe.Addr().(*net.TCPAddr).Port
		probe.Close()
		dir := t.TempDir()
		script := fmt.Sprintf(`import http.server, socket
class Server(http.server.HTTPServer):
    address_family = socket.AF_INET6
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"hello ipv6")
Server(("::1", %d), Handler).serve_forever()
`, port6)
		if err := os.WriteFile(filepath.Join(dir, "serve.py"), []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := y.st.ProposePreview(context.Background(), store.NewPreview{
			App: "v6", Command: "python3 serve.py", Dir: dir, Port: port6,
		}, "builder"); err != nil {
			t.Fatal(err)
		}
		if _, err := y.st.ApprovePreview(context.Background(), "v6", "ada"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "IPv6 preview running", func() bool { return y.state("v6").State == store.PreviewRunning })
		waitFor(t, "IPv6 preview answer", func() bool {
			resp, err := http.Get(srv.URL + "/preview/v6/")
			if err != nil {
				return false
			}
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			return resp.StatusCode == http.StatusOK && strings.Contains(string(raw), "hello ipv6")
		})
	})
}

func TestStartWithoutApprovalIsRefused(t *testing.T) {
	y := newYard(t)
	port := freePort(t)
	y.propose(t, "demo", "true", port, false)
	if err := y.sup.Start(context.Background(), "demo"); err == nil || !strings.Contains(err.Error(), "no approval") {
		t.Fatalf("start err = %v", err)
	}
	if got := y.state("demo"); got.State != store.PreviewPending {
		t.Fatalf("state = %+v", got)
	}
}

// A start runs the approved command only on the depot tree the approval
// was given on. An unchanged tree starts as it always did; one whose
// commit has moved is not started: the approval is taken back, the card
// waits pending for a new one, and the command runs once it is given. The
// command writes its marker outside the tree, so it leaves the tree as it
// found it. (The dirty mark is the store's rule, in its own test.)
func TestStartRunsOnlyOnTheApprovedTree(t *testing.T) {
	ctx := context.Background()
	// start runs the approved form and waits for the command to have run
	// and exited; marker is the file the command touches.
	start := func(t *testing.T, y *yard, marker string) {
		t.Helper()
		if err := y.sup.Start(ctx, "demo"); err != nil {
			t.Fatalf("start: %v", err)
		}
		waitFor(t, "the approved command to run and exit", func() bool {
			_, err := os.Stat(marker)
			return err == nil && y.state("demo").StateDetail == "exited"
		})
	}

	t.Run("an unchanged tree starts", func(t *testing.T) {
		y := newYard(t)
		dir, _ := gitTree(t)
		marker := filepath.Join(t.TempDir(), "ran")
		y.proposeIn(t, "demo", "touch "+marker, dir, freePort(t), true)
		start(t, y, marker)
	})

	t.Run("another tree waits for a new approval", func(t *testing.T) {
		y := newYard(t)
		dir, git := gitTree(t)
		marker := filepath.Join(t.TempDir(), "ran")
		y.proposeIn(t, "demo", "touch "+marker, dir, freePort(t), true)
		git("commit", "-q", "--allow-empty", "-m", "two")
		err := y.sup.Start(ctx, "demo")
		if err == nil || !strings.Contains(err.Error(), "approval is taken back") {
			t.Fatalf("start on another tree err = %v, want the approval taken back", err)
		}
		head := git("rev-parse", "HEAD")
		got := y.state("demo")
		if got.State != store.PreviewPending || got.StateDetail != store.PreviewTreeChangedDetail || got.Approval != nil || got.Commit != head {
			t.Fatalf("after the refused start = %+v, want pending at %q with no approval", got, head)
		}
		// No pass over the registry, and no second start, runs a card that
		// waits for its person.
		y.sup.reconcile(ctx, false)
		if err := y.sup.Start(ctx, "demo"); err == nil {
			t.Fatal("start of a card with no approval succeeded")
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("the command ran on a tree its approval was not given on")
		}
		if _, err := y.st.ApprovePreview(ctx, "demo", "ada"); err != nil {
			t.Fatal(err)
		}
		start(t, y, marker)
	})

	// A preview that runs when the tree moves and its restart is asked for
	// goes back to pending too, so what ran is stopped with the approval.
	t.Run("a running preview whose approval is taken back is stopped", func(t *testing.T) {
		if _, err := os.Stat(python3()); err != nil {
			t.Skip("python3 not available for the stand-in app")
		}
		y := newYard(t)
		y.run(t)
		dir, git := gitTree(t)
		port := freePort(t)
		y.proposeIn(t, "demo", fmt.Sprintf("python3 -m http.server %d --bind 127.0.0.1", port), dir, port, true)
		waitFor(t, "preview running", func() bool { return y.state("demo").State == store.PreviewRunning })
		git("commit", "-q", "--allow-empty", "-m", "two")
		if _, err := y.st.RestartPreview(ctx, "demo", "ada"); err == nil || !strings.Contains(err.Error(), "approval is taken back") {
			t.Fatalf("restart on another tree err = %v, want the approval taken back", err)
		}
		waitFor(t, "the taken-back preview's process to be gone", func() bool {
			return !y.sup.Running("demo") && listeningAddress(port) == ""
		})
		if got := y.state("demo"); got.State != store.PreviewPending || got.Approval != nil {
			t.Fatalf("after the process stopped = %+v, want pending with no approval", got)
		}
	})
}

func TestNonLoopbackBindIsRefused(t *testing.T) {
	if _, err := os.Stat(python3()); err != nil {
		t.Skip("python3 not available for the stand-in app")
	}
	hosts := []string{"0.0.0.0"}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				hosts = append(hosts, ip.String())
				break
			}
		}
	}
	for i, host := range hosts {
		t.Run(host, func(t *testing.T) {
			y := newYard(t)
			y.run(t)
			app := "wide"
			if i > 0 {
				app = "specific"
			}
			port := freePortAt(t, host)
			y.propose(t, app, fmt.Sprintf("python3 -m http.server %d --bind %s", port, host), port, true)
			waitFor(t, "preview refused for an outside bind", func() bool {
				return y.state(app).State == store.PreviewRefused
			})
			if got := y.state(app); !strings.HasPrefix(got.StateDetail, "bound ") {
				t.Fatalf("refusal detail = %q", got.StateDetail)
			}
		})
	}
	t.Run("binds outside after loopback", func(t *testing.T) {
		y := newYard(t)
		y.run(t)
		port := freePort(t)
		dir := t.TempDir()
		script := fmt.Sprintf(`import socket, time
port = %d
local = socket.socket()
local.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
local.bind(("127.0.0.1", port))
local.listen()
time.sleep(0.6)
local.close()
wide = socket.socket()
wide.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
wide.bind(("0.0.0.0", port))
wide.listen()
time.sleep(60)
`, port)
		if err := os.WriteFile(filepath.Join(dir, "bind.py"), []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := y.st.ProposePreview(context.Background(), store.NewPreview{
			App: "late-wide", Command: "python3 bind.py", Dir: dir, Port: port,
		}, "builder"); err != nil {
			t.Fatal(err)
		}
		if _, err := y.st.ApprovePreview(context.Background(), "late-wide", "ada"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "later outside bind refused", func() bool {
			return y.state("late-wide").State == store.PreviewRefused
		})
	})
}

func TestCrashLoopEndsCrashed(t *testing.T) {
	y := newYard(t)
	y.run(t)
	port := freePort(t)
	y.propose(t, "flap", "echo crashing; exit 1", port, true)
	waitFor(t, "preview crashed after five exits", func() bool {
		return y.state("flap").State == store.PreviewCrashed
	})
	if got := y.state("flap"); got.RestartCount < 3 {
		t.Fatalf("restarts = %d, want the backoff restarts recorded", got.RestartCount)
	}
	lines, err := LogTail(filepath.Join(y.home, "previews", "flap.log"), 100)
	if err != nil || len(lines) == 0 || !strings.Contains(strings.Join(lines, "\n"), "crashing") {
		t.Fatalf("crash log = %v, %v", lines, err)
	}

	t.Run("a stop survives an outstanding backoff", func(t *testing.T) {
		y := newYard(t)
		y.sup.Backoffs = []time.Duration{time.Hour}
		y.run(t)
		y.propose(t, "stoppable", "echo crashing; exit 1", freePort(t), true)
		waitFor(t, "first crash enters its backoff", func() bool {
			p := y.state("stoppable")
			return p.RestartCount == 1 && p.State == store.PreviewRunning
		})
		if _, err := y.st.PreviewStop(context.Background(), "stoppable", "builder"); err != nil {
			t.Fatal(err)
		}
		y.sup.restart(context.Background(), "stoppable")
		if got := y.state("stoppable"); got.State != store.PreviewStopped || y.sup.Running("stoppable") {
			t.Fatalf("stop lost to a delayed restart: %+v", got)
		}
	})
}

func TestCleanExitIsNotRestarted(t *testing.T) {
	y := newYard(t)
	y.run(t)
	port := freePort(t)
	y.propose(t, "done", "echo finished; exit 0", port, true)
	waitFor(t, "clean exit stopped", func() bool {
		got := y.state("done")
		return got.State == store.PreviewStopped && got.StateDetail == "exited"
	})
	if got := y.state("done"); got.RestartCount != 0 {
		t.Fatalf("clean exit restarted %d times", got.RestartCount)
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	y.propose(t, "orphan", "sleep 60 & echo $! > "+strconv.Quote(pidFile)+"; exit 0", freePort(t), true)
	waitFor(t, "clean exit stopped and killed its background child", func() bool {
		return y.state("orphan").State == store.PreviewStopped
	})
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read background child pid: %v", err)
	}
	child, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse background child pid %q: %v", data, err)
	}
	waitFor(t, "background child is gone", func() bool {
		return errors.Is(syscall.Kill(child, 0), syscall.ESRCH)
	})
}

func TestIdleStopAndRebootRule(t *testing.T) {
	if _, err := os.Stat(python3()); err != nil {
		t.Skip("python3 not available for the stand-in app")
	}
	y := newYard(t)
	port := freePort(t)
	y.propose(t, "demo", fmt.Sprintf("python3 -m http.server %d --bind 127.0.0.1", port), port, true)
	// Backdate the last request past the default idle bound once
	// running: the next pass owes it the idle stop.
	y.run(t)
	waitFor(t, "preview running", func() bool { return y.state("demo").State == store.PreviewRunning })
	old := time.Now().Add(-5 * time.Hour)
	if _, err := y.st.UpdatePreview(context.Background(), "demo", store.PreviewUpdate{LastRequest: &old}); err != nil {
		t.Fatal(err)
	}
	y.sup.mu.Lock()
	y.sup.inflight["demo"] = 1
	y.sup.mu.Unlock()
	y.sup.reconcile(context.Background(), false)
	if got := y.state("demo"); got.State != store.PreviewRunning {
		t.Fatalf("active response was stopped as idle: %+v", got)
	}
	y.sup.leave("demo")
	waitFor(t, "idle preview stopped", func() bool {
		got := y.state("demo")
		return got.State == store.PreviewStopped && got.StateDetail == "idle"
	})
	// A row left running when the supervisor starts is the reboot
	// rule: stopped, not restarted.
	y2 := newYard(t)
	y2.propose(t, "demo", "true", freePort(t), true)
	started := time.Now()
	if _, err := y2.st.UpdatePreview(context.Background(), "demo", store.PreviewUpdate{
		State: store.PreviewRunning, PID: 424242, StartedAt: &started}); err != nil {
		t.Fatal(err)
	}
	y2.run(t)
	waitFor(t, "reboot-stopped preview", func() bool {
		got := y2.state("demo")
		return got.State == store.PreviewStopped && got.StateDetail == "reboot"
	})
}

// A restart the person asks for on the card stops the running process
// and starts the approved form again: the ask is a stopped row the
// supervisor flips back to approved, and the next pass starts it. A stop
// that wins while the process stops stands instead of the restart.
func TestRestartAskStopsAndStartsAgain(t *testing.T) {
	if _, err := os.Stat(python3()); err != nil {
		t.Skip("python3 not available for the stand-in app")
	}
	ctx := context.Background()
	y := newYard(t)
	y.run(t)
	port := freePort(t)
	y.propose(t, "demo", fmt.Sprintf("python3 -m http.server %d --bind 127.0.0.1", port), port, true)
	waitFor(t, "preview running", func() bool { return y.state("demo").State == store.PreviewRunning })
	first := y.state("demo").PID
	if _, err := y.st.RestartPreview(ctx, "demo", "ada"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "restarted preview running", func() bool {
		got := y.state("demo")
		return got.State == store.PreviewRunning && got.PID != 0 && got.PID != first
	})
	if got := y.state("demo"); got.StateDetail != "" {
		t.Fatalf("after restart detail = %q", got.StateDetail)
	}
	// The completion reads the row again: a stop that won while the
	// process stopped stands.
	y2 := newYard(t)
	y2.propose(t, "held", "true", freePort(t), true)
	if _, err := y2.st.RestartPreview(ctx, "held", "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := y2.st.PreviewStop(ctx, "held", "ada"); err != nil {
		t.Fatal(err)
	}
	y2.sup.finishRestart(ctx, "held")
	if got := y2.state("held"); got.State != store.PreviewStopped || got.StateDetail != "stopped" {
		t.Fatalf("after winning stop = %+v", got)
	}
}

func TestMemoryWatcherStopsOverCap(t *testing.T) {
	if _, err := os.Stat(python3()); err != nil {
		t.Skip("python3 not available for the stand-in app")
	}
	y := newYard(t)
	y.sup.MemLimit = 1 // every process is over a one-byte cap
	y.sup.MemoryTick = 10 * time.Millisecond
	y.sup.MemoryHold = 20 * time.Millisecond
	y.run(t)
	port := freePort(t)
	y.propose(t, "demo", fmt.Sprintf("python3 -m http.server %d --bind 127.0.0.1", port), port, true)
	waitFor(t, "preview stopped for memory", func() bool {
		got := y.state("demo")
		return got.State == store.PreviewStopped && got.StateDetail == "memory"
	})
}

func TestOnDemandStartThroughEndpoint(t *testing.T) {
	if _, err := os.Stat(python3()); err != nil {
		t.Skip("python3 not available for the stand-in app")
	}
	y := newYard(t) // no Run: the request alone must start it
	t.Cleanup(func() { y.sup.kill("demo") })
	dir, git := gitTree(t)
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("hello preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "index.html")
	git("commit", "-q", "-m", "index")
	port := freePort(t)
	y.proposeIn(t, "demo", fmt.Sprintf("python3 -m http.server %d --bind 127.0.0.1", port), dir, port, true)
	srv := httptest.NewServer(y.sup.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/preview/demo/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "hello preview") {
		t.Fatalf("on-demand answer = %d %s", resp.StatusCode, raw)
	}
	if got := y.state("demo"); got.State != store.PreviewRunning || got.LastRequestAt == nil {
		t.Fatalf("after on-demand start = %+v", got)
	}
	if _, err := y.st.PreviewStop(context.Background(), "demo", "ada"); err != nil {
		t.Fatal(err)
	}
	y.sup.kill("demo")
	git("commit", "-q", "--allow-empty", "-m", "two")
	resp, err = http.Get(srv.URL + "/preview/demo/")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(raw), "approval is taken back") {
		t.Fatalf("on-demand start on another tree = %d %s, want refusal", resp.StatusCode, raw)
	}
	got := y.state("demo")
	if got.State != store.PreviewPending || got.StateDetail != store.PreviewTreeChangedDetail || got.Approval != nil || got.Commit != git("rev-parse", "HEAD") {
		t.Fatalf("after refused on-demand start = %+v, want pending at the new tree without approval", got)
	}
	if y.sup.Running("demo") || listeningAddress(port) != "" {
		t.Fatal("on-demand start ran the command on another tree")
	}
	if _, err := y.st.ApprovePreview(context.Background(), "demo", "ada"); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(srv.URL + "/preview/demo/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "hello preview") {
		t.Fatalf("on-demand answer after a new approval = %d %s", resp.StatusCode, raw)
	}
}

func TestInFlightCap(t *testing.T) {
	y := newYard(t)
	for i := 0; i < store.PreviewMaxInFlight; i++ {
		if !y.sup.enter("demo") {
			t.Fatalf("enter %d refused under the cap", i)
		}
	}
	if y.sup.enter("demo") {
		t.Fatal("21st request in flight accepted")
	}
	y.sup.leave("demo")
	if !y.sup.enter("demo") {
		t.Fatal("request after one left refused")
	}
}

func python3() string {
	for _, p := range []string{"/usr/local/bin/python3", "/usr/bin/python3", "/opt/homebrew/bin/python3"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "python3"
}
