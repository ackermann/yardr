package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// Fake executables, by what `--version` does in each.
const (
	toolAnswers = "#!/bin/sh\necho \"tool 1.2.3\"\n"
	toolFails   = "#!/bin/sh\necho \"broken install\" >&2\nexit 3\n"
	toolHangs   = "#!/bin/sh\nsleep 30\n"
)

// fakeTools puts an executable per name on a PATH of their own, each a
// script with the given body, and makes lookPath find those and nothing
// else. It returns the directory.
func fakeTools(t *testing.T, tools map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := lookPath
	lookPath = func(name string) (string, error) {
		if _, ok := tools[name]; ok {
			return filepath.Join(dir, name), nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = old })
	return dir
}

// stub sets a package variable for the test.
func stub[T any](t *testing.T, v *T, to T) {
	t.Helper()
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

// doctorJSON runs yardr doctor --json and returns its report and error.
func doctorJSON(t *testing.T, st store.API) (doctorReport, error) {
	t.Helper()
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor", "--json")
	var rep doctorReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("doctor --json: %v\n%s", jerr, out)
	}
	return rep, err
}

func wantText(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(out, l) {
			t.Errorf("output lacks %q:\n%s", l, out)
		}
	}
}

// On a home with no yard doctor says so, checks the machine, shows every
// step as todo and creates nothing: no store, no server. Without a terminal
// too, where any other command is refused.
func TestDoctorEmptyHome(t *testing.T) {
	home := newHome(t)
	setInteractive(t, false)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers, "grok": toolFails})

	var code int
	out := capture(t, func() { code = execute(&app{}, []string{"doctor"}) })
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	wantText(t, out, "no yard in "+home+" yet", here("yardr init --harness <kind>"), "Nothing was created.",
		"ok     git ", "ok     herdr ", "a harness a yard can be created on: answers its version flag (tool 1.2.3)",
		// A harness nothing runs on yet that does not work is said, and is no fault.
		"note   grok ", "its version flag fails: exit status 3: broken install",
		"todo   a rig: ", "todo   a first merged bead: ", "Nothing the yard needs is missing.")
	if strings.Contains(out, "The yard\n") || strings.Contains(out, "Optional") || strings.Contains(out, "Next step") {
		t.Errorf("a home with no yard has no yard to report on:\n%s", out)
	}
	for _, f := range []string{"yard.db", "yard.sock", "server.pid", "daemon.lock"} {
		if _, err := os.Stat(filepath.Join(home, f)); err == nil {
			t.Errorf("doctor created %s", f)
		}
	}

	// What the onboarding needs and the machine lacks is a fault here too.
	fakeTools(t, map[string]string{"git": toolAnswers})
	out = capture(t, func() { code = execute(&app{}, []string{"doctor"}) })
	if code != 1 {
		t.Fatalf("no herdr, no harness: exit %d:\n%s", code, out)
	}
	wantText(t, out, "FAULT  herdr ", "Get it: https://herdr.dev", "FAULT  a harness ", "2 faults")
	rep, err := doctorJSON(t, nil)
	if !errors.Is(err, errCheckFailed) || rep.Yard || rep.State != nil || len(rep.Faults) != 2 || len(rep.Steps) == 0 || rep.Steps[0].Done {
		t.Fatalf("json: %v %+v", err, rep)
	}
	if _, err := os.Stat(filepath.Join(home, "yard.db")); err == nil {
		t.Fatal("doctor --json created a store")
	}
}

