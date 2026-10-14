package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
	_ "yardr.dev/yardr/internal/storetest" // the tests' stores do not wait for the disk
	"yardr.dev/yardr/internal/version"
)

// home is a yard home short enough for its unix socket.
func home(t *testing.T) *config.Config {
	t.Helper()
	dir, err := os.MkdirTemp("", "ysv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return &config.Config{Home: dir}
}

var installed = time.Now()

// install puts a "binary" whose contents are its revision at path, the way
// go install does: a new file renamed over the old one. Each install is
// stamped a second after the last, so mtime orders them: only a newer build
// replaces.
func install(t *testing.T, path, rev string) {
	t.Helper()
	installed = installed.Add(time.Second)
	installAt(t, path, rev, installed)
}

// installAt is install with the build's mtime given. The time is set before
// the rename, so a server checking its binary never sees the file with any
// other.
func installAt(t *testing.T, path, rev string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path+".tmp", []byte(rev), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path+".tmp", mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

// execs records what a fake exec was asked to run.
type execs struct {
	mu    sync.Mutex
	calls [][]string // env of each call
}

func (e *execs) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

// binary watches a fake binary running rev; exec is called in place of
// syscall.Exec.
func binary(t *testing.T, rev string, exec func(env []string) error) (*version.Binary, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "yardr")
	install(t, path, rev)
	b, err := version.Watch(path, rev)
	if err != nil {
		t.Fatal(err)
	}
	b.ReadBuild = func(p string) (version.Build, error) {
		data, err := os.ReadFile(p)
		return version.Build{Revision: string(data)}, err
	}
	b.Exec = func(_ string, _, env []string) error { return exec(env) }
	return b, path
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// start runs a server for c until the test ends; it returns run's result.
func start(t *testing.T, r *runner) <-chan error {
	t.Helper()
	done, _ := startStoppable(t, r)
	return done
}

// startStoppable is start, also returning the cancel that stops the server
// the way SIGTERM does.
func startStoppable(t *testing.T, r *runner) (<-chan error, context.CancelFunc) {
	t.Helper()
	if r.log == nil {
		r.log = quiet
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		done <- r.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("server did not stop")
		}
	})
	waitUp(t, r.c)
	return done, cancel
}

func waitUp(t *testing.T, c *config.Config) {
	t.Helper()
	cl := rpc.NewClient(c.Socket())
	waitFor(t, "server did not come up", func() bool { return cl.Ping(context.Background()) == nil })
}

// waitFor polls until cond holds, and fails the test with what when it never
// does. A passing test never waits the deadline out, so it is sized for the
// whole suite, where every package tests at once under -race and a spawned
// process can take seconds to start, not for an idle machine.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

