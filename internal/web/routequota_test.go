package web_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/web"
)

// A stage whose route names several groups lists them in order in the flow
// column, the routes page prints the route as it was typed, and the column
// of beads that wait answers for a manual group's route as it did. No page
// runs the limits command: only the dispatcher may.
func TestFlowsListARoutesGroupsInOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	dir := t.TempDir()
	script := filepath.Join(dir, "provider")
	must(t, os.WriteFile(script, []byte("#!/bin/sh\ntouch "+dir+"/asked\necho '{\"provider\":\"Claude\",\"windows\":[]}'\n"), 0o700))
	_, err := f.st.PutSource(ctx, "quokka.yaml", "name: quokka\nlimits:\n  command: ["+script+
		", \"{provider}\"]\n  kinds: { claude: anthropic, codex: openai }\n", "me")
	must(t, err)
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "line", Base: "origin/main"}, "me"))
	fl, err := store.ParseFlow("new -> review on done\nreview -> merged\nmerged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f.st.PutFlow(ctx, *fl, "me")
	must(t, err)
	must(t, f.st.SetFlowPointer(ctx, "line", "", "code", "me"))
	for _, g := range []store.Group{
		{Name: "a", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "b", Runner: "herdr", Config: map[string]any{"kind": "codex"}},
		{Name: "people", Runner: "manual", Members: []string{"ada"}},
	} {
		must(t, f.st.PutGroup(ctx, g, "me"))
	}
	_, err = f.st.AddRoute(ctx, store.Route{Stage: "new", Group: "a", When: "Weekly<80", Else: []store.RouteGroup{{Name: "b"}}}, "me")
	must(t, err)
	_, err = f.st.AddRoute(ctx, store.Route{Stage: "review", Group: "people"}, "me")
	must(t, err)
	waiting, err := f.st.CreateBead(ctx, store.NewBead{Title: "waiting", Depot: "line"}, "me")
	must(t, err)
	inReview, err := f.st.CreateBead(ctx, store.NewBead{Title: "in review", Depot: "line", Stage: "review"}, "me")
	must(t, err)

	var got web.Flows
	f.getJSON("/flows", &got)
	if len(got.Depots) != 1 || len(got.Depots[0].Flows) != 1 || len(got.Depots[0].Flows[0].Stages) != 3 {
		t.Fatalf("flows = %+v", got)
	}
	stages := got.Depots[0].Flows[0].Stages
	if s := stages[0]; s.Group != "a" || !reflect.DeepEqual(s.Else, []string{"b"}) || s.Human || len(s.Members) != 0 {
		t.Errorf("stage new = %+v, want a, then b, worked by agents", s)
	}
	if s := stages[1]; s.Group != "people" || s.Else != nil || !s.Human || !reflect.DeepEqual(s.Members, []string{"ada"}) {
		t.Errorf("stage review = %+v, want people by hand, as before", s)
	}
	page := squashLines(f.get("/flows").body)
	for _, want := range []string{`<p class="label stage-who">a, else b</p>`, `<p class="label stage-who">people · by hand: ada</p>`} {
		if !strings.Contains(page, want) {
			t.Errorf("/flows lacks %s", want)
		}
	}
	routes := squash(f.get("/routes").body)
	for _, want := range []string{`<h2>new → a@Weekly&lt;80 else b</h2>`, `<h2>review → people</h2>`} {
		if !strings.Contains(routes, want) {
			t.Errorf("/routes lacks %s", want)
		}
	}

	// Who waits for a person: the bead of the manual group, not the one
	// that waits for agents, whichever of the route's groups takes it.
	var line web.Line
	f.getJSON("/line", &line)
	var parked []string
	for _, w := range append(line.Waits, line.Parked...) {
		parked = append(parked, w.Bead)
		if w.Bead == inReview.ID && (len(w.Why) != 1 || w.Why[0].Text != "waits for people (ada) in review") {
			t.Errorf("why the bead in review waits = %+v", w.Why)
		}
	}
	if !reflect.DeepEqual(parked, []string{inReview.ID}) {
		t.Errorf("beads that wait = %v, want the one in review (not %s)", parked, waiting.ID)
	}
	if _, err := os.Stat(filepath.Join(dir, "asked")); err == nil {
		t.Error("a page ran the limits command")
	}
}
