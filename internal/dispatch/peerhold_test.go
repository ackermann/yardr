package dispatch_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/testload"
	"yardr.dev/yardr/internal/yardkey"
	"yardr.dev/yardr/internal/yards"
)

// asked is the answer to one question, and how long it took.
type asked struct {
	page store.PeerPage
	took time.Duration
	err  error
}

// ask has y ask its peer of that name for what waits there, held for up to
// wait, as y's daemon asks; the answer comes on the channel.
func (y *peerYard) ask(t *testing.T, ctx context.Context, peer string, clock *peerClock, wait time.Duration) <-chan asked {
	t.Helper()
	key, err := store.Load(y.d.PeerKey)
	must(t, err)
	p, err := y.api.GetPeer(ctx, peer)
	must(t, err)
	c := &yards.Caller{Key: key, Now: func() time.Time { return clock.now }}
	out := make(chan asked, 1)
	go func() {
		start := time.Now()
		batch, err := c.Pull(ctx, *p, p.Pulled, wait)
		a := asked{took: time.Since(start), err: err}
		if err == nil {
			a.err = json.Unmarshal(batch.Body, &a.page)
		}
		out <- a
	}()
	return out
}

// until waits for ok, as long as it takes: the deadline is a fuse against a
// hang (dispatch.WaitFuse), not a window the effect has to fit in.
func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	fuse := testload.Scale(dispatch.WaitFuse)
	for end := time.Now().Add(fuse); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%s: not within %s", what, fuse)
		}
	}
}

// answered takes the answer from ch, which has to come within within.
func answered(t *testing.T, ch <-chan asked, within time.Duration) asked {
	t.Helper()
	select {
	case a := <-ch:
		must(t, a.err)
		return a
	case <-time.After(within):
		t.Fatalf("no answer within %s", within)
		return asked{}
	}
}

// untilQuiet waits until y's listener goes quiet: no request for a
// stretch no polling peer keeps (three of the daemon's polls), which
// says the peer asking it is in a hold.
func untilQuiet(t *testing.T, y *peerYard) {
	t.Helper()
	fuse := testload.Scale(dispatch.WaitFuse)
	start := time.Now()
	last, since := y.asked.Load(), time.Now()
	for {
		time.Sleep(5 * time.Millisecond)
		if n := y.asked.Load(); n != last {
			last, since = n, time.Now()
		}
		if time.Since(since) >= 150*time.Millisecond {
			return
		}
		if time.Since(start) > fuse {
			t.Fatalf("no quiet stretch within %s", fuse)
		}
	}
}

// oldPeer serves questions as a yardr from before questions were held:
// every one answered at once with an empty page, signed with a's key
// so the asker takes it. It counts the questions it hears.
func oldPeer(t *testing.T, a *peerYard) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	key, err := store.Load(a.d.PeerKey)
	must(t, err)
	asked := &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		answer := []byte(`{"messages":[]}`)
		w.Header().Set(yardkey.HeaderSig, store.SignResponse(key, http.StatusOK, r.Header.Get(yardkey.HeaderSig), answer))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(answer)
	}))
	t.Cleanup(srv.Close)
	return srv, asked
}

// A question that asks to wait is held while nothing waits for its asker,
// and answered when something is queued for it: not when something is
// queued for another peer.
func TestPeerHoldAnsweredWhenQueued(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b, c := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock), newPeerYard(t, "c", clock)
	link(t, a, b, false, true)
	link(t, a, c, false, true)
	ctx := context.Background()

	askedAt := a.asked.Load()
	ch := b.ask(t, ctx, "a", clock, 20*time.Second)
	until(t, "a hears b's question", func() bool { return a.asked.Load() > askedAt })
	a.send(t, "c")
	select {
	case got := <-ch:
		t.Fatalf("a message for c ended b's question: %+v", got)
	case <-time.After(300 * time.Millisecond):
	}
	queued := time.Now()
	id := a.send(t, "b")
	got := answered(t, ch, time.Second)
	if len(got.page.Messages) != 1 || got.page.Messages[0].ID != id {
		t.Fatalf("the held question was answered with %+v, want the ping %s (%s after it was queued)", got.page, id, time.Since(queued))
	}
	// What waits already is handed over at once, wait or no wait.
	if got := answered(t, b.ask(t, ctx, "a", clock, 20*time.Second), time.Second); len(got.page.Messages) != 1 {
		t.Fatalf("a question with something waiting was answered with %+v", got.page)
	}
}

