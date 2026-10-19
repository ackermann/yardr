package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/flowdraw"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// quotaCLI is a yard with two groups that start agents, one of people,
// and a limits source whose command leaves a file behind when it runs:
// only the dispatcher may run it.
// routeErr runs route with its error stream in errs.
func routeErr(t *testing.T, a *app, errs io.Writer, args ...string) (string, error) {
	t.Helper()
	c := a.routeCmd()
	c.SetErr(errs)
	return run(t, c, args...)
}

func quotaCLI(t *testing.T) (a *app, st store.API, ran func() bool) {
	t.Helper()
	st = storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	script := filepath.Join(dir, "provider")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch "+dir+"/asked\necho '{\"provider\":\"Claude\",\"windows\":[]}'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "quokka.yaml", "name: quokka\nlimits:\n  command: ["+script+
		", \"{provider}\"]\n  kinds: { claude: anthropic, codex: openai }\n", "me"); err != nil {
		t.Fatal(err)
	}
	for _, g := range []store.Group{
		{Name: "a", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "b", Runner: "herdr", Config: map[string]any{"kind": "codex"}},
		{Name: "people", Runner: "manual", Members: []string{"jo"}},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	return &app{st: st, actor: "me"}, st, func() bool {
		_, err := os.Stat(filepath.Join(dir, "asked"))
		return err == nil
	}
}

// route add takes a condition after @ and further groups with --else, and
// route list prints the groups in order with their conditions.
func TestRouteAddTakesFurtherGroupsAndConditions(t *testing.T) {
	a, st, ran := quotaCLI(t)
	ctx := context.Background()
	id, err := run(t, a.routeCmd(), "add", "--stage", "new", "--group", "a@Weekly<80,5h<90", "--else", "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.routeCmd(), "add", "--stage", "review", "--group", "people"); err != nil {
		t.Fatal(err)
	}
	routes, err := st.Routes(ctx)
	if err != nil || len(routes) != 2 {
		t.Fatalf("routes = %+v, %v", routes, err)
	}
	r := routes[0]
	if r.ID != strings.TrimSpace(id) || r.GroupList() != "a@Weekly<80,5h<90 else b" {
		t.Fatalf("the route stored = %+v (%s), want %s with a's condition, then b", r, r.GroupList(), id)
	}
	out, err := run(t, a.routeCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	var row, plain string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, r.ID) {
			row = strings.Join(strings.Fields(line), " ")
		} else if strings.Contains(line, "review") {
			plain = strings.Join(strings.Fields(line), " ")
		}
	}
	if want := r.ID + " default new - - - - 0 a@Weekly<80,5h<90 else b"; row != want {
		t.Errorf("route list row = %q, want %q", row, want)
	}
	if !strings.HasSuffix(plain, " default review - - - any 0 people") {
		t.Errorf("a route with one group lists as %q, want its group alone", plain)
	}
	if out, err := run(t, a.groupCmd(), "show", "b"); err != nil || !strings.Contains(out, r.ID) {
		t.Errorf("group show b does not list the route that names it with --else: %v\n%s", err, out)
	}

	// --else or a condition with a group that starts no agents, and what
	// does not parse.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--group", "a", "--else", "people"}, "group people is manual: --else and a quota condition are for groups that start agents"},
		{[]string{"--group", "people@Weekly<80"}, "group people is manual: --else and a quota condition are for groups that start agents"},
		{[]string{"--group", "a@Weekly"}, `is not <window><<percent used>`},
		{[]string{"--group", "a", "--else", "ghost"}, "group ghost"},
	} {
		c := a.routeCmd()
		if _, err := run(t, c, append([]string{"add", "--stage", "new"}, tc.args...)...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("route add %v: %v, want %q", tc.args, err, tc.want)
		}
	}
	if routes, _ := st.Routes(ctx); len(routes) != 2 {
		t.Fatalf("a refused route was stored: %+v", routes)
	}

	// group add on a group such a route names refuses another runner.
	for _, name := range []string{"a", "b"} {
		if _, err := run(t, a.groupCmd(), "add", name, "--runner", "manual"); err == nil ||
			!strings.Contains(err.Error(), "names it with --else or a quota condition") {
			t.Errorf("group add %s --runner manual: %v", name, err)
		}
		if g, _ := st.GetGroup(ctx, name); g.Runner != "herdr" {
			t.Errorf("group %s became %s", name, g.Runner)
		}
	}
	if ran() {
		t.Error("a command of the CLI ran the limits command")
	}
}

