package dispatch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
	"yardr.dev/yardr/internal/yards"
)

// peerClock is the one clock of every yard in a test.
type peerClock struct{ now time.Time }

// peerYard is a yard with a key, a receiver on a listener of its own and a
// daemon. down makes the listener answer as something that is not the
// yard: a proxy with nothing behind it.
type peerYard struct {
	name  string
	api   store.API
	own   *store.Store
	pub   string
	srv   *httptest.Server
	rc    *yards.Receiver
	d     *dispatch.Dispatcher
	down  atomic.Bool
	asked atomic.Int64 // requests that reached the listener
}

// peerSendDisallowedHook changes a link after the dispatcher's peer
// snapshot but before its delivery-time allowance check.
type peerSendDisallowedHook struct {
	store.API
	before func()
}

func (h *peerSendDisallowedHook) PeerSendDisallowed(ctx context.Context, name string, seq uint64) (bool, error) {
	if h.before != nil {
		before := h.before
		h.before = nil
		before()
	}
	return h.API.PeerSendDisallowed(ctx, name, seq)
}

func newPeerYard(t *testing.T, name string, clock *peerClock) *peerYard {
	t.Helper()
	api, own := storetest.NewOwned(t)
	keyFile := filepath.Join(t.TempDir(), "peer.key")
	key, _, err := yardkey.Ensure(keyFile)
	must(t, err)
	y := &peerYard{name: name, api: api, own: own, pub: yardkey.Public(key)}
	// As the server says it on opening the store: a bead that crosses a
	// link names the yards it passes by their keys.
	own.SetYard(keyFile, "test")
	y.rc = yards.NewReceiver(own, keyFile, nil)
	y.rc.SetClock(func() time.Time { return clock.now })
	inner := y.rc.Handler()
	y.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		y.asked.Add(1)
		if y.down.Load() {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(y.srv.Close)
	y.d = &dispatch.Dispatcher{Store: api, Runners: map[string]dispatch.Runner{store.PeerRunner: &runner.Peer{Store: api}}, PeerKey: keyFile}
	dispatch.SetClock(y.d, func() time.Time { return clock.now })
	return y
}

// link makes a and b peers of each other, each under the other's name, with
// ping allowed both ways; reachable says whether a is given b's URL, and b
// a's.
func link(t *testing.T, a, b *peerYard, aCallsB, bCallsA bool) {
	t.Helper()
	ctx := context.Background()
	for _, l := range []struct {
		in, other *peerYard
		url       bool
	}{{a, b, aCallsB}, {b, a, bCallsA}} {
		p := store.Peer{Name: l.other.name, Key: l.other.pub}
		if l.url {
			p.URL = l.other.srv.URL
		}
		_, err := l.in.api.AddPeer(ctx, p, "jo")
		must(t, err)
		_, err = l.in.api.AllowPeer(ctx, p.Name, store.PeerAllow{Send: []string{"ping"}, Receive: []string{"ping"}}, "jo")
		must(t, err)
	}
}

func (y *peerYard) pass(t *testing.T) {
	t.Helper()
	must(t, y.d.Reconcile(context.Background()))
}

func (y *peerYard) send(t *testing.T, to string) string {
	t.Helper()
	m, err := y.api.PeerSend(context.Background(), to, "ping", nil, "jo")
	must(t, err)
	return m.ID
}

func (y *peerYard) events(t *testing.T, kind string) []store.Event {
	t.Helper()
	evs, err := y.api.Events(context.Background(), "", 500)
	must(t, err)
	var out []store.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// pong is the event of the pong that answers the ping id, if it has come.
func (y *peerYard) pong(t *testing.T, id string) *store.Event {
	t.Helper()
	for _, e := range y.events(t, "peer_message_received") {
		if e.Data["re"] == id {
			return &e
		}
	}
	return nil
}

func (y *peerYard) waiting(t *testing.T, peer string) []store.PeerOut {
	t.Helper()
	out, err := y.api.PeerOutbox(context.Background(), peer, 0)
	must(t, err)
	return out
}

// If a kind becomes allowed again between the pass's peer snapshot and the
// store's delivery check, the still-waiting message stays ahead of later
// messages in the outbox.
func TestPeerSendReallowedWhileWaitingDoesNotLetMessagesOvertake(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	a.yardmaster(t, "b")
	b.yardmaster(t, "a")
	ctx := context.Background()
	for _, setting := range []struct {
		yard *peerYard
		peer string
	}{{a, "b"}, {b, "a"}} {
		_, err := setting.yard.api.AllowPeer(ctx, setting.peer, store.PeerAllow{
			Send: []string{store.PeerPing, store.PeerMail}, Receive: []string{store.PeerPing, store.PeerMail},
		}, "jo")
		must(t, err)
	}
	mail, err := b.api.MailPeer(ctx, "yardmaster", "a", "later mail", "yardmaster")
	must(t, err)
	ping, err := b.api.PeerSend(ctx, "a", store.PeerPing, nil, "jo")
	must(t, err)
	_, err = b.api.AllowPeer(ctx, "a", store.PeerAllow{NoSend: []string{store.PeerMail}}, "jo")
	must(t, err)

	// Re-enable mail after Reconcile has read the now-disallowed peer, but
	// before push checks the old head of its outbox.
	b.d.Store = &peerSendDisallowedHook{API: b.api, before: func() {
		_, err := b.api.AllowPeer(ctx, "a", store.PeerAllow{Send: []string{store.PeerPing, store.PeerMail}}, "jo")
		must(t, err)
	}}
	b.pass(t)
	b.d.Store = b.api
	if got := b.waiting(t, "a"); len(got) != 2 || got[0].ID != mail.ID || got[1].ID != ping.ID {
		t.Fatalf("outbox after the stale disallow check = %+v, want both messages still waiting in order", got)
	}
	if got := b.events(t, "peer_message_sent"); len(got) != 0 {
		t.Fatalf("a later message crossed while the first remained waiting: %+v", got)
	}

	b.pass(t)
	sent := b.events(t, "peer_message_sent")
	if len(sent) < 2 || sent[0].Data["id"] != mail.ID || sent[1].Data["id"] != ping.ID {
		t.Fatalf("messages were not delivered in outbox order: %+v", sent)
	}
}

func newClock() *peerClock { return &peerClock{now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)} }

// Two yards that can both be called: the ping is posted, the pong is
// posted back, and each is logged once on each side.
func TestPeersPush(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	id := a.send(t, "b")
	a.pass(t)
	if got := b.events(t, "peer_message_received"); len(got) != 1 || got[0].Data["id"] != id || got[0].Data["way"] != "push" || got[0].Data["peer"] != "a" {
		t.Fatalf("b received %+v", got)
	}
	if sent := a.events(t, "peer_message_sent"); len(sent) != 1 || sent[0].Data["id"] != id || sent[0].Data["way"] != "push" || sent[0].Data["peer"] != "b" {
		t.Fatalf("a sent %+v", sent)
	}
	if a.pong(t, id) != nil {
		t.Fatal("the pong came before b's daemon made a pass")
	}
	b.pass(t)
	if pong := a.pong(t, id); pong == nil || pong.Data["way"] != "push" || pong.Data["kind"] != "ping" {
		t.Fatalf("a's pong = %+v", pong)
	}
	if len(a.waiting(t, "b"))+len(b.waiting(t, "a")) != 0 {
		t.Fatal("messages still wait")
	}
	// More passes change nothing: nothing is sent or taken twice.
	a.pass(t)
	b.pass(t)
	if len(a.events(t, "peer_message_received")) != 1 || len(b.events(t, "peer_message_received")) != 1 ||
		len(a.events(t, "peer_message_sent")) != 1 || len(b.events(t, "peer_message_sent")) != 1 {
		t.Fatal("a further pass sent or took a message again")
	}
	// Asking a yard that posts ends nothing there, so it writes nothing:
	// a question every few seconds must not wake the yard asked.
	headA, err := a.api.Head(context.Background())
	must(t, err)
	b.pass(t)
	b.pass(t)
	if head, _ := a.api.Head(context.Background()); head != headA {
		t.Fatalf("b's questions wrote to a's store: head %d -> %d", headA, head)
	}
}

// b cannot be called: a never calls it. b posts what it has for a and asks
// a for what waits there, so a ping crosses either way with only a
// reachable.
func TestPeersPull(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)

	// a pings b: the ping is pulled, the pong posted.
	id := a.send(t, "b")
	a.pass(t)
	if b.asked.Load() != 0 || len(a.waiting(t, "b")) != 1 {
		t.Fatalf("a called a peer without a URL (%d requests), or its ping does not wait", b.asked.Load())
	}
	b.pass(t)
	if got := b.events(t, "peer_message_received"); len(got) != 1 || got[0].Data["id"] != id || got[0].Data["way"] != "pull" {
		t.Fatalf("b received %+v", got)
	}
	if pong := a.pong(t, id); pong == nil || pong.Data["way"] != "push" {
		t.Fatalf("a's pong, which b posts in the pass that pulled the ping = %+v", pong)
	}
	// a logs the ping as sent once b's next question says it has it.
	if len(a.events(t, "peer_message_sent")) != 0 {
		t.Fatal("a logged the ping as sent before b said it has it")
	}
	b.pass(t)
	if sent := a.events(t, "peer_message_sent"); len(sent) != 1 || sent[0].Data["id"] != id || sent[0].Data["way"] != "pull" {
		t.Fatalf("a sent %+v", sent)
	}

	// b pings a: the ping is posted, the pong pulled.
	id = b.send(t, "a")
	b.pass(t)
	if got := a.events(t, "peer_message_received"); len(got) != 2 || got[1].Data["id"] != id || got[1].Data["way"] != "push" {
		t.Fatalf("a received %+v", got)
	}
	if pong := b.pong(t, id); pong == nil || pong.Data["way"] != "pull" {
		t.Fatalf("b's pong = %+v", pong)
	}
	a.pass(t)
	b.pass(t)
	if b.asked.Load() != 0 {
		t.Fatalf("b was called %d times", b.asked.Load())
	}
	if len(a.events(t, "peer_message_received")) != 2 || len(b.events(t, "peer_message_received")) != 2 {
		t.Fatal("a message was taken twice")
	}
	if len(a.waiting(t, "b"))+len(b.waiting(t, "a")) != 0 {
		t.Fatal("messages still wait")
	}
}

