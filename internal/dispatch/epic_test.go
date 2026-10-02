package dispatch_test

import (
	"context"
	"os"
	"slices"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// epicFlow is the yard.epic flow exactly as the repository carries it.
func epicFlow(t *testing.T) *store.Flow {
	t.Helper()
	text, err := os.ReadFile("../../.yardr/flows/yard.epic.flow")
	must(t, err)
	f, err := store.ParseFlow(string(text))
	must(t, err)
	f.Name = "yard.epic"
	return f
}

// The epic flow as agreed: every stage's outcomes, and the moves left to
// humans.
func TestEpicFlowOutcomes(t *testing.T) {
	f := epicFlow(t)
	want := map[string][]store.Outcome{
		"open":     {{Name: "done", To: "review"}, {Name: "failed", To: "open"}},
		"review":   {{Name: "done", To: "approved"}, {Name: "changes", To: "open"}, {Name: "question", To: "decide"}},
		"approved": {{Name: "done", To: "merged"}, {Name: "failed", To: "open"}, {Name: "conflict", To: "merge"}},
		"merge":    {{Name: "done", To: "review"}, {Name: "rework", To: "open"}, {Name: "question", To: "decide"}},
	}
	for stage, outs := range want {
		if got := f.Outcomes[stage]; !slices.Equal(got, outs) {
			t.Errorf("%s outcomes = %+v, want %+v", stage, got, outs)
		}
	}
	// backlog's single way out is its implicit done.
	for stage, to := range map[string][]string{"backlog": {"open"}, "open": {"backlog"}, "decide": {"open", "backlog"}} {
		for _, s := range to {
			if !slices.Contains(f.Transitions[stage], s) {
				t.Errorf("%s -> %s missing: %v", stage, s, f.Transitions[stage])
			}
		}
	}
	if !slices.Equal(f.Terminal, []string{"merged"}) {
		t.Errorf("terminal = %v, want merged", f.Terminal)
	}
}

// An epic in open is routed to the integrators only once its last child
// closed (the parent edge made each child a blocker), by its type route
// over the stage route for open, with its children and outcomes in the job.
func TestEpicWaitsForItsChildren(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutRig(ctx, store.Rig{Name: "yard", Base: "main"}, "me"))
	_, err := s.PutFlow(ctx, *epicFlow(t), "me")
	must(t, err)
	must(t, s.SetFlowPointer(ctx, "yard", store.TypeEpic, "yard.epic", "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "yard-integrators", Runner: "fake", Limit: 1}))
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}))
	rig, epic := "yard", store.TypeEpic
	_, err = s.AddRoute(ctx, store.Route{Stage: "open", Type: &epic, Rig: &rig, Group: "yard-integrators", Priority: 10})
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "open", Group: "builders"})
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"})
	must(t, err)
	for _, nb := range []store.NewBead{
		{ID: "yard-e", Title: "epic", Type: store.TypeEpic, Rig: "yard", Stage: "open"},
		{ID: "yard-1", Title: "one", Rig: "yard"},
		{ID: "yard-2", Title: "two", Rig: "yard"},
	} {
		_, err := s.CreateBead(ctx, nb, "me")
		must(t, err)
	}
	must(t, s.AddDep(ctx, "yard-e", "yard-1", store.DepParent, "me"))
	must(t, s.AddDep(ctx, "yard-e", "yard-2", store.DepParent, "me"))

	must(t, d.Reconcile(ctx))
	if got := f.starts(); slices.Contains(got, "yard-e") {
		t.Fatalf("started %v: the epic with open children was dispatched", got)
	}
	for _, c := range []string{"yard-1", "yard-2"} {
		_, err := s.CloseBead(ctx, c, "merged", store.Expect{}, "me")
		must(t, err)
		must(t, d.Reconcile(ctx))
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	var job *dispatch.Job
	for i := range f.jobs {
		if f.jobs[i].Bead.ID == "yard-e" {
			job = &f.jobs[i]
		}
	}
	if job == nil {
		t.Fatalf("started %v: the epic was not dispatched once its children closed", f.started)
	}
	if job.Group.Name != "yard-integrators" {
		t.Fatalf("epic went to %s, want yard-integrators", job.Group.Name)
	}
	if !slices.Equal(job.Children, []string{"yard-1", "yard-2"}) || job.Base != "main" {
		t.Fatalf("job children %v base %q, want yard-1, yard-2 on main", job.Children, job.Base)
	}
	var outs []string
	for _, o := range job.Outcomes {
		outs = append(outs, o.Outcome+">"+o.Stage)
	}
	if want := []string{"done>review", "failed>open"}; !slices.Equal(outs, want) {
		t.Fatalf("outcomes %v, want %v", outs, want)
	}
}
