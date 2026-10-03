package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The record of an import is written when the import changed something or
// says something new, under the time and actor of that import; an import
// that changed nothing leaves it as it is and logs pack_unchanged. It goes
// with its rig.
func TestRecordPack(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	rec := store.PackImport{Pack: "starter", Rig: "web", Source: "/packs/starter", Commit: "abc", Flow: "default",
		Groups: map[string]string{"builders": "web-builders"}, Residents: map[string]string{"mayor": "boss"},
		Objects: []store.PackObject{
			{Kind: "group", Name: "web-builders"},
			{Kind: "route", Stage: "new", Group: "web-builders"},
			{Kind: "subscription", Resident: "boss", Subscription: &store.Subscription{Source: "linear", Scope: "involved"}},
		}}
	if _, err := st.RecordPack(ctx, rec, true, "ann"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a record for a rig the yard lacks: %v", err)
	}
	if err := st.PutRig(ctx, store.Rig{Name: "web"}, "me"); err != nil {
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
	if e := last(); e.Kind != "pack_applied" || e.Actor != "ann" || e.Data["pack"] != "starter" || e.Data["rig"] != "web" || e.Data["commit"] != "abc" {
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

	// No object written, but the record says something new (a resident
	// mapped anew): it is written.
	again.Residents = map[string]string{"mayor": "carol"}
	if wrote, err := st.RecordPack(ctx, again, false, "bob"); err != nil || !wrote {
		t.Fatalf("a record that differs: %v, %v", wrote, err)
	}
	if got, _ := st.Packs(ctx); got[0].By != "bob" || got[0].Commit != "def" || got[0].Residents["mayor"] != "carol" {
		t.Fatalf("records = %+v", got)
	}

	if _, err := st.DeleteRig(ctx, "web", "me"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Packs(ctx); len(got) != 0 {
		t.Fatalf("records after the rig was removed = %+v", got)
	}
}

// What identifies a pack's object stays the same when the pack changes
// where it leads: a route's group, a pointer's flow, a subscription's scope.
func TestPackObjectKey(t *testing.T) {
	same := [][2]store.PackObject{
		{{Kind: "route", Stage: "new", Group: "a"}, {Kind: "route", Stage: "new", Group: "b"}},
		{{Kind: "pointer", Type: "epic", Flow: "a"}, {Kind: "pointer", Type: "epic", Flow: "b"}},
		{{Kind: "subscription", Resident: "r", Subscription: &store.Subscription{Source: "linear", Scope: "all"}},
			{Kind: "subscription", Resident: "r", Subscription: &store.Subscription{Source: "linear", Scope: "involved"}}},
	}
	for _, p := range same {
		if p[0].Key() != p[1].Key() {
			t.Errorf("%+v and %+v have different keys", p[0], p[1])
		}
	}
	differ := [][2]store.PackObject{
		{{Kind: "route", Stage: "new"}, {Kind: "route", Stage: "new", Type: "epic"}},
		{{Kind: "group", Name: "a"}, {Kind: "flow", Name: "a"}},
		{{Kind: "subscription", Resident: "r", Subscription: &store.Subscription{Source: "linear", Scope: "all"}},
			{Kind: "subscription", Resident: "r", Subscription: &store.Subscription{Source: "linear", Scope: "all", Where: map[string]string{"team": "ENG"}}}},
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
	if _, err := st.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	plain := store.Subscription{Source: "linear", Scope: "all"}
	filtered := store.Subscription{Source: "linear", Scope: "all", Where: map[string]string{"team": "ENG"}}
	for _, s := range []store.Subscription{plain, filtered} {
		if err := st.AddSubscription(ctx, "mayor", s, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RemoveSubscription(ctx, "mayor", plain, "me"); err != nil {
		t.Fatal(err)
	}
	r, _ := st.GetResident(ctx, "mayor")
	if len(r.Subscriptions) != 1 || r.Subscriptions[0].String() != filtered.String() {
		t.Fatalf("subscriptions = %v, want the filtered one alone", r.Subscriptions)
	}
	if err := st.RemoveSubscription(ctx, "mayor", plain, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removing it again: %v", err)
	}
}
