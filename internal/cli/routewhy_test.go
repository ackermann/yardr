package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

const weeklyReason = "Claude's Weekly window is 83% used, the route asks under 80"

// bead show says in one line which route gave a bead to its group and
// which groups it passed over, names the group that held the bead in the
// stage before, and says nothing of a route that had no choice to make.
func TestBeadShowSaysWhyABeadWentWhereItWent(t *testing.T) {
	a, st, ran := quotaCLI(t)
	ctx := context.Background()
	// Room for the three beads a takes below.
	if err := st.PutGroup(ctx, store.Group{Name: "a", Runner: "herdr", Limit: 5, Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	r, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "a", When: "Weekly<80", Else: []store.RouteGroup{{Name: "b"}}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	passed := []store.PassedGroup{{Group: "a", Reason: weeklyReason}}
	if _, err := st.Claim(ctx, b.ID, "b", "new", "yardr", store.Routed{Route: r.ID, Passed: passed}); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.beadCmd(), "show", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := "\n  routed to b by route " + r.ID + ": a passed over (" + weeklyReason + ")\n"; !strings.Contains(out, want) {
		t.Fatalf("bead show does not say why the bead went to b, want %q:\n%s", want, out)
	}
	js := &app{st: st, actor: "me", json: true}
	out, err = run(t, js.beadCmd(), "show", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var shown struct {
		ID      string
		Routing *beadRouting
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil || shown.ID != b.ID || shown.Routing == nil ||
		shown.Routing.Group != "b" || shown.Routing.Route != r.ID || len(shown.Routing.Passed) != 1 || shown.Routing.Passed[0].Reason != weeklyReason {
		t.Fatalf("bead show --json routing = %+v, %v\n%s", shown.Routing, err, out)
	}

	// Sent back to the stage and taken by the first group this time: the
	// line names who held it before. Nothing was passed over.
	for _, stage := range []string{"review", "new"} {
		if _, err := st.Advance(ctx, b.ID, stage, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if out, err = run(t, a.beadCmd(), "show", b.ID); err != nil || strings.Contains(out, "routed to") {
		t.Fatalf("bead show of a bead back in its stage, unclaimed, still says where it went: %v\n%s", err, out)
	}
	if _, err := st.Claim(ctx, b.ID, "a", "new", "yardr", store.Routed{Route: r.ID}); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, a.beadCmd(), "show", b.ID); err != nil ||
		!strings.Contains(out, "\n  routed to a by route "+r.ID+": b held it in this stage before\n") {
		t.Fatalf("bead show does not name the group that held the bead before: %v\n%s", err, out)
	}

	// A condition that could not be evaluated is said too.
	unknown, err := st.CreateBead(ctx, store.NewBead{Title: "y"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	why := "the limits of a were not known: no source says the limits of kind claude"
	if _, err := st.Claim(ctx, unknown.ID, "a", "new", "yardr", store.Routed{Route: r.ID, Unknown: why}); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, a.beadCmd(), "show", unknown.ID); err != nil || !strings.Contains(out, "\n  routed to a by route "+r.ID+": "+why+"\n") {
		t.Fatalf("bead show does not say the limits were not known: %v\n%s", err, out)
	}

	// A route with nothing to say: the bead shows as it always did.
	plain, err := st.CreateBead(ctx, store.NewBead{Title: "z"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Claim(ctx, plain.ID, "a", "new", "yardr", store.Routed{Route: r.ID}); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, a.beadCmd(), "show", plain.ID); err != nil || strings.Contains(out, "routed to") || !strings.Contains(out, "owned by a") {
		t.Fatalf("bead show of a bead its route's first group took: %v\n%s", err, out)
	}
	if ran() {
		t.Error("bead show ran the limits command")
	}
}

// A bead that waits because no group's condition holds shows what it waits
// for and until when, from its quota_wait event, until a group has it.
func TestBeadShowSaysWhatAWaitingBeadWaitsFor(t *testing.T) {
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	r, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "a", When: "Weekly<80"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.beadCmd(), "show", b.ID); err != nil || strings.Contains(out, "waits since") {
		t.Fatalf("bead show before the dispatcher said anything: %v\n%s", err, out)
	}
	resets := time.Now().Add(26 * time.Hour).Truncate(time.Second)
	if err := st.LogDispatch(ctx, b.ID, "quota_wait", "yardr", map[string]any{
		"stage": "new", "route": r.ID, "reason": "a passed over (" + weeklyReason + ")",
		"passed":    []store.PassedGroup{{Group: "a", Reason: weeklyReason, ResetsAt: &resets}},
		"resets_at": resets.UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.beadCmd(), "show", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "  waits since ") {
			line = l
		}
	}
	if !strings.Contains(line, " for a group of route "+r.ID+" whose condition holds: a passed over ("+weeklyReason+"); the first of those windows resets ") ||
		!strings.HasSuffix(line, " (in 1d 2h)") {
		t.Fatalf("bead show of a waiting bead says %q:\n%s", line, out)
	}
	js := &app{st: st, actor: "me", json: true}
	out, err = run(t, js.beadCmd(), "show", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var shown struct{ Routing *beadRouting }
	if err := json.Unmarshal([]byte(out), &shown); err != nil || shown.Routing == nil || shown.Routing.Group != "" ||
		shown.Routing.ResetsAt == nil || !shown.Routing.ResetsAt.Equal(resets) {
		t.Fatalf("bead show --json routing of a waiting bead = %+v, %v", shown.Routing, err)
	}

	// Once a group has it, the wait is over and is not shown.
	if _, err := st.Claim(ctx, b.ID, "a", "new", "yardr", store.Routed{Route: r.ID}); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.beadCmd(), "show", b.ID); err != nil || strings.Contains(out, "waits since") {
		t.Fatalf("bead show of a claimed bead still says it waits: %v\n%s", err, out)
	}
}

// route add warns of a further group with no condition that burns the
// provider of a conditioned group before it, and adds the route.
func TestRouteAddWarnsOfAFurtherGroupOnTheSameProvider(t *testing.T) {
	a, st, ran := quotaCLI(t)
	ctx := context.Background()
	if _, err := st.PutSource(ctx, "machine.yaml", "name: machine\nlimits:\n  command: [/bin/true]\n", "me"); err != nil {
		t.Fatal(err)
	}
	for _, g := range []store.Group{
		{Name: "c", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		// Another kind, set to burn a's provider.
		{Name: "d", Runner: "herdr", Config: map[string]any{"kind": "codex", "provider": "anthropic"}},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	added := 0
	for _, tc := range []struct {
		args []string
		warn string // the group warned of, "" for none
	}{
		{[]string{"--group", "a@Weekly<80", "--else", "c"}, "c"},
		{[]string{"--group", "a@machine.load<80", "--else", "c"}, ""},
		{[]string{"--group", "a@machine.load<80,Weekly<80", "--else", "c"}, "c"},
		{[]string{"--group", "a@Weekly<80", "--else", "d"}, "d"},
		{[]string{"--group", "a@Weekly<80", "--else", "b@<90", "--else", "c"}, "c"},
		{[]string{"--group", "a@Weekly<80", "--else", "b"}, ""},
		{[]string{"--group", "a@Weekly<80", "--else", "c@Weekly<95"}, ""},
		// b always holds, so c after it is never reached and takes nothing.
		{[]string{"--group", "a@Weekly<80", "--else", "b", "--else", "c"}, ""},
		{[]string{"--group", "a"}, ""},
	} {
		errs := captureStderr(t)
		id, err := run(t, a.routeCmd(), append([]string{"add", "--stage", "new", "--priority", "1"}, tc.args...)...)
		if err != nil {
			t.Fatalf("route add %v: %v", tc.args, err)
		}
		id = strings.TrimSpace(id)
		added++
		if routes, _ := st.Routes(ctx); len(routes) != added {
			t.Fatalf("route add %v: the yard has %d routes, want %d: the route is added whatever is warned of", tc.args, len(routes), added)
		}
		got := errs.String()
		if tc.warn == "" {
			if got != "" {
				t.Errorf("route add %v warned: %s", tc.args, got)
			}
			continue
		}
		want := "warning: route " + id + ": " + tc.warn + " burns provider anthropic, as a does, and has no condition: " +
			"a bead that passes over a goes on to " + tc.warn + " however far the same windows are used."
		if !strings.HasPrefix(got, want) || strings.Count(got, "\n") != 1 {
			t.Errorf("route add %v warned %q, want one line beginning %q", tc.args, got, want)
		}
	}
	if ran() {
		t.Error("route add ran the limits command")
	}
}

// route add warns of a further group on the same provider only when the two
// read one login: a personal group failing over to the operator's, on
// different logins, warns of nothing, while two groups on one file warn as
// before.
func TestRouteAddWarnsOnlyWhenTheTwoGroupsShareOneLogin(t *testing.T) {
	a, st, ran := quotaCLI(t)
	ctx := context.Background()
	for _, g := range []store.Group{
		{Name: "own", Runner: "herdr", Config: map[string]any{"kind": "claude", "env_file": "/logins/own.env"}},
		{Name: "spare", Runner: "herdr", Config: map[string]any{"kind": "claude", "env_file": "/logins/spare.env"}},
		{Name: "twin", Runner: "herdr", Config: map[string]any{"kind": "claude", "env_file": "/logins/own.env"}},
		{Name: "spare-twin", Runner: "herdr", Config: map[string]any{"kind": "claude", "env_file": "/logins/spare.env"}},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		args  []string
		first string // the conditioned group the warning compares with
		warn  string // the group warned of, "" for none
	}{
		// Different logins read different windows: the failover is real.
		{args: []string{"--group", "own@Weekly<80", "--else", "spare"}},
		// A personal group failing over to the operator's ambient login.
		{args: []string{"--group", "own@Weekly<80", "--else", "a"}},
		// One file is one login: the bead goes on as used up as it is.
		{args: []string{"--group", "own@Weekly<80", "--else", "twin"}, first: "own", warn: "twin"},
		// A later condition on another login must not hide that login's
		// own unconditioned fallback from the warning.
		{args: []string{"--group", "own@Weekly<80", "--else", "spare@Weekly<80", "--else", "spare-twin"}, first: "spare", warn: "spare-twin"},
	} {
		errs := captureStderr(t)
		id, err := run(t, a.routeCmd(), append([]string{"add", "--stage", "new"}, tc.args...)...)
		if err != nil {
			t.Fatalf("route add %v: %v", tc.args, err)
		}
		id = strings.TrimSpace(id)
		got := errs.String()
		if tc.warn == "" {
			if got != "" {
				t.Errorf("route add %v warned: %s", tc.args, got)
			}
			continue
		}
		want := "warning: route " + id + ": " + tc.warn + " burns provider anthropic, as " + tc.first + " does, and has no condition: " +
			"a bead that passes over " + tc.first + " goes on to " + tc.warn + " however far the same windows are used."
		if !strings.HasPrefix(got, want) || strings.Count(got, "\n") != 1 {
			t.Errorf("route add %v warned %q, want one line beginning %q", tc.args, got, want)
		}
	}
	if ran() {
		t.Error("route add ran the limits command")
	}
}

// doctor notes a route whose condition is on a group no source answers
// for, with a limits source in the yard and with none.
func TestDoctorNotesARouteWhoseConditionNoSourceAnswersFor(t *testing.T) {
	_, st, ran := quotaCLI(t)
	ctx := context.Background()
	cfg := &config.Config{Home: t.TempDir()}
	for _, g := range []store.Group{
		{Name: "g", Runner: "herdr", Config: map[string]any{"kind": "grok"}},
		{Name: "z", Runner: "herdr", Config: map[string]any{"kind": "claude", "provider": "zai"}},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	answered, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "a", When: "Weekly<80", Else: []store.RouteGroup{{Name: "g"}}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	part, err := limitsPart(ctx, cfg, st, nil, false)
	if err != nil || len(part.Notes) != 0 {
		t.Fatalf("a condition a source answers for, and a group with none: %+v %v", part, err)
	}
	r, err := st.AddRoute(ctx, store.Route{Stage: "review", Group: "g", When: "Weekly<80,5h<90", Else: []store.RouteGroup{{Name: "z", When: "<70"}}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"route " + r.ID + " (stage review) puts the condition Weekly<80,5h<90 on group g, and no source answers for its kind grok: the condition holds whatever is used (limits.kinds)",
		"route " + r.ID + " (stage review) puts the condition <70 on group z, and no source answers for its provider zai: the condition holds whatever is used (limits.kinds)",
	}
	part, err = limitsPart(ctx, cfg, st, nil, false)
	if err != nil || !part.Configured || strings.Join(part.Notes, "\n") != strings.Join(want, "\n") {
		t.Fatalf("notes = %q, %v; want %q", part.Notes, err, want)
	}
	if ran() {
		t.Error("doctor without --try ran the limits command")
	}

	// With no limits source no condition can be evaluated: each is noted.
	bare := storetest.New(t)
	if err := bare.PutGroup(ctx, store.Group{Name: "a", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	lone, err := bare.AddRoute(ctx, store.Route{Stage: "new", Group: "a", When: answered.When}, "me")
	if err != nil {
		t.Fatal(err)
	}
	part, err = limitsPart(ctx, cfg, bare, nil, false)
	if err != nil || part.Configured || len(part.Notes) != 1 ||
		part.Notes[0] != "route "+lone.ID+" (stage new) puts the condition Weekly<80 on group a, and no source answers for its kind claude: the condition holds whatever is used (limits.kinds)" {
		t.Fatalf("with no source: %+v %v", part, err)
	}
}

// doctor notes a comparison that names a source with no limits of its own,
// and stays quiet where the source answers for itself.
func TestDoctorNotesARouteWhoseConditionNamesNoSuchSource(t *testing.T) {
	_, st, _ := quotaCLI(t)
	ctx := context.Background()
	cfg := &config.Config{Home: t.TempDir()}
	r, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "a", When: "ghost.load<80",
		Else: []store.RouteGroup{{Name: "b", When: "ghost.load<80,ghost.swap<80"}}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	part, err := limitsPart(ctx, cfg, st, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"route " + r.ID + " (stage new) puts the condition ghost.load<80 on group a, and no source named ghost says limits of itself: the comparison holds whatever is used (a limits key with no kinds)",
		"route " + r.ID + " (stage new) puts the condition ghost.load<80,ghost.swap<80 on group b, and no source named ghost says limits of itself: the comparison holds whatever is used (a limits key with no kinds)",
	}
	if strings.Join(part.Notes, "\n") != strings.Join(want, "\n") {
		t.Fatalf("notes = %q; want %q", part.Notes, want)
	}
	if _, err := st.PutSource(ctx, "machine.yaml", "name: machine\nlimits:\n  command: [/bin/true]\n", "me"); err != nil {
		t.Fatal(err)
	}
	sourced, err := st.AddRoute(ctx, store.Route{Stage: "review", Group: "a", When: "machine.load<80"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	part, err = limitsPart(ctx, cfg, st, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range part.Notes {
		if strings.Contains(n, sourced.ID) {
			t.Fatalf("a condition the machine source answers for is noted: %q", n)
		}
	}
}

// A herdr group stored without a kind burns the provider of the yard's
// default_kind, as its sessions do, in route add's warning and doctor's
// note alike. (yardr-j6f0: both took it for the runner's own default.)
func TestRouteWarningsTakeAGroupWithoutAKindForTheYardsDefaultKind(t *testing.T) {
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	cfg := &config.Config{Home: t.TempDir()}
	if err := st.PutGroup(ctx, store.Group{Name: "n", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	add := func(args ...string) (id, warned string) {
		t.Helper()
		errs := captureStderr(t)
		id, err := run(t, a.routeCmd(), append([]string{"add"}, args...)...)
		if err != nil {
			t.Fatalf("route add %v: %v", args, err)
		}
		return strings.TrimSpace(id), errs.String()
	}
	// With no default_kind n is of the runner's default, claude, as a is.
	if _, warned := add("--stage", "new", "--group", "a@Weekly<80", "--else", "n"); !strings.Contains(warned, "n burns provider anthropic, as a does") {
		t.Errorf("with no default_kind route add warned %q, want it to warn of n", warned)
	}
	if err := st.SetSetting(ctx, store.DefaultKindSetting, "codex", "me"); err != nil {
		t.Fatal(err)
	}
	if _, warned := add("--stage", "review", "--group", "a@Weekly<80", "--else", "n"); warned != "" {
		t.Errorf("with default_kind codex route add warned %q: n burns another provider than a", warned)
	}
	if _, warned := add("--stage", "merge", "--group", "b@Weekly<80", "--else", "n"); !strings.Contains(warned, "n burns provider openai, as b does") {
		t.Errorf("with default_kind codex route add warned %q, want it to warn of n", warned)
	}

	if err := st.SetSetting(ctx, store.DefaultKindSetting, "grok", "me"); err != nil {
		t.Fatal(err)
	}
	id, _ := add("--stage", "land", "--group", "n@Weekly<80")
	part, err := limitsPart(ctx, cfg, st, nil, false)
	want := "route " + id + " (stage land) puts the condition Weekly<80 on group n, and no source answers for its kind grok: the condition holds whatever is used (limits.kinds)"
	if err != nil || len(part.Notes) != 1 || part.Notes[0] != want {
		t.Fatalf("notes = %q, %v; want %q", part.Notes, err, want)
	}
}
