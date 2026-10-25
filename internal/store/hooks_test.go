package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func TestHookCRUD(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	for _, bad := range []store.Hook{
		{Name: "Linear", Source: "linear", SecretFile: "/s"},           // uppercase
		{Name: "a/b", Source: "linear", SecretFile: "/s"},              // path separator
		{Name: "", Source: "linear", SecretFile: "/s"},                 // empty
		{Name: "linear", Source: "", SecretFile: "/s"},                 // no source
		{Name: "linear", Source: "linear", SecretFile: ""},             // no secret file
		{Name: strings.Repeat("a", 65), Source: "t", SecretFile: "/s"}, // too long
	} {
		if _, err := s.PutHook(ctx, bad, "me"); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	h, err := s.PutHook(ctx, store.Hook{Name: "linear", Source: "linear", SecretFile: "/etc/yard/linear"}, "me")
	if err != nil || h.Name != "linear" {
		t.Fatalf("put: %+v %v", h, err)
	}
	if _, err := s.PutHook(ctx, store.Hook{Name: "linear", Source: "linear", SecretFile: "/etc/yard/linear2"}, "me"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetHook(ctx, "linear")
	if err != nil || got.SecretFile != "/etc/yard/linear2" {
		t.Fatalf("replace: %+v %v", got, err)
	}
	if hs, _ := s.Hooks(ctx); len(hs) != 1 {
		t.Fatalf("hooks: %+v", hs)
	}
	if err := s.DeleteHook(ctx, "linear", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetHook(ctx, "linear"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.DeleteHook(ctx, "linear", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	evs, _ := s.Events(ctx, "", 10)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "hook_added,hook_added,hook_removed" {
		t.Fatalf("events: %v", kinds)
	}
}

// A hook for a source hearing by identity stores unknown without
// --unknown and the word given with it, and each is refused for a
// source with no identity hearing, telegram's included, and a word
// outside the three everywhere. This is the write a terminal hook add
// runs, so it holds what the card's approval stores through runHookAdd.
func TestHookUnknownWord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := storetest.New(t)
	declarePack(t, s, "slack")
	declarePack(t, s, "telegram")
	// Without the flag the hook stores unknown.
	h, err := s.PutHook(ctx, store.Hook{Name: "slackline", Source: "slack", SecretFile: "/s"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if h.Unknown != store.HookUnknownUnknown {
		t.Fatalf("hook without --unknown stored %q, want %q", h.Unknown, store.HookUnknownUnknown)
	}
	if h.UnknownPolicy() != store.HookUnknownUnknown || (store.Hook{}).UnknownPolicy() != store.HookUnknownUnknown {
		t.Fatal("an empty unknown does not read as unknown")
	}
	// With each word it stores that word.
	for _, word := range store.HookUnknowns {
		name := "slack-" + word
		h, err := s.PutHook(ctx, store.Hook{Name: name, Source: "slack", SecretFile: "/s", Unknown: word}, "me")
		if err != nil {
			t.Fatalf("--unknown %s: %v", word, err)
		}
		if h.Unknown != word {
			t.Fatalf("--unknown %s stored %q", word, h.Unknown)
		}
	}
	// A word outside the three, for slack and telegram alike.
	for _, src := range []string{"slack", "telegram"} {
		if _, err := s.PutHook(ctx, store.Hook{Name: "bad-" + src, Source: src, SecretFile: "/s", Unknown: "shrug"}, "me"); err == nil ||
			!strings.Contains(err.Error(), `--unknown "shrug": want unknown, allow, ignore`) {
			t.Errorf("--unknown shrug for %s = %v", src, err)
		}
	}
	// Each word for a source with no identity hearing, telegram's
	// included, is refused, and a telegram hook stores no word.
	for _, word := range store.HookUnknowns {
		if _, err := s.PutHook(ctx, store.Hook{Name: "tg-" + word, Source: "telegram", SecretFile: "/s", Unknown: word}, "me"); err == nil ||
			!strings.Contains(err.Error(), "for a source with no identity hearing") {
			t.Errorf("--unknown %s for telegram = %v", word, err)
		}
	}
	h, err = s.PutHook(ctx, store.Hook{Name: "tg", Source: "telegram", SecretFile: "/s"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if h.Unknown != "" {
		t.Fatalf("telegram hook stored %q, want no word", h.Unknown)
	}
	// A hook alone hearing by identity takes --unknown too: its senders
	// are unknown the same way, though the source is never called.
	mail := "name: mail\nhook:\n" +
		"  verify:\n    scheme: secret\n    signature: { header: M-Secret }\n" +
		"  bodies: unique\n  normalize: [normalize]\n  keys: [sender, to, subject]\n" +
		"hear:\n  field: sender\n  by: identity\n"
	if _, err := s.PutSource(ctx, "mail.yaml", mail, "me"); err != nil {
		t.Fatalf("hook-only source hearing by identity: %v", err)
	}
	h, err = s.PutHook(ctx, store.Hook{Name: "mailbox", Source: "mail", SecretFile: "/s"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if h.Unknown != store.HookUnknownUnknown {
		t.Fatalf("hook-only hook without --unknown stored %q, want %q", h.Unknown, store.HookUnknownUnknown)
	}
	h, err = s.PutHook(ctx, store.Hook{Name: "mailbox", Source: "mail", SecretFile: "/s", Unknown: store.HookUnknownUnknown}, "me")
	if err != nil {
		t.Fatalf("hook-only hook with --unknown unknown: %v", err)
	}
	if h.Unknown != store.HookUnknownUnknown {
		t.Fatalf("hook-only hook with --unknown unknown stored %q", h.Unknown)
	}
}

func TestRecordHookKeepsPayload(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.LogDispatch(ctx, "", "daemon_replaced", "me", nil); err != nil {
		t.Fatal(err)
	}
	payload := []byte("{\"type\":\"Comment\",\"bin\":\"\x00\xff\"}")
	seq, _, err := own.RecordHook(ctx, "linear", map[string]any{"source": "linear", "type": "comment"}, payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := s.Events(ctx, "", 10)
	last := evs[len(evs)-1]
	if last.Seq != seq || last.Kind != "hook" || last.Actor != "hook:linear" || last.Data["hook"] != "linear" ||
		last.Data["type"] != "comment" {
		t.Fatalf("event %+v, seq %d", last, seq)
	}
	got, err := s.HookPayload(ctx, seq)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("payload %q %v", got, err)
	}
	if _, err := s.HookPayload(ctx, seq-1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("payload of a non-hook event: %v", err)
	}
	if _, _, err := own.RecordHook(ctx, "linear", nil, make([]byte, store.MaxHookPayload+1), nil); err == nil {
		t.Fatal("oversize payload stored")
	}
}
