package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"yardr.dev/yardr/internal/pairing"
	"yardr.dev/yardr/internal/yardkey"
)

// pairHost is a yard with a pairing to open, at a time the test sets.
type pairHost struct {
	*Store
	key string
	at  time.Time
}

// newPairHost sets the package's fake clock and shortens pairing.Stretch,
// so its callers stay sequential.
func newPairHost(t *testing.T) *pairHost {
	t.Helper()
	h := &pairHost{at: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	clock = func() time.Time { return h.at }
	stretch := pairing.Stretch
	pairing.Stretch = 16
	t.Cleanup(func() { clock, pairing.Stretch = time.Now, stretch })
	var err error
	if h.Store, err = Open(filepath.Join(t.TempDir(), "y.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(context.Background()) })
	h.key = newPublicKey(t)
	return h
}

func newPublicKey(t *testing.T) string {
	t.Helper()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "peer.key"))
	if err != nil {
		t.Fatal(err)
	}
	return yardkey.Public(k)
}

func (h *pairHost) open(t *testing.T) (*PairingCode, string) {
	t.Helper()
	p, err := h.PairOpen(context.Background(), PairOffer{Name: "north", Key: h.key, URL: "http://10.0.0.5:8790"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	code, _ := pairing.CleanCode(p.Code)
	return p, code
}

// call is the calling yard's two messages, as its side makes them.
func (h *pairHost) call(code, name, key string) (pairing.Transcript, error) {
	ctx := context.Background()
	me := pairing.Party{Nonce: pairing.NewNonce(), Name: name, Key: key}
	t := pairing.Transcript{Commit: pairing.Commit(me), Joiner: me}
	k := pairing.Key(code, t.Commit)
	offer, err := h.PairHello(ctx, pairing.NewHello(k, me))
	if err != nil {
		return t, err
	}
	t.Host = offer.Party
	_, err = h.PairReveal(ctx, pairing.NewReveal(k, t))
	return t, err
}

func reason(err error) string {
	var no *pairing.Refused
	if errors.As(err, &no) {
		return no.Reason
	}
	return ""
}

func (h *pairHost) refusals(t *testing.T) []Event {
	t.Helper()
	evs, err := h.Events(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range evs {
		if e.Kind == "peer_pair_refused" {
			out = append(out, e)
		}
	}
	return out
}

// A code is good for ten minutes: a call that comes after is told the code
// has run out, the pairing has ended as timed out, and the yard opens
// another.
func TestPairingCodeExpires(t *testing.T) {
	// Sequential: the package's fake clock and pairing.Stretch (via newPairHost).
	h := newPairHost(t)
	ctx := context.Background()
	opened, code := h.open(t)
	if got := opened.Expires.Sub(h.at); got != pairing.TTL || pairing.TTL != 10*time.Minute {
		t.Fatalf("the code is good for %s", got)
	}
	laptop := newPublicKey(t)

	h.at = h.at.Add(pairing.TTL - time.Second)
	if p, err := h.GetPairing(ctx, opened.ID); err != nil || p.State != PairOpened {
		t.Fatalf("a second before it runs out: %+v, %v", p, err)
	}
	h.at = h.at.Add(time.Second)
	if _, err := h.call(code, "laptop", laptop); reason(err) != pairing.TimedOut {
		t.Fatalf("a call with a code that ran out = %v", err)
	}
	p, err := h.GetPairing(ctx, opened.ID)
	if err != nil || p.State != PairEnded || p.Reason != pairing.TimedOut {
		t.Fatalf("the pairing = %+v, %v", p, err)
	}
	if evs := h.refusals(t); len(evs) != 1 || evs[0].Data["reason"] != pairing.TimedOut || evs[0].Data["pairing"] != opened.ID {
		t.Fatalf("refusals = %+v", evs)
	}
	if ps, _ := h.Peers(ctx); len(ps) != 0 {
		t.Fatalf("peers = %+v", ps)
	}
	// The yard is free for another, whose code is another.
	again, code2 := h.open(t)
	if again.ID == opened.ID || code2 == code {
		t.Fatalf("the second pairing = %+v", again)
	}
	if _, err := h.call(code, "laptop", laptop); reason(err) != pairing.WrongCode {
		t.Fatalf("the first pairing's code at the second = %v", err)
	}
}

// Once a yard has called, the two operators have five minutes from the
// call, whatever was left of the code's ten. When they run out with one
// yes, or two, missing, nothing is saved.
func TestPairingConfirmTimesOut(t *testing.T) {
	// Sequential: the package's fake clock and pairing.Stretch (via newPairHost).
	h := newPairHost(t)
	ctx := context.Background()
	opened, code := h.open(t)
	laptop := newPublicKey(t)

	h.at = h.at.Add(9 * time.Minute)
	tr, err := h.call(code, "laptop", laptop)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := h.GetPairing(ctx, opened.ID)
	if p.State != PairCalled || p.Expires.Sub(h.at) != pairing.ConfirmTTL || p.Check != tr.Check() {
		t.Fatalf("after the call: %+v", p)
	}
	// Past the code's ten minutes, within the five to confirm.
	h.at = h.at.Add(4 * time.Minute)
	if out, err := h.PairConfirmed(ctx, tr.Session(), true); err != nil || out.State != pairing.Waiting {
		t.Fatalf("the caller's yes = %+v, %v", out, err)
	}
	h.at = h.at.Add(time.Minute)
	if p, err := h.PairAnswer(ctx, opened.ID, true, "jo"); err != nil || p.State != PairEnded || p.Reason != pairing.TimedOut {
		t.Fatalf("the host's yes after the time ran out = %+v, %v", p, err)
	}
	if out, err := h.PairConfirmed(ctx, tr.Session(), true); err != nil || out.State != pairing.Ended || out.Reason != pairing.TimedOut {
		t.Fatalf("the caller is told %+v, %v", out, err)
	}
	if ps, _ := h.Peers(ctx); len(ps) != 0 {
		t.Fatalf("peers = %+v", ps)
	}
	if evs := h.refusals(t); len(evs) != 1 || evs[0].Data["reason"] != pairing.TimedOut || evs[0].Data["calls"] != "laptop" {
		t.Fatalf("refusals = %+v", evs)
	}
}

// The code is in the server's memory until a yard that knows it has
// revealed itself, or the pairing ends, and is forgotten then: the
// confirming is done without it. A later call with the code takes an
// earlier one's place until one has revealed itself.
func TestPairingForgetsTheCode(t *testing.T) {
	// Sequential: the package's fake clock and pairing.Stretch (via newPairHost).
	h := newPairHost(t)
	ctx := context.Background()
	_, code := h.open(t)
	held := func() bool {
		h.pair.mu.Lock()
		defer h.pair.mu.Unlock()
		p := h.pair.cur
		return p.code != "" || p.key != nil || p.commit != "" || p.nonce != ""
	}
	if !held() {
		t.Fatal("an open pairing has no code")
	}

	// Two first messages: the second takes the first's place.
	first := pairing.Party{Nonce: pairing.NewNonce(), Name: "laptop", Key: newPublicKey(t)}
	t1 := pairing.Transcript{Commit: pairing.Commit(first), Joiner: first}
	k1 := pairing.Key(code, t1.Commit)
	offer, err := h.PairHello(ctx, pairing.NewHello(k1, first))
	if err != nil {
		t.Fatal(err)
	}
	t1.Host = offer.Party
	second := newPublicKey(t)
	t2, err := h.call(code, "desktop", second)
	if err != nil {
		t.Fatal(err)
	}
	if held() {
		t.Fatal("the code is held after a yard has revealed itself")
	}
	if _, err := h.PairReveal(ctx, pairing.NewReveal(k1, t1)); reason(err) != pairing.Used {
		t.Fatalf("the first call's second message = %v", err)
	}
	if p, _ := h.GetPairing(ctx, ""); p.Key != second || p.Check != t2.Check() || p.Tries != 0 {
		t.Fatalf("the pairing = %+v", p)
	}
	if err := h.PairClose(ctx, "", "jo"); err != nil {
		t.Fatal(err)
	}

	// Ended before any call, by the operator or by wrong codes.
	_, _ = h.open(t)
	if err := h.PairClose(ctx, "", "jo"); err != nil || held() {
		t.Fatalf("PairClose = %v; the code is held: %v", err, held())
	}
	h.open(t)
	for range pairing.MaxTries {
		if _, err := h.call("ZZZZZZZZ", "laptop", second); reason(err) != pairing.WrongCode {
			t.Fatal(err)
		}
	}
	if held() {
		t.Fatal("the code is held after the wrong codes ended the pairing")
	}
}

// A second message that opens another commitment than the one it was sent
// for is no message, from one who knows the code or not; a name that is no
// peer's name ends the pairing with the way out.
func TestPairingReveal(t *testing.T) {
	// Sequential: the package's fake clock and pairing.Stretch (via newPairHost).
	h := newPairHost(t)
	ctx := context.Background()
	_, code := h.open(t)
	me := pairing.Party{Nonce: pairing.NewNonce(), Name: "laptop", Key: newPublicKey(t)}
	tr := pairing.Transcript{Commit: pairing.Commit(me), Joiner: me}
	k := pairing.Key(code, tr.Commit)
	offer, err := h.PairHello(ctx, pairing.NewHello(k, me))
	if err != nil {
		t.Fatal(err)
	}
	tr.Host = offer.Party
	// Another key than was committed to, under the same commitment.
	swapped := tr
	swapped.Joiner.Key = newPublicKey(t)
	if _, err := h.PairReveal(ctx, pairing.NewReveal(k, swapped)); err == nil || reason(err) != "" {
		t.Fatalf("a reveal of another key = %v", err)
	}
	if p, _ := h.GetPairing(ctx, ""); p.State != PairOpened || p.Tries != 1 {
		t.Fatalf("the pairing = %+v", p)
	}
	// Made without the code.
	if _, err := h.PairReveal(ctx, pairing.NewReveal(pairing.Key("ZZZZZZZZ", tr.Commit), tr)); reason(err) != pairing.WrongCode {
		t.Fatalf("a reveal without the code = %v", err)
	}
	// The honest one is still taken.
	if _, err := h.PairReveal(ctx, pairing.NewReveal(k, tr)); err != nil {
		t.Fatal(err)
	}
	if err := h.PairClose(ctx, "", "jo"); err != nil {
		t.Fatal(err)
	}

	_, code = h.open(t)
	if _, err := h.call(code, "Jo/Laptop", newPublicKey(t)); reason(err) != pairing.BadName {
		t.Fatalf("a call by a yard named Jo/Laptop = %v", err)
	}
	if p, _ := h.GetPairing(ctx, ""); p.State != PairEnded || p.Reason != pairing.BadName || p.Detail == "" {
		t.Fatalf("the pairing = %+v", p)
	}
	// The calling side logs only reasons a pairing ends for.
	if err := h.PairRefused(ctx, pairing.Declined, "north", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := h.PairRefused(ctx, "K7QM-2XPD", "north", "jo"); err == nil {
		t.Fatal("PairRefused took a reason that is none")
	}
	if err := h.PairRefused(ctx, pairing.Declined, "a b\n", "jo"); err == nil {
		t.Fatal("PairRefused took a name that is none")
	}
}
