package dispatch_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// quotaYard is a yard with two groups that start agents, `a` of kind kimi
// (whose windows the stand-in source answers for) and `b` of kind codex,
// and one route for stage new that names first and then the further groups.
func quotaYard(t *testing.T, first string, further ...string) (store.API, *fakeRunner, *dispatch.Dispatcher, *standIn, *store.Route, *time.Time) {
	t.Helper()
	s := storetest.New(t)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": f}, Backoff: time.Nanosecond}
	now := noon()
	dispatch.SetClock(d, func() time.Time { return now })
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "a", Runner: "herdr", Limit: 5, Config: map[string]any{"kind": "kimi"}}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "b", Runner: "herdr", Limit: 5, Config: map[string]any{"kind": "codex"}}, "me"))
	p := newStandIn(t, s)
	g := store.ParseRouteGroup(first)
	r := store.Route{Stage: "new", Group: g.Name, When: g.When}
	for _, e := range further {
		r.Else = append(r.Else, store.ParseRouteGroup(e))
	}
	saved, err := s.AddRoute(ctx, r, "me")
	must(t, err)
	return s, f, d, p, saved, &now
}

// weekly is a report with the Weekly window at week percent and the 5h
// window at five, resetting three days and one hour after noon.
func weekly(week, five float64) string {
	return fmt.Sprintf(`{"provider":"Kimi","windows":[`+
		`{"label":"5h","used_percent":%v,"resets_at":%q},{"label":"Weekly","used_percent":%v,"resets_at":%q}]}`,
		five, noon().Add(time.Hour).Format(time.RFC3339), week, noon().Add(72*time.Hour).Format(time.RFC3339))
}

func newBead(t *testing.T, s store.API, title string) store.Bead {
	t.Helper()
	b, err := s.CreateBead(context.Background(), store.NewBead{Title: title}, "me")
	must(t, err)
	return *b
}

// claimedBy is the group that has the bead, "" for none, and its claimed
// event's data.
func claimedBy(t *testing.T, s store.API, id string) (string, map[string]any) {
	t.Helper()
	b, err := s.GetBead(context.Background(), id)
	must(t, err)
	if b.Group == nil {
		if evs := eventKinds(t, s, id, "claimed"); len(evs) != 0 {
			t.Fatalf("bead %s has no group and a claimed event: %v", id, evs)
		}
		return "", nil
	}
	evs := eventKinds(t, s, id, "claimed")
	if len(evs) != 1 {
		t.Fatalf("claimed events of %s = %v, want one", id, evs)
	}
	return *b.Group, evs[0].Data
}

// A group whose provider's window is past the route's threshold is passed
// over for the next group of the route, and the claimed event says by
// which route and why; under the threshold the first group takes the bead.
func TestRouteTakesTheFirstGroupWhoseQuotaConditionHolds(t *testing.T) {
	ctx := context.Background()
	s, _, d, p, r, _ := quotaYard(t, "a@Weekly<80", "b")
	p.say(weekly(85, 10))
	b := newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	group, data := claimedBy(t, s, b.ID)
	if group != "b" || data["group"] != "b" || data["route"] != r.ID || data["limits_unknown"] != nil {
		t.Fatalf("claimed by %q with %v, want b by route %s", group, data, r.ID)
	}
	passed, _ := data["passed"].([]any)
	if len(passed) != 1 {
		t.Fatalf("passed = %v, want the one group", data["passed"])
	}
	if one, _ := passed[0].(map[string]any); one["group"] != "a" ||
		one["reason"] != "Kimi's Weekly window is 85% used, the route asks under 80" {
		t.Fatalf("passed = %v", one)
	}
	if evs := eventKinds(t, s, b.ID, "quota_wait"); len(evs) != 0 {
		t.Fatalf("quota_wait for a bead that started: %v", evs)
	}

	s, _, d, p, r, _ = quotaYard(t, "a@Weekly<80", "b")
	p.say(weekly(70, 10))
	b = newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	if group, data := claimedBy(t, s, b.ID); group != "a" || data["route"] != r.ID || data["passed"] != nil || data["limits_unknown"] != nil {
		t.Fatalf("at 70%%: claimed by %q with %v, want a, nobody passed over", group, data)
	}
}

