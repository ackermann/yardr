package web_test

import (
	"context"
	"encoding/json"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// The stream and the feed tell each viewer their own events: mail to its
// parties alone, a setting's value for a told setting alone, and every
// other event whole to an operator and held to its row otherwise. Nobody
// is party to no mail.
func TestStreamTellsEachViewerTheirOwn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.SetSetting(ctx, store.OperatorSetting, "ada", "ada"))
	for _, name := range []string{"sam", "bob"} {
		must(t, func() error { _, err := f.st.AddUser(ctx, name, "ada"); return err }())
	}
	must(t, f.st.Mail(ctx, "ada", "for ada's eyes", "bob"))
	must(t, f.st.LogDispatch(ctx, "", "move_refused", "yard", map[string]any{"session": "s1", "reason": "not_allowed",
		"command": "yardr bead done yd-1 --outcome shipped", "error": "flow has no outcome shipped"}))
	must(t, f.st.SetSetting(ctx, store.WebJWTUsersSetting, "ada@example.net=ada", "ada"))
	must(t, f.st.SetSetting(ctx, store.StyleSetting, "guide", "ada"))
	head, err := f.st.Head(ctx)
	must(t, err)

	sam, ada := f.as(t, "sam"), f.as(t, "ada")
	streamOf := func(v *fixture) *stream {
		t.Helper()
		if v.session == nil {
			return v.stream(t, "/events?format=json&after=0")
		}
		return v.stream(t, "/events?format=json&after=0", "Cookie", v.session.Name+"="+v.session.Value)
	}
	// framesOf replays the log's head frames for v: no more is written
	// after, so no frame waits on the yard.
	framesOf := func(v *fixture) []frame {
		t.Helper()
		s := streamOf(v)
		defer s.cancel()
		var out []frame
		for range head {
			out = append(out, s.next(t))
		}
		return out
	}
	type event struct {
		Kind string         `json:"kind"`
		Data map[string]any `json:"data"`
	}
	// dataOf is the data the frames tell of the kind, and of the
	// setting key where one is named: absent where a frame carries no
	// data for it, as a dropped event is its id alone.
	dataOf := func(frames []frame, kind, key string) map[string]any {
		t.Helper()
		for _, fr := range frames {
			if fr.data == "" {
				continue
			}
			var e event
			must(t, json.Unmarshal([]byte(fr.data), &e))
			if e.Kind != kind {
				continue
			}
			if key != "" && e.Data["key"] != key {
				continue
			}
			return e.Data
		}
		return nil
	}
	for _, v := range []struct {
		name string
		f    *fixture
		// party reads the mail whole; operator reads every other
		// event whole and the operator's words with their value.
		party, operator bool
	}{
		{"nobody", f, false, false},
		{"a member", sam, false, false},
		{"the operator", ada, true, true},
	} {
		frames := framesOf(v.f)
		if data := dataOf(frames, "mail", ""); (data != nil) != v.party {
			t.Errorf("%s: the stream tells mail %v, want told: %v", v.name, data != nil, v.party)
		} else if v.party && data["text"] != "for ada's eyes" {
			t.Errorf("%s: the stream tells mail %v, want it whole", v.name, data)
		}
		data := dataOf(frames, "move_refused", "")
		if data == nil {
			t.Errorf("%s: the stream drops the refusal, want it told", v.name)
		} else if _, told := data["command"]; told != v.operator {
			t.Errorf("%s: the stream tells the refusal %v, want the command told: %v", v.name, data, v.operator)
		}
		if data := dataOf(frames, "setting_changed", store.WebJWTUsersSetting); data == nil {
			t.Errorf("%s: the stream drops the users change, want its key told", v.name)
		} else if _, told := data["value"]; told {
			t.Errorf("%s: the stream tells the users change %v, want its key alone", v.name, data)
		}
		data = dataOf(frames, "setting_changed", store.StyleSetting)
		if data == nil {
			t.Errorf("%s: the stream drops the style change, want its key told", v.name)
		} else if _, told := data["value"]; told != v.operator {
			t.Errorf("%s: the stream tells the style change %v, want its value told: %v", v.name, data, v.operator)
		}
		// The feed's JSON names every entry the viewer is told: the
		// mail for its party alone, the refusal with its words for
		// the operator alone.
		events := homeJSON(t, v.f, "/").Events
		var mail, refusal *web.Entry
		for i := range events {
			switch events[i].Kind {
			case "mail":
				mail = &events[i]
			case "move_refused":
				refusal = &events[i]
			}
		}
		if (mail != nil) != v.party {
			t.Errorf("%s: the feed names mail: %v, want named: %v", v.name, mail != nil, v.party)
		}
		if refusal == nil {
			t.Errorf("%s: the feed names no refusal", v.name)
		} else if (refusal.Note != "") != v.operator {
			t.Errorf("%s: the feed names the refusal %q, want its words: %v", v.name, refusal.Note, v.operator)
		}
	}
}

// What a stream is counted under: loopback is one client however it is
// written, an IPv6 address stands for its /64, and a port tells nothing.
func TestStreamKey(t *testing.T) {
	t.Parallel()
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
