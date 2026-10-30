package pack_test

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// exposePack is the starter pack asking that its yardmaster be exposed to
// the yard's peers, and for nothing else of the operator.
func exposePack(t *testing.T) string {
	t.Helper()
	dir := copyStarter(t)
	edit(t, dir, "    open_threads: [linear]\n", "    expose_to_peers: true\n")
	return dir
}

// linked adds a peer to the yard and returns its key.
func linked(t *testing.T, st store.API, name string) string {
	t.Helper()
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "peer.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPeer(ctx, store.Peer{Name: name, Key: store.Public(k)}, "me"); err != nil {
		t.Fatal(err)
	}
	return store.Public(k)
}

// exposed is the plan's entry for the crew member the pack asks to expose.
func exposed(t *testing.T, p *pack.Plan, member string) pack.Expose {
	t.Helper()
	i := slices.IndexFunc(p.Expose, func(e pack.Expose) bool { return e.CrewMember == member })
	if i < 0 {
		t.Fatalf("the plan asks nothing for %s: %+v", member, p.Expose)
	}
	return p.Expose[i]
}

// applyExpose applies the plan with the operator's answers to whom the
// crew member is exposed to.
func applyExpose(t *testing.T, st store.API, p *pack.Plan, answers map[string]map[string]bool) *pack.Result {
	t.Helper()
	a := pack.Answers{}
	for _, q := range p.Questions {
		a[q.ID] = map[string]string{"kind": "claude", "dir": t.TempDir()}[q.Field]
	}
	res, err := pack.Apply(ctx, st, p, pack.ApplyOptions{Answers: a, Expose: answers, Actor: "me"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func exposedOn(t *testing.T, st store.API, peer string) string {
	t.Helper()
	p, err := st.GetPeer(ctx, peer)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(p.Expose, ",")
}

// expose_to_peers is a crew member's key, true or false: the pack asks,
// and names no peer.
func TestPackReadsExposeToPeers(t *testing.T) {
	dir := exposePack(t)
	p := load(t, dir)
	if i := slices.IndexFunc(p.Crew, func(r pack.CrewMember) bool { return r.Name == "yardmaster" }); i < 0 || !p.Crew[i].ExposeToPeers {
		t.Fatalf("crew = %+v", p.Crew)
	}
	if p := load(t, copyStarter(t)); slices.ContainsFunc(p.Crew, func(r pack.CrewMember) bool { return r.ExposeToPeers }) {
		t.Fatalf("the starter asks to be exposed: %+v", p.Crew)
	}
	edit(t, dir, "expose_to_peers: true", "expose_to_peers: [north]")
	_, err := pack.LoadDir(dir)
	if err == nil || !strings.Contains(err.Error(), "crew.yardmaster.expose_to_peers") || !strings.Contains(err.Error(), "want true or false") {
		t.Fatalf("a pack that names a peer: %v", err)
	}
}

// The import asks about each peer the yard has, and exposes the crew
// member to those the operator says yes for. Building the plan exposes
// nobody, and neither does an import without an answer. A no is recorded
// and not asked again, under whatever name the peer has; a yes taken back
// by hand is asked again; and another yard under a name that was told no
// is asked about as any new peer.
func TestImportAsksToExposeACrewMemberPerPeer(t *testing.T) {
	st, o := yard(t)
	o.Crew = map[string]string{"yardmaster": "boss"}
	dir := exposePack(t)

	// A yard with no peer has nothing to ask.
	ex := exposed(t, build(t, st, load(t, dir), o), "boss")
	if ex.Pack != "yardmaster" || len(ex.Has)+len(ex.Asks)+len(ex.Declined)+len(ex.Keys) != 0 {
		t.Fatalf("with no peer: %+v", ex)
	}
	north, south := linked(t, st, "north"), linked(t, st, "south")
	plan := build(t, st, load(t, dir), o)
	if ex = exposed(t, plan, "boss"); !reflect.DeepEqual(ex.Asks, []string{"north", "south"}) || len(ex.Has)+len(ex.Declined) != 0 {
		t.Fatalf("with two peers: %+v", ex)
	}

	// Without an answer nobody is exposed, and nothing is recorded as
	// answered: the next import still asks.
	res := applyExpose(t, st, plan, nil)
	if len(res.Exposed) != 0 || exposedOn(t, st, "north")+exposedOn(t, st, "south") != "" || record(t, st).Expose != nil {
		t.Fatalf("unanswered: %+v, record %v", res.Exposed, record(t, st).Expose)
	}
	plan = build(t, st, load(t, dir), o)
	if ex = exposed(t, plan, "boss"); !reflect.DeepEqual(ex.Asks, []string{"north", "south"}) {
		t.Fatalf("after an import without an answer: %+v", ex)
	}

	// An answer the plan does not ask for is refused before anything is
	// written.
	for _, bad := range []map[string]map[string]bool{{"boss": {"west": true}}, {"clerk": {"north": true}}} {
		if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: pack.Answers{}, Expose: bad, Actor: "me"}); err == nil || !strings.Contains(err.Error(), "the plan does not ask whether crew member") {
			t.Fatalf("answers %v: %v", bad, err)
		}
	}

	// Yes for north, no for south: the grant is the link's own, logged as
	// any, and both answers are recorded by the peer's key.
	h := head(t, st)
	res = applyExpose(t, st, plan, map[string]map[string]bool{"boss": {"north": true, "south": false}})
	if want := []pack.Exposed{{CrewMember: "boss", Peers: []string{"north"}}}; !reflect.DeepEqual(res.Exposed, want) || !res.Wrote() || !res.Recorded {
		t.Fatalf("exposed %+v, wrote %v, recorded %v", res.Exposed, res.Wrote(), res.Recorded)
	}
	if exposedOn(t, st, "north") != "boss" || exposedOn(t, st, "south") != "" {
		t.Fatalf("north exposes %q, south %q", exposedOn(t, st, "north"), exposedOn(t, st, "south"))
	}
	evs, _ := st.EventsSince(ctx, h, 0)
	if len(evs) != 2 || evs[0].Kind != "peer_allowed" || evs[0].Actor != "me" || evs[0].Data["peer"] != "north" ||
		!reflect.DeepEqual(evs[0].Data["expose"], []any{"boss"}) || evs[1].Kind != "pack_applied" {
		t.Fatalf("events = %+v", evs)
	}
	want := map[string]map[string]bool{"boss": {north: true, south: false}}
	if got := record(t, st).Expose; !reflect.DeepEqual(got, want) {
		t.Fatalf("record = %v", got)
	}

	// Twice is once: north is had, south was declined, nothing is asked.
	plan = build(t, st, load(t, dir), o)
	if ex = exposed(t, plan, "boss"); !reflect.DeepEqual(ex.Has, []string{"north"}) || !reflect.DeepEqual(ex.Declined, []string{"south"}) || len(ex.Asks) != 0 {
		t.Fatalf("the second plan: %+v", ex)
	}
	h = head(t, st)
	if res = applyExpose(t, st, plan, nil); res.Wrote() || res.Recorded || !reflect.DeepEqual(record(t, st).Expose, want) {
		t.Fatalf("the second import: %+v, record %v", res, record(t, st).Expose)
	}
	if evs, _ = st.EventsSince(ctx, h, 0); len(evs) != 1 || evs[0].Kind != "pack_unchanged" {
		t.Fatalf("the second import logged %+v", evs)
	}

	// The no is the yard's that got it: it follows a rename, and a new yard
	// under the old name is asked about.
	if _, err := st.RenamePeer(ctx, "south", "west", "me"); err != nil {
		t.Fatal(err)
	}
	linked(t, st, "south")
	if ex = exposed(t, build(t, st, load(t, dir), o), "boss"); !reflect.DeepEqual(ex.Declined, []string{"west"}) || !reflect.DeepEqual(ex.Asks, []string{"south"}) {
		t.Fatalf("after a rename and a new peer: %+v", ex)
	}

	// A yes taken back by hand is not given again unasked: it is asked,
	// and forgotten by an import that gets no answer.
	if _, err := st.AllowPeer(ctx, "north", store.PeerAllow{NoExpose: []string{"boss"}}, "me"); err != nil {
		t.Fatal(err)
	}
	plan = build(t, st, load(t, dir), o)
	if ex = exposed(t, plan, "boss"); !reflect.DeepEqual(ex.Asks, []string{"north", "south"}) || len(ex.Has) != 0 {
		t.Fatalf("after --no-expose: %+v", ex)
	}
	if res = applyExpose(t, st, plan, nil); len(res.Exposed) != 0 || exposedOn(t, st, "north") != "" {
		t.Fatalf("exposed unasked: %+v", res.Exposed)
	}
	if got := record(t, st).Expose; !reflect.DeepEqual(got, map[string]map[string]bool{"boss": {south: false}}) {
		t.Fatalf("record after the yes was taken back = %v", got)
	}

	// A pack that asks no longer has nothing in the plan or the record.
	edit(t, dir, "    expose_to_peers: true\n", "")
	plan = build(t, st, load(t, dir), o)
	if len(plan.Expose) != 0 {
		t.Fatalf("a pack that asks for nothing: %+v", plan.Expose)
	}
	if applyExpose(t, st, plan, nil); record(t, st).Expose != nil {
		t.Fatalf("record = %v", record(t, st).Expose)
	}
}
