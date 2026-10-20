package edgelink

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// served is a handler that keeps every request the link handed it.
type served struct {
	mu   sync.Mutex
	reqs []servedReq
}

type servedReq struct {
	Path, RemoteAddr, Signature string
	Body                        string
}

func (s *served) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.reqs = append(s.reqs, servedReq{Path: r.URL.RequestURI(), RemoteAddr: r.RemoteAddr, Signature: r.Header.Get("X-Hub-Signature-256"), Body: string(body)})
	s.mu.Unlock()
	w.Header().Set("X-Seen", r.URL.Path)
	w.WriteHeader(http.StatusAccepted)
	io.WriteString(w, "seen "+r.URL.Path)
}

func (s *served) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.reqs {
		out = append(out, r.Path)
	}
	return out
}

// linked is a yard's link to a stand-in edge over TLS, running.
type linked struct {
	edge   *standin.Edge
	link   *Link
	served *served
	stop   func()

	// told is every state the link told, in order; more is signalled at
	// each, never blocking the link.
	mu   sync.Mutex
	told []State
	more chan struct{}
}

// linkTo starts a link from a yard of its own to a stand-in edge that
// behaves as behave has it, with the timing tune gives the link; the test's
// end stops it.
func linkTo(t *testing.T, tune func(*Link), behave func(*standin.EdgeBehaviour)) *linked {
	t.Helper()
	edge, srv := edgeAt(t)
	if behave != nil {
		edge.Behave(behave)
	}
	h := &served{}
	l := &Link{
		Edge:    store.Edge{URL: srv.URL, Key: edge.PublicKey(), Handle: "mckean"},
		Key:     newKey(t),
		Handler: h,
		HTTP:    srv.Client(),
		Backoff: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
	}
	if tune != nil {
		tune(l)
	}
	y := &linked{edge: edge, link: l, served: h, more: make(chan struct{}, 1)}
	l.Told = func(s State) {
		y.mu.Lock()
		y.told = append(y.told, s)
		y.mu.Unlock()
		select {
		case y.more <- struct{}{}:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		l.Run(ctx)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(stop)
	y.stop = stop
	return y
}

// next is the next link the stand-in welcomed.
func (y *linked) next(t *testing.T) *standin.EdgeLink {
	t.Helper()
	select {
	case l := <-y.edge.Links():
		return l
	case <-time.After(10 * time.Second):
		t.Fatal("the yard's link never came up at the edge")
		return nil
	}
}

// until is the first state told, not yet looked at by an earlier until,
// that ok takes.
func (y *linked) until(t *testing.T, ok func(State) bool) State {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		y.mu.Lock()
		for len(y.told) > 0 {
			s := y.told[0]
			y.told = y.told[1:]
			if ok(s) {
				y.mu.Unlock()
				return s
			}
		}
		y.mu.Unlock()
		select {
		case <-y.more:
		case <-deadline:
			t.Fatalf("the link never came to that state; it is %+v", y.link.State())
		}
	}
}

func deliver(t *testing.T, l *standin.EdgeLink, f standin.EdgeFrame) standin.EdgeAnswer {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := l.Deliver(ctx, f)
	if err != nil {
		t.Fatalf("frame %s (seq %d) to %s was not answered: %v", f.ID, f.Seq, f.Path, err)
	}
	return a
}

func send(t *testing.T, l *standin.EdgeLink, f standin.EdgeFrame) {
	t.Helper()
	if err := l.Send(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

func hook(name string) (string, http.Header, []byte) {
	h := http.Header{}
	h.Set("X-Hub-Signature-256", "sha256=abc")
	h.Set("X-Github-Delivery", "d-"+name)
	return "/hooks/" + name, h, []byte(`{"zen":"` + name + `"}`)
}

// Frames from the edge, in the order it numbers them, are each served by
// the handler from RemoteAddr, with the sender's headers and body, and the
// handler's answer goes back whole on the frame's id. The link says it is
// up once welcomed.
func TestLinkServesTheEdgesFramesInOrder(t *testing.T) {
	t.Parallel()
	y := linkTo(t, nil, nil)
	l := y.next(t)
	if s := y.until(t, func(s State) bool { return s.Up }); s.Ups != 1 || s.Handle != "mckean" {
		t.Errorf("up as %+v", s)
	}
	var want []string
	for _, name := range []string{"github", "linear", "slack", "github", "github"} {
		path, h, body := hook(name)
		f := l.Frame(http.MethodPost, path+"?n=1", h, body, false)
		a := deliver(t, l, f)
		if a.ID != f.ID || a.Status != http.StatusAccepted || string(a.Body) != "seen "+path || a.Header.Get("X-Seen") != path {
			t.Errorf("frame %d answered %+v", f.Seq, a)
		}
		want = append(want, path+"?n=1")
	}
	y.stop()
	if got := y.served.paths(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("served %v, want %v", got, want)
	}
	for _, r := range y.served.reqs {
		if r.RemoteAddr != RemoteAddr || r.Signature != "sha256=abc" || !strings.HasPrefix(r.Body, `{"zen":`) {
			t.Errorf("served %+v", r)
		}
	}
}

// A body at the edge's 1 MiB limit and headers near the router's HTTP
// header limit fit in one frame after JSON encodes the body as base64.
func TestLinkAcceptsTheEdgesLargestHookFrame(t *testing.T) {
	t.Parallel()
	const headerBytes = 900 << 10
	const bodyBytes = store.MaxHookPayload
	seen := make(chan bool, 1)
	y := linkTo(t, func(l *Link) {
		l.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			seen <- err == nil && len(body) == bodyBytes && len(r.Header.Get("X-Large")) == headerBytes
			w.WriteHeader(http.StatusAccepted)
		})
	}, nil)
	l := y.next(t)
	f := l.Frame(http.MethodPost, "/hooks/large", http.Header{"X-Large": {strings.Repeat("x", headerBytes)}}, make([]byte, bodyBytes), false)
	if a := deliver(t, l, f); a.Status != http.StatusAccepted {
		t.Fatalf("the largest hook frame answered %+v", a)
	}
	select {
	case ok := <-seen:
		if !ok {
			t.Error("the yard received a frame with a changed body or header")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the yard did not serve the largest hook frame")
	}
}

// A frame is refused, and never reaches the handler, when it is not the
// edge's as it came: signed by another key, or changed after the edge
// signed it in a header the yard verifies with (the sender's signature, its
// delivery id), in its body, or in queued, which chooses the clock the
// sender's timestamp is checked by. The link stays up: the next frame is
// served.
func TestLinkRefusesAFrameNotTheEdgesAsItCame(t *testing.T) {
	t.Parallel()
	y := linkTo(t, nil, nil)
	l := y.next(t)
	other := newKey(t)
	for name, change := range map[string]func(*standin.EdgeFrame){
		"another key's":    func(f *standin.EdgeFrame) { f.Sig = l.Sign(other, *f) },
		"no signature":     func(f *standin.EdgeFrame) { f.Sig = "" },
		"signature header": func(f *standin.EdgeFrame) { f.Header.Set("X-Hub-Signature-256", "sha256=abd") },
		"delivery id":      func(f *standin.EdgeFrame) { f.Header.Set("X-Github-Delivery", "d-other") },
		"added header":     func(f *standin.EdgeFrame) { f.Header.Add("X-Github-Delivery", "d-other") },
		"body":             func(f *standin.EdgeFrame) { f.Body = append(f.Body, ' ') },
		"queued":           func(f *standin.EdgeFrame) { f.Queued = !f.Queued },
		"received":         func(f *standin.EdgeFrame) { f.Received = "2026-10-20T09:14:03Z" },
		"path":             func(f *standin.EdgeFrame) { f.Path = "/hooks/other" },
	} {
		path, h, body := hook("bad")
		bad := l.Frame(http.MethodPost, path, h, body, false)
		change(&bad)
		send(t, l, bad)
		gpath, gh, gbody := hook("good")
		if a := deliver(t, l, l.Frame(http.MethodPost, gpath, gh, gbody, false)); a.Status != http.StatusAccepted {
			t.Errorf("%s: the frame after it answered %+v", name, a)
		}
		for _, a := range l.Answers() {
			if a.ID == bad.ID {
				t.Errorf("a frame changed in its %s was answered: %+v", name, a)
			}
		}
	}
	// Every handler has returned once the link is stopped.
	y.stop()
	for _, p := range y.served.paths() {
		if p != "/hooks/good" {
			t.Errorf("served %s, a frame that is not the edge's", p)
		}
	}
	if s := y.link.State(); s.Ups != 1 {
		t.Errorf("a refused frame took the link down: %+v", s)
	}
}

// A frame the edge sent, sent a second time on the same link, is refused by
// its seq: a webhook delivery, and a device's call that would post twice.
func TestLinkRefusesAFrameSentAgain(t *testing.T) {
	t.Parallel()
	y := linkTo(t, nil, nil)
	l := y.next(t)
	hookPath, h, body := hook("github")
	frames := []standin.EdgeFrame{
		l.Frame(http.MethodPost, hookPath, h, body, false),
		l.Frame(http.MethodPost, "/user/call/PostMessage", http.Header{"Authorization": {"Bearer t"}}, []byte(`{"text":"once"}`), false),
	}
	for _, f := range frames {
		deliver(t, l, f)
		send(t, l, f)
		// And one numbered before it, signed as the edge would sign it.
		older := f
		older.Seq--
		older.Sig = l.Sign(nil, older)
		send(t, l, older)
	}
	last := l.Frame(http.MethodGet, "/hooks/after", nil, nil, false)
	deliver(t, l, last)
	y.stop()
	got := strings.Join(y.served.paths(), " ")
	if got != hookPath+" /user/call/PostMessage /hooks/after" {
		t.Errorf("served %s: a frame sent again was served again", got)
	}
	if n := len(l.Answers()); n != 3 {
		t.Errorf("%d answers, want one for each frame served", n)
	}
}

// A frame recorded from one link is refused on the next: the session it
// was signed over is the other socket's, though its seq is new there.
func TestLinkRefusesAFrameFromAnotherLink(t *testing.T) {
	t.Parallel()
	y := linkTo(t, nil, nil)
	first := y.next(t)
	path, h, body := hook("github")
	recorded := first.Frame(http.MethodPost, path, h, body, false)
	deliver(t, first, recorded)
	first.Close()
	second := y.next(t)
	y.until(t, func(s State) bool { return s.Up && s.Ups == 2 })
	send(t, second, recorded)
	deliver(t, second, second.Frame(http.MethodGet, "/hooks/after", nil, nil, false))
	y.stop()
	if got := strings.Join(y.served.paths(), " "); got != path+" /hooks/after" {
		t.Errorf("served %s: the first link's frame was served again on the second", got)
	}
}

// A handler's context ends when its socket does, so a handler waiting on the
// request does not keep the old link from reconnecting.
func TestLinkCancelsHandlersWhenSocketCloses(t *testing.T) {
	t.Parallel()
	started, canceled := make(chan struct{}), make(chan struct{})
	y := linkTo(t, func(l *Link) {
		l.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/user/events" {
				close(started)
				<-r.Context().Done()
				close(canceled)
				return
			}
			w.WriteHeader(http.StatusAccepted)
		})
	}, nil)
	first := y.next(t)
	send(t, first, first.Frame(http.MethodGet, "/user/events", nil, nil, false))
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler did not start")
	}
	first.Close()
	select {
	case <-canceled:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler's request context stayed live after the socket ended")
	}
	second := y.next(t)
	if a := deliver(t, second, second.Frame(http.MethodGet, "/hooks/reconnected", nil, nil, false)); a.Status != http.StatusAccepted {
		t.Errorf("the reconnected link answered %+v", a)
	}
}