// accepts reports whether the server's socket still takes a connection.
func accepts(c *config.Config) bool {
	conn, err := net.Dial("unix", c.Socket())
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func client(c *config.Config, rev string) *rpc.Client {
	cl := rpc.NewClient(c.Socket())
	cl.Revision = rev
	return cl
}

// envOf returns KEY's value in env.
func envOf(env []string, key string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

func TestNewerClientReplacesTheServer(t *testing.T) {
	c := home(t)
	var ex execs
	var next <-chan error
	var path string
	var bin *version.Binary
	bin, path = binary(t, "old", func(env []string) error {
		ex.mu.Lock()
		ex.calls = append(ex.calls, env)
		ex.mu.Unlock()
		// The store must be closed before the new build opens it.
		st, err := store.Open(c.DBPath())
		if err != nil {
			t.Errorf("store still open at exec: %v", err)
			return err
		}
		st.Close(context.Background())
		// Stand in for the exec'd process: the new build, started with the
		// environment it was given.
		t.Setenv("YARDR_REPLACED_FROM", envOf(env, "YARDR_REPLACED_FROM"))
		nb, _ := binary(t, "new", func([]string) error { return errors.New("no exec") })
		next = start(t, &runner{c: c, bin: nb, rev: "new"})
		return nil
	})
	done := start(t, &runner{c: c, bin: bin, rev: "old"})
	head, err := client(c, "old").Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	install(t, path, "new")
	if _, err := client(c, "new").Head(context.Background()); err != nil {
		t.Fatalf("call across the replace: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("old server: %v", err)
	}
	if ex.count() != 1 {
		t.Fatalf("exec calls = %d", ex.count())
	}
	if from := envOf(ex.calls[0], "YARDR_REPLACED_FROM"); from != "old" {
		t.Fatalf("exec env YARDR_REPLACED_FROM = %q", from)
	}
	if b, _ := os.ReadFile(c.PIDFile()); strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("pidfile = %q", b)
	}
	evs, err := client(c, "new").EventsSince(context.Background(), head, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != "server_replaced" || evs[0].Data["from"] != "old" || evs[0].Data["to"] != "new" {
		t.Fatalf("events after the replace = %+v", evs)
	}
	if v := os.Getenv("YARDR_REPLACED_FROM"); v != "" {
		t.Fatalf("YARDR_REPLACED_FROM left set (%q); agents would inherit it", v)
	}
	select {
	case err := <-next:
		t.Fatalf("new server exited: %v", err)
	default:
	}
}

func TestOlderClientDoesNotReplace(t *testing.T) {
	c := home(t)
	var ex atomic.Int32
	bin, _ := binary(t, "new", func([]string) error { ex.Add(1); return nil })
	start(t, &runner{c: c, bin: bin, rev: "new"})

	if _, err := client(c, "old").Head(context.Background()); err != nil {
		t.Fatalf("older client not served: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if ex.Load() != 0 {
		t.Fatal("an older client made the server exec (downgrade)")
	}
}

func TestOlderClientDoesNotTriggerAnUpgrade(t *testing.T) {
	c := home(t)
	var ex atomic.Int32
	bin, path := binary(t, "old", func([]string) error { ex.Add(1); return nil })
	start(t, &runner{c: c, bin: bin, rev: "old"})

	install(t, path, "new")
	if _, err := client(c, "older").Head(context.Background()); err != nil {
		t.Fatalf("older client should still be served: %v", err)
	}
	if ex.Load() != 0 {
		t.Fatal("an older client triggered an upgrade")
	}

	newer := client(c, "new")
	newer.Retry = 0
	if _, err := newer.Head(context.Background()); err == nil || !strings.Contains(err.Error(), "replacing") {
		t.Fatalf("newer client did not trigger replacement: %v", err)
	}
}

// A program that is not yardr (docs/api.md) is served whatever the server's
// build, and never is a reason to replace it: one that sends no revision,
// one that names itself, and one whose revision is no build of yardr, also
// while a newer yardr is installed and waits for a caller of its own.
func TestAClientThatIsNotYardrIsServed(t *testing.T) {
	c := home(t)
	var ex atomic.Int32
	bin, path := binary(t, "old", func([]string) error { ex.Add(1); return nil })
	start(t, &runner{c: c, bin: bin, rev: "old"})
	install(t, path, "new")

	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket())
	}}}
	for _, headers := range []map[string]string{
		{},
		{rpc.ClientHeader: "footplate/0.1"},
		{rpc.ClientHeader: "footplate/0.1", version.Header: "footplate/0.1"},
	} {
		req, _ := http.NewRequest(http.MethodPost, "http://yard/call/Users", strings.NewReader("{}"))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatalf("%v: %v", headers, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != `{"result":[]}` {
			t.Errorf("%v: status %s, reply %s; want the yard's users", headers, resp.Status, body)
		}
		if resp.Header.Get(version.Header) != "old" || resp.Header.Get(rpc.PIDHeader) != strconv.Itoa(os.Getpid()) {
			t.Errorf("%v: reply headers: revision %q pid %q", headers, resp.Header.Get(version.Header), resp.Header.Get(rpc.PIDHeader))
		}
	}
	if ex.Load() != 0 {
		t.Fatal("a client that is not yardr made the server replace itself")
	}
}

func TestOlderInstallIsSkippedAndLogged(t *testing.T) {
	// A go install of an older checkout (a stale bead worktree, say) over
	// the running server must not downgrade it, even when the caller is
	// that older build (yard-r4z9).
	c := home(t)
	var ex atomic.Int32
	bin, path := binary(t, "new", func([]string) error { ex.Add(1); return nil })
	start(t, &runner{c: c, bin: bin, rev: "new", check: 20 * time.Millisecond})
	head, err := client(c, "new").Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	installAt(t, path, "old", time.Now().Add(-time.Hour))
	if _, err := client(c, "old").Head(context.Background()); err != nil {
		t.Fatalf("the older client should be served: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // several backstop checks
	if ex.Load() != 0 {
		t.Fatal("an older build was exec'd")
	}
	evs, err := client(c, "new").EventsSince(context.Background(), head, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Kind != "replace_skipped" || evs[0].Data["candidate"] != "old" ||
		!strings.HasPrefix(fmt.Sprint(evs[0].Data["reason"]), "built ") {
		t.Fatalf("events = %+v; want one replace_skipped naming why", evs)
	}
}

func TestInFlightCallCompletesAcrossReplace(t *testing.T) {
	c := home(t)
	release := make(chan struct{})
	started := make(chan struct{})
	slowDone := make(chan error, 1)
	var execd atomic.Bool
	bin, path := binary(t, "old", func([]string) error {
		execd.Store(true)
		select {
		case err := <-slowDone:
			if err != nil {
				t.Errorf("in-flight call: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("in-flight call had no reply by exec")
		}
		return nil
	})
	wrap := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Slow") != "" {
				close(started)
				<-release
			}
			h.ServeHTTP(w, r)
		})
	}
	done := start(t, &runner{c: c, bin: bin, rev: "old", wrap: wrap})

	go func() {
		hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket())
		}}}
		req, _ := http.NewRequest(http.MethodPost, "http://yard/call/Head", strings.NewReader("[]"))
		req.Header.Set("X-Slow", "1")
		resp, err := hc.Do(req)
		if err != nil {
			slowDone <- err
			return
		}
		defer resp.Body.Close()
		var r struct {
			Result *uint64 `json:"result"`
			Error  any     `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			slowDone <- err
		} else if r.Result == nil || r.Error != nil {
			slowDone <- errors.New("no result")
		} else {
			slowDone <- nil
		}
	}()
	<-started

	install(t, path, "new")
	newer := client(c, "new")
	newer.Retry = 0
	if _, err := newer.Head(context.Background()); err == nil || !strings.Contains(err.Error(), "replacing") {
		t.Fatalf("newer client's call during the replace: %v", err)
	}
	// The server stops accepting while the slow call is still running...
	waitFor(t, "server kept accepting after the replace began", func() bool { return !accepts(c) })
	time.Sleep(50 * time.Millisecond)
	if execd.Load() {
		t.Fatal("exec'd before the in-flight call finished")
	}
	// ...and execs only once it is answered.
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !execd.Load() {
		t.Fatal("no exec")
	}
}

func TestServerChecksItsBinaryWithoutClients(t *testing.T) {
	c := home(t)
	execd := make(chan struct{})
	bin, path := binary(t, "old", func([]string) error { close(execd); return nil })
	done := start(t, &runner{c: c, bin: bin, rev: "old", check: 20 * time.Millisecond})
	install(t, path, "new")
	select {
	case <-execd:
	case <-time.After(5 * time.Second):
		t.Fatal("the backstop never replaced the server")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFailedExecKeepsServingAndExplainsSkew(t *testing.T) {
	c := home(t)
	var ex atomic.Int32
	bin, path := binary(t, "old", func([]string) error { ex.Add(1); return errors.New("exec format error") })
	start(t, &runner{c: c, bin: bin, rev: "old"})

	install(t, path, "new")
	newer := client(c, "new")
	if _, err := newer.Head(context.Background()); err != nil {
		t.Fatalf("after a failed exec the old build should serve on: %v", err)
	}
	if ex.Load() != 1 {
		t.Fatalf("exec calls = %d; a failed binary must not be retried", ex.Load())
	}
	// A method only the new build has: the client names the skew.
	req, _ := http.NewRequest(http.MethodPost, "http://yard/call/OnlyInNew", strings.NewReader("[]"))
	req.Header.Set(version.Header, "new")
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket())
	}}}
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get(version.Header) != "old" || resp.Header.Get(rpc.PIDHeader) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("reply headers: revision %q pid %q", resp.Header.Get(version.Header), resp.Header.Get(rpc.PIDHeader))
	}
	if ex.Load() != 1 {
		t.Fatalf("exec calls = %d", ex.Load())
	}
}

func TestConnectWaitsForAServerBetweenBuilds(t *testing.T) {
	c := home(t)
	// The pidfile names a live process (us) but nothing answers yet, as
	// while a server execs; Connect must wait, not start a second server.
	if err := os.WriteFile(c.PIDFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The test ends only once start has seen its server up: Connect can
	// return on the server's first answer, and a test that ended there
	// stopped the server before start's own ping, which then failed the
	// finished test five seconds later (yardr-ep5y).
	started := make(chan struct{})
	defer func() { <-started }()
	go func() {
		defer close(started)
		time.Sleep(300 * time.Millisecond)
		start(t, &runner{c: c, rev: "new"})
	}()
	if _, err := Connect(context.Background(), c); err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

// A connect the system does not permit (a sandbox that denies unix sockets,
// a socket this user may not write) fails at once with the socket and the
// reason. Connect used to take it for no server: it waited out a live pid,
// then started a server of its own (yardr-v44w).
func TestConnectDeniedFailsAtOnce(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root connects whatever the socket's mode")
	}
	c := home(t)
	ln, err := net.Listen("unix", c.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := os.Chmod(c.Socket(), 0); err != nil {
		t.Fatal(err)
	}
	// A live pid, which Connect would wait on for as long as a call retries.
	if err := os.WriteFile(c.PIDFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := executable
	executable = func() (string, error) {
		t.Error("Connect went on to start a server")
		return "", errors.New("no server in this test")
	}
	defer func() { executable = old }()

	began := time.Now()
	_, err = Connect(context.Background(), c)
	if err == nil {
		t.Fatal("Connect: no error")
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("Connect took %s", took)
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Errorf("Connect: %v, want EACCES", err)
	}
	for _, want := range []string{c.Socket(), "permission denied", "outside"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Connect: %q does not say %q", err, want)
		}
	}
}

// The build a server execs writes its pid (the same one) over the old
// build's pidfile while Connect reads it to decide between waiting and
// starting a server. A reader must never find the file empty or half
// written: that reads as no server, and Connect starts a second one
// (yardr-ep5y).
func TestPIDFileIsNeverSeenHalfWritten(t *testing.T) {
	c := home(t)
	if err := writePID(c); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	written := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				written <- nil
				return
			default:
			}
			if err := writePID(c); err != nil {
				written <- err
				return
			}
		}
	}()
	for i := 0; i < 20000; i++ {
		if !alive(c) {
			_, err := readPID(c)
			close(stop)
			<-written
			t.Fatalf("read %d: the pidfile named no live process while it was rewritten (%v)", i, err)
		}
	}
	close(stop)
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

// slowCall wraps the server's handler so a call marked X-Slow waits for
// release before it reaches the store, and reports through answered whether
// the store answered it (the client's connection may be gone by then).
func slowCall(started, release chan struct{}, answered chan<- bool) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Slow") == "" {
				h.ServeHTTP(w, r)
				return
			}
			close(started)
			<-release
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			var reply struct {
				Result *uint64 `json:"result"`
				Error  any     `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &reply)
			answered <- reply.Result != nil && reply.Error == nil
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
		})
	}
}

