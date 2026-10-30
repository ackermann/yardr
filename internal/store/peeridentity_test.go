package store_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// identitySource is a declared source called name that hears by identity:
// fields.sender holds a sender's id. plainSource is one that hears every
// delivery and no sender by identity.
func identitySource(name string) string {
	env := strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_TOKEN"
	return "name: " + name + "\nbase: https://" + name + ".example.com\ncredential: { env: " + env + ` }
calls:
  message:
    kind: update
    params: { text: { type: string } }
    request: { method: POST, path: /send, body: { text: "{text}" } }
hook:
  verify: { scheme: secret, signature: { header: Sender-Secret } }
  bodies: unique
  normalize: [normalize]
  keys: [sender, channel]
hear:
  field: sender
  by: identity
`
}

func plainSource(name string) string {
	return strings.Replace(identitySource(name), "hear:\n  field: sender\n  by: identity\n", "", 1)
}

// peerIdentityYard is rosterYard (the peer north, the operator jo, the user ada,
// the crew members yardmaster and clerk) with the sources chat and talk,
// which hear by identity, and plain, which does not.
func peerIdentityYard(t *testing.T) *peerYard {
	t.Helper()
	y, _ := rosterYard(t)
	for name, text := range map[string]string{"chat": identitySource("chat"), "talk": identitySource("talk"), "plain": plainSource("plain")} {
		if _, err := y.api.PutSource(y.ctx, name+".yaml", text, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	return y
}

func (y *peerYard) mapPeer(t *testing.T, peer, source, id, crew string) *store.Peer {
	t.Helper()
	p, err := y.api.SetPeerIdentity(y.ctx, peer, store.PeerIdentity{Source: source, ID: id, Crew: crew}, "jo")
	if err != nil {
		t.Fatalf("mapping %s in %s to %s@%s: %v", id, source, crew, peer, err)
	}
	return p
}

// The operator says that an id in a source is a crew member of the peer's
// yard, and the link keeps it sorted by source, then crew. Giving the crew
// member another id in that source replaces the first, and says which it
// was. Taking it back leaves the rest. Each change is logged.
func TestPeerIdentitySetReplaceRemove(t *testing.T) {
	t.Parallel()
	y := peerIdentityYard(t)
	if p, err := y.api.GetPeer(y.ctx, "north"); err != nil || p.Identities != nil {
		t.Fatalf("a new link maps %+v, %v", p.Identities, err)
	}

	y.mapPeer(t, "north", "talk", "T9", "mayor")
	y.mapPeer(t, "north", "chat", "U8", "clerk")
	p := y.mapPeer(t, "north", "chat", "U7", "mayor")
	want := []store.PeerIdentity{{Source: "chat", ID: "U8", Crew: "clerk"}, {Source: "chat", ID: "U7", Crew: "mayor"}, {Source: "talk", ID: "T9", Crew: "mayor"}}
	if !reflect.DeepEqual(p.Identities, want) {
		t.Fatalf("identities = %+v, want %+v", p.Identities, want)
	}
	if got := p.Identities[1].Speaker("north"); got != "mayor@north" {
		t.Fatalf("the speaker = %q", got)
	}
	// As every reader of the link has it.
	if ps, err := y.api.Peers(y.ctx); err != nil || !reflect.DeepEqual(ps[0].Identities, want) {
		t.Fatalf("Peers: %+v, %v", ps, err)
	}
	if read, err := y.own.PeerIdentities(y.ctx); err != nil || !reflect.DeepEqual(read, map[string][]store.PeerIdentity{"north": want}) {
		t.Fatalf("PeerIdentities = %+v, %v", read, err)
	}
	set := peerEvents(t, y.api, "peer_identity_set")
	if last := set[len(set)-1]; len(set) != 3 || last.Actor != "jo" || last.Data["peer"] != "north" || last.Data["crew"] != "mayor" ||
		last.Data["source"] != "chat" || last.Data["id"] != "U7" || last.Data["was"] != nil {
		t.Fatalf("peer_identity_set = %+v", set)
	}

	// Another id for the same crew member in the same source replaces it.
	p = y.mapPeer(t, "north", "chat", "U6", "mayor")
	want[1].ID = "U6"
	if !reflect.DeepEqual(p.Identities, want) {
		t.Fatalf("after a replace: %+v, want %+v", p.Identities, want)
	}
	set = peerEvents(t, y.api, "peer_identity_set")
	if last := set[len(set)-1]; last.Data["id"] != "U6" || last.Data["was"] != "U7" {
		t.Fatalf("peer_identity_set for a replace = %+v", last)
	}
	// The same again changes nothing a reader sees.
	if p = y.mapPeer(t, "north", "chat", "U6", "mayor"); !reflect.DeepEqual(p.Identities, want) {
		t.Fatalf("after the same again: %+v", p.Identities)
	}

	p, err := y.api.RemovePeerIdentity(y.ctx, "north", "mayor", "chat", "jo")
	want = []store.PeerIdentity{want[0], want[2]}
	if err != nil || !reflect.DeepEqual(p.Identities, want) {
		t.Fatalf("after taking one back: %+v, %v", p, err)
	}
	removed := peerEvents(t, y.api, "peer_identity_removed")
	if len(removed) != 1 || removed[0].Actor != "jo" || removed[0].Data["peer"] != "north" || removed[0].Data["crew"] != "mayor" ||
		removed[0].Data["source"] != "chat" || removed[0].Data["id"] != "U6" {
		t.Fatalf("peer_identity_removed = %+v", removed)
	}
	if _, err := y.api.RemovePeerIdentity(y.ctx, "north", "mayor", "chat", "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("taking back what is not there = %v, want not found", err)
	}
	if _, err := y.api.RemovePeerIdentity(y.ctx, "nowhere", "mayor", "chat", "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("taking back on a peer that is not there = %v, want not found", err)
	}
	if _, err := y.api.RemovePeerIdentity(y.ctx, "north", "clerk", "chat", "jo"); err != nil {
		t.Fatal(err)
	}
	p, err = y.api.RemovePeerIdentity(y.ctx, "north", "mayor", "talk", "jo")
	if err != nil || p.Identities != nil {
		t.Fatalf("with all taken back: %+v, %v", p, err)
	}
	if read, err := y.own.PeerIdentities(y.ctx); err != nil || len(read) != 0 {
		t.Fatalf("PeerIdentities with none = %+v, %v", read, err)
	}
}

// A sender mapped to a crew member of a linked yard ends the mapping card
// that asks who they are, as mapping a user does: the card's bead closes
// with a reason naming the speaker, and its ask is taken back, so the
// operator's queue holds no card for a sender who is mapped. Another
// sender's card stands.
func TestPeerIdentityEndsTheMappingCard(t *testing.T) {
	t.Parallel()
	y := peerIdentityYard(t)
	for _, sender := range []string{"T9", "T8"} {
		if created, err := y.own.ClaimIdentity(y.ctx, "talk", sender, "C1", "hi"); err != nil || !created {
			t.Fatalf("ClaimIdentity for %s = %v, %v", sender, created, err)
		}
	}
	queue, err := y.api.UserAsks(y.ctx, "jo", "jo")
	if err != nil || len(queue.Open) != 2 {
		t.Fatalf("the operator's queue = %+v, %v, want the two cards", queue, err)
	}
	cards := map[string]store.OpenAsk{}
	for _, o := range queue.Open {
		cards[o.Identity.Sender] = o
	}

	y.mapPeer(t, "north", "talk", "T9", "mayor")

	b, err := y.api.GetBead(y.ctx, cards["T9"].Bead)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "closed" || b.CloseReason == nil || *b.CloseReason != "T9 is mayor@north in talk" {
		t.Fatalf("the card for T9 is %s with %v, want it closed with the speaker named", b.Status, b.CloseReason)
	}
	if b, err = y.api.GetBead(y.ctx, cards["T8"].Bead); err != nil || b.Status != "open" {
		t.Fatalf("the card for T8 = %+v, %v, want it left open", b, err)
	}
	queue, err = y.api.UserAsks(y.ctx, "jo", "jo")
	if err != nil || len(queue.Open) != 1 || queue.Open[0].Identity.Sender != "T8" {
		t.Fatalf("the operator's queue after the mapping = %+v, %v, want T8's card alone", queue, err)
	}
	var ended []store.DecidedAsk
	for _, d := range queue.Decided {
		if d.Bead == cards["T9"].Bead {
			ended = append(ended, d)
		}
	}
	if len(ended) != 1 || ended[0].State != "taken back" || ended[0].Words != "T9 is mayor@north in talk" {
		t.Fatalf("T9's card among the decided = %+v, want it taken back with the mapping as the reason", ended)
	}
}

// What cannot be a mapping is refused with why, and keeps the link as it was.
func TestPeerIdentityRefusals(t *testing.T) {
	t.Parallel()
	y := peerIdentityYard(t)
	y.mapPeer(t, "north", "chat", "U7", "mayor")
	for _, c := range []struct {
		peer string
		id   store.PeerIdentity
		want string
	}{
		{"north", store.PeerIdentity{ID: "U1", Crew: "clerk"}, "identity: source is required"},
		{"north", store.PeerIdentity{Source: "chat", Crew: "clerk"}, "identity: id is required"},
		{"north", store.PeerIdentity{Source: "chat", ID: "U1"}, `crew member "" of a peer: use letters, digits`},
		{"north", store.PeerIdentity{Source: "chat", ID: "U1", Crew: "clerk@north"}, `crew member "clerk@north" of a peer: use letters, digits`},
		{"north", store.PeerIdentity{Source: "chat", ID: "U1", Crew: "two words"}, `crew member "two words" of a peer: use letters, digits`},
		{"nowhere", store.PeerIdentity{Source: "chat", ID: "U1", Crew: "clerk"}, "peer nowhere: not found"},
		{"north", store.PeerIdentity{Source: "nowhere", ID: "U1", Crew: "clerk"}, "peer north: nowhere is no source this yard declares"},
		// A source that hears no sender by identity has no one to map.
		{"north", store.PeerIdentity{Source: "plain", ID: "U1", Crew: "clerk"}, "peer north: plain hears no sender by identity"},
		// Another crew member of the yard with the id the mayor has.
		{"north", store.PeerIdentity{Source: "chat", ID: "U7", Crew: "clerk"}, "peer north: U7 in chat names mayor@north already: an id in a source names one speaker of the yard"},
	} {
		if _, err := y.api.SetPeerIdentity(y.ctx, c.peer, c.id, "jo"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("mapping %+v on %s = %v, want %q", c.id, c.peer, err, c.want)
		}
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); !reflect.DeepEqual(p.Identities, []store.PeerIdentity{{Source: "chat", ID: "U7", Crew: "mayor"}}) {
		t.Fatalf("a refused mapping changed the link: %+v", p.Identities)
	}
	if set := peerEvents(t, y.api, "peer_identity_set"); len(set) != 1 {
		t.Fatalf("the refusals logged %d events", len(set)-1)
	}
}

// One id names one speaker of the yard, a user or a crew member of a linked
// yard. Whichever is written second is refused, by id or by key. A link that
// has an id cannot give it to another crew member, and neither can another
// link. The same id in another source is no clash.
func TestPeerIdentityOneIdOneSpeaker(t *testing.T) {
	t.Parallel()
	y := peerIdentityYard(t)
	other, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "south.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "south", Key: store.Public(other)}, "jo"); err != nil {
		t.Fatal(err)
	}

	// A user's id, or the key a user is known by in any case, is theirs.
	if err := y.api.SetIdentity(y.ctx, "ada", "chat", store.Identity{ID: "U1", Key: "ada-key"}, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"U1", "ADA-key"} {
		_, err := y.api.SetPeerIdentity(y.ctx, "north", store.PeerIdentity{Source: "chat", ID: id, Crew: "mayor"}, "jo")
		if err == nil || !strings.Contains(err.Error(), "peer north: "+id+" in chat names ada already: an id in a source names one speaker of the yard") {
			t.Errorf("mapping %s, which ada has = %v", id, err)
		}
	}
	// The same id in another source is another sender's.
	y.mapPeer(t, "north", "talk", "U1", "mayor")

	// A crew member's mapped id is theirs: no user is given it, by id or by
	// key in any case.
	y.mapPeer(t, "north", "chat", "U2", "mayor")
	for _, id := range []store.Identity{{ID: "U2"}, {Key: "u2"}, {ID: "U3", Key: "U2"}} {
		err := y.api.SetIdentity(y.ctx, "ada", "chat", id, "jo")
		if err == nil || !strings.Contains(err.Error(), "user ada: ") || !strings.Contains(err.Error(), " in chat names mayor@north already: an id in a source names one speaker of the yard") {
			t.Errorf("giving ada %+v, which mayor@north has = %v", id, err)
		}
	}
	if u, _ := y.api.GetUser(y.ctx, "ada"); u.Identities["chat"] != (store.Identity{ID: "U1", Key: "ada-key"}) {
		t.Fatalf("a refused identity changed ada: %+v", u.Identities)
	}
	// Taking the mapping back frees the id.
	if _, err := y.api.RemovePeerIdentity(y.ctx, "north", "mayor", "chat", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.api.SetIdentity(y.ctx, "ada", "chat", store.Identity{ID: "U2"}, "jo"); err != nil {
		t.Fatalf("giving ada the id back from a mapping taken back: %v", err)
	}

	// Another link may not map an id a link has already.
	y.mapPeer(t, "north", "chat", "U4", "mayor")
	_, err = y.api.SetPeerIdentity(y.ctx, "south", store.PeerIdentity{Source: "chat", ID: "U4", Crew: "mayor"}, "jo")
	if err == nil || !strings.Contains(err.Error(), "peer south: U4 in chat names mayor@north already") {
		t.Fatalf("another link mapping U4 = %v", err)
	}
	y.mapPeer(t, "south", "talk", "U4", "mayor")
	// The crew member replaced by its own id again clashes with nothing.
	y.mapPeer(t, "north", "chat", "U4", "mayor")
}

// Removing a link takes its identities with it and says which they were, and
// a yard added again under the name starts with none. Renaming it keeps them,
// and the speaker follows the new name.
func TestPeerIdentityGoesWithTheLink(t *testing.T) {
	t.Parallel()
	y := peerIdentityYard(t)
	y.mapPeer(t, "north", "chat", "U7", "mayor")
	y.mapPeer(t, "north", "talk", "T7", "mayor")

	p, err := y.api.RenamePeer(y.ctx, "north", "up", "jo")
	want := []store.PeerIdentity{{Source: "chat", ID: "U7", Crew: "mayor"}, {Source: "talk", ID: "T7", Crew: "mayor"}}
	if err != nil || !reflect.DeepEqual(p.Identities, want) {
		t.Fatalf("after a rename: %+v, %v", p, err)
	}
	if read, err := y.own.PeerIdentities(y.ctx); err != nil || !reflect.DeepEqual(read, map[string][]store.PeerIdentity{"up": want}) {
		t.Fatalf("PeerIdentities after a rename = %+v, %v", read, err)
	}

	if err := y.api.DeletePeer(y.ctx, "up", "jo"); err != nil {
		t.Fatal(err)
	}
	gone := peerEvents(t, y.api, "peer_removed")
	if len(gone) != 1 {
		t.Fatalf("peer_removed = %+v", gone)
	}
	if got, _ := gone[0].Data["identities"].([]any); len(got) != 2 {
		t.Fatalf("peer_removed says of the identities %+v", gone[0].Data["identities"])
	}
	if read, err := y.own.PeerIdentities(y.ctx); err != nil || len(read) != 0 {
		t.Fatalf("PeerIdentities after the link was removed = %+v, %v", read, err)
	}
	// The ids are free for a user, and a yard added under the name starts with none.
	if err := y.api.SetIdentity(y.ctx, "ada", "chat", store.Identity{ID: "U7"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "up", Key: y.key}, "jo"); err != nil {
		t.Fatal(err)
	}
	if p, err := y.api.GetPeer(y.ctx, "up"); err != nil || p.Identities != nil {
		t.Fatalf("a yard added again under the name maps %+v, %v", p, err)
	}
}

// Renaming a link carries the subscriptions that filter on one of its
// crew's speakers, a crew member's, a user's and a group's: the filter
// follows the new name, so a delivery from the mapped sender still reaches
// the subscriber. Before the rename the delivery arrives under the old
// speaker; after, under the new one, and nothing is left under the old. A
// speaker of another yard stands as it is.
func TestPeerRenameCarriesSpeakerFilters(t *testing.T) {
	t.Parallel()
	y := peerIdentityYard(t)
	y.mapPeer(t, "north", "chat", "U7", "mayor")
	sub := store.Subscription{Source: "chat", Scope: store.ScopeAll, Where: map[string]string{"speaker": "mayor@north"}}
	if err := y.api.AddSubscription(y.ctx, "clerk", sub, "clerk"); err != nil {
		t.Fatal(err)
	}
	if err := y.api.AddSubscription(y.ctx, "clerk", store.Subscription{Source: "chat", Scope: store.ScopeAll,
		Where: map[string]string{"speaker": "clerk@elsewhere"}}, "clerk"); err != nil {
		t.Fatal(err)
	}
	// The user's filter holds another speaker beside the mayor's, which
	// the rename keeps.
	if err := y.api.AddSubscription(y.ctx, "ada", store.Subscription{Source: "chat", Scope: store.ScopeAll,
		Where: map[string]string{"speaker": "mayor@north\x00ada"}}, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := y.api.PutGroup(y.ctx, store.Group{Name: "watch", Runner: "manual", Members: []string{"yardmaster"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.api.AddSubscription(y.ctx, "watch", sub, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	deliver := func(t *testing.T, speaker, body string) {
		t.Helper()
		if _, dup, err := y.own.RecordHook(y.ctx, "chatline", map[string]any{
			"source": "chat", "type": "message", "action": "create", "hook": "chatline",
			"fields": map[string]any{"speaker": []any{speaker}, "sender": []any{"U7"}},
		}, []byte(body), nil); err != nil || dup {
			t.Fatalf("RecordHook = %v, %v", dup, err)
		}
	}
	inbox := func(t *testing.T) int {
		t.Helper()
		page, err := y.api.Inbox(y.ctx, "clerk", 0)
		if err != nil {
			t.Fatal(err)
		}
		return page.Total
	}
	deliver(t, "mayor@north", "before")
	if got := inbox(t); got != 1 {
		t.Fatalf("clerk's inbox before the rename holds %d, want the mayor's delivery", got)
	}

	if _, err := y.api.RenamePeer(y.ctx, "north", "south", "jo"); err != nil {
		t.Fatal(err)
	}
	r, err := y.api.GetCrew(y.ctx, "clerk")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Subscriptions) != 2 || r.Subscriptions[0].Where["speaker"] != "mayor@south" ||
		r.Subscriptions[1].Where["speaker"] != "clerk@elsewhere" {
		t.Fatalf("clerk after the rename subscribes to %v", r.Subscriptions)
	}
	u, err := y.api.GetUser(y.ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Subscriptions) != 1 || !slices.Equal(store.WhereValues(u.Subscriptions[0].Where["speaker"]), []string{"ada", "mayor@south"}) {
		t.Fatalf("ada after the rename subscribes to %v", u.Subscriptions)
	}
	g, err := y.api.GetGroup(y.ctx, "watch")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Subscriptions) != 1 || g.Subscriptions[0].Where["speaker"] != "mayor@south" {
		t.Fatalf("watch after the rename subscribes to %v", g.Subscriptions)
	}
	renamed := peerEvents(t, y.api, "peer_renamed")
	if raw, _ := json.Marshal(renamed[0].Data["subscriptions"]); len(renamed) != 1 || string(raw) != `["clerk","ada","watch"]` {
		t.Fatalf("peer_renamed = %+v", renamed)
	}

	deliver(t, "mayor@south", "after")
	if got := inbox(t); got != 2 {
		t.Fatalf("clerk's inbox after the rename holds %d, want the mayor's delivery under the new speaker too", got)
	}
	deliver(t, "mayor@north", "old")
	if got := inbox(t); got != 2 {
		t.Fatalf("clerk's inbox after a delivery under the old speaker holds %d, want still 2", got)
	}
}

// Renaming a link carries the hooks whose says and makes lines filter on one
// of its crew's speakers: the filter follows the new name, so a delivery
// from the mapped sender still matches the line. Before the rename the
// delivery matches under the old speaker; after, under the new one, and
// nothing matches under the old. A line filtering another yard's speaker
// stands as it is, and the event names the hooks that followed.
func TestPeerRenameCarriesHookSpeakerFilters(t *testing.T) {
	t.Parallel()
	y := peerIdentityYard(t)
	y.mapPeer(t, "north", "chat", "U7", "mayor")
	says, err := store.ParseSay("speaker=mayor@north -> done")
	if err != nil {
		t.Fatal(err)
	}
	makes, err := store.ParseMake("speaker=mayor@north -> task:new")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.PutHook(y.ctx, store.Hook{Name: "chatline", Source: "chat", SecretFile: "/nonexistent/s",
		Says: []store.Say{says}, Makes: []store.Make{makes}}, "jo"); err != nil {
		t.Fatal(err)
	}
	other, err := store.ParseSay("speaker=clerk@elsewhere -> done")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.PutHook(y.ctx, store.Hook{Name: "other", Source: "chat", SecretFile: "/nonexistent/s",
		Says: []store.Say{other}}, "jo"); err != nil {
		t.Fatal(err)
	}
	// A delivery from the mapped sender, as the receiver stamps it: the
	// sender's id beside the speaker the mapping gives it now.
	delivered := func(t *testing.T, speaker string) map[string]any {
		t.Helper()
		seq, dup, err := y.own.RecordHook(y.ctx, "chatline", map[string]any{
			"source": "chat", "type": "message", "action": "create", "hook": "chatline",
			"fields": map[string]any{"speaker": []any{speaker}, "sender": []any{"U7"}},
		}, []byte(speaker), nil)
		if err != nil || dup {
			t.Fatalf("RecordHook = %v, %v", dup, err)
		}
		for _, e := range peerEvents(t, y.api, "hook") {
			if e.Seq == seq {
				return e.Data
			}
		}
		t.Fatalf("no hook event %d", seq)
		return nil
	}
	hook := func(t *testing.T, name string) store.Hook {
		t.Helper()
		h, err := y.api.GetHook(y.ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return *h
	}
	matched := func(t *testing.T, data map[string]any) (say, made string) {
		t.Helper()
		h := hook(t, "chatline")
		if line := h.Said(data, "new", "task"); line != nil {
			say = line.Outcome
		}
		if line := h.Made(data); line != nil {
			made = line.Type + ":" + line.Stage
		}
		return say, made
	}
	if say, made := matched(t, delivered(t, "mayor@north")); say != "done" || made != "task:new" {
		t.Fatalf("before the rename the delivery says %q and makes %q, want done and task:new", say, made)
	}

	if _, err := y.api.RenamePeer(y.ctx, "north", "south", "jo"); err != nil {
		t.Fatal(err)
	}
	h := hook(t, "chatline")
	if len(h.Says) != 1 || h.Says[0].Where["speaker"] != "mayor@south" ||
		len(h.Makes) != 1 || h.Makes[0].Where["speaker"] != "mayor@south" {
		t.Fatalf("chatline after the rename says %v and makes %v", h.Says, h.Makes)
	}
	if o := hook(t, "other"); len(o.Says) != 1 || o.Says[0].Where["speaker"] != "clerk@elsewhere" {
		t.Fatalf("the other yard's line after the rename says %v", o.Says)
	}
	renamed := peerEvents(t, y.api, "peer_renamed")
	if raw, _ := json.Marshal(renamed[0].Data["hooks"]); len(renamed) != 1 || string(raw) != `["chatline"]` {
		t.Fatalf("peer_renamed = %+v", renamed)
	}

	if say, made := matched(t, delivered(t, "mayor@south")); say != "done" || made != "task:new" {
		t.Fatalf("after the rename the delivery says %q and makes %q, want done and task:new", say, made)
	}
	if say, made := matched(t, delivered(t, "mayor@north")); say != "" || made != "" {
		t.Fatalf("after the rename a delivery under the old speaker says %q and makes %q, want nothing", say, made)
	}
}

// Folding a source's records into another's (pack apply folds
// <name>-declared into <name>) takes the link's identities along, as it does
// the users' and the crew's. Left behind, they would map a sender in a source
// no delivery names. A crew member with a different id under each name is
// refused, and the same id under both is kept once.
func TestMoveSourceRecordsMovesPeerIdentities(t *testing.T) {
	t.Parallel()
	y := peerIdentityYard(t)
	if _, err := y.api.PutSource(y.ctx, "chat-declared.yaml", identitySource("chat-declared"), "jo"); err != nil {
		t.Fatal(err)
	}
	y.mapPeer(t, "north", "chat-declared", "U7", "mayor")
	y.mapPeer(t, "north", "chat-declared", "U8", "clerk")
	y.mapPeer(t, "north", "talk", "T7", "mayor")
	// The mayor has another id already in chat: the move is refused, and nothing moves.
	y.mapPeer(t, "north", "chat", "U9", "mayor")
	_, err := y.api.MoveSourceRecords(y.ctx, "chat-declared", "chat", "jo")
	if err == nil || !strings.Contains(err.Error(), "mayor@north has one chat-declared identity and another chat identity") {
		t.Fatalf("moving with two ids for the mayor = %v", err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); len(p.Identities) != 4 || p.Identities[0].Source != "chat" {
		t.Fatalf("a refused move changed the link: %+v", p.Identities)
	}

	// The same id under both names is one identity afterwards.
	if _, err := y.api.RemovePeerIdentity(y.ctx, "north", "mayor", "chat-declared", "jo"); err != nil {
		t.Fatal(err)
	}
	y.mapPeer(t, "north", "chat-declared", "U9", "mayor")
	moved, err := y.api.MoveSourceRecords(y.ctx, "chat-declared", "chat", "jo")
	if err != nil || moved.Identities != 2 {
		t.Fatalf("moved = %+v, %v", moved, err)
	}
	want := []store.PeerIdentity{
		{Source: "chat", ID: "U8", Crew: "clerk"}, {Source: "chat", ID: "U9", Crew: "mayor"}, {Source: "talk", ID: "T7", Crew: "mayor"}}
	if p, _ := y.api.GetPeer(y.ctx, "north"); !reflect.DeepEqual(p.Identities, want) {
		t.Fatalf("after the move: %+v, want %+v", p.Identities, want)
	}
	if again, err := y.api.MoveSourceRecords(y.ctx, "chat-declared", "chat", "jo"); err != nil || again.Moved() {
		t.Fatalf("moving again = %+v, %v", again, err)
	}
}

// Folding a peer mapping into a source refuses an id a user or another
// linked yard already holds there. Otherwise the store would make the
// sender ambiguous after both setters had kept that id one-to-one.
func TestMoveSourceRecordsRefusesPeerIdentityClashes(t *testing.T) {
	for _, other := range []string{"user", "peer"} {
		other := other
		t.Run(other, func(t *testing.T) {
			t.Parallel()
			y := peerIdentityYard(t)
			if _, err := y.api.PutSource(y.ctx, "chat-declared.yaml", identitySource("chat-declared"), "jo"); err != nil {
				t.Fatal(err)
			}
			wantHolder := "ada"
			if other == "user" {
				if err := y.api.SetIdentity(y.ctx, "ada", "chat", store.Identity{ID: "U7"}, "jo"); err != nil {
					t.Fatal(err)
				}
			} else {
				key, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "south.key"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "south", Key: store.Public(key)}, "jo"); err != nil {
					t.Fatal(err)
				}
				if _, err := y.api.SetPeerIdentity(y.ctx, "south", store.PeerIdentity{Source: "chat", ID: "U7", Crew: "mayor"}, "jo"); err != nil {
					t.Fatal(err)
				}
				wantHolder = "mayor@south"
			}
			y.mapPeer(t, "north", "chat-declared", "U7", "mayor")

			head, err := y.api.Head(y.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := y.api.MoveSourceRecords(y.ctx, "chat-declared", "chat", "jo"); err == nil || !strings.Contains(err.Error(), "U7 in chat would also name "+wantHolder) {
				t.Fatalf("moving a colliding identity = %v", err)
			}
			if now, _ := y.api.Head(y.ctx); now != head {
				t.Fatalf("refused, yet %d events were logged", now-head)
			}
			p, err := y.api.GetPeer(y.ctx, "north")
			if err != nil || !reflect.DeepEqual(p.Identities, []store.PeerIdentity{{Source: "chat-declared", ID: "U7", Crew: "mayor"}}) {
				t.Fatalf("a refused move changed the mapping: %+v, %v", p.Identities, err)
			}
		})
	}
}

// A speaker with an "@" is a crew member of a linked yard and no user,
// because no user's name has one. The inbox line frames it as information on
// that ground (SpeakerIsForeign). This pins it for the ways a user gets a
// name, and for a link's name, which the speaker is built from. A crew member
// of this yard is never a speaker.
func TestSpeakerIsForeign(t *testing.T) {
	t.Parallel()
	for speaker, want := range map[string]bool{
		"hal@yardr-north": true, "@north": true, "ada@": true,
		"ada": false, "": false, store.SpeakerOutside: false,
	} {
		if got := store.SpeakerIsForeign(speaker); got != want {
			t.Errorf("SpeakerIsForeign(%q) = %v, want %v", speaker, got, want)
		}
	}
	y, _ := rosterYard(t)
	for _, name := range []string{"ada@north", "@north", "ada@"} {
		if _, err := y.api.AddUser(y.ctx, name, "jo"); err == nil {
			t.Errorf("a user was added as %q", name)
		}
		if err := y.api.SetSetting(y.ctx, store.OperatorSetting, name, "jo"); err == nil {
			t.Errorf("the operator was set to %q", name)
		}
		if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: name, Key: peerKey(t)}, "jo"); err == nil {
			t.Errorf("a peer was added as %q", name)
		}
	}
}
