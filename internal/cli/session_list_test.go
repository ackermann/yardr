package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
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
		{Name: "assembly", Runner: "exec", Config: map[string]any{"cmd": "true"}, Limit: 1},
		{Name: "backlog", Runner: "manual", Limit: 1},
	}, "builders", "assembly", "backlog")

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
	runners := map[string]string{"builders": "herdr", "assembly": "exec"}
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

// session list shows each session's bead originator beside its group, in
// text and in --json; it is read from the bead, so an originator change
// shows on the next list.
func TestSessionListOriginator(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	depot := gitDepot(t, st)
	commitDepot(t, depot)
	if _, err := st.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 4}, "jo"); err != nil {
		t.Fatal(err)
	}
	jo, err := st.CreateBead(ctx, store.NewBead{Title: "jo's bead", Depot: depot.Name}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	hal, err := st.CreateBead(ctx, store.NewBead{Title: "hal's bead", Depot: depot.Name, For: "hal"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []*store.Bead{jo, hal} {
		if _, err := st.Claim(ctx, b.ID, "builders", b.Stage, "jo"); err != nil {
			t.Fatal(err)
		}
	}

	// byBead is each listed row's originator by bead: the cell after the
	// group, whatever the row's empty cells are.
	byBead := func() map[string]string {
		t.Helper()
		out, err := run(t, (&app{st: st}).rootCmd(), "session", "list")
		if err != nil {
			t.Fatalf("session list: %v\n%s", err, out)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if h := strings.Fields(lines[0]); !slices.Equal(h, []string{"ID", "BEAD", "TRAIN", "STAGE", "GROUP", "ORIGINATOR", "LOGIN", "STATE", "RUNNER", "STARTED"}) {
			t.Fatalf("session list header %q, want the originator and login beside the group", lines[0])
		}
		got := map[string]string{}
		for _, l := range lines[1:] {
			f := strings.Fields(l)
			i := slices.Index(f, "builders")
			if i < 0 || i+1 >= len(f) {
				t.Fatalf("session list row %q has no group", l)
			}
			got[f[1]] = f[i+1]
		}
		return got
	}
	if got := byBead(); got[jo.ID] != "jo" || got[hal.ID] != "hal" {
		t.Errorf("session list originators = %v, want jo's jo and hal's hal", got)
	}

	out, err := run(t, (&app{st: st}).rootCmd(), "session", "list", "--json")
	if err != nil {
		t.Fatalf("session list --json: %v\n%s", err, out)
	}
	var listed []dispatch.ListedSession
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("session list --json = %q: %v", out, err)
	}
	js := map[string]string{}
	for _, s := range listed {
		js[s.Bead] = s.Originator
	}
	if js[jo.ID] != "jo" || js[hal.ID] != "hal" {
		t.Errorf("session list --json originators = %v, want jo's jo and hal's hal", js)
	}

	// The originator is read from the bead, not kept on the session: an
	// operator change shows on the next list.
	to := "jo"
	if _, err := st.UpdateBead(ctx, hal.ID, store.BeadPatch{Originator: &to}, store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if got := byBead(); got[hal.ID] != "jo" {
		t.Errorf("after the change, hal's session originator = %q, want jo", got[hal.ID])
	}
}

// session list shows each session's effective login beside its bead
// originator, in text and in --json: the group's login_for, else the
// operator's. A claimed bead for hal on a group with no login_for shows
// hal and jo (the operator), while one on hal's own group shows hal and
// hal (yardr-q0xd).
func TestSessionListLogin(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	depot := gitDepot(t, st)
	commitDepot(t, depot)
	if _, err := st.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 4}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "hal-builders", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "login_for": "hal"}, Limit: 4}, "jo"); err != nil {
		t.Fatal(err)
	}
	claim := func(title, group string) string {
		t.Helper()
		b, err := st.CreateBead(ctx, store.NewBead{Title: title, Depot: depot.Name, For: "hal"}, "jo")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Claim(ctx, b.ID, group, b.Stage, "jo"); err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	subsidy := claim("hal's bead on the operator's login", "builders")
	personal := claim("hal's bead on hal's login", "hal-builders")

	out, err := run(t, (&app{st: st}).rootCmd(), "session", "list")
	if err != nil {
		t.Fatalf("session list: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if h := strings.Fields(lines[0]); !slices.Equal(h, []string{"ID", "BEAD", "TRAIN", "STAGE", "GROUP", "ORIGINATOR", "LOGIN", "STATE", "RUNNER", "STARTED"}) {
		t.Fatalf("session list header %q, want LOGIN beside ORIGINATOR", lines[0])
	}
	type pair struct{ originator, login string }
	got := map[string]pair{}
	for _, l := range lines[1:] {
		f := strings.Fields(l)
		i := slices.Index(f, "builders")
		if i < 0 {
			i = slices.Index(f, "hal-builders")
		}
		if i < 0 || i+2 >= len(f) {
			t.Fatalf("session list row %q has no group with two cells after it", l)
		}
		got[f[1]] = pair{f[i+1], f[i+2]}
	}
	if got[subsidy] != (pair{"hal", "jo"}) {
		t.Errorf("subsidy session = %+v, want originator hal and login jo", got[subsidy])
	}
	if got[personal] != (pair{"hal", "hal"}) {
		t.Errorf("personal session = %+v, want originator hal and login hal", got[personal])
	}

	out, err = run(t, (&app{st: st}).rootCmd(), "session", "list", "--json")
	if err != nil {
		t.Fatalf("session list --json: %v\n%s", err, out)
	}
	var listed []dispatch.ListedSession
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("session list --json = %q: %v", out, err)
	}
	js := map[string]pair{}
	for _, s := range listed {
		js[s.Bead] = pair{s.Originator, s.Login}
	}
	if js[subsidy] != (pair{"hal", "jo"}) || js[personal] != (pair{"hal", "hal"}) {
		t.Errorf("session list --json logins = %v, want subsidy hal/jo and personal hal/hal", js)
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
	var listed []dispatch.ListedSession
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
