package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
	"yardr.dev/yardr/internal/web"
)

// holdDaemonLock holds an exclusive flock on the home's daemon lock, as a
// running daemon would, until the test ends.
func holdDaemonLock(t *testing.T, home string) {
	t.Helper()
	path := config.At(home).DaemonLock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	})
}

// answerServer has /health read a server on build since the given start.
func answerServer(build string, since time.Time) func() {
	return web.SetDescribeServer(func(context.Context, *config.Config) (server.Info, bool) {
		return server.Info{PID: 1234, Revision: build, Since: since}, true
	})
}

// logDaemonStart records a daemon start of build, as the dispatcher logs it.
func logDaemonStart(t *testing.T, st store.API, kind, build string) {
	t.Helper()
	if err := st.LogDispatch(context.Background(), "", kind, "yard", map[string]any{"to": build}); err != nil {
		t.Fatal(err)
	}
}

func healthOf(t *testing.T, r resp) web.Health {
	t.Helper()
	if r.status != 200 || r.header.Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, type %q: %s", r.status, r.header.Get("Content-Type"), r.body)
	}
	var got web.Health
	if err := json.Unmarshal([]byte(r.body), &got); err != nil {
		t.Fatalf("%v: %s", err, r.body)
	}
	return got
}

// GET /health on a whole yard: the server's build and start, the daemon's
// lock, build and last pass, and the view's build, with status ok.
func TestHealth(t *testing.T) {
	f := newFixture(t)
	since := time.Date(2026, 10, 14, 9, 2, 11, 0, time.UTC)
	pass := time.Date(2026, 10, 14, 9, 1, 44, 0, time.UTC)
	defer answerServer("1904417", since)()
	holdDaemonLock(t, f.home)
	logDaemonStart(t, f.st, "daemon_started", "1904417")
	if err := config.WritePassStamp(f.home, config.PassStamp{Build: "1904417", At: pass}); err != nil {
		t.Fatal(err)
	}
	r := f.get("/health")
	want := web.Health{Status: "ok",
		Server: web.HealthServer{Build: "1904417", Since: &since},
		Daemon: web.HealthDaemon{Running: true, Build: "1904417", LastPass: &pass},
		View:   web.HealthView{Build: version.Current()}}
	if got := healthOf(t, r); !reflect.DeepEqual(got, want) {
		t.Errorf("health\n got %+v\nwant %+v", got, want)
	}
	// A machine route: JSON even for a browser, with no template behind it.
	if r := f.get("/health", "Accept", "text/html"); r.status != 200 || r.header.Get("Content-Type") != "application/json" {
		t.Errorf("for a browser: status %d, type %q, want 200 JSON", r.status, r.header.Get("Content-Type"))
	}
	// The store's head sequence is not public health data, and nothing
	// renders an unknown build as "unknown".
	for _, not := range []string{`"head"`, "unknown"} {
		if strings.Contains(r.body, not) {
			t.Errorf("/health answers %q, which it must not carry: %s", not, r.body)
		}
	}
	if v := r.header.Get("Cache-Control"); v != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: the yard moves under the answer", v)
	}
}

