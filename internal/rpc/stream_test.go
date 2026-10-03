package rpc

import (
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
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
	live, err := c.Subscribe(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
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

	// The reader that kept up is still served.
	select {
	case e, ok := <-live:
		if !ok {
			t.Fatal("the live subscriber was closed along with the stalled one")
		}
		if e.Kind != "noise" {
			t.Fatalf("event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the live subscriber got nothing")
	}
}