// A held question ends at its wait with an empty answer, signed as any.
func TestPeerHoldEndsAtTheWait(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	got := answered(t, b.ask(t, context.Background(), "a", clock, time.Second), 4*time.Second)
	if got.took < time.Second || len(got.page.Messages) != 0 || got.page.Push {
		t.Fatalf("answered after %s with %+v, want an empty page after the second asked for", got.took, got.page)
	}
	if evs := a.events(t, "peer_message_refused"); len(evs) != 0 {
		t.Fatalf("a refused %v", evs)
	}
}

// A peer holds one question: its next ends the one before with an empty
// answer. And a question whose asker goes away mid-hold is over on a's
// side too, with nothing left of it.
func TestPeerHoldOnePerPeer(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	askedAt := a.asked.Load()
	first := b.ask(t, context.Background(), "a", clock, 20*time.Second)
	until(t, "a hears b's first question", func() bool { return a.asked.Load() > askedAt })
	ctx, kill := context.WithCancel(context.Background())
	defer kill()
	second := b.ask(t, ctx, "a", clock, 20*time.Second)
	if got := answered(t, first, time.Second); len(got.page.Messages) != 0 {
		t.Fatalf("the first question ended with %+v, want an empty page", got.page)
	}
	kill()
	if got := <-second; got.err == nil {
		t.Fatalf("the question that was given up was answered: %+v", got.page)
	}
	// The ended hold leaves nothing behind: a's next question is held
	// fresh and answered when something is queued for it.
	askedAt = a.asked.Load()
	third := b.ask(t, context.Background(), "a", clock, 20*time.Second)
	until(t, "a hears b's next question", func() bool { return a.asked.Load() > askedAt })
	id := a.send(t, "b")
	if got := answered(t, third, time.Second); len(got.page.Messages) != 1 || got.page.Messages[0].ID != id {
		t.Fatalf("the next question was answered with %+v, want the ping %s", got.page, id)
	}
}

// A question without wait is still that peer's next question and ends its
// previously held one with an empty answer.
func TestPeerPullWithoutWaitEndsPriorHold(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	askedAt := a.asked.Load()
	first := b.ask(t, context.Background(), "a", clock, 20*time.Second)
	until(t, "a hears b's question", func() bool { return a.asked.Load() > askedAt })
	second := b.ask(t, context.Background(), "a", clock, 0)
	if got := answered(t, first, time.Second); len(got.page.Messages) != 0 {
		t.Fatalf("the held question ended with %+v, want an empty page", got.page)
	}
	if got := answered(t, second, time.Second); got.err != nil {
		t.Fatalf("the question without wait: %v", got.err)
	}
}

// A question without wait is answered at once, as one of a yardr from
// before questions were held; so is one of a peer the yard posts to,
// which would wait for nothing.
func TestPeerHoldNotAsked(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	ctx := context.Background()
	if got := answered(t, b.ask(t, ctx, "a", clock, 0), time.Second); len(got.page.Messages) != 0 {
		t.Fatalf("a question without wait: %+v", got.page)
	}
	_, err := a.api.SetPeerURL(ctx, "b", b.srv.URL, "jo")
	must(t, err)
	if got := answered(t, b.ask(t, ctx, "a", clock, 20*time.Second), time.Second); !got.page.Push {
		t.Fatalf("a question of a peer a posts to: %+v", got.page)
	}
}

// run starts y's daemon, with a hold of a second and a poll and backoff of
// 50ms, and stops it with the test.
func (y *peerYard) run(t *testing.T) (stop func()) {
	t.Helper()
	y.d.PeerHold, y.d.PeerPoll, y.d.Backoff = time.Second, 50*time.Millisecond, 50*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = y.d.Run(ctx) }()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the daemon did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

