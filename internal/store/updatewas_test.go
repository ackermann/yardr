package store_test

import (
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// An update's event says what each field it set was before it: the log is
// the only place an old title or body is kept (yardr-pekm).
func TestUpdateKeepsWhatWas(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t)
	last := func() store.Event {
		t.Helper()
		evs, err := s.Events(ctx, b.ID, 1)
		if err != nil || len(evs) != 1 || evs[0].Kind != "updated" {
			t.Fatalf("the bead's last event: %+v, %v", evs, err)
		}
		return evs[0]
	}
	title, body, pri := "the work", "First.\nSecond.\n", 1
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &title, Body: &body, Priority: &pri, AddLabels: []string{"api"},
		RemoveLabel: []string{"ui"}, Metadata: map[string]any{"k": "v1"}}, store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	e := last()
	if want := map[string]any{"title": "work", "body": "", "priority": float64(b.Priority), "labels": []any{"ui"},
		"metadata": map[string]any{"k": nil}}; !reflect.DeepEqual(e.Data["was"], want) {
		t.Errorf("was = %#v, want %#v", e.Data["was"], want)
	}
	// What the event said before it kept the old values, it still says.
	if e.Data["title"] != "the work" || e.Data["body"] != true || e.Data["priority"] != float64(1) {
		t.Errorf("the new values: %#v", e.Data)
	}

	// Only what the patch names: the body of the first update is what the
	// second one's was holds, and a metadata key its old value.
	body2 := "First.\nThird.\n"
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Body: &body2, Metadata: map[string]any{"k": "v2"}}, store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"body": body, "metadata": map[string]any{"k": "v1"}}; !reflect.DeepEqual(last().Data["was"], want) {
		t.Errorf("was = %#v, want %#v", last().Data["was"], want)
	}
}

// What a body was is nothing said now: a name in the body an update
// replaces mentions nobody again, and a subscriber's line about the update
// names the fields that changed without the old text. The operator's line
// names them as the event does, in was, and any other person's by the new
// values the event tells (store.Event.WithoutHeld holds was, which is the old
// text).
func TestOldBodyIsNotSaidAgain(t *testing.T) {
	t.Parallel()
	s, ctx, _ := yardrDepot(t)
	user(t, s, "ada", yardrSub(store.ScopeMentions))
	user(t, s, "bob", yardrSub(store.ScopeAll, "kind=updated"))
	user(t, s, "op", yardrSub(store.ScopeAll, "kind=updated"))
	if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "door", Body: "ask @ada about the hinge", Depot: "yard", Stage: "new"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	if got := texts(notifications(t, s, "ada", 0)); len(got) != 1 {
		t.Fatalf("the bead that mentions ada: %v", got)
	}
	body := "hung"
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Body: &body}, store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	if got := texts(notifications(t, s, "ada", 0)); len(got) != 1 {
		t.Errorf("the old body mentioned ada again: %v", got)
	}
	got := texts(notifications(t, s, "op", 0))
	if len(got) != 1 || !strings.Contains(got[0], "body=true was=[body]") || strings.Contains(got[0], "hinge") {
		t.Errorf("the operator's line about the update: %v", got)
	}
	got = texts(notifications(t, s, "bob", 0))
	if len(got) != 1 || !strings.Contains(got[0], "body=true") || strings.Contains(got[0], "was=") || strings.Contains(got[0], "hinge") {
		t.Errorf("bob's line about the update: %v", got)
	}
}

func TestBrief(t *testing.T) {
	t.Parallel()
	data := map[string]any{"title": "b", "was": map[string]any{"title": "a", "body": "long"}}
	if got, want := store.Brief(data), (map[string]any{"title": "b", "was": []any{"body", "title"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("Brief = %#v, want %#v", got, want)
	}
	if _, kept := data["was"].(map[string]any); !kept {
		t.Error("Brief changed the event's own data")
	}
	// Another event's was is its own word.
	status := map[string]any{"status": "idle", "was": "blocked"}
	if got := store.Brief(status); !reflect.DeepEqual(got, status) {
		t.Errorf("Brief = %#v", got)
	}
}