// route add takes a condition on the window's target, with an optional
// margin, and route list prints it as written; a malformed right side is
// refused with a message that names both forms.
func TestRouteAddTakesATargetCondition(t *testing.T) {
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	id, err := run(t, a.routeCmd(), "add", "--stage", "new", "--group", "a@Weekly<target+10", "--else", "b")
	if err != nil {
		t.Fatal(err)
	}
	routes, err := st.Routes(ctx)
	if err != nil || len(routes) != 1 || routes[0].ID != strings.TrimSpace(id) ||
		routes[0].GroupList() != "a@Weekly<target+10 else b" {
		t.Fatalf("the route stored = %+v, %v; want a@Weekly<target+10 else b", routes, err)
	}
	out, err := run(t, a.routeCmd(), "list")
	if err != nil || !strings.Contains(strings.Join(strings.Fields(out), " "), "a@Weekly<target+10 else b") {
		t.Fatalf("route list does not print the condition as written: %v\n%s", err, out)
	}
	for _, bad := range []string{
		"a@Weekly<target+lots",
		"a@Weekly<target+",
		"a@Weekly<target++1",
		"a@Weekly<target-+1",
		"a@Weekly<lots",
	} {
		_, err := run(t, a.routeCmd(), "add", "--stage", "new", "--group", bad)
		if err == nil || !strings.Contains(err.Error(), "is not <window><<percent used>") ||
			!strings.Contains(err.Error(), "nor <window><target> with an optional margin") {
			t.Errorf("route add --group %s: %v, want a refusal naming both forms", bad, err)
		}
	}
	if routes, _ := st.Routes(ctx); len(routes) != 1 {
		t.Fatalf("a refused route was stored: %+v", routes)
	}
}