// The daemon asks a yard that holds no question at the rhythm of before
// (PeerPoll), and holds from the first question the yard does hold: then it
// asks once a hold, and what is queued for it arrives as it is queued, with
// the answer posted back.
func TestPeersDaemonHoldsOrPolls(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	ctx := context.Background()
	// Against a yardr that knows no wait, b polls at the rhythm of
	// before there were holds.
	old, oldAsked := oldPeer(t, a)
	if _, err := b.api.SetPeerURL(ctx, "a", old.URL, "jo"); err != nil {
		t.Fatal(err)
	}
	b.run(t)

	until(t, "b polls a", func() bool { return oldAsked.Load() >= 5 })
	from, start := oldAsked.Load(), time.Now()
	// Six more questions, in whatever time the machine takes over
	// them: a daemon that polls asks them, one pause apart, and one
	// that held would ask none. Counting them in a fixed window
	// flaked under race load, when fewer than three polls fit in
	// 500ms.
	until(t, "b polls on", func() bool { return oldAsked.Load() >= from+6 })
	// The six span five pauses of 50ms; asked at once after each
	// answer, they would all land in a moment. Load only stretches
	// the time, never shortens it.
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("against a yard that holds nothing b asked six times in %s, want about 250ms (every 50ms)", elapsed)
	}

	// Against a itself, b holds from the first question it asks of it.
	atFlip := a.asked.Load()
	if _, err := b.api.SetPeerURL(ctx, "a", a.srv.URL, "jo"); err != nil {
		t.Fatal(err)
	}
	until(t, "b asks a itself", func() bool { return a.asked.Load() > atFlip })
	from = a.asked.Load()
	// The second-long hold does not end in the time, so a daemon that holds
	// asks nothing more; one that polled would ask about ten times.
	time.Sleep(500 * time.Millisecond)
	if n := a.asked.Load() - from; n > 1 {
		t.Fatalf("held for a second each, b asked %d times in 500ms, want none", n)
	}

	untilQuiet(t, a)
	queued := time.Now()
	id := a.send(t, "b")
	for len(b.events(t, "peer_message_received")) == 0 {
		if time.Since(queued) > time.Second {
			t.Fatal("a ping queued while b's question was held did not arrive within a second")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The pass the puller woke posts the pong, and b's next question, asked
	// at once, says the ping arrived.
	until(t, "the pong", func() bool { return a.pong(t, id) != nil })
	until(t, "a logs the ping as sent", func() bool { return len(a.events(t, "peer_message_sent")) == 1 })
	// A pull the edge ends itself is no failure: the daemon asks again after
	// the poll, without a wait that doubles, and holds again once the yard
	// answers itself.
	from = a.asked.Load()
	a.capped.Store(true)
	// Ten questions quickly: asked again after the 50ms poll, not after a
	// wait that doubles into the minutes, which would take most of a minute.
	until(t, "b asks through the cap", func() bool { return a.asked.Load() >= from+10 })
	a.capped.Store(false)
	untilQuiet(t, a)
	id = a.send(t, "b")
	until(t, "the pong to the ping queued after the cap", func() bool { return a.pong(t, id) != nil })
	until(t, "nothing waits at a for b", func() bool { return len(a.waiting(t, "b")) == 0 })
}

// What is queued while the asker is away waits, and arrives at its next
// question: with its daemon not running, and with the yard it asks not
// answering, after which it asks again after a wait and is back to
// holding.
func TestPeersDaemonGetsWhatQueuedWhileAway(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	first := a.send(t, "b")
	stop := b.run(t)
	until(t, "the ping queued before b's daemon ran", func() bool { return a.pong(t, first) != nil })
	// The pong has arrived before b has written that it was posted. A
	// daemon stopped between the two posts it again, here to a yard that
	// is down, and waits Backoff by a clock that stands still in this
	// test: the second pong would wait behind it for good.
	until(t, "b has written that the pong was posted", func() bool { return len(b.waiting(t, "a")) == 0 })
	stop()

	second := a.send(t, "b")
	a.down.Store(true)
	b.run(t)
	until(t, "b asks a, which is down", func() bool { return a.asked.Load() > 0 && len(b.events(t, "peer_message_received")) == 1 })
	from := a.asked.Load()
	until(t, "b asks again after a wait", func() bool { return a.asked.Load() >= from+3 })
	a.down.Store(false)
	until(t, "the ping queued while a did not answer", func() bool { return a.pong(t, second) != nil })
	untilQuiet(t, a)
	// The hold registers before the handover write lands, so what waits is
	// read only once nothing does: under load the write lands after a poll
	// that already sees the hold.
	until(t, "nothing waits at a for b", func() bool { return len(a.waiting(t, "b")) == 0 })
}
