package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// route add takes --note, and route list prints the scope and the note:
// the SCOPE column names the depot's own routes and the yard defaults,
// and the NOTE column carries what the priority and the chain cannot,
// in words and in --json.
func TestRouteAddTakesNoteAndListPrintsScopeAndNote(t *testing.T) {
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	def, err := run(t, a.routeCmd(), "add", "--stage", "new", "--group", "a",
		"--note", "the plain line: whatever the week says")
	if err != nil {
		t.Fatal(err)
	}
	own, err := run(t, a.routeCmd(), "add", "--stage", "new", "--depot", "web", "--group", "b", "--priority", "2")
	if err != nil {
		t.Fatal(err)
	}
	def, own = strings.TrimSpace(def), strings.TrimSpace(own)

	routes, err := st.Routes(ctx)
	if err != nil || len(routes) != 2 {
		t.Fatalf("routes = %+v, %v", routes, err)
	}
	for _, r := range routes {
		if r.ID == def && r.Note != "the plain line: whatever the week says" {
			t.Errorf("the default stored = %+v, want the note", r)
		}
	}

	out, err := run(t, a.routeCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		rows = append(rows, strings.Join(strings.Fields(line), " "))
	}
	want := []string{
		"ID SCOPE STAGE TYPE LABEL BEADS ORIGINATOR PRI GROUP NOTE",
		own + " web new - - - - 2 b",
		def + " default new - - - - 0 a the plain line: whatever the week says",
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Errorf("route list =\n%s\nwant\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}

	a.json = true
	out, err = run(t, a.routeCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	var listed []store.Route
	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed) != 2 {
		t.Fatalf("route list --json = %s, %v; want the two routes", out, err)
	}
	for _, r := range listed {
		if r.ID == def && r.Note != "the plain line: whatever the week says" {
			t.Errorf("route list --json carries %+v, want the note", r)
		}
	}
}

// route list --depot lists the depot's effective routes in match order,
// each marked own or default, with the defaults its own routes set aside
// named underneath, each with the own route that states its key. A depot
// that sets nothing aside names nothing, and a depot that is not there
// fails as depot show does.
func TestRouteListDepotShowsEffectiveRoutes(t *testing.T) {
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "other"} {
		if err := st.PutDepot(ctx, store.Depot{Name: name}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	add := func(args ...string) string {
		t.Helper()
		id, err := run(t, a.routeCmd(), append([]string{"add"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(id)
	}
	plain := add("--stage", "new", "--group", "a")
	typed := add("--stage", "new", "--type", "bug", "--group", "a")
	review := add("--stage", "review", "--group", "people")
	ownPlain := add("--stage", "new", "--depot", "web", "--group", "b", "--priority", "2")
	ownTyped := add("--stage", "new", "--type", "bug", "--depot", "web", "--group", "b", "--priority", "1")

	out, err := run(t, a.routeCmd(), "list", "--depot", "web")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var rows []string
	for _, line := range lines {
		rows = append(rows, strings.Join(strings.Fields(line), " "))
	}
	// In match order: the own routes by priority, then the review
	// default; the new defaults are set aside below the table.
	want := []string{
		"ID OWN/DEFAULT STAGE TYPE LABEL BEADS ORIGINATOR PRI GROUP NOTE",
		ownPlain + " own new - - - me 2 b",
		ownTyped + " own new bug - - me 1 b",
		review + " default review - - - any 0 people",
		"2 defaults set aside in web:",
		"new (type bug, no label, the operator's beads) is stated by " + ownTyped + " to b at priority 1",
		"new (no label, the operator's beads) is stated by " + ownPlain + " to b at priority 2",
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Errorf("route list --depot web =\n%s\nwant\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}

	// One default set aside reads as one line, and a depot that states
	// nothing names nothing.
	if err := st.PutDepot(ctx, store.Depot{Name: "third"}, "me"); err != nil {
		t.Fatal(err)
	}
	solo := add("--stage", "new", "--depot", "other", "--group", "b")
	out, err = run(t, a.routeCmd(), "list", "--depot", "other")
	if err != nil {
		t.Fatal(err)
	}
	if want := "1 default set aside in other: new (no label, the operator's beads) is stated by " + solo + " to b at priority 0"; !strings.Contains(out, want) {
		t.Errorf("route list --depot other lacks %q:\n%s", want, out)
	}
	var table []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "1 default") {
			break
		}
		table = append(table, line)
	}
	joined := strings.Join(table, "\n")
	if strings.Contains(joined, plain) {
		t.Errorf("route list --depot other still tables the plain default it sets aside:\n%s", out)
	}
	if !strings.Contains(joined, typed) || !strings.Contains(joined, review) {
		t.Errorf("route list --depot other lost a default it sets nothing of:\n%s", out)
	}
	out, err = run(t, a.routeCmd(), "list", "--depot", "third")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "set aside") {
		t.Errorf("route list --depot third names set-aside defaults, want none:\n%s", out)
	}

	if _, err := run(t, a.routeCmd(), "list", "--depot", "ghost"); exitCode(err) != 3 ||
		!strings.Contains(err.Error(), "depot ghost: not found") {
		t.Errorf("route list --depot ghost: %v, want it to fail as depot show does", err)
	}
}

// route list --depot --json carries each route as stored plus whether
// it is the depot's own or a yard default, and the set-aside defaults
// with the own route that sets each aside.
func TestRouteListDepotJson(t *testing.T) {
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	def, err := run(t, a.routeCmd(), "add", "--stage", "new", "--group", "a")
	if err != nil {
		t.Fatal(err)
	}
	own, err := run(t, a.routeCmd(), "add", "--stage", "new", "--depot", "web", "--group", "b")
	if err != nil {
		t.Fatal(err)
	}
	def, own = strings.TrimSpace(def), strings.TrimSpace(own)

	a.json = true
	out, err := run(t, a.routeCmd(), "list", "--depot", "web")
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Routes []struct {
			store.Route
			Effective string `json:"effective"`
		} `json:"routes"`
		SetAside []struct {
			Route store.Route `json:"route"`
			Own   store.Route `json:"own"`
		} `json:"set_aside"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("route list --depot web --json = %s, %v", out, err)
	}
	if len(listed.Routes) != 1 || listed.Routes[0].ID != own || listed.Routes[0].Effective != "own" ||
		listed.Routes[0].Group != "b" {
		t.Errorf("routes = %+v, want web's own route alone, marked own", listed.Routes)
	}
	if len(listed.SetAside) != 1 || listed.SetAside[0].Route.ID != def || listed.SetAside[0].Own.ID != own {
		t.Errorf("set_aside = %+v, want the default %s set by %s", listed.SetAside, def, own)
	}
}

// A route's note is its writer's words on one line of the list: a line
// break or an escape in it starts no line of its own and moves no
// cursor, in the list and in what route set says it changed to.
func TestRouteNotePrintsOnOneLine(t *testing.T) {
	a, _, _ := quotaCLI(t)
	forged := "first line\nforged row\x1b(0`"
	id, err := run(t, a.routeCmd(), "add", "--stage", "new", "--group", "a", "--note", forged)
	if err != nil {
		t.Fatal(err)
	}
	id = strings.TrimSpace(id)
	out, err := run(t, a.routeCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "\nforged row") ||
		!strings.Contains(out, "first line forged row(0`") {
		t.Errorf("route list prints the note unguarded:\n%q", out)
	}
	out, err = run(t, a.routeCmd(), "set", id, "--note", forged+" again")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\x1b") || strings.Contains(out, "\nforged row") ||
		!strings.Contains(out, "first line forged row(0` again") {
		t.Errorf("route set prints the note unguarded:\n%q", out)
	}
}