// b's listener is down for a while: the message is tried again after a
// wait that grows, nothing overtakes it, and when b is back it arrives
// once, with the ones behind it in order.
func TestPeersListenerDownThenUp(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	b.down.Store(true)
	first, second := a.send(t, "b"), a.send(t, "b")

	a.pass(t)
	if b.asked.Load() != 1 {
		t.Fatalf("with b down, a made %d requests in a pass, want the one for its oldest message", b.asked.Load())
	}
	w := a.waiting(t, "b")
	if len(w) != 2 || w[0].ID != first || w[0].Attempts != 1 || w[0].Error == "" || !w[0].RetryAt.Equal(clock.now.Add(30*time.Second)) || w[1].Attempts != 0 {
		t.Fatalf("after the first try: %+v", w)
	}
	// Not due: no request, however many passes.
	a.pass(t)
	clock.now = clock.now.Add(29 * time.Second)
	a.pass(t)
	if b.asked.Load() != 1 {
		t.Fatalf("a called b again before the retry was due (%d requests)", b.asked.Load())
	}
	// Due, and b still down: the wait doubles.
	clock.now = clock.now.Add(time.Second)
	a.pass(t)
	if w := a.waiting(t, "b"); w[0].Attempts != 2 || !w[0].RetryAt.Equal(clock.now.Add(time.Minute)) || w[1].Attempts != 0 {
		t.Fatalf("after the second try: %+v", w)
	}
	if len(a.events(t, "peer_message_failed"))+len(a.events(t, "peer_message_sent")) != 0 {
		t.Fatal("a try that failed was logged")
	}

	b.down.Store(false)
	a.pass(t) // not due yet
	if len(b.events(t, "peer_message_received")) != 0 {
		t.Fatal("the message was posted before its retry was due")
	}
	clock.now = clock.now.Add(time.Minute)
	a.pass(t)
	got := b.events(t, "peer_message_received")
	if len(got) != 2 || got[0].Data["id"] != first || got[1].Data["id"] != second {
		t.Fatalf("b received %+v, want the two in order", got)
	}
	sent := a.events(t, "peer_message_sent")
	if len(sent) != 2 || sent[0].Data["id"] != first || sent[0].Data["attempts"] != float64(3) && sent[0].Data["attempts"] != 3 || sent[1].Data["attempts"] != nil {
		t.Fatalf("a sent %+v", sent)
	}
	a.pass(t)
	b.pass(t)
	a.pass(t)
	if len(b.events(t, "peer_message_received")) != 2 || a.pong(t, first) == nil || a.pong(t, second) == nil {
		t.Fatal("the messages did not arrive once each, or their pongs did not come")
	}
}

