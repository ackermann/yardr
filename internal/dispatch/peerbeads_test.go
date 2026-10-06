package dispatch_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// sending is the flow of a yard that has its building done by another: the
// stage build is worked by a linked yard, and the two outcomes that are
// the sender's own go to a person, as a question and a failure do.
const sending = "new -> build\nbuild -> review on done | decide on question | decide on failed | decide on refused | decide on undelivered\nreview -> merged\ndecide -> build\nmerged terminal\n"

// taking is a flow for beads from another yard: one stage, and back.
const taking = "work -> returned on done | returned on failed | returned on odd\nreturned terminal\n"

func putFlow(t *testing.T, y *peerYard, name, text string) {
	t.Helper()
	f, err := store.ParseFlow(text)
	must(t, err)
	f.Name = name
	_, err = y.api.PutFlow(context.Background(), *f, "jo")
	must(t, err)
}

// sends makes y a yard whose stage build, in its rig web, is worked by the
// peer to: the link carries bead out, a peer group names the yard and a
// route sends the stage to it.
func (y *peerYard) sends(t *testing.T, to string, set map[string]any) {
	t.Helper()
	ctx := context.Background()
	if _, err := y.api.GetRig(ctx, "web"); err != nil {
		must(t, y.api.PutRig(ctx, store.Rig{Name: "web", Base: "origin/main"}, "jo"))
		putFlow(t, y, "sending", sending)
		must(t, y.api.SetFlowPointer(ctx, "web", "", "sending", "jo"))
	}
	_, err := y.api.AllowPeer(ctx, to, store.PeerAllow{Send: []string{"bead"}}, "jo")
	must(t, err)
	cfg := map[string]any{store.GroupPeer: to}
	for k, v := range set {
		cfg[k] = v
	}
	must(t, y.api.PutGroup(ctx, store.Group{Name: "at-" + to, Runner: store.PeerRunner, Limit: 3, Config: cfg}))
	_, err = y.api.AddRoute(ctx, store.Route{Stage: "build", Group: "at-" + to})
	must(t, err)
}

// takes makes y a yard that takes the beads of the peer from into its rig
// in, pinned to the flow taking, held unless auto; jo is a user of it.
func (y *peerYard) takes(t *testing.T, from, in string, auto bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := y.api.GetRig(ctx, in); err != nil {
		must(t, y.api.PutRig(ctx, store.Rig{Name: in, Base: "origin/main"}, "jo"))
	}
	if _, err := y.api.GetFlow(ctx, "taking"); err != nil {
		putFlow(t, y, "taking", taking)
		_, err := y.api.AddUser(ctx, "jo", "jo")
		must(t, err)
	}
	_, err := y.api.AllowPeer(ctx, from, store.PeerAllow{Receive: []string{"bead"}}, "jo")
	must(t, err)
	_, err = y.api.SetPeerIntake(ctx, from, &store.PeerIntake{Rig: in, Flow: "taking", Auto: auto}, "jo")
	must(t, err)
}

// bead makes a bead of y in the stage its peer works.
func (y *peerYard) bead(t *testing.T, title string) *store.Bead {
	t.Helper()
	b, err := y.api.CreateBead(context.Background(), store.NewBead{Title: title, Body: "As a CSV.", Type: "feature", Rig: "web", Stage: "build", Priority: 1}, "jo")
	must(t, err)
	return b
}

func (y *peerYard) get(t *testing.T, id string) *store.Bead {
	t.Helper()
	b, err := y.api.GetBead(context.Background(), id)
	must(t, err)
	return b
}

// from is the one open bead y has that came from the peer name.
func (y *peerYard) from(t *testing.T, name string) *store.Bead {
	t.Helper()
	bs, err := y.api.ListBeads(context.Background(), store.BeadFilter{Label: "from-" + name})
	must(t, err)
	if len(bs) != 1 {
		t.Fatalf("%s has %d open beads from %s, want one", y.name, len(bs), name)
	}
	return y.get(t, bs[0].ID)
}

func (y *peerYard) beadEvents(t *testing.T, id, kind string) []store.Event {
	t.Helper()
	evs, err := y.api.Events(context.Background(), id, 500)
	must(t, err)
	return slices.DeleteFunc(evs, func(e store.Event) bool { return e.Kind != kind })
}

func (y *peerYard) notes(t *testing.T, id string) []store.Note {
	t.Helper()
	ns, err := y.api.Notes(context.Background(), id)
	must(t, err)
	return ns
}

func (y *peerYard) live(t *testing.T) []store.Session {
	t.Helper()
	ss, err := y.api.Sessions(context.Background(), false)
	must(t, err)
	return ss
}

