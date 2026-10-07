package store_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// inboxKinds are the kinds of the events in the crew member's inbox, oldest
// first.
func inboxKinds(t *testing.T, s store.API, name string) string {
	t.Helper()
	page, err := s.Inbox(context.Background(), name, 0)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range page.Events {
		kinds = append(kinds, e.Kind)
	}
	return strings.Join(kinds, ",")
}

// A crew member that hears the class by its name hears a kind that joins the
// class after the member was made. One with a copy of the list as it was
// does not: that was every crew member until the class (yardr-qcm2).
func TestAKindThatJoinsTheClassReachesTheCrewMadeBefore(t *testing.T) {
	// raw logs a kind as an earlier release did, which no call of the API
	// takes.
	s, raw := storetest.NewOwned(t)
	ctx := context.Background()
	for name, on := range map[string][]string{
		"byname": {store.KindAttention},
		"beside": {"session_died", store.KindAttention},
		"copied": slices.Clone(store.AttentionKinds),
	} {
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr", On: on}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	log := func(bead, kind string) {
		t.Helper()
		if err := raw.LogEvent(ctx, bead, kind, "yard", nil); err != nil {
			t.Fatal(err)
		}
	}
	// One of the class about a bead, one about none, one under the word an
	// earlier release logged it with, and two kinds that are not of it.
	log(b.ID, "held")
	log("", "runtime_down")
	log("", "resident_gave_up")
	log(b.ID, "session_died")
	log(b.ID, "session_unblocked")
	for name, want := range map[string]string{
		"byname": "held,runtime_down,resident_gave_up",
		"beside": "held,runtime_down,resident_gave_up,session_died",
		"copied": "held,runtime_down,resident_gave_up",
	} {
		if got := inboxKinds(t, s, name); got != want {
			t.Errorf("%s hears %q, want %q", name, got, want)
		}
		page, err := s.Inbox(ctx, name, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AckInbox(ctx, name, page.Last); err != nil {
			t.Fatal(err)
		}
	}

	was := store.AttentionKinds
	t.Cleanup(func() { store.AttentionKinds = was })
	store.AttentionKinds = append(slices.Clone(was), "session_unblocked")
	log(b.ID, "session_unblocked")
	// The one from before it joined is heard too: it was logged after what
	// the crew member last read, and --on is matched when the inbox is.
	for name, want := range map[string]string{"byname": "session_unblocked,session_unblocked", "beside": "session_unblocked,session_unblocked", "copied": ""} {
		if got := inboxKinds(t, s, name); got != want {
			t.Errorf("after the kind joined the class %s hears %q, want %q", name, got, want)
		}
	}
}

// kind=<the class> in a filter is every kind of the class, so with a depot
// beside it one subscription is what needs someone in that depot. A group
// has no --on: it takes the class the same way, for its members that are
// crew members (yardr-qcm2).
func TestTheClassInAFilterIsOneDepotsAttention(t *testing.T) {
	s, ctx, b := yardrDepot(t, "clerk", "both", "member")
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	web, err := s.CreateBead(ctx, store.NewBead{Title: "web", Depot: "web"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "watch", Runner: "manual", Members: []string{"member"}}); err != nil {
		t.Fatal(err)
	}
	subscribe(t, s, "clerk", yardrSub(store.ScopeAll, "depot=yard", "kind="+store.KindAttention))
	subscribe(t, s, "watch", yardrSub(store.ScopeAll, "depot=web", "kind="+store.KindAttention))
	// The class and a kind beside it, as --where given twice stores them.
	subscribe(t, s, "both", yardrSub(store.ScopeAll, "depot=yard", "kind="+store.JoinWhere([]string{store.KindAttention, "session_unblocked"})))
	r, err := s.GetCrew(ctx, "clerk")
	if err != nil || len(r.Subscriptions) != 1 || r.Subscriptions[0].String() != "yardr all depot=yard kind=attention" {
		t.Fatalf("clerk's subscriptions = %v, %v; want the class under its name", r.Subscriptions, err)
	}
	for _, id := range []string{b.ID, web.ID} {
		for _, kind := range []string{"session_blocked", "session_unblocked", "session_prompt_lost"} {
			if err := s.LogDispatch(ctx, id, kind, "yard", map[string]any{"stage": "new"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	wantHeard(t, s, b.ID, "clerk", "session_blocked:all")
	wantHeard(t, s, web.ID, "clerk")
	wantHeard(t, s, b.ID, "both", "session_blocked:all", "session_unblocked:all")
	wantHeard(t, s, web.ID, "both")
	wantHeard(t, s, b.ID, "member")
	wantHeard(t, s, web.ID, "member", "session_blocked:all via watch")

	// The filter too goes by the class as it is when the event is logged.
	was := store.AttentionKinds
	t.Cleanup(func() { store.AttentionKinds = was })
	store.AttentionKinds = append(slices.Clone(was), "session_prompt_lost")
	if err := s.LogDispatch(ctx, b.ID, "session_prompt_lost", "yard", nil); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, b.ID, "clerk", "session_blocked:all", "session_prompt_lost:all")
}
