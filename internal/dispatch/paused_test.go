package dispatch_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// A group at limit 0 is paused: it claims nothing new, while its running
// session carries on and advances its bead; raising the limit resumes it.
// A bead that waits for its paused group gets no event, as a bead at its
// group's limit gets none.
func TestGroupLimitZeroPauses(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 1}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)

	t1, _ := s.CreateBead(ctx, store.NewBead{Title: "t1"}, "me")
	t2, _ := s.CreateBead(ctx, store.NewBead{Title: "t2"}, "me")
	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{t1.ID}) {
		t.Fatalf("starts = %v, want %v", got, []string{t1.ID})
	}

	zero := 0
	_, err = s.SetGroup(ctx, "builders", store.GroupEdit{Limit: &zero}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{t1.ID}) {
		t.Fatalf("starts under a pause = %v, want nothing new", got)
	}
	if n := len(cappedEvents(t, s, t2.ID)); n != 0 {
		t.Errorf("%d capped events on the bead that waits for its group, want none", n)
	}

	// The running session carries on: its bead advances.
	if _, err := s.Advance(ctx, t1.ID, "review", store.Expect{}, "me"); err != nil {
		t.Fatalf("advance of the running bead: %v", err)
	}

	one := 1
	_, err = s.SetGroup(ctx, "builders", store.GroupEdit{Limit: &one}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{t1.ID, t2.ID}) {
		t.Fatalf("starts after the resume = %v", got)
	}
}

// max_sessions 0 pauses the yard: no herdr or exec session starts,
// whatever runs, while a manual group's claims go through as ever. Each
// waiting bead gets one capped event naming the pause; unsetting the
// setting resumes.
func TestMaxSessionsZeroPauses(t *testing.T) {
	s, f, d := setup(t)
	exec := &fakeRunner{dead: map[string]bool{}}
	people := &fakeRunner{dead: map[string]bool{}}
	d.Runners["herdr"] = dispatch.Runner(f)
	d.Runners["exec"] = dispatch.Runner(exec)
	d.Runners["manual"] = dispatch.Runner(people)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 3}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "landers", Runner: "exec", Limit: 3, Config: map[string]any{"cmd": "true"}}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 5}, "me"))
	land, ask := "land", "ask"
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Type: &land, Group: "landers"}, "me")
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Type: &ask, Group: "decisions"}, "me")
	must(t, err)
	must(t, s.SetSetting(ctx, store.MaxSessionsSetting, "0", "me"))

	t1, _ := s.CreateBead(ctx, store.NewBead{Title: "t1"}, "me")
	l1, _ := s.CreateBead(ctx, store.NewBead{Title: "l1", Type: "land"}, "me")
	q1, _ := s.CreateBead(ctx, store.NewBead{Title: "q1", Type: "ask"}, "me")

	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if got := f.starts(); len(got) != 0 {
		t.Errorf("herdr starts under a pause = %v, want none", got)
	}
	if got := exec.starts(); len(got) != 0 {
		t.Errorf("exec starts under a pause = %v, want none", got)
	}
	if got := people.starts(); !slices.Equal(got, []string{q1.ID}) {
		t.Errorf("manual claims under a pause = %v, want %v", got, []string{q1.ID})
	}
	for _, id := range []string{t1.ID, l1.ID} {
		evs := cappedEvents(t, s, id)
		if len(evs) != 1 {
			t.Errorf("capped events on %s after 2 passes = %d, want 1", id, len(evs))
			continue
		}
		if reason, _ := evs[0].Data["reason"].(string); !strings.Contains(reason, "(paused)") || evs[0].Data["stage"] != "new" {
			t.Errorf("capped event on %s = %v, want the pause named", id, evs[0].Data)
		}
	}
	if n := len(cappedEvents(t, s, q1.ID)); n != 0 {
		t.Errorf("%d capped events on %s, which a person holds", n, q1.ID)
	}

	// Unset: the beads that waited start on the next pass.
	must(t, s.SetSetting(ctx, store.MaxSessionsSetting, "", "me"))
	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{t1.ID}) {
		t.Errorf("herdr starts after unsetting = %v", got)
	}
	if got := exec.starts(); !slices.Equal(got, []string{l1.ID}) {
		t.Errorf("exec starts after unsetting = %v", got)
	}
}
