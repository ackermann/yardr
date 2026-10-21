package store_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// A session whose agent waits, not stuck, snoozes: the wait and the count
// go on its handle, and a session_snoozed event says what it waits on. A
// snooze past 15m, one with nothing waited on, one for no session or a
// session that is no live bead session, and a fourth in a row are refused.
func TestSnoozeSession(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "builders", "new", "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRunning(ctx, sess.ID, paneShell(t, "yard-x-new")); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, os.Getpid())
	// An RPC caller outside a session's pane may name the session in both
	// YARDR_SESSION and the call arguments, but that does not make it the
	// session's agent.
	outsider := store.WithSession(store.WithCaller(ctx, os.Getppid()), sess.ID)
	if _, err := s.SnoozeSession(outsider, sess.ID, time.Minute, "test gate", "yard-x-new"); err == nil ||
		!strings.Contains(err.Error(), "inside a bead session") {
		t.Fatalf("SnoozeSession from outside the pane err = %v, want refused", err)
	}
	if _, err := s.SnoozeSession(pane, "another-session", time.Minute, "test gate", "yard-x-new"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("SnoozeSession for another session err = %v, want conflict", err)
	}

	before := time.Now()
	until, err := s.SnoozeSession(pane, sess.ID, 10*time.Minute, "test gate", "yard-x-new")
	if err != nil {
		t.Fatalf("SnoozeSession: %v", err)
	}
	if d := until.Sub(before.Add(10 * time.Minute)); d < -time.Minute || d > time.Minute {
		t.Errorf("snooze ends at %s, want about %s", until, before.Add(10*time.Minute))
	}
	got, err := s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantUntil := until.UTC().Format(time.RFC3339)
	if got.Handle[store.SnoozeUntil] != wantUntil {
		t.Errorf("handle %s = %v, want %s", store.SnoozeUntil, got.Handle[store.SnoozeUntil], wantUntil)
	}
	if n, ok := got.Handle[store.SnoozeCount].(float64); !ok || n != 1 {
		t.Errorf("handle %s = %v (%T), want 1", store.SnoozeCount, got.Handle[store.SnoozeCount], got.Handle[store.SnoozeCount])
	}
	evs, err := s.Events(ctx, b.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	var snoozed *store.Event
	for i := range evs {
		if evs[i].Kind == "session_snoozed" {
			snoozed = &evs[i]
		}
	}
	if snoozed == nil {
		t.Fatalf("no session_snoozed event among %d events", len(evs))
	}
	if snoozed.Data["session"] != sess.ID || snoozed.Data["duration"] != (10*time.Minute).String() || snoozed.Data["why"] != "test gate" {
		t.Errorf("session_snoozed data = %v, want the session, duration and why", snoozed.Data)
	}

	// Two more go through; the fourth in a row is refused, and the idle
	// limits apply to it as they always did: nothing on the handle moves.
	for i := 0; i < 2; i++ {
		if _, err := s.SnoozeSession(pane, sess.ID, time.Minute, "still waiting", "yard-x-new"); err != nil {
			t.Fatalf("snooze %d: %v", i+2, err)
		}
	}
	if _, err := s.SnoozeSession(pane, sess.ID, time.Minute, "still waiting", "yard-x-new"); err == nil ||
		!strings.Contains(err.Error(), "3 times in a row") {
		t.Fatalf("fourth snooze err = %v, want the run refused", err)
	}
	got, err = s.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := got.Handle[store.SnoozeCount].(float64); !ok || n != 3 {
		t.Errorf("handle %s = %v (%T) after the refused fourth, want 3", store.SnoozeCount, got.Handle[store.SnoozeCount], got.Handle[store.SnoozeCount])
	}

	// Past the longest, nothing waited on, no time at all.
	for _, tc := range []struct {
		name string
		dur  time.Duration
		why  string
		want string
	}{
		{"longer than 15m", 16 * time.Minute, "test gate", "15m"},
		{"empty why", time.Minute, "", "what the session waits on"},
		{"blank why", time.Minute, "  ", "what the session waits on"},
		{"no time", 0, "test gate", "at most"},
		{"negative", -time.Minute, "test gate", "at most"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.SnoozeSession(pane, sess.ID, tc.dur, tc.why, "yard-x-new"); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SnoozeSession(%s, %q) err = %v, want %q", tc.dur, tc.why, err, tc.want)
			}
		})
	}

	// Outside a session, and for sessions that are none.
	if _, err := s.SnoozeSession(ctx, "", time.Minute, "test gate", "me"); err == nil ||
		!strings.Contains(err.Error(), "inside a bead session") {
		t.Fatalf("SnoozeSession without a session err = %v, want refused", err)
	}
	if _, err := s.SnoozeSession(ctx, "no-such-session", time.Minute, "test gate", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("SnoozeSession for an unknown session err = %v, want not found", err)
	}
	if err := s.EndSession(ctx, sess.ID, "failed", "over"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SnoozeSession(ctx, sess.ID, time.Minute, "test gate", "me"); err == nil ||
		!strings.Contains(err.Error(), "only a live session snoozes") {
		t.Fatalf("SnoozeSession for an ended session err = %v, want refused", err)
	}
}
