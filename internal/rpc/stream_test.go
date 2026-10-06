package rpc

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// A stream whose reader has stopped taking is closed once a write has
// waited the timeout, rather than holding the handler in Write for good
// (past a drain's settle); a reader that keeps up is never cut off.
func TestStalledStreamReaderIsCutOff(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(context.Background())
	sock := socket(t)
	serve(t, sock, handler(st, nil, 200*time.Millisecond).ServeHTTP)
	ctx := context.Background()

	// A reader that keeps up sees everything, however long the stream is quiet.
	c := NewClient(sock)
	sub, err := c.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Taken as they come, for the whole test: Subscribe's channel has no
	// buffer, so a subscriber the test only looked at now and then would
	// stop taking in between and be cut off as a stalled reader itself.
	live := make(chan store.Event, 32)
	go func() {
		defer close(live)
		for e := range sub {
			live <- e
		}
	}()
	time.Sleep(500 * time.Millisecond) // quiet for longer than the timeout
	if err := st.LogEvent(ctx, "", "noise", "test", map[string]any{"n": 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-live:
		if e.Kind != "noise" {
			t.Fatalf("event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a quiet stream was cut off, or the event never came")
	}

	// A raw subscriber that never reads: the socket fills and the server's
	// write blocks.
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET /events?after=0 HTTP/1.1\r\nHost: yard\r\n\r\n")
	pad := strings.Repeat("x", 256<<10)
	for i := 0; i < 16; i++ { // 4MB: more than any socket buffers between them
		if err := st.LogEvent(ctx, "", "noise", "test", map[string]any{"pad": pad}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(600 * time.Millisecond) // the blocked write hits its deadline
	// Draining the connection now ends in EOF only if the server closed it.
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("the stream is still open after its reader stalled: %v", err)
	}

	// The reader that kept up is still served, and missed nothing.
	for i := 0; i < 16; i++ {
		select {
		case e, ok := <-live:
			if !ok {
				t.Fatalf("the live subscriber was closed along with the stalled one, after %d of 16 events", i)
			}
			if e.Kind != "noise" {
				t.Fatalf("event = %+v", e)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the live subscriber got %d of 16 events", i)
		}
	}
}

// heldConn is a connection whose reads wait while hold is locked.
type heldConn struct {
	net.Conn
	hold *sync.Mutex
}

func (c heldConn) Read(p []byte) (int, error) {
	c.hold.Lock()
	c.hold.Unlock()
	return c.Conn.Read(p)
}

// A subscriber through the Client is cut off like any other once it stops
// taking: its channel closes, and what the server had not written whole is
// lost with it. The reader is held back by the test, not by load: this is
// what TestStalledStreamReaderIsCutOff's live subscriber met on a busy
// machine while that test read from it only at its ends.
func TestStalledClientSubscriberIsClosed(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(context.Background())
	sock := socket(t)
	serve(t, sock, handler(st, nil, 200*time.Millisecond).ServeHTTP)
	ctx := context.Background()

	var hold sync.Mutex
	c := NewClient(sock)
	c.http = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			conn, err := d.DialContext(ctx, "unix", sock)
			if err != nil {
				return nil, err
			}
			return heldConn{conn, &hold}, nil
		},
		DisableKeepAlives: true,
	}}
	sub, err := c.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LogEvent(ctx, "", "noise", "test", map[string]any{"n": 0}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sub:
	case <-time.After(5 * time.Second):
		t.Fatal("the event never came")
	}

	hold.Lock()
	pad := strings.Repeat("x", 256<<10)
	for i := 0; i < 16; i++ { // 4MB: more than any socket buffers between them
		if err := st.LogEvent(ctx, "", "noise", "test", map[string]any{"pad": pad}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(600 * time.Millisecond) // the blocked write hits its deadline
	hold.Unlock()

	// At most the one read that was already waiting got through, so no
	// event of 256KB arrived whole.
	select {
	case e, ok := <-sub:
		if ok {
			t.Fatalf("a subscriber that had stopped taking was still served: seq %d", e.Seq)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream is still open after its reader stalled")
	}
}
