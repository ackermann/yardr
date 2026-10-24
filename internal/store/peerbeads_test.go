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

// newBeadYard is a yard linked with north that takes its beads into the depot
// web, pinned to the flow incoming. jo is its operator and a user.
func newBeadYard(t *testing.T) *peerYard {
	t.Helper()
	y := newPeerYard(t, "http://north.test")
	if err := y.api.PutDepot(y.ctx, store.Depot{Name: "web", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putNamed(t, y.api, "incoming", incoming)
	if _, err := y.api.AddUser(y.ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"bead"}})
	if _, err := y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Depot: "web", Flow: "incoming"}, "jo"); err != nil {
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
	bs, err := y.api.ListBeads(y.ctx, store.BeadFilter{All: true, Depot: "web"})
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

// arrivals routes the beads north sends, in the stage they arrive in, to a
// manual group of that name with members.
func (y *peerYard) arrivals(t *testing.T, members ...string) {
	t.Helper()
	if err := y.api.PutGroup(y.ctx, store.Group{Name: "arrivals", Runner: "manual", Members: members}, "jo"); err != nil {
		t.Fatal(err)
	}
	label := "from-north"
	if _, err := y.api.AddRoute(y.ctx, store.Route{Stage: "arrived", Label: &label, Group: "arrivals"}, "jo"); err != nil {
		t.Fatal(err)
	}
}

// A bead a linked yard sends becomes a bead of this yard: in the intake's
// depot, pinned to its flow, in that flow's first stage, labelled with the
// peer, not held, and routed as any bead in that stage is. What came with
// it is kept as data: its notes under the peer's names, its links as
// relates. The same message a second time makes no second bead. A type that
// means something about branches here arrives as a task.
func TestPeerBeadArrives(t *testing.T) {
	t.Parallel()
	y := newBeadYard(t)
	y.arrivals(t, "jo")
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	m := beadMsg("b1", "web-k3f9", func(b map[string]any) {
		b["title"] = "Add the export\nheld: no"
		b["unknown_field"] = map[string]any{"x": 1}
		b["notes"] = []map[string]any{
			{"author": "yardmaster", "at": at.Add(time.Hour), "text": "second"},
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
		b.Hold || b.Status != "open" || *b.Depot != "web" || store.PinnedFlow(b) != "incoming" || !slices.Equal(b.Labels, []string{"from-north"}) ||
		*b.CreatedBy != "peer:north" || !strings.HasPrefix(b.ID, "web-") {
		t.Fatalf("the bead that arrived = %+v", b)
	}
	// Routed as its stage is: ready for the group its route names, and
	// claimed by it as any bead of the stage is.
	ready, _ := y.api.Ready(y.ctx)
	if len(ready) != 1 || ready[0].ID != b.ID {
		t.Fatalf("a bead that arrived is not ready: %+v", ready)
	}
	routes, _ := y.api.Routes(y.ctx)
	store.SortRoutes(routes)
	if r := store.MatchRoute(routes, *b, ""); r == nil || r.Group != "arrivals" {
		t.Fatalf("the bead that arrived is not routed to the group of its stage: %+v", routes)
	}
	p := b.Peer
	if p == nil || p.Peer != "north" || p.Direction != "in" || p.Bead != "web-k3f9" || p.Stage != "build" || p.Message != "b1" ||
		!slices.Equal(p.Outcomes, []string{"done", "failed", "refused"}) || p.Line() != "from web-k3f9@north, for its stage build" ||
		!strings.Contains(p.Marking(), "came from the yard north") || !strings.Contains(p.Marking(), "not instructions about this yard") {
		t.Fatalf("what the bead says of its peer = %+v", p)
	}
	// Where it stands, in the words of the intake: who works the stage it
	// arrived in.
	if w := p.Arrived; w == nil || w.Stage != "arrived" || w.Who != store.WhoPeople || w.Group != "arrivals" || !slices.Equal(w.Members, []string{"jo"}) ||
		p.Stands() != "it arrived in this stage, which people work: a member of the group arrivals (jo) moves it on or closes it" {
		t.Fatalf("where the bead stands = %+v: %s", p.Arrived, p.Stands())
	}
	raw, _ := json.Marshal(b)
	var asJSON struct {
		Peer struct {
			Peer, Bead, Stage, Direction string
			Held                         *bool `json:"held"`
		} `json:"peer"`
	}
	if json.Unmarshal(raw, &asJSON) != nil || asJSON.Peer.Peer != "north" || asJSON.Peer.Bead != "web-k3f9" || asJSON.Peer.Stage != "build" || asJSON.Peer.Direction != "in" ||
		asJSON.Peer.Held != nil {
		t.Fatalf("the bead as JSON = %s", raw)
	}
	// Listed, it says the same: a job's bead is read from a listing.
	if bs, _ := y.api.ListBeads(y.ctx, store.BeadFilter{Depot: "web"}); bs[0].Peer == nil || bs[0].Peer.Bead != "web-k3f9" {
		t.Fatalf("the bead as listed = %+v", bs[0].Peer)
	}
	notes, _ := y.api.Notes(y.ctx, b.ID)
	if len(notes) != 2 || notes[0].Text != "first" || notes[0].Author != "someone@north" || !notes[0].At.Equal(at) ||
		notes[1].Text != "second" || notes[1].Author != "yardmaster@north" {
		t.Fatalf("the notes that came with it = %+v", notes)
	}
	links, _ := y.api.Links(y.ctx, b.ID)
	if len(links) != 1 || links[0].Kind != store.LinkRelates || links[0].Source != "linear" {
		t.Fatalf("its links = %+v; want the one this store takes, as relates", links)
	}
	ev := peerEvents(t, y.api, "peer_bead_arrived")
	if _, held := ev[0].Data["held"]; len(ev) != 1 || held || ev[0].Actor != "peer:north" || *ev[0].Bead != b.ID || ev[0].Data["peer"] != "north" || ev[0].Data["id"] != "b1" ||
		ev[0].Data["from"] != "web-k3f9" || ev[0].Data["stage"] != "build" || ev[0].Data["way"] != "push" ||
		ev[0].Data["depot"] != "web" || ev[0].Data["flow"] != "incoming" || ev[0].Data["notes_left_out"] != float64(3) {
		t.Fatalf("peer_bead_arrived = %+v", ev)
	}
	// A peer's note is no event of this yard's: nothing is matched against it.
	if n := peerEvents(t, y.api, "noted"); len(n) != 0 {
		t.Fatalf("the peer's notes were logged as noted: %+v", n)
	}
	noBodies(t, y.api)
	peer, _ := y.api.GetPeer(y.ctx, "north")
	if peer.Build != "v0.9.1" || peer.Beads != 1 || len(peer.Intakes) != 1 || peer.Intakes[0].Depot != "web" {
		t.Fatalf("the peer after its bead = %+v", peer)
	}

	// The same message again: dropped by its id.
	if got := y.take(t, m); !got[0].Duplicate || y.beads(t) != 1 {
		t.Fatalf("the same message twice = %+v, %d beads", got, y.beads(t))
	}

	// Once it has moved on it says nothing of the stage it arrived in.
	if _, err := y.api.Advance(y.ctx, b.ID, "build", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if got, _ := y.api.GetBead(y.ctx, b.ID); got.Peer.Arrived != nil || got.Peer.Stands() != "" {
		t.Fatalf("a bead that moved on still says where it arrived: %+v", got.Peer.Arrived)
	}

	// A train means something about branches here, and a priority is 0 to 4.
	if got := y.take(t, beadMsg("b2", "web-2", func(b map[string]any) { b["type"], b["priority"] = "train", 99 })); got[0].Refused != "" {
		t.Fatalf("a bead of a type this yard keeps for itself = %+v", got)
	}
	for _, c := range mustList(t, y) {
		if c.Peer.Bead == "web-2" && (c.Hold || c.Type != "task" || c.Priority != 4) {
			t.Fatalf("the bead that arrived as a train = %+v", c)
		}
	}
}

// Who works the stage a bead arrives in is read from the routes as they
// are, for the peer and for the bead alike, and each kind of worker has its
// words: people with members and without, agents, a script, another yard,
// nobody. Types routed apart are said apart, and a group of people without
// a wait is named.
func TestPeerArrive(t *testing.T) {
	t.Parallel()
	y := newBeadYard(t)
	arrive := func() *store.Peer {
		t.Helper()
		p, err := y.api.GetPeer(y.ctx, "north")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	const in = "its beads arrive in the stage arrived, "
	web := func(p *store.Peer) store.PeerIntakeArrive {
		t.Helper()
		if st := p.ArriveFor("web"); st != nil {
			return *st
		}
		t.Fatalf("no arrival for depot web: %+v", p.ArriveByDepot)
		return store.PeerIntakeArrive{}
	}
	p := arrive()
	st := web(p)
	if len(st.Arrive) != 1 || st.Arrive[0].Who != store.WhoNobody || !slices.Equal(st.Arrive[0].Types, []string{"task", "bug", "feature"}) ||
		st.ArriveLine("north") != in+"which no route leads anywhere from: they wait until one does (yardr route add --stage arrived --group <group> --label from-north)" {
		t.Fatalf("an intake no route leads from = %+v: %s", st.Arrive, st.ArriveLine("north"))
	}
	y.take(t, beadMsg("b1", "web-1", nil))
	if b := y.arrived(t); b.Peer.Stands() != "it arrived in this stage, which no route leads anywhere from: it waits until one does (yardr route add --stage arrived --group <group> --label from-north)" {
		t.Fatalf("a bead no route leads from = %q", b.Peer.Stands())
	}
	south, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "south.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.AddPeer(y.ctx, store.Peer{Name: "south", Key: yardkey.Public(south)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.AllowPeer(y.ctx, "south", store.PeerAllow{Send: []string{"bead"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	label := "from-north"
	for _, c := range []struct {
		group     store.Group
		who       string
		intake    string
		bead      string
		untold    bool
		seenFirst bool
	}{
		{store.Group{Name: "arrivals", Runner: "manual", Members: []string{"jo", "yardmaster"}, Config: map[string]any{"wait": "1d"}}, store.WhoPeople,
			"which people work: a member of the group arrivals (jo, yardmaster) moves each on (yardr bead done) or closes it",
			"which people work: a member of the group arrivals (jo, yardmaster) moves it on or closes it", false, true},
		{store.Group{Name: "anyone", Runner: "manual"}, store.WhoPeople,
			"which people work: the group anyone names no members, so anyone of this yard may move one on, a crew member too",
			"which people work: the group anyone names no members, so anyone of this yard may move it on, a crew member too", true, false},
		{store.Group{Name: "builders", Runner: "herdr"}, store.WhoAgents,
			"which agents of the group builders work: WORK STARTS ON EACH UNSEEN, on the other yard's text",
			"which agents of the group builders work: work starts on it unseen, on the other yard's text", false, false},
		{store.Group{Name: "checks", Runner: "exec", Config: map[string]any{"cmd": "true"}}, store.WhoScript,
			"which the script of the group checks works: it runs on each unseen",
			"which the script of the group checks works: it runs on it unseen", false, false},
		{store.Group{Name: "onward", Runner: store.PeerRunner, Config: map[string]any{store.GroupPeer: "south"}}, store.WhoPeer,
			"which the yard south works: each is sent on unseen",
			"which the yard south works: it is sent on unseen", false, false},
	} {
		if err := y.api.PutGroup(y.ctx, c.group, "jo"); err != nil {
			t.Fatal(err)
		}
		r, err := y.api.AddRoute(y.ctx, store.Route{Stage: "arrived", Label: &label, Group: c.group.Name}, "jo")
		if err != nil {
			t.Fatal(err)
		}
		p := arrive()
		st := web(p)
		if len(st.Arrive) != 1 || st.Arrive[0].Who != c.who || st.Arrive[0].Group != c.group.Name || st.Arrive[0].Seen() != c.seenFirst ||
			st.ArriveLine("north") != in+c.intake || (len(p.Untold()) == 1) != c.untold {
			t.Errorf("an intake whose first stage %s works = %+v, untold %v:\n got %s\nwant %s", c.group.Name, st.Arrive, p.Untold(), st.ArriveLine("north"), in+c.intake)
		}
		if b := y.arrived(t); b.Peer.Arrived == nil || b.Peer.Stands() != "it arrived in this stage, "+c.bead {
			t.Errorf("a bead in a stage %s works:\n got %s\nwant %s", c.group.Name, b.Peer.Stands(), "it arrived in this stage, "+c.bead)
		}
		if err := y.api.DeleteRoute(y.ctx, r.ID, "jo"); err != nil {
			t.Fatal(err)
		}
	}

	// Types routed apart: a bug to people, the rest to builders.
	bug := "bug"
	for _, r := range []store.Route{{Stage: "arrived", Type: &bug, Group: "anyone"}, {Stage: "arrived", Group: "builders"}} {
		if _, err := y.api.AddRoute(y.ctx, r, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	p = arrive()
	st = web(p)
	want := "its beads arrive in the stage arrived, by their type: a task or a feature, which agents of the group builders work: WORK STARTS ON EACH UNSEEN, on the other yard's text; " +
		"a bug, which people work: the group anyone names no members, so anyone of this yard may move one on, a crew member too"
	if len(st.Arrive) != 2 || st.ArriveLine("north") != want || !slices.Equal(p.Untold(), []string{"anyone"}) {
		t.Fatalf("types routed apart = %+v, untold %v:\n got %s\nwant %s", st.Arrive, p.Untold(), st.ArriveLine("north"), want)
	}
	// A listing, which the daemon reads on every pass, reads none of it.
	if ps, _ := y.api.Peers(y.ctx); len(ps[0].ArriveByDepot)+len(ps[1].ArriveByDepot) != 0 {
		t.Fatalf("a listed peer says who works its beads: %+v", ps)
	}
	// An intake whose flow has gone says so, where the line was.
	if _, err := y.api.CloseBead(y.ctx, y.arrived(t).ID, "done with", store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.api.DeleteFlow(y.ctx, "incoming", "jo"); err != nil {
		t.Fatal(err)
	}
	p = arrive()
	st = web(p)
	if len(st.Arrive) != 0 || !strings.Contains(st.Error, "incoming") || st.ArriveLine("north") != "" {
		t.Fatalf("an intake whose flow is gone = %+v, %q", st.Arrive, st.Error)
	}
}

// Every way a bead is refused creates nothing, tells the sender why, logs
// it once, and leaves the id free: the sender may send it again when the
// reason is gone.
func TestPeerBeadRefusals(t *testing.T) {
	t.Parallel()
	y := newBeadYard(t)
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
	if _, err := y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Depot: "web", Flow: "incoming", Limit: 2}, "jo"); err != nil {
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

// An intake names a depot of the yard and a flow that resolves for it, with a
// stage a bead can start in. It is shown with the peer, and removed.
func TestPeerIntake(t *testing.T) {
	t.Parallel()
	y := newBeadYard(t)
	putNamed(t, y.api, "ends", "over terminal\n")
	for want, in := range map[string]store.PeerIntake{
		"depot nowhere": {Depot: "nowhere", Flow: "incoming"},
		// Not "the bead pins": no bead does yet (found in the trial).
		"the intake names flow nothing: not found": {Depot: "web", Flow: "nothing"},
		"terminal":                 {Depot: "web", Flow: "ends"},
		"names a depot and a flow": {Depot: "web"},
		"limit":                    {Depot: "web", Flow: "incoming", Limit: -1},
	} {
		if _, err := y.api.SetPeerIntake(y.ctx, "north", &in, "jo"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("SetPeerIntake(%+v) = %v, want %q", in, err, want)
		}
	}
	if _, err := y.api.SetPeerIntake(y.ctx, "nobody", &store.PeerIntake{Depot: "web", Flow: "incoming"}, "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an intake for no peer = %v", err)
	}
	p, _ := y.api.GetPeer(y.ctx, "north")
	if len(p.Intakes) != 1 || p.Intakes[0].Depot != "web" || p.Intakes[0].Flow != "incoming" || p.Intakes[0].Most() != store.PeerIntakeLimit {
		t.Fatalf("the intake a refused one left = %+v", p.Intakes)
	}
	p, err := y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Depot: "web", Flow: "incoming", Limit: 3}, "jo")
	if err != nil || len(p.Intakes) != 1 || p.Intakes[0].Most() != 3 {
		t.Fatalf("SetPeerIntake = %+v, %v", p, err)
	}
	ev := peerEvents(t, y.api, "peer_intake")
	if last := ev[len(ev)-1]; last.Data["peer"] != "north" || last.Data["depot"] != "web" || last.Data["flow"] != "incoming" || last.Data["limit"] != float64(3) {
		t.Fatalf("peer_intake = %+v", last)
	}
	// What the link carries is changed without losing it.
	if p := y.allow(t, store.PeerAllow{Send: []string{"ping"}}); len(p.Intakes) != 1 || p.Intakes[0].Most() != 3 {
		t.Fatalf("the intake after peer allow = %+v", p.Intakes)
	}
	if p, err := y.api.SetPeerIntake(y.ctx, "north", nil, "clerk"); err != nil || len(p.Intakes) != 0 {
		t.Fatalf("removing the intake = %+v, %v", p, err)
	}
	ev = peerEvents(t, y.api, "peer_intake")
	if last := ev[len(ev)-1]; last.Data["none"] != true || last.Actor != "clerk" {
		t.Fatalf("peer_intake for none = %+v", last)
	}
}

// A link holds an intake per depot, each added or replaced on its own. A
// bead that names no remote goes to the default one: the first, or the one
// marked since. Each keeps its own count of open beads against its limit,
// and each is removed on its own.
func TestPeerIntakesPerDepot(t *testing.T) {
	t.Parallel()
	y := newBeadYard(t)
	inDepot := func(depot string) []store.Bead {
		t.Helper()
		bs, err := y.api.ListBeads(y.ctx, store.BeadFilter{All: true, Depot: depot})
		if err != nil {
			t.Fatal(err)
		}
		return bs
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); len(p.Intakes) != 1 || !p.Intakes[0].Default {
		t.Fatalf("the first intake = %+v; want the default", p.Intakes)
	}
	if err := y.api.PutDepot(y.ctx, store.Depot{Name: "api", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	p, err := y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Depot: "api", Flow: "incoming"}, "jo")
	if err != nil || len(p.Intakes) != 2 || p.Intakes[0].Depot != "api" || p.Intakes[1].Depot != "web" ||
		p.Intakes[0].Default || !p.Intakes[1].Default {
		t.Fatalf("adding a second intake = %+v, %v", p.Intakes, err)
	}
	if ev := peerEvents(t, y.api, "peer_intake"); ev[len(ev)-1].Data["default"] != false {
		t.Fatalf("peer_intake for a nondefault intake = %+v", ev[len(ev)-1])
	}
	// Words with no remote go to the default one.
	if got := y.take(t, beadMsg("d1", "web-1", nil)); got[0].Refused != "" {
		t.Fatalf("a bead with no remote = %+v", got)
	}
	if bs := inDepot("web"); len(bs) != 1 || len(inDepot("api")) != 0 {
		t.Fatalf("the bead went to %d in web, %d in api; want one in web", len(bs), len(inDepot("api")))
	}
	// Marking the other moves the default, and the next bead follows it.
	p, err = y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Depot: "api", Flow: "incoming", Default: true}, "jo")
	if err != nil || !p.Intakes[0].Default || p.Intakes[1].Default {
		t.Fatalf("marking the other default = %+v, %v", p.Intakes, err)
	}
	if ev := peerEvents(t, y.api, "peer_intake"); ev[len(ev)-1].Data["default"] != true {
		t.Fatalf("peer_intake for a default = %+v", ev[len(ev)-1])
	}
	if got := y.take(t, beadMsg("d2", "web-2", nil)); got[0].Refused != "" {
		t.Fatalf("a bead with no remote after the move = %+v", got)
	}
	if bs := inDepot("api"); len(bs) != 1 {
		t.Fatalf("beads in api = %d; want one", len(bs))
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Beads != 2 || p.OpenByDepot["web"] != 1 || p.OpenByDepot["api"] != 1 {
		t.Fatalf("the peer's open beads = %d %+v", p.Beads, p.OpenByDepot)
	}
	// Replacing the default without --default keeps its mark and records
	// the resulting state in the event.
	p, err = y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Depot: "api", Flow: "incoming", Limit: 2}, "jo")
	if err != nil || !p.Intakes[0].Default {
		t.Fatalf("replacing the default intake = %+v, %v", p.Intakes, err)
	}
	if ev := peerEvents(t, y.api, "peer_intake"); ev[len(ev)-1].Data["default"] != true {
		t.Fatalf("peer_intake for a preserved default = %+v", ev[len(ev)-1])
	}
	// A replace without the mark keeps what the intake had.
	p, err = y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Depot: "web", Flow: "incoming", Limit: 1}, "jo")
	if err != nil || p.Intakes[1].Most() != 1 || p.Intakes[1].Default || !p.Intakes[0].Default {
		t.Fatalf("replacing without the mark = %+v, %v", p.Intakes, err)
	}
	// Each intake's limit holds against its own depot: web keeps one, with
	// one open, while api holds another.
	if _, err := y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Depot: "web", Flow: "incoming", Limit: 1, Default: true}, "jo"); err != nil {
		t.Fatal(err)
	}
	if got := y.take(t, beadMsg("d3", "web-3", nil)); got[0].Refused != "busy" {
		t.Fatalf("a bead over the depot's limit = %+v", got)
	}
	if ev := peerEvents(t, y.api, "peer_message_refused"); ev[len(ev)-1].Data["open"] != float64(1) || ev[len(ev)-1].Data["limit"] != float64(1) {
		t.Fatalf("busy says whose count: %+v", ev[len(ev)-1])
	}
	// Removing the default leaves no default: a bead with no remote is
	// refused, and the event says so. Removing what is not there is an
	// error, not a second event.
	p, err = y.api.RemovePeerIntake(y.ctx, "north", "web", "clerk")
	if err != nil || len(p.Intakes) != 1 || p.Intakes[0].Depot != "api" {
		t.Fatalf("removing one intake = %+v, %v", p.Intakes, err)
	}
	if got := y.take(t, beadMsg("d4", "web-4", nil)); got[0].Refused != "no_intake" {
		t.Fatalf("a bead with no remote and no default = %+v", got)
	}
	if ev := peerEvents(t, y.api, "peer_message_refused"); ev[len(ev)-1].Data["detail"] != "the bead names no remote, and no intake is the default one" {
		t.Fatalf("no_intake with no default = %+v", ev[len(ev)-1])
	}
	if _, err := y.api.RemovePeerIntake(y.ctx, "north", "web", "clerk"); err == nil || !strings.Contains(err.Error(), "has no intake for depot web") {
		t.Fatalf("removing it again = %v", err)
	}
	if ev := peerEvents(t, y.api, "peer_intake"); ev[len(ev)-1].Data["depot"] != "web" || ev[len(ev)-1].Data["none"] != true {
		t.Fatalf("peer_intake for one removed = %+v", ev[len(ev)-1])
	}
}

