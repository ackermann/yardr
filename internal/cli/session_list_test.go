package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// session list --running leaves out a manual group's claims, which are
// beads waiting for a person; without the flag they are listed as before
// (yardr-jcw1: 35 of 38 rows were backlog beads).
func TestSessionListRunning(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	depot := gitDepot(t, st)
	commitDepot(t, depot)
	beads := workYard(t, st, depot.Name, []store.Group{
		{Name: "builders", Runner: "herdr", Limit: 1},
		{Name: "refinery", Runner: "exec", Limit: 1},
		{Name: "backlog", Runner: "manual", Limit: 1},
	}, "builders", "refinery", "backlog")

	// The beads listed, in text and in JSON.
	list := func(args ...string) (text, js []string) {
		t.Helper()
		out, err := run(t, (&app{st: st}).rootCmd(), append([]string{"session", "list"}, args...)...)
		if err != nil {
			t.Fatalf("session list %v: %v\n%s", args, err, out)
		}
		for _, l := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
			text = append(text, strings.Fields(l)[1])
		}
		out, err = run(t, (&app{st: st}).rootCmd(), append([]string{"session", "list", "--json"}, args...)...)
		var ss []store.Session
		if jerr := json.Unmarshal([]byte(out), &ss); err != nil || jerr != nil {
			t.Fatalf("session list --json %v: %v %v\n%s", args, err, jerr, out)
		}
		for _, s := range ss {
			js = append(js, s.Bead)
		}
		slices.Sort(text)
		slices.Sort(js)
		return text, js
	}
	sorted := func(ids ...string) []string { slices.Sort(ids); return ids }

	all := sorted(beads...)
	if text, js := list(); !slices.Equal(text, all) || !slices.Equal(js, all) {
		t.Errorf("session list = %v, --json %v, want %v", text, js, all)
	}
	// Claiming a bead mints a new handle, so use the claimed sessions' bead
	// handles rather than the pre-claim IDs returned by workYard.
	ss, err := st.Sessions(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	var running []string
	runners := map[string]string{"builders": "herdr", "refinery": "exec"}
	for _, s := range ss {
		if want, ok := runners[s.Group]; ok {
			if s.Runner != want {
				t.Errorf("%s session runner = %q, want %q", s.Group, s.Runner, want)
			}
			running = append(running, s.Bead)
		}
	}
	if len(running) != 2 {
		t.Fatalf("got %d herdr/exec sessions, want 2", len(running))
	}
	running = sorted(running...)
	if text, js := list("--running"); !slices.Equal(text, running) || !slices.Equal(js, running) {
		t.Errorf("session list --running = %v, --json %v, want %v", text, js, running)
	}
}

// session list says until when a session waits for the window of its usage
// limit, and the model its group starts no further session with meanwhile,
// under the table and as reset_wait in --json: read from the session's
// handle as the daemon keeps it (yardr-vpzj).
func TestSessionListSaysUntilWhenAGroupIsHeldBack(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	depot := gitDepot(t, st)
	commitDepot(t, depot)
	workYard(t, st, depot.Name, []store.Group{{Name: "builders", Runner: "herdr", Limit: 3}}, "builders", "builders", "builders")
	ss, err := st.Sessions(t.Context(), false)
	if err != nil || len(ss) != 3 {
		t.Fatalf("sessions = %d, %v, want 3", len(ss), err)
	}
	until := time.Now().Add(42*time.Minute + 30*time.Second).UTC().Truncate(time.Second)
	wait := func(s store.Session, model string, at time.Time) {
		t.Helper()
		h := map[string]any{"harness_error": "rate_limited", "harness_reset_at": at.Format(time.RFC3339Nano),
			"harness_reset_window": "5h", "harness_reset_model": model}
		if err := st.SetHandle(t.Context(), s.ID, h); err != nil {
			t.Fatal(err)
		}
	}
	wait(ss[0], "k3", until)
	due := until.Add(-45*time.Minute - 30*time.Second) // 3m ago
	wait(ss[1], "", due)

	out, err := run(t, (&app{st: st}).rootCmd(), "session", "list")
	if err != nil {
		t.Fatalf("session list: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 6 {
		t.Fatalf("session list has %d lines, want the header, 3 sessions and 2 waits:\n%s", len(lines), out)
	}
	clock := func(at time.Time) string { return limits.Clock(at, time.Now()) }
	for i, want := range []string{
		"builders starts no further session with model k3 until " + clock(until) + " (in 42m): session " + ss[0].ID +
			" (bead " + ss[0].Bead + ") waits for the 5h window of its usage limit to reset",
		"builders starts no further session with the agent's own model until " + clock(due) + " (3m ago): session " + ss[1].ID +
			" (bead " + ss[1].Bead + ") waits for the 5h window of its usage limit to reset",
	} {
		if got := lines[4+i]; got != want {
			t.Errorf("line %d = %q\nwant     %q", 4+i, got, want)
		}
	}

	out, err = run(t, (&app{st: st}).rootCmd(), "session", "list", "--json")
	var listed []listedSession
	if jerr := json.Unmarshal([]byte(out), &listed); err != nil || jerr != nil {
		t.Fatalf("session list --json: %v %v\n%s", err, jerr, out)
	}
	for _, l := range listed {
		switch w := l.ResetWait; l.ID {
		case ss[0].ID:
			if w == nil || !w.Until.Equal(until) || w.Window != "5h" || w.Model != "k3" {
				t.Errorf("reset_wait of %s = %+v, want until %v, 5h, k3", l.ID, w, until)
			}
		case ss[1].ID:
			if w == nil || w.Model != "" {
				t.Errorf("reset_wait of %s = %+v, want one with no model", l.ID, w)
			}
		default:
			if w != nil {
				t.Errorf("reset_wait of %s = %+v, want none: it does not wait", l.ID, w)
			}
		}
	}
}