// After init and before anything else: the daemon and the mayor are what
// is missing (exit 1), no step is done, and the next one is the rig. The
// store is as it was.
func TestDoctorFreshYard(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	before, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) || exitCode(err) != 1 {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "yardr doctor: the yard in ", "Needed on this machine", "The yard", "Setup", "Optional",
		"the harness of the yard's default, resident mayor: answers its version flag (tool 1.2.3)",
		"FAULT  daemon ", here("yardr init --start"), "FAULT  mayor ", "mayor is not running: the daemon starts it",
		"ok     residents ", "ok     held ", "todo   a rig: no repository is a rig of this yard yet",
		"todo   routes for new, review and approved: no route for new, review and approved",
		"not configured  web view: ", "not configured  hooks: ", "not configured  packs: ", "not configured  login service: ",
		"2 faults", "Next step: a rig.\n  ", here("yardr rig add <rig> <path> --base <branch>"))
	if strings.Contains(out, "done ") {
		t.Errorf("a step is done on a fresh yard:\n%s", out)
	}
	rep, err := doctorJSON(t, st)
	if !errors.Is(err, errCheckFailed) || !rep.Yard || rep.State == nil || rep.State.Daemon || rep.State.Mayor == nil ||
		!rep.State.Mayor.Resident || rep.State.Mayor.Running || rep.Next == nil || rep.Next.Key != stepRig || len(rep.Optional) != 4 {
		t.Fatalf("json: %v %+v", err, rep)
	}
	// For a home that is not the default, the next step's command names it
	// like every other.
	if !config.IsDefaultHome(rep.Home) && !strings.Contains(out, "\n  yardr --home "+rep.Home+" rig add <rig>") {
		t.Errorf("the next step's command goes to another yard:\n%s", out)
	}
	if rep.Next.Next != "yardr rig add <rig> <path> --base <branch>" {
		t.Errorf("json next = %q", rep.Next.Next)
	}
	if after, _ := st.Head(ctx); after != before {
		t.Fatalf("doctor wrote to the store: head %d, was %d", after, before)
	}
}

// gitRig makes a repository with rig init's files committed on main and
// adds it as rig web.
func gitRig(t *testing.T, st store.API) store.Rig {
	t.Helper()
	dir := t.TempDir()
	rig := store.Rig{Name: "web", Path: dir, Base: "main", Kind: store.RigGit}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if _, err := rigInit(rig, "Jo", "mayor", true); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRig(context.Background(), rig, "me"); err != nil {
		t.Fatal(err)
	}
	return rig
}

