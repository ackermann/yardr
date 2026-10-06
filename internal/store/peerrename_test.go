package store_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// otherKey is the public key of one more yard.
func otherKey(t *testing.T) string {
	t.Helper()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	return yardkey.Public(k)
}

// conversation is what yardr mail --with reads back, as "from>to state: text".
func (y *peerYard) conversation(t *testing.T, me, with string) []string {
	t.Helper()
	ls, err := y.api.Conversation(y.ctx, me, with, 0)
	if err != nil {
		t.Fatalf("Conversation(%s, %s): %v", me, with, err)
	}
	out := []string{}
	for _, l := range ls {
		out = append(out, l.From+">"+l.To+" "+l.Delivery+": "+l.Text)
	}
	return out
}

// A rename moves everything the yard keys by the peer's name in one write:
// the link as it was, what waits for it, the ids taken from it and the
// residents' grants. Mail from before and after it is one conversation,
// read under the new name, and the old name reaches nothing.
func TestPeerRename(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	y.resident(t, "mayor", "north,west")
	y.resident(t, "clerk", "")
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "west", Key: otherKey(t)}, "jo"); err != nil {
		t.Fatal(err)
	}
	was := y.allow(t, store.PeerAllow{Send: []string{"mail", "ping"}, Receive: []string{"mail"}})
	before, err := y.api.MailPeer(y.ctx, "clerk", "north", "before the rename", "mayor")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{mail("m1", "mayor", "clerk", "from before")}); err != nil || got[0].Refused != "" {
		t.Fatalf("PeerReceive = %+v, %v", got, err)
	}

	// The three refusals, each with its reason, and nothing changed.
	if _, err := y.api.RenamePeer(y.ctx, "nowhere", "up", "jo"); !errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "peer nowhere") {
		t.Fatalf("renaming no peer = %v", err)
	}
	for to, want := range map[string]string{
		"Up North": `peer "Up North": use lowercase letters`,
		"west":     "peer west is added already",
		"north":    "peer north has that name already",
	} {
		if _, err := y.api.RenamePeer(y.ctx, "north", to, "jo"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("rename to %q = %v, want %q", to, err, want)
		}
	}
	if p, err := y.api.GetPeer(y.ctx, "north"); err != nil || p.Waiting != 1 || len(p.Was) != 0 || len(peerEvents(t, y.api, "peer_renamed")) != 0 {
		t.Fatalf("after the refusals: %+v, %v", p, err)
	}

	p, err := y.api.RenamePeer(y.ctx, "north", "up", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "up" || p.Key != was.Key || p.URL != was.URL || p.Calls != "the-north" || !slices.Equal(p.Send, was.Send) || !slices.Equal(p.Receive, was.Receive) ||
		!p.AddedAt.Equal(was.AddedAt) || p.Mails != 1 || p.Waiting != 1 || len(p.Was) != 1 || p.Was[0].Name != "north" {
		t.Fatalf("the renamed peer = %+v, was %+v", p, was)
	}
	ev := peerEvents(t, y.api, "peer_renamed")
	if len(ev) != 1 || ev[0].Actor != "jo" || ev[0].Data["from"] != "north" || ev[0].Data["to"] != "up" || ev[0].Data["peer"] != "up" ||
		len(ev[0].Data["residents"].([]any)) != 1 || ev[0].Data["residents"].([]any)[0] != "mayor" || p.Was[0].Until != ev[0].Seq {
		t.Fatalf("peer_renamed = %+v, was %+v", ev, p.Was)
	}

	// Nothing is left under the old name, and its error says the new one.
	if _, err := y.api.GetPeer(y.ctx, "north"); !errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "the yard that was north here until ") || !strings.Contains(err.Error(), " is up now") {
		t.Fatalf("the old name = %v", err)
	}
	if _, err := y.api.PeerOutbox(y.ctx, "north", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the old name's outbox = %v", err)
	}
	if ps, _ := y.api.Peers(y.ctx); len(ps) != 2 || ps[0].Name != "up" || ps[1].Name != "west" {
		t.Fatalf("peers = %+v", ps)
	}
	if got, _ := y.own.PeerByKey(y.ctx, y.key); got == nil || got.Name != "up" {
		t.Fatalf("the peer by its key = %+v", got)
	}

	// What waited still waits, under the number it had, and is delivered.
	out, err := y.api.PeerOutbox(y.ctx, "up", 0)
	if err != nil || len(out) != 1 || out[0].ID != before.ID || out[0].Seq != before.Seq || out[0].Peer != "up" {
		t.Fatalf("the outbox after the rename = %+v, %v", out, err)
	}
	if err := y.api.PeerDelivered(y.ctx, "up", before.Seq); err != nil {
		t.Fatal(err)
	}
	if sent := peerEvents(t, y.api, "peer_message_sent"); len(sent) != 1 || sent[0].Data["id"] != before.ID || sent[0].Data["peer"] != "up" {
		t.Fatalf("peer_message_sent = %+v", sent)
	}

	// A message handed over again is not taken twice.
	if got, err := y.own.PeerReceive(y.ctx, "up", y.key, []store.PeerMessage{mail("m1", "mayor", "clerk", "from before")}); err != nil || !got[0].Duplicate {
		t.Fatalf("m1 again after the rename = %+v, %v", got, err)
	}

	// The grant followed: the resident may mail the new name and not the old.
	if r, _ := y.api.GetResident(y.ctx, "mayor"); !slices.Equal(r.MayMail(), []string{"up", "west"}) {
		t.Fatalf("mayor may mail %v", r.MayMail())
	}
	if _, err := y.api.MailPeer(y.ctx, "clerk", "north", "to the old name", "mayor"); !errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "is up now") {
		t.Fatalf("mail to the old name = %v", err)
	}
	if _, err := y.api.MailPeer(y.ctx, "clerk", "up", "after the rename", "mayor"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.MailPeer(y.ctx, "clerk", "up", "hello", "clerk"); err == nil || !strings.Contains(err.Error(), "resident clerk may not mail residents of up") {
		t.Fatalf("a resident without the grant = %v", err)
	}

	// The inbox names the sender by the name its yard has now, and the yard
	// told the resident of it. The log keeps what it said.
	in := y.inbox(t, "mayor")
	if len(in) != 2 || in[0].Actor != "clerk@up" || in[0].Data["from_peer"] != "up" || in[0].Data["from_peer_was"] != "north" ||
		in[1].Actor != "yard" || !strings.Contains(in[1].Data["text"].(string), "the linked yard north is called up here now: write to its residents as <name>@up.") {
		t.Fatalf("mayor's inbox = %+v", in)
	}
	if len(y.inbox(t, "clerk")) != 0 {
		t.Fatal("a resident that could not mail the peer was told of its rename")
	}
	logged := false
	for _, e := range peerEvents(t, y.api, "mail") {
		logged = logged || e.Actor == "clerk@north" && e.Data["from_peer"] == "north" && e.Data["from_peer_was"] == nil
	}
	if !logged {
		t.Fatal("the log's mail event does not say what it said")
	}

	// One conversation, before and after, under the new name only.
	want := []string{"mayor>clerk@up sent: before the rename", "clerk@up>mayor : from before", "mayor>clerk@up queued: after the rename"}
	if got := y.conversation(t, "mayor", "clerk@up"); !slices.Equal(got, want) {
		t.Fatalf("the conversation = %q, want %q", got, want)
	}
	if ls, _ := y.api.Conversation(y.ctx, "mayor", "clerk@up", before.Seq+100); len(ls) != 0 {
		t.Fatalf("the conversation after the head = %+v", ls)
	}
	if got := y.conversation(t, "mayor", "nobody@up"); len(got) != 0 {
		t.Fatalf("the conversation with another resident = %q", got)
	}
	if _, err := y.api.Conversation(y.ctx, "mayor", "clerk@north", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the conversation under the old name = %v", err)
	}

	// A second yard added under the old name is another yard: no grant, no
	// conversation, no ids taken, nothing waiting.
	second := otherKey(t)
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "north", Key: second}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.AllowPeer(y.ctx, "north", store.PeerAllow{Send: []string{"mail"}, Receive: []string{"mail"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if n, err := y.api.GetPeer(y.ctx, "north"); err != nil || n.Waiting != 0 || len(n.Was) != 0 || n.Mails != 0 {
		t.Fatalf("the yard added under the old name = %+v, %v", n, err)
	}
	if _, err := y.api.MailPeer(y.ctx, "clerk", "north", "hello", "mayor"); err == nil || !strings.Contains(err.Error(), "resident mayor may not mail residents of north") {
		t.Fatalf("mail to the yard added under the old name = %v", err)
	}
	if got := y.conversation(t, "mayor", "clerk@north"); len(got) != 0 {
		t.Fatalf("the new yard's conversation = %q", got)
	}
	if got, err := y.own.PeerReceive(y.ctx, "north", second, []store.PeerMessage{mail("m1", "mayor", "clerk", "from the other north")}); err != nil || got[0].Duplicate || got[0].Refused != "" {
		t.Fatalf("m1 from the yard added under the old name = %+v, %v", got, err)
	}
	if got := y.conversation(t, "mayor", "clerk@north"); !slices.Equal(got, []string{"clerk@north>mayor : from the other north"}) {
		t.Fatalf("the new yard's conversation = %q", got)
	}
	if got := y.conversation(t, "mayor", "clerk@up"); !slices.Equal(got, want) {
		t.Fatalf("the renamed yard's conversation = %q, want %q", got, want)
	}
	// Each yard's mail under its own name, the old mail still the renamed one's.
	in = y.inbox(t, "mayor")
	if len(in) != 3 || in[0].Actor != "clerk@up" || in[2].Actor != "clerk@north" || in[2].Data["from_peer_was"] != nil {
		t.Fatalf("mayor's inbox with both yards = %+v", in)
	}

	// Renamed back and forth, the link is still one.
	if _, err := y.api.RenamePeer(y.ctx, "up", "top", "jo"); err != nil {
		t.Fatal(err)
	}
	for i := range want {
		want[i] = strings.ReplaceAll(want[i], "@up", "@top")
	}
	if got := y.conversation(t, "mayor", "clerk@top"); !slices.Equal(got, want) {
		t.Fatalf("after a second rename = %q, want %q", got, want)
	}
	if in := y.inbox(t, "mayor"); in[0].Actor != "clerk@top" || in[0].Data["from_peer_was"] != "north" {
		t.Fatalf("mayor's inbox after a second rename = %+v", in[0])
	}
}

// A peer that asks for its messages gets what waited from before the
// rename, under the numbers it counts by, and a refusal it has not been
// told of is still told.
func TestPeerRenamePull(t *testing.T) {
	y := newPeerYard(t, "")
	y.allow(t, store.PeerAllow{Send: []string{"ping"}})
	m, err := y.api.PeerSend(y.ctx, "north", "ping", nil, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.RenamePeer(y.ctx, "north", "up", "jo"); err != nil {
		t.Fatal(err)
	}
	page, err := y.own.PeerHandOver(y.ctx, "up", y.key, 0, nil)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != m.ID || page.Messages[0].Seq != m.Seq {
		t.Fatalf("handed over after the rename = %+v, %v", page, err)
	}
	if _, err := y.own.PeerHandOver(y.ctx, "north", y.key, 0, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("handing over under the old name = %v", err)
	}
	if page, err := y.own.PeerHandOver(y.ctx, "up", y.key, m.Seq, nil); err != nil || len(page.Messages) != 0 {
		t.Fatalf("the next question = %+v, %v", page, err)
	}
	if sent := peerEvents(t, y.api, "peer_message_sent"); len(sent) != 1 || sent[0].Data["peer"] != "up" || sent[0].Data["way"] != "pull" {
		t.Fatalf("peer_message_sent = %+v", sent)
	}
	if p, _ := y.api.GetPeer(y.ctx, "up"); p.Waiting != 0 {
		t.Fatalf("still waiting: %+v", p)
	}
}