// A bead that enters a stage routed to a peer group is sent to that yard,
// arrives there held, is released by a user and worked through that yard's
// flow, and the outcome of that flow moves this yard's bead on, with the
// notes written there. With the other yard reachable, and with one that
// has to be asked.
func TestPeerBeadRoundTrip(t *testing.T) {
	for name, aCallsB := range map[string]bool{"push": true, "b asks": false} {
		t.Run(name, func(t *testing.T) {
			clock := newClock()
			a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
			link(t, a, b, aCallsB, true)
			a.sends(t, "b", nil)
			b.takes(t, "a", "web", false)
			ctx := context.Background()
			mine := a.bead(t, "Add the export")
			must(t, a.api.AddNote(ctx, mine.ID, "the columns are in the issue", "mayor"))

			a.pass(t)
			sent := a.beadEvents(t, mine.ID, "peer_bead_sent")
			if len(sent) != 1 || sent[0].Data["peer"] != "b" || sent[0].Data["stage"] != "build" || sent[0].Data["group"] != "at-b" {
				t.Fatalf("a's peer_bead_sent = %+v", sent)
			}
			if ss := a.live(t); len(ss) != 1 || ss[0].Runner != store.PeerRunner || ss[0].State != "running" || ss[0].Bead != mine.ID {
				t.Fatalf("a's sessions with the bead away = %+v", ss)
			}
			// Where a does not call, b asks, and its next question says that
			// it has the bead.
			b.pass(t)
			b.pass(t)
			a.pass(t)
			theirs := b.from(t, "a")
			if !theirs.Hold || theirs.Stage != "work" || theirs.Title != "Add the export" || theirs.Body != "As a CSV." || theirs.Type != "feature" ||
				theirs.Peer.Bead != mine.ID || theirs.Peer.Stage != "build" ||
				!slices.Equal(theirs.Peer.Outcomes, []string{"done", "question", "failed", "refused", "undelivered"}) {
				t.Fatalf("the bead as it arrived at b = %+v, peer %+v", theirs, theirs.Peer)
			}
			if ns := b.notes(t, theirs.ID); len(ns) != 1 || ns[0].Author != "mayor@a" || ns[0].Text != "the columns are in the issue" {
				t.Fatalf("the notes that came with it = %+v", ns)
			}
			away := a.get(t, mine.ID).Away()
			if away == nil || away.Peer != "b" || away.Direction != "out" || away.Stage != "build" || away.DeliveredAt == nil || away.ReturnedAt != nil ||
				away.HeardAt == nil || !strings.HasPrefix(away.Line(), "at b: sent ") || !strings.Contains(away.Line(), ", b last heard ") ||
				strings.Contains(away.Line(), "not delivered") {
				t.Fatalf("what a's bead says while it is away = %+v, %q", away, away.Line())
			}
			raw, _ := json.Marshal(a.get(t, mine.ID))
			var asJSON struct {
				Peer struct{ Peer, Direction, Stage string } `json:"peer"`
			}
			if json.Unmarshal(raw, &asJSON) != nil || asJSON.Peer.Peer != "b" || asJSON.Peer.Direction != "out" || asJSON.Peer.Stage != "build" {
				t.Fatalf("a's bead as JSON = %s", raw)
			}
			if p, _ := a.api.GetPeer(ctx, "b"); p.Away != 1 || p.HeardAt == nil {
				t.Fatalf("a's peer b with a bead there = %+v", p)
			}
			// Nothing moves while b has not answered, however often a looks.
			for i := 0; i < 3; i++ {
				a.pass(t)
				b.pass(t)
			}
			if got := a.get(t, mine.ID); got.Stage != "build" || got.Group == nil || len(a.beadEvents(t, mine.ID, "peer_bead_sent")) != 1 {
				t.Fatalf("a's bead while b works = %+v", got)
			}

			_, err := b.api.SetHold(ctx, theirs.ID, false, store.Expect{}, "jo")
			must(t, err)
			must(t, b.api.AddNote(ctx, theirs.ID, "exported as RFC 4180", "builder"))
			_, err = b.api.Done(ctx, theirs.ID, "done", "", store.Expect{}, "builder")
			must(t, err)
			b.pass(t)
			a.pass(t)
			b.pass(t)

			got := a.get(t, mine.ID)
			if got.Stage != "review" || got.Group != nil || got.Hold || len(a.live(t)) != 0 {
				t.Fatalf("a's bead after b's done = %+v", got)
			}
			adv := a.beadEvents(t, mine.ID, "advanced")
			if len(adv) != 1 || adv[0].Actor != "peer:b" || adv[0].Data["outcome"] != "done" || adv[0].Data["from"] != "build" || adv[0].Data["to"] != "review" {
				t.Fatalf("a's advanced = %+v", adv)
			}
			ns := a.notes(t, mine.ID)
			if len(ns) != 2 || ns[1].Author != "builder@b" || ns[1].Text != "exported as RFC 4180" {
				t.Fatalf("a's notes after the return = %+v", ns)
			}
			ret := a.beadEvents(t, mine.ID, "peer_bead_returned")
			if len(ret) != 1 || ret[0].Data["outcome"] != "done" || ret[0].Data["from"] != theirs.ID || ret[0].Data["stale"] != nil {
				t.Fatalf("a's peer_bead_returned = %+v", ret)
			}
			away = a.get(t, mine.ID).Away()
			if away.Outcome != "done" || away.Bead != theirs.ID || !strings.HasPrefix(away.Line(), "was at b: sent ") ||
				!strings.Contains(away.Line(), "returned done") || !strings.Contains(away.Line(), "(its bead "+theirs.ID+")") {
				t.Fatalf("what a's bead says after the return = %+v, %q", away, away.Line())
			}
			if p, _ := a.api.GetPeer(ctx, "b"); p.Away != 0 {
				t.Fatalf("a still counts %d beads at b", p.Away)
			}
		})
	}
}

