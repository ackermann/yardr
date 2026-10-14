package store_test

import (
	"errors"
	"reflect"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// renamedAndAddedAnew is a yard whose peer north (the key y.key) was
// renamed to up, and whose name north was then given to another yard, with
// the key returned. Both links carry pings and mail, and one ping waits for
// each. It is what may happen between the receiver finding a request's peer
// by its key and the store taking the request under the name found.
func renamedAndAddedAnew(t *testing.T) (y *peerYard, second string, forUp, forNorth *store.PeerOut) {
	t.Helper()
	y = newPeerYard(t, "")
	y.member(t, "yardmaster", "")
	both := store.PeerAllow{Send: []string{"ping"}, Receive: []string{"ping", "mail"}, Expose: []string{"yardmaster"}}
	y.allow(t, both)
	forUp, err := y.api.PeerSend(y.ctx, "north", "ping", nil, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.RenamePeer(y.ctx, "north", "up", "jo"); err != nil {
		t.Fatal(err)
	}
	second = otherKey(t)
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "north", Key: second}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, both)
	if forNorth, err = y.api.PeerSend(y.ctx, "north", "ping", nil, "jo"); err != nil {
		t.Fatal(err)
	}
	return y, second, forUp, forNorth
}

// What one yard signed is not taken under a name that is another yard's by
// now: nothing of the yard that has the name is touched, and the same
// messages are taken under the name their sender has.
func TestPeerReceiveUnderANameThatIsAnothersNow(t *testing.T) {
	y, second, _, _ := renamedAndAddedAnew(t)
	msgs := []store.PeerMessage{mail("m1", "yardmaster", "clerk", "from the first north"), ping("p1")}
	before, err := y.api.GetPeer(y.ctx, "north")
	if err != nil {
		t.Fatal(err)
	}
	quiet(t, y.own, "a post under another yard's name", func() {
		if got, err := y.own.PeerReceive(y.ctx, "north", y.key, msgs); !errors.Is(err, store.ErrNotFound) || got != nil {
			t.Fatalf("PeerReceive under the name of another yard = %+v, %v", got, err)
		}
	})
	if after, _ := y.api.GetPeer(y.ctx, "north"); !reflect.DeepEqual(before, after) {
		t.Fatalf("the yard that has the name = %+v, was %+v", after, before)
	}
	if in := y.inbox(t, "yardmaster"); len(in) != 0 {
		t.Fatalf("yardmaster's inbox = %+v", in)
	}
	if evs := peerEvents(t, y.api, "peer_message_received"); len(evs) != 0 {
		t.Fatalf("peer_message_received = %+v", evs)
	}

	// Under its own name the sender's messages are taken, as its own.
	got, err := y.own.PeerReceive(y.ctx, "up", y.key, msgs)
	if err != nil || len(got) != 2 || got[0].Refused != "" || got[0].Duplicate || got[1].Refused != "" || got[1].Duplicate {
		t.Fatalf("PeerReceive under the sender's name = %+v, %v", got, err)
	}
	if in := y.inbox(t, "yardmaster"); len(in) != 1 || in[0].Actor != "clerk@up" {
		t.Fatalf("yardmaster's inbox = %+v", in)
	}
	// The ids the other yard has sent are its own: none was marked seen.
	got, err = y.own.PeerReceive(y.ctx, "north", second, msgs)
	if err != nil || len(got) != 2 || got[0].Duplicate || got[0].Refused != "" || got[1].Duplicate {
		t.Fatalf("the same ids from the yard that has the name = %+v, %v", got, err)
	}
	// And a key that is no peer's takes nothing under either name.
	if _, err := y.own.PeerReceive(y.ctx, "up", otherKey(t), []store.PeerMessage{ping("p2")}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("PeerReceive with a key that is no peer's = %v", err)
	}
}

// A question signed by one yard ends nothing that waits for the yard that
// has its old name, as sent or as refused, and is not answered with what
// waits for it.
func TestPeerHandOverUnderANameThatIsAnothersNow(t *testing.T) {
	y, second, forUp, forNorth := renamedAndAddedAnew(t)
	before, err := y.api.GetPeer(y.ctx, "north")
	if err != nil {
		t.Fatal(err)
	}
	for what, ask := range map[string]func() (*store.PeerPage, error){
		"a first question":       func() (*store.PeerPage, error) { return y.own.PeerHandOver(y.ctx, "north", y.key, 0, nil) },
		"a question that has it": func() (*store.PeerPage, error) { return y.own.PeerHandOver(y.ctx, "north", y.key, forNorth.Seq, nil) },
		"a question that refused it": func() (*store.PeerPage, error) {
			return y.own.PeerHandOver(y.ctx, "north", y.key, forNorth.Seq, map[uint64]store.Refusal{forNorth.Seq: {Reason: store.RefusedKind}})
		},
		"a question by no peer's key": func() (*store.PeerPage, error) {
			return y.own.PeerHandOver(y.ctx, "north", otherKey(t), forNorth.Seq, nil)
		},
	} {
		quiet(t, y.own, what, func() {
			if page, err := ask(); !errors.Is(err, store.ErrNotFound) || (page != nil && len(page.Messages) != 0) {
				t.Fatalf("%s under the name of another yard = %+v, %v", what, page, err)
			}
		})
	}
	if after, _ := y.api.GetPeer(y.ctx, "north"); !reflect.DeepEqual(before, after) || after.Waiting != 1 {
		t.Fatalf("the yard that has the name = %+v, was %+v", after, before)
	}
	if out, err := y.api.PeerOutbox(y.ctx, "north", 0); err != nil || len(out) != 1 || out[0].ID != forNorth.ID || out[0].Failed != nil {
		t.Fatalf("what waits for the yard that has the name = %+v, %v", out, err)
	}
	for _, kind := range []string{"peer_message_sent", "peer_message_failed"} {
		if evs := peerEvents(t, y.api, kind); len(evs) != 0 {
			t.Fatalf("%s = %+v", kind, evs)
		}
	}

	// Under its own name the asker gets what waits for it, and ends it.
	page, err := y.own.PeerHandOver(y.ctx, "up", y.key, 0, nil)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != forUp.ID {
		t.Fatalf("handed over under the asker's name = %+v, %v", page, err)
	}
	if page, err = y.own.PeerHandOver(y.ctx, "up", y.key, forUp.Seq, nil); err != nil || len(page.Messages) != 0 {
		t.Fatalf("the next question = %+v, %v", page, err)
	}
	if sent := peerEvents(t, y.api, "peer_message_sent"); len(sent) != 1 || sent[0].Data["id"] != forUp.ID || sent[0].Data["peer"] != "up" {
		t.Fatalf("peer_message_sent = %+v", sent)
	}
	// The yard that has the name still gets its own.
	if page, err = y.own.PeerHandOver(y.ctx, "north", second, 0, nil); err != nil || len(page.Messages) != 1 || page.Messages[0].ID != forNorth.ID {
		t.Fatalf("handed over to the yard that has the name = %+v, %v", page, err)
	}
}
