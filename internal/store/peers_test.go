package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
)

// peerYard is a yard with one peer, north, whose private key the test
// holds, so it can sign as north does.
type peerYard struct {
	api   store.API
	own   *store.Store
	ctx   context.Context
	north []byte // north's private key
	key   string // north's public key
}

func newPeerYard(t *testing.T, url string) *peerYard {
	t.Helper()
	api, own := storetest.NewOwned(t)
	return newPeerYardOn(t, url, api, own)
}

// newPeerYardOn is newPeerYard over a store the test opened itself, for one
// that closes it and opens the file again.
func newPeerYardOn(t *testing.T, url string, api store.API, own *store.Store) *peerYard {
	t.Helper()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	y := &peerYard{api: api, own: own, ctx: context.Background(), north: k, key: store.Public(k)}
	if _, err := api.AddPeer(y.ctx, store.Peer{Name: "north", Key: y.key, URL: url, Calls: "the-north"}, "jo"); err != nil {
		t.Fatal(err)
	}
	return y
}

func (y *peerYard) allow(t *testing.T, ch store.PeerAllow) *store.Peer {
	t.Helper()
	p, err := y.api.AllowPeer(y.ctx, "north", ch, "jo")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// peerEvents are the yard's events of kind, oldest first.
func peerEvents(t *testing.T, api store.API, kind string) []store.Event {
	t.Helper()
	evs, err := api.Events(context.Background(), "", 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func ping(id string) store.PeerMessage {
	return store.PeerMessage{ID: id, Kind: store.PeerPing, Body: json.RawMessage(`{"secret":"hunter2"}`)}
}

// A peer is added under a name of this yard's choosing with nothing
// allowed, a name or a key that is a peer already is refused, and removing
// it drops what waited for it.
func TestPeerAddListRemove(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://10.0.0.5:8790/")
	p, err := y.api.GetPeer(y.ctx, "north")
	if err != nil || p.Key != y.key || p.URL != "http://10.0.0.5:8790" || p.Calls != "the-north" ||
		len(p.Send) != 0 || len(p.Receive) != 0 || p.Send == nil || p.Receive == nil {
		t.Fatalf("peer = %+v, %v", p, err)
	}
	added := peerEvents(t, y.api, "peer_added")
	if len(added) != 1 || added[0].Actor != "jo" || added[0].Data["peer"] != "north" || added[0].Data["key"] != y.key {
		t.Fatalf("peer_added = %+v", added)
	}

	other, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	for name, peer := range map[string]store.Peer{
		"is added already":      {Name: "north", Key: store.Public(other)},
		"is peer north's":       {Name: "south", Key: y.key},
		"no yard's public key":  {Name: "south", Key: "nope"},
		"use lowercase letters": {Name: "South Yard", Key: store.Public(other)},
		"want http://host:port": {Name: "south", Key: store.Public(other), URL: "ftp://x"},
	} {
		if _, err := y.api.AddPeer(y.ctx, peer, "jo"); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("AddPeer(%+v) = %v, want %q", peer, err, name)
		}
	}
	// What an invite may claim of what the link carries is not taken.
	if p, err := y.api.AddPeer(y.ctx, store.Peer{Name: "south", Key: store.Public(other), Send: []string{"ping"}, Receive: []string{"ping"}, Pulled: 9}, "jo"); err != nil || len(p.Send)+len(p.Receive) != 0 || p.Pulled != 0 {
		t.Fatalf("a peer added with kinds = %+v, %v", p, err)
	}
	if ps, err := y.api.Peers(y.ctx); err != nil || len(ps) != 2 || ps[0].Name != "north" || ps[1].Name != "south" {
		t.Fatalf("Peers = %+v, %v", ps, err)
	}

	y.allow(t, store.PeerAllow{Send: []string{"ping"}})
	for i := 0; i < 2; i++ {
		if _, err := y.api.PeerSend(y.ctx, "north", "ping", nil, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if err := y.api.DeletePeer(y.ctx, "north", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.GetPeer(y.ctx, "north"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetPeer after rm = %v", err)
	}
	if err := y.api.DeletePeer(y.ctx, "north", "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second rm = %v", err)
	}
	removed := peerEvents(t, y.api, "peer_removed")
	if len(removed) != 1 || removed[0].Data["peer"] != "north" || removed[0].Data["messages_dropped"] != float64(2) && removed[0].Data["messages_dropped"] != 2 {
		t.Fatalf("peer_removed = %+v", removed)
	}
	// Added again, it is a new link: nothing waits, nothing is allowed.
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "north", Key: y.key}, "jo"); err != nil {
		t.Fatal(err)
	}
	if p, err := y.api.GetPeer(y.ctx, "north"); err != nil || p.Waiting != 0 || len(p.Send) != 0 {
		t.Fatalf("north added again = %+v, %v", p, err)
	}
}

// Nothing is allowed by default; allow adds kinds each direction on its
// own, the no- forms take them away, and a kind there is not is refused.
func TestPeerAllow(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "")
	if p := y.allow(t, store.PeerAllow{Send: []string{"ping"}}); !reflect.DeepEqual(p.Send, []string{"ping"}) || len(p.Receive) != 0 {
		t.Fatalf("after --send ping: %+v", p)
	}
	if p := y.allow(t, store.PeerAllow{Receive: []string{"ping", "ping"}}); !reflect.DeepEqual(p.Send, []string{"ping"}) || !reflect.DeepEqual(p.Receive, []string{"ping"}) {
		t.Fatalf("after --receive ping: %+v", p)
	}
	if p := y.allow(t, store.PeerAllow{NoSend: []string{"ping"}}); len(p.Send) != 0 || !reflect.DeepEqual(p.Receive, []string{"ping"}) {
		t.Fatalf("after --no-send ping: %+v", p)
	}
	for _, ch := range []store.PeerAllow{{Send: []string{"memories"}}, {Receive: []string{"beads"}}, {NoSend: []string{"x"}}} {
		if _, err := y.api.AllowPeer(y.ctx, "north", ch, "jo"); err == nil || !strings.Contains(err.Error(), "no kind of message between yards") {
			t.Errorf("AllowPeer(%+v) = %v", ch, err)
		}
	}
	if _, err := y.api.AllowPeer(y.ctx, "nobody", store.PeerAllow{Send: []string{"ping"}}, "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("allow of no peer = %v", err)
	}
	evs := peerEvents(t, y.api, "peer_allowed")
	if len(evs) != 3 || evs[2].Actor != "jo" || evs[2].Data["peer"] != "north" ||
		len(evs[2].Data["send"].([]any)) != 0 || len(evs[2].Data["receive"].([]any)) != 1 {
		t.Fatalf("peer_allowed = %+v", evs)
	}
}

// A message of a kind the link does not carry out is refused before it is
// queued, and logged. One it carries waits in the outbox under an id minted
// here, in order, until it is delivered or given up.
func TestPeerOutbox(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://north.test")
	if _, err := y.api.PeerSend(y.ctx, "north", "ping", nil, "jo"); err == nil || !strings.Contains(err.Error(), "does not carry ping out") ||
		!strings.Contains(err.Error(), "yardr peer allow north --send ping") {
		t.Fatalf("PeerSend with nothing allowed = %v", err)
	}
	refused := peerEvents(t, y.api, "peer_message_refused")
	if len(refused) != 1 || refused[0].Data["direction"] != "out" || refused[0].Data["kind"] != "ping" || refused[0].Data["peer"] != "north" || refused[0].Data["id"] == "" {
		t.Fatalf("peer_message_refused = %+v", refused)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Waiting != 0 {
		t.Fatalf("a refused message waits: %+v", p)
	}
	if _, err := y.api.PeerSend(y.ctx, "nobody", "ping", nil, "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("PeerSend to no peer = %v", err)
	}

	y.allow(t, store.PeerAllow{Send: []string{"ping"}})
	// Over the socket the client cannot even write it; either way it is
	// not queued.
	if _, err := y.api.PeerSend(y.ctx, "north", "ping", json.RawMessage(`{not json`), "jo"); err == nil {
		t.Fatalf("a body that is not JSON = %v", err)
	}
	big, _ := json.Marshal(strings.Repeat("x", store.MaxPeerBody))
	if _, err := y.api.PeerSend(y.ctx, "north", "ping", big, "jo"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("a body over the limit = %v", err)
	}
	var ids []string
	for i := 0; i < 3; i++ {
		m, err := y.api.PeerSend(y.ctx, "north", "ping", json.RawMessage(`{"n":1}`), "jo")
		if err != nil || m.ID == "" || m.Peer != "north" || m.Kind != "ping" {
			t.Fatalf("PeerSend = %+v, %v", m, err)
		}
		ids = append(ids, m.ID)
	}
	out, err := y.api.PeerOutbox(y.ctx, "north", 0)
	if err != nil || len(out) != 3 || out[0].ID != ids[0] || out[2].ID != ids[2] || !(out[0].Seq < out[1].Seq && out[1].Seq < out[2].Seq) ||
		string(out[0].Body) != `{"n":1}` {
		t.Fatalf("outbox = %+v, %v", out, err)
	}
	if two, _ := y.api.PeerOutbox(y.ctx, "north", 2); len(two) != 2 || two[1].ID != ids[1] {
		t.Fatalf("outbox of 2 = %+v", two)
	}

	// The first fails twice and is then given up; the second is delivered.
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	if err := y.api.PeerSendFailed(y.ctx, "north", out[0].Seq, "connection refused", at, false); err != nil {
		t.Fatal(err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Waiting != 3 || p.Failed != 0 || p.Error != "connection refused" {
		t.Fatalf("after one failed try: %+v", p)
	}
	if got, _ := y.api.PeerOutbox(y.ctx, "north", 1); got[0].Attempts != 1 || got[0].RetryAt == nil || !got[0].RetryAt.Equal(at) {
		t.Fatalf("the message after one failed try: %+v", got[0])
	}
	if len(peerEvents(t, y.api, "peer_message_failed")) != 0 {
		t.Fatal("a try that failed was logged as the message failing")
	}
	if err := y.api.PeerSendFailed(y.ctx, "north", out[0].Seq, "connection refused", at, true); err != nil {
		t.Fatal(err)
	}
	failed := peerEvents(t, y.api, "peer_message_failed")
	if len(failed) != 1 || failed[0].Data["id"] != ids[0] || failed[0].Data["kind"] != "ping" || failed[0].Data["peer"] != "north" ||
		failed[0].Data["reason"] != "connection refused" || failed[0].Data["attempts"] != float64(2) && failed[0].Data["attempts"] != 2 {
		t.Fatalf("peer_message_failed = %+v", failed)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Waiting != 2 || p.Failed != 1 {
		t.Fatalf("after giving one up: %+v", p)
	}
	rest, _ := y.api.PeerOutbox(y.ctx, "north", 0)
	if len(rest) != 2 || rest[0].ID != ids[1] {
		t.Fatalf("the outbox behind a message given up = %+v", rest)
	}
	if err := y.api.PeerDelivered(y.ctx, "north", out[0].Seq); err == nil {
		t.Fatal("a message given up was ended as delivered")
	}
	if err := y.api.PeerDelivered(y.ctx, "north", rest[0].Seq); err != nil {
		t.Fatal(err)
	}
	if err := y.api.PeerDelivered(y.ctx, "north", rest[0].Seq); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delivered twice = %v", err)
	}
	sent := peerEvents(t, y.api, "peer_message_sent")
	if len(sent) != 1 || sent[0].Data["id"] != ids[1] || sent[0].Data["way"] != "push" || sent[0].Data["peer"] != "north" || sent[0].Data["kind"] != "ping" {
		t.Fatalf("peer_message_sent = %+v", sent)
	}
	noBodies(t, y.api)
}

// quiet fails when fn wakes the store's wakers: it wrote.
func quiet(t *testing.T, own *store.Store, what string, fn func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake, err := own.Wake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Whatever an earlier write left pending is not fn's.
	time.Sleep(50 * time.Millisecond)
	for drained := false; !drained; {
		select {
		case <-wake:
		default:
			drained = true
		}
	}
	fn()
	select {
	case <-wake:
		t.Errorf("%s wrote to the store", what)
	case <-time.After(100 * time.Millisecond):
	}
}

// noBodies fails when any peer event carries anything of a message's body.
func noBodies(t *testing.T, api store.API) {
	t.Helper()
	evs, err := api.Events(context.Background(), "", 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		raw, _ := json.Marshal(e)
		if strings.HasPrefix(e.Kind, "peer_") && (e.Data["body"] != nil || strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), `"n":1`)) {
			t.Errorf("event %s carries the body: %s", e.Kind, raw)
		}
	}
}

// What a peer posts is taken once by its id; a kind the link does not
// carry in is refused and logged; a ping is answered with a pong in the
// outbox, and a pong with nothing. Every message counts against the peer's
// hour, whatever became of it.
func TestPeerReceive(t *testing.T) {
	// Sequential: the package's fake clock.
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	store.SetClockForTest(func() time.Time { return at })
	t.Cleanup(func() { store.SetClockForTest(time.Now) })
	y := newPeerYard(t, "http://north.test")
	receive := func(ms ...store.PeerMessage) []store.PeerTaken {
		t.Helper()
		got, err := y.own.PeerReceive(y.ctx, "north", y.key, ms)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// Nothing is allowed: refused, logged, not remembered as taken.
	if got := receive(ping("m1")); len(got) != 1 || got[0].Refused != "kind_not_allowed" || got[0].Duplicate {
		t.Fatalf("a ping with nothing allowed = %+v", got)
	}
	refused := peerEvents(t, y.api, "peer_message_refused")
	if len(refused) != 1 || refused[0].Actor != "peer:north" || refused[0].Data["direction"] != "in" || refused[0].Data["id"] != "m1" ||
		refused[0].Data["kind"] != "ping" || refused[0].Data["peer"] != "north" || refused[0].Data["way"] != "push" {
		t.Fatalf("peer_message_refused = %+v", refused)
	}
	if len(peerEvents(t, y.api, "peer_message_received")) != 0 {
		t.Fatal("a refused message was logged as received")
	}

	// Taken in, but not answered: this yard may not send ping.
	y.allow(t, store.PeerAllow{Receive: []string{"ping"}})
	if got := receive(ping("m1")); got[0].Refused != "" || got[0].Duplicate {
		t.Fatalf("a ping once allowed = %+v", got)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Waiting != 0 {
		t.Fatalf("a pong waits though ping may not be sent: %+v", p)
	}
	if out := peerEvents(t, y.api, "peer_message_refused"); len(out) != 2 || out[1].Data["direction"] != "out" || out[1].Data["re"] != "m1" {
		t.Fatalf("the pong that may not be sent = %+v", out)
	}

	// The same id twice is taken once.
	y.allow(t, store.PeerAllow{Send: []string{"ping"}})
	if got := receive(ping("m1")); !got[0].Duplicate || got[0].Refused != "" {
		t.Fatalf("m1 again = %+v", got)
	}
	if got := receive(ping("m2"), ping("m2")); got[0].Duplicate || !got[1].Duplicate {
		t.Fatalf("m2 twice in one go = %+v", got)
	}
	received := peerEvents(t, y.api, "peer_message_received")
	if len(received) != 2 || received[0].Data["id"] != "m1" || received[1].Data["id"] != "m2" || received[1].Actor != "peer:north" ||
		received[1].Data["way"] != "push" || received[1].Data["kind"] != "ping" {
		t.Fatalf("peer_message_received = %+v", received)
	}
	// m2 is answered: one pong, of kind ping, that names it.
	out, _ := y.api.PeerOutbox(y.ctx, "north", 0)
	if len(out) != 1 || out[0].Kind != "ping" || out[0].Re != "m2" || out[0].ID == "m2" || out[0].ID == "" {
		t.Fatalf("the pong = %+v", out)
	}
	// A pong is taken and not answered.
	pong := ping("m3")
	pong.Re = out[0].ID
	if got := receive(pong); got[0].Duplicate || got[0].Refused != "" {
		t.Fatalf("a pong = %+v", got)
	}
	if after, _ := y.api.PeerOutbox(y.ctx, "north", 0); len(after) != 1 {
		t.Fatalf("a pong was answered: %+v", after)
	}
	if ev := peerEvents(t, y.api, "peer_message_received"); ev[2].Data["re"] != out[0].ID {
		t.Fatalf("the pong's event = %+v", ev[2])
	}

	// What is no message is refused whole, whoever signed it.
	for name, m := range map[string]store.PeerMessage{
		"id":       {ID: "../../etc", Kind: "ping", Body: json.RawMessage(`{}`)},
		"no id":    {Kind: "ping", Body: json.RawMessage(`{}`)},
		"kind":     {ID: "m9", Kind: "Ping it", Body: json.RawMessage(`{}`)},
		"re":       {ID: "m9", Kind: "ping", Re: "a b", Body: json.RawMessage(`{}`)},
		"not JSON": {ID: "m9", Kind: "ping", Body: json.RawMessage(`{`)},
		"no body":  {ID: "m9", Kind: "ping"},
		"exceeds":  {ID: "m9", Kind: "ping", Body: json.RawMessage(`"` + strings.Repeat("x", store.MaxPeerBody) + `"`)},
	} {
		if _, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{m}); err == nil {
			t.Errorf("a message with a bad %s was taken", name)
		}
	}
	if _, err := y.own.PeerReceive(y.ctx, "nobody", y.key, []store.PeerMessage{ping("m1")}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("PeerReceive from no peer = %v", err)
	}
	if p, err := y.own.PeerByKey(y.ctx, y.key); err != nil || p.Name != "north" {
		t.Fatalf("PeerByKey = %+v, %v", p, err)
	}
	if _, err := y.own.PeerByKey(y.ctx, "someone-else"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("PeerByKey of no peer = %v", err)
	}
	// The refused ping, the taken ones and the duplicates each count
	// against the hour; the one pong queued counts with them. What was no
	// message counted nothing. None of it is the peer's mail.
	if p, err := y.api.GetPeer(y.ctx, "north"); err != nil || p.Messages != 6 || p.Pongs != 1 ||
		p.Mails != 0 || p.Files != 0 || p.Events != 0 || p.Arrivals != 0 || p.MailSince == nil {
		t.Fatalf("the peer's counts = %+v, %v", p, err)
	}
	// Thirty pongs may wait for one peer in an hour. This batch fills the
	// remaining places; later pings count but are not answered.
	capPings := make([]store.PeerMessage, store.PeerPongLimit+1)
	for i := range capPings {
		capPings[i] = ping(fmt.Sprintf("cap-%d", i))
	}
	if got := receive(capPings...); len(got) != len(capPings) {
		t.Fatalf("pings at the cap = %+v", got)
	} else {
		for i, taken := range got {
			if taken.Refused != "" || taken.Duplicate {
				t.Fatalf("ping at the cap %d = %+v", i, taken)
			}
		}
	}
	if out, _ := y.api.PeerOutbox(y.ctx, "north", 0); len(out) != store.PeerPongLimit {
		t.Fatalf("pongs within the hour = %d, want %d", len(out), store.PeerPongLimit)
	}
	if p, err := y.api.GetPeer(y.ctx, "north"); err != nil || p.Messages != 6+store.PeerPongLimit+1 || p.Pongs != store.PeerPongLimit {
		t.Fatalf("the peer's counts at the cap = %+v, %v", p, err)
	}

	// Both counts start again once the hour has passed.
	at = at.Add(store.PeerMailWindow + time.Second)
	if got := receive(ping("after-hour")); len(got) != 1 || got[0].Refused != "" || got[0].Duplicate {
		t.Fatalf("a ping once the hour has passed = %+v", got)
	}
	if p, err := y.api.GetPeer(y.ctx, "north"); err != nil || p.Messages != 1 || p.Pongs != 1 {
		t.Fatalf("the peer's counts in the new hour = %+v, %v", p, err)
	}
	noBodies(t, y.api)
}

// A peer that is never called asks: it is handed what waits after the
// sequence number it names, and what it says it has is ended as sent by
// pull. A peer that is called gets only the word that it is posted to.
func TestPeerHandOver(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "")
	y.allow(t, store.PeerAllow{Send: []string{"ping"}})
	var ids []string
	for i := 0; i < 3; i++ {
		m, err := y.api.PeerSend(y.ctx, "north", "ping", nil, "jo")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	page, err := y.own.PeerHandOver(y.ctx, "north", y.key, 0, nil)
	if err != nil || page.Push || len(page.Messages) != 3 || page.Messages[0].ID != ids[0] || page.Messages[0].Seq == 0 {
		t.Fatalf("the first page = %+v, %v", page, err)
	}
	if len(peerEvents(t, y.api, "peer_message_sent")) != 0 {
		t.Fatal("a message handed over was logged as sent before the peer said it has it")
	}
	// Asked the same again (its answer was lost): the same again, and
	// nothing written: a peer asks every few seconds.
	quiet(t, y.own, "a question that ends nothing", func() {
		if again, _ := y.own.PeerHandOver(y.ctx, "north", y.key, 0, nil); len(again.Messages) != 3 {
			t.Fatalf("asked again = %+v", again)
		}
	})
	// It has the first two.
	page, err = y.own.PeerHandOver(y.ctx, "north", y.key, page.Messages[1].Seq, nil)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != ids[2] {
		t.Fatalf("after the first two = %+v, %v", page, err)
	}
	sent := peerEvents(t, y.api, "peer_message_sent")
	if len(sent) != 2 || sent[0].Data["id"] != ids[0] || sent[1].Data["id"] != ids[1] || sent[0].Data["way"] != "pull" {
		t.Fatalf("peer_message_sent = %+v", sent)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Waiting != 1 {
		t.Fatalf("waiting = %+v", p)
	}
	if _, err := y.own.PeerHandOver(y.ctx, "nobody", y.key, 0, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("PeerHandOver to no peer = %v", err)
	}

	called := newPeerYard(t, "http://north.test")
	called.allow(t, store.PeerAllow{Send: []string{"ping"}})
	if _, err := called.api.PeerSend(called.ctx, "north", "ping", nil, "jo"); err != nil {
		t.Fatal(err)
	}
	quiet(t, called.own, "the question of a peer that is called", func() {
		page, err = called.own.PeerHandOver(called.ctx, "north", called.key, 99, nil)
	})
	if err != nil || !page.Push || len(page.Messages) != 0 {
		t.Fatalf("a page for a peer that is called = %+v, %v", page, err)
	}
	if p, _ := called.api.GetPeer(called.ctx, "north"); p.Waiting != 1 {
		t.Fatalf("its question ended a message that is to be posted: %+v", p)
	}
}

// A message that waited while its word was switched off is dropped at
// delivery, not posted: given up, logged as peer_message_failed with why,
// and never tried again. Allowed anew meanwhile, it is left waiting.
func TestPeerSendDisallowed(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://north.test")
	y.allow(t, store.PeerAllow{Send: []string{"ping"}})
	m, err := y.api.PeerSend(y.ctx, "north", "ping", nil, "jo")
	if err != nil {
		t.Fatal(err)
	}
	// Still carried: left waiting.
	if dropped, err := y.api.PeerSendDisallowed(y.ctx, "north", m.Seq); err != nil || dropped {
		t.Fatalf("PeerSendDisallowed of a carried message = %v, %v", dropped, err)
	}
	y.allow(t, store.PeerAllow{NoSend: []string{"ping"}})
	dropped, err := y.api.PeerSendDisallowed(y.ctx, "north", m.Seq)
	if err != nil || !dropped {
		t.Fatalf("PeerSendDisallowed after --no-send ping = %v, %v", dropped, err)
	}
	failed := peerEvents(t, y.api, "peer_message_failed")
	if len(failed) != 1 || failed[0].Data["peer"] != "north" || failed[0].Data["kind"] != "ping" ||
		failed[0].Data["reason"] != "the link with north stopped carrying ping out while the message waited" {
		t.Fatalf("peer_message_failed = %+v", failed)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Waiting != 0 || p.Failed != 1 {
		t.Fatalf("after the drop: %+v", p)
	}
	// Dropped twice is logged once.
	if dropped, err := y.api.PeerSendDisallowed(y.ctx, "north", m.Seq); err != nil || dropped {
		t.Fatalf("PeerSendDisallowed of a dropped message = %v, %v", dropped, err)
	}
	if len(peerEvents(t, y.api, "peer_message_failed")) != 1 {
		t.Fatalf("the drop was logged again")
	}
	// Allowed anew, a message that still waits is left waiting.
	n, err := y.api.PeerSend(y.ctx, "north", "ping", nil, "jo")
	if err == nil {
		t.Fatalf("a ping was queued over a link that sends none: %+v", n)
	}
	y.allow(t, store.PeerAllow{Send: []string{"ping"}})
	n, err = y.api.PeerSend(y.ctx, "north", "ping", nil, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if dropped, err := y.api.PeerSendDisallowed(y.ctx, "north", n.Seq); err != nil || dropped {
		t.Fatalf("PeerSendDisallowed after --send ping = %v, %v", dropped, err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Waiting != 1 || p.Failed != 1 {
		t.Fatalf("after the re-allow: %+v", p)
	}
}

// A peer that asks is handed nothing the link no longer sends: what waits
// of a switched-off kind is dropped with the question, not handed over,
// and what the link still sends crosses past it.
func TestPeerHandOverDropsDisallowed(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "") // north asks for its messages
	y.member(t, "yardmaster", "north")
	y.allow(t, store.PeerAllow{Send: []string{"ping", "mail"}})
	ping, err := y.api.PeerSend(y.ctx, "north", "ping", nil, "jo")
	if err != nil {
		t.Fatal(err)
	}
	mail, err := y.api.MailPeer(y.ctx, "yardmaster", "north", "hello", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{NoSend: []string{"ping"}})
	page, err := y.own.PeerHandOver(y.ctx, "north", y.key, 0, nil)
	if err != nil || page.Push || len(page.Messages) != 1 || page.Messages[0].ID != mail.ID {
		t.Fatalf("PeerHandOver after --no-send ping = %+v, %v", page, err)
	}
	failed := peerEvents(t, y.api, "peer_message_failed")
	if len(failed) != 1 || failed[0].Data["id"] != ping.ID || failed[0].Data["way"] != "pull" ||
		failed[0].Data["reason"] != "the link with north stopped carrying ping out while the message waited" {
		t.Fatalf("peer_message_failed = %+v", failed)
	}
	// Asked again, the dropped ping is not handed over, and the mail the
	// peer took is ended as sent.
	if _, err := y.own.PeerHandOver(y.ctx, "north", y.key, mail.Seq, nil); err != nil {
		t.Fatal(err)
	}
	if sent := peerEvents(t, y.api, "peer_message_sent"); len(sent) != 1 || sent[0].Data["id"] != mail.ID {
		t.Fatalf("peer_message_sent = %+v", sent)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Waiting != 0 || p.Failed != 1 {
		t.Fatalf("after the question: %+v", p)
	}
}

// What the daemon pulled is taken only when the peer signed it, for the
// request it answers: the store checks, so nothing on the socket can hand
// in a message of its own.
func TestPeerPulled(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://north.test")
	y.allow(t, store.PeerAllow{Receive: []string{"ping"}, Send: []string{"ping"}})
	batch := func(key []byte, page store.PeerPage) store.PeerBatch {
		body, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		return store.PeerBatch{RequestSig: "req-1", Signature: store.SignResponse(key, 200, "req-1", body), Body: body}
	}
	m1, m2 := ping("m1"), ping("m2")
	m1.Seq, m2.Seq = 4, 7
	good := batch(y.north, store.PeerPage{Messages: []store.PeerMessage{m1, m2}})

	other, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	changed := good
	changed.Body = []byte(strings.Replace(string(good.Body), "m1", "mX", 1))
	moved := good
	moved.RequestSig = "req-2"
	unsigned := good
	unsigned.Signature = ""
	for name, b := range map[string]store.PeerBatch{
		"signed with another key": batch(other, store.PeerPage{Messages: []store.PeerMessage{m1}}),
		"a changed body":          changed,
		"another request's":       moved,
		"unsigned":                unsigned,
	} {
		if _, err := y.api.PeerPulled(y.ctx, "north", b); err == nil || !strings.Contains(err.Error(), "not signed by the peer") {
			t.Errorf("%s: PeerPulled = %v", name, err)
		}
	}
	if len(peerEvents(t, y.api, "peer_message_received")) != 0 {
		t.Fatal("a batch that is not the peer's was taken")
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Pulled != 0 || p.Waiting != 0 {
		t.Fatalf("after the refused batches: %+v", p)
	}

	res, err := y.api.PeerPulled(y.ctx, "north", good)
	if err != nil || len(res.Taken) != 2 || res.Taken[0].Duplicate || res.Push {
		t.Fatalf("PeerPulled = %+v, %v", res, err)
	}
	p, _ := y.api.GetPeer(y.ctx, "north")
	if p.Pulled != 7 || p.Waiting != 2 { // two pings, two pongs
		t.Fatalf("after the batch: %+v", p)
	}
	received := peerEvents(t, y.api, "peer_message_received")
	if len(received) != 2 || received[0].Data["way"] != "pull" || received[0].Data["id"] != "m1" {
		t.Fatalf("peer_message_received = %+v", received)
	}
	// The same answer played again: nothing is taken twice, the place is kept.
	res, err = y.api.PeerPulled(y.ctx, "north", good)
	if err != nil || !res.Taken[0].Duplicate || !res.Taken[1].Duplicate {
		t.Fatalf("the batch again = %+v, %v", res, err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Pulled != 7 || p.Waiting != 2 || len(peerEvents(t, y.api, "peer_message_received")) != 2 {
		t.Fatalf("after the batch again: %+v", p)
	}
	// An older page does not move the place back.
	m0 := ping("m0")
	m0.Seq = 2
	if _, err := y.api.PeerPulled(y.ctx, "north", batch(y.north, store.PeerPage{Messages: []store.PeerMessage{m0}})); err != nil {
		t.Fatal(err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Pulled != 7 {
		t.Fatalf("the place moved back: %+v", p)
	}
	// A page that hands over nothing writes nothing: the daemon asks every
	// few seconds, and a write would wake it to ask again at once.
	quiet(t, y.own, "an empty page", func() {
		if res, err := y.api.PeerPulled(y.ctx, "north", batch(y.north, store.PeerPage{Messages: []store.PeerMessage{}, Push: true})); err != nil || !res.Push || len(res.Taken) != 0 {
			t.Fatalf("a page that says it posts = %+v, %v", res, err)
		}
	})
	// A kind not allowed in is refused on this way too, and passed by.
	y.allow(t, store.PeerAllow{NoReceive: []string{"ping"}})
	m8 := ping("m8")
	m8.Seq = 8
	res, err = y.api.PeerPulled(y.ctx, "north", batch(y.north, store.PeerPage{Messages: []store.PeerMessage{m8}}))
	if err != nil || res.Taken[0].Refused != "kind_not_allowed" {
		t.Fatalf("a pulled message of a kind not allowed = %+v, %v", res, err)
	}
	if ref := peerEvents(t, y.api, "peer_message_refused"); len(ref) != 1 || ref[0].Data["way"] != "pull" || ref[0].Data["direction"] != "in" {
		t.Fatalf("peer_message_refused = %+v", ref)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Pulled != 8 {
		t.Fatalf("a refused message is asked for again: %+v", p)
	}
	// What is no message is passed by too, and does not hold up the one
	// behind it: answered with an error it would be handed over for good.
	y.allow(t, store.PeerAllow{Receive: []string{"ping"}})
	bad, m10 := store.PeerMessage{ID: "../../etc/passwd", Kind: "ping", Body: json.RawMessage(`{}`), Seq: 9}, ping("m10")
	m10.Seq = 10
	res, err = y.api.PeerPulled(y.ctx, "north", batch(y.north, store.PeerPage{Messages: []store.PeerMessage{bad, m10}}))
	if err != nil || len(res.Taken) != 2 || res.Taken[0].Refused != "not_a_message" || res.Taken[0].ID != "" || res.Taken[1].ID != "m10" || res.Taken[1].Refused != "" {
		t.Fatalf("a page with something that is no message = %+v, %v", res, err)
	}
	ref := peerEvents(t, y.api, "peer_message_refused")
	if last := ref[len(ref)-1]; last.Data["reason"] != "not_a_message" || last.Data["id"] != nil || last.Data["kind"] != nil {
		t.Fatalf("its event = %+v", last)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Pulled != 10 {
		t.Fatalf("the place after it: %+v", p)
	}
	noBodies(t, y.api)
}