// The bead message delivered twice makes one bead at b, and b's return
// delivered twice moves a's bead once.
func TestPeerBeadDeliveredTwice(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	a.sends(t, "b", nil)
	b.takes(t, "a", "web", true)
	ctx := context.Background()
	mine := a.bead(t, "Add the export")
	// Kept from a pass in which b did not answer, to post a second time.
	b.down.Store(true)
	a.pass(t)
	w := a.waiting(t, "b")
	if len(w) != 1 || w[0].Kind != store.PeerBead || w[0].Attempts != 1 || w[0].Tries != store.PeerBeadTries {
		t.Fatalf("a's outbox with b down = %+v", w)
	}
	if line := a.get(t, mine.ID).Away().Line(); !strings.Contains(line, "not delivered yet") || !strings.Contains(line, "b not heard from yet") {
		t.Fatalf("a's bead while b is down says %q", line)
	}
	b.down.Store(false)
	clock.now = clock.now.Add(time.Hour)
	a.pass(t)
	again, err := b.own.PeerReceive(ctx, "a", a.pub, []store.PeerMessage{w[0].PeerMessage})
	must(t, err)
	if !again[0].Duplicate || again[0].Refused != "" {
		t.Fatalf("the bead message a second time = %+v", again)
	}
	theirs := b.from(t, "a")
	if arrived := b.events(t, "peer_bead_arrived"); len(arrived) != 1 {
		t.Fatalf("b's peer_bead_arrived = %+v", arrived)
	}

	_, err = b.api.Done(ctx, theirs.ID, "done", "built", store.Expect{}, "builder")
	must(t, err)
	back, err := b.api.PeerOutbox(ctx, "a", 0)
	must(t, err)
	if len(back) != 1 || back[0].Kind != store.PeerBeadReturn || back[0].Re != w[0].ID {
		t.Fatalf("b's outbox after its flow ended = %+v", back)
	}
	b.pass(t)
	if got := a.get(t, mine.ID); got.Stage != "review" {
		t.Fatalf("a's bead after the return = %+v", got)
	}
	taken, err := a.own.PeerReceive(ctx, "b", b.pub, []store.PeerMessage{back[0].PeerMessage})
	must(t, err)
	if !taken[0].Duplicate {
		t.Fatalf("the return a second time = %+v", taken)
	}
	if adv := a.beadEvents(t, mine.ID, "advanced"); len(adv) != 1 {
		t.Fatalf("a's bead moved %d times for one return", len(adv))
	}
	if ns := a.notes(t, mine.ID); len(ns) != 1 || ns[0].Author != "builder@b" {
		t.Fatalf("a's notes = %+v", ns)
	}
}

// b takes no beads of a (no intake): its refusal ends a's stage as
// refused, which a's flow sends to a person, with a note that says why in
// a's words.
func TestPeerBeadRefused(t *testing.T) {
	for name, aCallsB := range map[string]bool{"push": true, "b asks": false} {
		t.Run(name, func(t *testing.T) {
			clock := newClock()
			a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
			link(t, a, b, aCallsB, true)
			a.sends(t, "b", nil)
			_, err := b.api.AllowPeer(context.Background(), "a", store.PeerAllow{Receive: []string{"bead"}}, "jo")
			must(t, err)
			mine := a.bead(t, "Add the export")
			for i := 0; i < 3; i++ {
				a.pass(t)
				b.pass(t)
			}
			got := a.get(t, mine.ID)
			if got.Stage != "decide" || got.Hold || len(a.live(t)) != 0 {
				t.Fatalf("a's bead after b refused it = %+v", got)
			}
			adv := a.beadEvents(t, mine.ID, "advanced")
			if len(adv) != 1 || adv[0].Data["outcome"] != "refused" || adv[0].Actor != "yard" {
				t.Fatalf("a's advanced = %+v", adv)
			}
			ns := a.notes(t, mine.ID)
			if len(ns) != 1 || ns[0].Author != "yard" || !strings.Contains(ns[0].Text, "The yard b did not take this bead: it has no intake for this yard's beads") {
				t.Fatalf("a's note of the refusal = %+v", ns)
			}
			if ev := a.beadEvents(t, mine.ID, "peer_bead_refused"); len(ev) != 1 || ev[0].Actor != "yard" || ev[0].Data["peer"] != "b" || ev[0].Data["reason"] != "it has no intake for this yard's beads" {
				t.Fatalf("a's peer_bead_refused = %+v", ev)
			}
			if line := a.get(t, mine.ID).Away().Line(); !strings.HasPrefix(line, "was at b: sent ") || !strings.Contains(line, "refused") || !strings.Contains(line, "no intake") {
				t.Fatalf("a's bead says %q", line)
			}
			if b.own != nil {
				if bs, _ := b.api.ListBeads(context.Background(), store.BeadFilter{All: true}); len(bs) != 0 {
					t.Fatalf("b made %d beads of one it refused", len(bs))
				}
			}
		})
	}
}

