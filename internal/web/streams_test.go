package web_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// streamsFixture serves a yard that lets max streams in from off loopback,
// and as many from loopback, perAddr of them from one address.
func streamsFixture(t *testing.T, max, perAddr int) (*web.Server, *httptest.Server, store.API) {
	t.Helper()
	st := storetest.New(t)
	srv, err := web.NewServer(st, filepath.Join(t.TempDir(), ".depot"), nil)
	must(t, err)
	srv.SetStreamLimits(max, perAddr)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	return srv, ts, st
}

// shut closes a test's server, its clients' connections first. Close alone
// waits for every request to end, and a stream ends when its client goes:
// one that stays (a test that failed before it closed its stream, or a
// client that is none of the test's, as a browser tab left on an earlier
// server of this port, which asks whatever listens there next) holds Close
// until the test binary's timeout, which then names no cause. A handler
// that outlives its connection holds it still, as it should.
func shut(ts *httptest.Server) {
	// First the listener, so that no client comes after the ones closed.
	ts.Listener.Close()
	ts.CloseClientConnections()
	ts.Close()
}

// A client that keeps its stream open does not keep a test's server from
// closing, and the stream ends with the connection.
func TestShutWithAStreamOpen(t *testing.T) {
	srv, ts, _ := streamsFixture(t, 8, 2)
	addr := ts.Listener.Addr().String()
	stays, err := net.Dial("tcp", addr)
	must(t, err)
	defer stays.Close()
	_, err = fmt.Fprintf(stays, "GET /events?format=json&after=0 HTTP/1.1\r\nHost: %s\r\n\r\n", addr)
	must(t, err)
	settle(t, "the client has no stream", func() bool { return srv.Streams() == 1 })

	done := make(chan struct{})
	go func() {
		defer close(done)
		shut(ts)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		// Let the server go, or the test's own cleanup waits as long.
		stays.Close()
		<-done
		t.Fatal("the server waited 10s to close for a client that kept its stream open")
	}
	settle(t, "a stream outlived its server", func() bool { return srv.Streams() == 0 })
}

// streamFrom asks srv for a stream as a client at remote would. A stream
// that is served comes back as the func that closes it and waits for its
// place to be free; one that is refused as the answer.
func streamFrom(t *testing.T, srv *web.Server, remote string) (stop func(), refusal *httptest.ResponseRecorder) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/events?format=json&after=0", nil).WithContext(ctx)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	before := srv.Streams()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeHTTP(rec, req)
	}()
	deadline := time.After(10 * time.Second)
	for srv.Streams() == before {
		select {
		case <-done:
			cancel()
			return nil, rec
		case <-deadline:
			t.Fatalf("a stream from %s was neither served nor refused", remote)
		case <-time.After(5 * time.Millisecond):
		}
	}
	return func() {
		cancel()
		<-done
	}, nil
}

// served fails unless a stream from remote is served, and returns what
// closes it.
func served(t *testing.T, srv *web.Server, remote, what string) func() {
	t.Helper()
	stop, refusal := streamFrom(t, srv, remote)
	if refusal != nil {
		t.Fatalf("%s: a stream from %s was answered %d (%s)", what, remote, refusal.Code, refusal.Body)
	}
	return stop
}

// refused fails unless a stream from remote is answered status, with a
// Retry-After and a line that says why.
func refused(t *testing.T, srv *web.Server, remote string, status int, what string) {
	t.Helper()
	stop, refusal := streamFrom(t, srv, remote)
	if refusal == nil {
		stop()
		t.Fatalf("%s: a stream from %s was served, want %d", what, remote, status)
	}
	if refusal.Code != status || refusal.Header().Get("Retry-After") != "5" || refusal.Body.Len() == 0 {
		t.Errorf("%s: a stream from %s was answered %d, Retry-After %q, %q; want %d, 5 and a reason",
			what, remote, refusal.Code, refusal.Header().Get("Retry-After"), refusal.Body, status)
	}
}

