package dispatch_test

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/scripttest"
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
	t.Cleanup(func() { dispatch.WaitLimitsIdle(d) })
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

// weeklyResetting is a report with the Weekly window at week percent
// resetting the given time after noon, the dispatcher's clock in these tests.
func weeklyResetting(week float64, in time.Duration) string {
	return fmt.Sprintf(`{"provider":"Kimi","windows":[{"label":"Weekly","used_percent":%v,"resets_at":%q}]}`,
		week, noon().Add(in).Format(time.RFC3339))
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

// A quota condition gates on the conditioned group's own login: two groups
// of one kind on different logins read different windows, and the bead goes
// by the first group's, not the ambient answer. Each login is asked once,
// whatever the other reads.
func TestQuotaConditionGatesOnTheGroupsOwnWindows(t *testing.T) {
	// Serial: it changes process environment, which parallel tests cannot share.
	ctx := context.Background()
	t.Setenv("YARDR_TEST_LOGIN", "operator")
	s := storetest.New(t)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": f}, Backoff: time.Nanosecond}
	now := noon()
	dispatch.SetClock(d, func() time.Time { return now })
	aEnv := loginEnv(t, "a-login")
	must(t, s.PutGroup(ctx, store.Group{Name: "a", Runner: "herdr", Limit: 5,
		Config: map[string]any{"kind": "kimi", "env_file": aEnv}}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "b", Runner: "herdr", Limit: 5,
		Config: map[string]any{"kind": "kimi", "env_file": loginEnv(t, "b-login")}}, "me"))
	p := newLoginStandIn(t, s)
	p.sayAs("a-login", weekly(85, 10))
	p.sayAs("b-login", weekly(10, 10))
	p.sayAs("operator", weekly(10, 10))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "a", When: "Weekly<80",
		Else: []store.RouteGroup{{Name: "b", When: "Weekly<80"}}}, "me")
	must(t, err)

	primeGroupLimits(t, s, d, "a")
	primeGroupLimits(t, s, d, "b")
	b := newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	group, data := claimedBy(t, s, b.ID)
	if group != "b" {
		t.Fatalf("claimed by %q with %v, want b: a's own login is past the threshold", group, data)
	}
	passed, _ := data["passed"].([]any)
	if len(passed) != 1 {
		t.Fatalf("passed = %v, want the one group", data["passed"])
	}
	if one, _ := passed[0].(map[string]any); one["group"] != "a" ||
		one["reason"] != "Kimi's Weekly window is 85% used, the route asks under 80" {
		t.Fatalf("passed = %v, want a passed over on its own 85%%", one)
	}
	if got, want := p.asks(), []string{"moonshot a-login", "moonshot b-login"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("asks = %v, want %v: each login read once, the ambient answer never", got, want)
	}

	// Within the five minutes the answers are kept, per login: a bead
	// more asks nothing.
	b = newBead(t, s, "y")
	must(t, d.Reconcile(ctx))
	if group, _ := claimedBy(t, s, b.ID); group != "b" {
		t.Fatalf("claimed by %q, want b", group)
	}
	if got := p.asks(); len(got) != 2 {
		t.Fatalf("asks = %v, want no further run within the five minutes", got)
	}

	// Changing the env_file in place changes which login this group burns,
	// so a fresh answer is needed even inside the old login's cache window.
	if err := os.WriteFile(aEnv, []byte("YARDR_TEST_LOGIN=a-login-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.sayAs("a-login-2", weekly(10, 10))
	primeGroupLimits(t, s, d, "a")
	b = newBead(t, s, "login changed")
	must(t, d.Reconcile(ctx))
	if group, _ := claimedBy(t, s, b.ID); group != "a" {
		t.Fatalf("claimed by %q, want a after the group's login changes", group)
	}
	if got, want := p.asks(), []string{"moonshot a-login", "moonshot b-login", "moonshot a-login-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("asks = %v, want a fresh read under the group's new login", got)
	}

	// After them a's windows open again, and the bead goes to a on its
	// own answer, read once more.
	now = now.Add(6 * time.Minute)
	p.sayAs("a-login-2", weekly(10, 10))
	primeGroupLimits(t, s, d, "a")
	b = newBead(t, s, "z")
	must(t, d.Reconcile(ctx))
	if group, _ := claimedBy(t, s, b.ID); group != "a" {
		t.Fatalf("claimed by %q, want a", group)
	}
	if got, want := p.asks(), []string{"moonshot a-login", "moonshot b-login", "moonshot a-login-2", "moonshot a-login-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("asks = %v, want %v", got, want)
	}
}

// Groups carrying the same env overlay read the same login and share the
// cached answer, so a route does not ask the provider twice for it.
func TestQuotaMemoryIsSharedAcrossGroupsWithTheSameLogin(t *testing.T) {
	// Serial: it changes process environment, which parallel tests cannot share.
	ctx := context.Background()
	t.Setenv("YARDR_TEST_LOGIN", "operator")
	s := storetest.New(t)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": f}, Backoff: time.Nanosecond}
	shared := loginEnv(t, "shared-login")
	for _, name := range []string{"a", "b"} {
		must(t, s.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Limit: 5,
			Config: map[string]any{"kind": "kimi", "env_file": shared}}, "me"))
	}
	p := newLoginStandIn(t, s)
	p.sayAs("shared-login", weekly(85, 10))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "a", When: "Weekly<80",
		Else: []store.RouteGroup{{Name: "b", When: "Weekly<80"}}}, "me")
	must(t, err)
	primeGroupLimits(t, s, d, "a")
	b := newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	if group, _ := claimedBy(t, s, b.ID); group != "" {
		t.Fatalf("claimed by %q, want both groups to wait on the shared login", group)
	}
	if got, want := p.asks(), []string{"moonshot shared-login"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("asks = %v, want one read shared by both groups", got)
	}
}

