package dispatch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// staged is a flow for beads from another yard with a stage to arrive in
// and one to be worked in.
const staged = "arrived -> work on done\nwork -> returned on done | returned on failed\nreturned terminal\n"

// threadYards are a, whose stage build b works, and b, which takes a's
// beads into the flow staged. The link carries thread from b to a as the
// two arguments say: bSends on b's side, aReceives on a's.
func threadYards(t *testing.T, aCallsB, bSends, aReceives bool) (a, b *peerYard) {
	t.Helper()
	clock := newClock()
	a, b = newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, aCallsB, true)
	a.sends(t, "b", nil)
	b.takes(t, "a", "web")
	ctx := context.Background()
	putFlow(t, b, "staged", staged)
	_, err := b.api.SetPeerIntake(ctx, "a", &store.PeerIntake{Depot: "web", Flow: "staged"}, "jo")
	must(t, err)
	if bSends {
		_, err := b.api.AllowPeer(ctx, "a", store.PeerAllow{Send: []string{store.PeerThread}}, "jo")
		must(t, err)
	}
	if aReceives {
		_, err := a.api.AllowPeer(ctx, "b", store.PeerAllow{Receive: []string{store.PeerThread}}, "jo")
		must(t, err)
	}
	return a, b
}

// cross lets what waits on either side cross, whichever yard calls.
func cross(t *testing.T, a, b *peerYard) {
	t.Helper()
	for i := 0; i < 3; i++ {
		b.pass(t)
		a.pass(t)
	}
}

