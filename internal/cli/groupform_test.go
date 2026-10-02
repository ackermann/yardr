package cli

import (
	"reflect"
	"testing"

	"github.com/mckean/yard/internal/store"
)

func TestGroupFormRoundTrip(t *testing.T) {
	g := store.Group{Name: "builders", Runner: "herdr", Limit: 2, Config: map[string]any{
		"kind": "claude", "prompt": "roles/builder.md", "keep": true,
		"args": []any{"--permission-mode", "auto", "--model", "opus", "--verbose"},
	}}
	a := answersFrom(g)
	if a.Model != "opus" || a.Kind != "claude" || a.Extra != "--verbose" ||
		!reflect.DeepEqual(a.PermArgs, []string{"--permission-mode", "auto"}) || a.Other["keep"] != true {
		t.Fatalf("answers = %+v", a)
	}
	back := a.group()
	want := store.Group{Name: "builders", Runner: "herdr", Limit: 2, Config: map[string]any{
		"kind": "claude", "model": "opus", "prompt": "roles/builder.md", "keep": true,
		"args": []any{"--permission-mode", "auto", "--verbose"},
	}}
	if !reflect.DeepEqual(back, want) {
		t.Fatalf("group = %+v\nwant   %+v", back, want)
	}
	// Changing runner to manual drops agent settings but keeps unknown keys.
	a.Runner = "manual"
	if m := a.group(); m.Config["kind"] != nil || m.Config["keep"] != true {
		t.Fatalf("manual config = %v", m.Config)
	}
}

func TestDescribe(t *testing.T) {
	g := store.Group{Runner: "herdr", Limit: 1, Config: map[string]any{"kind": "codex", "model": "gpt-x"}}
	if got := describe(g); got != "herdr · codex · gpt-x · limit 1" {
		t.Fatalf("describe = %q", got)
	}
}
