package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mckean/yard/internal/store"
	"github.com/mckean/yard/internal/storetest"
)

func TestHookCRUD(t *testing.T) {
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

func TestRecordHookKeepsPayload(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.LogEvent(ctx, "", "noise", "me", nil); err != nil {
		t.Fatal(err)
	}
	payload := []byte("{\"type\":\"Comment\",\"bin\":\"\x00\xff\"}")
	seq, err := s.RecordHook(ctx, "linear", map[string]any{"source": "linear", "type": "comment"}, payload)
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
	if _, err := s.RecordHook(ctx, "linear", nil, make([]byte, store.MaxHookPayload+1)); err == nil {
		t.Fatal("oversize payload stored")
	}
}