// While b works a's bead, b tells a how its bead stands: one bead event
// for each of arrived, advanced, held, unheld and gave_up, over a link that
// sends thread at b and receives it at a. a keeps what b said last on the
// record of the stay, and bead show and the bead's JSON say it. The move
// that ends b's bead sends no event: the return says it, and after it the
// line is gone.
func TestPeerBeadEvents(t *testing.T) {
	for name, aCallsB := range map[string]bool{"push": true, "b asks": false} {
		t.Run(name, func(t *testing.T) {
			a, b := threadYards(t, aCallsB, true, true)
			ctx := context.Background()
			mine := a.bead(t, "Add the export")
			a.pass(t)
			cross(t, a, b)
			theirs := b.from(t, "a")

			said := func(n int, event, stage string, held bool) {
				t.Helper()
				cross(t, a, b)
				evs := a.beadEvents(t, mine.ID, "peer_bead_event")
				if len(evs) != n {
					t.Fatalf("a has %d peer_bead_event after %s, want %d: %+v", len(evs), event, n, evs)
				}
				e := evs[len(evs)-1]
				if e.Actor != "peer:b" || e.Data["event"] != event || e.Data["stage"] != stage || e.Data["held"] != held ||
					e.Data["from"] != theirs.ID || e.Data["peer"] != "b" || e.Data["stale"] != nil {
					t.Fatalf("a's peer_bead_event for %s = %+v", event, e)
				}
				away := a.get(t, mine.ID).Away()
				if away == nil || away.Bead != theirs.ID || away.ThereStage != stage || away.ThereHeld != held || away.SaidAt == nil {
					t.Fatalf("a's bead after %s = %+v", event, away)
				}
				want := fmt.Sprintf("there it is %s, in the stage %s", theirs.ID, stage)
				if held {
					want += ", held"
				}
				want += " (as b said " + away.SaidAt.Local().Format("01-02 15:04") + ")"
				if got := away.There(); got != want {
					t.Fatalf("a's second line after %s = %q, want %q", event, got, want)
				}
				// The first line is as it was before the word.
				if !strings.HasPrefix(away.Line(), "at b: sent ") || !strings.Contains(away.Line(), ", b last heard ") {
					t.Fatalf("a's first line after %s = %q", event, away.Line())
				}
				// An event moves nothing here.
				if got := a.get(t, mine.ID); got.Stage != "build" || got.Hold || got.Group == nil || len(a.live(t)) != 1 {
					t.Fatalf("a's bead moved by the event %s: %+v", event, got)
				}
			}
			said(1, "arrived", "arrived", false)

			_, err := b.api.Done(ctx, theirs.ID, "done", "", store.Expect{}, "jo")
			must(t, err)
			said(2, "advanced", "work", false)
			if e := a.beadEvents(t, mine.ID, "peer_bead_event")[1]; e.Data["left"] != "arrived" {
				t.Fatalf("a's advanced event does not say the stage left: %+v", e)
			}

			_, err = b.api.SetHold(ctx, theirs.ID, true, store.Expect{}, "jo")
			must(t, err)
			said(3, "held", "work", true)
			raw, _ := json.Marshal(a.get(t, mine.ID))
			var asJSON struct {
				Peer struct {
					Bead       string `json:"bead"`
					ThereStage string `json:"there_stage"`
					ThereHeld  bool   `json:"there_held"`
					SaidAt     string `json:"said_at"`
				} `json:"peer"`
			}
			if json.Unmarshal(raw, &asJSON) != nil || asJSON.Peer.Bead != theirs.ID || asJSON.Peer.ThereStage != "work" || !asJSON.Peer.ThereHeld || asJSON.Peer.SaidAt == "" {
				t.Fatalf("a's bead as JSON = %s", raw)
			}

			must(t, b.api.LogDispatch(ctx, theirs.ID, "gave_up", "yard", map[string]any{"stage": "work", "failures": 3}))
			said(4, "gave_up", "work", true)

			_, err = b.api.SetHold(ctx, theirs.ID, false, store.Expect{}, "jo")
			must(t, err)
			said(5, "unheld", "work", false)

			// Each was one message, and nothing of them waits.
			sent := slices.DeleteFunc(b.events(t, "peer_message_sent"), func(e store.Event) bool { return e.Data["kind"] != store.PeerBeadEvent })
			if len(sent) != 5 || len(b.waiting(t, "a")) != 0 {
				t.Fatalf("b sent %d bead events and has %d messages waiting, want 5 and 0", len(sent), len(b.waiting(t, "a")))
			}

			// The end is told by the return alone.
			_, err = b.api.Done(ctx, theirs.ID, "done", "", store.Expect{}, "builder")
			must(t, err)
			cross(t, a, b)
			if evs := a.beadEvents(t, mine.ID, "peer_bead_event"); len(evs) != 5 {
				t.Fatalf("a has %d peer_bead_event after the return, want 5", len(evs))
			}
			got := a.get(t, mine.ID)
			if got.Stage != "review" || got.Away().There() != "" || !strings.Contains(got.Away().Line(), "returned done") {
				t.Fatalf("a's bead after the return = %+v, %q, %q", got, got.Away().Line(), got.Away().There())
			}
			// What happens to b's bead after it went back is b's alone.
			_, err = b.api.Reopen(ctx, theirs.ID, store.Expect{}, "jo")
			must(t, err)
			_, err = b.api.SetHold(ctx, theirs.ID, true, store.Expect{}, "jo")
			must(t, err)
			cross(t, a, b)
			sent = slices.DeleteFunc(b.events(t, "peer_message_sent"), func(e store.Event) bool { return e.Data["kind"] != store.PeerBeadEvent })
			if len(sent) != 5 || len(b.waiting(t, "a")) != 0 {
				t.Fatalf("b told a of a returned bead: %d bead events sent, %d waiting", len(sent), len(b.waiting(t, "a")))
			}
		})
	}
}