// A group whose provider's window is past the route's threshold is passed
// over for the next group of the route, and the claimed event says by
// which route and why; under the threshold the first group takes the bead.
func TestRouteTakesTheFirstGroupWhoseQuotaConditionHolds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, d, p, r, _ := quotaYard(t, "a@Weekly<80", "b")
	p.say(weekly(85, 10))
	primeGroupLimits(t, s, d, "a")
	primeGroupLimits(t, s, d, "b")
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
	primeGroupLimits(t, s, d, "a")
	b = newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	if group, data := claimedBy(t, s, b.ID); group != "a" || data["route"] != r.ID || data["passed"] != nil || data["limits_unknown"] != nil {
		t.Fatalf("at 70%%: claimed by %q with %v, want a, nobody passed over", group, data)
	}

	// A comparison that ends in a duration holds a window past its number
	// that resets within it: a takes the bead at 90% resetting in 20 hours,
	// and the claimed event says which window was held and for what.
	s, _, d, p, r, _ = quotaYard(t, "a@Weekly<80/1d", "b")
	p.say(weeklyResetting(90, 20*time.Hour))
	primeGroupLimits(t, s, d, "a")
	b = newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	if group, data := claimedBy(t, s, b.ID); group != "a" || data["route"] != r.ID || data["passed"] != nil || data["limits_unknown"] != nil ||
		!reflect.DeepEqual(data["held"], []any{"Weekly 90% but resets in 20h"}) {
		t.Fatalf("at 90%% resetting in 20h: claimed by %q with %v, want a, the Weekly window held", group, data)
	}

	// Three days away it is not soon: the group is passed over for its
	// number, as it would be without the duration, and nothing is held.
	s, _, d, p, r, _ = quotaYard(t, "a@Weekly<80/1d", "b")
	p.say(weekly(90, 10))
	primeGroupLimits(t, s, d, "a")
	primeGroupLimits(t, s, d, "b")
	b = newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	group, data = claimedBy(t, s, b.ID)
	if group != "b" || data["route"] != r.ID || data["held"] != nil || data["limits_unknown"] != nil {
		t.Fatalf("at 90%% resetting in 3 days: claimed by %q with %v, want b by route %s, nothing held", group, data, r.ID)
	}
	passed, _ = data["passed"].([]any)
	if len(passed) != 1 {
		t.Fatalf("passed = %v, want the one group", data["passed"])
	}
	if one, _ := passed[0].(map[string]any); one["group"] != "a" ||
		one["reason"] != "Kimi's Weekly window is 90% used, the route asks under 80" {
		t.Fatalf("passed = %v", one)
	}
}

// With no further group the bead waits for its group: it is not claimed,
// and one quota_wait event says why and when the first of the windows that
// failed resets, however many passes find it waiting and whatever the
// percentage moves to. It starts once the window is under the threshold.
func TestBeadWaitsWhileNoGroupsConditionHolds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, d, p, r, now := quotaYard(t, "a@Weekly<80,5h<50")
	p.say(weekly(85, 60))
	primeGroupLimits(t, s, d, "a")
	b := newBead(t, s, "x")
	for i := range 3 {
		must(t, d.Reconcile(ctx))
		// A later answer with other numbers is the same wait.
		*now = now.Add(6 * time.Minute)
		p.say(weekly(86+float64(i), 60))
		primeGroupLimits(t, s, d, "a")
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
	primeGroupLimits(t, s, d, "a")
	must(t, d.Reconcile(ctx))
	if group, _ := claimedBy(t, s, b.ID); group != "a" {
		t.Fatalf("claimed by %q once the windows opened, want a", group)
	}
	t.Run("a plain route waits while a provider window is used up", func(t *testing.T) {
		ctx := context.Background()
		s, f, d, p, r, now := quotaYard(t, "a")
		primeGroupLimits(t, s, d, "a")
		first := newBead(t, s, "no limit")
		must(t, d.Reconcile(ctx))
		if group, data := claimedBy(t, s, first.ID); group != "a" || data["route"] != r.ID || data["passed"] != nil || data["limits_unknown"] != nil {
			t.Fatalf("claimed by %q with %v, want a by the route, with nothing said of limits: the provider reports no window", group, data)
		}
		*now = now.Add(6 * time.Minute)
		p.say(weekly(100, 10))
		primeGroupLimits(t, s, d, "a")
		b := newBead(t, s, "x")
		for range 3 {
			must(t, d.Reconcile(ctx))
		}
		if group, _ := claimedBy(t, s, b.ID); group != "" || !reflect.DeepEqual(f.starts(), []string{first.ID}) {
			t.Fatalf("claimed by %q, starts %v, want nobody new: a's Weekly window is used up", group, f.starts())
		}
		evs := eventKinds(t, s, b.ID, "quota_wait")
		if len(evs) != 1 {
			t.Fatalf("quota_wait events = %v, want one", evs)
		}
		reason, _ := evs[0].Data["reason"].(string)
		if evs[0].Data["route"] != r.ID || reason != "a passed over (Kimi's Weekly window is 100% used, the route asks under 100)" ||
			evs[0].Data["resets_at"] != noon().Add(72*time.Hour).UTC().Format(time.RFC3339) {
			t.Fatalf("quota_wait = %v, want the window and the Weekly window's reset", evs[0].Data)
		}

		// Just under 100% is not used up.
		*now = now.Add(6 * time.Minute)
		p.say(weekly(99.9, 10))
		primeGroupLimits(t, s, d, "a")
		must(t, d.Reconcile(ctx))
		group, data := claimedBy(t, s, b.ID)
		if group != "a" || data["route"] != r.ID || data["passed"] != nil || data["limits_unknown"] != nil {
			t.Fatalf("claimed by %q with %v, want a by the route, nobody passed over", group, data)
		}

		// A window the source says counts for one model alone holds no group
		// with no condition, however far it is used: a bead goes to a. A shared
		// window used up beside it still holds, and the wait names that one.
		perModel := func(week float64) string {
			return fmt.Sprintf(`{"provider":"Kimi","windows":[{"label":"Weekly","used_percent":%v},`+
				`{"label":"Weekly Fable","model":"Fable","used_percent":100}]}`, week)
		}
		*now = now.Add(6 * time.Minute)
		p.say(perModel(10))
		primeGroupLimits(t, s, d, "a")
		c := newBead(t, s, "y")
		must(t, d.Reconcile(ctx))
		if group, data := claimedBy(t, s, c.ID); group != "a" || data["route"] != r.ID || data["passed"] != nil || data["limits_unknown"] != nil {
			t.Fatalf("claimed by %q with %v, want a by the route: only a per-model window is used up", group, data)
		}
		*now = now.Add(6 * time.Minute)
		p.say(perModel(100))
		primeGroupLimits(t, s, d, "a")
		e := newBead(t, s, "z")
		must(t, d.Reconcile(ctx))
		if group, _ := claimedBy(t, s, e.ID); group != "" {
			t.Fatalf("claimed by %q, want nobody: a's Weekly window is used up beside the per-model one", group)
		}
		evs = eventKinds(t, s, e.ID, "quota_wait")
		if len(evs) != 1 {
			t.Fatalf("quota_wait events = %v, want one", evs)
		}
		if reason, _ := evs[0].Data["reason"].(string); reason != "a passed over (Kimi's Weekly window is 100% used, the route asks under 100)" {
			t.Fatalf("quota_wait reason = %q, want the Weekly window alone", reason)
		}
	})
	t.Run("a reset beyond the route's duration waits, one within it starts", func(t *testing.T) {
		ctx := context.Background()
		s, _, d, p, r, now := quotaYard(t, "a@Weekly<80/1d")
		p.say(weeklyResetting(90, 72*time.Hour))
		primeGroupLimits(t, s, d, "a")
		b := newBead(t, s, "x")
		must(t, d.Reconcile(ctx))
		if group, _ := claimedBy(t, s, b.ID); group != "" {
			t.Fatalf("claimed by %q, want nobody: the Weekly window is past 80%% and resets in three days", group)
		}
		evs := eventKinds(t, s, b.ID, "quota_wait")
		if len(evs) != 1 {
			t.Fatalf("quota_wait events = %v, want one", evs)
		}
		reason, _ := evs[0].Data["reason"].(string)
		if evs[0].Data["route"] != r.ID || reason != "a passed over (Kimi's Weekly window is 90% used, the route asks under 80)" ||
			evs[0].Data["resets_at"] != noon().Add(72*time.Hour).UTC().Format(time.RFC3339) {
			t.Fatalf("quota_wait = %v, want the window and its reset", evs[0].Data)
		}

		// A provider that says no reset gives the duration nothing to hold.
		*now = now.Add(6 * time.Minute)
		p.say(`{"provider":"Kimi","windows":[{"label":"Weekly","used_percent":90}]}`)
		primeGroupLimits(t, s, d, "a")
		must(t, d.Reconcile(ctx))
		if group, _ := claimedBy(t, s, b.ID); group != "" {
			t.Fatalf("claimed by %q, want nobody: the report says no reset", group)
		}

		// The window now says it resets in under a day: the bead starts, and
		// its claim says the window was held, measured from the pass's clock.
		*now = now.Add(6 * time.Minute)
		p.say(weeklyResetting(90, 20*time.Hour))
		primeGroupLimits(t, s, d, "a")
		must(t, d.Reconcile(ctx))
		group, data := claimedBy(t, s, b.ID)
		if group != "a" || data["route"] != r.ID || data["passed"] != nil ||
			!reflect.DeepEqual(data["held"], []any{"Weekly 90% but resets in 19h 48m"}) {
			t.Fatalf("claimed by %q with %v, want a with the Weekly window held for its reset 19h 48m on", group, data)
		}
	})
}