// route add takes a condition on a source's window, wait after the groups,
// and one exec group gated on a source alone; route list prints them as
// written. wait ends the route, so nothing warns of it.
func TestRouteAddTakesASourceConditionWaitAndOneExecGroup(t *testing.T) {
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	if err := st.PutGroup(ctx, store.Group{Name: "refinery", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--group", "a@machine.load<80", "--else", "wait"}, "a@machine.load<80 else wait"},
		{[]string{"--group", "refinery@machine.load<80", "--else", "wait"}, "refinery@machine.load<80 else wait"},
		{[]string{"--group", "a@machine.load<80,Weekly<80", "--else", "b"}, "a@machine.load<80,Weekly<80 else b"},
	} {
		errs := captureStderr(t)
		id, err := routeErr(t, a, errs, append([]string{"add", "--stage", "new", "--priority", strconv.Itoa(i + 1)}, tc.args...)...)
		if err != nil {
			t.Fatalf("route add %v: %v", tc.args, err)
		}
		routes, err := st.Routes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		i := -1
		for n, r := range routes {
			if r.ID == strings.TrimSpace(id) {
				i = n
			}
		}
		if i < 0 || routes[i].GroupList() != tc.want {
			t.Fatalf("route add %v stored %v, want %s", tc.args, routes, tc.want)
		}
		if got := errs.String(); got != "" {
			t.Errorf("route add %v warned: %s", tc.args, got)
		}
	}
	out, err := run(t, a.routeCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a@machine.load<80 else wait", "refinery@machine.load<80 else wait", "a@machine.load<80,Weekly<80 else b"} {
		if !strings.Contains(strings.Join(strings.Fields(out), " "), want) {
			t.Errorf("route list has no %q:\n%s", want, out)
		}
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--group", "wait"}, "wait ends a route (--else wait): it is not a group to send to"},
		{[]string{"--group", "a@Weekly<80", "--else", "wait@Weekly<80"}, "wait takes no condition"},
		{[]string{"--group", "a@Weekly<80", "--else", "wait", "--else", "b"}, "nothing is tried after wait"},
		{[]string{"--group", "refinery@Weekly<80"}, "group refinery starts no agents"},
		{[]string{"--group", "a", "--else", "refinery"}, "group refinery is exec: a route with several groups names only groups that start agents"},
		{[]string{"--group", "people", "--else", "wait"}, "group people is manual: --else and a quota condition are for groups that start agents"},
		{[]string{"--group", "a@.load<80"}, `is not <window><<percent used>`},
	} {
		if _, err := run(t, a.routeCmd(), append([]string{"add", "--stage", "new"}, tc.args...)...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("route add %v: %v, want %q", tc.args, err, tc.want)
		}
	}
	if routes, _ := st.Routes(ctx); len(routes) != 3 {
		t.Fatalf("a refused route was stored: %+v", routes)
	}
}

// flow show names a route's groups in order for a stage, and review and
// the member check answer for a route with one manual group as they did.
// None of them runs the limits command.
func TestViewsNameARoutesGroupsInOrder(t *testing.T) {
	a, st, ran := quotaCLI(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"))
	f, err := store.ParseFlow("new -> review on done\nreview -> merged\nmerged terminal")
	must(err)
	f.Name = "code"
	_, err = st.PutFlow(ctx, *f, "me")
	must(err)
	must(st.SetFlowPointer(ctx, "yard", "", "code", "me"))
	_, err = st.AddRoute(ctx, store.Route{Stage: "new", Group: "a", When: "Weekly<80", Else: []store.RouteGroup{{Name: "b"}}}, "me")
	must(err)
	_, err = st.AddRoute(ctx, store.Route{Stage: "review", Group: "people"}, "me")
	must(err)
	waiting, err := st.CreateBead(ctx, store.NewBead{Title: "waiting", Depot: "yard"}, "me")
	must(err)
	inReview, err := st.CreateBead(ctx, store.NewBead{Title: "in review", Depot: "yard", Stage: "review"}, "me")
	must(err)

	views, err := flowViews(ctx, st, "yard", "")
	must(err)
	if len(views) != 1 || len(views[0].Stages) != 3 {
		t.Fatalf("views = %+v", views)
	}
	if s := views[0].Stages[0]; s.Group != "a" || strings.Join(s.Else, ",") != "b" || s.Human || len(s.Members) != 0 {
		t.Errorf("stage new = %+v, want a, then b, worked by agents", s)
	}
	if s := views[0].Stages[1]; s.Group != "people" || s.Else != nil || !s.Human || strings.Join(s.Members, ",") != "jo" {
		t.Errorf("stage review = %+v, want people by hand, as before", s)
	}
	out, err := run(t, a.flowCmd(), "show", "yard")
	must(err)
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "new a else b - 1") {
		t.Errorf("flow show does not name both groups of stage new:\n%s", out)
	}
	diagram, err := depotDiagram(io.Discard, ctx, st, "yard", "", "")
	must(err)
	if svg := string(flowdraw.SVG(*diagram)); !strings.Contains(svg, ">a else b</text>") {
		t.Errorf("flow show --svg does not name both groups of stage new:\n%s", svg)
	}

	// The queue of a person: the bead of the manual group, for its member
	// only; the bead that waits for agents is nobody's to review.
	for actor, want := range map[string]string{"jo": inReview.ID, "mo": ""} {
		items, err := reviewQueue(ctx, st, actor, nil)
		must(err)
		got := []string{}
		for _, it := range items {
			got = append(got, it.Bead.ID)
		}
		if strings.Join(got, ",") != want {
			t.Errorf("review queue of %s = %v, want %q (not %s, which waits for agents)", actor, got, want, waiting.ID)
		}
	}
	// checkMembers: who is no member is refused, a member moves it.
	if _, err := st.Advance(ctx, inReview.ID, "merged", store.Expect{}, "mo"); err == nil || !strings.Contains(err.Error(), "only members of group people") {
		t.Errorf("a move out of review by mo: %v", err)
	}
	if _, err := st.Advance(ctx, inReview.ID, "merged", store.Expect{}, "jo"); err != nil {
		t.Errorf("a move out of review by jo: %v", err)
	}
	// A bead no group has claimed at a route with several groups has no
	// members to ask: anyone moves it, as with one group of agents.
	if _, err := st.Advance(ctx, waiting.ID, "review", store.Expect{}, "mo"); err != nil {
		t.Errorf("a move out of new by mo: %v", err)
	}
	if ran() {
		t.Error("a view ran the limits command")
	}
}
