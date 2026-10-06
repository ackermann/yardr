package store_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// incoming is a flow for beads from another yard: a first stage a person or
// a builder works, and two ways to end.
const incoming = "arrived -> build | returned on refused\nbuild -> returned on done | returned on failed | gave-up\nreturned terminal\ngave-up terminal\n"

// newBeadYard is a yard linked with north that takes its beads into the rig
// web, pinned to the flow incoming. jo is its operator and a user.
func newBeadYard(t *testing.T, auto bool) *peerYard {
	t.Helper()
	y := newPeerYard(t, "http://north.test")
	if err := y.api.PutRig(y.ctx, store.Rig{Name: "web", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putNamed(t, y.api, "incoming", incoming)
	if _, err := y.api.AddUser(y.ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"bead"}})
	if _, err := y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Rig: "web", Flow: "incoming", Auto: auto}, "jo"); err != nil {
		t.Fatal(err)
	}
	return y
}

// beadMsg is a bead message as north would send it: its bead theirs, which
// waits in its stage build.
func beadMsg(id, theirs string, change func(map[string]any)) store.PeerMessage {
	b := map[string]any{"v": 1, "yardr": "v0.9.1", "bead": theirs, "stage": "build", "title": "Add the export", "body": "As a CSV.",
		"type": "feature", "priority": 1, "outcomes": []string{"done", "failed", "refused"}}
	if change != nil {
		change(b)
	}
	body, _ := json.Marshal(b)
	return store.PeerMessage{ID: id, Kind: store.PeerBead, Body: body}
}