// A group at its limit of sessions is waited for, not passed over: only a
// quota condition sends a bead to a further group. The waiting bead gets
// one group_wait event naming the group, and none again on the next pass.
func TestGroupAtItsLimitIsWaitedForNotPassedOver(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, f, d, p, r, _ := quotaYard(t, "a@Weekly<80", "b")
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
	waiting := ""
	for _, id := range []string{one.ID, two.ID} {
		g, _ := claimedBy(t, s, id)
		groups = append(groups, g)
		if g == "" {
			waiting = id
		}
	}
	if strings.Join(groups, ",") != "a," && strings.Join(groups, ",") != ",a" {
		t.Fatalf("groups of the two beads = %q, want one with a and one waiting", groups)
	}
	evs := eventKinds(t, s, waiting, "group_wait")
	if len(evs) != 1 {
		t.Fatalf("group_wait events of the waiting bead = %d, want 1 over three passes", len(evs))
	}
	data := evs[0].Data
	gs, _ := data["groups"].([]any)
	if data["stage"] != "new" || data["route"] != r.ID || len(gs) != 1 {
		t.Fatalf("group_wait = %v, want stage new, the route and the one group", data)
	}
	g, _ := gs[0].(map[string]any)
	if g["group"] != "a" || !strings.Contains(fmt.Sprint(g["reason"]), "limit (1)") {
		t.Fatalf("group_wait groups = %v, want a at its limit", gs)
	}
	started := one.ID
	if waiting == one.ID {
		started = two.ID
	}
	if evs := eventKinds(t, s, started, "group_wait"); len(evs) != 0 {
		t.Fatalf("group_wait events of the started bead = %v, want none", evs)
	}
}

// A bead whose only group is at its limit waits with one group_wait event,
// and none again on the next pass. No group is free, so the choice reports
// the wait itself instead of asking the quota.
func TestSingleGroupRouteAtItsLimitWaitsWithOneEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, f, d, p, r, _ := quotaYard(t, "a")
	must(t, s.PutGroup(ctx, store.Group{Name: "a", Runner: "herdr", Limit: 1, Config: map[string]any{"kind": "kimi"}}, "me"))
	p.say(weekly(70, 10))
	one, two := newBead(t, s, "one"), newBead(t, s, "two")
	must(t, d.Reconcile(ctx))
	must(t, d.Reconcile(ctx))
	if got := f.starts(); len(got) != 1 {
		t.Fatalf("starts = %v, want one: a is at its limit", got)
	}
	waiting := one.ID
	if g, _ := claimedBy(t, s, one.ID); g != "" {
		waiting = two.ID
	}
	evs := eventKinds(t, s, waiting, "group_wait")
	if len(evs) != 1 {
		t.Fatalf("group_wait events of the waiting bead = %d, want 1 over two passes", len(evs))
	}
	data := evs[0].Data
	gs, _ := data["groups"].([]any)
	if data["stage"] != "new" || data["route"] != r.ID || len(gs) != 1 {
		t.Fatalf("group_wait = %v, want stage new, the route and the one group", data)
	}
	g, _ := gs[0].(map[string]any)
	if g["group"] != "a" || !strings.Contains(fmt.Sprint(g["reason"]), "limit (1)") {
		t.Fatalf("group_wait groups = %v, want a at its limit", gs)
	}
}

// pacedStandIn replaces the yard's limits source with one whose script
// reads its stdin to the end before it answers: the yard runs the command
// with nothing to read, so a stub that waited on input would hang the
// pass. It answers with what say last set.
func pacedStandIn(t *testing.T, s store.API) (say func(string)) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "provider")
	scripttest.Write(t, script, "#!/bin/sh\ncat >/dev/null\ncat "+dir+"/answer\n")
	_, err := s.PutSource(context.Background(), "quokka.yaml", "name: quokka\nlimits:\n  command: ["+script+
		", \"{provider}\", --json]\n  kinds: { kimi: moonshot, codex: openai }\n", "me")
	must(t, err)
	return func(report string) {
		t.Helper()
		must(t, os.WriteFile(filepath.Join(dir, "answer"), []byte(report), 0o600))
	}
}

