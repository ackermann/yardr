package rpc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

// socket returns a short unix socket path (t.TempDir is too long).
func socket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

func serve(t *testing.T, sock string, h http.HandlerFunc) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
}

func TestSkewMessage(t *testing.T) {
	sock := socket(t)
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(version.Header, "0123456789ab")
		w.Header().Set(PIDHeader, "4242")
		writeJSON(w, reply{Error: &wireError{Code: "error", Message: "no such method " + strings.TrimPrefix(r.URL.Path, "/call/")}})
	})
	c := NewClient(sock)
	c.Revision = "ba9876543210"
	_, err := c.AdvanceNote(context.Background(), "b", "review", "n", store.Expect{}, "me")
	want := "the yardr server (pid 4242, built 0123456789ab) differs from this yardr (ba9876543210); " +
		"run `yardr server stop` (no such method AdvanceNote)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %s", err, want)
	}

	// A server from before the handshake sends no revision or pid.
	sock = socket(t)
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, reply{Error: &wireError{Code: "error", Message: "no such method AdvanceNote"}})
	})
	c = NewClient(sock)
	c.Revision = "ba9876543210"
	_, err = c.AdvanceNote(context.Background(), "b", "review", "n", store.Expect{}, "me")
	want = "the yardr server (pid unknown, built unknown) differs from this yardr (ba9876543210); " +
		"run `yardr server stop` (no such method AdvanceNote)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %s", err, want)
	}
}

func TestSameBuildKeepsPlainError(t *testing.T) {
	sock := socket(t)
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(version.Header, r.Header.Get(version.Header))
		writeJSON(w, reply{Error: &wireError{Code: "error", Message: "no such method AdvanceNote"}})
	})
	c := NewClient(sock)
	c.Revision = "0123456789ab"
	_, err := c.AdvanceNote(context.Background(), "b", "review", "n", store.Expect{}, "me")
	if err == nil || err.Error() != "no such method AdvanceNote" {
		t.Fatalf("err = %v", err)
	}
}

func TestCallSendsRevisionAndRetriesWhileReplacing(t *testing.T) {
	sock := socket(t)
	var calls atomic.Int32
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(version.Header) != "0123456789ab" {
			t.Errorf("revision header = %q", r.Header.Get(version.Header))
		}
		if calls.Add(1) < 3 {
			WriteReplacing(w)
			return
		}
		writeJSON(w, reply{Result: uint64(7)})
	})
	c := NewClient(sock)
	c.Revision = "0123456789ab"
	head, err := c.Head(context.Background())
	if err != nil || head != 7 {
		t.Fatalf("Head = %d, %v", head, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d; want 2 refusals then the answer", calls.Load())
	}
}

func TestCallWaitsForAServerToListen(t *testing.T) {
	sock := socket(t)
	// A stale socket file refuses connections, as while a server execs.
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.Remove(sock)
		serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, reply{Result: uint64(1)})
		})
	}()
	c := NewClient(sock)
	if head, err := c.Head(context.Background()); err != nil || head != 1 {
		t.Fatalf("Head = %d, %v", head, err)
	}

	c = NewClient(socket(t))
	c.Retry = 200 * time.Millisecond
	start := time.Now()
	if _, err := c.Head(context.Background()); err == nil {
		t.Fatal("no server, yet the call succeeded")
	} else if time.Since(start) > 2*time.Second {
		t.Fatalf("gave up after %v; retry is meant to be brief", time.Since(start))
	}
}

// A call the server took and did not answer is store.ErrNoAnswer: whether
// it ran is not known. A refusal is an answer, and a call nothing listened
// for never ran: neither is one.
func TestCallWithoutAnAnswer(t *testing.T) {
	sock := socket(t)
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		// The server dies with the call in hand.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	})
	c := NewClient(sock)
	_, err := c.GiveSecret(context.Background(), "b", 1, map[string]string{"NAME": "value"}, "me")
	if !errors.Is(err, store.ErrNoAnswer) {
		t.Fatalf("a broken call: %v, want store.ErrNoAnswer", err)
	}
	if strings.Contains(err.Error(), "value") {
		t.Fatalf("the error holds what was sent: %v", err)
	}

	sock = socket(t)
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, reply{Error: &wireError{Code: "error", Message: "a change to a source is the operator's"}})
	})
	_, err = NewClient(sock).GiveSecret(context.Background(), "b", 1, nil, "me")
	if err == nil || errors.Is(err, store.ErrNoAnswer) {
		t.Fatalf("a refusal: %v", err)
	}

	c = NewClient(socket(t)) // nothing listens
	c.Retry = 0
	_, err = c.GiveSecret(context.Background(), "b", 1, nil, "me")
	if err == nil || errors.Is(err, store.ErrNoAnswer) {
		t.Fatalf("a call nothing listened for: %v", err)
	}
}
