package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func TestSettings(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if got, err := st.Settings(ctx); err != nil || len(got) != 0 {
		t.Fatalf("fresh settings = %v, %v", got, err)
	}
	if err := st.SetSetting(ctx, "default_kind", "codex", "me"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Settings(ctx); got["default_kind"] != "codex" || len(got) != 1 {
		t.Fatalf("settings = %v", got)
	}
	evs, _ := st.Events(ctx, "", 1)
	if len(evs) != 1 || evs[0].Kind != "setting_changed" || evs[0].Data["value"] != "codex" {
		t.Fatalf("event = %+v", evs)
	}
	// Empty removes it.
	if err := st.SetSetting(ctx, "default_kind", "", "me"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Settings(ctx); len(got) != 0 {
		t.Fatalf("after unset = %v", got)
	}
	if err := st.SetSetting(ctx, "Bad Key", "x", "me"); err == nil {
		t.Fatal("bad key accepted")
	}
}

// A yard from before the onboarding (depots, groups or crew members, no
// default_kind) gets default_kind when it is opened, from its first herdr
// group's kind, else its first herdr crew member's, else claude, once, with
// settings_migrated; the operator stays unset. An empty store and one that
// has the setting are left alone (yard-w6np).
func TestMigrateSettings(t *testing.T) {
	ctx := context.Background()
	must := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	reopen := func(t *testing.T, fill func(*store.Store)) (*store.Store, []store.Event) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "yard.db")
		s, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		fill(s)
		head, _ := s.Head(ctx)
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		for i := range 2 { // the second open changes nothing
			if s, err = store.Open(path); err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				must(t, s.Close(ctx))
			}
		}
		t.Cleanup(func() { s.Close(ctx) })
		all, err := s.Events(ctx, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		var evs []store.Event
		for _, e := range all {
			if e.Seq > head {
				evs = append(evs, e)
			}
		}
		return s, evs
	}
	herdr := func(kind string) map[string]any { return map[string]any{"kind": kind} }
	for _, tc := range []struct {
		name string
		fill func(*store.Store)
		want string // "" is no migration
	}{
		{"empty", func(*store.Store) {}, ""},
		{"set", func(s *store.Store) {
			must(t, s.PutDepot(ctx, store.Depot{Name: "r"}, "me"))
			must(t, s.SetSetting(ctx, "default_kind", "gemini", "me"))
		}, ""},
		{"depot only", func(s *store.Store) { must(t, s.PutDepot(ctx, store.Depot{Name: "r"}, "me")) }, "claude"},
		{"herdr group", func(s *store.Store) {
			must(t, s.PutGroup(ctx, store.Group{Name: "a", Runner: "other", Config: herdr("pi")}, "me"))
			must(t, s.PutGroup(ctx, store.Group{Name: "b", Runner: "herdr", Config: herdr("codex")}, "me"))
			_, err := s.PutCrew(ctx, store.CrewMember{Name: "m", Runner: "herdr", Config: herdr("gemini")}, "me")
			must(t, err)
		}, "codex"},
		{"herdr crew member", func(s *store.Store) {
			_, err := s.PutCrew(ctx, store.CrewMember{Name: "m", Runner: "herdr", Config: herdr("gemini")}, "me")
			must(t, err)
		}, "gemini"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, evs := reopen(t, tc.fill)
			got, err := s.Settings(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := got["operator"]; ok {
				t.Fatalf("operator set: %v", got)
			}
			if tc.want == "" {
				if len(evs) != 0 {
					t.Fatalf("events on open: %+v", evs)
				}
				return
			}
			if got["default_kind"] != tc.want {
				t.Fatalf("default_kind = %q, want %q", got["default_kind"], tc.want)
			}
			if len(evs) != 1 || evs[0].Kind != "settings_migrated" || evs[0].Data["default_kind"] != tc.want {
				t.Fatalf("events on open = %+v", evs)
			}
		})
	}
}