// commitRig commits the rig's .yardr files on its base.
func commitRig(t *testing.T, rig store.Rig) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "yardr files"}} {
		cmd := exec.Command("git", append([]string{"-C", rig.Path, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

// A yard set up step by step: each step turns done as the store shows it,
// the next step follows, and with the daemon and the mayor running and a
// bead merged there is nothing left: exit 0.
func TestDoctorSetUpYard(t *testing.T) {
	home := newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	next := func(want string) {
		t.Helper()
		steps, err := setupSteps(ctx, st)
		if err != nil {
			t.Fatal(err)
		}
		got := "complete"
		if n := nextStep(steps); n != nil {
			got = n.Key
		}
		if got != want {
			t.Fatalf("next step = %s, want %s: %+v", got, want, steps)
		}
	}
	next(stepRig)
	rig := gitRig(t, st)
	next(stepRigFiles) // written, not committed: not where yardr reads them
	commitRig(t, rig)
	next(stepBuilders)

	group := func(name, runner string, cfg map[string]any) {
		t.Helper()
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: runner, Config: cfg}); err != nil {
			t.Fatal(err)
		}
	}
	route := func(stage, group string) {
		t.Helper()
		if _, err := st.AddRoute(ctx, store.Route{Stage: stage, Rig: &rig.Name, Group: group}); err != nil {
			t.Fatal(err)
		}
	}
	group("web/builders", "herdr", map[string]any{"kind": "claude", "prompt": "roles/builder.md"})
	next(stepReviewers)
	group("web/reviewers", "herdr", map[string]any{"kind": "claude", "prompt": "roles/reviewer.md"})
	next(stepLanding)
	// People who land are known as such by their route.
	group("web/mergers", "manual", nil)
	next(stepLanding)
	route("approved", "web/mergers")
	next(stepRoutes)
	route("new", "web/builders")
	route("review", "web/reviewers")
	next(stepFirstBead)

	b, err := st.CreateBead(ctx, store.NewBead{Title: "first", Rig: rig.Name}, "me")
	if err != nil {
		t.Fatal(err)
	}
	next(stepMerged)
	// Closed by hand is not through the pipeline.
	if _, err := st.CloseBead(ctx, b.ID, "dropped", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	next(stepMerged)
	if _, err := st.Reopen(ctx, b.ID, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"new", "review", "approved", "merged"} {
		if _, err := st.Advance(ctx, b.ID, stage, store.Expect{}, "me"); err != nil {
			t.Fatalf("to %s: %v", stage, err)
		}
	}
	next("complete")

	// The yard runs: a daemon holds the lock, the mayor has a session.
	unlock, err := lockDaemon(filepath.Join(home, "daemon.lock"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := st.ResidentStarted(ctx, "mayor", mayorHandle()); err != nil {
		t.Fatal(err)
	}
	stub(t, &serverRunning, func(context.Context, *config.Config) bool { return true })
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "ok     server     running", "ok     daemon     running", "mayor is running (herdr agent y-mayor)",
		"the harness of the yard's default, resident mayor, group web/builders, group web/reviewers",
		"done   a rig: web", "done   the rig's .yardr files on its base: web has its flow on its base main",
		"done   a group that builds: web/builders", "done   a group that reviews: web/reviewers",
		"done   a group or person that lands approved beads: web/mergers",
		"done   routes for new, review and approved: new to web/builders, review to web/reviewers, approved to web/mergers",
		"done   a first bead: "+b.ID, "done   a first merged bead: "+b.ID+" (merged)",
		"Nothing the yard needs is missing.", "The setup is complete.")
	if strings.Contains(out, "todo ") || strings.Contains(out, "FAULT") {
		t.Errorf("a set-up yard lacks something:\n%s", out)
	}
	rep, err := doctorJSON(t, st)
	if err != nil || rep.Next != nil || len(rep.Faults) != 0 || !rep.State.Daemon || !rep.State.Mayor.Running {
		t.Fatalf("json: %v %+v", err, rep)
	}

	// What is no fault: a held bead, a mayor stopped on purpose.
	held, err := st.CreateBead(ctx, store.NewBead{Title: "second", Rig: rig.Name}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetHold(ctx, held.ID, true, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.ResidentStopped(ctx, "mayor", "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetResidentEnabled(ctx, "mayor", false, "", "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "note   held       1 held, waiting for someone: "+held.ID, "note   mayor      mayor is stopped (disabled)")
}

// A program the yard needs and the machine lacks is a fault with where to
// get it: a harness a group names, like git and herdr themselves.
func TestDoctorMissingDependency(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"herdr": toolAnswers, "claude": toolAnswers})
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]string{"b": "codex", "c": "cursor"} {
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": kind}}); err != nil {
			t.Fatal(err)
		}
	}
	// A manual group runs on no harness, whatever its config says.
	if err := st.PutGroup(ctx, store.Group{Name: "m", Runner: "manual", Config: map[string]any{"kind": "kimi"}}); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out,
		"FAULT  git        not on PATH: a rig is a git repository: beads are worked on branches, in worktrees. Get it: https://git-scm.com/downloads",
		"FAULT  codex      not on PATH: the harness of group b. Get it: npm install -g @openai/codex",
		"FAULT  cursor-agent  not on PATH: the harness of group c. Get it: see cursor's own documentation; it must be on PATH as cursor-agent")
	if strings.Contains(out, "kimi") {
		t.Errorf("a manual group's kind is no harness the yard needs:\n%s", out)
	}
	rep, _ := doctorJSON(t, st)
	var missing []string
	for _, n := range rep.Machine {
		if !n.Found {
			missing = append(missing, n.Program)
			if n.Get == "" || n.Version != nil {
				t.Errorf("%s: get %q, version %+v", n.Program, n.Get, n.Version)
			}
		}
	}
	slices.Sort(missing)
	if !slices.Equal(missing, []string{"codex", "cursor-agent", "git"}) {
		t.Fatalf("missing = %v", missing)
	}
}

// On PATH is not "works": a harness that is found is run with its version
// flag and answers, fails or hangs; a kind with no known flag is not run.
func TestDoctorVersionCheck(t *testing.T) {
	dir := fakeTools(t, map[string]string{"claude": toolAnswers, "codex": toolFails, "kimi": toolHangs, "cursor-agent": toolHangs})
	ctx := context.Background()
	check := func(kind string, want versionCheck) {
		t.Helper()
		if got := probe(ctx, kind, harnessNeed(kind, "x").Path); got != want {
			t.Errorf("%s: %+v, want %+v", kind, got, want)
		}
	}
	// With the timeout a person gets: a loaded machine must not turn an
	// answer into a hang.
	check("claude", versionCheck{versionAnswers, "tool 1.2.3"})
	check("codex", versionCheck{versionFails, "exit status 3: broken install"})
	check("cursor", versionCheck{versionNotChecked, ""}) // would hang, were it run
	// A path that cannot be run fails, and says why.
	if got := probe(ctx, "claude", filepath.Join(dir, "gone")); got.State != versionFails || !strings.Contains(got.Output, "no such file") {
		t.Errorf("missing executable: %+v", got)
	}
	// A long first line is cut on a character, not in the middle of one.
	if got := versionLine("\n  " + strings.Repeat("─", 90) + "\nmore"); got != strings.Repeat("─", 60)+"…" {
		t.Errorf("versionLine = %q", got)
	}
	// A hang ends on the timeout, the script's sleep with it.
	stub(t, &versionTimeout, 300*time.Millisecond)
	began := time.Now()
	check("kimi", versionCheck{versionHangs, ""})
	if d := time.Since(began); d > 5*time.Second {
		t.Errorf("the hanging check took %s", d)
	}

	// In the report: a failing or hanging harness the yard runs on is a
	// fault, and on macOS the hang names Gatekeeper. The timeout leaves the
	// failing one time to fail.
	stub(t, &versionTimeout, 2*time.Second)
	newHome(t)
	st := storetest.New(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "kimi": toolHangs, "codex": toolFails, "cursor-agent": toolHangs})
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "kimi"); err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]string{"b": "codex", "c": "cursor"} {
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": kind}}); err != nil {
			t.Fatal(err)
		}
	}
	stub(t, &goos, "darwin")
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "FAULT  kimi ", "on PATH, but it did not answer its version flag within 2s. On macOS that can be a quarantined binary "+
		"waiting on a Gatekeeper dialog nobody sees: run kimi once in a terminal and answer it",
		"FAULT  codex ", "on PATH, but its version flag fails: exit status 3: broken install",
		"ok     cursor-agent ", "not checked, yardr knows no version flag for cursor")
	rep, _ := doctorJSON(t, st)
	states := map[string]string{}
	stub(t, &goos, "linux")
	for _, n := range rep.Machine {
		if n.Version != nil {
			states[n.Harness] = n.Version.State
		}
		// Off macOS a hang is only a hang.
		if l := n.line(); n.Harness == "kimi" && (l.tag != tagFault || strings.Contains(l.text, "Gatekeeper") || !strings.HasSuffix(l.text, "within 2s")) {
			t.Errorf("off macOS: %+v", l)
		}
	}
	if states["kimi"] != versionHangs || states["codex"] != versionFails || states["cursor"] != versionNotChecked {
		t.Fatalf("states = %v", states)
	}
}

