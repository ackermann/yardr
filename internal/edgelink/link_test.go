package edgelink

import (
	"bytes"
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

// deliverAcrossRedial delivers frame's request on l, or on the link the yard
// dials after l when l dies first, and returns the answer with the link that
// served it. A link the test wants to deliver on can die under a loaded
// machine through no silence of its own: with DeadAfter in the hundreds of
// milliseconds, one scheduling stall of that length leaves its pongs unheard
// and the yard dials again. The frame is built anew on each link, since it
// is signed over the link's own session. A deliver that fails on a link that
// is still alive is the test's failure, not a flap.
func deliverAcrossRedial(t *testing.T, y *linked, l *standin.EdgeLink, frame func(*standin.EdgeLink) standin.EdgeFrame) (standin.EdgeAnswer, *standin.EdgeLink) {
	t.Helper()
	var f standin.EdgeFrame
	for outlived := 0; outlived < 4; outlived++ {
		f = frame(l)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		a, err := l.Deliver(ctx, f)
		cancel()
		if err == nil {
			return a, l
		}
		select {
		case <-l.Done():
			l = y.next(t)
		case <-time.After(10 * time.Second):
			t.Fatalf("frame %s (seq %d) to %s was not answered: %v", f.ID, f.Seq, f.Path, err)
		}
	}
	t.Fatalf("frame to %s was not answered: four links in a row died under the deliver", f.Path)
	return standin.EdgeAnswer{}, nil
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
	for _, role := range []string{"", "web"} {
		t.Run("role="+role, func(t *testing.T) {
			t.Parallel()
			y := linkTo(t, func(l *Link) { l.Role = role }, nil)
			l := y.next(t)
			if got := l.Header.Values("Yardr-Link-Role"); (role == "" && len(got) != 0) || (role != "" && (len(got) != 1 || got[0] != role)) {
				t.Fatalf("upgrade role headers = %v, want %q", got, role)
			}
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
		})
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
// new link serves. The millisecond row pings hundreds of times across the
// silence window, so a write that ended the link would cut it short as a
// closed socket instead.
func TestLinkDialsAgainAfterASilentSocket(t *testing.T) {
	t.Parallel()
	for _, ping := range []struct {
		name  string
		every time.Duration
	}{
		{"twenty milliseconds", 20 * time.Millisecond},
		{"a millisecond", time.Millisecond},
	} {
		t.Run(ping.name, func(t *testing.T) {
			t.Parallel()
			y := linkTo(t, func(l *Link) { l.PingEvery, l.DeadAfter = ping.every, 200*time.Millisecond }, nil)
			first := y.next(t)
			first.Silence()
			down := y.until(t, func(s State) bool { return !s.Up })
			if down.Reason != DownPings {
				t.Errorf("down as %+v, want %q", down, DownPings)
			}
			second := y.next(t)
			// The deliver rides a redial of its own when a loaded machine kills
			// the new link first: its pongs go unheard through no silence.
			a, _ := deliverAcrossRedial(t, y, second, func(l *standin.EdgeLink) standin.EdgeFrame {
				return l.Frame(http.MethodGet, "/hooks/back", nil, nil, false)
			})
			if a.Status != http.StatusAccepted {
				t.Errorf("the new link answered %+v", a)
			}
			if s := y.until(t, func(s State) bool { return s.Up }); s.Reconnects() != 1 {
				t.Errorf("up again as %+v", s)
			}

			// A deliberate second silence pins the redial path the loaded
			// deliver above must tolerate when its link dies first.
			second.Silence()
			if down := y.until(t, func(s State) bool { return !s.Up }); down.Reason != DownPings {
				t.Errorf("down as %+v, want %q", down, DownPings)
			}
			a, served := deliverAcrossRedial(t, y, second, func(l *standin.EdgeLink) standin.EdgeFrame {
				return l.Frame(http.MethodGet, "/hooks/back", nil, nil, false)
			})
			if served == second {
				t.Error("the deliver was answered on the dead link itself, not on the redial")
			}
			if a.Status != http.StatusAccepted {
				t.Errorf("the redial answered %+v", a)
			}
			if s := y.until(t, func(s State) bool { return s.Up }); s.Reconnects() != 2 {
				t.Errorf("up again as %+v", s)
			}
		})
	}
}

// A link that hears the edge's pongs is alive: it stays up beyond DeadAfter
// for as long as it hears something within each DeadAfter.
func TestLinkThatHearsTheEdgeStaysUp(t *testing.T) {
	t.Parallel()
	y := linkTo(t, func(l *Link) { l.PingEvery, l.DeadAfter = 50*time.Millisecond, 2*time.Second }, nil)
	l := y.next(t)
	// Forty-five pongs take longer than DeadAfter, so a link that did not
	// reset the heard timer would have closed before the last one.
	for i := 0; i < 45; i++ {
		select {
		case <-l.PongEvents():
		case <-l.Done():
			t.Fatalf("the edge answered only %d pings before the link closed", l.Pongs())
		case <-time.After(10 * time.Second):
			t.Fatalf("the edge answered only %d pings", l.Pongs())
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

// An answer that encodes past the edge's 1 MiB frame limit goes as a stream
// instead: the head marked with stream and an empty body, the body in data
// frames, then end. An 8 MiB blob crosses whole, and the link serves the
// next call as before.
func TestLinkStreamsAnAnswerLargerThanOneFrame(t *testing.T) {
	t.Parallel()
	body := make([]byte, 8<<20)
	for i := range body {
		body[i] = byte(i*31 + 7)
	}
	shortURL := "/user/blobs/" + strings.Repeat("a", 64)
	largeURL := "/user/blobs/" + strings.Repeat("b", 64)
	y := linkTo(t, func(l *Link) {
		l.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/user/blobs/") {
				w.Header().Set("Content-Type", "application/octet-stream")
				if r.URL.Path == shortURL {
					w.Write(body[:900<<10])
				} else {
					w.Write(body)
				}
				return
			}
			w.WriteHeader(http.StatusOK)
		})
	}, nil)
	l := y.next(t)
	streamAnswer := func(path string, want []byte) {
		t.Helper()
		f := l.Frame(http.MethodGet, path, nil, nil, false)
		head := deliver(t, l, f)
		if !head.Stream || head.Status != http.StatusOK || head.Header.Get("Content-Type") != "application/octet-stream" || len(head.Body) != 0 {
			t.Fatalf("the head is %+v, want a streamed 200 octet-stream with an empty body", head)
		}
		var got []byte
		for len(got) < len(want) {
			select {
			case d := <-l.DataEvents():
				if d.ID != f.ID {
					t.Fatalf("the data is for %s, want %s", d.ID, f.ID)
				}
				if len(d.Body) > maxDataBody {
					t.Fatalf("a data piece is %d bytes, over maxDataBody %d", len(d.Body), maxDataBody)
				}
				got = append(got, d.Body...)
			case <-time.After(10 * time.Second):
				t.Fatalf("received %d of %d bytes", len(got), len(want))
			}
		}
		if !bytes.Equal(got, want) {
			t.Fatal("the reassembled body differs from the blob it answers")
		}
		select {
		case id := <-l.EndEvents():
			if id != f.ID {
				t.Errorf("the end is for %s, want the stream's %s", id, f.ID)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no end after the streamed answer")
		}
	}
	// The original 900 KiB case just crossed the one-frame limit. Keep it
	// alongside the full blob so both the old boundary and the edge bound fit.
	streamAnswer(shortURL, body[:900<<10])
	streamAnswer(largeURL, body)
	if a := deliver(t, l, l.Frame(http.MethodGet, "/hooks/after", nil, nil, false)); a.Status != http.StatusOK {
		t.Errorf("the call after the stream answered %+v", a)
	}
	if s := y.link.State(); !s.Up || s.Ups != 1 {
		t.Errorf("the link went down on the streamed answer: %+v", s)
	}
}

// Only a head that alone encodes past the edge's 1 MiB frame limit, its
// headers, is answered 413 with a short error: the link stays up for the
// next call.
func TestLinkAnswers413WhenTheHeadAloneIsLargerThanOneFrame(t *testing.T) {
	t.Parallel()
	y := linkTo(t, func(l *Link) {
		l.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/user/big-head" {
				w.Header().Set("X-Pad", strings.Repeat("x", 1<<20))
				w.Write([]byte("small"))
				return
			}
			w.WriteHeader(http.StatusOK)
		})
	}, nil)
	l := y.next(t)
	big := l.Frame(http.MethodGet, "/user/big-head", nil, nil, false)
	if a := deliver(t, l, big); a.Status != http.StatusRequestEntityTooLarge || len(a.Body) == 0 || len(a.Body) > 1024 {
		t.Errorf("the oversized head was answered %d with %d bytes", a.Status, len(a.Body))
	}
	select {
	case id := <-l.EndEvents():
		if id != big.ID {
			t.Errorf("the end is for %s, want the 413's %s", id, big.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no end after the 413")
	}
	if a := deliver(t, l, l.Frame(http.MethodGet, "/hooks/after", nil, nil, false)); a.Status != http.StatusOK {
		t.Errorf("the call after the 413 answered %+v", a)
	}
	if s := y.link.State(); !s.Up || s.Ups != 1 {
		t.Errorf("the link went down on the oversized head: %+v", s)
	}
}

// A flushed head that alone is too large is refused 413 with nothing after
// it: what the handler wrote before or after the flush crosses as no data.
func TestLinkSendsNoBodyAfterAFlushedOversizedHead(t *testing.T) {
	t.Parallel()
	y := linkTo(t, func(l *Link) {
		l.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/user/big-head-flush" {
				w.Header().Set("X-Pad", strings.Repeat("x", 1<<20))
				w.Write([]byte("first"))
				w.(http.Flusher).Flush()
				w.Write([]byte("second"))
				return
			}
			w.WriteHeader(http.StatusOK)
		})
	}, nil)
	l := y.next(t)
	f := l.Frame(http.MethodGet, "/user/big-head-flush", nil, nil, false)
	if a := deliver(t, l, f); a.Status != http.StatusRequestEntityTooLarge || len(a.Body) == 0 || len(a.Body) > 1024 {
		t.Errorf("the flushed oversized head was answered %d with %d bytes", a.Status, len(a.Body))
	}
	pieces := 0
	ended := false
	for !ended {
		select {
		case <-l.DataEvents():
			pieces++
		case id := <-l.EndEvents():
			if id != f.ID {
				t.Errorf("the end is for %s, want the 413's %s", id, f.ID)
			}
			ended = true
		case <-time.After(10 * time.Second):
			t.Fatal("no end after the 413")
		}
	}
	// The yard sends data before the end, so once the end is here every
	// piece already waits; drain without waiting.
drain:
	for {
		select {
		case <-l.DataEvents():
			pieces++
		default:
			break drain
		}
	}
	if pieces != 0 {
		t.Errorf("the 413 carried %d data pieces", pieces)
	}
	if a := deliver(t, l, l.Frame(http.MethodGet, "/hooks/after", nil, nil, false)); a.Status != http.StatusOK {
		t.Errorf("the call after the 413 answered %+v", a)
	}
	if s := y.link.State(); !s.Up || s.Ups != 1 {
		t.Errorf("the link went down on the flushed oversized head: %+v", s)
	}
}