// A socket that falls silent, with no close either side sees, is counted
// dead after DeadAfter, told as unanswered pings, and dialled again; the
// new link serves.
func TestLinkDialsAgainAfterASilentSocket(t *testing.T) {
	t.Parallel()
	y := linkTo(t, func(l *Link) { l.PingEvery, l.DeadAfter = 20*time.Millisecond, 200*time.Millisecond }, nil)
	first := y.next(t)
	first.Silence()
	down := y.until(t, func(s State) bool { return !s.Up })
	if down.Reason != DownPings {
		t.Errorf("down as %+v, want %q", down, DownPings)
	}
	second := y.next(t)
	if a := deliver(t, second, second.Frame(http.MethodGet, "/hooks/back", nil, nil, false)); a.Status != http.StatusAccepted {
		t.Errorf("the new link answered %+v", a)
	}
	if s := y.until(t, func(s State) bool { return s.Up }); s.Reconnects() != 1 {
		t.Errorf("up again as %+v", s)
	}
}

// A link that hears the edge's pongs is alive: it stays up well past
// DeadAfter, for as long as it hears something within each DeadAfter.
func TestLinkThatHearsTheEdgeStaysUp(t *testing.T) {
	t.Parallel()
	y := linkTo(t, func(l *Link) { l.PingEvery, l.DeadAfter = 10*time.Millisecond, 60*time.Millisecond }, nil)
	l := y.next(t)
	// Twenty pongs are some 200 ms of pings, more than three times
	// DeadAfter.
	for deadline := time.Now().Add(10 * time.Second); l.Pongs() < 20; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the edge answered %d pings", l.Pongs())
		}
	}
	if s := y.link.State(); !s.Up || s.Ups != 1 {
		t.Fatalf("a link that heard the edge went down: %+v", s)
	}
	deliver(t, l, l.Frame(http.MethodGet, "/hooks/still", nil, nil, false))
}

