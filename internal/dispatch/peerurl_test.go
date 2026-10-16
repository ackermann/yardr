package dispatch_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"
)

// A peer added without a URL is given one: what waited for it to ask is
// posted to it in the next pass, and so is a ping sent after. With the URL
// taken away again the yard is not called, and its ping waits to be asked
// for.
func TestPeersURLSetAndTakenAway(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	waited := a.send(t, "b")
	a.pass(t)
	if b.asked.Load() != 0 || len(a.waiting(t, "b")) != 1 {
		t.Fatalf("a called a peer without a URL (%d requests), or its ping does not wait", b.asked.Load())
	}

	_, err := a.api.SetPeerURL(ctx, "b", b.srv.URL, "jo")
	must(t, err)
	id := a.send(t, "b")
	a.pass(t)
	got := b.events(t, "peer_message_received")
	if len(got) != 2 || got[0].Data["id"] != waited || got[0].Data["way"] != "push" || got[1].Data["id"] != id || got[1].Data["way"] != "push" {
		t.Fatalf("b received %+v, want the ping that waited and the one after, both posted", got)
	}
	if sent := a.events(t, "peer_message_sent"); len(sent) != 2 || sent[0].Data["way"] != "push" || sent[1].Data["id"] != id || sent[1].Data["way"] != "push" {
		t.Fatalf("a sent %+v", sent)
	}
	b.pass(t)
	if a.pong(t, waited) == nil || a.pong(t, id) == nil {
		t.Fatal("the pongs of the pings a posted did not come")
	}

	_, err = a.api.SetPeerURL(ctx, "b", "", "jo")
	must(t, err)
	asked := b.asked.Load()
	id = a.send(t, "b")
	a.pass(t)
	if b.asked.Load() != asked || len(a.waiting(t, "b")) != 1 {
		t.Fatalf("a called a peer whose URL was taken away (%d requests), or its ping does not wait", b.asked.Load()-asked)
	}
	b.pass(t)
	got = b.events(t, "peer_message_received")
	if len(got) != 3 || got[2].Data["id"] != id || got[2].Data["way"] != "pull" {
		t.Fatalf("b received %+v, want the last ping by asking", got)
	}
	b.pass(t)
	if sent := a.events(t, "peer_message_sent"); len(sent) != 3 || sent[2].Data["id"] != id || sent[2].Data["way"] != "pull" {
		t.Fatalf("a sent %+v", sent)
	}
	if b.asked.Load() != asked || a.pong(t, id) == nil {
		t.Fatalf("b was called %d times after its URL was taken away, or its pong did not come", b.asked.Load()-asked)
	}
	if len(a.waiting(t, "b"))+len(b.waiting(t, "a")) != 0 {
		t.Fatal("messages still wait")
	}
}

// b moved: its old address answers no more, and a message for it waits on
// a retry. Once a is told the new address the message goes there, at the
// try it was waiting for and not before, once, and the old address is not
// called again.
func TestPeersMessageFollowsTheURL(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	b.down.Store(true)
	id := a.send(t, "b")
	a.pass(t)
	w := a.waiting(t, "b")
	if len(w) != 1 || w[0].Attempts != 1 || !w[0].RetryAt.Equal(clock.now.Add(30*time.Second)) || b.asked.Load() != 1 {
		t.Fatalf("with b's address down: %+v, %d requests", w, b.asked.Load())
	}

	// The same yard, by its key, at another address.
	moved := httptest.NewServer(b.rc.Handler())
	t.Cleanup(moved.Close)
	_, err := a.api.SetPeerURL(ctx, "b", moved.URL, "jo")
	must(t, err)
	if now := a.waiting(t, "b"); len(now) != 1 || now[0].ID != id || now[0].Seq != w[0].Seq || now[0].Attempts != 1 || !now[0].RetryAt.Equal(*w[0].RetryAt) {
		t.Fatalf("what waited, after the URL changed: %+v; it was %+v", now, w)
	}
	a.pass(t)
	if got := b.events(t, "peer_message_received"); len(got) != 0 {
		t.Fatalf("the message was posted before its retry was due: %+v", got)
	}

	clock.now = clock.now.Add(30 * time.Second)
	a.pass(t)
	if got := b.events(t, "peer_message_received"); len(got) != 1 || got[0].Data["id"] != id || got[0].Data["way"] != "push" {
		t.Fatalf("b received %+v at its new address, want the ping that waited", got)
	}
	sent := a.events(t, "peer_message_sent")
	if len(sent) != 1 || sent[0].Data["id"] != id || sent[0].Data["way"] != "push" || sent[0].Data["attempts"] != float64(2) && sent[0].Data["attempts"] != 2 {
		t.Fatalf("a sent %+v", sent)
	}
	b.pass(t)
	a.pass(t)
	if a.pong(t, id) == nil || len(b.events(t, "peer_message_received")) != 1 || len(a.waiting(t, "b")) != 0 {
		t.Fatal("the pong did not come, or the ping arrived twice or still waits")
	}
	if b.asked.Load() != 1 {
		t.Fatalf("the old address was called %d times, want the one try before the URL changed", b.asked.Load())
	}
}

// b has asked for a ping and not yet said that it has it when a is given
// b's URL: the ping is posted too, as all that waits is, and b takes it
// once.
func TestPeersURLSetBetweenTwoQuestions(t *testing.T) {
	t.Parallel()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, false, true)
	id := a.send(t, "b")
	b.pass(t)
	if got := b.events(t, "peer_message_received"); len(got) != 1 || got[0].Data["way"] != "pull" || len(a.waiting(t, "b")) != 1 {
		t.Fatalf("b pulled %+v, and %d wait at a", got, len(a.waiting(t, "b")))
	}
	_, err := a.api.SetPeerURL(context.Background(), "b", b.srv.URL, "jo")
	must(t, err)
	b.pass(t)
	a.pass(t)
	b.pass(t)
	if got := b.events(t, "peer_message_received"); len(got) != 1 || got[0].Data["id"] != id {
		t.Fatalf("b received %+v, want the ping once", got)
	}
	if sent := a.events(t, "peer_message_sent"); len(sent) != 1 || sent[0].Data["id"] != id || len(a.waiting(t, "b")) != 0 {
		t.Fatalf("a sent %+v, and %d still wait", sent, len(a.waiting(t, "b")))
	}
	if len(a.events(t, "peer_message_received")) != 1 || a.pong(t, id) == nil {
		t.Fatal("the pong did not come once")
	}
}