// b is down for as long as a tries: after its group's tries, five by
// default and about a quarter of an hour, the bead ends its stage as
// undelivered, with a note that says since when b was not reached and how
// to send again. Mail to the same yard is still tried: it is given up
// after the daemon's own count. Sent again once b is up, the bead arrives.
func TestPeerBeadUndelivered(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	a.sends(t, "b", nil)
	b.takes(t, "a", "web", true)
	a.mayor(t, "b")
	ctx := context.Background()
	_, err := a.api.MailPeer(ctx, "mayor", "b", "are you there", "mayor")
	must(t, err)
	b.down.Store(true)
	mine := a.bead(t, "Add the export")
	// The mail is first in the outbox, and is what each pass posts: a try
	// that does not reach b is a try of the bead behind it too.
	for i := 0; i < store.PeerBeadTries; i++ {
		if got := a.get(t, mine.ID); i > 0 && got.Stage != "build" {
			t.Fatalf("a's bead was given up after %d tries", i)
		}
		a.pass(t)
		clock.now = clock.now.Add(2 * time.Hour)
	}
	got := a.get(t, mine.ID)
	if got.Stage != "decide" || len(a.live(t)) != 0 {
		t.Fatalf("a's bead after %d tries = %+v; outbox %+v", store.PeerBeadTries, got, a.waiting(t, "b"))
	}
	un := a.beadEvents(t, mine.ID, "peer_bead_undelivered")
	if len(un) != 1 || un[0].Data["peer"] != "b" || un[0].Data["attempts"] != float64(store.PeerBeadTries) && un[0].Data["attempts"] != store.PeerBeadTries {
		t.Fatalf("a's peer_bead_undelivered = %+v", un)
	}
	adv := a.beadEvents(t, mine.ID, "advanced")
	if len(adv) != 1 || adv[0].Data["outcome"] != "undelivered" || adv[0].Data["to"] != "decide" {
		t.Fatalf("a's advanced = %+v", adv)
	}
	ns := a.notes(t, mine.ID)
	if len(ns) != 1 || !strings.Contains(ns[0].Text, "This bead did not reach the yard b: b was not reached in 5 tries since ") ||
		!strings.Contains(ns[0].Text, "yardr bead advance "+mine.ID+" --to build") {
		t.Fatalf("a's note of the bead it could not deliver = %+v", ns)
	}
	// Mail keeps its own give-up: after as many failures it still waits.
	w := a.waiting(t, "b")
	if len(w) != 1 || w[0].Kind != store.PeerMail || w[0].Attempts != store.PeerBeadTries {
		t.Fatalf("a's outbox after the bead was given up = %+v", w)
	}
	if failed := a.events(t, "peer_message_failed"); len(failed) != 1 || failed[0].Data["kind"] != "bead" {
		t.Fatalf("a's peer_message_failed = %+v", failed)
	}

	// The person at a sends it again, and b is back.
	b.down.Store(false)
	_, err = a.api.Advance(ctx, mine.ID, "build", store.Expect{}, "jo")
	must(t, err)
	clock.now = clock.now.Add(2 * time.Hour)
	a.pass(t)
	theirs := b.from(t, "a")
	if theirs.Peer.Bead != mine.ID || len(b.events(t, "peer_bead_arrived")) != 1 {
		t.Fatalf("the bead sent again arrived as %+v", theirs)
	}
	sent := a.beadEvents(t, mine.ID, "peer_bead_sent")
	if len(sent) != 2 || sent[0].Data["id"] == sent[1].Data["id"] {
		t.Fatalf("a's peer_bead_sent, sent again = %+v", sent)
	}
	if a.get(t, mine.ID).Away().ReturnedAt != nil {
		t.Fatal("the bead sent again still says how its first stay ended")
	}
}

