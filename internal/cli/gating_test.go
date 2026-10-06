package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rigcheck"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// yardr rig check run in a bead's worktree, or from one, is known to the
// yard while it runs: the bead, light or full, and since when. Nothing is
// left when it has ended, pass or fail, and a run for no bead leaves nothing
// at all.
func TestRigCheckIsKnownWhileItRuns(t *testing.T) {
	home := newHome(t)
	st := storetest.New(t)
	ctx := t.Context()
	repo := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "commit", "-q", "--allow-empty", "-m", "init")
	// The gate shows what the yard knows of it as it runs.
	gating := filepath.Join(home, rigcheck.RunningDir)
	show := `cat ` + gating + `/* 2>/dev/null; echo "files=$(ls ` + gating + ` 2>/dev/null | wc -l | tr -d ' ')"`
	if err := st.PutRig(ctx, store.Rig{Name: "r", Path: repo, Base: "main", Check: show + `; test ! -e fail`, CheckLight: show}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"r-b1", "r-b2"} {
		if _, err := st.CreateBead(ctx, store.NewBead{ID: id, Title: id, Rig: "r"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	wt := filepath.Join(t.TempDir(), "r-b1")
	git(repo, "worktree", "add", "-q", "-b", "yard/r-b1", wt, "main")
	a := &app{st: st, actor: "me", cfg: &config.Config{Home: home}}
	started := time.Now().Add(-time.Second)
	// The gate's own record, as the check read it, and that it is gone after.
	check := func(what string, wantErr bool, args ...string) *rigcheck.Gate {
		t.Helper()
		out, err := run(t, a.rigCheckCmd(), args...)
		if (err != nil) != wantErr {
			t.Fatalf("%s: %v\n%s", what, err, out)
		}
		if entries, _ := os.ReadDir(gating); len(entries) != 0 {
			t.Fatalf("%s: left behind %v", what, entries)
		}
		if gates, err := rigcheck.Gates(home); err != nil || len(gates) != 0 {
			t.Fatalf("%s: gates after it ended: %+v, %v", what, gates, err)
		}
		if strings.Contains(out, "files=0\n") {
			return nil
		}
		if !strings.Contains(out, "files=1\n") {
			t.Fatalf("%s: the check did not see one record:\n%s", what, out)
		}
		var g rigcheck.Gate
		// After the line a light gate says first, if it does.
		if err := json.Unmarshal([]byte(strings.SplitN(out[strings.Index(out, "{"):], "\n", 2)[0]), &g); err != nil {
			t.Fatalf("%s: %v\n%s", what, err, out)
		}
		return &g
	}

	g := check("full gate in the bead's worktree", false, "r", "--dir", wt)
	if g == nil || g.Bead != "r-b1" || g.Rig != "r" || g.Light || g.Waiting || g.Dir != wt || g.Pid != os.Getpid() || g.Since.Before(started) || g.Since.After(time.Now()) {
		t.Fatalf("full gate: %+v", g)
	}
	if g := check("light gate in the bead's worktree", false, "r", "--light", "--dir", wt); g == nil || g.Bead != "r-b1" || !g.Light {
		t.Fatalf("light gate: %+v", g)
	}
	// A red gate is known as it runs too, and gone after.
	if err := os.WriteFile(filepath.Join(wt, "fail"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if g := check("red gate", true, "r", "--dir", wt); g == nil || g.Bead != "r-b1" {
		t.Fatalf("red gate: %+v", g)
	}
	if err := os.Remove(filepath.Join(wt, "fail")); err != nil {
		t.Fatal(err)
	}
	// Run from the worktree with no --dir: the agent waits for it all the same.
	t.Chdir(wt)
	if g := check("from the worktree", false, "r"); g == nil || g.Bead != "r-b1" || g.Dir != repo {
		t.Fatalf("from the worktree: %+v", g)
	}
	// In the rig itself, on no bead's branch: nobody's gate.
	t.Chdir(repo)
	if g := check("in the rig", false, "r"); g != nil {
		t.Fatalf("in the rig: %+v", g)
	}
	// Unless the session says which bead it works: a dir rig's bead has no
	// worktree of its own.
	t.Setenv("YARDR_BEAD", "r-b2")
	if g := check("in the rig, by YARDR_BEAD", false, "r"); g == nil || g.Bead != "r-b2" {
		t.Fatalf("by YARDR_BEAD: %+v", g)
	}
	// The worktree's branch says more than the environment.
	if g := check("worktree before YARDR_BEAD", false, "r", "--dir", wt); g == nil || g.Bead != "r-b1" {
		t.Fatalf("worktree before YARDR_BEAD: %+v", g)
	}
	t.Setenv("YARDR_BEAD", "r-none")
	if g := check("a YARDR_BEAD that is no bead", false, "r"); g != nil {
		t.Fatalf("a YARDR_BEAD that is no bead: %+v", g)
	}
}

// yardr session list says which sessions wait for their gate, light or
// full and for how long, and which of those still wait for a slot; --json
// has the gate on the session. yardr status names the beads.
func TestSessionListShowsTheGate(t *testing.T) {
	gateSlots(t)
	home, st, a := statusYard(t, "abcdef123456")
	if err := st.PutRig(t.Context(), store.Rig{Name: "r", Path: t.TempDir(), Kind: store.RigDir}, "me"); err != nil {
		t.Fatal(err)
	}
	beads := workYard(t, st, "r", []store.Group{
		{Name: "builders", Runner: "herdr", Limit: 3},
		{Name: "backlog", Runner: "manual", Limit: 1},
	}, "builders", "builders", "builders", "backlog")
	mark := func(g rigcheck.Gate) {
		t.Helper()
		r, err := rigcheck.MarkRunning(home, g)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(r.End)
	}
	now := time.Now()
	mark(rigcheck.Gate{Bead: beads[0], Rig: "r", Light: true, Since: now.Add(-6*time.Minute - 5*time.Second)})
	mark(rigcheck.Gate{Bead: beads[1], Rig: "r", Waiting: true, Since: now.Add(-2*time.Minute - 5*time.Second)})
	// A gate for a bead a person holds is nobody's wait.
	mark(rigcheck.Gate{Bead: beads[3], Rig: "r", Since: now})

	// A command of a real yard has its home from its store's configuration.
	inHome := func() *app {
		app := a()
		app.cfg = &config.Config{Home: home}
		return app
	}
	out, err := run(t, inHome().rootCmd(), "session", "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !regexp.MustCompile(`\bSTARTED +GATE$`).MatchString(lines[0]) {
		t.Fatalf("header = %q", lines[0])
	}
	want := map[string]string{beads[0]: "gating 6m (light)", beads[1]: "waiting 2m (full)", beads[2]: "-", beads[3]: "-"}
	for _, l := range lines[1:] {
		bead := strings.Fields(l)[1]
		if w, ok := want[bead]; !ok || !strings.HasSuffix(l, "  "+w) {
			t.Errorf("session of %s: %q, want it to end with %q", bead, l, w)
		}
		delete(want, bead)
	}
	if len(want) != 0 {
		t.Errorf("sessions not listed: %v\n%s", want, out)
	}

	out, err = run(t, inHome().rootCmd(), "session", "list", "--json")
	var listed []listedSession
	if jerr := json.Unmarshal([]byte(out), &listed); err != nil || jerr != nil || len(listed) != 4 {
		t.Fatalf("session list --json: %v %v\n%s", err, jerr, out)
	}
	for _, s := range listed {
		switch g := s.Gate; s.Bead {
		case beads[0]:
			if g == nil || g.Bead != s.Bead || !g.Light || g.Waiting || g.Rig != "r" || g.Since.IsZero() {
				t.Errorf("--json, the light gate: %+v", g)
			}
		case beads[1]:
			if g == nil || g.Light || !g.Waiting {
				t.Errorf("--json, the waiting gate: %+v", g)
			}
		default:
			if g != nil {
				t.Errorf("--json, %s has a gate: %+v", s.Bead, g)
			}
		}
		// The session's own fields are beside it, as they were.
		if s.ID == "" || s.Group == "" || s.State == "" {
			t.Errorf("--json, the session: %+v", s)
		}
	}
	if strings.Count(out, `"gate": {`) != 2 {
		t.Errorf("--json:\n%s", out)
	}

	// yardr status names the beads; the machine's counts are not said by a
	// yard no slot of which is taken.
	out, err = run(t, a().rootCmd(), "status")
	line := "\ngates      of this yard: " + beads[0] + " gating 6m (light), " + beads[1] + " waiting 2m (full), " + beads[3] + " gating "
	if err != nil || !strings.Contains(out, line) {
		t.Fatalf("status: %v\n%s\nwant %q", err, out, line)
	}
	if g := statusJSON(t, a()).Gates; g == nil || len(g.Beads) != 3 || g.Beads[0].Bead != beads[0] || g.Limit != 0 {
		t.Fatalf("status --json: %+v", g)
	}
	// With a slot taken, the counts come first.
	holdSlot(t, os.Getenv("YARDR_GATE_DIR"), 2)
	if out, err = run(t, a().rootCmd(), "status"); err != nil || !strings.Contains(out, "\ngates      1 running, 0 waiting (limit 2); of this yard: "+beads[0]+" gating 6m (light), ") {
		t.Fatalf("status with a slot taken: %v\n%s", err, out)
	}
}
