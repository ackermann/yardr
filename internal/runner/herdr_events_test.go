package runner

import (
	"bufio"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHerdr accepts subscriptions and lets the test push lines to the
// current connection.
func fakeHerdr(t *testing.T) (sock string, conns <-chan net.Conn) {
	// Not under t.TempDir: with the test's name in it the socket path is
	// exactly at macOS's limit (TMPDIR there is ~50 bytes already).
	dir, err := os.MkdirTemp("", "yh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock = filepath.Join(dir, "h.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	ch := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			if !strings.Contains(line, `"events.subscribe"`) || !strings.Contains(line, `"pane.exited"`) {
				c.Close()
				continue
			}
			c.Write([]byte(`{"id":"yard-events","result":{"type":"subscription_started"}}` + "\n"))
			ch <- c
		}
	}()
	return sock, ch
}

func waitWake(t *testing.T, w <-chan struct{}, want bool) {
	t.Helper()
	select {
	case <-w:
		if !want {
			t.Fatal("unexpected wake")
		}
	case <-time.After(500 * time.Millisecond):
		if want {
			t.Fatal("no wake")
		}
	}
}

func TestHerdrWakeOnEventsAndReconnect(t *testing.T) {
	t.Parallel()
	sock, conns := fakeHerdr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := &Herdr{Socket: sock}
	w, err := h.Wake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := <-conns
	waitWake(t, w, false) // the first subscription is not itself a wake

	c.Write([]byte(`{"event":"pane_exited","data":{"type":"pane_exited","pane_id":"w1:p2","workspace_id":"w1"}}` + "\n"))
	waitWake(t, w, true)

	// herdr restarts: the stream drops, yard reconnects and wakes because
	// exits may have been missed meanwhile.
	c.Close()
	select {
	case <-conns:
	case <-time.After(3 * time.Second):
		t.Fatal("did not reconnect")
	}
	waitWake(t, w, true)
}