// A group may say after how many tries its beads are given up.
func TestPeerBeadTriesOfTheGroup(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	a.sends(t, "b", map[string]any{store.GroupTries: float64(2)})
	b.down.Store(true)
	mine := a.bead(t, "Add the export")
	a.pass(t)
	if a.get(t, mine.ID).Stage != "build" {
		t.Fatal("given up after one try of two")
	}
	clock.now = clock.now.Add(time.Hour)
	a.pass(t)
	if got := a.get(t, mine.ID); got.Stage != "decide" {
		t.Fatalf("a's bead after its group's two tries = %+v", got)
	}
}

// A stage that does not name the outcome that came keeps the bead, held,
// with a note naming the outcome and the stage's own; unheld, it is sent
// to the yard again, as a new message.
func TestPeerBeadOutcomeTheStageDoesNotName(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	a.sends(t, "b", nil)
	b.takes(t, "a", "web", true)
	ctx := context.Background()
	mine := a.bead(t, "Add the export")
	a.pass(t)
	theirs := b.from(t, "a")
	_, err := b.api.Done(ctx, theirs.ID, "odd", "", store.Expect{}, "builder")
	must(t, err)
	b.pass(t)
	got := a.get(t, mine.ID)
	if got.Stage != "build" || !got.Hold || got.Group != nil || len(a.live(t)) != 0 {
		t.Fatalf("a's bead after an outcome its stage does not name = %+v", got)
	}
	ns := a.notes(t, mine.ID)
	if len(ns) != 1 || !strings.Contains(ns[0].Text, "ended as odd, which the stage does not name (it names done, question, failed, refused, undelivered)") ||
		!strings.Contains(ns[0].Text, "yardr bead unhold "+mine.ID) {
		t.Fatalf("a's note = %+v", ns)
	}
	held := a.beadEvents(t, mine.ID, "held")
	if len(held) != 1 || held[0].Actor != "peer:b" || held[0].Data["outcome"] != "odd" {
		t.Fatalf("a's held = %+v", held)
	}
	for i := 0; i < 2; i++ {
		a.pass(t)
	}
	if len(a.beadEvents(t, mine.ID, "peer_bead_sent")) != 1 {
		t.Fatal("a held bead was sent again")
	}
	_, err = a.api.SetHold(ctx, mine.ID, false, store.Expect{}, "jo")
	must(t, err)
	a.pass(t)
	sent := a.beadEvents(t, mine.ID, "peer_bead_sent")
	if len(sent) != 2 || sent[0].Data["id"] == sent[1].Data["id"] {
		t.Fatalf("a's peer_bead_sent after the unhold = %+v", sent)
	}
	if again := b.from(t, "a"); again.ID == theirs.ID || again.Peer.Bead != mine.ID {
		t.Fatalf("b's bead of the second sending = %+v", again)
	}
}

// A person takes the bead back while it is away: the return that comes
// later is logged as stale and changes nothing.
func TestPeerBeadTakenBack(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	a.sends(t, "b", nil)
	b.takes(t, "a", "web", true)
	ctx := context.Background()
	mine := a.bead(t, "Add the export")
	a.pass(t)
	theirs := b.from(t, "a")
	_, err := a.api.Advance(ctx, mine.ID, "decide", store.Expect{}, "jo")
	must(t, err)
	if len(a.live(t)) != 0 {
		t.Fatal("taking the bead back left its session live")
	}
	_, err = b.api.Done(ctx, theirs.ID, "done", "built", store.Expect{}, "builder")
	must(t, err)
	b.pass(t)
	a.pass(t)
	got := a.get(t, mine.ID)
	if got.Stage != "decide" || len(a.notes(t, mine.ID)) != 0 || len(a.beadEvents(t, mine.ID, "advanced")) != 1 {
		t.Fatalf("a's bead after a return for a stay it had left = %+v", got)
	}
	ret := a.beadEvents(t, mine.ID, "peer_bead_returned")
	if len(ret) != 1 || ret[0].Data["stale"] != true || ret[0].Data["outcome"] != "done" {
		t.Fatalf("a's peer_bead_returned = %+v", ret)
	}
	if len(b.waiting(t, "a")) != 0 {
		t.Fatal("the stale return still waits at b: a did not take it")
	}
}