// A link without the word behaves as it did before it, on both sides: b
// queues nothing and logs nothing of it, a keeps nothing, and the bead goes
// there and comes back. With the word on b's side alone, b's events are
// refused by a as a kind its link does not carry and given up at b, and
// the bead still goes and comes back.
func TestPeerBeadEventsWithoutTheWord(t *testing.T) {
	for name, bSends := range map[string]bool{"neither": false, "b sends, a does not receive": true} {
		t.Run(name, func(t *testing.T) {
			a, b := threadYards(t, true, bSends, false)
			ctx := context.Background()
			mine := a.bead(t, "Add the export")
			a.pass(t)
			cross(t, a, b)
			theirs := b.from(t, "a")
			_, err := b.api.Done(ctx, theirs.ID, "done", "", store.Expect{}, "jo")
			must(t, err)
			_, err = b.api.SetHold(ctx, theirs.ID, true, store.Expect{}, "jo")
			must(t, err)
			_, err = b.api.SetHold(ctx, theirs.ID, false, store.Expect{}, "jo")
			must(t, err)
			cross(t, a, b)

			away := a.get(t, mine.ID).Away()
			if away.SaidAt != nil || away.ThereStage != "" || away.Bead != "" || away.There() != "" || len(a.beadEvents(t, mine.ID, "peer_bead_event")) != 0 {
				t.Fatalf("a kept what a link without the word carried: %+v", away)
			}
			of := func(y *peerYard, kind string) []store.Event {
				return slices.DeleteFunc(y.events(t, kind), func(e store.Event) bool { return e.Data["kind"] != store.PeerBeadEvent })
			}
			if bSends {
				// arrived, advanced, held, unheld: each refused by name of
				// the reason, and given up.
				refused, failed := of(a, "peer_message_refused"), of(b, "peer_message_failed")
				if len(refused) != 4 || refused[0].Data["reason"] != store.RefusedKind || len(failed) != 4 ||
					!strings.Contains(fmt.Sprint(failed[0].Data["reason"]), "its link does not carry thread in") {
					t.Fatalf("a refused %+v, b gave up %+v", refused, failed)
				}
			} else if n := len(of(b, "peer_message_refused")) + len(of(b, "peer_message_sent")) + len(of(b, "peer_message_failed")) + len(of(a, "peer_message_refused")); n != 0 {
				t.Fatalf("%d events about bead events over a link without the word", n)
			}
			if got := b.get(t, theirs.ID); got.Stage != "work" || got.Hold {
				t.Fatalf("b's bead = %+v", got)
			}

			_, err = b.api.Done(ctx, theirs.ID, "done", "", store.Expect{}, "builder")
			must(t, err)
			cross(t, a, b)
			if got := a.get(t, mine.ID); got.Stage != "review" || got.Hold {
				t.Fatalf("a's bead after b's done = %+v", got)
			}
		})
	}
}

// Switching thread off drops an event that already waits: b queues one
// while a is down, the link stops sending thread, and when a is back the
// event is dropped, logged with why, and never posted; a takes nothing.
// An event that reaches a yard whose link no longer receives thread is
// refused as any kind the link does not carry.
func TestPeerBeadEventSwitchedOffWhileWaiting(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	a.sends(t, "b", nil)
	b.takes(t, "a", "web")
	ctx := context.Background()
	putFlow(t, b, "staged", staged)
	_, err := b.api.SetPeerIntake(ctx, "a", &store.PeerIntake{Depot: "web", Flow: "staged"}, "jo")
	must(t, err)
	_, err = b.api.AllowPeer(ctx, "a", store.PeerAllow{Send: []string{store.PeerThread}}, "jo")
	must(t, err)
	_, err = a.api.AllowPeer(ctx, "b", store.PeerAllow{Receive: []string{store.PeerThread}}, "jo")
	must(t, err)

	mine := a.bead(t, "Add the export")
	a.pass(t)
	cross(t, a, b)
	theirs := b.from(t, "a")
	if evs := a.beadEvents(t, mine.ID, "peer_bead_event"); len(evs) != 1 {
		t.Fatalf("a has %d peer_bead_event, want the arrival", len(evs))
	}
	of := func(y *peerYard, kind string) []store.Event {
		return slices.DeleteFunc(y.events(t, kind), func(e store.Event) bool { return e.Data["kind"] != store.PeerBeadEvent })
	}

	// b queues an event while a is down, and the link stops sending
	// thread before delivery resumes.
	a.down.Store(true)
	_, err = b.api.SetHold(ctx, theirs.ID, true, store.Expect{}, "jo")
	must(t, err)
	b.pass(t)
	if w := b.waiting(t, "a"); len(w) != 1 || w[0].Kind != store.PeerBeadEvent || w[0].Attempts != 1 {
		t.Fatalf("b's outbox with a down = %+v", w)
	}
	_, err = b.api.AllowPeer(ctx, "a", store.PeerAllow{NoSend: []string{store.PeerThread}}, "jo")
	must(t, err)

	// a is back: the event is dropped, not posted.
	a.down.Store(false)
	clock.now = clock.now.Add(time.Hour)
	b.pass(t)
	failed := of(b, "peer_message_failed")
	if len(failed) != 1 || failed[0].Data["peer"] != "a" ||
		!strings.Contains(fmt.Sprint(failed[0].Data["reason"]), "stopped carrying thread out") {
		t.Fatalf("b's peer_message_failed = %+v", failed)
	}
	if sent := of(b, "peer_message_sent"); len(sent) != 1 {
		t.Fatalf("b sent its waiting event after thread was switched off: %+v", sent)
	}
	if w := b.waiting(t, "a"); len(w) != 0 {
		t.Fatalf("b still posts %+v over a link that sends no thread", w)
	}
	if evs := a.beadEvents(t, mine.ID, "peer_bead_event"); len(evs) != 1 {
		t.Fatalf("a took the event after the switch-off: %+v", evs)
	}
	if away := a.get(t, mine.ID).Away(); away.ThereStage != "arrived" || away.ThereHeld {
		t.Fatalf("a's record after the switch-off = %+v", away)
	}
	// And it stays dropped: another pass neither posts nor logs again.
	b.pass(t)
	if failed, sent := of(b, "peer_message_failed"), of(b, "peer_message_sent"); len(failed) != 1 || len(sent) != 1 {
		t.Fatalf("after another pass b has %+v and %+v", failed, sent)
	}

	// The other way round: b sends thread again, a stops receiving it,
	// and the next event is refused as a kind the link does not carry.
	_, err = b.api.AllowPeer(ctx, "a", store.PeerAllow{Send: []string{store.PeerThread}}, "jo")
	must(t, err)
	_, err = a.api.AllowPeer(ctx, "b", store.PeerAllow{NoReceive: []string{store.PeerThread}}, "jo")
	must(t, err)
	_, err = b.api.SetHold(ctx, theirs.ID, false, store.Expect{}, "jo")
	must(t, err)
	cross(t, a, b)
	refused := of(a, "peer_message_refused")
	if len(refused) != 1 || refused[0].Data["reason"] != store.RefusedKind || refused[0].Data["direction"] != "in" {
		t.Fatalf("a's peer_message_refused = %+v", refused)
	}
	if failed := of(b, "peer_message_failed"); len(failed) != 2 ||
		!strings.Contains(fmt.Sprint(failed[1].Data["reason"]), "its link does not carry thread in") {
		t.Fatalf("b's peer_message_failed = %+v", failed)
	}
	if evs := a.beadEvents(t, mine.ID, "peer_bead_event"); len(evs) != 1 {
		t.Fatalf("a took the event after it stopped receiving thread: %+v", evs)
	}
	if w := b.waiting(t, "a"); len(w) != 0 {
		t.Fatalf("b still waits to post %+v", w)
	}
}

