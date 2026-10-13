package store_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// herdr_session takes what herdr takes as a session's name, and is stored
// and removed as any setting.
func TestHerdrSessionSettingTakesASessionsName(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	for _, bad := range []string{"../x", "a/b", "a b", "..", "gc\n"} {
		if err := st.SetSetting(ctx, store.HerdrSessionSetting, bad, "me"); err == nil || !strings.Contains(err.Error(), "herdr session's name") {
			t.Errorf("SetSetting(herdr_session, %q) = %v", bad, err)
		}
	}
	for _, name := range []string{"gc", "default", "yard-2.a_b", ""} {
		if err := st.SetSetting(ctx, store.HerdrSessionSetting, name, "me"); err != nil {
			t.Fatalf("SetSetting(herdr_session, %q) = %v", name, err)
		}
		if all, _ := st.Settings(ctx); all[store.HerdrSessionSetting] != name {
			t.Fatalf("after setting %q: %v", name, all)
		}
	}
}

// The setting is not changed under what runs in a pane: a crew member that
// runs or is being started, and a bead's session that herdr runs, are
// listed and it stays as it was. A session of another runner has no pane,
// and setting the value it has already is no change.
func TestHerdrSessionIsNotChangedUnderWhatRuns(t *testing.T) {
	api, s := deviceYard(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, store.HerdrSessionSetting, "gc", "jo"); err != nil {
		t.Fatal(err)
	}
	refused := func(when, value string, want ...string) {
		t.Helper()
		err := api.SetSetting(ctx, store.HerdrSessionSetting, value, "jo")
		if err == nil {
			t.Fatalf("%s: herdr_session was set to %q", when, value)
		}
		for _, w := range append(want, "herdr_session is changed while nothing of the yard runs in a pane") {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: SetSetting(herdr_session, %q) = %v, want %q", when, value, err, w)
			}
		}
		if all, _ := api.Settings(ctx); all[store.HerdrSessionSetting] != "gc" {
			t.Fatalf("%s: the refused change was stored: %v", when, all)
		}
	}

	if err := s.CrewStarting(ctx, "yardmaster", map[string]any{"pane": "w1:p1"}); err != nil {
		t.Fatal(err)
	}
	refused("a crew member being started", "other", "crew member yardmaster")
	if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"pane": "w1:p1", "agent": "y-yardmaster"}); err != nil {
		t.Fatal(err)
	}
	refused("a running crew member", "other", "crew member yardmaster")
	refused("a running crew member, unsetting", "", "crew member yardmaster")
	if err := api.SetSetting(ctx, store.HerdrSessionSetting, "gc", "jo"); err != nil {
		t.Fatalf("the value it has: %v", err)
	}
	if err := s.CrewStopped(ctx, "yardmaster", "test"); err != nil {
		t.Fatal(err)
	}

	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yard", "", outcomeFlow)
	for _, g := range []store.Group{{Name: "agents", Runner: "herdr"}, {Name: "scripts", Runner: "exec", Config: map[string]any{"cmd": "true"}}} {
		if err := s.PutGroup(ctx, g, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	claim := func(group string) *store.Session {
		t.Helper()
		b, err := s.CreateBead(ctx, store.NewBead{Title: group, Depot: "yard", Stage: "new"}, "jo")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, group, "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		return sess
	}
	script := claim("scripts")
	if err := api.SetSetting(ctx, store.HerdrSessionSetting, "gc2", "jo"); err != nil {
		t.Fatalf("with a script's session alone: %v", err)
	}
	if err := api.SetSetting(ctx, store.HerdrSessionSetting, "gc", "jo"); err != nil {
		t.Fatal(err)
	}
	agent := claim("agents")
	refused("a bead's session", "other", "session "+agent.ID+" (agents)")
	if err := s.EndSession(ctx, agent.ID, "died", "test"); err != nil {
		t.Fatal(err)
	}
	if err := api.SetSetting(ctx, store.HerdrSessionSetting, "other", "jo"); err != nil {
		t.Fatalf("with nothing in a pane (the script's session %s still runs): %v", script.ID, err)
	}
}

// A yard that runs names its session for the first time without stopping:
// unset, the setting takes the session every running pane is recorded in
// (the runner's record on the handle), since that moves none. Another
// session's name is refused with the list as before, and so is the pane's
// own session while anything runs whose handle names none: a pane of an
// older build, a session that has no pane yet, or one in another session.
// Once set, the records allow no change.
func TestHerdrSessionIsNamedUnderPanesRecordedInIt(t *testing.T) {
	api, s := deviceYard(t)
	ctx := context.Background()
	const refusal = "herdr_session is changed while nothing of the yard runs in a pane"
	refused := func(when, value string, want ...string) {
		t.Helper()
		was, _ := api.Settings(ctx)
		err := api.SetSetting(ctx, store.HerdrSessionSetting, value, "jo")
		if err == nil {
			t.Fatalf("%s: herdr_session was set to %q", when, value)
		}
		for _, w := range append(want, refusal) {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: SetSetting(herdr_session, %q) = %v, want %q", when, value, err, w)
			}
		}
		if all, _ := api.Settings(ctx); all[store.HerdrSessionSetting] != was[store.HerdrSessionSetting] {
			t.Fatalf("%s: the refused change was stored: %v", when, all)
		}
	}
	unset := func() {
		t.Helper()
		if err := s.CrewStopped(ctx, "yardmaster", "test"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSetting(ctx, store.HerdrSessionSetting, "", "jo"); err != nil {
			t.Fatal(err)
		}
	}
	in := func(session string) map[string]any {
		h := map[string]any{"pane": "w1:p1", "agent": "y-yardmaster"}
		if session != "" {
			h[store.HandleHerdrSession] = session
		}
		return h
	}

	// A pane of a build that recorded no session keeps the refusal.
	if err := s.CrewStarted(ctx, "yardmaster", in("")); err != nil {
		t.Fatal(err)
	}
	refused("a pane with no record of its session", "gc", "crew member yardmaster")
	unset()

	if err := s.CrewStarted(ctx, "yardmaster", in("gc")); err != nil {
		t.Fatal(err)
	}
	refused("a pane recorded in gc", "other", "crew member yardmaster")
	if err := api.SetSetting(ctx, store.HerdrSessionSetting, "gc", "jo"); err != nil {
		t.Fatalf("the session the running pane is recorded in: %v", err)
	}
	if all, _ := api.Settings(ctx); all[store.HerdrSessionSetting] != "gc" {
		t.Fatalf("after setting gc: %v", all)
	}
	// Set, it is changed under nothing that runs, whatever a handle says.
	refused("set, to another", "other", "crew member yardmaster")
	refused("set, unsetting", "", "crew member yardmaster")
	unset()

	// The pane of a start counts beside the running one's.
	if err := s.CrewStarted(ctx, "yardmaster", in("gc")); err != nil {
		t.Fatal(err)
	}
	if err := s.CrewStarting(ctx, "yardmaster", in("")); err != nil {
		t.Fatal(err)
	}
	refused("a start with no record beside a pane in gc", "gc", "crew member yardmaster")
	if err := s.CrewStarting(ctx, "yardmaster", in("gc")); err != nil {
		t.Fatal(err)
	}

	// A bead's session: none while it has no pane, then its handle's.
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "agents", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "agents", Depot: "yard", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "agents", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	listed := "session " + sess.ID + " (agents)"
	refused("a bead's session without a pane yet", "gc", listed)
	if err := s.SetHandle(ctx, sess.ID, map[string]any{"pane": "w2:p1", store.HandleHerdrSession: "other"}); err != nil {
		t.Fatal(err)
	}
	// What is in the session named is not in the list: it need not stop.
	err = api.SetSetting(ctx, store.HerdrSessionSetting, "gc", "jo")
	if err == nil || !strings.Contains(err.Error(), listed) || strings.Contains(err.Error(), "crew member yardmaster") {
		t.Fatalf("a bead's session recorded in another session: %v", err)
	}
	if err := s.SetHandle(ctx, sess.ID, map[string]any{"pane": "w2:p1", store.HandleHerdrSession: "gc"}); err != nil {
		t.Fatal(err)
	}
	refused("every pane recorded in gc", "other", "crew member yardmaster", listed)
	if err := api.SetSetting(ctx, store.HerdrSessionSetting, "gc", "jo"); err != nil {
		t.Fatalf("every pane recorded in gc: %v", err)
	}
}

// Pane ids are a herdr session's own: w1:p1 of the yard's session and
// w1:p1 of another are two panes. The store never takes a pane's id from
// the caller, it goes by the process that called and the shell it recorded
// for the pane, so a caller in the other session's w1:p1 is in no pane of
// the yard. It is taken for what it is, a process of this user outside the
// yard's panes, exactly as one in a terminal with no herdr at all: it may
// do what the operator's name allows and nothing a pane's agent may, and it
// is no crew member. And the yard's own w1:p1 stays refused whatever herdr
// session its environment claims: nothing of the caller's environment is
// asked.
func TestCallerInAnotherHerdrSessionIsInNoPaneOfTheYard(t *testing.T) {
	api, own := deviceYard(t)
	ctx := context.Background()
	if err := own.SetSetting(ctx, store.HerdrSessionSetting, "gc", "jo"); err != nil {
		t.Fatal(err)
	}
	// The yardmaster's pane, w1:p1 of the yard's session: its shell is a
	// process of its own.
	shell := exec.Command("sleep", "60")
	if err := shell.Start(); err != nil {
		t.Skipf("no process to stand for a pane's shell: %v", err)
	}
	t.Cleanup(func() { shell.Process.Kill(); shell.Wait() })
	ps, err := peer.Describe(shell.Process.Pid)
	if err != nil {
		t.Skipf("the pane's shell cannot be described: %v", err)
	}
	if err := api.CrewStarted(ctx, "yardmaster", map[string]any{"pane": "w1:p1", "shell_pid": shell.Process.Pid, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}

	// From the yard's w1:p1, naming the operator: refused. The caller's
	// environment could say any herdr session; the store is given the
	// process alone (WithCaller).
	inYard := store.WithCaller(ctx, shell.Process.Pid)
	if _, err := own.AddDevice(inYard, "ada", "phone", "jo"); err == nil || !strings.Contains(err.Error(), "this is the crew member yardmaster") {
		t.Fatalf("AddDevice from the yard's pane as jo = %v", err)
	}

	// From w1:p1 of another herdr session: this process, which no pane of
	// the yard holds. It is not the yardmaster for sharing its pane's id.
	elsewhere := store.WithCaller(ctx, os.Getpid())
	if _, err := own.AddDevice(elsewhere, "ada", "phone", "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "only the operator (jo) may add a device; this is yardmaster") {
		t.Fatalf("AddDevice from another session's pane as yardmaster = %v", err)
	}
	// It gains nothing over a caller in no herdr at all: both are refused
	// and served alike, by the name they give.
	plain := ctx
	for _, actor := range []string{"ada", "yardmaster", ""} {
		_, errElsewhere := own.AddDevice(elsewhere, "bob", "tablet", actor)
		_, errPlain := own.AddDevice(plain, "bob", "tablet", actor)
		if errElsewhere == nil || errPlain == nil || errElsewhere.Error() != errPlain.Error() {
			t.Errorf("as %q: from another session's pane %v, from no pane %v", actor, errElsewhere, errPlain)
		}
	}
	if _, err := own.AddDevice(elsewhere, "ada", "phone", "jo"); err != nil {
		t.Fatalf("the operator's name from outside the yard's panes, as from any terminal: %v", err)
	}
	// And the yard's pane is still refused after it.
	if _, err := own.AddDevice(inYard, "bob", "tablet", "jo"); err == nil || !strings.Contains(err.Error(), "this is the crew member yardmaster") {
		t.Fatalf("AddDevice from the yard's pane as jo = %v", err)
	}
}
