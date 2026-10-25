package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// screenHerdr is a PaneReader that answers every pane with screen, keeping
// the pane it was asked for.
type screenHerdr struct {
	screen string
	pane   string
}

func (f *screenHerdr) PaneScreen(_ context.Context, pane string) (string, error) {
	f.pane = pane
	return f.screen, nil
}

// SessionScreen answers the pane's screen through the store's reader: by
// session id, by the herdr agent name of a live session, and by crew
// member's name for the member's own session. What none names is not
// found, and a finished session's agent name names nothing; a session with
// no pane recorded is refused naming it; and a store no server gave a
// reader is refused naming the server. Two live sessions with one agent
// name are refused naming both.
func TestSessionScreen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	api, st := storetest.NewOwned(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(api.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 4}, "me"))
	b, err := api.CreateBead(ctx, store.NewBead{Title: "Paint the shed"}, "me")
	must(err)
	sess, err := api.Claim(ctx, b.ID, "builders", "new", "me")
	must(err)
	must(api.MarkRunning(ctx, sess.ID, map[string]any{"pane": "w1:p1"}))
	herd, err := api.CreateBead(ctx, store.NewBead{Title: "Mend the fence"}, "me")
	must(err)
	herdSess, err := api.Claim(ctx, herd.ID, "builders", "new", "me")
	must(err)
	must(api.MarkRunning(ctx, herdSess.ID, map[string]any{"agent": "mend-new", "pane": "w1:p3"}))
	gone, err := api.CreateBead(ctx, store.NewBead{Title: "Sweep the yard"}, "me")
	must(err)
	goneSess, err := api.Claim(ctx, gone.ID, "builders", "new", "me")
	must(err)
	must(api.MarkRunning(ctx, goneSess.ID, map[string]any{"agent": "sweep-new", "pane": "w1:p4"}))
	must(api.EndSession(ctx, goneSess.ID, "died", "the runner went away"))
	plain, err := api.CreateBead(ctx, store.NewBead{Title: "Oil the gate"}, "me")
	must(err)
	paneLess, err := api.Claim(ctx, plain.ID, "builders", "new", "me")
	must(err)
	must(api.MarkRunning(ctx, paneLess.ID, map[string]any{"pid": 4242}))
	_, err = api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me")
	must(err)
	must(api.CrewStarted(ctx, "yardmaster", map[string]any{"pane": "w1:p2"}))

	fake := &screenHerdr{screen: "\x1b[1mPaint the shed\x1b[0m\n$ "}
	st.SetPaneReader(fake)
	for _, c := range []struct{ session, pane string }{
		{sess.ID, "w1:p1"},
		{"mend-new", "w1:p3"},
		{"yardmaster", "w1:p2"},
	} {
		got, err := api.SessionScreen(ctx, c.session)
		if err != nil {
			t.Fatalf("SessionScreen(%q) = %v", c.session, err)
		}
		if got.Screen != fake.screen || fake.pane != c.pane {
			t.Errorf("SessionScreen(%q) = %q of %s, want the reader's screen of %s", c.session, got.Screen, fake.pane, c.pane)
		}
	}
	duplicate, err := api.CreateBead(ctx, store.NewBead{Title: "Mend the wall"}, "me")
	must(err)
	duplicateSess, err := api.Claim(ctx, duplicate.ID, "builders", "new", "me")
	must(err)
	must(api.MarkRunning(ctx, duplicateSess.ID, map[string]any{"agent": "mend-new", "pane": "w1:p5"}))
	if _, err := api.SessionScreen(ctx, "no-such-session"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SessionScreen(no-such-session) = %v, want not found", err)
	}
	if _, err := api.SessionScreen(ctx, "sweep-new"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SessionScreen(sweep-new) = %v, want a finished session's agent name not found", err)
	}
	if _, err := api.SessionScreen(ctx, "mend-new"); err == nil ||
		!strings.Contains(err.Error(), herdSess.ID) || !strings.Contains(err.Error(), duplicateSess.ID) {
		t.Errorf("SessionScreen(mend-new) = %v, want an error naming both live sessions %s and %s", err, herdSess.ID, duplicateSess.ID)
	}
	if _, err := api.SessionScreen(ctx, goneSess.ID); err != nil {
		t.Errorf("SessionScreen(%s) = %v, want the finished session still read by its id", goneSess.ID, err)
	}
	if _, err := api.SessionScreen(ctx, paneLess.ID); err == nil || !strings.Contains(err.Error(), "has no pane") {
		t.Errorf("SessionScreen(%s) = %v, want it refused for having no pane", paneLess.ID, err)
	}
	st.SetPaneReader(nil)
	if _, err := api.SessionScreen(ctx, sess.ID); err == nil || !strings.Contains(err.Error(), "no server has registered a pane reader") {
		t.Errorf("SessionScreen without a reader = %v, want it refused naming the server", err)
	}
}