// sendSlow starts a slow Head call on c's socket; its result is not waited for.
func sendSlow(c *config.Config) {
	go func() {
		hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", c.Socket())
		}}}
		req, _ := http.NewRequest(http.MethodPost, "http://yard/call/Head", strings.NewReader("[]"))
		req.Header.Set("X-Slow", "1")
		if resp, err := hc.Do(req); err == nil {
			resp.Body.Close()
		}
	}()
}

// A call still running when the shutdown grace runs out keeps the store until
// it finishes (bounded by settle): the store is not closed under it.
func TestStoreOutlivesCallsPastTheShutdownGrace(t *testing.T) {
	c := home(t)
	started, release, answered := make(chan struct{}), make(chan struct{}), make(chan bool, 1)
	done, stop := startStoppable(t, &runner{c: c, rev: "old", grace: 50 * time.Millisecond, settle: 5 * time.Second,
		wrap: slowCall(started, release, answered)})
	sendSlow(c)
	<-started
	stop()
	select {
	case err := <-done:
		t.Fatalf("server returned (%v) with a call still running", err)
	case <-time.After(300 * time.Millisecond): // well past the grace
	}
	close(release)
	if !<-answered {
		t.Fatal("the in-flight call found the store closed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop once the call finished")
	}
}

// A stop that arrives while the server drains for a replace stops it: the
// new build is not exec'd, and the socket and pidfile go.
func TestStopDuringReplaceDrainIsHonoured(t *testing.T) {
	c := home(t)
	var execd atomic.Bool
	bin, path := binary(t, "old", func([]string) error {
		execd.Store(true)
		return nil
	})
	started, release, answered := make(chan struct{}), make(chan struct{}), make(chan bool, 1)
	done, stop := startStoppable(t, &runner{c: c, bin: bin, rev: "old", wrap: slowCall(started, release, answered)})
	sendSlow(c)
	<-started

	install(t, path, "new")
	newer := client(c, "new")
	newer.Retry = 0
	if _, err := newer.Head(context.Background()); err == nil || !strings.Contains(err.Error(), "replacing") {
		t.Fatalf("newer client's call: %v", err)
	}
	// Draining: the listener is closed while the slow call runs.
	waitFor(t, "server kept accepting after the replace began", func() bool { return !accepts(c) })
	stop()
	close(release)
	<-answered
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop")
	}
	if execd.Load() {
		t.Fatal("exec'd the new build after being told to stop")
	}
	if _, err := os.Stat(c.PIDFile()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pidfile left behind: %v", err)
	}
}