// A message that fails PeerTries times is given up, with an event, and
// gets out of the way of the one behind it.
func TestPeersGiveUp(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	a.d.PeerTries = 3
	b.down.Store(true)
	first, second := a.send(t, "b"), a.send(t, "b")
	for i := 0; i < 3; i++ {
		a.pass(t)
		clock.now = clock.now.Add(time.Hour)
	}
	failed := a.events(t, "peer_message_failed")
	if len(failed) != 1 || failed[0].Data["id"] != first || failed[0].Data["peer"] != "b" || failed[0].Data["kind"] != "ping" ||
		failed[0].Data["attempts"] != float64(3) && failed[0].Data["attempts"] != 3 || failed[0].Data["reason"] == "" {
		t.Fatalf("peer_message_failed = %+v", failed)
	}
	p, err := a.api.GetPeer(context.Background(), "b")
	must(t, err)
	if p.Failed != 1 || p.Waiting != 1 {
		t.Fatalf("a's outbox = %+v", p)
	}
	b.down.Store(false)
	a.pass(t)
	if got := b.events(t, "peer_message_received"); len(got) != 1 || got[0].Data["id"] != second {
		t.Fatalf("b received %+v, want the message behind the one given up", got)
	}
	for i := 0; i < 3; i++ {
		clock.now = clock.now.Add(time.Hour)
		a.pass(t)
	}
	if len(b.events(t, "peer_message_received")) != 1 || len(a.events(t, "peer_message_failed")) != 1 {
		t.Fatal("a message given up was posted again")
	}
}