// A challenge the yard cannot take for the edge's, signed by another key or
// over a nonce that is not this socket's, is not answered: no hello, so the
// edge never welcomes the link, and it is told down as a refused key.
func TestLinkRefusesAChallengeNotTheEdges(t *testing.T) {
	t.Parallel()
	for name, behave := range map[string]func(*standin.EdgeBehaviour){
		"another key":   func(b *standin.EdgeBehaviour) { b.ChallengeSigner = newKey(t) },
		"another nonce": func(b *standin.EdgeBehaviour) { b.ChallengeOverNonce = make([]byte, yardkey.NonceSize) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			y := linkTo(t, nil, behave)
			s := y.until(t, func(s State) bool { return !s.Up })
			if s.Reason != DownRefusedKey || !strings.Contains(s.Detail, "not signed by the edge's key") {
				t.Errorf("told %+v", s)
			}
			y.stop()
			if y.edge.Upgrades() == 0 {
				t.Fatal("the yard never dialled")
			}
			select {
			case <-y.edge.Links():
				t.Error("the edge welcomed a link whose challenge was not its own: the yard answered it")
			default:
			}
		})
	}
}

// An answer larger than the edge passes on is sent cut one byte past
// MaxAnswer: the edge takes it as no answer either way, and the frame stays
// under what it reads of one.
func TestLinkCutsAnAnswerTooLargeToPassOn(t *testing.T) {
	t.Parallel()
	y := linkTo(t, func(l *Link) {
		l.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write(make([]byte, 3*MaxAnswer))
		})
	}, nil)
	l := y.next(t)
	if a := deliver(t, l, l.Frame(http.MethodGet, "/hooks/big", nil, nil, false)); len(a.Body) != MaxAnswer+1 || a.Status != http.StatusOK {
		t.Errorf("answered %d with %d bytes", a.Status, len(a.Body))
	}
}
