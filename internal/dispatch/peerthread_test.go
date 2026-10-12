package dispatch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

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
