package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// threads lets the link with north carry thread both ways, beside what it
// carried.
func (y *homeYard) threads(t *testing.T, more ...string) {
	t.Helper()
	if _, err := y.AllowPeer(y.ctx, "north", PeerAllow{Send: append([]string{PeerThread}, more...), Receive: []string{PeerThread}}, "jo"); err != nil {
		t.Fatal(err)
	}
}

// kept counts what this yard keeps of reading beads at other yards.
func (y *homeYard) kept(t *testing.T) (n int) {
	t.Helper()
	if err := y.read(func(tx *bolt.Tx) error {
		n = tx.Bucket(bPeerReads).Stats().KeyN
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// Reads count in the peer's hour beside its mail and asks: the working yard
// answers thirty questions, refuses the next as rate_limited and logs that
// once, in the count its mail has, and answers again when the hour has
// passed.
func TestPeerReadLimit(t *testing.T) {
	y := newHomeYard(t)
	y.threads(t)
	b, err := y.CreateBead(y.ctx, NewBead{Title: "From north"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.write(func(w *wtx) error {
		return put(w.Tx, bPeerBeads, b.UUID, peerBead{Bead: b.UUID, Key: y.key, Direction: "in", Theirs: "n-1", Stage: "build", Message: "stay-1", At: now()})
	}); err != nil {
		t.Fatal(err)
	}
	n := 0
	ask := func() string {
		t.Helper()
		n++
		y.now = y.now.Add(time.Second)
		return y.take(t, PeerMessage{ID: fmt.Sprintf("q%d", n), Kind: PeerBeadRead, Re: "stay-1", Body: json.RawMessage(`{"v":1}`)}).Refused
	}
	if PeerMailLimit != 30 {
		t.Fatalf("a yard takes %d mails, asks and reads an hour, want 30", PeerMailLimit)
	}
	for i := 0; i < PeerMailLimit; i++ {
		if got := ask(); got != "" {
			t.Fatalf("read %d of the hour = %q", i+1, got)
		}
	}
	for i := 0; i < 3; i++ {
		if got := ask(); got != RefusedRate {
			t.Fatalf("read %d over the limit = %q", i+1, got)
		}
	}
	if got := y.refusals(t, RefusedRate); got != 1 {
		t.Fatalf("%d events for the reads over the limit, want 1", got)
	}
	out, _ := y.outbox(t)
	if len(out) != PeerMailLimit || out[0].Kind != PeerBeadRead || out[0].Re != "q1" {
		t.Fatalf("north is answered %d times, want %d: %+v", len(out), PeerMailLimit, out)
	}
	// The hour is the one its mail is counted in.
	if p, err := y.GetPeer(y.ctx, "north"); err != nil || p.Mails != PeerMailLimit+3 || !p.MailLimited || p.Events != 0 {
		t.Fatalf("the peer's counts = %+v, %v", p, err)
	}
	y.now = y.now.Add(PeerMailWindow)
	if got := ask(); got != "" {
		t.Fatalf("a read once the hour has passed = %q", got)
	}
}

// What this yard keeps of reading a bead at another yard is one record
// beside the record of the stay, and it is removed in the write that ends
// the stay: the return, here.
func TestPeerReadKeptUntilTheStayEnds(t *testing.T) {
	y := newHomeYard(t)
	y.threads(t, PeerBead)
	b, err := y.CreateBead(y.ctx, NewBead{Title: "Add the export"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	stay := sentID(b.UUID, 1)
	if err := y.write(func(w *wtx) error {
		entered, _, err := stayOf(w.Tx, b.UUID)
		if err != nil {
			return err
		}
		return put(w.Tx, bPeerSent, b.UUID, peerSent{Bead: b.UUID, Key: y.key, Stage: b.Stage, Entered: entered, Message: stay, Seq: 1, At: now()})
	}); err != nil {
		t.Fatal(err)
	}
	if y.kept(t) != 0 {
		t.Fatal("something is kept before anyone read")
	}
	asked, err := y.PeerRead(y.ctx, b.ID, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	held := true
	body, _ := json.Marshal(PeerReadMessage{V: 1, Bead: "n-1", Stage: "build", Held: &held, Notes: []PeerNote{{Author: "builder", At: y.now, Text: "CSV or XLSX?"}}})
	if got := y.take(t, PeerMessage{ID: "a1", Kind: PeerBeadRead, Re: asked.ID, Body: body}); got.Refused != "" {
		t.Fatalf("north's answer = %+v", got)
	}
	got, err := y.PeerReadKept(y.ctx, b.ID, "mckean")
	if err != nil || got.Answer == nil || got.Answer.Bead != "n-1" || !got.Answer.Held || !got.Answer.At.Equal(y.now) || y.kept(t) != 1 {
		t.Fatalf("what is kept of the answer = %+v, %v (%d records)", got, err, y.kept(t))
	}
	// The answer is not the peer's mail.
	if p, err := y.GetPeer(y.ctx, "north"); err != nil || p.Mails != 0 {
		t.Fatalf("the peer's counts after its answer = %+v, %v", p, err)
	}
	ret, _ := json.Marshal(PeerReturnMessage{V: 1, Bead: "n-1", Outcome: "done"})
	if got := y.take(t, PeerMessage{ID: "r1", Kind: PeerBeadReturn, Re: stay, Body: ret}); got.Refused != "" {
		t.Fatalf("north's return = %+v", got)
	}
	if n := y.kept(t); n != 0 {
		t.Fatalf("%d records kept after the return", n)
	}
	if _, err := y.PeerReadKept(y.ctx, b.ID, "mckean"); err == nil || !strings.Contains(err.Error(), "its stay at north ended") {
		t.Fatalf("reading what is kept after the return = %v", err)
	}
}

// A foreign ask whose bead is the one the other yard works for a bead of
// this yard says so: the body has a line naming this yard's bead, and that
// bead is the first of the ask's about-beads.
func TestBeadForeignIsACounterpart(t *testing.T) {
	y := newHomeYard(t)
	ask := northPeerAsk()
	ask.Since = y.now.Add(-time.Hour)
	y.takeAsk(t, "f1", ask)
	mine, err := y.CreateBead(y.ctx, NewBead{Title: "Move the links, here"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	other, err := y.CreateBead(y.ctx, NewBead{Title: "Another yard's"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	sent := func(rec peerSent) {
		t.Helper()
		if err := y.write(func(w *wtx) error { return put(w.Tx, bPeerSent, rec.Bead, rec) }); err != nil {
			t.Fatal(err)
		}
	}
	// The same handle at another yard is not north's bead.
	sent(peerSent{Bead: other.UUID, Key: "another-yards-key", Theirs: "yd-links", Stage: "new", Message: "m0", At: y.now})
	v, err := y.Bead(y.ctx, "yd-links@north")
	if err != nil || strings.Contains(v.Body, "this yard's") || len(v.Asks[0].About) != 1 {
		t.Fatalf("a foreign ask on a bead that is nobody's counterpart = %+v, %v", v, err)
	}
	sent(peerSent{Bead: mine.UUID, Key: y.key, Theirs: "yd-links", Stage: "new", Message: "m1", At: y.now})
	v, err = y.Bead(y.ctx, "yd-links@north")
	if err != nil {
		t.Fatal(err)
	}
	want := "This bead lives in north's yard, at stage build.\nThis is the bead north works for this yard's " + mine.ID + ".\nThe links file declares the new source."
	if v.Body != want {
		t.Fatalf("body = %q, want %q", v.Body, want)
	}
	about := v.Asks[0].About
	if len(about) != 2 || about[0] != (AboutBead{ID: mine.ID, Title: "Move the links, here", Stage: mine.Stage}) || about[1].ID != "yd-train" {
		t.Fatalf("about = %+v", about)
	}
	// Once the stay has ended it worked it.
	at := y.now
	sent(peerSent{Bead: mine.UUID, Key: y.key, Theirs: "yd-links", Stage: "new", Message: "m1", At: y.now, Outcome: "done", EndedAt: &at})
	if v, err := y.Bead(y.ctx, "yd-links@north"); err != nil || !strings.Contains(v.Body, "\nThis is the bead north worked for this yard's "+mine.ID+".\n") {
		t.Fatalf("after the stay: %+v, %v", v, err)
	}
}
