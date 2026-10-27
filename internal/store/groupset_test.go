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
	t.Parallel()
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
	// negative limit, and what PutGroup refuses of the result.
	neg := -1
	members := []string{"jo"}
	for want, e := range map[string]store.GroupEdit{
		"--unset kind: builders has no such setting (it has: args, model)": {Unset: []string{"kind"}},
		"a limit is 0 or more":               {Limit: &neg},
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

// A limit of 0 pauses, and only SetGroup sets one: it is stored, every
// claim is refused, and the group's live session runs on while its bead
// advances. 0 through PutGroup is no limit named: a new group starts with
// the default of its runner (1; 50 for people), and so does a replaced one
// — except a paused group put back, which stays paused. A negative limit
// is refused everywhere.
func TestGroupLimitZeroPauses(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// A new group that names no limit starts with one session, as before.
	must(s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "jo"))
	if g, err := s.GetGroup(ctx, "builders"); err != nil || g.Limit != 1 {
		t.Fatalf("new group = %+v, %v; want limit 1", g, err)
	}
	// A group of people starts with 50 instead: they read what waits for
	// them as a list, and a limit of 1 let one parked decision hold back
	// every other bead for the group. A script group, like an agent group,
	// keeps 1, and so does a peer's.
	must(s.PutGroup(ctx, store.Group{Name: "tick", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "jo"))
	must(s.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual"}, "jo"))
	for name, want := range map[string]int{"tick": 1, "decisions": 50} {
		if g, err := s.GetGroup(ctx, name); err != nil || g.Limit != want {
			t.Fatalf("new group %s = %+v, %v; want limit %d", name, g, err, want)
		}
	}
	if got := store.DefaultLimit(store.PeerRunner); got != 1 {
		t.Fatalf("DefaultLimit(peer) = %d, want 1", got)
	}
	// A negative limit is refused, not floored.
	if err := s.PutGroup(ctx, store.Group{Name: "bad", Runner: "herdr", Limit: -1}, "jo"); err == nil ||
		!strings.Contains(err.Error(), "a limit is 0 or more") {
		t.Fatalf("PutGroup with limit -1 = %v", err)
	}
	if _, err := s.GetGroup(ctx, "bad"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused group was stored: %v", err)
	}
	// Replaced without a limit, a running group keeps the default 1: 0
	// pauses only through SetGroup, never by omission.
	must(s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "jo"))
	if g, err := s.GetGroup(ctx, "builders"); err != nil || g.Limit != 1 {
		t.Fatalf("replaced without a limit = %+v, %v; want limit 1", g, err)
	}

	b1, err := s.CreateBead(ctx, store.NewBead{Title: "one", Depot: "yard", Stage: "new"}, "jo")
	must(err)
	if _, err := s.Claim(ctx, b1.ID, "builders", "new", "yard"); err != nil {
		t.Fatalf("claim at limit 1: %v", err)
	}
	// Pause: 0 is stored, and the next claim is refused.
	zero := 0
	ch, err := s.SetGroup(ctx, "builders", store.GroupEdit{Limit: &zero}, "jo")
	must(err)
	if ch.Is.Limit != 0 {
		t.Fatalf("limit 0: %+v", ch.Is)
	}
	b2, err := s.CreateBead(ctx, store.NewBead{Title: "two", Depot: "yard", Stage: "new"}, "jo")
	must(err)
	if _, err := s.Claim(ctx, b2.ID, "builders", "new", "yard"); !errors.Is(err, store.ErrAtLimit) {
		t.Fatalf("claim on a paused group = %v, want ErrAtLimit", err)
	}
	// Put back whole, as a role switch writes it: the pause stays.
	g, err := s.GetGroup(ctx, "builders")
	must(err)
	must(s.PutGroup(ctx, *g, "jo"))
	if g, err := s.GetGroup(ctx, "builders"); err != nil || g.Limit != 0 {
		t.Fatalf("after PutGroup: %+v, %v; want the pause kept", g, err)
	}
	// The live session runs on: its bead advances.
	if _, err := s.Advance(ctx, b1.ID, "review", store.Expect{}, "jo"); err != nil {
		t.Fatalf("advance of the running bead: %v", err)
	}
}
