package cli

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// route add takes --originator, route list prints it, and group show names
// it beside the other filters. A route without the flag takes the
// operator's alone; any takes every originator's.
func TestRouteAddTakesOriginator(t *testing.T) {
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AddUser(ctx, "me", "me"); err != nil {
		t.Fatal(err)
	}
	must(st.SetSetting(ctx, store.OperatorSetting, "me", "me"))
	if _, err := st.AddUser(ctx, "hal", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "hal-builders", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "login_for": "hal", "env_file": "/tmp/hal.env"}}, "me"); err != nil {
		t.Fatal(err)
	}
	id, err := run(t, a.routeCmd(), "add", "--stage", "new", "--originator", "hal", "--group", "hal-builders")
	must(err)
	id = strings.TrimSpace(id)
	if _, err := run(t, a.routeCmd(), "add", "--stage", "new", "--originator", "mallory", "--group", "a"); err == nil ||
		!strings.Contains(err.Error(), "--originator") {
		t.Errorf("a route for an unknown originator: %v", err)
	}
	routes, err := st.Routes(ctx)
	must(err)
	var saved *store.Route
	for i := range routes {
		if routes[i].ID == id {
			saved = &routes[i]
		}
	}
	if saved == nil || saved.Originator == nil || *saved.Originator != "hal" || !saved.ByBead() {
		t.Fatalf("the originator route stored = %+v", saved)
	}
	out, err := run(t, a.routeCmd(), "list")
	must(err)
	var row string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, id) {
			row = strings.Join(strings.Fields(line), " ")
		}
	}
	if want := id + " new - - - - hal 0 hal-builders"; row != want {
		t.Errorf("route list row = %q, want %q", row, want)
	}
	out, err = run(t, a.groupCmd(), "show", "hal-builders")
	must(err)
	if !strings.Contains(out, "login_for=hal") {
		t.Errorf("group show does not print login_for:\n%s", out)
	}
	if !strings.Contains(out, "route "+id+": stage new · originator hal · priority 0") {
		t.Errorf("group show does not say the route's originator:\n%s", out)
	}
	// The flow's stage is who works the operator's beads: hal's route
	// takes none of them.
	must(st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"))
	f, err := store.ParseFlow("new -> merged\nmerged terminal")
	must(err)
	f.Name = "code"
	_, err = st.PutFlow(ctx, *f, "me")
	must(err)
	must(st.SetFlowPointer(ctx, "yard", "", "code", "me"))
	if _, err := run(t, a.routeCmd(), "add", "--stage", "new", "--group", "a"); err != nil {
		t.Fatal(err)
	}
	views, err := flowViews(ctx, st, "yard", "")
	must(err)
	if len(views) != 1 || views[0].Stages[0].Group != "a" {
		t.Fatalf("flow views = %+v, want stage new worked by a", views)
	}
}

// route add warns when a route taking operator beads names a personal-login
// group first, and doctor warns on a login_for group with no env_file —
// exempting login_for for the operator.
func TestRouteWarnsOnPersonalGroupForOperatorBeads(t *testing.T) {
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	errs := captureStderr(t)
	if _, err := st.AddUser(ctx, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "hal", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "hal-builders", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "login_for": "hal"}}, "me"); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, a.routeCmd(), "add", "--stage", "new", "--group", "hal-builders")
	if err != nil {
		t.Fatal(err)
	}
	if got := errs.String(); !strings.Contains(got, "hal-builders declares login_for=hal") ||
		!strings.Contains(got, "burns hal's login for the operator's work") {
		t.Errorf("route add warned %q, want the personal-login warning", got)
	}
	rep, _ := doctorJSON(t, st)
	joined := strings.Join(rep.State.Config, "\n")
	if !strings.Contains(joined, "group hal-builders declares login_for=hal and names no env_file") {
		t.Errorf("doctor's config notes lack the login_for warning:\n%s", joined)
	}
	// login_for for the operator needs no env_file and warns of nothing.
	if err := st.PutGroup(ctx, store.Group{Name: "ops", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "login_for": "me"}}, "me"); err != nil {
		t.Fatal(err)
	}
	rep, _ = doctorJSON(t, st)
	for _, n := range rep.State.Config {
		if strings.Contains(n, "group ops") {
			t.Errorf("doctor warns on the operator's login_for group: %q", n)
		}
	}
	errs.Reset()
	if _, err := run(t, a.routeCmd(), "add", "--stage", "review", "--group", "ops"); err != nil {
		t.Fatal(err)
	}
	if got := errs.String(); strings.Contains(got, "login_for=me") {
		t.Errorf("route add warns on the operator's login_for group: %q", got)
	}
}