// One word allows a bead and its answer, each against the other: a yard
// that takes beads may send returns and takes none, and nothing on the
// socket sends either kind.
func TestPeerBeadKinds(t *testing.T) {
	t.Parallel()
	y := newBeadYard(t)
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

// A bead from another yard in a stage a manual group with members works is
// moved on by a member, a user or a crew member the group names: not by a
// bead's agent, not by any other crew member, not by a name that is no
// member. A refused move changes nothing, and closing the bead is anyone's,
// as it is of any bead: that returns it as refused.
func TestPeerBeadMovedOnByAMember(t *testing.T) {
	t.Parallel()
	y := newBeadYard(t)
	y.arrivals(t, "jo", "warden")
	for _, name := range []string{"yardmaster", "clerk", "warden"} {
		y.member(t, name, "")
	}
	y.take(t, beadMsg("b1", "web-k3f9", nil))
	b := y.arrived(t)

	// An agent's session: one the yard started for another bead.
	if err := y.api.PutGroup(y.ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"); err != nil {
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
		"this is the session " + sess.ID + " of a bead's agent": {store.Expect{Session: sess.ID}, "jo"},
		`(you are "yardmaster")`:                                {store.Expect{}, "yardmaster"},
		`(you are "clerk")`:                                     {store.Expect{}, "clerk"},
		`(you are "stranger")`:                                  {store.Expect{}, "stranger"},
	} {
		for how, move := range map[string]func() (*store.Bead, error){
			"done":    func() (*store.Bead, error) { return y.api.Done(y.ctx, b.ID, "refused", "", try.exp, try.actor) },
			"advance": func() (*store.Bead, error) { return y.api.Advance(y.ctx, b.ID, "build", try.exp, try.actor) },
		} {
			_, err := move()
			if err == nil || !strings.Contains(err.Error(), who) ||
				!strings.Contains(err.Error(), "only members of group arrivals may move it out of stage arrived: jo, warden") {
				t.Errorf("%s as %s = %v", how, who, err)
			}
		}
	}
	if got, _ := y.api.GetBead(y.ctx, b.ID); got.Stage != "arrived" || got.Peer.Arrived == nil || len(peerEvents(t, y.api, "advanced")) != 0 {
		t.Fatalf("a refused move changed the bead: %+v", got)
	}
	if ref := peerEvents(t, y.api, "move_refused"); len(ref) == 0 || ref[0].Data["reason"] != store.RefusedNotMember {
		t.Fatalf("move_refused = %+v", ref)
	}

	// The crew member the group names.
	got, err := y.api.Advance(y.ctx, b.ID, "build", store.Expect{}, "warden")
	if err != nil || got.Stage != "build" || got.Hold {
		t.Fatalf("the move of a crew member that is a member = %+v, %v", got, err)
	}
	if ev := peerEvents(t, y.api, "advanced"); len(ev) != 1 || *ev[0].Bead != b.ID || ev[0].Actor != "warden" || ev[0].Data["from"] != "arrived" || ev[0].Data["to"] != "build" {
		t.Fatalf("advanced = %+v", ev)
	}
	// Past the stage it arrived in it is a bead like any other: held and
	// unheld by whoever works it.
	if _, err := y.api.SetHold(y.ctx, b.ID, true, store.Expect{}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.SetHold(y.ctx, b.ID, false, store.Expect{}, "stranger"); err != nil {
		t.Fatalf("unhold of a bead that came from a peer: %v", err)
	}

	// A user, on a second bead; and a hold in the stage it arrived in is
	// a hold like any other, lifted by anyone and moving nothing.
	y.take(t, beadMsg("b2", "web-2", nil))
	var second store.Bead
	for _, c := range mustList(t, y) {
		if c.Peer != nil && c.Peer.Bead == "web-2" {
			second = c
		}
	}
	if _, err := y.api.SetHold(y.ctx, second.ID, true, store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if got, err := y.api.SetHold(y.ctx, second.ID, false, store.Expect{}, "clerk"); err != nil || got.Stage != "arrived" {
		t.Fatalf("unhold in the stage it arrived in = %+v, %v", got, err)
	}
	if got, err := y.api.Advance(y.ctx, second.ID, "build", store.Expect{}, "jo"); err != nil || got.Stage != "build" {
		t.Fatalf("a user's move = %+v, %v", got, err)
	}

	// Closing is not a move out of the stage by the flow: anyone's, and
	// the sender hears refused.
	y.take(t, beadMsg("b3", "web-3", nil))
	for _, c := range mustList(t, y) {
		if c.Peer != nil && c.Peer.Bead == "web-3" {
			if _, err := y.api.CloseBead(y.ctx, c.ID, "not ours", store.Expect{}, "clerk"); err != nil {
				t.Fatalf("closing a bead that arrived: %v", err)
			}
		}
	}
	if out, _ := y.api.PeerOutbox(y.ctx, "north", 0); len(out) != 1 || out[0].Re != "b3" {
		t.Fatalf("the return of a bead closed as it arrived = %+v", out)
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
	t.Parallel()
	y := newBeadYard(t)
	y.take(t, beadMsg("b1", "web-k3f9", func(b map[string]any) {
		b["notes"] = []map[string]any{{"author": "yardmaster", "at": time.Now().Add(-time.Hour), "text": "theirs"}}
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
	t.Parallel()
	y := newBeadYard(t)
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
	t.Parallel()
	y := newBeadYard(t)
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
	t.Parallel()
	y := newBeadYard(t)
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
// its label and a crew member's grant to mail follow, and the return goes
// to the peer under its new name. After the link is removed the bead says
// so, nobody's grant is left, and nothing is returned.
func TestPeerBeadRenameAndRemove(t *testing.T) {
	t.Parallel()
	y := newBeadYard(t)
	if _, err := y.api.PutCrew(y.ctx, store.CrewMember{Name: "warden", Runner: "herdr", Config: map[string]any{store.MailPeers: "north"}}, "jo"); err != nil {
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
	r, _ := y.api.GetCrew(y.ctx, "warden")
	if !slices.Equal(r.MayMail(), []string{"nord"}) {
		t.Fatalf("the grants after the rename = mail %v", r.MayMail())
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
	if ev := peerEvents(t, y.api, "peer_removed"); ev[0].Data["beads"] != float64(3) {
		t.Fatalf("peer_removed = %+v", ev[0].Data)
	}
	if r, _ := y.api.GetCrew(y.ctx, "warden"); len(r.MayMail()) != 0 {
		t.Fatalf("grants for a link that is removed: %+v", r.Config)
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
	if _, err := y.api.SetPeerIntake(y.ctx, "north", &store.PeerIntake{Depot: "web", Flow: "incoming", Limit: 1}, "jo"); err != nil {
		t.Fatal(err)
	}
	if got := y.take(t, beadMsg("b9", "web-3", nil)); got[0].Refused != "" {
		t.Fatalf("a bead over the new link = %+v", got)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Beads != 1 {
		t.Fatalf("open beads of the new link = %d", p.Beads)
	}
}

// A bead handed over to a pull is taken as one posted is, and a refusal is
// kept for the next question.
func TestPeerBeadPulled(t *testing.T) {
	t.Parallel()
	y := newBeadYard(t)
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
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Refused[5].Reason != "already_here" || p.Pulled != 5 {
		t.Fatalf("the peer after the pull = %+v", p)
	}
}

// A move asked from a crew member's pane is that crew member's, whatever
// actor the command names: a crew member talked into moving a peer's bead
// on cannot do it as a member by saying it is one. The pane is the shell
// the runner recorded for the crew member's tab. A crew member that is a
// member moves it from its pane under any name.
func TestPeerBeadMoveFromACrewPane(t *testing.T) {
	t.Parallel()
	y := newBeadYard(t)
	y.arrivals(t, "jo", "warden")
	y.member(t, "yardmaster", "")
	y.member(t, "warden", "")
	y.take(t, beadMsg("b1", "web-1", nil), beadMsg("b2", "web-2", nil))
	var one, two store.Bead
	for _, c := range mustList(t, y) {
		if c.Peer.Bead == "web-1" {
			one = c
		} else {
			two = c
		}
	}
	me := os.Getpid()
	st, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := y.api.CrewStarted(y.ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": st.Start}); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(y.ctx, me)
	// The move is recorded as the pane's actor before the member check
	// runs, so the refusal names the crew member, not the forged name.
	want := `only members of group arrivals may move it out of stage arrived: jo, warden (you are "yardmaster")`
	if _, err := y.own.Done(pane, one.ID, "refused", "", store.Expect{}, "jo"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("a move from the yardmaster's pane as jo = %v", err)
	}
	if _, err := y.own.Advance(pane, one.ID, "build", store.Expect{}, "jo"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("an advance from the yardmaster's pane as jo = %v", err)
	}
	// The shell is gone, and its pid someone else's: jo is jo again.
	if err := y.api.CrewStarted(y.ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": st.Start + 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := y.own.Advance(pane, one.ID, "build", store.Expect{}, "jo"); err != nil {
		t.Fatalf("a member's move from a pid a crew member's shell once had: %v", err)
	}
	// The pane of a crew member the group names, the yardmaster's tab closed.
	if err := y.api.CrewStarted(y.ctx, "yardmaster", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := y.api.CrewStarted(y.ctx, "warden", map[string]any{"shell_pid": me, "shell_start": st.Start}); err != nil {
		t.Fatal(err)
	}
	if _, err := y.own.Advance(pane, two.ID, "build", store.Expect{}, "stranger"); err != nil {
		t.Fatalf("a move from the pane of a crew member that is a member: %v", err)
	}
}

// The member check goes by the pane for every group with members, not only
// for a stage a peer's bead arrived in: a bead of this yard's own in a
// stage of people is moved out of it from a crew member's pane as that
// crew member, and from the pane of a bead's agent by nobody.
func TestMembersByThePane(t *testing.T) {
	t.Parallel()
	y := newBeadYard(t)
	y.member(t, "yardmaster", "")
	putNamed(t, y.api, "decided", "decide -> build\nbuild -> over\nover terminal\n")
	if err := y.api.PutGroup(y.ctx, store.Group{Name: "decisions", Runner: "manual", Members: []string{"jo"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.api.PutGroup(y.ctx, store.Group{Name: "builders", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	for stage, group := range map[string]string{"decide": "decisions", "build": "builders"} {
		if _, err := y.api.AddRoute(y.ctx, store.Route{Stage: stage, Group: group}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	own, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "to decide", Depot: "web", Metadata: map[string]any{"flow": "decided"}}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	work, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "at work", Depot: "web", Stage: "build", Metadata: map[string]any{"flow": "decided"}}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	me := os.Getpid()
	st, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	pane := store.WithCaller(y.ctx, me)

	// The pane of a crew member that is no member.
	if err := y.api.CrewStarted(y.ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": st.Start}); err != nil {
		t.Fatal(err)
	}
	// The move is recorded as the pane's actor before the member check
	// runs, so the refusal names the crew member, not the forged name.
	if _, err := y.own.Advance(pane, own.ID, "build", store.Expect{}, "jo"); err == nil ||
		!strings.Contains(err.Error(), `only members of group decisions may move it out of stage decide: jo (you are "yardmaster")`) {
		t.Fatalf("a move from the yardmaster's pane as jo = %v", err)
	}
	if err := y.api.CrewStarted(y.ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": st.Start + 1}); err != nil {
		t.Fatal(err)
	}
	// The pane of the agent that works another bead.
	sess, err := y.api.Claim(y.ctx, work.ID, "builders", "build", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.api.MarkRunning(y.ctx, sess.ID, map[string]any{"agent": "web-build", "shell_pid": me, "shell_start": st.Start}); err != nil {
		t.Fatal(err)
	}
	if _, err := y.own.Advance(pane, own.ID, "build", store.Expect{}, "jo"); err == nil ||
		!strings.Contains(err.Error(), "only members of group decisions may move it out of stage decide: jo (this is the session "+sess.ID+" of a bead's agent, whatever actor the command names)") {
		t.Fatalf("a move from an agent's pane as jo = %v", err)
	}
	// Its own bead, in a stage whose group has no members, it moves as before.
	if _, err := y.own.Advance(pane, work.ID, "over", store.Expect{}, "web-build"); err != nil {
		t.Fatalf("an agent's move of its own bead: %v", err)
	}
	// With no pane the store knows, the name is what there is, as before.
	// Asked in the owning process: over the socket this test's process is
	// the agent's pane from here on.
	if _, err := y.own.Advance(y.ctx, own.ID, "build", store.Expect{}, "yardmaster"); err == nil || !strings.Contains(err.Error(), `(you are "yardmaster")`) {
		t.Fatalf("a non-member's move = %v", err)
	}
	if got, err := y.own.Advance(y.ctx, own.ID, "build", store.Expect{}, "jo"); err != nil || got.Stage != "build" {
		t.Fatalf("a member's move = %+v, %v", got, err)
	}
}

// What a sender is told when its message was refused is said in its own
// words, for each reason a bead is refused for. A return has no kind of its
// own to allow, so its refusal names the bead's (found in the trial: "its
// link does not carry bead-return in" names nothing an operator can allow).
func TestPeerRefusalOfBeads(t *testing.T) {
	t.Parallel()
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
		if got := store.PeerRefusal("north", store.PeerBead, reason); got != want {
			t.Errorf("PeerRefusal(bead, %s) = %q, want %q", reason, got, want)
		}
	}
	if got := store.PeerRefusal("north", store.PeerBeadReturn, "kind_not_allowed"); got != "its link sends this yard no beads, so it takes no return of one" {
		t.Errorf("PeerRefusal(bead-return, kind_not_allowed) = %q", got)
	}
	if got := store.PeerRefusal("north", store.PeerMail, "too_large"); got != "the text is larger than it takes" {
		t.Errorf("PeerRefusal(mail, too_large) = %q", got)
	}
}
