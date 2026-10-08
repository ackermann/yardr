package dispatch_test

import (
	"context"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// A bead that has been in a stage of a manual group for longer than the
// group's wait raises waited once, by the daemon's clock, whether the group
// holds it or is at its limit, and the yardmaster has it in its inbox. A
// manual group with no wait raises nothing, as before (yardr-bygh).
func TestBeadThatWaitsLongInAManualGroupRaisesWaitedOnce(t *testing.T) {
	s, _, d := crewSetup(t, store.CrewMember{On: []string{store.KindAttention}})
	d.Runners["manual"] = &fakeRunner{dead: map[string]bool{}}
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Limit: 1, Config: map[string]any{"wait": "1h"}}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "shapers", Runner: "manual"}, "me"))
	for stage, group := range map[string]string{"decide": "deciders", "backlog": "shapers"} {
		_, err := s.AddRoute(ctx, store.Route{Stage: stage, Group: group}, "me")
		must(t, err)
	}
	bead := func(title, stage string) *store.Bead {
		t.Helper()
		b, err := s.CreateBead(ctx, store.NewBead{Title: title, Stage: stage}, "me")
		must(t, err)
		return b
	}
	held, queued, quiet := bead("held by the group", "decide"), bead("past the group's limit", "decide"), bead("no wait", "backlog")
	start := time.Now()
	now := start
	dispatch.SetClock(d, func() time.Time { return now })
	waited := func(b *store.Bead) []store.Event { return eventKinds(t, s, b.ID, dispatch.Waited) }
	counts := func(want int, when string) {
		t.Helper()
		if h, q, n := len(waited(held)), len(waited(queued)), len(waited(quiet)); h != want || q != want || n != 0 {
			t.Fatalf("%s: waited raised %d times on the held bead, %d on the queued, want %d each; %d on the one whose group sets no wait, want 0",
				when, h, q, want, n)
		}
	}

	must(t, d.Reconcile(ctx))
	if b, err := s.GetBead(ctx, queued.ID); err != nil || b.Group != nil {
		t.Fatalf("the second bead is not left unclaimed by a group at its limit: %+v, %v", b, err)
	}
	now = start.Add(59 * time.Minute)
	must(t, d.Reconcile(ctx))
	counts(0, "before the hour")

	now = start.Add(61 * time.Minute)
	must(t, d.Reconcile(ctx))
	counts(1, "after the hour")
	e := waited(held)[0]
	since, err := time.Parse(time.RFC3339, e.Data["since"].(string))
	if err != nil || since.Sub(held.CreatedAt).Abs() > time.Minute || e.Data["group"] != "deciders" || e.Data["stage"] != "decide" {
		t.Fatalf("waited = %+v, %v; want group, stage and since when the bead was made", e.Data, err)
	}
	if got, err := time.ParseDuration(e.Data["waited"].(string)); err != nil || got < time.Hour || got > 62*time.Minute {
		t.Fatalf("waited says %v (%v), want a little over the hour", e.Data["waited"], err)
	}
	page, err := s.Inbox(ctx, "yardmaster", 0)
	must(t, err)
	inbox := 0
	for _, e := range page.Events {
		if e.Kind == dispatch.Waited {
			inbox++
		}
	}
	if inbox != 2 {
		t.Fatalf("the yardmaster, who hears the class attention, has %d waited in its inbox, want 2: %+v", inbox, page.Events)
	}

	// Not again: on a later pass, a day on, or by a daemon that has just
	// started and remembers nothing.
	must(t, d.Reconcile(ctx))
	now = start.Add(25 * time.Hour)
	must(t, d.Reconcile(ctx))
	counts(1, "a day later")
	fresh := &dispatch.Dispatcher{Store: s, Runners: d.Runners, PromptSettle: -1, PromptWait: -1}
	dispatch.SetClock(fresh, func() time.Time { return now })
	must(t, fresh.Reconcile(ctx))
	counts(1, "after the daemon was replaced")

	// A bead that leaves the stage and comes back begins a new stay: it is
	// told of again, and only once that stay is longer than the wait.
	for _, stage := range []string{"backlog", "decide"} {
		_, err := s.Advance(ctx, held.ID, stage, store.Expect{}, "me")
		must(t, err)
	}
	back := time.Now()
	now = back.Add(59 * time.Minute)
	must(t, d.Reconcile(ctx))
	if n := len(waited(held)); n != 1 {
		t.Fatalf("waited raised %d times on a bead that came back to the stage 59m ago, want the 1 of its first stay", n)
	}
	now = back.Add(61 * time.Minute)
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	again := waited(held)
	if len(again) != 2 {
		t.Fatalf("waited raised %d times on a bead in its second stay past the wait, want 2", len(again))
	}
	if second, err := time.Parse(time.RFC3339, again[1].Data["since"].(string)); err != nil || second.Before(back.Add(-time.Minute)) {
		t.Fatalf("the second waited counts from %v (%v), want from when the bead came back, %v", again[1].Data["since"], err, back)
	}
	if n := len(waited(quiet)); n != 0 {
		t.Fatalf("waited raised %d times in a manual group with no wait", n)
	}
}
