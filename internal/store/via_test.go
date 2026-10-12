package store_test

import (
	"context"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A call that came through a device marks what it writes as the device's
// user, and nothing else: a write the same call makes as another actor
// carries no mark, nor does any write without the device.
func TestViaMarksTheUsersOwnWrites(t *testing.T) {
	// The mark is the listener's to set, in the process that owns the store.
	_, st := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "Paint the shed"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	head, _ := st.Head(ctx)
	away := store.WithVia(ctx, "jo", "phone")
	for _, write := range []func() error{
		func() error { return st.Mail(away, "yardmaster", "through the phone", "jo") },
		func() error { return st.AddNote(away, b.ID, "through the phone", "jo") },
		// Not the device's user: whatever let the call in, it is not theirs.
		func() error { return st.Mail(away, "jo", "as the yardmaster", "yardmaster") },
		func() error { return st.AddNote(away, b.ID, "as the yardmaster", "yardmaster") },
		func() error { return st.Mail(ctx, "yardmaster", "at the terminal", "jo") },
	} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := st.EventsSince(ctx, head, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"phone", "phone", "", "", ""}
	if len(evs) != len(want) {
		t.Fatalf("%d events, want %d", len(evs), len(want))
	}
	for i, e := range evs {
		if got := store.Via(e); got != want[i] {
			t.Errorf("event %d (%s by %s) is via %q, want %q", i, e.Kind, e.Actor, got, want[i])
		}
	}
	notes, err := st.Notes(ctx, b.ID)
	if err != nil || len(notes) != 2 || notes[0].Via != "phone" || notes[1].Via != "" {
		t.Errorf("the notes are %+v (%v), want the first via phone", notes, err)
	}
	if got := store.ViaMark("jo", "phone") + ", " + store.ViaMark("jo", ""); got != "jo (via phone), jo" {
		t.Errorf("the marks read %q", got)
	}
}
