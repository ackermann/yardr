package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mckean/yard/internal/store"
	"github.com/mckean/yard/internal/storetest"
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
	if err := s.PutRig(ctx, store.Rig{Name: "yard", Path: "/tmp/yard", Base: "origin/main"}); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

func putFlow(t *testing.T, s store.API, rig, typ, text string) *store.Flow {
	t.Helper()
	f, err := store.ParseFlow(text)
	if err != nil {
		t.Fatal(err)
	}
	f.Rig, f.Type = rig, typ
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
	f.Rig = "yard"
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
	again.Rig = "yard"
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

func TestPutFlowValidates(t *testing.T) {
	s, ctx := flowRig(t)
	cases := map[string]store.Flow{
		"no rig":            {Transitions: map[string][]string{"a": {"b"}}},
		"unknown rig":       {Rig: "nope", Transitions: map[string][]string{"a": {"b"}}},
		"no stages":         {Rig: "yard"},
		"terminal way out":  {Rig: "yard", Transitions: map[string][]string{"done": {"new"}}, Terminal: []string{"done"}},
		"bad stage name":    {Rig: "yard", Transitions: map[string][]string{"a b": {"c"}}},
		"empty target name": {Rig: "yard", Transitions: map[string][]string{"a": {"-x"}}},
	}
	for name, f := range cases {
		if _, err := s.PutFlow(ctx, f, "me"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := s.PutFlow(ctx, store.Flow{Rig: "nope", Stages: []string{"a"}}, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown rig err = %v, want ErrNotFound", err)
	}
}

func TestNoFlowPassesThrough(t *testing.T) {
	s, ctx := flowRig(t)
	// A rig without a flow, and a bead without a rig while flows exist
	// elsewhere, move anywhere and start in any stage.
	if err := s.PutRig(ctx, store.Rig{Name: "other", Path: "/tmp/o", Base: "origin/main"}); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "other", "", yardFlow)
	for _, rig := range []string{"yard", ""} {
		b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: rig, Stage: "whatever"}, "me")
		if err != nil {
			t.Fatalf("rig %q: create: %v", rig, err)
		}
		for _, to := range []string{"merged", "anything", "new"} {
			if b, err = s.Advance(ctx, b.ID, to, "me"); err != nil {
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
	// Removing the flow restores passthrough.
	if err := s.DeleteFlow(ctx, "other", "", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "y", Rig: "other", Stage: "whatever"}, "me"); err != nil {
		t.Fatalf("after rm: %v", err)
	}
	if err := s.DeleteFlow(ctx, "other", "", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second rm err = %v", err)
	}
	if _, err := s.GetFlow(ctx, "other", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetFlow after rm err = %v", err)
	}
}

func TestFlowAllowsAndRefusesTransitions(t *testing.T) {
	s, ctx := flowRig(t)
	putFlow(t, s, "yard", "", yardFlow)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.Advance(ctx, b.ID, "review", "me"); err != nil || b.Stage != "review" {
		t.Fatalf("new -> review: %v %+v", err, b)
	}
	_, err = s.Advance(ctx, b.ID, "merged", "me")
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
	if next.Flow == nil || next.Flow.Rig != "yard" || !slices.Equal(next.Stages, []string{"approved", "new"}) {
		t.Fatalf("next = %+v", next)
	}
	if _, err := s.Advance(ctx, b.ID, "approved", "me"); err != nil {
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
	b, err = s.Advance(ctx, b.ID, "merged", "merger")
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
	// A reopened bead in a terminal stage is not stuck: it may re-enter any
	// stage of the flow, but not one outside it.
	if _, err := s.Reopen(ctx, b.ID, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, b.ID, "elsewhere", "me"); err == nil {
		t.Fatal("reopened bead left the flow")
	}
	if b, err = s.Advance(ctx, b.ID, "new", "me"); err != nil || b.Status != "open" {
		t.Fatalf("reopened bead back to new: %v %+v", err, b)
	}
}

func TestFlowStageUnknownToFlowCanRejoin(t *testing.T) {
	s, ctx := flowRig(t)
	// A bead created before the flow, in a stage the flow does not know.
	b, err := s.CreateBead(ctx, store.NewBead{Title: "old", Rig: "yard", Stage: "blocked"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yard", "", yardFlow)
	next, _ := s.NextStages(ctx, b.ID)
	if len(next.Stages) != 7 {
		t.Fatalf("next from unknown stage = %v, want every flow stage", next.Stages)
	}
	if _, err := s.Advance(ctx, b.ID, "elsewhere", "me"); err == nil {
		t.Fatal("moved to a stage outside the flow")
	}
	if _, err := s.Advance(ctx, b.ID, "review", "me"); err != nil {
		t.Fatal(err)
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
	if _, err := s.CreateBead(ctx, store.NewBead{Title: "q2", Rig: "yard", Type: "decision"}, "me"); err == nil {
		t.Fatal("decision created in 'new', which only the rig flow has")
	}
	if _, err := s.Advance(ctx, d.ID, "review", "me"); err == nil || !strings.Contains(err.Error(), "allowed next: decided, dropped") {
		t.Fatalf("decision -> review err = %v", err)
	}
	next, _ := s.NextStages(ctx, d.ID)
	if next.Flow == nil || next.Flow.Type != "decision" {
		t.Fatalf("decision governed by %+v", next.Flow)
	}
	if d, err = s.Advance(ctx, d.ID, "dropped", "me"); err != nil || d.Status != "closed" || *d.CloseReason != "dropped" {
		t.Fatalf("dropped: %v %+v", err, d)
	}

	// Other types still follow the rig's flow.
	f, _ := s.CreateBead(ctx, store.NewBead{Title: "feat", Rig: "yard", Type: "feature"}, "me")
	if _, err := s.Advance(ctx, f.ID, "decided", "me"); err == nil {
		t.Fatal("feature used the decision flow")
	}
	if _, err := s.Advance(ctx, f.ID, "review", "me"); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetFlow(ctx, "yard", "decision")
	if err != nil || got.Type != "decision" {
		t.Fatalf("GetFlow = %+v, %v", got, err)
	}
	all, _ := s.Flows(ctx)
	if len(all) != 2 || all[0].Type != "" || all[1].Type != "decision" {
		t.Fatalf("flows = %+v", all)
	}
}