// paced is a report with the Weekly window at week percent and the 5h
// window at five; weekTarget is the Weekly window's target, "" for a
// report that gives none.
func paced(week, five float64, weekTarget string) string {
	target := ""
	if weekTarget != "" {
		target = `,"target_percent":` + weekTarget
	}
	return fmt.Sprintf(`{"provider":"Kimi","windows":[{"label":"5h","used_percent":%v},{"label":"Weekly","used_percent":%v%s}]}`,
		five, week, target)
}

// A route may pass a group over when its window is burned past the share
// of it that has passed: with used 38 and target 17 the bead goes to the
// further group, with used 20 to the first. A window with no target reads
// as a condition that cannot be evaluated: the group takes the bead and
// the claimed event says the limits were not known.
func TestRouteComparesThePaceWithTheTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, d, _, r, _ := quotaYard(t, "a@Weekly<target+10", "b")
	say := pacedStandIn(t, s)
	say(paced(38, 10, "17"))
	primeGroupLimits(t, s, d, "a")
	primeGroupLimits(t, s, d, "b")
	b := newBead(t, s, "past")
	must(t, d.Reconcile(ctx))
	group, data := claimedBy(t, s, b.ID)
	if group != "b" || data["route"] != r.ID {
		t.Fatalf("at used 38, target 17: claimed by %q with %v, want b", group, data)
	}
	passed, _ := data["passed"].([]any)
	if len(passed) != 1 {
		t.Fatalf("passed = %v, want the one group", data["passed"])
	}
	if one, _ := passed[0].(map[string]any); one["group"] != "a" ||
		one["reason"] != "Kimi's Weekly window is 38% used, the route asks under target 17% + 10" {
		t.Fatalf("passed = %v", one)
	}

	s, _, d, _, _, _ = quotaYard(t, "a@Weekly<target+10", "b")
	say = pacedStandIn(t, s)
	say(paced(20, 10, "17"))
	primeGroupLimits(t, s, d, "a")
	b = newBead(t, s, "within")
	must(t, d.Reconcile(ctx))
	if group, data := claimedBy(t, s, b.ID); group != "a" || data["passed"] != nil || data["limits_unknown"] != nil {
		t.Fatalf("at used 20, target 17: claimed by %q with %v, want a, nobody passed over", group, data)
	}

	s, _, d, _, _, _ = quotaYard(t, "a@Weekly<target+10", "b")
	say = pacedStandIn(t, s)
	say(paced(99, 99, ""))
	primeGroupLimits(t, s, d, "a")
	b = newBead(t, s, "untargeted")
	must(t, d.Reconcile(ctx))
	group, data = claimedBy(t, s, b.ID)
	unknown, _ := data["limits_unknown"].(string)
	if group != "a" || data["passed"] != nil ||
		unknown != "the limits of a were not known: Kimi's report does not say the target of the Weekly window" {
		t.Fatalf("with no target: claimed by %q with %v, want a with the limits not known", group, data)
	}

	// With no further group the bead waits, and the quota_wait event says
	// the target and the margin.
	s, _, d, _, r, _ = quotaYard(t, "a@Weekly<target+10")
	say = pacedStandIn(t, s)
	say(paced(38, 10, "17"))
	primeGroupLimits(t, s, d, "a")
	b = newBead(t, s, "waiting")
	must(t, d.Reconcile(ctx))
	if group, _ := claimedBy(t, s, b.ID); group != "" {
		t.Fatalf("claimed by %q, want nobody: Weekly is past target 17 + 10", group)
	}
	evs := eventKinds(t, s, b.ID, "quota_wait")
	if len(evs) != 1 {
		t.Fatalf("quota_wait events = %v, want one", evs)
	}
	if reason, _ := evs[0].Data["reason"].(string); evs[0].Data["route"] != r.ID ||
		reason != "a passed over (Kimi's Weekly window is 38% used, the route asks under target 17% + 10)" {
		t.Fatalf("quota_wait = %v", evs[0].Data)
	}
}

// `@<80` reads every window the provider reports.
func TestConditionWithoutALabelReadsEveryWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, d, p, _, _ := quotaYard(t, "a@<80", "b")
	p.say(weekly(10, 90))
	primeGroupLimits(t, s, d, "a")
	primeGroupLimits(t, s, d, "b")
	b := newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	group, data := claimedBy(t, s, b.ID)
	if group != "b" || !strings.Contains(fmt.Sprint(data["passed"]), "Kimi's 5h window is 90% used, the route asks under 80") {
		t.Fatalf("claimed by %q with %v, want b: the 5h window is past 80", group, data)
	}
}