// An event b queued before its return and that a hears after the stay
// ended some other way is dropped and logged as stale: a took its bead
// back by hand while b was not reached.
func TestPeerBeadEventAfterTheStay(t *testing.T) {
	a, b := threadYards(t, true, true, true)
	ctx := context.Background()
	mine := a.bead(t, "Add the export")
	a.pass(t)
	cross(t, a, b)
	theirs := b.from(t, "a")
	if evs := a.beadEvents(t, mine.ID, "peer_bead_event"); len(evs) != 1 {
		t.Fatalf("a has %d peer_bead_event, want the arrival", len(evs))
	}
	_, err := a.api.Advance(ctx, mine.ID, "decide", store.Expect{}, "jo")
	must(t, err)
	_, err = b.api.SetHold(ctx, theirs.ID, true, store.Expect{}, "jo")
	must(t, err)
	b.pass(t)
	evs := a.beadEvents(t, mine.ID, "peer_bead_event")
	if len(evs) != 2 || evs[1].Data["stale"] != true || evs[1].Data["event"] != "held" ||
		!strings.Contains(fmt.Sprint(evs[1].Data["reason"]), "has left the stage build") {
		t.Fatalf("a's peer_bead_event for a bead it took back = %+v", evs)
	}
	// The record keeps what was said while the bead was there.
	if away := a.get(t, mine.ID).Away(); away.ThereStage != "arrived" || away.ThereHeld {
		t.Fatalf("a stale event changed the record: %+v", away)
	}
	if got := a.get(t, mine.ID); got.Stage != "decide" || got.Hold {
		t.Fatalf("a's bead = %+v", got)
	}
}