// b does not take the kind: its signed 403 gives the message up at once,
// and b logs what it refused. A third yard b has not added gets no such
// word: it is refused like anyone, and keeps trying in vain.
func TestPeersRefused(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b, c := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock), newPeerYard(t, "c", clock)
	link(t, a, b, true, true)
	ctx := context.Background()
	_, err := b.api.AllowPeer(ctx, "a", store.PeerAllow{NoReceive: []string{"ping"}}, "jo")
	must(t, err)
	id := a.send(t, "b")
	a.pass(t)
	failed := a.events(t, "peer_message_failed")
	if len(failed) != 1 || failed[0].Data["id"] != id || failed[0].Data["attempts"] != float64(1) && failed[0].Data["attempts"] != 1 {
		t.Fatalf("a's peer_message_failed = %+v", failed)
	}
	if ref := b.events(t, "peer_message_refused"); len(ref) != 1 || ref[0].Data["id"] != id || ref[0].Data["direction"] != "in" || ref[0].Data["peer"] != "a" {
		t.Fatalf("b's peer_message_refused = %+v", ref)
	}
	if len(b.events(t, "peer_message_received")) != 0 || len(a.waiting(t, "b")) != 0 {
		t.Fatal("the refused message was taken, or still waits")
	}

	// c has b's invite and added it; b never added c.
	_, err = c.api.AddPeer(ctx, store.Peer{Name: "b", Key: b.pub, URL: b.srv.URL}, "jo")
	must(t, err)
	_, err = c.api.AllowPeer(ctx, "b", store.PeerAllow{Send: []string{"ping"}, Receive: []string{"ping"}}, "jo")
	must(t, err)
	c.send(t, "b")
	before := b.rc.Rejected()["unknown_peer"]
	c.pass(t)
	if w := c.waiting(t, "b"); len(w) != 1 || w[0].Attempts != 1 {
		t.Fatalf("c's message to a yard that has not added it: %+v", w)
	}
	if b.rc.Rejected()["unknown_peer"] != before+1 || len(b.events(t, "peer_message_received")) != 0 || len(b.events(t, "peer_message_refused")) != 1 {
		t.Fatalf("b's rejections = %v", b.rc.Rejected())
	}
	if len(c.events(t, "peer_message_failed")) != 0 {
		t.Fatal("an unsigned 401 gave c's message up at once: anyone on the way could do that")
	}
}

