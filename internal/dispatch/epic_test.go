package dispatch_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// epicFlow is the repository's epic flow exactly as it carries it, under
// the name these tests use for it.
func epicFlow(t *testing.T) *store.Flow {
	t.Helper()
	text, err := os.ReadFile(storetest.RepoFlow(t, "yardr.epic"))
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
		"open":     {{Name: "done", To: "review"}, {Name: "failed", To: "open"}, {Name: "conflict", To: "merge"}},
		"review":   {{Name: "done", To: "approved"}, {Name: "changes", To: "open"}, {Name: "question", To: "decide"}},
		"approved": {{Name: "done", To: "in-pr"}, {Name: "failed", To: "open"}, {Name: "conflict", To: "merge"}},
		"in-pr":    {{Name: "done", To: "land"}, {Name: "changes", To: "open"}, {Name: "question", To: "decide"}},
		"land":     {{Name: "done", To: "merged"}, {Name: "failed", To: "open"}},
		"merge":    {{Name: "done", To: "open"}, {Name: "rework", To: "open"}, {Name: "question", To: "decide"}},
	}
	for stage, outs := range want {
		if got := f.Outcomes[stage]; !slices.Equal(got, outs) {
			t.Errorf("%s outcomes = %+v, want %+v", stage, got, outs)
		}
	}
	// backlog's single way out is its implicit done. approved -> merged is
	// bare: merge-local advances an epic there when it lands it locally
	// (YARDR_PR unset), and no agent is offered it as an outcome.
	for stage, to := range map[string][]string{"backlog": {"open"}, "open": {"backlog"}, "decide": {"open", "backlog"},
		"approved": {"merged"}} {
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
	if want := []string{"done>review", "failed>open", "conflict>merge"}; !slices.Equal(outs, want) {
		t.Fatalf("outcomes %v, want %v", outs, want)
	}
}

// An epic whose rebase onto main conflicted goes to the mergers, and once
// they report done it is back in open with no child open, so the integrators
// take it again: they rebase it cleanly, gate it and send it to review.
func TestEpicConflictGoesToTheMergersAndBack(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutRig(ctx, store.Rig{Name: "yard", Base: "main"}, "me"))
	_, err := s.PutFlow(ctx, *epicFlow(t), "me")
	must(t, err)
	must(t, s.SetFlowPointer(ctx, "yard", store.TypeEpic, "yard.epic", "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "yard-integrators", Runner: "fake", Limit: 1}))
	must(t, s.PutGroup(ctx, store.Group{Name: "mergers", Runner: "fake", Limit: 1}))
	rig, epic := "yard", store.TypeEpic
	_, err = s.AddRoute(ctx, store.Route{Stage: "open", Type: &epic, Rig: &rig, Group: "yard-integrators", Priority: 10})
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "merge", Group: "mergers"})
	must(t, err)
	_, err = s.CreateBead(ctx, store.NewBead{ID: "yard-e", Title: "epic", Type: store.TypeEpic, Rig: "yard", Stage: "open"}, "me")
	must(t, err)

	must(t, d.Reconcile(ctx))
	if b, _ := s.GetBead(ctx, "yard-e"); b.Group == nil || *b.Group != "yard-integrators" {
		t.Fatalf("epic in open not taken by the integrators: %+v", b)
	}
	if _, err := s.Done(ctx, "yard-e", "conflict", "integrator: conflicts in: a.go", store.Expect{}, "integrator"); err != nil {
		t.Fatalf("open has no conflict outcome: %v", err)
	}
	must(t, d.Reconcile(ctx))
	b, _ := s.GetBead(ctx, "yard-e")
	if b.Stage != "merge" || b.Group == nil || *b.Group != "mergers" {
		t.Fatalf("epic %s with %v after a conflict, want merge with the mergers", b.Stage, b.Group)
	}
	if _, err := s.Done(ctx, "yard-e", "done", "merge re-review: rebased", store.Expect{}, "merger"); err != nil {
		t.Fatal(err)
	}
	must(t, d.Reconcile(ctx))
	b, _ = s.GetBead(ctx, "yard-e")
	if b.Stage != "open" || b.Group == nil || *b.Group != "yard-integrators" {
		t.Fatalf("resolved epic %s with %v, want open with the integrators again", b.Stage, b.Group)
	}
	if got := f.starts(); !slices.Equal(got, []string{"yard-e", "yard-e", "yard-e"}) {
		t.Fatalf("starts = %v, want integrator, merger, integrator", got)
	}
}