// A bead at a yard whose link is removed, or stops carrying beads out,
// ends its stage as undelivered: no return can come.
func TestPeerBeadLinkGoesWhileAway(t *testing.T) {
	for name, cut := range map[string]func(a *peerYard) error{
		"removed": func(a *peerYard) error { return a.api.DeletePeer(context.Background(), "b", "jo") },
		"no beads out": func(a *peerYard) error {
			_, err := a.api.AllowPeer(context.Background(), "b", store.PeerAllow{NoSend: []string{"bead"}}, "jo")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			clock := newClock()
			a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
			link(t, a, b, true, true)
			a.sends(t, "b", nil)
			b.takes(t, "a", "web", true)
			there, waiting := a.bead(t, "Delivered"), (*store.Bead)(nil)
			a.pass(t)
			b.down.Store(true)
			waiting = a.bead(t, "Still in the outbox")
			a.pass(t)
			must(t, cut(a))
			for _, id := range []string{there.ID, waiting.ID} {
				got := a.get(t, id)
				if got.Stage != "decide" || len(a.beadEvents(t, id, "peer_bead_undelivered")) != 1 {
					t.Fatalf("a's bead %s after the link went = %+v", got.Title, got)
				}
				if ns := a.notes(t, id); len(ns) != 1 || !strings.Contains(ns[0].Text, "the link with b") {
					t.Fatalf("its note = %+v", ns)
				}
			}
			if name == "removed" {
				if line := a.get(t, there.ID).Away().Line(); !strings.HasPrefix(line, "was at the yard that was b (link removed): ") {
					t.Fatalf("a's bead says %q", line)
				}
				return
			}
			if w := a.waiting(t, "b"); len(w) != 0 {
				t.Fatalf("a still posts %+v over a link that carries no beads", w)
			}
			// A bead that enters the stage now cannot be sent at all.
			late := a.bead(t, "After the link stopped")
			a.pass(t)
			if got := a.get(t, late.ID); got.Stage != "decide" {
				t.Fatalf("a bead for a link that carries none = %+v", got)
			}
			if ns := a.notes(t, late.ID); len(ns) != 1 || !strings.Contains(ns[0].Text, "does not carry bead out") {
				t.Fatalf("its note = %+v", ns)
			}
		})
	}
}

// Renaming the link changes nothing of a bead that is away: its group
// sends to the yard under the new name, the bead's line follows, and so
// does when the yard was last heard from.
func TestPeerBeadRename(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	a.sends(t, "b", nil)
	b.takes(t, "a", "web", true)
	ctx := context.Background()
	mine := a.bead(t, "Add the export")
	a.pass(t)
	heard := a.get(t, mine.ID).Away().HeardAt
	if heard == nil {
		t.Fatal("b was not heard from after it took the bead")
	}
	_, err := a.api.RenamePeer(ctx, "b", "mac", "jo")
	must(t, err)
	g, err := a.api.GetGroup(ctx, "at-b")
	must(t, err)
	if g.AtPeer() != "mac" {
		t.Fatalf("the group after the rename sends to %q", g.AtPeer())
	}
	away := a.get(t, mine.ID).Away()
	if away.Peer != "mac" || away.HeardAt == nil || !away.HeardAt.Equal(*heard) || !strings.Contains(away.Line(), "at mac: sent ") || !strings.Contains(away.Line(), "mac last heard ") {
		t.Fatalf("a's bead after the rename = %+v, %q", away, away.Line())
	}
	if p, _ := a.api.GetPeer(ctx, "mac"); p.HeardAt == nil || !p.HeardAt.Equal(*heard) || p.Away != 1 {
		t.Fatalf("the peer after the rename = %+v", p)
	}
	theirs := b.from(t, "a")
	_, err = b.api.Done(ctx, theirs.ID, "done", "built", store.Expect{}, "builder")
	must(t, err)
	b.pass(t)
	got := a.get(t, mine.ID)
	if adv := a.beadEvents(t, mine.ID, "advanced"); got.Stage != "review" || len(adv) != 1 || adv[0].Actor != "peer:mac" {
		t.Fatalf("a's bead after the renamed yard's return = %+v, %+v", got, adv)
	}
	if ns := a.notes(t, mine.ID); len(ns) != 1 || ns[0].Author != "builder@mac" {
		t.Fatalf("a's notes = %+v", ns)
	}
	// And the next bead goes out under the new name.
	next := a.bead(t, "The next one")
	a.pass(t)
	if sent := a.beadEvents(t, next.ID, "peer_bead_sent"); len(sent) != 1 || sent[0].Data["peer"] != "mac" {
		t.Fatalf("peer_bead_sent after the rename = %+v", sent)
	}
}

