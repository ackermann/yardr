package dispatch_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// goneStore is a store whose server has gone: Groups, the first call of
// every pass, fails as a client's does when nothing listens on the socket,
// until the server is back.
type goneStore struct {
	store.API
	gone   atomic.Bool
	served atomic.Int32 // the calls the store answered
}

func (s *goneStore) Groups(ctx context.Context) ([]store.Group, error) {
	if s.gone.Load() {
		return nil, fmt.Errorf("yardr server: Post \"http://yard/call/Groups\": dial unix /y/yard.sock: connect: %w", syscall.ENOENT)
	}
	s.served.Add(1)
	return s.API.Groups(ctx)
}

// The daemon connected to the server that ran when it started and only
// dials after. A server that is gone then (a replace whose new build did not
// come up, a stop, a crash) left it failing on a socket nothing listens on
// every pass, for good, until someone restarted it.
func TestDaemonAsksForTheServerAfterEachPassThatFails(t *testing.T) {
	t.Parallel()
	s, _, d := setup(t)
	gone := &goneStore{API: s}
	gone.gone.Store(true)
	d.Store = gone
	d.Log = slog.New(slog.DiscardHandler)
	d.Interval = 5 * time.Millisecond
	var asked atomic.Int32
	d.Server = func(context.Context) error {
		// The first two asks cannot start a server, and are asked again
		// after the next pass that fails; the third finds one.
		if asked.Add(1) < 3 {
			return errors.New("yardr server did not answer within 10s")
		}
		gone.gone.Store(false)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	until(t, "passes that work once the server is back", func() bool { return gone.served.Load() >= 3 })
	cancel()
	must(t, <-done)
	if n := asked.Load(); n != 3 {
		t.Fatalf("Server asked %d times, want 3: once for each pass that failed, and none for the passes after", n)
	}
}

// wakeGoneStore is a store whose server goes away mid-run: the first Wake
// succeeds, and every Wake after fails as a client's does when nothing
// listens on the socket. Passes keep working: no pass fails.
type wakeGoneStore struct {
	store.API
	wakes atomic.Int32
	lived chan struct{} // the first stream; closed when the server goes away
}

func (s *wakeGoneStore) Wake(ctx context.Context) (<-chan struct{}, error) {
	if s.wakes.Add(1) == 1 {
		return s.lived, nil
	}
	return nil, fmt.Errorf("yardr server: Post \"http://yard/call/Wake\": dial unix /y/yard.sock: connect: %w", syscall.ENOENT)
}

// lockedWriter serialises the log writes Run's goroutines make.
type lockedWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *lockedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// A wake stream that fails after it lived asks for the server again without
// waiting for a pass to fail: recovery starts seconds after the stream
// notices, not after the next pass fails its 30 s of call retries.
func TestWakeStreamAsksForTheServerWithoutAFailingPass(t *testing.T) {
	t.Parallel()
	s, _, d := setup(t)
	wakeGone := &wakeGoneStore{API: s, lived: make(chan struct{})}
	d.Store = wakeGone
	var logged lockedWriter
	d.Log = slog.New(slog.NewTextHandler(&logged, nil))
	d.Interval = time.Minute // no second pass while the test runs
	d.StreamRetry = 5 * time.Millisecond
	var asked atomic.Int32
	d.Server = func(context.Context) error {
		asked.Add(1)
		return errors.New("yardr server did not answer within 10s")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	until(t, "wake stream that lived", func() bool { return wakeGone.wakes.Load() >= 1 })
	close(wakeGone.lived) // the server goes away mid-run
	until(t, "Server asked from the wake stream", func() bool { return asked.Load() >= 1 })
	cancel()
	must(t, <-done)
	if n := strings.Count(logged.String(), "msg=reconcile"); n != 0 {
		t.Fatalf("%d failed passes, want none: the ask must come from the wake stream, not the backstop:\n%s", n, logged.String())
	}
}
