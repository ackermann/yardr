package store_test

import (
	"context"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A claim records the harness kind and model the session starts with, on the
// session and in the claimed event: the group's own, the yard's default_kind
// for a herdr group whose config names none, and neither for a manual group.
func TestClaimRecordsKindAndModel(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetSetting(ctx, "default_kind", "codex", "me"))
	must(s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Config: map[string]any{"kind": "kimi", "model": "k3"}}, "me"))
	must(s.PutGroup(ctx, store.Group{Name: "defaulters", Runner: "herdr"}, "me"))
	must(s.PutGroup(ctx, store.Group{Name: "people", Runner: "manual"}, "me"))

	claim := func(group string) (*store.Session, store.Event) {
		t.Helper()
		b, err := s.CreateBead(ctx, store.NewBead{Title: "work of " + group}, "me")
		must(err)
		sess, err := s.Claim(ctx, b.ID, group, "new", "yard")
		must(err)
		evs, err := s.Events(ctx, b.ID, 10)
		must(err)
		return sess, evs[len(evs)-1]
	}

	sess, ev := claim("builders")
	if sess.Kind != "kimi" || sess.Model != "k3" {
		t.Fatalf("builders' session = %+v", sess)
	}
	if ev.Kind != "claimed" || ev.Data["kind"] != "kimi" || ev.Data["model"] != "k3" {
		t.Fatalf("builders' claimed event = %+v", ev)
	}

	sess, ev = claim("defaulters")
	if sess.Kind != "codex" || sess.Model != "" {
		t.Fatalf("defaulters' session = %+v", sess)
	}
	if ev.Data["kind"] != "codex" || ev.Data["model"] != nil {
		t.Fatalf("defaulters' claimed event = %+v", ev)
	}

	sess, ev = claim("people")
	if sess.Kind != "" || sess.Model != "" {
		t.Fatalf("people's session = %+v", sess)
	}
	if _, ok := ev.Data["kind"]; ok {
		t.Fatalf("people's claimed event has kind: %+v", ev.Data)
	}
	if _, ok := ev.Data["model"]; ok {
		t.Fatalf("people's claimed event has model: %+v", ev.Data)
	}
}