// A condition that cannot be evaluated holds, and so does the one a group
// with none is judged by, and the claimed event says the limits were not
// known: a limits command that fails takes the gate away and stops nothing.
func TestConditionThatCannotBeEvaluatedHolds(t *testing.T) {
	t.Parallel()
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
		// A group with no condition is judged by every window.
		"no condition and the command failed": {"a", func(_ *testing.T, _ store.API, p *standIn) { p.fail() }, "not logged in"},
		"no condition and no source": {"a", func(t *testing.T, s store.API, _ *standIn) {
			must(t, s.PutGroup(ctx, store.Group{Name: "a", Runner: "herdr", Limit: 5, Config: map[string]any{"kind": "claude"}}, "me"))
		}, "no source of this yard says the limits of claude"},
	} {
		t.Run(name, func(t *testing.T) {
			s, _, d, p, _, _ := quotaYard(t, tc.first, "b")
			tc.break_(t, s, p)
			primeGroupLimits(t, s, d, "a")
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
// answer that failed included; and for a route with no condition only where
// its group starts agents.
func TestLimitsAreAskedOnceInFiveMinutesAndOnlyForAGatedRoute(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, f, d, p, _, now := quotaYard(t, "a@Weekly<80", "b")
	p.say(weekly(70, 10))
	primeGroupLimits(t, s, d, "a")
	for i := range 4 {
		newBead(t, s, fmt.Sprint("b", i))
	}
	for range 4 { // one start per pass for the group (starts.go)
		must(t, d.Reconcile(ctx))
	}
	newBead(t, s, "later")
	*now = now.Add(4 * time.Minute)
	must(t, d.Reconcile(ctx))
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
	dispatch.WaitLimitsIdle(d)
	if asks := p.asks(); len(asks) != 2 {
		t.Fatalf("the limits command ran %d times, want 2: a failure is remembered as an answer is", len(asks))
	}

	// A route with no condition is read as well, for the provider of the
	// group that takes the bead: b is not reached, so it is not asked.
	s, f, d, p, _, _ = quotaYard(t, "a", "b")
	primeGroupLimits(t, s, d, "a")
	newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	if got, asks := f.starts(), p.asks(); len(got) != 1 || len(asks) != 1 || asks[0] != "moonshot" {
		t.Fatalf("a route with no condition: starts %v, limits command ran for %v, want one start and one run, for a's provider", got, asks)
	}
}

// A route with one group and no condition claims as it always did; its
// claimed event names the route beside what it carried before, and a group
// that starts no agents does not ask for limits.
func TestRouteWithoutAConditionDispatchesAsBefore(t *testing.T) {
	t.Parallel()
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

	// A plain route to an exec group never runs the limits command, whatever
	// the yard's source says about the default agent kind's used-up window.
	s2 := storetest.New(t)
	f2 := &fakeRunner{dead: map[string]bool{}}
	d2 := &dispatch.Dispatcher{Store: s2, Runners: map[string]dispatch.Runner{"exec": f2}, Backoff: time.Nanosecond}
	dir := t.TempDir()
	asked, script := filepath.Join(dir, "asked"), filepath.Join(dir, "provider")
	scripttest.Write(t, script, "#!/bin/sh\necho \"$1\" >>"+asked+
		"\necho '{\"provider\":\"Claude\",\"windows\":[{\"label\":\"5h\",\"used_percent\":100}]}'}\n")
	_, err = s2.PutSource(ctx, "quokka.yaml", "name: quokka\nlimits:\n  command: ["+script+", \"{provider}\", --json]\n  kinds: { claude: claude }\n", "me")
	must(t, err)
	must(t, s2.PutGroup(ctx, store.Group{Name: "refinery", Runner: "exec", Limit: 5, Config: map[string]any{"cmd": "true"}}, "me"))
	_, err = s2.AddRoute(ctx, store.Route{Stage: "new", Group: "refinery"}, "me")
	must(t, err)
	b2 := newBead(t, s2, "exec")
	must(t, d2.Reconcile(ctx))
	if group, _ := claimedBy(t, s2, b2.ID); group != "refinery" || len(f2.starts()) != 1 {
		t.Fatalf("claimed by %q, starts %v, want refinery to start it", group, f2.starts())
	}
	if _, err := os.Stat(asked); !os.IsNotExist(err) {
		t.Fatalf("the limits command ran or could not be checked (%v), want no run for a group that starts no agents", err)
	}
}

// A stage a bead is not in yet has no chosen group: the job of a bead whose
// next stage has a route with several groups names them in order, the flow
// map says agents work it, and neither runs the limits command.
func TestNextStageNamesARoutesGroupsInOrderAndAsksNothing(t *testing.T) {
	t.Parallel()
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

// A bead that comes back to its stage is routed again, and when a window
// crossed its threshold in between it goes to another group than the one
// that worked it there: the claimed event names that group in `was`, and
// names none when the same group takes the bead again.
func TestClaimedEventNamesTheGroupThatHeldTheBeadInTheStageBefore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, d, p, _, now := quotaYard(t, "a@Weekly<80", "b")
	p.say(weekly(70, 10))
	primeGroupLimits(t, s, d, "a")
	b := newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	if group, data := claimedBy(t, s, b.ID); group != "a" || data["was"] != nil {
		t.Fatalf("first claimed by %q with %v, want a and no group before it", group, data)
	}
	// Review sends it back, twice; the window is past the threshold now.
	p.say(weekly(85, 10))
	for i, want := range []any{"a", nil} {
		_, err := s.Advance(ctx, b.ID, "review", store.Expect{}, "me")
		must(t, err)
		_, err = s.Advance(ctx, b.ID, "new", store.Expect{}, "me")
		must(t, err)
		*now = now.Add(6 * time.Minute)
		primeGroupLimits(t, s, d, "a")
		primeGroupLimits(t, s, d, "b")
		must(t, d.Reconcile(ctx))
		evs := eventKinds(t, s, b.ID, "claimed")
		if len(evs) != i+2 {
			t.Fatalf("claimed events after %d times back = %v", i+1, evs)
		}
		if data := evs[len(evs)-1].Data; data["group"] != "b" || data["was"] != want {
			t.Fatalf("claimed %d times back = %v, want b with was %v", i+1, data, want)
		}
	}
}

// machineStandIn is a source in place of the machine's: the yard's source
// "machine" answers for itself with what say last set, and runs counts how
// often its command ran.
func machineStandIn(t *testing.T, s store.API) (say func(string), runs func() int) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "machine")
	scripttest.Write(t, script, "#!/bin/sh\necho x >>"+dir+"/asked\ncat "+dir+"/answer\n")
	_, err := s.PutSource(context.Background(), "machine.yaml", "name: machine\nlimits:\n  command: ["+script+"]\n", "me")
	must(t, err)
	say = func(report string) {
		t.Helper()
		must(t, os.WriteFile(filepath.Join(dir, "answer"), []byte(report), 0o600))
	}
	say(`{"provider":"machine","windows":[]}`)
	return say, func() int {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, "asked"))
		if os.IsNotExist(err) {
			return 0
		}
		must(t, err)
		return len(b) / 2
	}
}

// loadReport is what the machine source says with load at load percent,
// swap and disk nearly idle, each with its target.
func loadReport(load float64) string {
	return fmt.Sprintf(`{"provider":"machine","windows":[`+
		`{"label":"load","used_percent":%v,"target_percent":80},`+
		`{"label":"swap","used_percent":10,"target_percent":85},`+
		`{"label":"disk","used_percent":40,"target_percent":90}]}`,
		load)
}