// A bead may be sent on: a's goes to b, whose flow sends it to c. c's
// outcome ends b's stage, b's flow ends and returns to a, and a's bead
// moves on: the chain unwinds hop by hop.
func TestPeerBeadSentOn(t *testing.T) {
	clock := newClock()
	a, b, c := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock), newPeerYard(t, "c", clock)
	link(t, a, b, true, true)
	link(t, b, c, true, true)
	ctx := context.Background()
	a.sends(t, "b", nil)
	// b takes a's beads into a flow whose one working stage c works.
	must(t, b.api.PutRig(ctx, store.Rig{Name: "web", Base: "origin/main"}, "jo"))
	putFlow(t, b, "relay", "build -> returned on done | returned on failed\nreturned terminal\n")
	_, err := b.api.AllowPeer(ctx, "a", store.PeerAllow{Receive: []string{"bead"}}, "jo")
	must(t, err)
	_, err = b.api.SetPeerIntake(ctx, "a", &store.PeerIntake{Rig: "web", Flow: "relay", Auto: true}, "jo")
	must(t, err)
	_, err = b.api.AllowPeer(ctx, "c", store.PeerAllow{Send: []string{"bead"}}, "jo")
	must(t, err)
	must(t, b.api.PutGroup(ctx, store.Group{Name: "at-c", Runner: store.PeerRunner, Config: map[string]any{store.GroupPeer: "c"}}))
	_, err = b.api.AddRoute(ctx, store.Route{Stage: "build", Group: "at-c"})
	must(t, err)
	c.takes(t, "b", "web", true)

	mine := a.bead(t, "Add the export")
	a.pass(t)
	b.pass(t)
	atB := b.from(t, "a")
	if away := atB.Away(); atB.From() == nil || atB.From().Peer != "a" || away == nil || away.Peer != "c" || atB.Peer.Onward != away {
		t.Fatalf("b's bead, from a and at c = %+v", atB.Peer)
	}
	atC := c.from(t, "b")
	if atC.Peer.Bead != atB.ID || atC.Title != "Add the export" {
		t.Fatalf("c's bead = %+v", atC)
	}
	_, err = c.api.Done(ctx, atC.ID, "done", "built at c", store.Expect{}, "builder")
	must(t, err)
	c.pass(t)
	b.pass(t)
	if got := b.get(t, atB.ID); got.Status != "closed" || got.Stage != "returned" || got.Peer.Outcome != "done" {
		t.Fatalf("b's bead after c's done = %+v, %+v", got, got.Peer)
	}
	got := a.get(t, mine.ID)
	if got.Stage != "review" {
		t.Fatalf("a's bead after the chain unwound = %+v", got)
	}
	// c's note came to b as builder@c, and went on to a as b's own note of
	// that author: a knows b, not c.
	if ns := a.notes(t, mine.ID); len(ns) != 1 || ns[0].Text != "built at c" || !strings.HasSuffix(ns[0].Author, "@b") {
		t.Fatalf("a's notes = %+v", ns)
	}
}

// A flow that would send a bead back to a yard it has been at is stopped
// there: a's bead goes to b, b's flow sends it to a, and a refuses it as a
// loop, which ends b's stage as refused and so returns to a.
func TestPeerBeadLoop(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	ctx := context.Background()
	a.sends(t, "b", nil)
	a.takes(t, "b", "inbox", true)
	must(t, b.api.PutRig(ctx, store.Rig{Name: "web", Base: "origin/main"}, "jo"))
	putFlow(t, b, "back", "build -> returned on done | returned on refused\nreturned terminal\n")
	_, err := b.api.AllowPeer(ctx, "a", store.PeerAllow{Receive: []string{"bead"}, Send: []string{"bead"}}, "jo")
	must(t, err)
	_, err = b.api.SetPeerIntake(ctx, "a", &store.PeerIntake{Rig: "web", Flow: "back", Auto: true}, "jo")
	must(t, err)
	must(t, b.api.PutGroup(ctx, store.Group{Name: "at-a", Runner: store.PeerRunner, Config: map[string]any{store.GroupPeer: "a"}}))
	_, err = b.api.AddRoute(ctx, store.Route{Stage: "build", Group: "at-a"})
	must(t, err)

	mine := a.bead(t, "Add the export")
	for i := 0; i < 3; i++ {
		a.pass(t)
		b.pass(t)
	}
	if bs, _ := a.api.ListBeads(ctx, store.BeadFilter{All: true, Rig: "inbox"}); len(bs) != 0 {
		t.Fatalf("a took its own bead back as a new one: %+v", bs)
	}
	ref := a.events(t, "peer_message_refused")
	if len(ref) != 1 || ref[0].Data["reason"] != store.RefusedLoop || ref[0].Data["kind"] != "bead" {
		t.Fatalf("a's peer_message_refused = %+v", ref)
	}
	bs, err := b.api.ListBeads(ctx, store.BeadFilter{All: true})
	must(t, err)
	if len(bs) != 1 || bs[0].Status != "closed" {
		t.Fatalf("b's bead after a refused the loop = %+v", bs)
	}
	if ev := b.beadEvents(t, bs[0].ID, "peer_bead_refused"); len(ev) != 1 || !strings.Contains(ev[0].Data["reason"].(string), "it would go round") {
		t.Fatalf("b's peer_bead_refused = %+v", ev)
	}
	got := a.get(t, mine.ID)
	if got.Stage != "decide" {
		t.Fatalf("a's bead after b's flow ended as refused = %+v", got)
	}
	if adv := a.beadEvents(t, mine.ID, "advanced"); len(adv) != 1 || adv[0].Data["outcome"] != "refused" || adv[0].Actor != "peer:b" {
		t.Fatalf("a's advanced = %+v", adv)
	}
}

