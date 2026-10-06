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

// stalledHeader marks the request of the subscriber that never reads, so
// the test's server can say when its handler has returned.
const stalledHeader = "X-Test-Stalled"

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
	// Seconds, not the 200ms of the tests beside this one: the live reader
	// below takes 256KB events through HTTP and JSON under -race, and on a
	// loaded machine went longer than 200ms over one, so the server cut it
	// off by its own rule. The stalled reader never reads at all, so its
	// cut-off needs no short timeout to show, only one wait of this one.
	const writeTimeout = 5 * time.Second
	h := handler(st, nil, writeTimeout)
	cut := make(chan struct{})
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		if r.Header.Get(stalledHeader) != "" {
			close(cut)
		}
	})
	ctx := context.Background()

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

	// A raw subscriber that never reads: the socket fills and the server's
	// write blocks.
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /events?after=0 HTTP/1.1\r\nHost: yard\r\n%s: 1\r\n\r\n", stalledHeader)
	pad := strings.Repeat("x", 256<<10)
	for i := 0; i < 16; i++ { // 4MB: more than any socket buffers between them
		if err := st.LogEvent(ctx, "", "noise", "test", map[string]any{"pad": pad}); err != nil {
			t.Fatal(err)
		}
	}
	// The blocked write hits its deadline and the handler returns. Waited
	// for, not slept through: nothing is read from conn before then, so the
	// reader stays stalled however late the server comes to that write.
	select {
	case <-cut:
	case <-time.After(writeTimeout + 30*time.Second):
		t.Fatal("the stream's handler is still in Write after its reader stalled")
	}
	// Draining the connection now ends in EOF only if the server closed it.
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
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
		case <-time.After(30 * time.Second):
			t.Fatalf("the live subscriber got %d of 16 events", i)
		}
	}
}

// A stream that is merely quiet is never cut off: the deadline belongs to
// one write, so a reader is still served after going longer than the
// timeout without an event, before the first one and after it. The events
// are small enough that no write waits on the reader, so the short timeout
// is no matter of the machine here.
func TestQuietStreamIsNotCutOff(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close(context.Background())
	sock := socket(t)
	serve(t, sock, handler(st, nil, 200*time.Millisecond).ServeHTTP)
	ctx := context.Background()

	sub, err := NewClient(sock).Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		time.Sleep(500 * time.Millisecond) // quiet for longer than the timeout
		if err := st.LogEvent(ctx, "", "noise", "test", map[string]any{"n": n}); err != nil {
			t.Fatal(err)
		}
		select {
		case e, ok := <-sub:
			if !ok {
				t.Fatalf("a quiet stream was cut off before event %d", n)
			}
			if e.Kind != "noise" {
				t.Fatalf("event = %+v", e)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("event %d never came", n)
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