// What b says of its bead reaches whoever on a subscribed to the source
// peer, as one delivery for each event, and the daemon's pass over the
// deliveries puts none of them to a table: no hook received them. Both
// yards have the row for the pair among the bead's links.
func TestPeerSourceBetweenTwoYards(t *testing.T) {
	a, b := threadYards(t, true, true, true)
	ctx := context.Background()
	_, err := a.api.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr"}, "jo")
	must(t, err)
	must(t, a.api.AddSubscription(ctx, "clerk", store.Subscription{Source: store.SourcePeer, Scope: store.ScopeAll, Where: map[string]string{"peer": "b"}}, "clerk"))
	_, err = b.api.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr"}, "jo")
	must(t, err)
	must(t, b.api.AddSubscription(ctx, "clerk", store.Subscription{Source: store.SourcePeer, Scope: store.ScopeAll}, "clerk"))

	mine := a.bead(t, "Add the export")
	a.pass(t)
	cross(t, a, b)
	theirs := b.from(t, "a")
	_, err = b.api.Done(ctx, theirs.ID, "done", "", store.Expect{}, "jo")
	must(t, err)
	_, err = b.api.SetHold(ctx, theirs.ID, true, store.Expect{}, "jo")
	must(t, err)
	cross(t, a, b)

	page, err := a.api.Inbox(ctx, "clerk", 0)
	must(t, err)
	var lines []string
	for _, e := range page.Events {
		if e.Kind != "hook" || e.Actor != "peer:b" || e.Data["hook"] != nil || store.Reason(e, "clerk") != store.ReasonAll {
			t.Fatalf("in clerk's inbox on a: %+v", e)
		}
		lines = append(lines, store.PeerDeliveryLine(e.Data))
	}
	want := []string{
		fmt.Sprintf("peer b: %s arrived there, in the stage arrived (there %s)", mine.ID, theirs.ID),
		fmt.Sprintf("peer b: %s moved on there, to the stage work (there %s)", mine.ID, theirs.ID),
		fmt.Sprintf("peer b: %s is held there, in the stage work (there %s)", mine.ID, theirs.ID),
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("clerk on a heard\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	// The yard that works the bead hears nothing through the source: the
	// events go one way.
	if page, err := b.api.Inbox(ctx, "clerk", 0); err != nil || len(page.Events) != 0 {
		t.Fatalf("clerk on b heard %+v, %v", page.Events, err)
	}
	if got := a.get(t, mine.ID); got.Stage != "build" || got.Hold || len(a.events(t, "hook_rule")) != 0 {
		t.Fatalf("a delivery of the source peer moved a's bead or was put to a table: %+v, %+v", got, a.events(t, "hook_rule"))
	}

	at := a.get(t, mine.ID).PeerLinks()
	if len(at) != 1 || at[0].Kind != store.LinkAt || at[0].URL != theirs.ID+"@b" {
		t.Fatalf("a's row for the pair = %+v", at)
	}
	from := b.get(t, theirs.ID).PeerLinks()
	if len(from) != 1 || from[0].Kind != store.LinkFrom || from[0].URL != mine.ID+"@a" || from[0].Stage != "build" {
		t.Fatalf("b's row for the pair = %+v", from)
	}
	for y, issue := range map[*peerYard]string{a: theirs.ID + "@b", b: mine.ID + "@a"} {
		found, err := y.api.IssueLinks(ctx, issue)
		if err != nil || len(found) != 1 {
			t.Fatalf("the bead whose counterpart is %s = %+v, %v", issue, found, err)
		}
	}

	// The return ends the stay, and the row says so.
	_, err = b.api.SetHold(ctx, theirs.ID, false, store.Expect{}, "jo")
	must(t, err)
	_, err = b.api.Done(ctx, theirs.ID, "done", "", store.Expect{}, "builder")
	must(t, err)
	cross(t, a, b)
	if at := a.get(t, mine.ID).PeerLinks(); len(at) != 1 || at[0].Kind != store.LinkWasAt || at[0].Until == nil {
		t.Fatalf("a's row after the return = %+v", at)
	}
	// And no link is stored for the pair on either yard.
	for y, id := range map[*peerYard]string{a: mine.ID, b: theirs.ID} {
		if stored, err := y.api.Links(ctx, id); err != nil || len(stored) != 0 {
			t.Fatalf("the stored links of %s = %+v, %v", id, stored, err)
		}
	}
}
