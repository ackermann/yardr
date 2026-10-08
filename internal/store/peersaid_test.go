package store_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// A message of a kind no build of this yard knows (a newer peer's) is
// refused as that, not as a kind the link does not carry: logged with the
// words, kept for the next question to tell the sender, and not remembered
// as taken. The messages that came with it in the same call are taken.
func TestPeerUnknownKind(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	y.allow(t, store.PeerAllow{Receive: []string{"ping"}, Send: []string{"ping"}})
	m1, m3 := ping("m1"), ping("m3")
	m2 := store.PeerMessage{ID: "m2", Kind: "handoff", Body: json.RawMessage(`{"secret":"hunter2"}`)}
	m1.Seq, m2.Seq, m3.Seq = 1, 2, 3
	body, err := json.Marshal(store.PeerPage{Messages: []store.PeerMessage{m1, m2, m3}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := y.api.PeerPulled(y.ctx, "north", store.PeerBatch{RequestSig: "req-1", Signature: yardkey.SignResponse(y.north, 200, "req-1", body), Body: body})
	if err != nil || len(res.Taken) != 3 {
		t.Fatalf("PeerPulled = %+v, %v", res, err)
	}
	if res.Taken[0].Refused != "" || res.Taken[2].Refused != "" || res.Taken[1].Refused != "kind_unknown" || store.RefusedUnknownKind != "kind_unknown" {
		t.Fatalf("taken = %+v, want the second alone refused as kind_unknown", res.Taken)
	}
	if got := peerEvents(t, y.api, "peer_message_received"); len(got) != 2 || got[0].Data["id"] != "m1" || got[1].Data["id"] != "m3" {
		t.Fatalf("peer_message_received = %+v", got)
	}
	ref := peerEvents(t, y.api, "peer_message_refused")
	if len(ref) != 1 || ref[0].Data["reason"] != "kind_unknown" || ref[0].Data["kind"] != "handoff" || ref[0].Data["id"] != "m2" ||
		ref[0].Data["detail"] != "this yard's yardr does not know handoff" || ref[0].Data["direction"] != "in" || ref[0].Data["way"] != "pull" ||
		ref[0].Actor != "peer:north" {
		t.Fatalf("peer_message_refused = %+v", ref)
	}
	// The next question tells north, by the message's place in its outbox.
	p, _ := y.api.GetPeer(y.ctx, "north")
	if !reflect.DeepEqual(p.Refused, map[uint64]string{2: "kind_unknown"}) || p.Pulled != 3 || p.Waiting != 2 {
		t.Fatalf("after the page: refused %v, pulled %d, %d pongs wait", p.Refused, p.Pulled, p.Waiting)
	}

	// Posted, it is refused the same and fails nothing; it is not
	// remembered, so it is taken once this yard's yardr knows the kind.
	for range 2 {
		got, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{{ID: "m4", Kind: "handoff", Body: json.RawMessage(`{}`)}})
		if err != nil || len(got) != 1 || got[0].Refused != "kind_unknown" || got[0].Duplicate {
			t.Fatalf("a posted handoff = %+v, %v", got, err)
		}
	}
	// No peer allow lets it in: the kind is none this yardr can allow.
	if _, err := y.api.AllowPeer(y.ctx, "north", store.PeerAllow{Receive: []string{"handoff"}}, "jo"); err == nil {
		t.Fatal("a kind this yardr does not know was allowed")
	}
	// A kind it knows and the link does not carry is still said as that.
	if got, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{{ID: "m5", Kind: "mail", Body: json.RawMessage(`{}`)}}); err != nil || got[0].Refused != "kind_not_allowed" {
		t.Fatalf("mail over a link that carries none = %+v, %v", got, err)
	}
	noBodies(t, y.api)
}