// An epic its flow sends back to the stage it is in (open -> open on
// failed: the integrator could not push or open the pull request) is not
// integrated again on the next pass: each return in a row doubles the wait
// before the next try, from Backoff up to BackoffMax, and the wait is read
// from the bead's events, so it survives a restart. A human's un-hold
// starts the count over.
func TestReturnToTheSameStageBacksOff(t *testing.T) {
	s, f, d := setup(t)
	d.Backoff, d.BackoffMax = time.Minute, 5*time.Minute
	var skew time.Duration
	dispatch.SetClock(d, func() time.Time { return time.Now().Add(skew) })
	ctx := context.Background()
	must(t, s.PutRig(ctx, store.Rig{Name: "yard", Base: "main"}, "me"))
	_, err := s.PutFlow(ctx, *epicFlow(t), "me")
	must(t, err)
	must(t, s.SetFlowPointer(ctx, "yard", store.TypeEpic, "yard.epic", "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "yard-integrators", Runner: "fake", Limit: 1}))
	rig, epic := "yard", store.TypeEpic
	_, err = s.AddRoute(ctx, store.Route{Stage: "open", Type: &epic, Rig: &rig, Group: "yard-integrators", Priority: 10})
	must(t, err)
	_, err = s.CreateBead(ctx, store.NewBead{ID: "yard-e", Title: "epic", Type: store.TypeEpic, Rig: "yard", Stage: "open"}, "me")
	must(t, err)
	fail := func() {
		t.Helper()
		_, err := s.Done(ctx, "yard-e", "failed", "integrator: GitHub is down", store.Expect{}, "integrator")
		must(t, err)
	}
	starts := func(want int) {
		t.Helper()
		if got := len(f.starts()); got != want {
			t.Fatalf("at skew %s: %d starts, want %d", skew, got, want)
		}
	}
	backoffs := func() []string {
		t.Helper()
		evs, err := s.Events(ctx, "yard-e", 200)
		must(t, err)
		var out []string
		for _, e := range evs {
			if e.Kind == "backoff" {
				out = append(out, e.Data["reason"].(string))
			}
		}
		return out
	}

	must(t, d.Reconcile(ctx))
	starts(1)
	// Returns 1..4: the wait doubles, 1m, 2m, 4m, then is capped at 5m.
	for i, wait := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute} {
		skew = 0
		fail()
		must(t, d.Reconcile(ctx))
		must(t, d.Reconcile(ctx)) // the wait is logged once, not per pass
		starts(i + 1)
		skew = wait - time.Second
		must(t, d.Reconcile(ctx))
		starts(i + 1)
		skew = wait + time.Second
		must(t, d.Reconcile(ctx))
		starts(i + 2)
	}
	got := backoffs()
	if len(got) != 4 || !strings.Contains(got[0], "sent back to open 1 times in a row") || !strings.Contains(got[3], "4 times in a row") {
		t.Fatalf("backoff events %q, want one per return with its count", got)
	}

	// The daemon restarts: the count comes from the events, so it still
	// waits. Then a human un-holds the epic: a fresh start, tried at once.
	skew = 0
	fail()
	f2 := &fakeRunner{dead: map[string]bool{}}
	d2 := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"fake": f2}, Backoff: time.Minute, BackoffMax: 5 * time.Minute}
	must(t, d2.Reconcile(ctx))
	if n := len(f2.starts()); n != 0 {
		t.Fatalf("a restarted dispatcher retried at once: %d starts", n)
	}
	_, err = s.SetHold(ctx, "yard-e", true, store.Expect{}, "human")
	must(t, err)
	_, err = s.SetHold(ctx, "yard-e", false, store.Expect{}, "human")
	must(t, err)
	must(t, d2.Reconcile(ctx))
	if n := len(f2.starts()); n != 1 {
		t.Fatalf("not retried after an un-hold: %d starts", n)
	}
}

// A part is started only as a child of an epic. One that lost its epic
// after it moved on, or that no flow governs, is not handed to an agent or
// a script, and the reason is logged once; a manual group still gets it, as
// someone has to see it to fix it.
func TestPartWithoutEpicIsNotStarted(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutRig(ctx, store.Rig{Name: "yard", Base: "main"}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}))
	people := &fakeRunner{dead: map[string]bool{}}
	d.Runners["manual"] = people
	must(t, s.PutGroup(ctx, store.Group{Name: "yardmaster", Runner: "manual", Limit: 5}))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"})
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "backlog", Group: "yardmaster"})
	must(t, err)
	for _, nb := range []store.NewBead{
		{ID: "yard-e", Title: "epic", Type: store.TypeEpic, Rig: "yard", Stage: "open"},
		{ID: "yard-1", Title: "a part of the epic", Type: store.TypePart, Rig: "yard"},
		{ID: "yard-2", Title: "a part that lost its epic", Type: store.TypePart, Rig: "yard"},
		{ID: "yard-3", Title: "a part that never had one", Type: store.TypePart, Rig: "yard"},
		{ID: "yard-4", Title: "a part with no epic, in backlog", Type: store.TypePart, Rig: "yard", Stage: "backlog"},
		{ID: "yard-5", Title: "a task", Rig: "yard"},
	} {
		_, err := s.CreateBead(ctx, nb, "me")
		must(t, err)
	}
	must(t, s.AddDep(ctx, "yard-e", "yard-1", store.DepParent, "me"))
	must(t, s.AddDep(ctx, "yard-e", "yard-2", store.DepParent, "me"))
	must(t, s.RemoveDep(ctx, "yard-e", "yard-2", store.DepParent, "me"))

	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	got := f.starts()
	slices.Sort(got)
	if want := []string{"yard-1", "yard-5"}; !slices.Equal(got, want) {
		t.Fatalf("started %v, want %v", got, want)
	}
	for _, id := range []string{"yard-2", "yard-3"} {
		evs := eventKinds(t, s, id, "part_without_epic")
		if len(evs) != 1 || !strings.Contains(fmt.Sprint(evs[0].Data["reason"]), "yardr dep add <epic> "+id+" -k parent") {
			t.Fatalf("part_without_epic events for %s = %+v, want one saying what to do", id, evs)
		}
	}
	if evs := eventKinds(t, s, "yard-4", "part_without_epic"); len(evs) != 0 {
		t.Fatalf("a part waiting with people was reported: %+v", evs)
	}
	if got := people.starts(); !slices.Equal(got, []string{"yard-4"}) {
		t.Fatalf("people were handed %v, want the part in backlog", got)
	}
}