// A client keeps retrying, and Connect keeps waiting for a live pid, for
// longer than the server's drain can last, so a call issued during a
// replace is answered by the new build rather than failed, and no second
// server is started against a store the first still holds.
func TestClientOutwaitsTheDrain(t *testing.T) {
	retry := rpc.NewClient("").Retry
	if drain := drainGrace + drainSettle; retry <= drain {
		t.Fatalf("client retry %v does not outlast the drain (%v + %v)", retry, drainGrace, drainSettle)
	}
	if retry < 2*drainSettle {
		t.Fatalf("client retry %v leaves the new build no time to start and migrate", retry)
	}
}

// A call issued while the server drains for a replace (a slow call still
// in flight past the shutdown grace) is answered by the new build, through
// Connect, which waits for the live pid instead of starting a second server.
func TestCallDuringReplaceLandsOnTheNewBuild(t *testing.T) {
	c := home(t)
	var ex execs
	var next <-chan error
	var path string
	var bin *version.Binary
	bin, path = binary(t, "old", func(env []string) error {
		ex.mu.Lock()
		ex.calls = append(ex.calls, env)
		ex.mu.Unlock()
		t.Setenv("YARDR_REPLACED_FROM", envOf(env, "YARDR_REPLACED_FROM"))
		nb, _ := binary(t, "new", func([]string) error { return errors.New("no exec") })
		next = start(t, &runner{c: c, bin: nb, rev: "new"})
		return nil
	})
	started, release, answered := make(chan struct{}), make(chan struct{}), make(chan bool, 1)
	done := start(t, &runner{c: c, bin: bin, rev: "old", grace: 50 * time.Millisecond,
		wrap: slowCall(started, release, answered)})
	head, err := client(c, "old").Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sendSlow(c)
	<-started

	install(t, path, "new")
	newer := client(c, "new")
	newer.Retry = 0
	if _, err := newer.Head(context.Background()); err == nil || !strings.Contains(err.Error(), "replacing") {
		t.Fatalf("newer client's call: %v", err)
	}
	// Draining, with the slow call holding the store past the grace.
	waitFor(t, "server kept accepting after the replace began", func() bool { return !accepts(c) })
	go func() {
		time.Sleep(time.Second)
		close(release)
	}()
	// What an agent's command does meanwhile: connect, then call.
	cl, err := Connect(context.Background(), c)
	if err != nil {
		t.Fatalf("Connect during the drain: %v", err)
	}
	evs, err := cl.EventsSince(context.Background(), head, 0)
	if err != nil {
		t.Fatalf("call during the drain: %v", err)
	}
	if !<-answered {
		t.Fatal("the slow call found the store closed")
	}
	if err := <-done; err != nil {
		t.Fatalf("old server: %v", err)
	}
	if ex.count() != 1 {
		t.Fatalf("exec calls = %d", ex.count())
	}
	if len(evs) != 1 || evs[0].Kind != "server_replaced" {
		t.Fatalf("the call was answered by %+v; want the new build's server_replaced", evs)
	}
	if _, err := os.Stat(c.LogFile()); err == nil {
		t.Fatal("Connect started a second server")
	}
	select {
	case err := <-next:
		t.Fatalf("new server exited: %v", err)
	default:
	}
}

