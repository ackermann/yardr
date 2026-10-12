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
	ls, _, err := y.api.Conversation(y.ctx, me, with, 0, 0, 0)
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
// crew members' grants. Mail from before and after it is one conversation,
// read under the new name, and the old name reaches nothing.
func TestPeerRename(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	y.member(t, "yardmaster", "north,west")
	y.member(t, "clerk", "")
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "west", Key: otherKey(t)}, "jo"); err != nil {
		t.Fatal(err)
	}
	was := y.allow(t, store.PeerAllow{Send: []string{"mail", "ping"}, Receive: []string{"mail"}, Expose: []string{"yardmaster"}})
	before, err := y.api.MailPeer(y.ctx, "clerk", "north", "before the rename", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{mail("m1", "yardmaster", "clerk", "from before")}); err != nil || got[0].Refused != "" {
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
		len(ev[0].Data["crew"].([]any)) != 1 || ev[0].Data["crew"].([]any)[0] != "yardmaster" || p.Was[0].Until != ev[0].Seq {
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
	if got, err := y.own.PeerReceive(y.ctx, "up", y.key, []store.PeerMessage{mail("m1", "yardmaster", "clerk", "from before")}); err != nil || !got[0].Duplicate {
		t.Fatalf("m1 again after the rename = %+v, %v", got, err)
	}

	// The grant followed: the crew member may mail the new name and not the old.
	if r, _ := y.api.GetCrew(y.ctx, "yardmaster"); !slices.Equal(r.MayMail(), []string{"up", "west"}) {
		t.Fatalf("yardmaster may mail %v", r.MayMail())
	}
	if _, err := y.api.MailPeer(y.ctx, "clerk", "north", "to the old name", "yardmaster"); !errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "is up now") {
		t.Fatalf("mail to the old name = %v", err)
	}
	if _, err := y.api.MailPeer(y.ctx, "clerk", "up", "after the rename", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.MailPeer(y.ctx, "clerk", "up", "hello", "clerk"); err == nil || !strings.Contains(err.Error(), "crew member clerk may not mail crew members of up") {
		t.Fatalf("a crew member without the grant = %v", err)
	}

	// The inbox names the sender by the name its yard has now, and the yard
	// told the crew member of it. The log keeps what it said.
	in := y.inbox(t, "yardmaster")
	if len(in) != 2 || in[0].Actor != "clerk@up" || in[0].Data["from_peer"] != "up" || in[0].Data["from_peer_was"] != "north" ||
		in[1].Actor != "yard" || !strings.Contains(in[1].Data["text"].(string), "the linked yard north is called up here now: write to its crew as <name>@up.") {
		t.Fatalf("yardmaster's inbox = %+v", in)
	}
	if len(y.inbox(t, "clerk")) != 0 {
		t.Fatal("a crew member that could not mail the peer was told of its rename")
	}
	logged := false
	for _, e := range peerEvents(t, y.api, "mail") {
		logged = logged || e.Actor == "clerk@north" && e.Data["from_peer"] == "north" && e.Data["from_peer_was"] == nil
	}
	if !logged {
		t.Fatal("the log's mail event does not say what it said")
	}

	// One conversation, before and after, under the new name only.
	want := []string{"yardmaster>clerk@up sent: before the rename", "clerk@up>yardmaster : from before", "yardmaster>clerk@up queued: after the rename"}
	if got := y.conversation(t, "yardmaster", "clerk@up"); !slices.Equal(got, want) {
		t.Fatalf("the conversation = %q, want %q", got, want)
	}
	if ls, _, _ := y.api.Conversation(y.ctx, "yardmaster", "clerk@up", before.Seq+100, 0, 0); len(ls) != 0 {
		t.Fatalf("the conversation after the head = %+v", ls)
	}
	if got := y.conversation(t, "yardmaster", "nobody@up"); len(got) != 0 {
		t.Fatalf("the conversation with another crew member = %q", got)
	}
	if _, _, err := y.api.Conversation(y.ctx, "yardmaster", "clerk@north", 0, 0, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the conversation under the old name = %v", err)
	}

	// A second yard added under the old name is another yard: no grant, no
	// conversation, no ids taken, nothing waiting.
	second := otherKey(t)
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "north", Key: second}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.AllowPeer(y.ctx, "north", store.PeerAllow{Send: []string{"mail"}, Receive: []string{"mail"}, Expose: []string{"yardmaster"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if n, err := y.api.GetPeer(y.ctx, "north"); err != nil || n.Waiting != 0 || len(n.Was) != 0 || n.Mails != 0 {
		t.Fatalf("the yard added under the old name = %+v, %v", n, err)
	}
	if _, err := y.api.MailPeer(y.ctx, "clerk", "north", "hello", "yardmaster"); err == nil || !strings.Contains(err.Error(), "crew member yardmaster may not mail crew members of north") {
		t.Fatalf("mail to the yard added under the old name = %v", err)
	}
	if got := y.conversation(t, "yardmaster", "clerk@north"); len(got) != 0 {
		t.Fatalf("the new yard's conversation = %q", got)
	}
	if got, err := y.own.PeerReceive(y.ctx, "north", second, []store.PeerMessage{mail("m1", "yardmaster", "clerk", "from the other north")}); err != nil || got[0].Duplicate || got[0].Refused != "" {
		t.Fatalf("m1 from the yard added under the old name = %+v, %v", got, err)
	}
	if got := y.conversation(t, "yardmaster", "clerk@north"); !slices.Equal(got, []string{"clerk@north>yardmaster : from the other north"}) {
		t.Fatalf("the new yard's conversation = %q", got)
	}
	if got := y.conversation(t, "yardmaster", "clerk@up"); !slices.Equal(got, want) {
		t.Fatalf("the renamed yard's conversation = %q, want %q", got, want)
	}
	// Each yard's mail under its own name, the old mail still the renamed one's.
	in = y.inbox(t, "yardmaster")
	if len(in) != 3 || in[0].Actor != "clerk@up" || in[2].Actor != "clerk@north" || in[2].Data["from_peer_was"] != nil {
		t.Fatalf("yardmaster's inbox with both yards = %+v", in)
	}

	// Renamed back and forth, the link is still one.
	if _, err := y.api.RenamePeer(y.ctx, "up", "top", "jo"); err != nil {
		t.Fatal(err)
	}
	for i := range want {
		want[i] = strings.ReplaceAll(want[i], "@up", "@top")
	}
	if got := y.conversation(t, "yardmaster", "clerk@top"); !slices.Equal(got, want) {
		t.Fatalf("after a second rename = %q, want %q", got, want)
	}
	if in := y.inbox(t, "yardmaster"); in[0].Actor != "clerk@top" || in[0].Data["from_peer_was"] != "north" {
		t.Fatalf("yardmaster's inbox after a second rename = %+v", in[0])
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

// User inboxes use the peer's current name for old mail too, as crew
// inboxes do. Chat uses that address to match unread mail to its roster row.
func TestPeerRenameUserInbox(t *testing.T) {
	y := newPeerYard(t, "")
	if _, err := y.api.AddUser(y.ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"mail"}, Expose: []string{"ada"}})
	if got, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{mail("m1", "ada", "mckean", "before rename")}); err != nil || got[0].Refused != "" {
		t.Fatalf("mail to ada before rename = %+v, %v", got, err)
	}
	if _, err := y.api.RenamePeer(y.ctx, "north", "up", "jo"); err != nil {
		t.Fatal(err)
	}
	if got, err := y.own.PeerReceive(y.ctx, "up", y.key, []store.PeerMessage{mail("m2", "ada", "mckean", "after rename")}); err != nil || got[0].Refused != "" {
		t.Fatalf("mail to ada after rename = %+v, %v", got, err)
	}
	box, err := y.api.UserInbox(y.ctx, "ada", 0)
	if err != nil || box.Unread != 2 || !slices.Equal(box.From, []string{"mckean@up"}) || len(box.Mail) != 2 || box.Mail[0].From != "mckean@up" || box.Mail[1].From != "mckean@up" {
		t.Fatalf("ada's inbox after north was renamed up = %+v, %v", box, err)
	}
}

// Mail from a yard that was removed names no address in the inbox: its
// name is free, and a yard added under it later is another yard. The row
// says the yard is linked no more and what it was called; mail of the yard
// that has the name now is that yard's, under the name.
func TestMailFromRemovedPeer(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	y.member(t, "yardmaster", "north")
	y.member(t, "clerk", "")
	y.allow(t, store.PeerAllow{Send: []string{"mail"}, Receive: []string{"mail"}, Expose: []string{"yardmaster"}})
	if got, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{mail("m1", "yardmaster", "clerk", "from the first north")}); err != nil || got[0].Refused != "" {
		t.Fatalf("PeerReceive = %+v, %v", got, err)
	}
	if in := y.inbox(t, "yardmaster"); len(in) != 1 || in[0].Actor != "clerk@north" || !store.FromOutside(in[0]) || store.MailSender(in[0]) != "clerk@north" {
		t.Fatalf("yardmaster's inbox while north is linked = %+v", in)
	}
	if err := y.api.DeletePeer(y.ctx, "north", "jo"); err != nil {
		t.Fatal(err)
	}

	gone := func(when string) {
		t.Helper()
		in := y.inbox(t, "yardmaster")
		if len(in) == 0 {
			t.Fatalf("%s: yardmaster's inbox is empty", when)
		}
		e := in[0]
		if e.Actor != "" || e.Data["from_peer"] != "" || e.Data["from_peer_unlinked"] != true || e.Data["from_peer_was"] != "north" || e.Data["from"] != "clerk" {
			t.Fatalf("%s: mail of the removed yard = %+v", when, e)
		}
		if got := store.MailSender(e); got != "clerk at a yard no longer linked (was north)" || !store.FromOutside(e) {
			t.Fatalf("%s: its sender reads %q, from outside %v", when, got, store.FromOutside(e))
		}
	}
	gone("no yard has the name")
	if _, _, err := y.api.Conversation(y.ctx, "yardmaster", "clerk@north", 0, 0, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the conversation with a removed yard = %v", err)
	}

	// Another yard under the name: the old mail is not its own.
	second := otherKey(t)
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "north", Key: second}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"mail"}, Expose: []string{"yardmaster"}})
	gone("another yard has the name")
	if got := y.conversation(t, "yardmaster", "clerk@north"); len(got) != 0 {
		t.Fatalf("the new yard's conversation has the removed yard's mail: %q", got)
	}
	if got, err := y.own.PeerReceive(y.ctx, "north", second, []store.PeerMessage{mail("m1", "yardmaster", "clerk", "from the second north")}); err != nil || got[0].Refused != "" {
		t.Fatalf("PeerReceive from the second north = %+v, %v", got, err)
	}
	gone("the yard that has the name wrote")
	in := y.inbox(t, "yardmaster")
	if len(in) != 2 || in[1].Actor != "clerk@north" || in[1].Data["from_peer"] != "north" || in[1].Data["from_peer_unlinked"] != nil ||
		in[1].Data["from_peer_was"] != nil || store.MailSender(in[1]) != "clerk@north" {
		t.Fatalf("mail of the yard that has the name now = %+v", in)
	}
	if got := y.conversation(t, "yardmaster", "clerk@north"); !slices.Equal(got, []string{"clerk@north>yardmaster : from the second north"}) {
		t.Fatalf("the new yard's conversation = %q", got)
	}

	// The log keeps what it said.
	if evs := peerEvents(t, y.api, "mail"); len(evs) != 2 || evs[0].Actor != "clerk@north" || evs[0].Data["from_peer"] != "north" || evs[0].Data["from_peer_unlinked"] != nil {
		t.Fatalf("the log's mail events = %+v", evs)
	}
}
