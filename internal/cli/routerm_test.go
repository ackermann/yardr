package cli

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// routeIDs is a store holding the routes a test chooses, ids and all, where
// AddRoute mints them at random, and the ids it was asked to delete.
type routeIDs struct {
	store.API
	routes  []store.Route
	deleted []string
}

func (s *routeIDs) Routes(context.Context) ([]store.Route, error) { return s.routes, nil }

func (s *routeIDs) Sessions(context.Context, bool) ([]store.Session, error) { return nil, nil }

func (s *routeIDs) DeleteRoute(_ context.Context, id, _ string) error {
	s.deleted = append(s.deleted, id)
	return nil
}

// route rm takes a route's whole id, or the start of it that only that
// route's id has, and hands the store the whole id. An id no route has, a
// start that fits several routes and one too short to go by are refused
// with nothing deleted. The store takes the removal of a route that is not
// there for done, so a mistyped id used to print nothing and exit 0
// (yardr-fsqg).
func TestRouteRmResolvesTheIdTyped(t *testing.T) {
	t.Parallel()
	routes := []store.Route{
		{ID: "k3f9m2aaaaaaaaaaaaaa", Stage: "new", Group: "builders"},
		{ID: "k3f9m2bbbbbbbbbbbbbb", Stage: "review", Group: "reviewers"},
		{ID: "q7t4x1cccccccccccccc", Stage: "approved", Group: "assembly"},
	}
	for _, tc := range []struct {
		name, ref string
		deleted   string // the id the store was asked to delete, "" for none
		refused   string // what the refusal says, "" when route rm goes through
		exit      int
	}{
		{name: "the whole id", ref: "k3f9m2bbbbbbbbbbbbbb", deleted: "k3f9m2bbbbbbbbbbbbbb"},
		{name: "a start only one route has", ref: "q7t4", deleted: "q7t4x1cccccccccccccc"},
		{name: "a start longer than it needs", ref: "k3f9m2b", deleted: "k3f9m2bbbbbbbbbbbbbb"},
		{name: "an id no route has", ref: "uuuu", refused: "no route uuuu", exit: 3},
		{name: "a start that fits two routes", ref: "k3f9", exit: 1,
			refused: "route k3f9 matches 2 routes: k3f9m2aaaaaaaaaaaaaa, k3f9m2bbbbbbbbbbbbbb; give more of its id"},
		{name: "a start too short to go by", ref: "q7t", exit: 1,
			refused: "route q7t: give at least 4 characters of its id, or all of it"},
		{name: "a start no route has, however short", ref: "u", refused: "no route u", exit: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &routeIDs{routes: routes}
			a := &app{st: st, actor: "me"}
			_, err := run(t, a.routeCmd(), "rm", tc.ref)
			if tc.refused == "" {
				if err != nil || !slices.Equal(st.deleted, []string{tc.deleted}) {
					t.Fatalf("route rm %s = %v, deleted %v; want %s deleted", tc.ref, err, st.deleted, tc.deleted)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.refused) || exitCode(err) != tc.exit || len(st.deleted) != 0 {
				t.Fatalf("route rm %s = %v (exit %d), deleted %v; want it refused with %q (exit %d) and nothing deleted",
					tc.ref, err, exitCode(err), st.deleted, tc.refused, tc.exit)
			}
		})
	}
}

