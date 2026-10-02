package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
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

// install puts a "binary" whose contents are its revision at path, the way
// go install does: a new file renamed over the old one.
func install(t *testing.T, path, rev string) {
	t.Helper()
	if err := os.WriteFile(path+".tmp", []byte(rev), 0o755); err != nil {
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
	b.ReadRevision = func(p string) (string, error) {
		data, err := os.ReadFile(p)
		return string(data), err
	}
	b.Exec = func(_ string, _, env []string) error { return exec(env) }
	return b, path
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// start runs a server for c until the test ends; it returns run's result.
func start(t *testing.T, r *runner) <-chan error {
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
	return done
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
