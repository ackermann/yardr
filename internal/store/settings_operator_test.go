package store_test

import (
	"context"
	"maps"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// Settings are the operator's alone to change: a crew member and a bead's
// agent are refused, by name and from their panes, for the address the
// server takes webhooks on and for who the operator is, and nothing is
// stored and no event written. The operator changes one as before, and
// everyone reads them.
func TestSettingsAreTheOperatorsToChange(t *testing.T) {
	t.Parallel()
	s, b := paneYard(t) // the operator is jo, the crew member yardmaster
	ctx := context.Background()
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	pane := store.WithCaller(ctx, me)
	before, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	refused := func(t *testing.T, ctx context.Context, actor, this string) {
		t.Helper()
		head, _ := s.Head(ctx)
		for key, value := range map[string]string{"hook_addr": "0.0.0.0:8790", store.OperatorSetting: "mallory", "default_kind": ""} {
			err := s.SetSetting(ctx, key, value, actor)
			if want := "only the operator (jo) may change a setting; this is " + this; err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s = %q as %s: %v, want %q", key, value, actor, err, want)
			}
		}
		if got, err := s.Settings(ctx); err != nil || !maps.Equal(got, before) {
			t.Errorf("settings = %v, %v after the refusals, want %v", got, err, before)
		}
		if now, _ := s.Head(ctx); now != head {
			t.Errorf("the refusals logged %d events", now-head)
		}
		if us, _ := s.Users(ctx); len(us) != 1 {
			t.Errorf("the refusals added a user: %+v", us)
		}
	}
	t.Run("a crew member's pane", func(t *testing.T) {
		if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
			t.Fatal(err)
		}
		refused(t, pane, "yardmaster", "yardmaster")
		if err := s.CrewStopped(ctx, "yardmaster", "the test is over"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a bead's session", func(t *testing.T) {
		sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.MarkRunning(ctx, sess.ID, map[string]any{"pid": me, "start": ps.Start}); err != nil {
			t.Fatal(err)
		}
		// The pane's actor (an exec session runs as its group),
		// whatever --actor says, even the operator's name (yardr-6g56).
		refused(t, pane, sess.ID, "builders")
		refused(t, pane, "jo", "builders")
	})
	t.Run("anyone else by name", func(t *testing.T) {
		refused(t, ctx, "kim", "kim")
		refused(t, ctx, "", "nobody")
	})
	t.Run("the operator", func(t *testing.T) {
		if err := s.SetSetting(ctx, "hook_addr", "127.0.0.1:8790", "jo"); err != nil {
			t.Fatal(err)
		}
		evs, _ := s.Events(ctx, "", 1)
		if len(evs) != 1 || evs[0].Kind != "setting_changed" || evs[0].Actor != "jo" || evs[0].Data["key"] != "hook_addr" {
			t.Errorf("event = %+v", evs)
		}
		// Naming another operator is theirs too, and from then on the
		// settings are the new one's.
		if err := s.SetSetting(ctx, store.OperatorSetting, "kim", "jo"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSetting(ctx, "hook_addr", "", "jo"); err == nil ||
			!strings.Contains(err.Error(), "only the operator (kim) may change a setting; this is jo") {
			t.Errorf("the operator before: %v", err)
		}
		if err := s.SetSetting(ctx, "hook_addr", "", "kim"); err != nil {
			t.Fatal(err)
		}
		// Reading stays anyone's, an agent's pane among them.
		if got, err := s.Settings(pane); err != nil || got[store.OperatorSetting] != "kim" || got["hook_addr"] != "" {
			t.Errorf("settings read from a pane = %v, %v", got, err)
		}
	})
}

// A yard that names no operator has nobody to hold a setting to: it takes
// one from whoever is no agent of its own (yardr init on a new home, a
// setting stored before any init), and none from a crew member or from the
// pane of a bead's session, so no agent names itself the operator.
func TestSettingsOfAYardWithoutAnOperator(t *testing.T) {
	t.Parallel()
	_, s := storetest.NewOwned(t)
	ctx := context.Background()
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "kim"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "yard"}, "kim"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "kim"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard"}, "kim")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "builders", b.Stage, "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRunning(ctx, sess.ID, map[string]any{"pid": me, "start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	pane := store.WithCaller(ctx, me)
	for _, c := range []struct {
		ctx         context.Context
		actor, this string
	}{
		{ctx, "yardmaster", "the crew member yardmaster"},
		// Whatever name the call from the pane gives.
		{pane, sess.ID, "the session " + sess.ID + " of a bead's agent"},
		{pane, "kim", "the session " + sess.ID + " of a bead's agent"},
	} {
		for key, value := range map[string]string{"hook_addr": "0.0.0.0:8790", store.OperatorSetting: "mallory"} {
			err := s.SetSetting(c.ctx, key, value, c.actor)
			if want := "only the operator may change a setting; this is " + c.this; err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s as %s: %v, want %q", key, c.actor, err, want)
			}
		}
	}
	if got, err := s.Settings(ctx); err != nil || len(got) != 1 || got[store.FilesMaxSetting] != "64" {
		t.Errorf("settings = %v, %v after the refusals", got, err)
	}
	if now, _ := s.Head(ctx); now != head {
		t.Errorf("the refusals logged %d events", now-head)
	}
	// A person sets one before the yard has an operator, and names it.
	if err := s.SetSetting(ctx, "hook_addr", "127.0.0.1:8790", "kim"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "kim"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "hook_addr", "", "kim"); err == nil {
		t.Error("kim changed a setting after naming jo the operator")
	}
}
