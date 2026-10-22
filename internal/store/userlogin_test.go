package store

import (
	"reflect"
	"testing"
)

// LoginOf finds what carries a user's own login by the groups that declare
// it and by name: a route that names such a group after another one, a
// route for their beads that leads to the operator's group, and a crew
// member that serves them or keeps a conversation for them, each once.
func TestLoginOf(t *testing.T) {
	ada, bob := "ada", "bob"
	groups := []Group{
		{Name: "builders", Runner: "herdr"},
		{Name: "ada-builders", Runner: "herdr", Config: map[string]any{"login_for": "ada", "env_file": "/y/groups/ada.env"}},
		{Name: "ada-spare", Runner: "herdr", Config: map[string]any{"login_for": "ada", "env_file": "/y/groups/ada.env"}},
		{Name: "bob-builders", Runner: "herdr", Config: map[string]any{"login_for": "bob"}},
	}
	routes := []Route{
		{ID: "r1", Stage: "new", Group: "builders"},
		{ID: "r2", Stage: "new", Group: "ada-builders", Originator: &ada},
		{ID: "r3", Stage: "review", Group: "builders", Originator: &ada}, // a subsidy route is theirs too
		{ID: "r4", Stage: "new", Group: "builders", Else: []RouteGroup{{Name: "ada-spare"}}},
		{ID: "r5", Stage: "new", Group: "bob-builders", Originator: &bob},
	}
	crew := []CrewMember{
		{Name: "yardmaster", Config: map[string]any{"for": "ada,bob", "env_for_ada": "/y/groups/ada.env"}},
		{Name: "clerk", Config: map[string]any{"for": []any{"ada"}}},
		{Name: "boss", Config: map[string]any{"env_for_ada": "/y/groups/ada.env"}},
		{Name: "idle", Config: map[string]any{"for": "bob"}},
	}
	got := LoginOf("ada", groups, routes, crew)
	want := UserLogin{
		Groups:   []string{"ada-builders", "ada-spare"},
		EnvFiles: []string{"/y/groups/ada.env"},
		Routes:   []string{"r2", "r3", "r4"},
		Crew:     []string{"boss", "clerk", "yardmaster"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoginOf(ada) = %+v\nwant %+v", got, want)
	}
	if got := LoginOf("eve", groups, routes, crew); !got.Empty() || len(got.Said("eve")) != 0 {
		t.Fatalf("LoginOf(eve) = %+v: nothing carries a login nobody has", got)
	}
}

// unserve takes the user out of a crew member's for= in the form it was
// written in, and out of its env_for_ key; a list the removal empties is
// dropped whole, since an empty for= serves everyone.
func TestUnserve(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  map[string]any
		want map[string]any
	}{
		{"a string of names", map[string]any{"for": "ada,jo", "env_for_ada": "/a.env", "env_for_jo": "/j.env"}, map[string]any{"for": "jo", "env_for_jo": "/j.env"}},
		{"a list", map[string]any{"for": []any{"ada", "jo"}}, map[string]any{"for": []any{"jo"}}},
		{"the only name", map[string]any{"for": "ada", "env_for_ada": "/a.env", "kind": "claude"}, map[string]any{"kind": "claude"}},
		{"the only name in a list", map[string]any{"for": []any{"ada"}}, map[string]any{}},
		{"served by not naming", map[string]any{"env_for_ada": "/a.env"}, map[string]any{}},
		{"another's alone", map[string]any{"for": "jo"}, map[string]any{"for": "jo"}},
	} {
		unserve(c.cfg, "ada")
		if !reflect.DeepEqual(c.cfg, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, c.cfg, c.want)
		}
	}
}
