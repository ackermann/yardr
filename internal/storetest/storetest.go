// Package storetest boots a throwaway in-memory SurrealDB for tests. Each
// call to New gets its own database inside one shared server process.
package storetest

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/mckean/yard/internal/store"
)

var (
	once     sync.Once
	addr     string
	startErr error
	seq      atomic.Int64
)

// Main wraps testing.M so the shared server is stopped after the package runs.
func Main(m *testing.M) {
	code := m.Run()
	if srv != nil {
		_ = srv.Process.Kill()
		_, _ = srv.Process.Wait()
	}
	os.Exit(code)
}

var srv *exec.Cmd

func start() {
	bin, err := exec.LookPath("surreal")
	if err != nil {
		startErr = err
		return
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		startErr = err
		return
	}
	addr = l.Addr().String()
	l.Close()
	srv = exec.Command(bin, "start", "--no-banner", "--log", "error", "--bind", addr,
		"--username", "root", "--password", "root", "memory")
	// Die with the test binary even if it panics before Main's cleanup runs.
	srv.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := srv.Start(); err != nil {
		startErr = err
		return
	}
	for i := 0; i < 150; i++ {
		resp, err := http.Get("http://" + addr + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	startErr = fmt.Errorf("surreal test server did not start on %s", addr)
}

// New returns a store on a fresh database, skipping if surreal is missing.
func New(t testing.TB) *store.Store {
	t.Helper()
	return Open(t, fmt.Sprintf("t%d_%d", os.Getpid(), seq.Add(1)))
}

// Open connects to the named test database, creating it on first use.
func Open(t testing.TB, db string) *store.Store {
	t.Helper()
	s, err := TryOpen(t, db)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return s
}

// TryOpen is Open that returns the error; safe to call from goroutines.
func TryOpen(t testing.TB, db string) (*store.Store, error) {
	once.Do(start)
	if startErr != nil {
		t.Skipf("surreal unavailable: %v", startErr)
	}
	s, err := store.Open(context.Background(), store.Options{
		Endpoint: "ws://" + addr, User: "root", Pass: "root", Namespace: "test", Database: db,
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s, nil
}

// FreshName returns an unused database name.
func FreshName() string { return fmt.Sprintf("t%d_%d", os.Getpid(), seq.Add(1)) }
