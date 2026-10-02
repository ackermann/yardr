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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
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
	if err := os.WriteFile(path+".tmp", []byte(rev), 0o755); err != nil {
		t.Fatal(err)
	}
	installed = installed.Add(time.Second)
	if err := os.Chtimes(path+".tmp", installed, installed); err != nil {
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
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cl.Ping(context.Background()) == nil {
			return
		}
	}
	t.Fatal("server did not come up")
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

	install(t, path, "old")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
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
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		conn, err := net.Dial("unix", c.Socket())
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("server kept accepting after the replace began")
		}
	}
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
	go func() {
		time.Sleep(300 * time.Millisecond)
		start(t, &runner{c: c, rev: "new"})
	}()
	if _, err := Connect(context.Background(), c); err != nil {
		t.Fatalf("Connect: %v", err)
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
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		conn, err := net.Dial("unix", c.Socket())
		if err != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatal("server kept accepting after the replace began")
		}
	}
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
