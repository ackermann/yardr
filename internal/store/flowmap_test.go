package store_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// mapFlow has a stage for every kind of worker, one nobody is routed to
// and one that ends the bead; mapTrain is the flow of the depot's trains.
const (
	mapFlow = "backlog -> new\nnew -> review on done | decide on question | backlog\n" +
		"review -> approved on done | new on changes | parked\napproved -> merged on done | new on failed\n" +
		"decide -> new\nparked -> new\nmerged terminal\n"
	mapTrain = "open -> review on done | open on failed\nreview -> merged on done | open on changes\nmerged terminal\n"
)

// mapDepot is a depot "web" following mapFlow, its trains mapTrain, with the
// groups and routes that give every stage but parked someone to work it.
func mapDepot(t *testing.T, files map[string]string) (store.API, context.Context) {
	t.Helper()
	all := map[string]string{".yardr/flows/default.flow": mapFlow, ".yardr/flows/type/train.flow": mapTrain}
	for name, text := range files {
		all[name] = text
	}
	s, ctx := storetest.New(t), context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Path: gitRepo(t, all), Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for name, runner := range map[string]string{"builders": "herdr", "assembly": "exec", "humans": "manual"} {
		cfg := map[string]any{}
		if runner == "exec" {
			cfg["cmd"] = "true"
		}
		if err := s.PutGroup(ctx, store.Group{Name: name, Runner: runner, Config: cfg}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	depot, train := "web", "train"
	for _, r := range []store.Route{
		{Stage: "backlog", Group: "humans"}, {Stage: "new", Depot: &depot, Group: "builders"},
		{Stage: "review", Group: "builders"}, {Stage: "approved", Group: "assembly"},
		{Stage: "decide", Group: "humans"}, {Stage: "merged", Group: "builders"},
		// A train's review is a person's; its open stage is a script's.
		{Stage: "review", Type: &train, Group: "humans"}, {Stage: "open", Type: &train, Group: "assembly"},
	} {
		if _, err := s.AddRoute(ctx, r, "me"); err != nil {
			t.Fatal(err)
		}
	}
	return s, ctx
}

func flowMap(t *testing.T, s store.API, id string) *store.FlowMap {
	t.Helper()
	m, err := s.FlowMap(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// pathOf writes a path as prime does, in full.
func pathOf(m *store.FlowMap) string {
	var steps []string
	for _, p := range m.Path {
		if p.Back != "" {
			steps = append(steps, p.Stage+" ("+p.Back+")")
		} else {
			steps = append(steps, p.Stage)
		}
	}
	return strings.Join(steps, " -> ")
}

// The map lists the flow's stages in the order the file wrote them, each
// with who works a bead like this one there (by the runner of the group
// its route names, never the group) and the stage file's one line: its
// `summary:` line, else its first sentence, cut on a word. A stage without
// a file has no line, one without a route is nobody's, a terminal one the
// end whatever is routed there.
func TestFlowMapStages(t *testing.T) {
	long := "The finished bead is reviewed here against its goal and its done-when and the notes of the builder who made it and more. Second sentence."
	s, ctx := mapDepot(t, map[string]string{
		".yardr/flows/default/backlog.md":  "summary: Shaped until a builder can finish it without asking.\n\nNew beads wait here. Nobody reads this on the map.\n",
		".yardr/flows/default/new.md":      "A builder implements the bead\non its own branch (see docs/line.md). The rest\nis not on the map.\n\nNor is this.\n",
		".yardr/flows/default/review.md":   long + "\n",
		".yardr/flows/default/approved.md": "### Landing\n\nThe assembly lands it.\n",
		".yardr/flows/default/parked.md":   "No full stop here\n\nSecond paragraph.\n",
		".yardr/flows/type/train/open.md":  "The train waits for its children.\n",
	})
	b, err := s.CreateBead(ctx, store.NewBead{Title: "t", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	m := flowMap(t, s, b.ID)
	want := []store.FlowMapStage{
		{Name: "backlog", Who: "people", Summary: "Shaped until a builder can finish it without asking."},
		{Name: "new", Who: "agents", Summary: "A builder implements the bead on its own branch (see docs/line.md).", Current: true},
		{Name: "review", Who: "agents", Summary: "The finished bead is reviewed here against its goal and its done-when and the notes of the builder…"},
		{Name: "decide", Who: "people"},
		{Name: "approved", Who: "script", Summary: "Landing"},
		{Name: "parked", Who: "nobody", Summary: "No full stop here"},
		{Name: "merged", Who: "end"},
	}
	if m.Name != "default" || !reflect.DeepEqual(m.Stages, want) {
		t.Errorf("map of %s:\n%+v\nwant:\n%+v", m.Name, m.Stages, want)
	}
	if len(m.Path) != 0 || m.Path == nil {
		t.Errorf("path of a bead that never moved = %#v, want an empty list", m.Path)
	}
	for _, st := range m.Stages {
		if n := utf8.RuneCountInString(st.Summary); n > store.SummaryMax {
			t.Errorf("summary of %s has %d characters", st.Name, n)
		}
	}

	// A train gets the map of its own flow, and its own routes.
	e, err := s.CreateBead(ctx, store.NewBead{Title: "e", Depot: "web", Type: "train"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	m = flowMap(t, s, e.ID)
	want = []store.FlowMapStage{
		{Name: "open", Who: "script", Summary: "The train waits for its children.", Current: true},
		{Name: "review", Who: "people"},
		{Name: "merged", Who: "end"},
	}
	if m.Name != "type/train" || !reflect.DeepEqual(m.Stages, want) {
		t.Errorf("map of a train, flow %s:\n%+v\nwant:\n%+v", m.Name, m.Stages, want)
	}

	// No flow, no map.
	if err := s.PutDepot(ctx, store.Depot{Name: "bare"}, "me"); err != nil {
		t.Fatal(err)
	}
	free, err := s.CreateBead(ctx, store.NewBead{Title: "f", Depot: "bare"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if m := flowMap(t, s, free.ID); m != nil {
		t.Errorf("map of a bead no flow governs: %+v", m)
	}
}

// The path is every stage the bead entered, from its events. A move to a
// stage that is not later in the flow's order is marked with the outcome
// that took it there, or as made by hand; a forward move is not marked,
// whatever it reported.
func TestFlowMapPath(t *testing.T) {
	s, ctx := mapDepot(t, nil)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "t", Depot: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	done := func(outcome string) {
		t.Helper()
		if _, err := s.Done(ctx, b.ID, outcome, "", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	to := func(stage string) {
		t.Helper()
		if _, err := s.Advance(ctx, b.ID, stage, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	// Notes and other events in between are not stages.
	if err := s.AddNote(ctx, b.ID, "a note", "me"); err != nil {
		t.Fatal(err)
	}
	done("done")
	if got, want := pathOf(flowMap(t, s, b.ID)), "new -> review"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	// Sent back twice: by the reviewer, then by hand to the backlog.
	done("changes")
	if got, want := pathOf(flowMap(t, s, b.ID)), "new -> review -> new (changes)"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	to("backlog")
	to("new")
	done("done")
	done("done")
	done("failed")
	m := flowMap(t, s, b.ID)
	if got, want := pathOf(m), "new -> review -> new (changes) -> backlog (by hand) -> new -> review -> approved -> new (failed)"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	for _, st := range m.Stages {
		if st.Current != (st.Name == "new") {
			t.Errorf("stage %s: current = %v, bead is in new", st.Name, st.Current)
		}
	}

	// A stage tried again is not forward either. The move is an event
	// because it ends the session that tried.
	e, err := s.CreateBead(ctx, store.NewBead{Title: "e", Depot: "web", Type: "train"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, e.ID, "assembly", "open", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Done(ctx, e.ID, "failed", "", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if got, want := pathOf(flowMap(t, s, e.ID)), "open -> open (failed)"; got != want {
		t.Errorf("path of a train = %q, want %q", got, want)
	}
}

// The one line never ends inside a character, is cut on a word where
// there is one, and a `summary:` line is no part of what the stage's
// worker reads.
func TestStageFileSummary(t *testing.T) {
	for _, c := range []struct{ name, text, summary, body string }{
		{"none", "", "", ""},
		{"first sentence", "Build it. Then test it.\n", "Build it.", "Build it. Then test it.\n"},
		{"a dot inside a word", "See docs/flows.md and v1.2 first! Then go.\n", "See docs/flows.md and v1.2 first!", "See docs/flows.md and v1.2 first! Then go.\n"},
		{"summary line", "\nsummary:  Built here. \n\nThe long text.\n", "Built here.", "The long text.\n"},
		{"summary line alone", "summary: Built here.\n", "Built here.", ""},
		{"empty summary line", "summary:\nThe long text. More.\n", "The long text.", "The long text. More.\n"},
		{"summary later is text", "The text.\nsummary: no\n", "The text.", "The text.\nsummary: no\n"},
		{"multibyte cut on a word", strings.Repeat("höhö ", 30) + "\n", strings.TrimSpace(strings.Repeat("höhö ", 19)) + "…", strings.Repeat("höhö ", 30) + "\n"},
		{"one long word", strings.Repeat("ü", 150), strings.Repeat("ü", 99) + "…", strings.Repeat("ü", 150)},
		{"exactly at the limit", strings.Repeat("ü", 100), strings.Repeat("ü", 100), strings.Repeat("ü", 100)},
		{"a long summary line is cut too", "summary: " + strings.Repeat("word ", 30) + "\n", strings.TrimSpace(strings.Repeat("word ", 19)) + "…", ""},
	} {
		sf := &store.StageFile{Text: c.text}
		if got := sf.Summary(); got != c.summary || !utf8.ValidString(got) || utf8.RuneCountInString(got) > store.SummaryMax {
			t.Errorf("%s: summary = %q, want %q", c.name, got, c.summary)
		}
		if got := sf.Body(); got != c.body {
			t.Errorf("%s: body = %q, want %q", c.name, got, c.body)
		}
	}
	var none *store.StageFile
	if s, b := none.Summary(), none.Body(); s != "" || b != "" {
		t.Errorf("no stage file: summary %q, body %q", s, b)
	}
}

// An outcome of a stage a linked yard works that leads to a terminal stage
// or to a script's is a landing, named with both stages; one that leads to
// a stage of agents or people is none, and neither is any outcome of a
// stage this yard works itself.
func TestPeerLandings(t *testing.T) {
	f, err := store.ParseFlow("new -> build on done | decide on question\n" +
		"build -> review on done | approved on tested | dropped on refused | decide on question\n" +
		"review -> approved on done | build on changes\napproved -> merged on done | build on failed\n" +
		"decide -> build\nmerged terminal\ndropped terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	groups := []store.Group{
		{Name: "builders", Runner: "herdr"}, {Name: "assembly", Runner: "exec"}, {Name: "humans", Runner: "manual"},
		{Name: "at-north", Runner: store.PeerRunner, Config: map[string]any{store.GroupPeer: "north"}},
	}
	routes := []store.Route{
		{ID: "1", Stage: "new", Group: "builders"}, {ID: "2", Stage: "build", Group: "at-north"},
		{ID: "3", Stage: "review", Group: "builders"}, {ID: "4", Stage: "approved", Group: "assembly"},
		{ID: "5", Stage: "decide", Group: "humans"},
	}
	got := store.PeerLandings(f, routes, groups, "web", "", nil)
	want := []store.PeerLanding{
		{Stage: "build", Yard: "north", Outcome: "tested", To: "approved", Who: store.WhoScript, Group: "assembly"},
		{Stage: "build", Yard: "north", Outcome: "refused", To: "dropped", Who: store.WhoEnd},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("landings = %+v, want %+v", got, want)
	}
	if s := got[0].Leads(); s != "its outcome tested leads to approved, which the script of the group assembly works" {
		t.Errorf("a script's: %q", s)
	}
	if s := got[1].Leads(); s != "its outcome refused leads to dropped, which is terminal" {
		t.Errorf("a terminal stage: %q", s)
	}

	// Every outcome of the peer's stage leads to agents or people: none.
	// approved -> merged is this yard's own script's outcome, and no note.
	seen, err := store.ParseFlow("build -> review on done | decide on question\nreview -> approved on done\n" +
		"approved -> merged on done\ndecide -> build\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := store.PeerLandings(seen, routes, groups, "web", "", nil); len(got) != 0 {
		t.Errorf("a flow whose peer outcomes all lead to agents or people: %+v", got)
	}
	// And a flow no peer works a stage of.
	if got := store.PeerLandings(f, append([]store.Route{{ID: "0", Stage: "build", Group: "builders", Priority: 1}}, routes...), groups, "web", "", nil); len(got) != 0 {
		t.Errorf("a peer route nothing reaches: %+v", got)
	}
}

// Which group works a stage is the route's choice by bead: a stage is a
// peer's when some bead of the flow may go to the peer there, and what the
// route asked of that bead decides where it may go after.
func TestPeerLandingsByRoute(t *testing.T) {
	f, err := store.ParseFlow("build -> approved on done\napproved -> merged on done\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	groups := []store.Group{
		{Name: "builders", Runner: "herdr"}, {Name: "assembly", Runner: "exec"}, {Name: "humans", Runner: "manual"},
		{Name: "at-north", Runner: store.PeerRunner, Config: map[string]any{store.GroupPeer: "north"}},
	}
	remote, local, web, api, bug, train := "remote", "local", "web", "api", "bug", "train"
	landing := []store.PeerLanding{{Stage: "build", Yard: "north", Outcome: "done", To: "approved", Who: store.WhoScript, Group: "assembly"}}
	for _, c := range []struct {
		name   string
		routes []store.Route
		typ    string
		typed  []string
		want   []store.PeerLanding
	}{
		{"only labelled beads go to the peer, and the script takes all", []store.Route{
			{ID: "1", Stage: "build", Group: "builders"}, {ID: "2", Stage: "build", Label: &remote, Group: "at-north"},
			{ID: "3", Stage: "approved", Group: "assembly"}}, "", nil, landing},
		{"the peer's beads are looked at by people, the others land", []store.Route{
			{ID: "1", Stage: "build", Group: "builders"}, {ID: "2", Stage: "build", Label: &remote, Group: "at-north"},
			{ID: "3", Stage: "approved", Group: "assembly"}, {ID: "4", Stage: "approved", Label: &remote, Group: "humans"}}, "", nil, nil},
		{"a bead may carry another label too", []store.Route{
			{ID: "1", Stage: "build", Label: &remote, Group: "at-north"},
			{ID: "3", Stage: "approved", Group: "humans"}, {ID: "4", Stage: "approved", Label: &local, Group: "assembly"}}, "", nil, landing},
		{"another depot's routes are not this flow's", []store.Route{
			{ID: "1", Stage: "build", Depot: &api, Group: "at-north"}, {ID: "2", Stage: "build", Group: "builders"},
			{ID: "3", Stage: "approved", Group: "assembly"}}, "", nil, nil},
		{"this depot's are", []store.Route{
			{ID: "1", Stage: "build", Depot: &web, Group: "at-north"},
			{ID: "3", Stage: "approved", Depot: &api, Group: "humans"}, {ID: "4", Stage: "approved", Group: "assembly"}}, "", nil, landing},
		{"a route for a type the depot's flow governs", []store.Route{
			{ID: "1", Stage: "build", Type: &bug, Group: "at-north"}, {ID: "3", Stage: "approved", Type: &bug, Group: "assembly"},
			{ID: "4", Stage: "approved", Group: "humans"}}, "", nil, landing},
		{"a route for a type with a flow of its own", []store.Route{
			{ID: "1", Stage: "build", Type: &train, Group: "at-north"}, {ID: "3", Stage: "approved", Group: "assembly"}}, "", []string{"train"}, nil},
		{"that type's flow has it", []store.Route{
			{ID: "1", Stage: "build", Type: &train, Group: "at-north"}, {ID: "3", Stage: "approved", Group: "assembly"}}, "train", nil, landing},
		{"another type's route in a type's flow", []store.Route{
			{ID: "1", Stage: "build", Type: &bug, Group: "at-north"}, {ID: "3", Stage: "approved", Group: "assembly"}}, "train", nil, nil},
	} {
		if got := store.PeerLandings(f, c.routes, groups, "web", c.typ, c.typed); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: landings = %+v, want %+v", c.name, got, c.want)
		}
	}
}
