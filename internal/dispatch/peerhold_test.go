package dispatch_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
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
	key, err := yardkey.Load(y.d.PeerKey)
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

// until waits for ok, for a few seconds.
func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("%s: not within 5s", what)
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

// A question that asks to wait is held while nothing waits for its asker,
// and answered when something is queued for it: not when something is
// queued for another peer.
func TestPeerHoldAnsweredWhenQueued(t *testing.T) {
	clock := newClock()
	a, b, c := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock), newPeerYard(t, "c", clock)
	link(t, a, b, false, true)
	link(t, a, c, false, true)
	ctx := context.Background()

	ch := b.ask(t, ctx, "a", clock, 20*time.Second)
	until(t, "a holds b's question", func() bool { return a.rc.Held() == 1 })
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
	until(t, "a's hold is over", func() bool { return a.rc.Held() == 0 })
	// What waits already is handed over at once, wait or no wait.
	if got := answered(t, b.ask(t, ctx, "a", clock, 20*time.Second), time.Second); len(got.page.Messages) != 1 {
		t.Fatalf("a question with something waiting was answered with %+v", got.page)
	}
}

// A held question ends at its wait with an empty answer, signed as any.
func TestPeerHoldEndsAtTheWait(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	got := answered(t, b.ask(t, context.Background(), "a", clock, time.Second), 4*time.Second)
	if got.took < time.Second || len(got.page.Messages) != 0 || got.page.Push {
		t.Fatalf("answered after %s with %+v, want an empty page after the second asked for", got.took, got.page)
	}
	if a.rc.Held() != 0 {
		t.Fatalf("a still holds %d questions", a.rc.Held())
	}
	if rej := a.rc.Rejected(); len(rej) != 0 {
		t.Fatalf("a rejected %v", rej)
	}
}

// A peer holds one question: its next ends the one before with an empty
// answer. And a question whose asker goes away mid-hold is over on a's
// side too, with nothing left of it.
func TestPeerHoldOnePerPeer(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	first := b.ask(t, context.Background(), "a", clock, 20*time.Second)
	until(t, "a holds b's first question", func() bool { return a.rc.Held() == 1 })
	ctx, kill := context.WithCancel(context.Background())
	defer kill()
	second := b.ask(t, ctx, "a", clock, 20*time.Second)
	if got := answered(t, first, time.Second); len(got.page.Messages) != 0 {
		t.Fatalf("the first question ended with %+v, want an empty page", got.page)
	}
	if a.rc.Held() != 1 {
		t.Fatalf("a holds %d questions, want b's second", a.rc.Held())
	}
	kill()
	if got := <-second; got.err == nil {
		t.Fatalf("the question that was given up was answered: %+v", got.page)
	}
	until(t, "a's hold ends with the connection", func() bool { return a.rc.Held() == 0 })
}

// A question without wait is still that peer's next question and ends its
// previously held one with an empty answer.
func TestPeerPullWithoutWaitEndsPriorHold(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	first := b.ask(t, context.Background(), "a", clock, 20*time.Second)
	until(t, "a holds b's question", func() bool { return a.rc.Held() == 1 })
	second := b.ask(t, context.Background(), "a", clock, 0)
	if got := answered(t, first, time.Second); len(got.page.Messages) != 0 {
		t.Fatalf("the held question ended with %+v, want an empty page", got.page)
	}
	if got := answered(t, second, time.Second); got.err != nil {
		t.Fatalf("the question without wait: %v", got.err)
	}
	until(t, "a's hold is over", func() bool { return a.rc.Held() == 0 })
}

// A question without wait is answered at once, as one of a yardr from
// before questions were held; so is one past what a yard holds at once, and
// one of a peer the yard posts to, which would wait for nothing.
func TestPeerHoldNotAsked(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	ctx := context.Background()
	if got := answered(t, b.ask(t, ctx, "a", clock, 0), time.Second); len(got.page.Messages) != 0 || a.rc.Held() != 0 {
		t.Fatalf("a question without wait: %+v, %d held", got.page, a.rc.Held())
	}
	a.rc.SetMaxHeld(0)
	if got := answered(t, b.ask(t, ctx, "a", clock, 20*time.Second), time.Second); len(got.page.Messages) != 0 || a.rc.Held() != 0 {
		t.Fatalf("a question past what a holds: %+v, %d held", got.page, a.rc.Held())
	}
	a.rc.SetMaxHeld(1)
	_, err := a.api.SetPeerURL(ctx, "b", b.srv.URL, "jo")
	must(t, err)
	if got := answered(t, b.ask(t, ctx, "a", clock, 20*time.Second), time.Second); !got.page.Push || a.rc.Held() != 0 {
		t.Fatalf("a question of a peer a posts to: %+v, %d held", got.page, a.rc.Held())
	}
}

// run starts y's daemon, with a hold of a second and everything else it
// waits for short, and stops it with the test.
func (y *peerYard) run(t *testing.T) (stop func()) {
	t.Helper()
	y.d.PeerHold, y.d.PeerPoll, y.d.Backoff = time.Second, 100*time.Millisecond, 50*time.Millisecond
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
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	a.rc.SetMaxHeld(0) // answers at once, as a yardr that knows no wait
	b.run(t)

	until(t, "b polls a", func() bool { return a.asked.Load() >= 5 })
	from := a.asked.Load()
	// Held, it would be two or three questions in the time; asked at once
	// after each answer, hundreds.
	time.Sleep(2 * time.Second)
	if n := a.asked.Load() - from; n < 6 || n > 30 {
		t.Fatalf("against a yard that holds nothing b asked %d times in 2s, want about 20 (every 100ms)", n)
	}
	if a.rc.Held() != 0 {
		t.Fatal("a held a question")
	}

	a.rc.SetMaxHeld(8)
	until(t, "b's question is held", func() bool { return a.rc.Held() == 1 })
	from = a.asked.Load()
	time.Sleep(2500 * time.Millisecond)
	if n := a.asked.Load() - from; n < 1 || n > 4 {
		t.Fatalf("held for a second each, b asked %d times in 2.5s, want 2 or 3", n)
	}

	until(t, "b's question is held", func() bool { return a.rc.Held() == 1 })
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
}

// What is queued while the asker is away waits, and arrives at its next
// question: with its daemon not running, and with the yard it asks not
// answering, after which it asks again after a wait and is back to
// holding.
func TestPeersDaemonGetsWhatQueuedWhileAway(t *testing.T) {
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
	until(t, "b's question is held again", func() bool { return a.rc.Held() == 1 })
	if w := a.waiting(t, "b"); len(w) != 0 {
		t.Fatalf("still waiting at a for b: %+v", w)
	}
}
