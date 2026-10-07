package store_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// SetGroup changes what it is given of a group and keeps the rest, the
// subscriptions with it, where PutGroup writes the group whole (yardr-9zv7).
func TestSetGroupKeepsTheRest(t *testing.T) {
	s, ctx := flowDepot(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	events := func() int {
		t.Helper()
		evs, err := s.EventsOfKind(ctx, "group_set", 50)
		must(err)
		return len(evs)
	}
	must(s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2,
		Config: map[string]any{"kind": "claude", "model": "opus", "args": []string{"-p"}}}, "jo"))
	n := events()

	limit := 4
	ch, err := s.SetGroup(ctx, "builders", store.GroupEdit{Set: map[string]any{"model": "sonnet"}, Unset: []string{"kind"}, Limit: &limit}, "ada")
	must(err)
	want := map[string]any{"model": "sonnet", "args": []any{"-p"}}
	if ch.Was.Config["model"] != "opus" || ch.Was.Limit != 2 || !reflect.DeepEqual(ch.Is.Config, want) || ch.Is.Limit != 4 {
		t.Fatalf("change = %+v", ch)
	}
	g, err := s.GetGroup(ctx, "builders")
	must(err)
	if !reflect.DeepEqual(g.Config, want) || g.Limit != 4 || g.Runner != "herdr" {
		t.Fatalf("stored = %+v", g)
	}
	evs, err := s.EventsOfKind(ctx, "group_set", 1)
	must(err)
	if events() != n+1 || evs[0].Actor != "ada" || evs[0].Data["name"] != "builders" {
		t.Fatalf("group_set after a set: %d events, last %+v", events()-n, evs[0])
	}

	// Set so already, a list given as another type included: no write.
	ch, err = s.SetGroup(ctx, "builders", store.GroupEdit{Set: map[string]any{"args": []string{"-p"}}, Limit: &limit}, "ada")
	must(err)
	if !reflect.DeepEqual(ch.Was, ch.Is) || events() != n+1 {
		t.Fatalf("a set that changes nothing: %+v, %d events", ch, events()-n)
	}

	// Refused, and nothing written: no group, a key that is not there, a
	// limit under 1, and what PutGroup refuses of the result.
	zero := 0
	members := []string{"jo"}
	for want, e := range map[string]store.GroupEdit{
		"--unset kind: builders has no such setting (it has: args, model)": {Unset: []string{"kind"}},
		"a limit is at least 1":              {Limit: &zero},
		"members are only for manual groups": {Members: &members},
	} {
		if _, err := s.SetGroup(ctx, "builders", e, "ada"); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("SetGroup(%+v) = %v, want %q", e, err, want)
		}
	}
	if _, err := s.SetGroup(ctx, "nobody", store.GroupEdit{Limit: &limit}, "ada"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SetGroup of no group = %v", err)
	}
	if got, err := s.GetGroup(ctx, "builders"); err != nil || !reflect.DeepEqual(got, g) || events() != n+1 {
		t.Fatalf("a refused set wrote: %+v, %v", got, err)
	}

	// Members of a manual group are replaced, deduplicated as PutGroup does.
	must(s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual", Members: []string{"jo"}, Config: map[string]any{"notify": true}}, "jo"))
	members = []string{"ada", " jo", "ada"}
	ch, err = s.SetGroup(ctx, "people", store.GroupEdit{Members: &members}, "jo")
	must(err)
	if strings.Join(ch.Is.Members, ",") != "ada,jo" || ch.Is.Config["notify"] != true {
		t.Fatalf("members set: %+v", ch.Is)
	}
}
