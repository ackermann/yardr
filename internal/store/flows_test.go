package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// yardFlow is the example lifecycle from the yard-18 bead.
const yardFlow = `
# the yard rig
backlog -> new
new -> review | backlog
review -> approved | new
approved -> merged | merge | new
merge -> approved
decide -> backlog
merged terminal
`

func flowRig(t *testing.T) (store.API, context.Context) {
	t.Helper()
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.PutRig(ctx, store.Rig{Name: "yard", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

// putFlow stores text as the named flow <rig> (or <rig>.<typ>) and points
// the rig (or its type) at it, the shape migrated rig flows take.
func putFlow(t *testing.T, s store.API, rig, typ, text string) *store.Flow {
	t.Helper()
	name := rig
	if typ != "" {
		name += "." + typ
	}
	saved := putNamed(t, s, name, text)
	if err := s.SetFlowPointer(context.Background(), rig, typ, name, "me"); err != nil {
		t.Fatal(err)
	}
	return saved
}

func putNamed(t *testing.T, s store.API, name, text string) *store.Flow {
	t.Helper()
	f, err := store.ParseFlow(text)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = name
	saved, err := s.PutFlow(context.Background(), *f, "me")
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestParseFlow(t *testing.T) {
	f, err := store.ParseFlow(yardFlow)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "yard"
	s, ctx := flowRig(t)
	saved, err := s.PutFlow(ctx, *f, "me")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"backlog", "new", "review", "approved", "merged", "merge", "decide"}
	if !slices.Equal(saved.Stages, want) {
		t.Fatalf("stages = %v, want %v", saved.Stages, want)
	}
	if !slices.Equal(saved.Transitions["approved"], []string{"merged", "merge", "new"}) {
		t.Fatalf("approved -> %v", saved.Transitions["approved"])
	}
	if !slices.Equal(saved.Terminal, []string{"merged"}) {
		t.Fatalf("terminal = %v", saved.Terminal)
	}
	// String writes the format ParseFlow reads.
	again, err := store.ParseFlow(saved.String())
	if err != nil {
		t.Fatal(err)
	}
	again.Name = "yard"
	resaved, err := s.PutFlow(ctx, *again, "me")
	if err != nil {
		t.Fatal(err)
	}
	if resaved.String() != saved.String() {
		t.Fatalf("round trip changed the flow:\n%s\nvs\n%s", saved, resaved)
	}
	// JSON is accepted too.
	j, err := store.ParseFlow(`{"transitions": {"a": ["b"]}, "terminal": ["b"]}`)
	if err != nil || !slices.Equal(j.Transitions["a"], []string{"b"}) {
		t.Fatalf("json flow = %+v, %v", j, err)
	}

	for _, bad := range []string{"a => b", "-> b", "a -> b |", "a terminal now"} {
		if _, err := store.ParseFlow(bad); err == nil {
			t.Errorf("ParseFlow(%q) accepted", bad)
		}
	}
}

// A JSON flow's stages come in the order listed, then as the rules reach
// them; never alphabetically, which would make the first stage (where a
// bead the flow does not know rejoins it) an accident of spelling. Without
// a list, two source stages that no rule leads to have no order, and the
// flow is refused.
func TestJSONFlowStageOrder(t *testing.T) {
	s, ctx := flowRig(t)
	save := func(text string) (*store.Flow, error) {
		f, err := store.ParseFlow(text)
		if err != nil {
			t.Fatal(err)
		}
		f.Name = "j"
		return s.PutFlow(ctx, *f, "me")
	}
	// Listed in full: kept as written.
	f, err := save(`{"stages": ["zeta", "alpha", "omega"], "transitions": {"zeta": ["alpha"], "alpha": ["omega"]}, "terminal": ["omega"]}`)
	if err != nil || !slices.Equal(f.Stages, []string{"zeta", "alpha", "omega"}) {
		t.Fatalf("listed stages = %v, %v", f.Stages, err)
	}
	if next := f.Next("ghost"); !slices.Equal(next, []string{"zeta"}) {
		t.Fatalf("rejoin from an unknown stage at %v, want zeta", next)
	}
	// Listed in part: the rest follow in the order the rules name them,
	// targets before the next source, outcomes' stages among them.
	f, err = save(`{"stages": ["zeta"], "transitions": {"zeta": ["mid", "alpha"], "alpha": ["omega"], "mid": ["alpha"]},
		"outcomes": {"omega": [{"name": "done", "to": "end"}]}, "terminal": ["end"]}`)
	if err != nil || !slices.Equal(f.Stages, []string{"zeta", "mid", "alpha", "omega", "end"}) {
		t.Fatalf("partly listed stages = %v, %v", f.Stages, err)
	}
	// Not listed, one source: the only order there is.
	f, err = save(`{"transitions": {"zeta": ["alpha", "omega"]}, "terminal": ["omega"]}`)
	if err != nil || !slices.Equal(f.Stages, []string{"zeta", "alpha", "omega"}) {
		t.Fatalf("single-source stages = %v, %v", f.Stages, err)
	}
	// A source the walk reaches last still has its place: alpha is the one
	// stage left once zeta's rule is followed.
	f, err = save(`{"stages": ["zeta"], "transitions": {"zeta": ["omega"], "alpha": ["zeta"]}}`)
	if err != nil || !slices.Equal(f.Stages, []string{"zeta", "omega", "alpha"}) {
		t.Fatalf("one straggler = %v, %v", f.Stages, err)
	}
	// Not listed, two sources: ambiguous, so refused rather than sorted.
	// A plain chain counts too: nothing says it starts at zeta.
	for _, text := range []string{
		`{"transitions": {"zeta": ["alpha"], "alpha": ["zeta"]}}`,
		`{"transitions": {"zeta": ["alpha"], "alpha": ["omega"]}}`,
		`{"stages": ["zeta"], "transitions": {"zeta": ["omega"], "alpha": ["zeta"], "beta": ["zeta"]}}`,
	} {
		_, err := save(text)
		if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), `"stages"`) {
			t.Fatalf("%s: err = %v, want ambiguous order naming \"stages\"", text, err)
		}
	}
	if _, err := save(`{"transitions": {"zeta": ["alpha"], "alpha": ["zeta"]}}`); !strings.Contains(err.Error(), "alpha, zeta") {
		t.Fatalf("ambiguous error should name the stages: %v", err)
	}
	// The line format lists stages as it goes, so it is never ambiguous.
	f, err = save("zeta -> alpha\nalpha -> zeta\n")
	if err != nil || !slices.Equal(f.Stages, []string{"zeta", "alpha"}) {
		t.Fatalf("line format stages = %v, %v", f.Stages, err)
	}
}

func TestPutFlowValidates(t *testing.T) {
	s, ctx := flowRig(t)
	cases := map[string]store.Flow{
		"no name":           {Transitions: map[string][]string{"a": {"b"}}},
		"bad name":          {Name: "a/b", Transitions: map[string][]string{"a": {"b"}}},
		"no stages":         {Name: "yard"},
		"terminal way out":  {Name: "yard", Transitions: map[string][]string{"done": {"new"}}, Terminal: []string{"done"}},
		"bad stage name":    {Name: "yard", Transitions: map[string][]string{"a b": {"c"}}},
		"empty target name": {Name: "yard", Transitions: map[string][]string{"a": {"-x"}}},
	}
	for name, f := range cases {
		if _, err := s.PutFlow(ctx, f, "me"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Pointers need a rig and a flow that exist.
	putNamed(t, s, "code", yardFlow)
	if err := s.SetFlowPointer(ctx, "nope", "", "code", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown rig err = %v, want ErrNotFound", err)
	}
	if err := s.SetFlowPointer(ctx, "yard", "", "nope", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown flow err = %v, want ErrNotFound", err)
	}
	if err := s.SetFlowPointer(ctx, "yard", "", "", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("clearing no pointer err = %v, want ErrNotFound", err)
	}
}

func TestNoFlowPassesThrough(t *testing.T) {
	s, ctx := flowRig(t)
	// A rig without a flow, and a bead without a rig while flows exist
	// elsewhere, move anywhere and start in any stage.
	if err := s.PutRig(ctx, store.Rig{Name: "other", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "other", "", yardFlow)
	for _, rig := range []string{"yard", ""} {
		b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: rig, Stage: "whatever"}, "me")
		if err != nil {
			t.Fatalf("rig %q: create: %v", rig, err)
		}
		for _, to := range []string{"merged", "anything", "new"} {
			if b, err = s.Advance(ctx, b.ID, to, store.Expect{}, "me"); err != nil {
				t.Fatalf("rig %q: advance to %s: %v", rig, to, err)
			}
		}
		if b.Status != "open" {
			t.Fatalf("rig %q: no flow, but 'merged' closed the bead", rig)
		}
		next, err := s.NextStages(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if next.Flow != nil || len(next.Stages) != 0 || next.Stage != "new" {
			t.Fatalf("rig %q: next = %+v, want no flow", rig, next)
		}
	}
	// Clearing the pointer, then removing the flow, restores passthrough.
	if err := s.SetFlowPointer(ctx, "other", "", "", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFlow(ctx, "other", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "y", Rig: "other", Stage: "whatever"}, "me"); err != nil {
		t.Fatalf("after rm: %v", err)
	}
	if err := s.DeleteFlow(ctx, "other", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second rm err = %v", err)
	}
	if _, err := s.GetFlow(ctx, "other"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetFlow after rm err = %v", err)
	}
}

func TestFlowAllowsAndRefusesTransitions(t *testing.T) {
	s, ctx := flowRig(t)
	putFlow(t, s, "yard", "", yardFlow)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", store.Expect{}, "me"); err != nil || b.Stage != "review" {
		t.Fatalf("new -> review: %v %+v", err, b)
	}
	_, err = s.Advance(ctx, b.ID, "merged", store.Expect{}, "me")
	if err == nil {
		t.Fatal("review -> merged was allowed")
	}
	for _, want := range []string{"review -> merged", "allowed next: approved, new"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	got, _ := s.GetBead(ctx, b.ID)
	if got.Stage != "review" || got.Revision != b.Revision {
		t.Fatalf("refused advance changed the bead: %+v", got)
	}
	next, err := s.NextStages(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Flow == nil || next.Flow.Name != "yard" || next.Source.String() != "store" || !slices.Equal(next.Stages, []string{"approved", "new"}) {
		t.Fatalf("next = %+v", next)
	}
	if _, err := s.Advance(ctx, b.ID, "approved", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	next, _ = s.NextStages(ctx, b.ID)
	if !slices.Equal(next.Stages, []string{"merged", "merge", "new"}) || !slices.Equal(next.Terminal, []string{"merged"}) {
		t.Fatalf("next from approved = %+v", next)
	}
}

func TestFlowCreateStage(t *testing.T) {
	s, ctx := flowRig(t)
	putFlow(t, s, "yard", "", yardFlow)
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard", Stage: "backlog"}, "me"); err != nil {
		t.Fatal(err)
	}
	// No stage: the flow's first, where the mayor shapes it; without a flow,
	// new, as before flows.
	if b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard"}, "me"); err != nil || b.Stage != "backlog" {
		t.Fatalf("created without a stage = %+v, %v; want the flow's first stage", b, err)
	}
	if err := s.PutRig(ctx, store.Rig{Name: "free", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "free"}, "me"); err != nil || b.Stage != "new" {
		t.Fatalf("created without a flow = %+v, %v; want new", b, err)
	}
	if b, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me"); err != nil || b.Stage != "new" {
		t.Fatalf("created without a rig = %+v, %v; want new", b, err)
	}
	_, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard", Stage: "nope"}, "me")
	if err == nil || !strings.Contains(err.Error(), "stages: backlog, new,") {
		t.Fatalf("create in unknown stage err = %v", err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard", Stage: "merged"}, "me"); err == nil {
		t.Fatal("created an open bead in a terminal stage")
	}
	// Knowledge beads are never governed.
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "j", Rig: "yard", Type: store.TypeJournal, Stage: "journal"}, "me"); err != nil {
		t.Fatalf("journal bead: %v", err)
	}
}

func TestFlowTerminalStageCloses(t *testing.T) {
	s, ctx := flowRig(t)
	putFlow(t, s, "yard", "", yardFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "mergers", Runner: "manual"}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard", Stage: "approved"}, "me")
	sess, err := s.Claim(ctx, b.ID, "mergers", "approved", "yard")
	if err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	b, err = s.Advance(ctx, b.ID, "merged", store.Expect{}, "merger")
	if err != nil {
		t.Fatal(err)
	}
	if b.Stage != "merged" || b.Status != "closed" || b.CloseReason == nil || *b.CloseReason != "merged" || b.ClosedAt == nil {
		t.Fatalf("terminal advance left %+v", b)
	}
	if b.Group != nil || b.Session != nil {
		t.Fatalf("still owned: %+v", b)
	}
	got, _ := s.GetSession(ctx, sess.ID)
	if got.State != "done" {
		t.Fatalf("session state %s, want done", got.State)
	}
	// One write: the advance and the close commit together, in that order.
	evs, err := s.EventsSince(ctx, head, 0)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	if !slices.Equal(kinds, []string{"advanced", "closed"}) || evs[1].Data["reason"] != "merged" {
		t.Fatalf("events = %v (%+v)", kinds, evs)
	}
	// A reopened bead in a terminal stage is not stuck: it may re-enter the
	// flow at its first stage, but not further on nor outside it.
	if _, err := s.Reopen(ctx, b.ID, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"elsewhere", "new"} {
		if _, err := s.Advance(ctx, b.ID, to, store.Expect{}, "me"); err == nil {
			t.Fatalf("reopened bead moved to %s", to)
		}
	}
	if b, err = s.Advance(ctx, b.ID, "backlog", store.Expect{}, "me"); err != nil || b.Status != "open" {
		t.Fatalf("reopened bead back to backlog: %v %+v", err, b)
	}
}

func TestFlowStageUnknownToFlowCanRejoin(t *testing.T) {
	s, ctx := flowRig(t)
	// A bead in a stage the flow then loses: pointing the rig at a flow
	// without the stage is refused (TestSetFlowPointerRefusesStrays), but
	// replacing the named flow is not.
	putFlow(t, s, "yard", "", "blocked -> new\n"+yardFlow)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "old", Rig: "yard", Stage: "blocked"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	putNamed(t, s, "yard", yardFlow)
	// It rejoins at the flow's first stage only: from an unknown stage any
	// stage would let it skip review and land as approved or merged.
	next, err := s.NextStages(ctx, b.ID)
	if err != nil || !slices.Equal(next.Stages, []string{"backlog"}) {
		t.Fatalf("next from unknown stage = %+v, %v; want only backlog", next, err)
	}
	for _, to := range []string{"elsewhere", "review", "approved", "merged"} {
		if _, err := s.Advance(ctx, b.ID, to, store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "allowed next: backlog") {
			t.Fatalf("unknown stage -> %s: err = %v", to, err)
		}
	}
	if _, err := s.Advance(ctx, b.ID, "backlog", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
}

// The first stage itself, when the flow gives it no way out, offers nothing
// rather than itself.
func TestFlowNextFromDeadFirstStage(t *testing.T) {
	f, err := store.ParseFlow("only terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	if next := f.Next("only"); len(next) != 0 {
		t.Fatalf("next = %v", next)
	}
	if next := f.Next("unknown"); !slices.Equal(next, []string{"only"}) {
		t.Fatalf("next from unknown = %v", next)
	}
}

func TestFlowTypeOverridesRig(t *testing.T) {
	s, ctx := flowRig(t)
	putFlow(t, s, "yard", "", yardFlow)
	putFlow(t, s, "yard", "decision", "open -> decided | dropped\ndecided terminal\ndropped terminal")

	d, err := s.CreateBead(ctx, store.NewBead{Title: "q1", Rig: "yard", Type: "decision", Stage: "open"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if q2, err := s.CreateBead(ctx, store.NewBead{Title: "q2", Rig: "yard", Type: "decision"}, "me"); err != nil || q2.Stage != "open" {
		t.Fatalf("decision without a stage = %+v, %v; want the type flow's first stage", q2, err)
	}
	if _, err := s.Advance(ctx, d.ID, "review", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "allowed next: decided, dropped") {
		t.Fatalf("decision -> review err = %v", err)
	}
	next, _ := s.NextStages(ctx, d.ID)
	if next.Flow == nil || next.Flow.Name != "yard.decision" || next.Source.String() != "store, type decision" {
		t.Fatalf("decision governed by %+v", next.Flow)
	}
	if d, err = s.Advance(ctx, d.ID, "dropped", store.Expect{}, "me"); err != nil || d.Status != "closed" || *d.CloseReason != "dropped" {
		t.Fatalf("dropped: %v %+v", err, d)
	}

	// Other types still follow the rig's flow.
	f, _ := s.CreateBead(ctx, store.NewBead{Title: "feat", Rig: "yard", Type: "feature", Stage: "new"}, "me")
	if _, err := s.Advance(ctx, f.ID, "decided", store.Expect{}, "me"); err == nil {
		t.Fatal("feature used the decision flow")
	}
	if _, err := s.Advance(ctx, f.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetFlow(ctx, "yard.decision")
	if err != nil || got.Name != "yard.decision" {
		t.Fatalf("GetFlow = %+v, %v", got, err)
	}
	all, _ := s.Flows(ctx)
	if len(all) != 2 || all[0].Name != "yard" || all[1].Name != "yard.decision" {
		t.Fatalf("flows = %+v", all)
	}
}

func TestAdvanceNote(t *testing.T) {
	s, ctx := flowRig(t)
	putFlow(t, s, "yard", "", yardFlow)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard", Stage: "review"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// A move the flow refuses leaves no note behind.
	if _, err := s.AdvanceNote(ctx, b.ID, "merged", "ship it", store.Expect{}, "me"); err == nil {
		t.Fatal("review -> merged allowed")
	}
	if notes, _ := s.Notes(ctx, b.ID); len(notes) != 0 {
		t.Fatalf("refused move left notes: %+v", notes)
	}
	got, err := s.AdvanceNote(ctx, b.ID, "new", "rename the flag", store.Expect{}, "reviewer")
	if err != nil || got.Stage != "new" {
		t.Fatalf("advance = %+v, %v", got, err)
	}
	notes, err := s.Notes(ctx, b.ID)
	if err != nil || len(notes) != 1 || notes[0].Text != "rename the flag" || notes[0].Author != "reviewer" {
		t.Fatalf("notes = %+v, %v", notes, err)
	}
	// An empty note adds none.
	if _, err := s.AdvanceNote(ctx, b.ID, "review", "  ", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if notes, _ := s.Notes(ctx, b.ID); len(notes) != 1 {
		t.Fatalf("empty note stored: %+v", notes)
	}
}
