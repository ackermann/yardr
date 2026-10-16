package dispatch_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// cappedEvents are the capped events on bead id.
func cappedEvents(t *testing.T, s store.API, id string) []store.Event {
	t.Helper()
	evs, err := s.Events(context.Background(), id, 0)
	must(t, err)
	return slices.DeleteFunc(evs, func(e store.Event) bool { return e.Kind != "capped" })
}

// max_sessions is the machine's number, whatever the groups allow
// (yardr-33s8): two groups of limit 3 each start two sessions between them
// with max_sessions 2, the third ready bead waits and says why once, and
// raising the setting starts it on the next pass, with no restart.
func TestMaxSessionsCapsAcrossGroups(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 3}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "bugs", Runner: "fake", Limit: 3}, "me"))
	bug := "bug"
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Type: &bug, Group: "bugs"}, "me")
	must(t, err)
	must(t, s.SetSetting(ctx, store.MaxSessionsSetting, "2", "me"))

	t1, _ := s.CreateBead(ctx, store.NewBead{Title: "t1"}, "me")
	b1, _ := s.CreateBead(ctx, store.NewBead{Title: "b1", Type: "bug"}, "me")
	t2, _ := s.CreateBead(ctx, store.NewBead{Title: "t2"}, "me")

	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if got := f.starts(); !slices.Equal(got, []string{t1.ID, b1.ID}) {
		t.Fatalf("starts under max_sessions 2 = %v, want %v", got, []string{t1.ID, b1.ID})
	}
	evs := cappedEvents(t, s, t2.ID)
	if len(evs) != 1 {
		t.Fatalf("capped events on the waiting bead after 3 passes = %d, want 1", len(evs))
	}
	if reason, _ := evs[0].Data["reason"].(string); !strings.Contains(reason, "max_sessions (2)") || evs[0].Data["stage"] != "new" {
		t.Errorf("capped event = %v", evs[0].Data)
	}
	for _, id := range []string{t1.ID, b1.ID} {
		if n := len(cappedEvents(t, s, id)); n != 0 {
			t.Errorf("%d capped events on %s, which started", n, id)
		}
	}

	// Raised: the dispatcher that was running reads it on its next pass.
	must(t, s.SetSetting(ctx, store.MaxSessionsSetting, "3", "me"))
	must(t, d.Reconcile(ctx))
	if got := f.starts(); !slices.Equal(got, []string{t1.ID, b1.ID, t2.ID}) {
		t.Fatalf("starts after max_sessions 3 = %v", got)
	}
}

// A session that ends frees its place, and a bead that waited for the cap
// starts then; unset and 0 cap nothing, as before the setting.
func TestMaxSessionsFreedAndUnset(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, value string
		first       int
	}{{"set", "1", 1}, {"zero", "0", 3}, {"unset", "", 3}} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, d := setup(t)
			ctx := context.Background()
			must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 3}, "me"))
			_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
			must(t, err)
			if tc.value != "" {
				must(t, s.SetSetting(ctx, store.MaxSessionsSetting, tc.value, "me"))
			}
			var ids []string
			for _, title := range []string{"a", "b", "c"} {
				b, _ := s.CreateBead(ctx, store.NewBead{Title: title}, "me")
				ids = append(ids, b.ID)
			}
			must(t, d.Reconcile(ctx))
			if got := f.starts(); !slices.Equal(got, ids[:tc.first]) {
				t.Fatalf("starts = %v, want %v", got, ids[:tc.first])
			}
			if tc.first == 3 {
				for _, id := range ids {
					if n := len(cappedEvents(t, s, id)); n != 0 {
						t.Errorf("%d capped events on %s with no cap", n, id)
					}
				}
				return
			}
			_, err = s.CloseBead(ctx, ids[0], "done", store.Expect{}, "me")
			must(t, err)
			must(t, d.Reconcile(ctx))
			if got := f.starts(); !slices.Equal(got, ids[:2]) {
				t.Fatalf("starts after the first closed = %v, want %v", got, ids[:2])
			}
		})
	}
}

// A manual group's claim is a person's turn: it takes no place under
// max_sessions, and the cap does not keep a bead from reaching a person.
func TestMaxSessionsLeavesManualGroups(t *testing.T) {
	t.Parallel()
	s, f, d := setup(t)
	people := &fakeRunner{dead: map[string]bool{}}
	d.Runners["manual"] = dispatch.Runner(people)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 3}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 5}, "me"))
	ask := "ask"
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Type: &ask, Group: "decisions"}, "me")
	must(t, err)
	must(t, s.SetSetting(ctx, store.MaxSessionsSetting, "1", "me"))

	q1, _ := s.CreateBead(ctx, store.NewBead{Title: "q1", Type: "ask"}, "me")
	q2, _ := s.CreateBead(ctx, store.NewBead{Title: "q2", Type: "ask"}, "me")
	t1, _ := s.CreateBead(ctx, store.NewBead{Title: "t1"}, "me")
	t2, _ := s.CreateBead(ctx, store.NewBead{Title: "t2"}, "me")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if got := people.starts(); !slices.Equal(got, []string{q1.ID, q2.ID}) {
		t.Errorf("claims of the manual group = %v, want both", got)
	}
	if got := f.starts(); !slices.Equal(got, []string{t1.ID}) {
		t.Errorf("starts = %v, want %v: two manual claims take no place, and one session is the cap", got, []string{t1.ID})
	}
	if n := len(cappedEvents(t, s, t2.ID)); n != 1 {
		t.Errorf("capped events on the bead that waits = %d, want 1", n)
	}
}