// A resident whose starts fail is a fault, with the reason of the last.
func TestDoctorFailingResident(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutResident(ctx, store.Resident{Name: "scribe", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetResidentEnabled(ctx, "scribe", true, "", "me"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"scribe", "mayor"} {
		if err := st.ResidentStartFailed(ctx, name, "no workspace", time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "FAULT  resident   scribe is not running: 1 failed starts so far, the last with: no workspace",
		"FAULT  mayor      mayor is not running: 1 failed starts so far, the last with: no workspace",
		here("yardr resident start scribe"))
	if strings.Contains(out, "none failing") {
		t.Errorf("output:\n%s", out)
	}
}

// The rigs a launchd agent cannot read are named once the daemon runs as a
// login service, on macOS, as a note and never as a fault.
func TestDoctorLoginServiceRigs(t *testing.T) {
	user := "/Users/jo"
	rigs := []store.Rig{{Name: "a", Path: user + "/Documents/a"}, {Name: "b", Path: user + "/projects/b"},
		{Name: "c", Path: user + "/Desktop"}, {Name: "d", Path: user + "/Downloads/x/d"}, {Name: "e", Path: user + "/Documents2/e"}, {Name: "f"}}
	if got := protectedRigs(rigs, user); !slices.Equal(got, []string{"a (" + user + "/Documents/a)", "c (" + user + "/Desktop)", "d (" + user + "/Downloads/x/d)"}) {
		t.Fatalf("protected = %v", got)
	}

	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	for _, r := range rigs[:2] {
		if err := st.PutRig(ctx, r, "me"); err != nil {
			t.Fatal(err)
		}
	}
	stub(t, &userHomeDir, func() (string, error) { return user, nil })
	stub(t, &goos, "darwin")
	note := "note            login service: rig a (" + user + "/Documents/a) is in a folder macOS protects"
	// No service: a terminal's daemon has the terminal's access.
	if out, _ := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor"); strings.Contains(out, note) || !strings.Contains(out, "not configured  login service") {
		t.Fatalf("without the service:\n%s", out)
	}
	stub(t, &loginService, func(*config.Config) bool { return true })
	out, _ := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	wantText(t, out, "configured      login service: the daemon starts at login", note, "a launchd agent needs its own access there")
	if strings.Contains(out, "rig b (") || strings.Contains(out, "FAULT  login") {
		t.Errorf("output:\n%s", out)
	}
	rep, _ := doctorJSON(t, st)
	for _, f := range rep.Faults {
		if strings.Contains(f, "Documents") {
			t.Errorf("a protected folder is a fault: %s", f)
		}
	}
	stub(t, &goos, "linux")
	if out, _ := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor"); strings.Contains(out, "macOS protects") {
		t.Errorf("off macOS:\n%s", out)
	}
}

// A yard of dir rigs lands nothing: no landing step, no route for approved,
// and its last step is a bead through to done.
func TestSetupStepsDirRig(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	rig := store.Rig{Name: "docs", Path: dir, Kind: store.RigDir}
	if err := st.PutRig(ctx, rig, "me"); err != nil {
		t.Fatal(err)
	}
	steps, err := setupSteps(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, s := range steps {
		keys = append(keys, s.Key)
	}
	if !slices.Equal(keys, []string{stepRig, stepRigFiles, stepBuilders, stepReviewers, stepRoutes, stepFirstBead, stepMerged}) {
		t.Fatalf("keys = %v", keys)
	}
	if n := nextStep(steps); n.Key != stepRigFiles || n.Next != "yardr rig init docs" {
		t.Fatalf("next = %+v", n)
	}
	if _, err := rigInit(rig, "Jo", "mayor", false); err != nil {
		t.Fatal(err)
	}
	steps, err = setupSteps(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if !steps[1].Done || steps[4].Title != "routes for new and review" || steps[6].Title != "a first bead through to the flow's end" {
		t.Fatalf("steps = %+v", steps)
	}
	// The steps of no yard are those of a yard with a repository to come.
	if none := noSetup(); len(none) != 8 || none[4].Key != stepLanding || nextStep(none).Key != stepRig {
		t.Fatalf("no yard: %+v", none)
	}
}

// The onboarding asks about the same programs with the same words, and
// says where to get them.
func TestPreflightSaysWhereToGet(t *testing.T) {
	fakeTools(t, map[string]string{"git": toolAnswers})
	stubAsk(t, func(...huh.Field) error { return nil })
	var err error
	out := capture(t, func() { err = preflight() })
	if !errors.Is(err, huh.ErrUserAborted) {
		t.Fatalf("no herdr: %v", err)
	}
	n := basicNeeds()[0]
	if n.Program != "herdr" || n.Found || !strings.Contains(out, "herdr is not on PATH: "+n.For+".\n  Get it: "+n.Get+"\n") {
		t.Fatalf("output:\n%s", out)
	}
	if line := n.line(); line.tag != tagFault || !strings.Contains(line.text, n.For) || !strings.Contains(line.text, n.Get) {
		t.Fatalf("doctor's line = %+v", line)
	}
}

// brokenStore is a store that answers nothing about the yard.
type brokenStore struct{ store.API }

func (brokenStore) Settings(context.Context) (map[string]string, error) { return nil, nil }
func (brokenStore) Rigs(context.Context) ([]store.Rig, error) {
	return nil, errors.New("server gone")
}

// A yard whose store cannot be read is a fault of the server, said in the
// report with the machine's part, not a bare error.
func TestDoctorUnreachableServer(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	out, err := run(t, (&app{st: brokenStore{storetest.New(t)}, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "ok     git ", "FAULT  server     cannot be reached: server gone", "1 fault")
	if strings.Contains(out, "Setup") || strings.Contains(out, "daemon") {
		t.Errorf("nothing else of the yard is known:\n%s", out)
	}
}
