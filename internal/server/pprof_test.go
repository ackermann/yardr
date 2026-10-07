package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// YARDR_PPROF has the server serve its profiles on a unix socket at that
// path, for as long as it serves; without it the server listens on nothing
// but its own socket.
func TestPprofServedOnlyWhenAsked(t *testing.T) {
	get := func(sock string) (int, error) {
		hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sock)
			},
		}}
		resp, err := hc.Get("http://pprof/debug/pprof/goroutine")
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}

	c := home(t)
	sock := filepath.Join(c.Home, "pprof.sock")
	t.Setenv("YARDR_PPROF", sock)
	r := &runner{c: c, log: quiet}
	done, stop := startStoppable(t, r)
	if code, err := get(sock); err != nil || code != http.StatusOK {
		t.Fatalf("GET /debug/pprof/goroutine on %s = %d, %v; want 200", sock, code, err)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the pprof socket: %v, %v; want mode 0600", fi, err)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := get(sock); err == nil {
		t.Fatal("the pprof socket still answers after the server stopped")
	}

	// Unset: the home holds the server's own socket and no other.
	t.Setenv("YARDR_PPROF", "")
	c = home(t)
	done, stop = startStoppable(t, &runner{c: c, log: quiet})
	socks := 0
	filepath.WalkDir(c.Home, func(_ string, d os.DirEntry, _ error) error {
		if d != nil && d.Type()&os.ModeSocket != 0 {
			socks++
		}
		return nil
	})
	if socks != 1 {
		t.Fatalf("%d sockets in the home of a server without YARDR_PPROF; want its own only", socks)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPprofDoesNotRemoveExistingNonSocketOrLiveSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "pp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	file := filepath.Join(dir, "existing")
	if err := os.WriteFile(file, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := servePprof(file, quiet)
	stop()
	if got, err := os.ReadFile(file); err != nil || string(got) != "keep me" {
		t.Fatalf("existing file = %q, %v; want it preserved", got, err)
	}

	sock := filepath.Join(dir, "active.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	stop = servePprof(sock, quiet)
	stop()
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		t.Fatalf("existing listener no longer accepts connections: %v", err)
	}
	conn.Close()
}