// With no further group the bead waits for its group: it is not claimed,
// and one quota_wait event says why and when the first of the windows that
// failed resets, however many passes find it waiting and whatever the
// percentage moves to. It starts once the window is under the threshold.
func TestBeadWaitsWhileNoGroupsConditionHolds(t *testing.T) {
	ctx := context.Background()
	s, _, d, p, r, now := quotaYard(t, "a@Weekly<80,5h<50")
	p.say(weekly(85, 60))
	b := newBead(t, s, "x")
	for i := range 3 {
		must(t, d.Reconcile(ctx))
		// A later answer with other numbers is the same wait.
		*now = now.Add(6 * time.Minute)
		p.say(weekly(86+float64(i), 60))
	}
	if group, _ := claimedBy(t, s, b.ID); group != "" {
		t.Fatalf("claimed by %q, want nobody: a's windows are past the thresholds", group)
	}
	evs := eventKinds(t, s, b.ID, "quota_wait")
	if len(evs) != 1 {
		t.Fatalf("quota_wait events = %v, want one", evs)
	}
	data := evs[0].Data
	reason, _ := data["reason"].(string)
	if data["route"] != r.ID || data["stage"] != "new" ||
		reason != "a passed over (Kimi's Weekly window is 85% used, the route asks under 80; Kimi's 5h window is 60% used, the route asks under 50)" {
		t.Fatalf("quota_wait = %v", data)
	}
	// The 5h window resets first.
	if want := noon().Add(time.Hour).UTC().Format(time.RFC3339); data["resets_at"] != want {
		t.Fatalf("resets_at = %v, want the earliest reset %s", data["resets_at"], want)
	}

	p.say(weekly(20, 10))
	*now = now.Add(6 * time.Minute)
	must(t, d.Reconcile(ctx))
	if group, _ := claimedBy(t, s, b.ID); group != "a" {
		t.Fatalf("claimed by %q once the windows opened, want a", group)
	}
}

// A group at its limit of sessions is waited for, not passed over: only a
// quota condition sends a bead to a further group.
func TestGroupAtItsLimitIsWaitedForNotPassedOver(t *testing.T) {
	ctx := context.Background()
	s, f, d, p, _, _ := quotaYard(t, "a@Weekly<80", "b")
	must(t, s.PutGroup(ctx, store.Group{Name: "a", Runner: "herdr", Limit: 1, Config: map[string]any{"kind": "kimi"}}, "me"))
	p.say(weekly(70, 10))
	one, two := newBead(t, s, "one"), newBead(t, s, "two")
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	if got := f.starts(); len(got) != 1 {
		t.Fatalf("starts = %v, want one: a is at its limit", got)
	}
	groups := []string{}
	for _, id := range []string{one.ID, two.ID} {
		g, _ := claimedBy(t, s, id)
		groups = append(groups, g)
	}
	if strings.Join(groups, ",") != "a," && strings.Join(groups, ",") != ",a" {
		t.Fatalf("groups of the two beads = %q, want one with a and one waiting", groups)
	}
}

// `@<80` reads every window the provider reports.
func TestConditionWithoutALabelReadsEveryWindow(t *testing.T) {
	ctx := context.Background()
	s, _, d, p, _, _ := quotaYard(t, "a@<80", "b")
	p.say(weekly(10, 90))
	b := newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	group, data := claimedBy(t, s, b.ID)
	if group != "b" || !strings.Contains(fmt.Sprint(data["passed"]), "Kimi's 5h window is 90% used, the route asks under 80") {
		t.Fatalf("claimed by %q with %v, want b: the 5h window is past 80", group, data)
	}
}

// A condition that cannot be evaluated holds, as a group with none does,
// and the claimed event says the limits were not known: a limits command
// that fails takes the gate away and stops nothing.
func TestConditionThatCannotBeEvaluatedHolds(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		first  string
		break_ func(*testing.T, store.API, *standIn)
		want   string
	}{
		"the command failed": {"a@Weekly<80", func(_ *testing.T, _ store.API, p *standIn) { p.fail() }, "not logged in"},
		"no such window":     {"a@Monthly<80", func(_ *testing.T, _ store.API, p *standIn) { p.say(weekly(99, 99)) }, "Kimi's report has no window labelled Monthly"},
		"no windows":         {"a@<80", func(*testing.T, store.API, *standIn) {}, "Kimi's report has no windows"},
		"no source": {"a@Weekly<80", func(t *testing.T, s store.API, _ *standIn) {
			must(t, s.PutGroup(ctx, store.Group{Name: "a", Runner: "herdr", Limit: 5, Config: map[string]any{"kind": "claude"}}, "me"))
		}, "no source of this yard says the limits of claude"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _, d, p, _, _ := quotaYard(t, tc.first, "b")
			tc.break_(t, s, p)
			b := newBead(t, s, "x")
			must(t, d.Reconcile(ctx))
			group, data := claimedBy(t, s, b.ID)
			unknown, _ := data["limits_unknown"].(string)
			if group != "a" || data["passed"] != nil || !strings.HasPrefix(unknown, "the limits of a were not known: ") || !strings.Contains(unknown, tc.want) {
				t.Fatalf("claimed by %q with %v, want a with limits_unknown saying %q", group, data, tc.want)
			}
		})
	}
}

