package store_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// away is the flow of a yard whose stage build another yard works.
const away = "build -> review on done | decide on refused | decide on undelivered\nreview -> merged\ndecide -> build\nmerged terminal\n"

// newSendingYard is a yard linked with north that has its stage build, in
// the rig web, worked there: the link carries bead out, the group at-north
// names the yard and a route sends the stage to it. It returns the yard and
// its own public key.
func newSendingYard(t *testing.T) (*peerYard, string) {
	t.Helper()
	y := newPeerYard(t, "http://north.test")
	keyFile := filepath.Join(t.TempDir(), "peer.key")
	k, _, err := yardkey.Ensure(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	y.own.SetYard(keyFile, "v0.9.2")
	if err := y.api.PutRig(y.ctx, store.Rig{Name: "web", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putNamed(t, y.api, "away", away)
	if err := y.api.SetFlowPointer(y.ctx, "web", "", "away", "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Send: []string{"bead"}})
	if err := y.api.PutGroup(y.ctx, store.Group{Name: "at-north", Runner: store.PeerRunner, Limit: 2, Config: map[string]any{"peer": "north"}}); err != nil {
		t.Fatal(err)
	}
	return y, yardkey.Public(k)
}

// send creates a bead in build, claims it for at-north as the dispatcher
// does and has it sent; it returns the bead and its session.
func (y *peerYard) send(t *testing.T, nb store.NewBead) (*store.Bead, *store.Session) {
	t.Helper()
	nb.Rig, nb.Stage = "web", "build"
	b, err := y.api.CreateBead(y.ctx, nb, "jo")
	if err != nil {
		t.Fatal(err)
	}
	return b, y.claim(t, b.ID)
}

func (y *peerYard) claim(t *testing.T, id string) *store.Session {
	t.Helper()
	sess, err := y.api.Claim(y.ctx, id, "at-north", "build", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.PeerBeadSend(y.ctx, sess.ID); err != nil {
		t.Fatal(err)
	}
	if err := y.api.MarkRunning(y.ctx, sess.ID, map[string]any{"peer": "north"}); err != nil {
		t.Fatal(err)
	}
	return sess
}

func (y *peerYard) outbox(t *testing.T) []store.PeerOut {
	t.Helper()
	out, err := y.api.PeerOutbox(y.ctx, "north", 0)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A peer group names a yard this one is linked with, whose link carries
// beads out, and has no members.
func TestPeerGroup(t *testing.T) {
	y := newPeerYard(t, "http://north.test")
	put := func(g store.Group) error {
		g.Name, g.Runner = "at-north", store.PeerRunner
		return y.api.PutGroup(y.ctx, g)
	}
	for want, g := range map[string]store.Group{
		"--set peer=<peer>":             {},
		"peer south: not found":         {Config: map[string]any{"peer": "south"}},
		"does not carry bead out":       {Config: map[string]any{"peer": "north"}},
		"members are only for manual":   {Config: map[string]any{"peer": "north"}, Members: []string{"jo"}},
		"yardr peer allow north --send": {Config: map[string]any{"peer": "north"}},
	} {
		if err := put(g); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a peer group %+v: %v, want %q", g, err, want)
		}
	}
	y.allow(t, store.PeerAllow{Send: []string{"bead"}})
	if err := put(store.Group{Config: map[string]any{"peer": "north", "tries": "none"}}); err == nil || !strings.Contains(err.Error(), "at least 1") {
		t.Fatalf("tries=none: %v", err)
	}
	if err := put(store.Group{Limit: 3, Config: map[string]any{"peer": "north", "tries": float64(2)}}); err != nil {
		t.Fatal(err)
	}
	g, err := y.api.GetGroup(y.ctx, "at-north")
	if err != nil || g.AtPeer() != "north" || g.Limit != 3 {
		t.Fatalf("the group = %+v, %v", g, err)
	}
	if other := (store.Group{Runner: "exec", Config: map[string]any{"peer": "north"}}); other.AtPeer() != "" {
		t.Fatal("a group of another runner sends to a peer")
	}
}

// What is sent is the bead as the other yard is to work it: the message of
// docs/peers.md, under an id made of the bead and the event that put it in
// the stage, so a second claim in the same stay sends nothing new.
func TestPeerBeadSend(t *testing.T) {
	y, own := newSendingYard(t)
	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "Add the export", Body: "As a CSV.", Type: "feature", Priority: 1, Rig: "web", Stage: "build",
		Links: []store.NewLink{{Kind: store.LinkTracks, Source: "linear", URL: "https://linear.app/x/issue/YRD-1"}}}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first", "second"} {
		if err := y.api.AddNote(y.ctx, b.ID, text, "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	sess := y.claim(t, b.ID)
	out := y.outbox(t)
	if len(out) != 1 || out[0].Kind != store.PeerBead || out[0].Re != "" || out[0].Tries != store.PeerBeadTries || !strings.HasPrefix(out[0].ID, b.UUID+"-") {
		t.Fatalf("the outbox after the claim = %+v", out)
	}
	var m store.PeerBeadMessage
	if err := json.Unmarshal(out[0].Body, &m); err != nil {
		t.Fatal(err)
	}
	if m.V != 1 || m.Yardr != "v0.9.2" || m.Bead != b.ID || m.Stage != "build" || m.Title != "Add the export" || m.Body != "As a CSV." || m.Type != "feature" ||
		m.Priority != 1 || !slices.Equal(m.Outcomes, []string{"done", "refused", "undelivered"}) || !slices.Equal(m.Path, []string{own}) ||
		len(m.Notes) != 2 || m.Notes[0].Text != "first" || m.Notes[0].Author != "yardmaster" || m.Notes[1].Text != "second" || m.NotesLeftOut != 0 ||
		len(m.Links) != 1 || m.Links[0].URL != "https://linear.app/x/issue/YRD-1" || m.Links[0].Kind != "tracks" {
		t.Fatalf("the bead message = %+v", m)
	}
	if strings.Contains(string(out[0].Body), b.UUID) {
		t.Fatal("the message's body names the bead by its UUID")
	}
	sent := peerEvents(t, y.api, "peer_bead_sent")
	if len(sent) != 1 || sent[0].Data["id"] != out[0].ID || sent[0].Data["session"] != sess.ID || *sent[0].Bead != b.ID {
		t.Fatalf("peer_bead_sent = %+v", sent)
	}
	for _, e := range sent {
		if raw, _ := json.Marshal(e.Data); strings.Contains(string(raw), "As a CSV") {
			t.Fatalf("an event carries the bead's body: %s", raw)
		}
	}

	// The daemon died while it started the session: the claim is made
	// again, and the message is the one that waits already.
	if err := y.api.EndSession(y.ctx, sess.ID, "failed", "start interrupted"); err != nil {
		t.Fatal(err)
	}
	y.claim(t, b.ID)
	if again := y.outbox(t); len(again) != 1 || again[0].ID != out[0].ID || len(peerEvents(t, y.api, "peer_bead_sent")) != 1 {
		t.Fatalf("the outbox after a second claim in the stay = %+v", again)
	}
	// And once north has it, nothing is queued for a third.
	if err := y.api.PeerDelivered(y.ctx, "north", out[0].Seq); err != nil {
		t.Fatal(err)
	}
	live, _ := y.api.Sessions(y.ctx, false)
	if err := y.api.EndSession(y.ctx, live[0].ID, "failed", "start interrupted"); err != nil {
		t.Fatal(err)
	}
	y.claim(t, b.ID)
	if len(y.outbox(t)) != 0 || len(peerEvents(t, y.api, "peer_bead_sent")) != 1 {
		t.Fatal("a bead north has was queued again")
	}
	// A bead that is no session's is sent by nobody.
	if _, err := y.api.PeerBeadSend(y.ctx, sess.ID); err == nil || !strings.Contains(err.Error(), "does not work bead") {
		t.Fatalf("sending for a session that ended: %v", err)
	}
	// It is the yard's own to send: not by hand.
	if _, err := y.api.PeerSend(y.ctx, "north", store.PeerBead, nil, "jo"); err == nil {
		t.Fatal("a bead message was sent by hand")
	}
}

// A bead too large to cross a link is not sent: its stage ends as refused,
// and the note says why. Older notes that do not fit are left out, and
// counted, as a return's are.
func TestPeerBeadTooLargeToSend(t *testing.T) {
	y, _ := newSendingYard(t)
	big, _ := y.send(t, store.NewBead{Title: "Too much", Body: strings.Repeat("x", store.MaxPeerBeadBody+1)})
	got, err := y.api.GetBead(y.ctx, big.ID)
	if err != nil || got.Stage != "decide" || len(y.outbox(t)) != 0 {
		t.Fatalf("a bead too large to send = %+v, %v; outbox %d", got, err, len(y.outbox(t)))
	}
	notes, _ := y.api.Notes(y.ctx, big.ID)
	if len(notes) != 1 || !strings.Contains(notes[0].Text, "The yard north did not take this bead: too large to send") {
		t.Fatalf("its note = %+v", notes)
	}
	if away := got.Away(); away == nil || away.Outcome != store.OutcomeRefused || !strings.Contains(away.Line(), "refused") {
		t.Fatalf("what it says = %+v", away)
	}

	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "Many notes", Rig: "web", Stage: "build"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := y.api.AddNote(y.ctx, b.ID, fmt.Sprintf("%d %s", i, strings.Repeat("n", store.MaxPeerNoteBytes)), "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	y.claim(t, b.ID)
	out := y.outbox(t)
	var m store.PeerBeadMessage
	if len(out) != 1 || json.Unmarshal(out[0].Body, &m) != nil {
		t.Fatalf("the outbox = %+v", out)
	}
	if len(m.Notes)+m.NotesLeftOut != 10 || m.NotesLeftOut == 0 || !strings.HasPrefix(m.Notes[len(m.Notes)-1].Text, "9 ") ||
		!strings.Contains(m.Notes[0].Text, "[cut: ") || len(out[0].Body) > store.MaxPeerBody {
		t.Fatalf("the notes sent: %d, %d left out, body %d bytes", len(m.Notes), m.NotesLeftOut, len(out[0].Body))
	}
}

// Where a bead has been comes with it and goes on with it: a bead that
// arrives with a path is sent on with that path and this yard's key, one
// from a yardr that said none with its sender's. A yard that finds itself
// on the path refuses the bead, and so does one it would reach as the
// ninth.
func TestPeerBeadPath(t *testing.T) {
	y, own := newSendingYard(t)
	if _, err := y.api.AddUser(y.ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	putNamed(t, y.api, "incoming", "build -> returned on done\nreturned terminal\n")
	y.allow(t, store.PeerAllow{Receive: []string{"bead"}})
	if _, err := y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Rig: "web", Flow: "incoming", Auto: true, Limit: 50}, "jo"); err != nil {
		t.Fatal(err)
	}
	keys := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("yard-%02d-%s", i, strings.Repeat("k", 32))
		}
		return out
	}
	with := func(path any) func(map[string]any) {
		return func(b map[string]any) { b["path"] = path }
	}
	for name, tc := range map[string]struct {
		path any
		want string
	}{
		"a loop":                   {[]string{own, y.key}, store.RefusedLoop},
		"a loop the sender hid":    {[]string{keys(1)[0], own}, store.RefusedLoop},
		"nine yards":               {append(keys(8), y.key), store.RefusedTooFar},
		"eight and the sender":     {keys(8), store.RefusedTooFar},
		"what is no key":           {[]string{"not a key"}, store.RefusedNotBead},
		"eight yards":              {append(keys(7), y.key), ""},
		"seven without the sender": {keys(7), ""},
		"none, an older yardr":     {nil, ""},
	} {
		id := "p-" + strings.ReplaceAll(name, " ", "-")[:min(len(name), 20)]
		id = strings.NewReplacer(",", "", ".", "").Replace(id)
		theirs := "web-" + id[2:8]
		m := beadMsg(id, theirs, nil)
		if tc.path != nil {
			m = beadMsg(id, theirs, with(tc.path))
		}
		if got := y.take(t, m); got[0].Refused != tc.want {
			t.Errorf("%s: taken as %+v, want refused %q", name, got[0], tc.want)
		}
	}
	if store.PeerRefusal("north", "bead", store.RefusedLoop) == store.PeerRefusal("north", "bead", "unheard-of") ||
		!strings.Contains(store.PeerRefusal("north", "bead", store.RefusedTooFar), "8") {
		t.Fatal("the sender has no words for a loop or a path too long")
	}

	// Sent on, each says where it has been: its path, with the sender it
	// came from last whatever it wrote, and this yard.
	bs, err := y.api.ListBeads(y.ctx, store.BeadFilter{Rig: "web", Stage: "build"})
	if err != nil || len(bs) != 3 {
		t.Fatalf("the beads that were taken = %d, %v", len(bs), err)
	}
	if err := y.api.PutGroup(y.ctx, store.Group{Name: "at-north", Runner: store.PeerRunner, Limit: 5, Config: map[string]any{"peer": "north"}}); err != nil {
		t.Fatal(err)
	}
	want := map[int]bool{9: true, 2: true} // eight and this yard twice over, the sender alone and this yard
	lens := []int{}
	for _, b := range bs {
		y.claim(t, b.ID)
	}
	for _, o := range y.outbox(t) {
		var m store.PeerBeadMessage
		if json.Unmarshal(o.Body, &m) != nil || m.Path[len(m.Path)-1] != own || m.Path[len(m.Path)-2] != y.key {
			t.Fatalf("a bead sent on says it has been at %v", m.Path)
		}
		lens = append(lens, len(m.Path))
		if !want[len(m.Path)] {
			t.Fatalf("a bead sent on has a path of %d yards", len(m.Path))
		}
	}
	slices.Sort(lens)
	if !slices.Equal(lens, []int{2, 9, 9}) {
		t.Fatalf("the paths sent on are %v yards long", lens)
	}
}

// A return that is none is refused, and one for a bead this yard has not
// sent changes nothing.
func TestPeerBeadReturnsThatAreNone(t *testing.T) {
	y, _ := newSendingYard(t)
	b, _ := y.send(t, store.NewBead{Title: "Add the export"})
	id := y.outbox(t)[0].ID
	ret := func(mid, re string, body map[string]any) store.PeerMessage {
		raw, _ := json.Marshal(body)
		return store.PeerMessage{ID: mid, Kind: store.PeerBeadReturn, Re: re, Body: raw}
	}
	for name, tc := range map[string]struct {
		m    store.PeerMessage
		want string
	}{
		"another version": {ret("r1", id, map[string]any{"v": 2, "bead": "web-x1", "outcome": "done"}), store.RefusedVersion},
		"no outcome":      {ret("r2", id, map[string]any{"v": 1, "bead": "web-x1"}), store.RefusedNotBead},
		"an outcome that is no word": {ret("r3", id, map[string]any{"v": 1, "bead": "web-x1", "outcome": "done\nheld: no"}),
			store.RefusedNotBead},
	} {
		if got := y.take(t, tc.m); got[0].Refused != tc.want {
			t.Errorf("%s: %+v, want refused %q", name, got[0], tc.want)
		}
	}
	// For a bead that is not there, and for no bead at all: taken, logged
	// as stale, nothing moved.
	y.take(t, ret("r4", "0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee-4", map[string]any{"v": 1, "bead": "web-x1", "outcome": "done"}),
		ret("r5", "nothing", map[string]any{"v": 1, "bead": "web-x1", "outcome": "done"}))
	stale := 0
	for _, e := range peerEvents(t, y.api, "peer_bead_returned") {
		if e.Data["stale"] == true {
			stale++
		}
	}
	if got, _ := y.api.GetBead(y.ctx, b.ID); stale != 2 || got.Stage != "build" || got.Group == nil {
		t.Fatalf("after returns for no bead: %d stale, bead %+v", stale, got)
	}
	// A link that sends no beads takes no return of one.
	y.allow(t, store.PeerAllow{NoSend: []string{"bead"}})
	if got := y.take(t, ret("r6", id, map[string]any{"v": 1, "bead": "web-x1", "outcome": "done"})); got[0].Refused != store.RefusedKind {
		t.Fatalf("a return over a link that sends no beads = %+v", got)
	}
}

// The map of a flow says which stage another yard works, and which yard.
func TestFlowMapOfAPeerStage(t *testing.T) {
	y, _ := newSendingYard(t)
	if _, err := y.api.AddRoute(y.ctx, store.Route{Stage: "build", Group: "at-north"}); err != nil {
		t.Fatal(err)
	}
	b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "Add the export", Rig: "web", Stage: "build"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	m, err := y.api.FlowMap(y.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Stages[0].Who != store.WhoPeer || m.Stages[0].Yard != "north" || !strings.Contains(m.Text(false), "> build    peer north") {
		t.Fatalf("the map = %+v\n%s", m.Stages[0], m.Text(false))
	}
}
