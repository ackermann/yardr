package store_test

import (
	"context"
	"testing"

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
