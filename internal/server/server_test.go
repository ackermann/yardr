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
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/source"
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

// A build made without a VCS stamp carries no revision and is newer by its
// file's time alone. It is replaced like any other: it was taken for a stop
// (the revision to exec was "", which serve returned for a stop too), so the
// server drained, removed its socket and pidfile, and ended, without a line
// in its log or an exec, and nothing started another.
func TestServerChecksItsBinaryWithoutClients(t *testing.T) {
	for _, tc := range []struct{ name, old, new string }{
		{"with a revision", "old", "new"},
		{"without a revision", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := home(t)
			execd := make(chan []string, 1)
			bin, path := binary(t, tc.old, func(env []string) error { execd <- env; return nil })
			done := start(t, &runner{c: c, bin: bin, rev: tc.old, check: 20 * time.Millisecond})
			install(t, path, tc.new)
			select {
			case env := <-execd:
				if from := envOf(env, "YARDR_REPLACED_FROM"); from != version.Show(tc.old) {
					t.Fatalf("exec env YARDR_REPLACED_FROM = %q, want %q", from, version.Show(tc.old))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the backstop never replaced the server")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			// The pidfile stays across the exec, for the clients that wait
			// for the new build by it.
			if b, _ := os.ReadFile(c.PIDFile()); strings.TrimSpace(string(b)) != strconv.Itoa(os.Getpid()) {
				t.Fatalf("pidfile after the replace = %q", b)
			}
		})
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
	// With our start time recorded, so the pid reads as the server's.
	if st, err := peer.Describe(os.Getpid()); err == nil {
		if err := os.WriteFile(c.PIDStart(), []byte(strconv.FormatUint(st.Start, 10)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
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
	t.Run("preview processes are stopped before the store closes", func(t *testing.T) {
		c := home(t)
		ctx := context.Background()
		st, err := store.Open(c.DBPath())
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetSetting(ctx, store.OperatorSetting, "ada", "ada"); err != nil {
			t.Fatal(err)
		}
		port, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		previewPort := port.Addr().(*net.TCPAddr).Port
		port.Close()
		dir := t.TempDir()
		pidPath := filepath.Join(dir, "pid")
		command := fmt.Sprintf("trap '' TERM; echo $$ > %q; exec sleep 60", pidPath)
		if _, err := st.ProposePreview(ctx, store.NewPreview{
			App: "demo", Command: command, Dir: dir, Port: previewPort,
		}, "builder"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ApprovePreview(ctx, "demo", "ada"); err != nil {
			t.Fatal(err)
		}
		if err := st.Close(ctx); err != nil {
			t.Fatal(err)
		}
		done, stop := startStoppable(t, &runner{c: c, rev: "old"})
		pid := 0
		waitFor(t, "approved preview started", func() bool {
			b, err := os.ReadFile(pidPath)
			if err != nil {
				return false
			}
			pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
			return err == nil && pid > 0
		})
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("server returned before stopping its preview")
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("preview process %d still exists after server stop: %v", pid, err)
		}
	})
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
	if _, err := os.Stat(c.PIDStart()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("start file left behind: %v", err)
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
	ctx := context.Background()
	st, err := store.Open(c.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	port, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	previewPort := port.Addr().(*net.TCPAddr).Port
	port.Close()
	dir := t.TempDir()
	previewPIDPath := filepath.Join(dir, "preview.pid")
	previewCommand := fmt.Sprintf("trap '' TERM; echo $$ > %q; exec sleep 60", previewPIDPath)
	if _, err := st.ProposePreview(ctx, store.NewPreview{
		App: "demo", Command: previewCommand, Dir: dir, Port: previewPort,
	}, "builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApprovePreview(ctx, "demo", "ada"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(ctx); err != nil {
		t.Fatal(err)
	}
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
	previewPID := 0
	waitFor(t, "preview process started before replacement", func() bool {
		b, err := os.ReadFile(previewPIDPath)
		if err != nil {
			return false
		}
		previewPID, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && previewPID > 0
	})
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
	if err := syscall.Kill(previewPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("preview process %d survived server replacement: %v", previewPID, err)
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

// egressEnviron are the proxy and CA variables of a box behind a company's
// egress proxy, each spelled out: a list taken from the code would pass
// whatever the code says. The lower-case forms are there because curl, and
// git with it, read them and not the upper-case http_proxy.
var egressEnviron = []string{"HTTPS_PROXY=http://proxy.corp:3128", "https_proxy=http://proxy.corp:3128",
	"HTTP_PROXY=http://proxy.corp:3128", "http_proxy=http://proxy.corp:3128",
	"NO_PROXY=localhost,.corp", "no_proxy=localhost,.corp",
	"SSL_CERT_FILE=/etc/corp/ca.pem", "SSL_CERT_DIR=/etc/corp/certs", "NODE_EXTRA_CA_CERTS=/etc/corp/ca.pem"}

// ServerEnv keeps PATH, HOME, the proxy and CA variables and the yardr
// knobs, passes the test process that started it through, replaces the home
// with the one served, and drops everything else: a credential from a crew
// member's pane, the starter's identity, the shell's own.
func TestServerEnv(t *testing.T) {
	environ := slices.Concat([]string{"PATH=/bin", "HOME=/Users/jo", "GITHUB_TOKEN=ghp_x", "LINEAR_API_KEY=lin_x"},
		egressEnviron,
		// Not among the nine: a proxy yardr was not asked to hand on.
		[]string{"ALL_PROXY=socks5://proxy.corp:1080", "CURL_CA_BUNDLE=/etc/corp/ca.pem"},
		[]string{"YARDR_HOME=y1", "YARDR_ACTOR=yardmaster", "YARDR_CREW=yardmaster", "YARDR_BEAD=web-3",
			"YARDR_SESSION=s1", "YARDR_STAGE=new", "YARDR_GROUP=g", "YARDR_BIN=/bin/yardr",
			"YARDR_HOOK_ADDR=127.0.0.1:1", "YARDR_WEB_ADDR=127.0.0.1:2", "YARDR_WEB_AS=jo", "YARDR_BDP_ADDR=127.0.0.1:3",
			"YARDR_EVENT_RETENTION=1d", "YARDR_TEST_RPC=1", "YARDR_BASE=main",
			"YARDR_HOOK_PAYLOAD_RETENTION=2d", "YARDR_PPROF=/y1/pprof.sock", "YARDR_TEST_OWNER=4242 987654",
			"YARDR_TEST_HOME_CHECK=1s", "SHELL=/bin/zsh", "TERM=xterm"})
	got := ServerEnv(environ, "/y1")
	want := slices.Concat([]string{"PATH=/bin", "HOME=/Users/jo"}, egressEnviron,
		[]string{"YARDR_HOOK_ADDR=127.0.0.1:1", "YARDR_WEB_ADDR=127.0.0.1:2",
			"YARDR_WEB_AS=jo", "YARDR_BDP_ADDR=127.0.0.1:3", "YARDR_EVENT_RETENTION=1d",
			"YARDR_HOOK_PAYLOAD_RETENTION=2d", "YARDR_PPROF=/y1/pprof.sock", "YARDR_TEST_OWNER=4242 987654",
			"YARDR_TEST_HOME_CHECK=1s", "YARDR_HOME=/y1"})
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ServerEnv =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// One set and empty goes as it is: yardr hands on what the starter has
	// and judges none of it.
	if got := ServerEnv([]string{"HTTPS_PROXY="}, "/y1"); !slices.Equal(got, []string{"HTTPS_PROXY=", "YARDR_HOME=/y1"}) {
		t.Errorf("ServerEnv with an empty proxy = %v", got)
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
	t.Setenv("YARDR_TEST_OWNER", "4242 987654")
	t.Setenv("YARDR_TEST_HOME_CHECK", "1s")
	// The wait for the server is the starter's, not the server's: it is in
	// the environment here, and the check below fails if the server gets it.
	t.Setenv("YARDR_TEST_START_WAIT", "90s")
	// A box behind a company's proxy has it in the operator's shell, and
	// the server it starts reaches its sources and remotes through it.
	for _, kv := range egressEnviron {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
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
		case k == "PATH", k == "HOME", config.IsVar(k, "HOME"), source.IsEgressVar(k), config.IsVar(k, config.TestOwnerEnv, config.TestHomeCheckEnv), config.IsVar(k, config.ServerSettings...):
		case k == "PWD", k == "OLDPWD", k == "SHLVL", k == "_": // the shell's own
		default:
			t.Errorf("server got %s", kv)
		}
	}
	for _, kv := range slices.Concat([]string{"YARDR_HOME=" + c.Home, "YARDR_HOOK_ADDR=127.0.0.1:1",
		"YARDR_EVENT_RETENTION=1d", "YARDR_HOOK_PAYLOAD_RETENTION=2d", "YARDR_PPROF=/y1/pprof.sock",
		"YARDR_TEST_OWNER=4242 987654", "YARDR_TEST_HOME_CHECK=1s"}, egressEnviron) {
		if !slices.Contains(env, kv) {
			t.Errorf("server lacks %s:\n%s", kv, strings.Join(env, "\n"))
		}
	}
	if envOf(env, "PATH") == "" || envOf(env, "HOME") == "" {
		t.Errorf("server lacks PATH or HOME:\n%s", strings.Join(env, "\n"))
	}
}

// Start waits ten seconds for the server it started, or as long as the bound
// a test names in YARDR_TEST_START_WAIT: the CLI a test runs reads it from the
// test's environment, where a Go variable cannot reach it, and a machine under
// load does not always leave a server ten seconds to start in (yardr-5jd7). A
// bound that is missing, malformed or not positive is the ten seconds.
func TestStartWait(t *testing.T) {
	// Serial: sets the process's environment.
	for _, tc := range []struct {
		name, value string
		want        time.Duration
	}{
		{"missing", "", 10 * time.Second},
		{"seconds", "90s", 90 * time.Second},
		{"minutes", "2m", 2 * time.Minute},
		{"under a second", "1500ms", 1500 * time.Millisecond},
		{"not a duration", "soon", 10 * time.Second},
		{"a number with no unit", "90", 10 * time.Second},
		{"zero", "0s", 10 * time.Second},
		{"negative", "-5s", 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("YARDR_TEST_START_WAIT", tc.value)
			if got := startWait(); got != tc.want {
				t.Errorf("startWait() = %v with YARDR_TEST_START_WAIT=%q, want %v", got, tc.value, tc.want)
			}
		})
	}
}

// The bound reaches Start's deadline, and the error names the bound it was
// given, in seconds as the unstubbed ten always were. The stand-in is a
// server that is up and has not bound its socket, which ends with its home.
func TestStartWaitEndsStartAtTheBoundGiven(t *testing.T) {
	c := home(t)
	script := filepath.Join(t.TempDir(), "yardr")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nwhile [ -d \"$YARDR_HOME\" ]; do sleep 0.2; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := executable
	executable = func() (string, error) { return script, nil }
	t.Cleanup(func() { executable = old })
	t.Setenv("YARDR_TEST_START_WAIT", "300ms")

	began := time.Now()
	err := Start(context.Background(), c)
	if err == nil {
		t.Fatal("Start: no error from a server that never answers")
	}
	// Only the direction a loaded machine cannot falsify: Start does not
	// give up before the bound.
	if took := time.Since(began); took < 300*time.Millisecond {
		t.Errorf("Start gave up after %v, want at least the 300ms it was given", took)
	}
	if want := "did not answer within 0.3s; see " + c.LogFile(); !strings.Contains(err.Error(), want) {
		t.Errorf("Start: %q, want it to say %q", err, want)
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
