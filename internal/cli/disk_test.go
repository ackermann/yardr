package cli

import (
	"context"
	"encoding/json"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// With disk keeping off, yardr disk keep does nothing and says nothing,
// in text and in JSON alike. It consults neither go nor the disk then, so
// this holds on any machine.
func TestDiskKeepOff(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.DiskKeepGBSetting, "0", "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.diskCmd(), "keep")
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("keep printed %q, want nothing", out)
	}
	a.json = true
	out, err = run(t, a.diskCmd(), "keep")
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Acted bool `json:"acted"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil || rep.Acted {
		t.Fatalf("keep --json = %s, %v", out, err)
	}
	if evs, err := st.EventsOfKind(ctx, store.DiskKept, 10); err != nil || len(evs) != 0 {
		t.Fatalf("events = %v, %v", evs, err)
	}
}
