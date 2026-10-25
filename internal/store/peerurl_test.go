package store_test

import (
	"errors"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// SetPeerURL changes the peer's URL and nothing else of the link, in one
// write with one event; what waits for the peer still waits as it was. A
// URL an add would refuse is refused in the add's words.
func TestSetPeerURL(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "")
	was := y.allow(t, store.PeerAllow{Send: []string{"ping"}, Receive: []string{"ping"}})
	m, err := y.api.PeerSend(y.ctx, "north", "ping", nil, "jo")
	if err != nil {
		t.Fatal(err)
	}

	// What an add refuses, each with the add's reason, and nothing changed.
	for _, bad := range []string{"ftp://x", "north.test:8790", "http://", "http://jo:secret@north.test", "http://north.test/?token=1", "http://north.test, http://x"} {
		_, added := y.api.AddPeer(y.ctx, store.Peer{Name: "other", Key: otherKey(t), URL: bad}, "jo")
		_, err := y.api.SetPeerURL(y.ctx, "north", bad, "jo")
		if added == nil || err == nil || !strings.Contains(err.Error(), "want http://host:port or https://host") ||
			err.Error() != strings.Replace(added.Error(), "peer other:", "peer north:", 1) {
			t.Errorf("SetPeerURL(%q) = %v; an add says %v", bad, err, added)
		}
	}
	// An edge URL is kept whole, base path and all, its trailing slash
	// trimmed as any URL's.
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "edge", Key: otherKey(t), URL: "https://my.yardr.dev/peers/mckean/mac/"}, "jo"); err != nil {
		t.Fatalf("AddPeer with an edge URL = %v", err)
	}
	if edge, err := y.api.GetPeer(y.ctx, "edge"); err != nil || edge.URL != "https://my.yardr.dev/peers/mckean/mac" {
		t.Fatalf("the edge URL as kept = %+v, %v", edge, err)
	}
	if _, err := y.api.SetPeerURL(y.ctx, "nowhere", "http://north.test", "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the URL of no peer = %v", err)
	}
	if _, err := y.api.SetPeerURL(y.ctx, "north", "", "jo"); err == nil || !strings.Contains(err.Error(), "peer north has no URL already") {
		t.Fatalf("taking away a URL it has not = %v", err)
	}
	if got := peerEvents(t, y.api, "peer_url_set"); len(got) != 0 {
		t.Fatalf("a refused change was logged: %+v", got)
	}

	// Kept in the one form an add keeps it in.
	p, err := y.api.SetPeerURL(y.ctx, "north", "http://north.test:8790/", "jo")
	if err != nil || p.URL != "http://north.test:8790" || p.Waiting != 1 {
		t.Fatalf("SetPeerURL = %+v, %v", p, err)
	}
	if _, err := y.api.SetPeerURL(y.ctx, "north", "http://north.test:8790", "jo"); err == nil || !strings.Contains(err.Error(), "is called at http://north.test:8790 already") {
		t.Fatalf("the URL it has = %v", err)
	}
	got, err := y.api.GetPeer(y.ctx, "north")
	if err != nil || got.URL != "http://north.test:8790" || got.Key != was.Key || got.Name != "north" || !got.AddedAt.Equal(was.AddedAt) ||
		strings.Join(got.Send, ",") != "ping" || strings.Join(got.Receive, ",") != "ping" {
		t.Fatalf("the link after its URL was set = %+v, %v; it was %+v", got, err, was)
	}
	if w, err := y.api.PeerOutbox(y.ctx, "north", 0); err != nil || len(w) != 1 || w[0].ID != m.ID || w[0].Seq != m.Seq {
		t.Fatalf("what waits after the URL was set = %+v, %v", w, err)
	}
	// A peer that is called is handed nothing when it asks.
	if page, err := y.own.PeerHandOver(y.ctx, "north", y.key, 0, nil); err != nil || !page.Push || len(page.Messages) != 0 {
		t.Fatalf("handed over to a peer with a URL = %+v, %v", page, err)
	}

	p, err = y.api.SetPeerURL(y.ctx, "north", "https://north.example", "jo")
	if err != nil || p.URL != "https://north.example" {
		t.Fatalf("a second URL = %+v, %v", p, err)
	}
	p, err = y.api.SetPeerURL(y.ctx, "north", "https://my.yardr.dev/peers/mckean/mac", "jo")
	if err != nil || p.URL != "https://my.yardr.dev/peers/mckean/mac" {
		t.Fatalf("an edge URL = %+v, %v", p, err)
	}
	p, err = y.api.SetPeerURL(y.ctx, "north", "", "jo")
	if err != nil || p.URL != "" || p.Waiting != 1 {
		t.Fatalf("the URL taken away = %+v, %v", p, err)
	}
	// Never called, it is handed what waited, under the number it had.
	if page, err := y.own.PeerHandOver(y.ctx, "north", y.key, 0, nil); err != nil || page.Push || len(page.Messages) != 1 || page.Messages[0].ID != m.ID || page.Messages[0].Seq != m.Seq {
		t.Fatalf("handed over once the URL was taken away = %+v, %v", page, err)
	}

	evs := peerEvents(t, y.api, "peer_url_set")
	if len(evs) != 4 {
		t.Fatalf("peer_url_set = %+v", evs)
	}
	for i, want := range []map[string]any{
		{"peer": "north", "url": "http://north.test:8790"},
		{"peer": "north", "url": "https://north.example", "was": "http://north.test:8790"},
		{"peer": "north", "url": "https://my.yardr.dev/peers/mckean/mac", "was": "https://north.example"},
		{"peer": "north", "was": "https://my.yardr.dev/peers/mckean/mac"},
	} {
		if e := evs[i]; e.Actor != "jo" || len(e.Data) != len(want) || e.Data["peer"] != want["peer"] || e.Data["url"] != want["url"] || e.Data["was"] != want["was"] {
			t.Errorf("peer_url_set %d = %s %+v, want %+v", i, e.Actor, e.Data, want)
		}
	}
}