func (y *peerYard) take(t *testing.T, ms ...store.PeerMessage) []store.PeerTaken {
	t.Helper()
	got, err := y.own.PeerReceive(y.ctx, "north", y.key, ms)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// arrived is the one bead that came from north, as GetBead returns it.
func (y *peerYard) arrived(t *testing.T) *store.Bead {
	t.Helper()
	bs, err := y.api.ListBeads(y.ctx, store.BeadFilter{All: true, Rig: "web"})
	if err != nil || len(bs) != 1 {
		t.Fatalf("beads in web = %+v, %v; want one", bs, err)
	}
	b, err := y.api.GetBead(y.ctx, bs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (y *peerYard) beads(t *testing.T) int {
	t.Helper()
	bs, err := y.api.ListBeads(y.ctx, store.BeadFilter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	return len(bs)
}

// A bead a linked yard sends becomes a bead of this yard: in the intake's
// rig, pinned to its flow, in that flow's first stage, labelled with the
// peer, and held. What came with it is kept as data: its notes under the
// peer's names, its links as relates. The same message a second time makes
// no second bead.
func TestPeerBeadArrivesHeld(t *testing.T) {
	y := newBeadYard(t, false)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	m := beadMsg("b1", "web-k3f9", func(b map[string]any) {
		b["title"] = "Add the export\nheld: no"
		b["unknown_field"] = map[string]any{"x": 1}
		b["notes"] = []map[string]any{
			{"author": "mayor", "at": at.Add(time.Hour), "text": "second"},
			{"author": "builder one", "at": at, "text": "first"},
		}
		b["notes_left_out"] = 3
		b["links"] = []map[string]any{
			{"kind": "tracks", "source": "linear", "url": "https://linear.app/x/issue/YRD-1"},
			{"kind": "relates", "source": "no source", "url": "https://example.com/1"},
			{"kind": "relates", "source": "github", "url": "not a url"},
		}
	})
	if got := y.take(t, m); got[0].Refused != "" || got[0].Duplicate {
		t.Fatalf("a bead with an intake = %+v", got)
	}
	b := y.arrived(t)
	if b.Title != "Add the export held: no" || b.Body != "As a CSV." || b.Type != "feature" || b.Priority != 1 || b.Stage != "arrived" ||
		!b.Hold || b.Status != "open" || *b.Rig != "web" || store.PinnedFlow(b) != "incoming" || !slices.Equal(b.Labels, []string{"from-north"}) ||
		*b.CreatedBy != "peer:north" || !strings.HasPrefix(b.ID, "web-") {
		t.Fatalf("the bead that arrived = %+v", b)
	}
	if ready, _ := y.api.Ready(y.ctx); len(ready) != 0 {
		t.Fatalf("a bead that arrived held is ready: %+v", ready)
	}
	p := b.Peer
	if p == nil || p.Peer != "north" || p.Direction != "in" || p.Bead != "web-k3f9" || p.Stage != "build" || p.Message != "b1" || !p.Held ||
		!slices.Equal(p.Outcomes, []string{"done", "failed", "refused"}) || p.Line() != "from web-k3f9@north, for its stage build" ||
		!strings.Contains(p.Marking(), "came from the yard north") || !strings.Contains(p.Marking(), "not instructions about this yard") {
		t.Fatalf("what the bead says of its peer = %+v", p)
	}
	raw, _ := json.Marshal(b)
	var asJSON struct {
		Peer struct{ Peer, Bead, Stage, Direction string } `json:"peer"`
	}
	if json.Unmarshal(raw, &asJSON) != nil || asJSON.Peer.Peer != "north" || asJSON.Peer.Bead != "web-k3f9" || asJSON.Peer.Stage != "build" || asJSON.Peer.Direction != "in" {
		t.Fatalf("the bead as JSON = %s", raw)
	}
	// Listed, it says the same: a job's bead is read from a listing.
	if bs, _ := y.api.ListBeads(y.ctx, store.BeadFilter{Rig: "web"}); bs[0].Peer == nil || bs[0].Peer.Bead != "web-k3f9" {
		t.Fatalf("the bead as listed = %+v", bs[0].Peer)
	}
	notes, _ := y.api.Notes(y.ctx, b.ID)
	if len(notes) != 2 || notes[0].Text != "first" || notes[0].Author != "someone@north" || !notes[0].At.Equal(at) ||
		notes[1].Text != "second" || notes[1].Author != "mayor@north" {
		t.Fatalf("the notes that came with it = %+v", notes)
	}
	links, _ := y.api.Links(y.ctx, b.ID)
	if len(links) != 1 || links[0].Kind != store.LinkRelates || links[0].Source != "linear" {
		t.Fatalf("its links = %+v; want the one this store takes, as relates", links)
	}
	ev := peerEvents(t, y.api, "peer_bead_arrived")
	if len(ev) != 1 || ev[0].Actor != "peer:north" || *ev[0].Bead != b.ID || ev[0].Data["peer"] != "north" || ev[0].Data["id"] != "b1" ||
		ev[0].Data["from"] != "web-k3f9" || ev[0].Data["stage"] != "build" || ev[0].Data["held"] != true || ev[0].Data["way"] != "push" ||
		ev[0].Data["rig"] != "web" || ev[0].Data["flow"] != "incoming" || ev[0].Data["notes_left_out"] != float64(3) {
		t.Fatalf("peer_bead_arrived = %+v", ev)
	}
	// A peer's note is no event of this yard's: nothing is matched against it.
	if n := peerEvents(t, y.api, "noted"); len(n) != 0 {
		t.Fatalf("the peer's notes were logged as noted: %+v", n)
	}
	noBodies(t, y.api)
	peer, _ := y.api.GetPeer(y.ctx, "north")
	if peer.Build != "v0.9.1" || peer.Beads != 1 || peer.Intake == nil || peer.Intake.Rig != "web" {
		t.Fatalf("the peer after its bead = %+v", peer)
	}

	// The same message again: dropped by its id.
	if got := y.take(t, m); !got[0].Duplicate || y.beads(t) != 1 {
		t.Fatalf("the same message twice = %+v, %d beads", got, y.beads(t))
	}
}

// With --auto on the intake a bead arrives ready, and a type that means
// something about branches here arrives as a task.
func TestPeerBeadArrivesReadyWithAuto(t *testing.T) {
	y := newBeadYard(t, true)
	if got := y.take(t, beadMsg("b1", "web-k3f9", func(b map[string]any) { b["type"], b["priority"] = "epic", 99 })); got[0].Refused != "" {
		t.Fatalf("a bead with an auto intake = %+v", got)
	}
	b := y.arrived(t)
	if b.Hold || b.Peer.Held || b.Type != "task" || b.Priority != 4 {
		t.Fatalf("the bead that arrived = %+v, %+v", b, b.Peer)
	}
	ready, _ := y.api.Ready(y.ctx)
	if len(ready) != 1 || ready[0].ID != b.ID {
		t.Fatalf("ready = %+v", ready)
	}
	if ev := peerEvents(t, y.api, "peer_bead_arrived"); len(ev) != 1 || ev[0].Data["held"] != false {
		t.Fatalf("peer_bead_arrived = %+v", ev)
	}
}

// Every way a bead is refused creates nothing, tells the sender why, logs
// it once, and leaves the id free: the sender may send it again when the
// reason is gone.
func TestPeerBeadRefusals(t *testing.T) {
	y := newBeadYard(t, false)
	refused := func(want string, m store.PeerMessage) store.Event {
		t.Helper()
		before, was := y.beads(t), len(peerEvents(t, y.api, "peer_message_refused"))
		got := y.take(t, m)
		ev := peerEvents(t, y.api, "peer_message_refused")
		if got[0].Refused != want || got[0].Duplicate || y.beads(t) != before || len(ev) != was+1 {
			t.Fatalf("%s: taken = %+v, beads %d -> %d, refusals %d -> %d", want, got, before, y.beads(t), was, len(ev))
		}
		e := ev[len(ev)-1]
		if e.Data["reason"] != want || e.Data["direction"] != "in" || e.Data["kind"] != "bead" || e.Data["id"] != m.ID || e.Actor != "peer:north" {
			t.Fatalf("%s: peer_message_refused = %+v", want, e)
		}
		return e
	}

	// The link does not take beads, intake or not.
	y.allow(t, store.PeerAllow{NoReceive: []string{"bead"}})
	refused("kind_not_allowed", beadMsg("r0", "web-1", nil))
	y.allow(t, store.PeerAllow{Receive: []string{"bead"}})

	// No intake.
	if _, err := y.api.SetPeerIntake(y.ctx, "north", nil, "jo"); err != nil {
		t.Fatal(err)
	}
	if e := refused("no_intake", beadMsg("r1", "web-1", nil)); e.Data["bead"] != "web-1" {
		t.Fatalf("no_intake names the bead: %+v", e)
	}
	if _, err := y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Rig: "web", Flow: "incoming", Limit: 2}, "jo"); err != nil {
		t.Fatal(err)
	}

	// A version this build does not know, and none at all.
	if e := refused("version", beadMsg("r2", "web-1", func(b map[string]any) { b["v"] = 2 })); e.Data["v"] != float64(2) || e.Data["known"] != float64(1) {
		t.Fatalf("version says both: %+v", e)
	}
	refused("version", beadMsg("r3", "web-1", func(b map[string]any) { delete(b, "v") }))

	// Over a limit: the title, the body, one note, the notes together.
	big := func(n int) string { return strings.Repeat("x", n) }
	note := func(n int) map[string]any { return map[string]any{"author": "a", "at": time.Now(), "text": big(n)} }
	refused("too_large", beadMsg("r4", "web-1", func(b map[string]any) { b["title"] = big(store.MaxPeerBeadTitle + 1) }))
	refused("too_large", beadMsg("r5", "web-1", func(b map[string]any) { b["body"] = big(store.MaxPeerBeadBody + 1) }))
	refused("too_large", beadMsg("r6", "web-1", func(b map[string]any) { b["notes"] = []map[string]any{note(store.MaxPeerNoteBytes + 1)} }))
	refused("too_large", beadMsg("r7", "web-1", func(b map[string]any) {
		var ns []map[string]any
		for range store.MaxPeerNotes/store.MaxPeerNoteBytes + 1 {
			ns = append(ns, note(store.MaxPeerNoteBytes))
		}
		b["notes"] = ns
	}))

	// What is no bead: no title, a handle that is none, a body that is no object.
	refused("not_a_bead", beadMsg("r8", "web-1", func(b map[string]any) { b["title"] = " \n " }))
	refused("not_a_bead", beadMsg("r9", "web 1", nil))
	refused("not_a_bead", store.PeerMessage{ID: "r10", Kind: "bead", Body: json.RawMessage(`[1]`)})

	// At the limits it is taken, with an id that was refused before.
	if got := y.take(t, beadMsg("r4", "web-1", func(b map[string]any) {
		b["title"], b["body"], b["notes"] = big(store.MaxPeerBeadTitle), big(store.MaxPeerBeadBody), []map[string]any{note(store.MaxPeerNoteBytes)}
	})); got[0].Refused != "" || y.beads(t) != 1 {
		t.Fatalf("a bead at the limits = %+v, %d beads", got, y.beads(t))
	}

	// The sender's bead is here already, open: under any message id.
	first := y.arrived(t)
	if e := refused("already_here", beadMsg("r11", "web-1", nil)); e.Data["here"] != first.ID {
		t.Fatalf("already_here names the bead it has: %+v", e)
	}

	// As many open beads as the intake keeps.
	if got := y.take(t, beadMsg("r12", "web-2", nil)); got[0].Refused != "" {
		t.Fatalf("a second bead under the limit = %+v", got)
	}
	if e := refused("busy", beadMsg("r13", "web-3", nil)); e.Data["open"] != float64(2) || e.Data["limit"] != float64(2) {
		t.Fatalf("busy says how many: %+v", e)
	}
	// One ends: there is room again, and the sender's bead that was here
	// may come again.
	if _, err := y.api.CloseBead(y.ctx, first.ID, "not ours", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if got := y.take(t, beadMsg("r13", "web-1", nil)); got[0].Refused != "" || y.beads(t) != 3 {
		t.Fatalf("after one ended = %+v, %d beads", got, y.beads(t))
	}

	// An intake whose flow has gone refuses, and says so to the operator.
	for _, id := range []string{"web-1", "web-2"} {
		for _, b := range mustList(t, y) {
			if b.Peer != nil && b.Peer.Bead == id && b.Status == "open" {
				if _, err := y.api.CloseBead(y.ctx, b.ID, "done with", store.Expect{}, "jo"); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := y.api.DeleteFlow(y.ctx, "incoming", "jo"); err != nil {
		t.Fatal(err)
	}
	if e := refused("no_intake", beadMsg("r14", "web-9", nil)); !strings.Contains(fmt.Sprint(e.Data["detail"]), "incoming") {
		t.Fatalf("no_intake for a flow that is gone = %+v", e)
	}
	noBodies(t, y.api)
}

func mustList(t *testing.T, y *peerYard) []store.Bead {
	t.Helper()
	bs, err := y.api.ListBeads(y.ctx, store.BeadFilter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	return bs
}

// An intake names a rig of the yard and a flow that resolves for it, with a
// stage a bead can start in. It is shown with the peer, and removed.
func TestPeerIntake(t *testing.T) {
	y := newBeadYard(t, false)
	putNamed(t, y.api, "ends", "over terminal\n")
	for want, in := range map[string]store.PeerIntake{
		"rig nowhere": {Rig: "nowhere", Flow: "incoming"},
		// Not "the bead pins": no bead does yet (found in the trial).
		"the intake names flow nothing: not found": {Rig: "web", Flow: "nothing"},
		"terminal":               {Rig: "web", Flow: "ends"},
		"names a rig and a flow": {Rig: "web"},
		"limit":                  {Rig: "web", Flow: "incoming", Limit: -1},
	} {
		if _, err := y.api.SetPeerIntake(y.ctx, "north", &in, "jo"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("SetPeerIntake(%+v) = %v, want %q", in, err, want)
		}
	}
	if _, err := y.api.SetPeerIntake(y.ctx, "nobody", &store.PeerIntake{Rig: "web", Flow: "incoming"}, "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an intake for no peer = %v", err)
	}
	p, _ := y.api.GetPeer(y.ctx, "north")
	if p.Intake == nil || p.Intake.Rig != "web" || p.Intake.Flow != "incoming" || p.Intake.Auto || p.Intake.Most() != store.PeerIntakeLimit {
		t.Fatalf("the intake a refused one left = %+v", p.Intake)
	}
	p, err := y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Rig: "web", Flow: "incoming", Auto: true, Limit: 3}, "jo")
	if err != nil || !p.Intake.Auto || p.Intake.Most() != 3 {
		t.Fatalf("SetPeerIntake = %+v, %v", p, err)
	}
	ev := peerEvents(t, y.api, "peer_intake")
	if last := ev[len(ev)-1]; last.Data["peer"] != "north" || last.Data["rig"] != "web" || last.Data["flow"] != "incoming" || last.Data["auto"] != true || last.Data["limit"] != float64(3) {
		t.Fatalf("peer_intake = %+v", last)
	}
	// What the link carries is changed without losing it.
	if p := y.allow(t, store.PeerAllow{Send: []string{"ping"}}); p.Intake == nil || !p.Intake.Auto {
		t.Fatalf("the intake after peer allow = %+v", p.Intake)
	}
	if p, err := y.api.SetPeerIntake(y.ctx, "north", nil, "clerk"); err != nil || p.Intake != nil {
		t.Fatalf("removing the intake = %+v, %v", p, err)
	}
	ev = peerEvents(t, y.api, "peer_intake")
	if last := ev[len(ev)-1]; last.Data["none"] != true || last.Actor != "clerk" {
		t.Fatalf("peer_intake for none = %+v", last)
	}
}

// One word allows a bead and its answer, each against the other: a yard
// that takes beads may send returns and takes none, and nothing on the
// socket sends either kind.
func TestPeerBeadKinds(t *testing.T) {
	y := newBeadYard(t, false)
	ret := store.PeerMessage{ID: "x1", Kind: store.PeerBeadReturn, Re: "b0", Body: json.RawMessage(`{"v":1,"bead":"n-1","outcome":"done"}`)}
	if got := y.take(t, ret); got[0].Refused != "kind_not_allowed" {
		t.Fatalf("a return where no bead was sent = %+v", got)
	}
	y.allow(t, store.PeerAllow{Send: []string{"bead"}})
	if got := y.take(t, ret); got[0].Refused != "" || y.beads(t) != 0 {
		t.Fatalf("a return over a link that sends beads = %+v", got)
	}
	for _, kind := range []string{"bead", "bead-return"} {
		if _, err := y.api.PeerSend(y.ctx, "north", kind, json.RawMessage(`{"v":1}`), "jo"); err == nil || !strings.Contains(err.Error(), "the yard's own to send") {
			t.Errorf("PeerSend of kind %s = %v", kind, err)
		}
	}
	if _, err := y.api.AllowPeer(y.ctx, "north", store.PeerAllow{Receive: []string{"bead-return"}}, "jo"); err == nil {
		t.Fatal("bead-return was allowed by its own name")
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Waiting != 0 {
		t.Fatalf("something waits for north: %+v", p)
	}
}

// A bead that arrived held is released by a user, or by a resident the
// operator named for that peer: not by a bead's agent, not by any other
// resident, not by a name the yard does not know. The release is logged,
// and holds: held again later, the bead is anyone's to unhold.
func TestPeerBeadRelease(t *testing.T) {
	y := newBeadYard(t, false)
	south, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "south.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "south", Key: yardkey.Public(south)}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.resident(t, "mayor", "")
	for name, release := range map[string]string{"clerk": "south", "warden": "north,south"} {
		if _, err := y.api.PutResident(y.ctx, store.Resident{Name: name, Runner: "herdr", Config: map[string]any{store.ReleasePeers: release}}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	y.take(t, beadMsg("b1", "web-k3f9", nil))
	b := y.arrived(t)

	// An agent's session: one the yard started for another bead.
	if err := y.api.PutGroup(y.ctx, store.Group{Name: "builders", Runner: "herdr"}); err != nil {
		t.Fatal(err)
	}
	other, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "its own"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := y.api.Claim(y.ctx, other.ID, "builders", other.Stage, "yard")
	if err != nil {
		t.Fatal(err)
	}
	for who, try := range map[string]struct {
		exp   store.Expect
		actor string
	}{
		"the session " + sess.ID + " of a bead's agent": {store.Expect{Session: sess.ID}, "jo"},
		"the resident mayor, which is not":              {store.Expect{}, "mayor"},
		"the resident clerk, which is not":              {store.Expect{}, "clerk"},
		"this is stranger, who is no user":              {store.Expect{}, "stranger"},
	} {
		_, err := y.api.SetHold(y.ctx, b.ID, false, try.exp, try.actor)
		if err == nil || !strings.Contains(err.Error(), who) || !strings.Contains(err.Error(), "arrived held from the yard north") ||
			!strings.Contains(err.Error(), "yardr resident set <resident> release=north") {
			t.Errorf("unhold as %s = %v", who, err)
		}
	}
	if got, _ := y.api.GetBead(y.ctx, b.ID); !got.Hold || !got.Peer.Held || len(peerEvents(t, y.api, "unheld")) != 0 {
		t.Fatalf("a refused release changed the bead: %+v", got)
	}

	// The resident allowed north.
	if _, err := y.api.SetHold(y.ctx, b.ID, false, store.Expect{}, "warden"); err != nil {
		t.Fatalf("the resident allowed north: %v", err)
	}
	got, _ := y.api.GetBead(y.ctx, b.ID)
	if got.Hold || got.Peer.Held || got.Peer.ReleasedBy != "warden" || got.Peer.ReleasedAt == nil {
		t.Fatalf("the released bead = %+v, %+v", got, got.Peer)
	}
	ev := peerEvents(t, y.api, "peer_bead_released")
	if len(ev) != 1 || *ev[0].Bead != b.ID || ev[0].Actor != "warden" || ev[0].Data["peer"] != "north" || ev[0].Data["by"] != "warden" {
		t.Fatalf("peer_bead_released = %+v", ev)
	}
	// Held again by whoever works it, it is a held bead like any other.
	if _, err := y.api.SetHold(y.ctx, b.ID, true, store.Expect{}, "mayor"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.SetHold(y.ctx, b.ID, false, store.Expect{}, "mayor"); err != nil {
		t.Fatalf("unhold of a bead released before: %v", err)
	}
	if len(peerEvents(t, y.api, "peer_bead_released")) != 1 {
		t.Fatal("a later unhold was logged as a release")
	}

	// A user, on a second bead.
	y.take(t, beadMsg("b2", "web-2", nil))
	var second store.Bead
	for _, c := range mustList(t, y) {
		if c.Peer != nil && c.Peer.Bead == "web-2" {
			second = c
		}
	}
	if _, err := y.api.SetHold(y.ctx, second.ID, false, store.Expect{}, "jo"); err != nil {
		t.Fatalf("a user's release: %v", err)
	}
	if got, _ := y.api.GetBead(y.ctx, second.ID); got.Hold || got.Peer.ReleasedBy != "jo" {
		t.Fatalf("the bead a user released = %+v", got.Peer)
	}
	// A bead that is no peer's is unheld by anyone, as before.
	if _, err := y.api.SetHold(y.ctx, other.ID, true, store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.SetHold(y.ctx, other.ID, false, store.Expect{}, "stranger"); err != nil {
		t.Fatalf("unhold of a bead of this yard's own: %v", err)
	}
}

// returned is the one return that waits for north.
func (y *peerYard) returned(t *testing.T) (store.PeerOut, store.PeerReturnMessage) {
	t.Helper()
	out, err := y.api.PeerOutbox(y.ctx, "north", 0)
	if err != nil || len(out) != 1 || out[0].Kind != store.PeerBeadReturn {
		t.Fatalf("the outbox = %+v, %v; want one return", out, err)
	}
	var r store.PeerReturnMessage
	if err := json.Unmarshal(out[0].Body, &r); err != nil {
		t.Fatal(err)
	}
	return out[0], r
}

// When a bead from a peer enters a terminal stage, its return is queued in
// that write: the outcome of the move, and the notes written here, not
// those that came with it. Once.
func TestPeerBeadReturn(t *testing.T) {
	y := newBeadYard(t, true)
	y.take(t, beadMsg("b1", "web-k3f9", func(b map[string]any) {
		b["notes"] = []map[string]any{{"author": "mayor", "at": time.Now().Add(-time.Hour), "text": "theirs"}}
	}))
	b := y.arrived(t)
	if _, err := y.api.Advance(y.ctx, b.ID, "build", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.api.AddNote(y.ctx, b.ID, "built it: see export.go", "builder"); err != nil {
		t.Fatal(err)
	}
	if out, _ := y.api.PeerOutbox(y.ctx, "north", 0); len(out) != 0 {
		t.Fatalf("something returned before the bead ended: %+v", out)
	}
	long := strings.Repeat("é", store.MaxPeerNoteBytes)
	if err := y.api.AddNote(y.ctx, b.ID, long, "builder"); err != nil {
		t.Fatal(err)
	}
	done, err := y.api.Done(y.ctx, b.ID, "failed", "the tests are red", store.Expect{}, "builder")
	if err != nil || done.Status != "closed" || done.Stage != "returned" {
		t.Fatalf("Done = %+v, %v", done, err)
	}
	m, r := y.returned(t)
	if m.Re != "b1" || r.V != 1 || r.Bead != b.ID || r.Outcome != "failed" || r.NotesLeftOut != 0 || len(r.Notes) != 3 ||
		r.Notes[0].Text != "built it: see export.go" || r.Notes[0].Author != "builder" || r.Notes[2].Text != "the tests are red" {
		t.Fatalf("the return = %+v re %q", r, m.Re)
	}
	if cut := r.Notes[1].Text; len(cut) > store.MaxPeerNoteBytes || !strings.HasSuffix(cut, fmt.Sprintf("[cut: %d bytes in all]", len(long))) || !strings.HasPrefix(cut, "éé") {
		t.Fatalf("a note longer than crosses = %d bytes, ...%q", len(cut), cut[max(0, len(cut)-40):])
	}
	ev := peerEvents(t, y.api, "peer_bead_returned")
	if len(ev) != 1 || *ev[0].Bead != b.ID || ev[0].Actor != "builder" || ev[0].Data["peer"] != "north" || ev[0].Data["outcome"] != "failed" ||
		ev[0].Data["id"] != m.ID || ev[0].Data["re"] != "b1" || ev[0].Data["to"] != "web-k3f9" || ev[0].Data["notes"] != float64(3) {
		t.Fatalf("peer_bead_returned = %+v", ev)
	}
	got, _ := y.api.GetBead(y.ctx, b.ID)
	if got.Peer.Outcome != "failed" || got.Peer.ReturnedAt == nil {
		t.Fatalf("the returned bead says = %+v", got.Peer)
	}
	noBodies(t, y.api)

	// Opened and ended again, it is not returned a second time.
	if _, err := y.api.Reopen(y.ctx, b.ID, store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.CloseBead(y.ctx, b.ID, "again", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if out, _ := y.api.PeerOutbox(y.ctx, "north", 0); len(out) != 1 || len(peerEvents(t, y.api, "peer_bead_returned")) != 1 {
		t.Fatalf("a bead returned twice: %+v", out)
	}
}

// A move to a terminal stage that names no outcome is told by the stage,
// and a bare close returns refused with its reason, also for a bead still
// held.
func TestPeerBeadReturnWithoutOutcome(t *testing.T) {
	y := newBeadYard(t, false)
	y.take(t, beadMsg("b1", "web-1", nil))
	b := y.arrived(t)
	if _, err := y.api.CloseBead(y.ctx, b.ID, "not work for this yard", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	m, r := y.returned(t)
	if m.Re != "b1" || r.Outcome != store.OutcomeRefused || len(r.Notes) != 1 || r.Notes[0].Text != "not work for this yard" || r.Notes[0].Author != "jo" {
		t.Fatalf("the return of a bare close = %+v", r)
	}
	if err := y.api.PeerDelivered(y.ctx, "north", m.Seq); err != nil {
		t.Fatal(err)
	}

	y.take(t, beadMsg("b2", "web-2", nil))
	var second store.Bead
	for _, c := range mustList(t, y) {
		if c.Peer != nil && c.Peer.Bead == "web-2" {
			second = c
		}
	}
	for _, to := range []string{"build", "gave-up"} {
		if _, err := y.api.Advance(y.ctx, second.ID, to, store.Expect{}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if m, r := y.returned(t); m.Re != "b2" || r.Outcome != "gave-up" || len(r.Notes) != 0 {
		t.Fatalf("the return of a move with no outcome = %+v", r)
	}
}

// More notes than cross a link: the newest are returned, the older counted.
func TestPeerBeadReturnLeavesOutOldNotes(t *testing.T) {
	y := newBeadYard(t, true)
	y.take(t, beadMsg("b1", "web-1", nil))
	b := y.arrived(t)
	n := store.MaxPeerNotes/store.MaxPeerNoteBytes + 2
	for i := range n {
		if err := y.api.AddNote(y.ctx, b.ID, fmt.Sprintf("%02d", i)+strings.Repeat("\x01", store.MaxPeerNoteBytes-2), "builder"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := y.api.Done(y.ctx, b.ID, "refused", "", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	m, r := y.returned(t)
	if len(m.Body) > store.MaxPeerBody || r.NotesLeftOut == 0 || r.NotesLeftOut+len(r.Notes) != n ||
		!strings.HasPrefix(r.Notes[len(r.Notes)-1].Text, fmt.Sprintf("%02d", n-1)) || !strings.HasPrefix(r.Notes[0].Text, fmt.Sprintf("%02d", r.NotesLeftOut)) {
		t.Fatalf("the return = %d bytes, %d notes, %d left out", len(m.Body), len(r.Notes), r.NotesLeftOut)
	}
	if ev := peerEvents(t, y.api, "peer_bead_returned"); ev[0].Data["notes_left_out"] != float64(r.NotesLeftOut) {
		t.Fatalf("peer_bead_returned = %+v", ev)
	}
}

// A link that stopped taking the peer's beads carries no return: the bead
// ends here, the refusal is logged, and it is not marked returned.
func TestPeerBeadReturnOverAClosedLink(t *testing.T) {
	y := newBeadYard(t, true)
	y.take(t, beadMsg("b1", "web-1", nil))
	b := y.arrived(t)
	y.allow(t, store.PeerAllow{NoReceive: []string{"bead"}})
	if _, err := y.api.CloseBead(y.ctx, b.ID, "no", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	ref := peerEvents(t, y.api, "peer_message_refused")
	if out, _ := y.api.PeerOutbox(y.ctx, "north", 0); len(out) != 0 || len(ref) != 1 || ref[0].Data["direction"] != "out" || ref[0].Data["kind"] != "bead-return" {
		t.Fatalf("outbox %+v, refused %+v", out, ref)
	}
	if got, _ := y.api.GetBead(y.ctx, b.ID); got.Peer.ReturnedAt != nil || len(peerEvents(t, y.api, "peer_bead_returned")) != 0 {
		t.Fatalf("a return that did not leave is recorded: %+v", got.Peer)
	}
}

// The record names the peer by its key: after a rename the bead's line,
// its label and a resident's grant to release follow, and the return goes
// to the peer under its new name. After the link is removed the bead says
// so, nobody's grant is left, and nothing is returned.
func TestPeerBeadRenameAndRemove(t *testing.T) {
	y := newBeadYard(t, false)
	if _, err := y.api.PutResident(y.ctx, store.Resident{Name: "warden", Runner: "herdr", Config: map[string]any{store.ReleasePeers: "north", store.MailPeers: "north"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.take(t, beadMsg("b1", "web-1", nil), beadMsg("b2", "web-2", nil))
	if _, err := y.api.RenamePeer(y.ctx, "north", "nord", "jo"); err != nil {
		t.Fatal(err)
	}
	var one, two *store.Bead
	for _, c := range mustList(t, y) {
		b, _ := y.api.GetBead(y.ctx, c.ID)
		if b.Peer.Bead == "web-1" {
			one = b
		} else {
			two = b
		}
	}
	if one.Peer.Peer != "nord" || one.Peer.Line() != "from web-1@nord, for its stage build" || !slices.Equal(one.Labels, []string{"from-nord"}) {
		t.Fatalf("the bead after the rename = %+v %v", one.Peer, one.Labels)
	}
	r, _ := y.api.GetResident(y.ctx, "warden")
	if !slices.Equal(r.MayRelease(), []string{"nord"}) || !slices.Equal(r.MayMail(), []string{"nord"}) {
		t.Fatalf("the grants after the rename = release %v, mail %v", r.MayRelease(), r.MayMail())
	}
	if ev := peerEvents(t, y.api, "peer_renamed"); !slices.Equal(toStrings(ev[0].Data["releasers"]), []string{"warden"}) {
		t.Fatalf("peer_renamed = %+v", ev[0].Data)
	}
	if _, err := y.api.SetHold(y.ctx, one.ID, false, store.Expect{}, "warden"); err != nil {
		t.Fatalf("release under the new name: %v", err)
	}
	// The next bead is labelled with the name the peer has now.
	if got, err := y.own.PeerReceive(y.ctx, "nord", y.key, []store.PeerMessage{beadMsg("b3", "web-3", nil)}); err != nil || got[0].Refused != "" {
		t.Fatalf("a bead after the rename = %+v, %v", got, err)
	}
	if _, err := y.api.CloseBead(y.ctx, one.ID, "no", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if out, _ := y.api.PeerOutbox(y.ctx, "nord", 0); len(out) != 1 || out[0].Re != "b1" || out[0].Peer != "nord" {
		t.Fatalf("the return after the rename = %+v", out)
	}

	if err := y.api.DeletePeer(y.ctx, "nord", "jo"); err != nil {
		t.Fatal(err)
	}
	got, _ := y.api.GetBead(y.ctx, two.ID)
	if got.Peer.Peer != "" || !got.Peer.Unlinked || got.Peer.Was != "nord" ||
		got.Peer.Line() != "from web-2@the yard that was nord (link removed), for its stage build" ||
		!strings.Contains(got.Peer.Marking(), "the yard that was nord (link removed)") {
		t.Fatalf("the bead after the link is removed = %+v: %s", got.Peer, got.Peer.Line())
	}
	if ev := peerEvents(t, y.api, "peer_removed"); ev[0].Data["beads"] != float64(3) || !slices.Equal(toStrings(ev[0].Data["releasers"]), []string{"warden"}) {
		t.Fatalf("peer_removed = %+v", ev[0].Data)
	}
	if r, _ := y.api.GetResident(y.ctx, "warden"); len(r.MayRelease()) != 0 || len(r.MayMail()) != 0 {
		t.Fatalf("grants for a link that is removed: %+v", r.Config)
	}
	// Still held as it arrived, and only a user's to release now.
	if _, err := y.api.SetHold(y.ctx, two.ID, false, store.Expect{}, "warden"); err == nil || !strings.Contains(err.Error(), "the resident warden, which is not") {
		t.Fatalf("release of a removed link's bead by a resident = %v", err)
	}
	if _, err := y.api.SetHold(y.ctx, two.ID, false, store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	// Nothing is returned, and nothing fails for it.
	returned := len(peerEvents(t, y.api, "peer_bead_returned"))
	if _, err := y.api.CloseBead(y.ctx, two.ID, "no", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if len(peerEvents(t, y.api, "peer_bead_returned")) != returned {
		t.Fatal("a bead of a removed link was returned")
	}

	// The same yard added again is a new link: none of the old one's beads
	// count against it, and its bead that was here may come again.
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "north", Key: y.key}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"bead"}})
	if _, err := y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Rig: "web", Flow: "incoming", Limit: 1}, "jo"); err != nil {
		t.Fatal(err)
	}
	if got := y.take(t, beadMsg("b9", "web-3", nil)); got[0].Refused != "" {
		t.Fatalf("a bead over the new link = %+v", got)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Beads != 1 {
		t.Fatalf("open beads of the new link = %d", p.Beads)
	}
}

func toStrings(v any) []string {
	var out []string
	list, _ := v.([]any)
	for _, e := range list {
		s, _ := e.(string)
		out = append(out, s)
	}
	return out
}

// A bead handed over to a pull is taken as one posted is, and a refusal is
// kept for the next question.
func TestPeerBeadPulled(t *testing.T) {
	y := newBeadYard(t, false)
	page := func(ms ...store.PeerMessage) store.PeerBatch {
		body, _ := json.Marshal(store.PeerPage{Messages: ms})
		return store.PeerBatch{RequestSig: "req-1", Signature: yardkey.SignResponse(y.north, 200, "req-1", body), Body: body}
	}
	a, b := beadMsg("p1", "web-1", nil), beadMsg("p2", "web-1", nil)
	a.Seq, b.Seq = 4, 5
	res, err := y.api.PeerPulled(y.ctx, "north", page(a, b))
	if err != nil || len(res.Taken) != 2 || res.Taken[0].Refused != "" || res.Taken[1].Refused != "already_here" {
		t.Fatalf("PeerPulled = %+v, %v", res, err)
	}
	if got := y.arrived(t); got.Peer.Message != "p1" {
		t.Fatalf("the bead that was pulled = %+v", got.Peer)
	}
	if ev := peerEvents(t, y.api, "peer_bead_arrived"); len(ev) != 1 || ev[0].Data["way"] != "pull" {
		t.Fatalf("peer_bead_arrived = %+v", ev)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Refused[5] != "already_here" || p.Pulled != 5 {
		t.Fatalf("the peer after the pull = %+v", p)
	}
}

// A release asked from a resident's pane is that resident's, whatever
// actor the command names: a resident talked into releasing a bead cannot
// do it as a user by saying it is one. The pane is the shell the runner
// recorded for the resident's tab.
func TestPeerBeadReleaseFromAResidentsPane(t *testing.T) {
	y := newBeadYard(t, false)
	y.resident(t, "mayor", "")
	y.take(t, beadMsg("b1", "web-1", nil))
	b := y.arrived(t)
	me := os.Getpid()
	st, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := y.api.ResidentStarted(y.ctx, "mayor", map[string]any{"shell_pid": me, "shell_start": st.Start}); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(y.ctx, me)
	if _, err := y.own.SetHold(pane, b.ID, false, store.Expect{}, "jo"); err == nil || !strings.Contains(err.Error(), "this is the resident mayor, which is not") {
		t.Fatalf("a release from the mayor's pane as jo = %v", err)
	}
	// The shell is gone, and its pid someone else's: jo is jo again.
	if err := y.api.ResidentStarted(y.ctx, "mayor", map[string]any{"shell_pid": me, "shell_start": st.Start + 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := y.own.SetHold(pane, b.ID, false, store.Expect{}, "jo"); err != nil {
		t.Fatalf("a user's release from a pid a resident's shell once had: %v", err)
	}
}

// What a sender is told when its message was refused is said in its own
// words, for each reason a bead is refused for. A return has no kind of its
// own to allow, so its refusal names the bead's (found in the trial: "its
// link does not carry bead-return in" names nothing an operator can allow).
func TestPeerRefusalOfBeads(t *testing.T) {
	for reason, want := range map[string]string{
		"kind_not_allowed": "its link does not carry bead in",
		"no_intake":        "it has no intake for this yard's beads",
		"busy":             "it has as many open beads of this yard as it keeps",
		"already_here":     "it has the bead already, open",
		"too_large":        "the bead is larger than it takes",
		"version":          "it does not know this version of the message",
		"not_a_bead":       "it does not read the bead",
		"made_up":          "it does not say why in words this yard knows",
	} {
		if got := store.PeerRefusal(store.PeerBead, reason); got != want {
			t.Errorf("PeerRefusal(bead, %s) = %q, want %q", reason, got, want)
		}
	}
	if got := store.PeerRefusal(store.PeerBeadReturn, "kind_not_allowed"); got != "its link sends this yard no beads, so it takes no return of one" {
		t.Errorf("PeerRefusal(bead-return, kind_not_allowed) = %q", got)
	}
	if got := store.PeerRefusal(store.PeerMail, "too_large"); got != "the text is larger than it takes" {
		t.Errorf("PeerRefusal(mail, too_large) = %q", got)
	}
}