// GET /health with a part missing answers 200 with status degraded: a
// daemon that holds no lock, a daemon whose build no start event names, a
// daemon that never stamped a pass, and a server that does not answer.
func TestHealthDegraded(t *testing.T) {
	since := time.Date(2026, 10, 14, 9, 2, 11, 0, time.UTC)
	pass := time.Date(2026, 10, 14, 9, 1, 44, 0, time.UTC)
	whole := func(t *testing.T, f *fixture) {
		t.Helper()
		holdDaemonLock(t, f.home)
		logDaemonStart(t, f.st, "daemon_started", "1904417")
		if err := config.WritePassStamp(f.home, config.PassStamp{Build: "1904417", At: pass}); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("daemon down", func(t *testing.T) {
		f := newFixture(t)
		defer answerServer("1904417", since)()
		logDaemonStart(t, f.st, "daemon_started", "1904417")
		if err := os.MkdirAll(f.home, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := config.WritePassStamp(f.home, config.PassStamp{Build: "1904417", At: pass}); err != nil {
			t.Fatal(err)
		}
		// No lock held: the daemon is down, and the last build and pass
		// it left are still reported.
		got := healthOf(t, f.get("/health"))
		want := web.Health{Status: "degraded",
			Server: web.HealthServer{Build: "1904417", Since: &since},
			Daemon: web.HealthDaemon{Running: false, Build: "1904417", LastPass: &pass},
			View:   web.HealthView{Build: version.Current()}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("health\n got %+v\nwant %+v", got, want)
		}
	})
	t.Run("daemon build unknown", func(t *testing.T) {
		f := newFixture(t)
		defer answerServer("1904417", since)()
		holdDaemonLock(t, f.home)
		if err := config.WritePassStamp(f.home, config.PassStamp{Build: "1904417", At: pass}); err != nil {
			t.Fatal(err)
		}
		// No start event: a daemon from before daemon_started, never
		// replaced since. Omitted, not "unknown".
		got := healthOf(t, f.get("/health"))
		want := web.Health{Status: "degraded",
			Server: web.HealthServer{Build: "1904417", Since: &since},
			Daemon: web.HealthDaemon{Running: true, LastPass: &pass},
			View:   web.HealthView{Build: version.Current()}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("health\n got %+v\nwant %+v", got, want)
		}
	})
	t.Run("no pass stamped", func(t *testing.T) {
		f := newFixture(t)
		defer answerServer("1904417", since)()
		holdDaemonLock(t, f.home)
		logDaemonStart(t, f.st, "daemon_started", "1904417")
		got := healthOf(t, f.get("/health"))
		want := web.Health{Status: "degraded",
			Server: web.HealthServer{Build: "1904417", Since: &since},
			Daemon: web.HealthDaemon{Running: true, Build: "1904417"},
			View:   web.HealthView{Build: version.Current()}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("health\n got %+v\nwant %+v", got, want)
		}
	})
	t.Run("server down", func(t *testing.T) {
		f := newFixture(t)
		whole(t, f)
		// No server answers on the home's socket: the yard still reads
		// through the store, so the route answers, without the server.
		got := healthOf(t, f.get("/health"))
		want := web.Health{Status: "degraded",
			Daemon: web.HealthDaemon{Running: true, Build: "1904417", LastPass: &pass},
			View:   web.HealthView{Build: version.Current()}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("health\n got %+v\nwant %+v", got, want)
		}
	})
}

// The daemon's build is the latest start, whether a replace or a fresh
// start came last: daemon_started names the build of a fresh-started daemon.
func TestHealthDaemonBuild(t *testing.T) {
	f := newFixture(t)
	defer answerServer("1904417", time.Now())()
	holdDaemonLock(t, f.home)
	logDaemonStart(t, f.st, "daemon_started", "1904410")
	if got := healthOf(t, f.get("/health")); got.Daemon.Build != "1904410" {
		t.Fatalf("daemon build %q, want the fresh start's 1904410", got.Daemon.Build)
	}
	logDaemonStart(t, f.st, "daemon_replaced", "1904417")
	if got := healthOf(t, f.get("/health")); got.Daemon.Build != "1904417" {
		t.Fatalf("daemon build %q, want the replace's 1904417", got.Daemon.Build)
	}
	logDaemonStart(t, f.st, "daemon_started", "1904420")
	if got := healthOf(t, f.get("/health")); got.Daemon.Build != "1904420" {
		t.Fatalf("daemon build %q, want the later fresh start's 1904420", got.Daemon.Build)
	}
}

// unreadableHealth is a yard whose events cannot be read at all.
type unreadableHealth struct {
	store.API
}

func (unreadableHealth) EventsOfKind(context.Context, string, int) ([]store.Event, error) {
	return nil, errors.New("the yard cannot be read")
}

// GET /health on a yard it cannot read at all fails through the error path.
func TestHealthStoreError(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	srv, err := web.NewServer(unreadableHealth{f.st}, f.home, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	r, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Errorf("status %d, want 500: the yard cannot be read", r.StatusCode)
	}
}