// One address off loopback holds its share of streams and no more: the
// next is answered 429, a stream that closes frees its place, and neither
// another address nor a page on this machine is refused meanwhile.
func TestStreamsPerAddress(t *testing.T) {
	srv, ts, st := streamsFixture(t, 8, 2)
	const phone, laptop = "192.168.178.20:50001", "192.168.178.21:50001"

	first := served(t, srv, phone, "under the limit")
	// Another port is the same address.
	second := served(t, srv, "192.168.178.20:50002", "under the limit")
	refused(t, srv, phone, http.StatusTooManyRequests, "at the address's limit")
	if n := srv.Streams(); n != 2 {
		t.Errorf("%d streams are open after a refusal, want the 2 that were served", n)
	}

	// The others go on as if nothing was: another address, and loopback,
	// through a real connection, with an event to show for it.
	other := served(t, srv, laptop, "another address, with one at its limit")
	page := openStream(t, ts.URL+"/events?format=json&after=0")
	bead(t, st, "one")
	if fr := page.next(t); fr.id != 1 {
		t.Errorf("the loopback page got event %d, want 1", fr.id)
	}
	// And so does a page itself, not only its stream.
	r, err := http.Get(ts.URL + "/")
	must(t, err)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Errorf("GET / on loopback is %d with another address at its limit", r.StatusCode)
	}

	first()
	third := served(t, srv, phone, "after one of the address's streams closed")
	refused(t, srv, phone, http.StatusTooManyRequests, "at the address's limit again")
	second()
	third()
	other()
	page.cancel()
	settle(t, "streams outlived their clients", func() bool { return srv.Streams() == 0 })
}

// The streams from off loopback are limited all together: past that every
// address is answered 503, and loopback, counted apart, is still served up
// to as many again.
func TestStreamsTotal(t *testing.T) {
	srv, ts, _ := streamsFixture(t, 3, 2)
	a := served(t, srv, "10.0.0.1:1", "under the limit")
	served(t, srv, "10.0.0.1:2", "under the limit")
	served(t, srv, "10.0.0.2:1", "under the limit")
	refused(t, srv, "10.0.0.3:1", http.StatusServiceUnavailable, "at the limit of all addresses")
	// An address at its own limit hears of that first: it is the one that
	// can close something.
	refused(t, srv, "10.0.0.1:3", http.StatusTooManyRequests, "at both limits")

	// Loopback has its own places, more than one address's share.
	var pages []*stream
	for range 3 {
		pages = append(pages, openStream(t, ts.URL+"/events?format=json&after=0"))
	}
	settle(t, "three loopback pages are not three more streams", func() bool { return srv.Streams() == 6 })
	resp, err := http.Get(ts.URL + "/events?format=json&after=0")
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "5" {
		t.Errorf("a loopback stream past the limit is %d, Retry-After %q; want 503 and 5", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	refused(t, srv, "[::1]:9", http.StatusServiceUnavailable, "::1 with 127.0.0.1 at loopback's limit")

	// A place freed off loopback goes to whoever asks next, and one freed
	// on loopback to loopback.
	a()
	served(t, srv, "10.0.0.3:1", "after a stream closed")
	pages[0].cancel()
	settle(t, "a closed loopback stream kept its place", func() bool { return srv.Streams() == 5 })
	openStream(t, ts.URL+"/events?format=json&after=0")
}

// A request that is no stream after all (a cursor that is no seq) gives
// its place back.
func TestStreamsBadRequestFreesPlace(t *testing.T) {
	srv, _, _ := streamsFixture(t, 1, 1)
	for range 3 {
		req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/events?after=x", nil)
		req.RemoteAddr = "10.0.0.1:1"
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("after=x is %d, want 400", rec.Code)
		}
	}
	served(t, srv, "10.0.0.1:1", "after three bad requests")
}

// What a stream is counted under: loopback is one client however it is
// written, an IPv6 address stands for its /64, and a port tells nothing.
func TestStreamKey(t *testing.T) {
	for remote, want := range map[string]string{
		"127.0.0.1:1":              "loopback",
		"127.8.9.1:1":              "loopback",
		"[::1]:1":                  "loopback",
		"[::ffff:127.0.0.1]:1":     "loopback",
		"192.168.1.5:40000":        "192.168.1.5",
		"[::ffff:192.168.1.5]:2":   "192.168.1.5",
		"[2001:db8:1:2:a:b:c:d]:1": "2001:db8:1:2::/64",
		"[2001:db8:1:2::1]:1":      "2001:db8:1:2::/64",
		"[2001:db8:1:3::1]:1":      "2001:db8:1:3::/64",
		"[fe80::1%en0]:1":          "fe80::/64",
		"":                         "unknown",
		"pipe":                     "unknown",
	} {
		if got := web.StreamKey(remote); got != want {
			t.Errorf("a stream from %q is counted under %q, want %q", remote, got, want)
		}
	}
}