// ServerEnv keeps PATH, HOME and the yardr knobs, replaces the home with
// the one served, and drops everything else: a credential from a
// crew member's pane, the starter's identity, the shell's own.
func TestServerEnv(t *testing.T) {
	got := ServerEnv([]string{"PATH=/bin", "HOME=/Users/jo", "GITHUB_TOKEN=ghp_x", "LINEAR_API_KEY=lin_x",
		"YARDR_HOME=y1", "YARDR_ACTOR=yardmaster", "YARDR_CREW=yardmaster", "YARDR_BEAD=web-3",
		"YARDR_SESSION=s1", "YARDR_STAGE=new", "YARDR_GROUP=g", "YARDR_BIN=/bin/yardr",
		"YARDR_HOOK_ADDR=127.0.0.1:1", "YARDR_WEB_ADDR=127.0.0.1:2", "YARDR_WEB_AS=jo", "YARDR_BDP_ADDR=127.0.0.1:3",
		"YARDR_EVENT_RETENTION=1d", "YARDR_TEST_RPC=1", "YARDR_BASE=main",
		"YARDR_HOOK_PAYLOAD_RETENTION=2d", "YARDR_PPROF=/y1/pprof.sock", "SHELL=/bin/zsh", "TERM=xterm"}, "/y1")
	want := []string{"PATH=/bin", "HOME=/Users/jo", "YARDR_HOOK_ADDR=127.0.0.1:1", "YARDR_WEB_ADDR=127.0.0.1:2",
		"YARDR_WEB_AS=jo", "YARDR_BDP_ADDR=127.0.0.1:3", "YARDR_EVENT_RETENTION=1d",
		"YARDR_HOOK_PAYLOAD_RETENTION=2d", "YARDR_PPROF=/y1/pprof.sock", "YARDR_HOME=/y1"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ServerEnv =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The detached server gets that environment and runs in the home, whatever
// the command that started it had: the first command on a yard is as often
// an agent's, in a pane holding its credentials and a worktree, as the
// operator's. A script stands in for the binary and records what it got;
// a server already on the socket answers Start's ping, so it need not serve.
func TestStartGivesTheServerItsOwnEnvAndDir(t *testing.T) {
	c := home(t)
	start(t, &runner{c: c, rev: "x"})
	dump := filepath.Join(c.Home, "env")
	script := filepath.Join(t.TempDir(), "yardr")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n{ pwd -P; echo ARGS=\"$*\"; env; } > "+dump+".tmp && mv "+dump+".tmp "+dump+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := executable
	executable = func() (string, error) { return script, nil }
	t.Cleanup(func() { executable = old })
	t.Setenv("GITHUB_TOKEN", "ghp_secret")
	t.Setenv("YARDR_ACTOR", "yardmaster")
	t.Setenv("YARDR_CREW", "yardmaster")
	t.Setenv("YARDR_HOME", "elsewhere")
	// The settings the server reads are the operator's to give it where
	// they start it: a session or a script is started without them
	// (config.WithoutServerSettings), the yard's own server is not.
	t.Setenv("YARDR_HOOK_ADDR", "127.0.0.1:1")
	t.Setenv("YARDR_EVENT_RETENTION", "1d")
	t.Setenv("YARDR_HOOK_PAYLOAD_RETENTION", "2d")
	t.Setenv("YARDR_PPROF", "/y1/pprof.sock")
	t.Chdir(t.TempDir())
	if err := Start(context.Background(), c); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var lines []string
	waitFor(t, "the stand-in server never ran", func() bool {
		raw, err := os.ReadFile(dump)
		if err != nil {
			return false
		}
		lines = strings.Split(strings.TrimSpace(string(raw)), "\n")
		return true
	})
	wantDir, _ := filepath.EvalSymlinks(c.Home)
	if lines[0] != wantDir {
		t.Errorf("server cwd = %s, want %s", lines[0], wantDir)
	}
	if lines[1] != "ARGS=server run" {
		t.Errorf("%s", lines[1])
	}
	env := lines[2:]
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		// The settings are asked of config, not listed: the test's own
		// environment has each of them, blank, when a session runs it
		// (config.BlankServerSettings), whichever this test sets.
		switch {
		case k == "PATH", k == "HOME", config.IsVar(k, "HOME"), config.IsVar(k, config.ServerSettings...):
		case k == "PWD", k == "OLDPWD", k == "SHLVL", k == "_": // the shell's own
		default:
			t.Errorf("server got %s", kv)
		}
	}
	for _, kv := range []string{"YARDR_HOME=" + c.Home, "YARDR_HOOK_ADDR=127.0.0.1:1",
		"YARDR_EVENT_RETENTION=1d", "YARDR_HOOK_PAYLOAD_RETENTION=2d", "YARDR_PPROF=/y1/pprof.sock"} {
		if !slices.Contains(env, kv) {
			t.Errorf("server lacks %s:\n%s", kv, strings.Join(env, "\n"))
		}
	}
	if envOf(env, "PATH") == "" || envOf(env, "HOME") == "" {
		t.Errorf("server lacks PATH or HOME:\n%s", strings.Join(env, "\n"))
	}
}

// Describe says which process and build answers on the home's socket and
// since when, for yardr status; with no server it says so and starts none.
func TestDescribe(t *testing.T) {
	c := home(t)
	if info, ok := Describe(context.Background(), c); ok {
		t.Fatalf("no server runs, and one is described: %+v", info)
	}
	if _, err := os.Stat(c.Socket()); err == nil {
		t.Fatal("Describe started a server")
	}
	before := time.Now().Add(-time.Second)
	start(t, &runner{c: c, rev: "abcdef123456"})
	info, ok := Describe(context.Background(), c)
	if !ok || info.PID != os.Getpid() || info.Revision != "abcdef123456" || info.Since.Before(before) || info.Since.After(time.Now().Add(time.Second)) {
		t.Fatalf("ok = %v, info = %+v", ok, info)
	}
	// A pidfile that names another process says nothing of this server.
	if err := os.WriteFile(c.PIDFile(), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, ok := Describe(context.Background(), c); !ok || info.PID != os.Getpid() || !info.Since.IsZero() {
		t.Fatalf("ok = %v, info = %+v", ok, info)
	}
}

// DescribeErr says why no server answers, for the upgrade check's fail
// line, and starts none.
func TestDescribeErr(t *testing.T) {
	c := home(t)
	if _, err := DescribeErr(context.Background(), c); err == nil {
		t.Fatal("no server runs, and no error says so")
	} else if !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("err = %v, want the missing socket", err)
	}
	if _, err := os.Stat(c.Socket()); err == nil {
		t.Fatal("DescribeErr started a server")
	}
	start(t, &runner{c: c, rev: "abcdef123456"})
	if info, err := DescribeErr(context.Background(), c); err != nil || info.Revision != "abcdef123456" {
		t.Fatalf("info = %+v, err = %v", info, err)
	}
}