// A route with a condition on a source holds dispatch while the source
// answers over the threshold and releases it under; the wait says the
// source's window, and the source's command runs at most once in five
// minutes however many beads are ready.
func TestRouteWithASourceConditionHoldsAndReleasesDispatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, d, _, r, now := quotaYard(t, "a@machine.load<80", "wait")
	say, runs := machineStandIn(t, s)
	say(loadReport(93))
	dispatch.PrimeSourceLimits(d, ctx, "machine")
	b := newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	if group, _ := claimedBy(t, s, b.ID); group != "" {
		t.Fatalf("claimed by %q, want nobody: machine.load is past the threshold", group)
	}
	evs := eventKinds(t, s, b.ID, "quota_wait")
	if len(evs) != 1 {
		t.Fatalf("quota_wait events = %v, want one", evs)
	}
	if reason, _ := evs[0].Data["reason"].(string); evs[0].Data["route"] != r.ID ||
		reason != "a passed over (machine's load window is 93% used, the route asks under 80)" {
		t.Fatalf("quota_wait = %v", evs[0].Data)
	}
	if _, has := evs[0].Data["resets_at"]; has {
		t.Fatalf("quota_wait = %v: the machine's windows never reset", evs[0].Data)
	}

	say(loadReport(12))
	*now = now.Add(6 * time.Minute)
	dispatch.PrimeSourceLimits(d, ctx, "machine")
	must(t, d.Reconcile(ctx))
	if group, data := claimedBy(t, s, b.ID); group != "a" || data["route"] != r.ID || data["passed"] != nil {
		t.Fatalf("claimed by %q with %v, want a with nobody passed over", group, data)
	}
	if got := runs(); got != 2 {
		t.Fatalf("the machine command ran %d times, want 2: once over, once under", got)
	}

	// With no declared source, the built-in gates on this machine's actual
	// load. Half the measured load holds, and ten points above releases; the
	// one-minute average is unlikely to cross either threshold between reads.
	t.Run("built-in machine", func(t *testing.T) {
		r, err := limits.MachineReport(ctx, t.TempDir())
		if err != nil {
			t.Skipf("this machine answers no limits: %v", err)
		}
		load := *r.Windows[0].UsedPercent
		// To a point, as the quota_wait reason renders the threshold.
		point := func(p float64) float64 { return math.Round(p*10) / 10 }
		hold := func() (string, bool) {
			under := point(load / 2)
			if under <= 0 {
				return "", false
			}
			if under > 99 {
				under = 99
			}
			return fmt.Sprintf("%g", under), true
		}
		release := func() (string, bool) {
			if load >= 100 {
				return "", false
			}
			under := point(load + 10)
			if under > 100 {
				under = 100
			}
			return fmt.Sprintf("%g", under), true
		}
		if under, ok := hold(); ok {
			s, _, d, _, r, _ := quotaYard(t, "a@machine.load<"+under, "wait")
			d.Home = t.TempDir()
			dispatch.PrimeSourceLimits(d, ctx, "machine")
			b := newBead(t, s, "x")
			must(t, d.Reconcile(ctx))
			if group, _ := claimedBy(t, s, b.ID); group != "" {
				t.Fatalf("claimed by %q, want nobody: this machine's load is over %s", group, under)
			}
			evs := eventKinds(t, s, b.ID, "quota_wait")
			if len(evs) != 1 {
				t.Fatalf("quota_wait events = %v, want one", evs)
			}
			reason, _ := evs[0].Data["reason"].(string)
			if evs[0].Data["route"] != r.ID || !strings.Contains(reason, "machine's load window is") ||
				!strings.Contains(reason, "the route asks under "+under) {
				t.Fatalf("quota_wait = %v", evs[0].Data)
			}
			if _, has := evs[0].Data["resets_at"]; has {
				t.Fatalf("quota_wait = %v: the built-in never resets", evs[0].Data)
			}
		}
		if under, ok := release(); ok {
			s, _, d, _, r, _ := quotaYard(t, "a@machine.load<"+under, "wait")
			d.Home = t.TempDir()
			dispatch.PrimeSourceLimits(d, ctx, "machine")
			b := newBead(t, s, "x")
			must(t, d.Reconcile(ctx))
			if group, data := claimedBy(t, s, b.ID); group != "a" || data["route"] != r.ID || data["passed"] != nil {
				t.Fatalf("claimed by %q with %v, want a with nobody passed over", group, data)
			}
			if evs := eventKinds(t, s, b.ID, "quota_wait"); len(evs) != 0 {
				t.Fatalf("quota_wait events = %v, want none", evs)
			}
		}
	})
	t.Run("remembered answer hold", func(t *testing.T) {
		// A remembered answer that says how long it holds is asked again
		// once that has passed, within the five minutes: a machine-gated
		// bead parked on one spiked reading starts on the first pass after
		// it ages out. An answer that says nothing holds for the whole five
		// minutes, as before.
		for name, tc := range map[string]struct {
			holds time.Duration
			want  string // "" parks the bead, else the claiming group
			runs  int
		}{
			"the machine's answer holds for one pass": {30 * time.Second, "a", 1},
			"a silent answer holds for five minutes":  {0, "", 0},
		} {
			t.Run(name, func(t *testing.T) {
				s, _, d, _, _, now := quotaYard(t, "a@machine.load<80", "wait")
				say, runs := machineStandIn(t, s)
				say(loadReport(20))
				over := 95.0
				dispatch.RememberSourceAnswer(d, "machine", *now, &limits.Report{
					Source: "machine", Provider: "machine", AskedAt: *now,
					HoldsFor: tc.holds,
					Windows:  []limits.Window{{Label: "load", UsedPercent: &over}},
				})
				b := newBead(t, s, "x")
				*now = now.Add(40 * time.Second)
				must(t, d.Reconcile(ctx))
				dispatch.WaitLimitsIdle(d)
				if group, _ := claimedBy(t, s, b.ID); group != tc.want {
					t.Fatalf("claimed by %q, want %q", group, tc.want)
				}
				if got := runs(); got != tc.runs {
					t.Fatalf("the machine command ran %d times, want %d", got, tc.runs)
				}
				if tc.want == "" {
					return
				}
				// The re-read answer says nothing, so it holds for the five
				// minutes: a bead more starts on it without another run.
				say(loadReport(95))
				*now = now.Add(40 * time.Second)
				later := newBead(t, s, "y")
				must(t, d.Reconcile(ctx))
				if group, _ := claimedBy(t, s, later.ID); group != "a" {
					t.Fatalf("claimed by %q, want a: the re-read 20%% still holds", group)
				}
				if got := runs(); got != 1 {
					t.Fatalf("the machine command ran %d times, want 1", got)
				}
			})
		}
	})
}