// The limits command runs at most once in five minutes for a provider,
// however many beads are ready and however many passes there are, an
// answer that failed included; and never for a route with no condition.
func TestLimitsAreAskedOnceInFiveMinutesAndOnlyForACondition(t *testing.T) {
	ctx := context.Background()
	s, f, d, p, _, now := quotaYard(t, "a@Weekly<80", "b")
	p.say(weekly(70, 10))
	for i := range 4 {
		newBead(t, s, fmt.Sprint("b", i))
	}
	for range 3 {
		must(t, d.Reconcile(ctx))
	}
	newBead(t, s, "later")
	*now = now.Add(4 * time.Minute)
	must(t, d.Reconcile(ctx))
	if got := f.starts(); len(got) != 5 {
		t.Fatalf("starts = %v, want all five", got)
	}
	if asks := p.asks(); len(asks) != 1 || asks[0] != "moonshot" {
		t.Fatalf("the limits command ran for %v, want once, for a's provider", asks)
	}
	// Nothing is ready: the answer may grow old unasked.
	*now = now.Add(time.Hour)
	must(t, d.Reconcile(ctx))
	if asks := p.asks(); len(asks) != 1 {
		t.Fatalf("the limits command ran %d times with nothing ready, want 1", len(asks))
	}
	p.fail()
	for i := range 3 {
		newBead(t, s, fmt.Sprint("c", i))
		must(t, d.Reconcile(ctx))
		*now = now.Add(time.Minute)
	}
	if asks := p.asks(); len(asks) != 2 {
		t.Fatalf("the limits command ran %d times, want 2: a failure is remembered as an answer is", len(asks))
	}

	s, f, d, p, _, _ = quotaYard(t, "a", "b")
	newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	if got, asks := f.starts(), p.asks(); len(got) != 1 || len(asks) != 0 {
		t.Fatalf("a route with no condition: starts %v, limits command ran for %v, want one start and no run", got, asks)
	}
}

// A route with one group and no condition claims as it always did; its
// claimed event names the route beside what it carried before.
func TestRouteWithoutAConditionDispatchesAsBefore(t *testing.T) {
	ctx := context.Background()
	s, f, d := setup(t)
	must(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake"}, "me"))
	r, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(t, err)
	b := newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	group, data := claimedBy(t, s, b.ID)
	if group != "builders" || len(f.starts()) != 1 {
		t.Fatalf("claimed by %q, starts %v", group, f.starts())
	}
	if len(data) != 4 || data["group"] != "builders" || data["stage"] != "new" || data["session"] == nil || data["route"] != r.ID {
		t.Fatalf("claimed = %v, want group, stage, session and the route", data)
	}
}

// A stage a bead is not in yet has no chosen group: the job of a bead whose
// next stage has a route with several groups names them in order, the flow
// map says agents work it, and neither runs the limits command.
func TestNextStageNamesARoutesGroupsInOrderAndAsksNothing(t *testing.T) {
	ctx := context.Background()
	s := storetest.New(t)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": f, "fake": f}, Backoff: time.Nanosecond}
	must(t, s.PutDepot(ctx, store.Depot{Name: "web", Path: "/tmp/web"}, "me"))
	flow, err := store.ParseFlow("draft -> new on done\nnew -> merged\nmerged terminal\n")
	must(t, err)
	flow.Name = "web"
	_, err = s.PutFlow(ctx, *flow, "me")
	must(t, err)
	must(t, s.SetFlowPointer(ctx, "web", "", "web", "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "drafters", Runner: "fake"}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "a", Runner: "herdr", Config: map[string]any{"kind": "kimi"}}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "b", Runner: "herdr", Config: map[string]any{"kind": "codex"}}, "me"))
	p := newStandIn(t, s)
	p.say(weekly(85, 10))
	_, err = s.AddRoute(ctx, store.Route{Stage: "draft", Group: "drafters"}, "me")
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "a", When: "Weekly<80", Else: []store.RouteGroup{{Name: "b"}}}, "me")
	must(t, err)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: "draft"}, "me")
	must(t, err)
	must(t, d.Reconcile(ctx))

	f.mu.Lock()
	jobs := append([]dispatch.Job{}, f.jobs...)
	f.mu.Unlock()
	if len(jobs) != 1 || jobs[0].Bead.ID != b.ID {
		t.Fatalf("jobs = %+v, want the one bead's", jobs)
	}
	want := dispatch.NextStage{Stage: "new", Group: "a", Runner: "herdr", Else: []string{"b"}}
	if got := jobs[0].Outcomes; len(got) != 1 || got[0].Outcome != "done" || !reflect.DeepEqual(got[0].NextStage, want) {
		t.Fatalf("outcomes = %+v, want done to %+v", got, want)
	}
	fm := jobs[0].FlowMap
	if fm == nil || len(fm.Stages) != 3 || fm.Stages[1].Name != "new" || fm.Stages[1].Who != store.WhoAgents {
		t.Fatalf("flow map = %+v, want agents at new", fm)
	}
	if asks := p.asks(); len(asks) != 0 {
		t.Fatalf("the limits command ran for %v: nothing was ready at a condition", asks)
	}
}