// A yard with peers and no key sends and asks for nothing, and its passes
// do not fail for it.
func TestPeersWithoutAKey(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	a.d.PeerKey = filepath.Join(t.TempDir(), "none.key")
	a.send(t, "b")
	a.pass(t)
	if b.asked.Load() != 0 || len(a.waiting(t, "b")) != 1 {
		t.Fatalf("a yard without a key made %d requests", b.asked.Load())
	}
}

// A peer renamed while a message for it waits on a retry gets the message
// at the retry, under its new name, and its pong comes back. A yard added
// under the name the rename freed is another yard: it is called at once,
// not left alone for the failures of the one that had the name.
func TestPeersRenamed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := newClock()
	a, b, c := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock), newPeerYard(t, "c", clock)
	link(t, a, b, true, true)
	b.down.Store(true)
	first := a.send(t, "b")
	a.pass(t)
	if w := a.waiting(t, "b"); len(w) != 1 || w[0].Attempts != 1 {
		t.Fatalf("with b down: %+v", w)
	}

	_, err := a.api.RenamePeer(ctx, "b", "bee", "jo")
	must(t, err)
	// c takes the name b had, on both sides.
	_, err = a.api.AddPeer(ctx, store.Peer{Name: "b", Key: c.pub, URL: c.srv.URL}, "jo")
	must(t, err)
	_, err = a.api.AllowPeer(ctx, "b", store.PeerAllow{Send: []string{"ping"}, Receive: []string{"ping"}}, "jo")
	must(t, err)
	_, err = c.api.AddPeer(ctx, store.Peer{Name: "a", Key: a.pub, URL: a.srv.URL}, "jo")
	must(t, err)
	_, err = c.api.AllowPeer(ctx, "a", store.PeerAllow{Send: []string{"ping"}, Receive: []string{"ping"}}, "jo")
	must(t, err)
	second := a.send(t, "b")
	a.pass(t)
	if got := c.events(t, "peer_message_received"); len(got) != 1 || got[0].Data["id"] != second {
		t.Fatalf("c, added under the name b had, received %+v in the pass after; want its ping", got)
	}
	if w := a.waiting(t, "bee"); len(w) != 1 || w[0].ID != first || w[0].Attempts != 1 {
		t.Fatalf("what waited for the renamed peer: %+v", w)
	}

	b.down.Store(false)
	clock.now = clock.now.Add(30 * time.Second)
	a.pass(t)
	if got := b.events(t, "peer_message_received"); len(got) != 1 || got[0].Data["id"] != first {
		t.Fatalf("b received %+v, want the ping from before the rename", got)
	}
	b.pass(t)
	c.pass(t)
	a.pass(t)
	if pong := a.pong(t, first); pong == nil || pong.Data["peer"] != "bee" {
		t.Fatalf("the pong of the renamed peer = %+v", pong)
	}
	if pong := a.pong(t, second); pong == nil || pong.Data["peer"] != "b" {
		t.Fatalf("the pong of the yard added under the old name = %+v", pong)
	}
}

// The daemons' own requests carry what each yard's build understands: with
// only a called, and nothing to deliver, one pass of b tells both. b hears
// it from a's answer to its question, a from the question.
func TestPeersSayWhatTheyUnderstand(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	said := func(y *peerYard, peer string) *store.PeerSaid {
		t.Helper()
		p, err := y.api.GetPeer(context.Background(), peer)
		must(t, err)
		return p.Said
	}
	a.pass(t) // a calls no one
	if said(a, "b") != nil || said(b, "a") != nil {
		t.Fatal("a link nothing crossed has said something")
	}
	b.pass(t)
	for _, got := range []*store.PeerSaid{said(a, "b"), said(b, "a")} {
		if got == nil || !slices.Equal(got.Understands, store.PeerKinds) || got.At.IsZero() {
			t.Fatalf("after b's pass: a says of b %+v, b of a %+v", said(a, "b"), said(b, "a"))
		}
	}
}