// A condition may mix a source's window with the provider's: each
// comparison reads its own windows.
func TestRouteConditionMixesASourceAndAProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, d, p, _, _ := quotaYard(t, "a@machine.load<80,Weekly<80", "b")
	say, _ := machineStandIn(t, s)
	p.say(weekly(70, 10))
	say(loadReport(93))
	dispatch.PrimeSourceLimits(d, ctx, "machine")
	primeGroupLimits(t, s, d, "a")
	primeGroupLimits(t, s, d, "b")
	b := newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	group, data := claimedBy(t, s, b.ID)
	if group != "b" {
		t.Fatalf("claimed by %q, want b: machine.load is past the threshold", group)
	}
	passed, _ := data["passed"].([]any)
	if len(passed) != 1 {
		t.Fatalf("passed = %v, want the one group", data["passed"])
	}
	if one, _ := passed[0].(map[string]any); one["group"] != "a" ||
		one["reason"] != "machine's load window is 93% used, the route asks under 80" {
		t.Fatalf("passed = %v", one)
	}
}

// A group with no kind is gated the same way: an exec group carries a
// condition on a source, and its beads wait while the source answers over.
func TestRouteGatesAGroupWithNoKindOnASource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.New(t)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"exec": f}, Backoff: time.Nanosecond}
	now := noon()
	dispatch.SetClock(d, func() time.Time { return now })
	must(t, s.PutGroup(ctx, store.Group{Name: "refinery", Runner: "exec", Limit: 5, Config: map[string]any{"cmd": "true"}}, "me"))
	say, _ := machineStandIn(t, s)
	g := store.ParseRouteGroup("refinery@machine.load<80")
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: g.Name, When: g.When, Else: []store.RouteGroup{{Name: "wait"}}}, "me")
	must(t, err)
	say(loadReport(93))
	dispatch.PrimeSourceLimits(d, ctx, "machine")
	b := newBead(t, s, "x")
	must(t, d.Reconcile(ctx))
	if group, _ := claimedBy(t, s, b.ID); group != "" {
		t.Fatalf("claimed by %q, want nobody: machine.load is past the threshold", group)
	}
	if evs := eventKinds(t, s, b.ID, "quota_wait"); len(evs) != 1 {
		t.Fatalf("quota_wait events = %v, want one", evs)
	}
	say(loadReport(12))
	now = now.Add(6 * time.Minute)
	dispatch.PrimeSourceLimits(d, ctx, "machine")
	must(t, d.Reconcile(ctx))
	if group, _ := claimedBy(t, s, b.ID); group != "refinery" {
		t.Fatalf("claimed by %q once load opened, want refinery", group)
	}
	if got := f.starts(); len(got) != 1 {
		t.Fatalf("starts = %v, want the one", got)
	}
}

// A herdr group stored without a kind is asked for as the kind its
// sessions start with: the yard's default_kind, not the runner's own
// default. (yardr-j6f0: the condition read the windows of another provider
// than the one the session burns.)
func TestConditionOnAGroupWithoutAKindReadsTheYardsDefaultKind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, d, p, _, now := quotaYard(t, "a@Weekly<80", "b")
	must(t, s.SetSetting(ctx, store.DefaultKindSetting, "kimi", "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "a", Runner: "herdr", Limit: 5}, "me"))
	p.say(weekly(10, 10))
	primeLimits(t, d, "kimi", "", nil)
	under := newBead(t, s, "under")
	must(t, d.Reconcile(ctx))
	group, data := claimedBy(t, s, under.ID)
	if group != "a" || data["kind"] != "kimi" || data["limits_unknown"] != nil || !reflect.DeepEqual(p.asks(), []string{"moonshot"}) {
		t.Fatalf("claimed by %q with %v after asking %v, want a as kind kimi after asking moonshot, kimi's provider", group, data, p.asks())
	}
	*now = now.Add(6 * time.Minute)
	p.say(weekly(91, 10))
	primeLimits(t, d, "kimi", "", nil)
	primeGroupLimits(t, s, d, "b")
	over := newBead(t, s, "over")
	must(t, d.Reconcile(ctx))
	if group, data := claimedBy(t, s, over.ID); group != "b" || data["limits_unknown"] != nil {
		t.Fatalf("claimed by %q with %v, want b: kimi's Weekly window is past the threshold", group, data)
	}
}

// hangingStandIn is a stand-in provider whose run for the provider
// named hangOn waits until release is called: a limits command that
// hangs past any fuse a test would set. asks is its runs, as standIn's.
type hangingStandIn struct {
	t   *testing.T
	dir string
}

func newHangingStandIn(t *testing.T, s store.API, hangOn string) *hangingStandIn {
	t.Helper()
	p := &hangingStandIn{t: t, dir: t.TempDir()}
	script := filepath.Join(p.dir, "provider")
	scripttest.Write(t, script, "#!/bin/sh\ncd "+p.dir+"\necho \"$1\" >>asked\n"+
		"if [ \"$1\" = \""+hangOn+"\" ]; then while [ ! -f release ]; do sleep 0.05; done; fi\ncat answer\n")
	_, err := s.PutSource(context.Background(), "quokka.yaml", "name: quokka\nlimits:\n  command: ["+script+
		", \"{provider}\", --json]\n  kinds: { kimi: moonshot, codex: openai }\n", "me")
	must(t, err)
	must(t, os.WriteFile(filepath.Join(p.dir, "answer"), []byte(`{"provider":"Kimi","windows":[]}`), 0o600))
	return p
}

func (p *hangingStandIn) release() {
	p.t.Helper()
	must(p.t, os.WriteFile(filepath.Join(p.dir, "release"), nil, 0o600))
}

func (p *hangingStandIn) asks() []string {
	p.t.Helper()
	b, err := os.ReadFile(filepath.Join(p.dir, "asked"))
	if os.IsNotExist(err) {
		return nil
	}
	must(p.t, err)
	return strings.Fields(string(b))
}

// waitAsked waits until the provider has been run for name: the effect
// a test waits on, never the wall time a run takes. The deadline is a fuse
// against a hang (dispatch.WaitFuse).
func waitAsked(t *testing.T, p *hangingStandIn, name string) {
	t.Helper()
	deadline := time.Now().Add(dispatch.WaitFuse)
	for time.Now().Before(deadline) {
		for _, a := range p.asks() {
			if a == name {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the provider was never run for %s (runs so far: %v)", name, p.asks())
}

// A limits command that hangs holds no pass: the pass completes while
// its run is still blocked, ready beads of two groups that share the
// provider's key are claimed at once with limits_unknown (the cold
// exposure, bounded by each group's limit), and the blocked run is the
// one run for all of them.
func TestHangingLimitsCommandHoldsNoPass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.New(t)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": f}, Backoff: time.Nanosecond}
	for _, name := range []string{"ga", "gb"} {
		must(t, s.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Limit: 5, Config: map[string]any{"kind": "kimi"}}, "me"))
	}
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "ga"}, "me")
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "review", Group: "gb"}, "me")
	must(t, err)
	p := newHangingStandIn(t, s, "moonshot")
	var beads []store.Bead
	for _, stage := range []string{"new", "new", "review", "review"} {
		b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Stage: stage}, "me")
		must(t, err)
		beads = append(beads, *b)
	}
	pass := func() {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- d.Reconcile(ctx) }()
		select {
		case err := <-done:
			must(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("a pass waited on the hanging limits command")
		}
	}
	// One start per group in this pass (starts.go): the other bead of
	// each group is claimed cold by the next, while the run still hangs.
	pass()
	if got := len(f.starts()); got != 2 {
		t.Fatalf("starts = %d, want one per group from the first pass", got)
	}
	pass()
	for _, b := range beads {
		_, data := claimedBy(t, s, b.ID)
		if data["limits_unknown"] == nil {
			t.Fatalf("claimed %s with %v, want limits_unknown while its provider's first run hangs", b.ID, data)
		}
	}
	if got := len(f.starts()); got != len(beads) {
		t.Fatalf("starts = %d, want all %d claimed cold (fail-open)", got, len(beads))
	}
	// A further pass while the run still hangs starts nothing more, and
	// wants no second run of the key.
	pass()
	if got := len(f.starts()); got != len(beads) {
		t.Fatalf("starts = %d after a third pass, want still %d", got, len(beads))
	}
	waitAsked(t, p, "moonshot")
	p.release()
	dispatch.WaitLimitsIdle(d)
	if asks := p.asks(); len(asks) != 1 {
		t.Fatalf("the provider was run for %v, want once, for many beads of two groups sharing its key", asks)
	}
}