// A peer session is a wait: max_age raises session_aged once, and nothing
// ends the session by its age, with a grace set or none.
func TestPeerBeadWaitsPastItsAge(t *testing.T) {
	for name, set := range map[string]map[string]any{
		"no grace": {"max_age": "1ms"},
		"grace":    {"max_age": "1ms", "grace": "0"},
	} {
		t.Run(name, func(t *testing.T) {
			clock := newClock()
			a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
			link(t, a, b, true, true)
			a.sends(t, "b", set)
			b.takes(t, "a", "web", false)
			mine := a.bead(t, "Add the export")
			a.pass(t)
			for i := 0; i < 4; i++ {
				time.Sleep(5 * time.Millisecond) // past twice its age, the cap of another session
				a.pass(t)
			}
			aged := a.beadEvents(t, mine.ID, "session_aged")
			if len(aged) != 1 || aged[0].Data["stop_after"] != nil {
				t.Fatalf("session_aged of a bead at a peer = %+v", aged)
			}
			if ss := a.live(t); len(ss) != 1 || ss[0].Bead != mine.ID {
				t.Fatalf("the session of a bead at a peer after its age = %+v", ss)
			}
			if got := a.get(t, mine.ID); got.Stage != "build" || got.Group == nil || len(a.beadEvents(t, mine.ID, "peer_bead_sent")) != 1 {
				t.Fatalf("a's bead after its age = %+v", got)
			}
		})
	}
}

// A bead b took held and nobody released goes back when the time b's
// intake gives has passed: b's daemon closes it and posts the return, and
// a's flow routes its bead on refused, with b's note. A daemon of b made
// anew after the time (the one before was down through it) does the same
// on its first pass, and no later pass returns it again. A bead of the
// same intake that was released stays.
func TestPeerBeadNobodyReleased(t *testing.T) {
	for name, anew := range map[string]bool{"the daemon that took it": false, "a daemon made anew": true} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			clock := newClock()
			a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
			link(t, a, b, true, true)
			a.sends(t, "b", nil)
			b.takes(t, "a", "web", false)
			_, err := b.api.SetPeerIntake(ctx, "a", &store.PeerIntake{Rig: "web", Flow: "taking", HoldFor: "6h"}, "jo")
			must(t, err)
			kept, mine := a.bead(t, "Add the import"), a.bead(t, "Add the export")
			a.pass(t)
			b.pass(t)
			bs, err := b.api.ListBeads(ctx, store.BeadFilter{Label: "from-a"})
			must(t, err)
			if len(bs) != 2 {
				t.Fatalf("b has %d beads from a, want two", len(bs))
			}
			var theirs, released *store.Bead
			for i := range bs {
				if bs[i].Title == "Add the export" {
					theirs = &bs[i]
				} else {
					released = &bs[i]
				}
			}
			_, err = b.api.SetHold(ctx, released.ID, false, store.Expect{}, "jo")
			must(t, err)
			// The store's clock is the machine's: the bead arrived just now.
			clock.now = time.Now().Add(3*time.Hour + time.Minute)
			b.pass(t)
			b.pass(t)
			if ev := b.beadEvents(t, theirs.ID, "peer_bead_expiring"); len(ev) != 1 || ev[0].Data["peer"] != "a" || b.get(t, theirs.ID).Status != "open" {
				t.Fatalf("b's peer_bead_expiring at half the time = %+v", ev)
			}
			clock.now = time.Now().Add(7 * time.Hour)
			if anew {
				b.d = &dispatch.Dispatcher{Store: b.api, Runners: b.d.Runners, PeerKey: b.d.PeerKey}
				dispatch.SetClock(b.d, func() time.Time { return clock.now })
			}
			for i := 0; i < 3; i++ {
				b.pass(t)
				a.pass(t)
			}
			if got := b.get(t, theirs.ID); got.Status != "closed" || got.From().Outcome != "refused" || len(b.beadEvents(t, theirs.ID, "peer_bead_returned")) != 1 {
				t.Fatalf("b's bead after its time = %+v", got)
			}
			if got := b.get(t, released.ID); got.Status != "open" || len(b.beadEvents(t, released.ID, "peer_bead_expiring")) != 0 {
				t.Fatalf("b's bead that was released = %+v", got)
			}
			got := a.get(t, mine.ID)
			if got.Stage != "decide" || got.Hold {
				t.Fatalf("a's bead after b let its time pass = %+v", got)
			}
			if adv := a.beadEvents(t, mine.ID, "advanced"); len(adv) != 1 || adv[0].Data["outcome"] != "refused" || adv[0].Actor != "peer:b" {
				t.Fatalf("a's advanced = %+v", adv)
			}
			if ns := a.notes(t, mine.ID); len(ns) != 1 || ns[0].Author != "yard@b" || ns[0].Text != "not released within 6h at the other yard" {
				t.Fatalf("a's note of it = %+v", ns)
			}
			if got := a.get(t, kept.ID); got.Stage != "build" || len(a.beadEvents(t, kept.ID, "advanced")) != 0 {
				t.Fatalf("a's bead that b released = %+v", got)
			}
		})
	}
}
