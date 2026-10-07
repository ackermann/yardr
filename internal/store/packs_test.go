package store_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The record of an import is written when the import changed something or
// says something new, under the time and actor of that import; an import
// that changed nothing leaves it as it is and logs pack_unchanged. It goes
// with its depot.
func TestRecordPack(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	rec := store.PackImport{Pack: "starter", Depot: "web", Source: "/packs/starter", Commit: "abc", Flow: "default",
		Groups: map[string]string{"builders": "web-builders"}, Crew: map[string]string{"yardmaster": "boss"},
		Objects: []store.PackObject{
			{Kind: "group", Name: "web-builders"},
			{Kind: "route", Stage: "new", Group: "web-builders"},
			{Kind: "subscription", CrewMember: "boss", Subscription: &store.Subscription{Source: "linear", Scope: "involved"}},
		}}
	if _, err := st.RecordPack(ctx, rec, true, "ann"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a record for a depot the yard lacks: %v", err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	last := func() store.Event {
		t.Helper()
		evs, err := st.Events(ctx, "", 1)
		if err != nil || len(evs) != 1 {
			t.Fatalf("events = %v, %v", evs, err)
		}
		return evs[0]
	}

	if wrote, err := st.RecordPack(ctx, rec, true, "ann"); err != nil || !wrote {
		t.Fatalf("first record: %v, %v", wrote, err)
	}
	if e := last(); e.Kind != "pack_applied" || e.Actor != "ann" || e.Data["pack"] != "starter" || e.Data["depot"] != "web" || e.Data["commit"] != "abc" {
		t.Fatalf("event = %+v", e)
	}
	recs, err := st.Packs(ctx)
	if err != nil || len(recs) != 1 || recs[0].By != "ann" || recs[0].At.IsZero() || !reflect.DeepEqual(recs[0].Objects, rec.Objects) {
		t.Fatalf("records = %+v, %v", recs, err)
	}

	// Nothing changed, and from another commit of the same content: the
	// record is the last import that did something.
	again := rec
	again.Commit = "def"
	if wrote, err := st.RecordPack(ctx, again, false, "bob"); err != nil || wrote {
		t.Fatalf("unchanged record: %v, %v", wrote, err)
	}
	if e := last(); e.Kind != "pack_unchanged" || e.Actor != "bob" || e.Data["commit"] != "def" {
		t.Fatalf("event = %+v", e)
	}
	if got, _ := st.Packs(ctx); !reflect.DeepEqual(got, recs) {
		t.Fatalf("the record changed: %+v", got)
	}

	// No object written, but the record says something new (a crew member
	// mapped anew): it is written.
	again.Crew = map[string]string{"yardmaster": "carol"}
	if wrote, err := st.RecordPack(ctx, again, false, "bob"); err != nil || !wrote {
		t.Fatalf("a record that differs: %v, %v", wrote, err)
	}
	if got, _ := st.Packs(ctx); got[0].By != "bob" || got[0].Commit != "def" || got[0].Crew["yardmaster"] != "carol" {
		t.Fatalf("records = %+v", got)
	}

	if _, err := st.DeleteDepot(ctx, "web", "me"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Packs(ctx); len(got) != 0 {
		t.Fatalf("records after the depot was removed = %+v", got)
	}
}

// What identifies a pack's object stays the same when the pack changes
// where it leads: a route's group, a pointer's flow, a subscription's scope.
func TestPackObjectKey(t *testing.T) {
	same := [][2]store.PackObject{
		{{Kind: "route", Stage: "new", Group: "a"}, {Kind: "route", Stage: "new", Group: "b"}},
		{{Kind: "pointer", Type: "train", Flow: "a"}, {Kind: "pointer", Type: "train", Flow: "b"}},
		{{Kind: "subscription", CrewMember: "r", Subscription: &store.Subscription{Source: "linear", Scope: "all"}},
			{Kind: "subscription", CrewMember: "r", Subscription: &store.Subscription{Source: "linear", Scope: "involved"}}},
	}
	for _, p := range same {
		if p[0].Key() != p[1].Key() {
			t.Errorf("%+v and %+v have different keys", p[0], p[1])
		}
	}
	differ := [][2]store.PackObject{
		{{Kind: "route", Stage: "new"}, {Kind: "route", Stage: "new", Type: "train"}},
		{{Kind: "group", Name: "a"}, {Kind: "flow", Name: "a"}},
		{{Kind: "subscription", CrewMember: "r", Subscription: &store.Subscription{Source: "linear", Scope: "all"}},
			{Kind: "subscription", CrewMember: "r", Subscription: &store.Subscription{Source: "linear", Scope: "all", Where: map[string]string{"team": "ENG"}}}},
	}
	for _, p := range differ {
		if p[0].Key() == p[1].Key() {
			t.Errorf("%+v and %+v share a key", p[0], p[1])
		}
	}
}

// RemoveSubscription drops the one subscription named, not the filtered
// ones of the same source and scope that RemoveSubscriptions without
// filters takes with it.
func TestRemoveSubscriptionIsExact(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	plain := store.Subscription{Source: "linear", Scope: "all"}
	filtered := store.Subscription{Source: "linear", Scope: "all", Where: map[string]string{"team": "ENG"}}
	for _, s := range []store.Subscription{plain, filtered} {
		if err := st.AddSubscription(ctx, "yardmaster", s, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RemoveSubscription(ctx, "yardmaster", plain, "me"); err != nil {
		t.Fatal(err)
	}
	r, _ := st.GetCrew(ctx, "yardmaster")
	if len(r.Subscriptions) != 1 || r.Subscriptions[0].String() != filtered.String() {
		t.Fatalf("subscriptions = %v, want the filtered one alone", r.Subscriptions)
	}
	if err := st.RemoveSubscription(ctx, "yardmaster", plain, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removing it again: %v", err)
	}
}

// The record keeps the answers to what a pack asked its crew may open
// threads in. The same answers again are the same record; a new one is a
// new record though the import wrote nothing else.
func TestRecordPackKeepsOpenThreadsAnswers(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	rec := store.PackImport{Pack: "starter", Depot: "web", Crew: map[string]string{"yardmaster": "boss"},
		OpenThreads: map[string]map[string]bool{"boss": {"linear": true}, "scout": {}}}
	if wrote, err := st.RecordPack(ctx, rec, true, "jo"); err != nil || !wrote {
		t.Fatalf("first: %v, %v", wrote, err)
	}
	recs, err := st.Packs(ctx)
	if err != nil || len(recs) != 1 || !reflect.DeepEqual(recs[0].OpenThreads, map[string]map[string]bool{"boss": {"linear": true}}) {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	if wrote, err := st.RecordPack(ctx, rec, false, "jo"); err != nil || wrote {
		t.Fatalf("the same answers again: wrote %v, %v", wrote, err)
	}
	rec.OpenThreads = map[string]map[string]bool{"boss": {"linear": true, "github": false}}
	if wrote, err := st.RecordPack(ctx, rec, false, "jo"); err != nil || !wrote {
		t.Fatalf("a further answer: wrote %v, %v", wrote, err)
	}
	// A record from before the field, and one without answers, are equal.
	rec.OpenThreads = nil
	if _, err := st.RecordPack(ctx, rec, true, "jo"); err != nil {
		t.Fatal(err)
	}
	rec.OpenThreads = map[string]map[string]bool{}
	if wrote, err := st.RecordPack(ctx, rec, false, "jo"); err != nil || wrote {
		t.Fatalf("no answers, as an empty map: wrote %v, %v", wrote, err)
	}
}

// The record keeps the answers to which calls of a declared source a pack
// asked for, as it keeps those about opening threads: the same answers
// again are the same record, a further one is a new record, and no answers
// are as a record from before the field.
func TestRecordPackKeepsCallAnswers(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "web"}, "me"); err != nil {
		t.Fatal(err)
	}
	rec := store.PackImport{Pack: "desk", Depot: "web", Groups: map[string]string{"builders": "web-builders"},
		Calls: map[string]map[string]bool{"group web-builders": {"crm:read": true}, "crew member boss": {}}}
	if wrote, err := st.RecordPack(ctx, rec, true, "jo"); err != nil || !wrote {
		t.Fatalf("first: %v, %v", wrote, err)
	}
	recs, err := st.Packs(ctx)
	if err != nil || len(recs) != 1 || !reflect.DeepEqual(recs[0].Calls, map[string]map[string]bool{"group web-builders": {"crm:read": true}}) {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	if wrote, err := st.RecordPack(ctx, rec, false, "jo"); err != nil || wrote {
		t.Fatalf("the same answers again: wrote %v, %v", wrote, err)
	}
	rec.Calls = map[string]map[string]bool{"group web-builders": {"crm:read": true, "crm:note": false}}
	if wrote, err := st.RecordPack(ctx, rec, false, "jo"); err != nil || !wrote {
		t.Fatalf("a further answer: wrote %v, %v", wrote, err)
	}
	rec.Calls = nil
	if _, err := st.RecordPack(ctx, rec, true, "jo"); err != nil {
		t.Fatal(err)
	}
	rec.Calls = map[string]map[string]bool{}
	if wrote, err := st.RecordPack(ctx, rec, false, "jo"); err != nil || wrote {
		t.Fatalf("no answers, as an empty map: wrote %v, %v", wrote, err)
	}
}

// EventsOfKind reads the last events of one kind, oldest first, and no
// event of a kind whose name only starts the same or follows it.
func TestEventsOfKind(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	const kind = "session_prompt_typed"
	if evs, err := st.EventsOfKind(ctx, kind, 10); err != nil || len(evs) != 0 {
		t.Fatalf("in a store without one: %+v, %v", evs, err)
	}
	for i, k := range []string{kind, "session_prompted", kind, "session_prompt_lost", "workspace_removed", kind} {
		if err := st.LogDispatch(ctx, "", k, "me", map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	ns := func(evs []store.Event) (out []int) {
		for _, e := range evs {
			if e.Kind != kind {
				t.Fatalf("an event of kind %s", e.Kind)
			}
			out = append(out, int(e.Data["n"].(float64)))
		}
		return out
	}
	evs, err := st.EventsOfKind(ctx, kind, 10)
	if err != nil || !slices.Equal(ns(evs), []int{0, 2, 5}) {
		t.Fatalf("all = %v, %v", ns(evs), err)
	}
	if evs, err = st.EventsOfKind(ctx, kind, 2); err != nil || !slices.Equal(ns(evs), []int{2, 5}) {
		t.Fatalf("the last two = %v, %v", ns(evs), err)
	}
	// The kind that sorts last in the index: nothing is after it to seek to.
	if evs, err = st.EventsOfKind(ctx, "workspace_removed", 5); err != nil || len(evs) != 1 || evs[0].Data["n"].(float64) != 4 {
		t.Fatalf("the last kind = %+v, %v", evs, err)
	}
}