// The report as it came, against the real store: route rm of the start of
// an id removed nothing and said nothing, and so did one of an id no route
// has. The start now removes its route and no other, and an id no route
// has changes nothing.
func TestRouteRmRemovesTheRouteNamed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := storetest.New(t)
	if err := st.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Members: []string{"jo"}}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	var ids []string
	for _, stage := range []string{"new", "review"} {
		id, err := run(t, a.routeCmd(), "add", "--stage", stage, "--group", "people")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, strings.TrimSpace(id))
	}
	// The start of the first id that the second does not share: ids made
	// within a few milliseconds begin alike.
	n := store.MinPrefix
	for strings.HasPrefix(ids[1], ids[0][:n]) {
		n++
	}

	if _, err := run(t, a.routeCmd(), "rm", "uuuu"); err == nil || !strings.Contains(err.Error(), "no route uuuu") {
		t.Fatalf("route rm of an id no route has = %v", err)
	}
	if routes, err := st.Routes(ctx); err != nil || len(routes) != 2 {
		t.Fatalf("after the refused rm: %+v, %v; want both routes", routes, err)
	}

	if _, err := run(t, a.routeCmd(), "rm", ids[0][:n]); err != nil {
		t.Fatal(err)
	}
	routes, err := st.Routes(ctx)
	if err != nil || len(routes) != 1 || routes[0].ID != ids[1] {
		t.Fatalf("after rm %s: %+v, %v; want the second route alone", ids[0][:n], routes, err)
	}
	evs, err := st.EventsOfKind(ctx, "route_removed", 50)
	if err != nil || len(evs) != 1 || evs[0].Data["id"] != ids[0] {
		t.Fatalf("route_removed events = %+v, %v; want one, of the first route", evs, err)
	}
}

// Every rm refuses a name that is not there, with the exit status of
// something not found. flow rm and crew rm always did; group rm and route
// rm took it for done, and now refuse it too.
func TestRmRefusesWhatIsNotThere(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := storetest.New(t)
	a := &app{st: st, actor: "me"}
	for _, tc := range []struct {
		cmd  func() *cobra.Command
		want string
	}{
		{a.groupCmd, "group nosuch: not found"},
		{a.flowCmd, "flow nosuch: not found"},
		{a.crewCmd, "crew member nosuch: not found"},
		{a.routeCmd, "no route nosuch: not found"},
	} {
		_, err := run(t, tc.cmd(), "rm", "nosuch")
		if err == nil || !errors.Is(err, store.ErrNotFound) || exitCode(err) != 3 || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("rm nosuch = %v (exit %d), want %q and exit 3", err, exitCode(err), tc.want)
		}
	}

	// A group that is there goes, and a second rm of it is refused.
	if err := st.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Members: []string{"jo"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.groupCmd(), "rm", "people"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetGroup(ctx, "people"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("group people after rm: %v", err)
	}
	if _, err := run(t, a.groupCmd(), "rm", "people"); exitCode(err) != 3 {
		t.Fatalf("the second group rm = %v, want not found", err)
	}
}

// Deleting a group a templated default resolves to leaves the default and
// stops it serving that depot; group rm says so, and the depot's list names
// the default as unserved.
func TestGroupRmNamesStoppedTemplatedDefault(t *testing.T) {
	t.Parallel()
	a, st, _ := quotaCLI(t)
	ctx := context.Background()
	for _, depot := range []string{"web", "other"} {
		if err := st.PutDepot(ctx, store.Depot{Name: depot}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []store.Group{
		{Name: "web-assembly", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "other-assembly", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	id, err := run(t, a.routeCmd(), "add", "--stage", "approved", "--group", "{depot}-assembly")
	if err != nil {
		t.Fatal(err)
	}
	id = strings.TrimSpace(id)
	out, err := run(t, a.groupCmd(), "rm", "web-assembly")
	if err != nil {
		t.Fatal(err)
	}
	if want := "default " + id + " (approved -> {depot}-assembly) no longer serves web: no group web-assembly"; !strings.Contains(out, want) {
		t.Errorf("group rm web-assembly lacks %q:\n%s", want, out)
	}
	if routes, err := st.Routes(ctx); err != nil || len(routes) != 1 || routes[0].ID != id {
		t.Fatalf("routes after group rm = %+v, %v; want the templated default left", routes, err)
	}
	out, err = run(t, a.routeCmd(), "list", "--depot", "web")
	if err != nil {
		t.Fatal(err)
	}
	if want := "default " + id + " (approved -> {depot}-assembly) does not serve web: no group web-assembly"; !strings.Contains(out, want) {
		t.Errorf("route list --depot web lacks %q:\n%s", want, out)
	}
	out, err = run(t, a.routeCmd(), "list", "--depot", "other")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "does not serve") || !strings.Contains(out, "other-assembly") {
		t.Errorf("route list --depot other lost its resolved default:\n%s", out)
	}
}