// One key's hanging run does not queue another key's behind it: the
// other provider's refresh is run and lands while the first still
// hangs, and a pass reads it.
func TestOneKeysHangingLimitsRunDoesNotQueueAnotherKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.New(t)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": f}, Backoff: time.Nanosecond}
	must(t, s.PutGroup(ctx, store.Group{Name: "ga", Runner: "herdr", Limit: 5, Config: map[string]any{"kind": "kimi"}}, "me"))
	must(t, s.PutGroup(ctx, store.Group{Name: "gb", Runner: "herdr", Limit: 5, Config: map[string]any{"kind": "codex"}}, "me"))
	_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "ga"}, "me")
	must(t, err)
	_, err = s.AddRoute(ctx, store.Route{Stage: "review", Group: "gb"}, "me")
	must(t, err)
	p := newHangingStandIn(t, s, "moonshot")
	slow, err := s.CreateBead(ctx, store.NewBead{Title: "slow"}, "me")
	must(t, err)
	fast, err := s.CreateBead(ctx, store.NewBead{Title: "fast", Stage: "review"}, "me")
	must(t, err)
	done := make(chan error, 1)
	go func() { done <- d.Reconcile(ctx) }()
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(dispatch.WaitFuse):
		t.Fatal("a pass waited on the hanging limits command")
	}
	// The codex key's run starts and lands behind no queue, while the
	// kimi key's run is still blocked on its release. The refresher marks a
	// key running before the goroutine that runs its command has started
	// it, so a run in flight has not asked yet: both commands are waited for
	// at the stand-in, the held one while it is held, and only then the
	// codex key's landing, which leaves one run in flight.
	waitAsked(t, p, "moonshot")
	waitAsked(t, p, "openai")
	until(t, "the codex key's run to land while the kimi key's hangs", func() bool { return dispatch.LimitsRunsInFlight(d) == 1 })
	if asks := p.asks(); len(asks) != 2 {
		t.Fatalf("the provider was run for %v, want both keys' runs started, neither queued behind the other", asks)
	}
	_ = slow
	_ = fast
	p.release()
	dispatch.WaitLimitsIdle(d)
}

// What Run warms gates from the first pass: a source a route condition
// names (warmed through its own key, as a provider's is), and a
// provider whose warmed window is used up, which writes one quota_wait
// with its reset and starts nothing, where 99.9% still starts.
func TestWarmedLimitsGateFromTheFirstPass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("a source a condition names", func(t *testing.T) {
		s, _, d, _, _, _ := quotaYard(t, "a@machine.load<80", "wait")
		say, runs := machineStandIn(t, s)
		say(loadReport(93))
		dispatch.WarmLimitsForTest(d, ctx)
		b := newBead(t, s, "x")
		must(t, d.Reconcile(ctx))
		if group, _ := claimedBy(t, s, b.ID); group != "" {
			t.Fatalf("claimed by %q, want nobody: the warmed source says load is past the threshold", group)
		}
		if evs := eventKinds(t, s, b.ID, "quota_wait"); len(evs) != 1 {
			t.Fatalf("quota_wait events = %v, want one from the warmed answer", evs)
		}
		if got := runs(); got != 1 {
			t.Fatalf("the machine command ran %d times, want the warm-up's one", got)
		}
	})
	t.Run("a provider window used up", func(t *testing.T) {
		s, f, d, p, _, _ := quotaYard(t, "a")
		p.say(weekly(100, 10))
		dispatch.WarmLimitsForTest(d, ctx)
		b := newBead(t, s, "x")
		must(t, d.Reconcile(ctx))
		if group, _ := claimedBy(t, s, b.ID); group != "" || len(f.starts()) != 0 {
			t.Fatalf("claimed by %q, starts %v; want nobody: the warmed window is used up", group, f.starts())
		}
		evs := eventKinds(t, s, b.ID, "quota_wait")
		if len(evs) != 1 || evs[0].Data["resets_at"] != noon().Add(72*time.Hour).UTC().Format(time.RFC3339) {
			t.Fatalf("quota_wait = %v, want one with the Weekly window's reset", evs)
		}
	})
	t.Run("a provider window just under used up", func(t *testing.T) {
		s, _, d, p, _, _ := quotaYard(t, "a")
		p.say(weekly(99.9, 10))
		dispatch.WarmLimitsForTest(d, ctx)
		b := newBead(t, s, "x")
		must(t, d.Reconcile(ctx))
		if group, data := claimedBy(t, s, b.ID); group != "a" || data["limits_unknown"] != nil {
			t.Fatalf("claimed by %q with %v, want a: 99.9%% is not used up", group, data)
		}
	})
}