// The sender of a message the peer's yardr does not know gives it up with
// that reason in its note, and not as a kind the peer's link does not carry.
func TestPeerUnknownKindToldToTheSender(t *testing.T) {
	y := newPeerYard(t, "") // north asks
	y.allow(t, store.PeerAllow{Send: []string{"ping"}})
	m, err := y.api.PeerSend(y.ctx, "north", "ping", nil, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.own.PeerHandOver(y.ctx, "north", y.key, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := y.own.PeerHandOver(y.ctx, "north", y.key, m.Seq, map[uint64]string{m.Seq: "kind_unknown"}); err != nil {
		t.Fatal(err)
	}
	failed := peerEvents(t, y.api, "peer_message_failed")
	if len(failed) != 1 || failed[0].Data["reason"] != "refused by the peer: north's yardr does not know ping" || failed[0].Data["id"] != m.ID {
		t.Fatalf("peer_message_failed = %+v", failed)
	}
	if len(peerEvents(t, y.api, "peer_message_sent")) != 0 {
		t.Fatal("a refused message was logged as sent")
	}
	for kind, want := range map[string]string{
		"kind_unknown":     "north's yardr does not know handoff",
		"kind_not_allowed": "its link does not carry handoff in",
	} {
		if got := store.PeerRefusal("north", "handoff", kind); got != want {
			t.Errorf("PeerRefusal(north, handoff, %s) = %q, want %q", kind, got, want)
		}
	}
}

// What a peer says its build understands is kept on its record with when,
// by the key the request was signed with; said again unchanged it writes
// nothing; a peer that says nothing is one that has not said.
func TestPeerHeard(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	said := func() *store.PeerSaid {
		t.Helper()
		p, err := y.api.GetPeer(y.ctx, "north")
		if err != nil {
			t.Fatal(err)
		}
		return p.Said
	}
	if got := said(); got != nil {
		t.Fatalf("a new peer has said %+v", got)
	}
	if got := store.Says("v1.2.3"); !reflect.DeepEqual(got.Understands, store.PeerKinds) || got.Yardr != "v1.2.3" {
		t.Fatalf("Says = %+v", got)
	}
	if err := y.own.PeerHeard(y.ctx, "north", y.key, store.Says("v1.2.3")); err != nil {
		t.Fatal(err)
	}
	first := said()
	if first == nil || !reflect.DeepEqual(first.Understands, []string{"bead", "file", "mail", "ping"}) || first.Yardr != "v1.2.3" || first.At.IsZero() {
		t.Fatalf("after it said: %+v", first)
	}
	if !first.Knows("bead") || first.Knows("handoff") || (*store.PeerSaid)(nil).Knows("ping") {
		t.Fatal("Knows does not go by what was said")
	}
	quiet(t, y.own, "the same said again", func() {
		if err := y.own.PeerHeard(y.ctx, "north", y.key, store.Says("v1.2.3")); err != nil {
			t.Fatal(err)
		}
	})
	// Upgraded: it understands another kind, on another build.
	if err := y.own.PeerHeard(y.ctx, "north", y.key, &store.PeerSays{Understands: []string{"ping", "handoff", "mail", "bead"}, Yardr: "v1.3.0"}); err != nil {
		t.Fatal(err)
	}
	if got := said(); got == nil || !reflect.DeepEqual(got.Understands, []string{"bead", "handoff", "mail", "ping"}) || got.Yardr != "v1.3.0" {
		t.Fatalf("after its upgrade: %+v", got)
	}
	// What it says is its text: kinds and a build of plain characters only.
	dirty := &store.PeerSays{Understands: []string{"Ping it", "mail", "mail", "../etc", strings.Repeat("x", 40)}, Yardr: "v1 (rm -rf)"}
	if err := y.own.PeerHeard(y.ctx, "north", y.key, dirty); err != nil {
		t.Fatal(err)
	}
	if got := said(); got == nil || !reflect.DeepEqual(got.Understands, []string{"mail"}) || got.Yardr != "" {
		t.Fatalf("what is kept of %+v: %+v", dirty, got)
	}
	// It understands nothing, and says so: that is said.
	if err := y.own.PeerHeard(y.ctx, "north", y.key, &store.PeerSays{Understands: []string{}}); err != nil {
		t.Fatal(err)
	}
	if got := said(); got == nil || len(got.Understands) != 0 {
		t.Fatalf("after it said it understands nothing: %+v", got)
	}
	// The name is the peer's only while the key is.
	if err := y.own.PeerHeard(y.ctx, "north", "another-key", store.Says("v9")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("PeerHeard under another key = %v", err)
	}
	// A rename keeps it: the record is the same yard's.
	if _, err := y.api.RenamePeer(y.ctx, "north", "nord", "jo"); err != nil {
		t.Fatal(err)
	}
	if p, err := y.api.GetPeer(y.ctx, "nord"); err != nil || p.Said == nil {
		t.Fatalf("after the rename: %+v, %v", p, err)
	}
	// Downgraded, or a request of an older yardr played: it says nothing.
	if err := y.own.PeerHeard(y.ctx, "nord", y.key, nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "nord"); p.Said != nil {
		t.Fatalf("after it said nothing: %+v", p.Said)
	}
	quiet(t, y.own, "nothing said again", func() {
		if err := y.own.PeerHeard(y.ctx, "nord", y.key, &store.PeerSays{}); err != nil {
			t.Fatal(err)
		}
	})
}

// A signed answer to a post is checked against the peer record before its
// says is kept; an answer forged with another key cannot update it.
func TestPeerPostAnswered(t *testing.T) {
	y := newPeerYard(t, "")
	page, err := json.Marshal(struct {
		Taken bool            `json:"taken"`
		Says  *store.PeerSays `json:"says"`
	}{true, store.Says("v1.2.3")})
	if err != nil {
		t.Fatal(err)
	}
	answer := store.PeerAnswer{Status: 200, RequestSig: "post-1", Signature: yardkey.SignResponse(y.north, 200, "post-1", page), Body: page}
	if err := y.api.PeerPostAnswered(y.ctx, "north", answer); err != nil {
		t.Fatal(err)
	}
	p, err := y.api.GetPeer(y.ctx, "north")
	if err != nil || p.Said == nil || p.Said.Yardr != "v1.2.3" {
		t.Fatalf("after signed post answer: %+v, %v", p, err)
	}

	forged := answer
	forged.Body = []byte(`{"says":{"understands":["handoff"],"yardr":"v9"}}`)
	if err := y.api.PeerPostAnswered(y.ctx, "north", forged); err == nil {
		t.Fatal("an answer signed over other words was accepted")
	}
	p, err = y.api.GetPeer(y.ctx, "north")
	if err != nil || p.Said.Yardr != "v1.2.3" {
		t.Fatalf("a forged answer changed what was kept: %+v, %v", p.Said, err)
	}
}

// The answer to a pull says what the peer's build understands, under the
// peer's signature: it is kept though the page hands over nothing, an
// answer that says the same writes nothing, and the page of an older yardr,
// which says nothing, is taken as before.
func TestPeerPulledSays(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	y.allow(t, store.PeerAllow{Receive: []string{"ping"}})
	pulled := func(page string) *store.PeerPulled {
		t.Helper()
		res, err := y.api.PeerPulled(y.ctx, "north", store.PeerBatch{RequestSig: "req-1", Signature: yardkey.SignResponse(y.north, 200, "req-1", []byte(page)), Body: []byte(page)})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	says := `{"messages":[],"says":{"understands":["bead","mail","ping"],"yardr":"v1.2.3"}}`
	pulled(says)
	p, _ := y.api.GetPeer(y.ctx, "north")
	if p.Said == nil || !reflect.DeepEqual(p.Said.Understands, []string{"bead", "mail", "ping"}) || p.Said.Yardr != "v1.2.3" {
		t.Fatalf("after a page that says: %+v", p.Said)
	}
	quiet(t, y.own, "an empty page that says the same", func() { pulled(says) })
	// Changed, the answer is no longer north's.
	forged := strings.Replace(says, "v1.2.3", "v6.6.6", 1)
	if _, err := y.api.PeerPulled(y.ctx, "north", store.PeerBatch{RequestSig: "req-1", Signature: yardkey.SignResponse(y.north, 200, "req-1", []byte(says)), Body: []byte(forged)}); err == nil {
		t.Fatal("a changed answer was taken")
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Said.Yardr != "v1.2.3" {
		t.Fatalf("a changed answer changed what north said: %+v", p.Said)
	}
	// The page a yardr from before this answers with: the message is taken.
	res := pulled(`{"messages":[{"seq":1,"id":"m1","kind":"ping","body":{}}]}`)
	if len(res.Taken) != 1 || res.Taken[0].Refused != "" {
		t.Fatalf("an older yardr's page = %+v", res)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Said != nil || p.Pulled != 1 {
		t.Fatalf("after an older yardr's page: said %+v, pulled %d", p.Said, p.Pulled)
	}
	quiet(t, y.own, "an older yardr's empty page", func() { pulled(`{"messages":[]}`) })
}
