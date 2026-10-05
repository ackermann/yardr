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
// and one that ends the bead; mapEpic is the flow of the rig's epics.
const (
	mapFlow = "backlog -> new\nnew -> review on done | decide on question | backlog\n" +
		"review -> approved on done | new on changes | parked\napproved -> merged on done | new on failed\n" +
		"decide -> new\nparked -> new\nmerged terminal\n"
	mapEpic = "open -> review on done | open on failed\nreview -> merged on done | open on changes\nmerged terminal\n"
)

// mapRig is a rig "web" following mapFlow, its epics mapEpic, with the
// groups and routes that give every stage but parked someone to work it.
func mapRig(t *testing.T, files map[string]string) (store.API, context.Context) {
	t.Helper()
	all := map[string]string{".yardr/flows/default.flow": mapFlow, ".yardr/flows/type/epic.flow": mapEpic}
	for name, text := range files {
		all[name] = text
	}
	s, ctx := storetest.New(t), context.Background()
	if err := s.PutRig(ctx, store.Rig{Name: "web", Path: gitRepo(t, all), Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for name, runner := range map[string]string{"builders": "herdr", "refinery": "exec", "humans": "manual"} {
		if err := s.PutGroup(ctx, store.Group{Name: name, Runner: runner, Config: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	rig, epic := "web", "epic"
	for _, r := range []store.Route{
		{Stage: "backlog", Group: "humans"}, {Stage: "new", Rig: &rig, Group: "builders"},
		{Stage: "review", Group: "builders"}, {Stage: "approved", Group: "refinery"},
		{Stage: "decide", Group: "humans"}, {Stage: "merged", Group: "builders"},
		// An epic's review is a person's; its open stage is a script's.
		{Stage: "review", Type: &epic, Group: "humans"}, {Stage: "open", Type: &epic, Group: "refinery"},
	} {
		if _, err := s.AddRoute(ctx, r); err != nil {
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
	s, ctx := mapRig(t, map[string]string{
		".yardr/flows/default/backlog.md":  "summary: Shaped until a builder can finish it without asking.\n\nNew beads wait here. Nobody reads this on the map.\n",
		".yardr/flows/default/new.md":      "A builder implements the bead\non its own branch (see docs/pipeline.md). The rest\nis not on the map.\n\nNor is this.\n",
		".yardr/flows/default/review.md":   long + "\n",
		".yardr/flows/default/approved.md": "### Landing\n\nThe refinery lands it.\n",
		".yardr/flows/default/parked.md":   "No full stop here\n\nSecond paragraph.\n",
		".yardr/flows/type/epic/open.md":   "The epic waits for its children.\n",
	})
	b, err := s.CreateBead(ctx, store.NewBead{Title: "t", Rig: "web", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	m := flowMap(t, s, b.ID)
	want := []store.FlowMapStage{
		{Name: "backlog", Who: "people", Summary: "Shaped until a builder can finish it without asking."},
		{Name: "new", Who: "agents", Summary: "A builder implements the bead on its own branch (see docs/pipeline.md).", Current: true},
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

	// An epic gets the map of its own flow, and its own routes.
	e, err := s.CreateBead(ctx, store.NewBead{Title: "e", Rig: "web", Type: "epic"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	m = flowMap(t, s, e.ID)
	want = []store.FlowMapStage{
		{Name: "open", Who: "script", Summary: "The epic waits for its children.", Current: true},
		{Name: "review", Who: "people"},
		{Name: "merged", Who: "end"},
	}
	if m.Name != "type/epic" || !reflect.DeepEqual(m.Stages, want) {
		t.Errorf("map of an epic, flow %s:\n%+v\nwant:\n%+v", m.Name, m.Stages, want)
	}

	// No flow, no map.
	if err := s.PutRig(ctx, store.Rig{Name: "bare"}, "me"); err != nil {
		t.Fatal(err)
	}
	free, err := s.CreateBead(ctx, store.NewBead{Title: "f", Rig: "bare"}, "me")
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
	s, ctx := mapRig(t, nil)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "t", Rig: "web", Stage: "new"}, "me")
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
	e, err := s.CreateBead(ctx, store.NewBead{Title: "e", Rig: "web", Type: "epic"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, e.ID, "refinery", "open", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Done(ctx, e.ID, "failed", "", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if got, want := pathOf(flowMap(t, s, e.ID)), "open -> open (failed)"; got != want {
		t.Errorf("path of an epic = %q, want %q", got, want)
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
