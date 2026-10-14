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
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// Fake executables, by what `--version` does in each.
const (
	toolAnswers = "#!/bin/sh\necho \"tool 1.2.3\"\n"
	toolFails   = "#!/bin/sh\necho \"broken install\" >&2\nexit 3\n"
	toolHangs   = "#!/bin/sh\nsleep 30\n"
)

// fakeToolsTimeout is the time a fake tool gets for its version flag.
const fakeToolsTimeout = 30 * time.Second

// fakeTools puts an executable per name on a PATH of their own, each a
// script with the given body, and makes lookPath find those and nothing
// else. It returns the directory.
//
// It also gives a version flag far longer than a person gets: on a loaded
// machine a script's start alone took over 2s, and an answer or a failure
// was read as a hang (yardr-r4q3, yardr-dywt). A test about the limit sets
// versionTimeout itself, after this.
func fakeTools(t *testing.T, tools map[string]string) string {
	t.Helper()
	stub(t, &versionTimeout, fakeToolsTimeout)
	dir := t.TempDir()
	for name, body := range tools {
		scripttest.Write(t, filepath.Join(dir, name), body)
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

// doctorJSON runs yardr doctor --json, with more flags if given, and returns
// its report and error.
func doctorJSON(t *testing.T, st store.API, flags ...string) (doctorReport, error) {
	t.Helper()
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), append([]string{"doctor", "--json"}, flags...)...)
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
// step as todo and creates nothing: not even the home directory, no store,
// no server. Without a terminal too, where any other command is refused.
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
		"todo   a depot: ", "todo   a first merged bead: ", "Nothing the yard needs is missing.")
	if strings.Contains(out, "The yard\n") || strings.Contains(out, "Optional") || strings.Contains(out, "Next step") {
		t.Errorf("a home with no yard has no yard to report on:\n%s", out)
	}
	for _, f := range []string{"yard.db", "yard.sock", "server.pid", "daemon.lock"} {
		if _, err := os.Stat(filepath.Join(home, f)); err == nil {
			t.Errorf("doctor created %s", f)
		}
	}
	if _, err := os.Stat(home); err == nil {
		t.Error("doctor created the home directory")
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
	if _, err := os.Stat(home); err == nil {
		t.Fatal("doctor --json created the home directory")
	}
}

// After init and before anything else: the daemon and the yardmaster are what
// is missing (exit 1), no step is done, and the next one is the depot. The
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
		"the harness of the yard's default, crew member yardmaster: answers its version flag (tool 1.2.3)",
		"FAULT  daemon ", here("yardr init --start"), "FAULT  yardmaster  ", "yardmaster is not running: the daemon starts it",
		"ok     crew ", "ok     held ", "todo   a depot: no repository is a depot of this yard yet",
		"todo   routes for new, review and approved: no route for new, review and approved",
		"not configured  web view: ", "not configured  hooks: ", "not configured  sources: ", "not configured  packs: ", "not configured  login service: ",
		"2 faults", "Next step: a depot.\n  ", here("yardr depot add <depot> <path> --base <branch>"))
	if strings.Contains(out, "done ") {
		t.Errorf("a step is done on a fresh yard:\n%s", out)
	}
	rep, err := doctorJSON(t, st)
	if !errors.Is(err, errCheckFailed) || !rep.Yard || rep.State == nil || rep.State.Daemon || rep.State.Yardmaster == nil ||
		!rep.State.Yardmaster.CrewMember || rep.State.Yardmaster.Running || rep.Next == nil || rep.Next.Key != stepDepot || len(rep.Optional) != 8 {
		t.Fatalf("json: %v %+v", err, rep)
	}
	// For a home that is not the default, the next step's command names it
	// like every other.
	if !config.IsDefaultHome(rep.Home) && !strings.Contains(out, "\n  yardr --home "+rep.Home+" depot add <depot>") {
		t.Errorf("the next step's command goes to another yard:\n%s", out)
	}
	if rep.Next.Next != "yardr depot add <depot> <path> --base <branch>" {
		t.Errorf("json next = %q", rep.Next.Next)
	}
	if after, _ := st.Head(ctx); after != before {
		t.Fatalf("doctor wrote to the store: head %d, was %d", after, before)
	}
}

// gitDepot makes a repository that keeps its own .yardr files
// (ownDepotFiles) and adds it as depot web.
func gitDepot(t *testing.T, st store.API) store.Depot {
	t.Helper()
	dir := t.TempDir()
	depot := store.Depot{Name: "web", Path: dir, Base: "main", Kind: store.DepotGit}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	ownDepotFiles(t, depot)
	if err := st.PutDepot(context.Background(), depot, "me"); err != nil {
		t.Fatal(err)
	}
	return depot
}

// commitDepot commits the depot's .yardr files on its base.
func commitDepot(t *testing.T, depot store.Depot) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "yardr files"}} {
		cmd := exec.Command("git", append([]string{"-C", depot.Path, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

// A yard set up step by step: each step turns done as the store shows it,
// the next step follows, and with the daemon and the yardmaster running and a
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
	next(stepDepot)
	depot := gitDepot(t, st)
	next(stepDepotFiles) // written, not committed: not where yardr reads them
	commitDepot(t, depot)
	next(stepBuilders)

	group := func(name, runner string, cfg map[string]any) {
		t.Helper()
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: runner, Config: cfg}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	route := func(stage, group string) {
		t.Helper()
		if _, err := st.AddRoute(ctx, store.Route{Stage: stage, Depot: &depot.Name, Group: group}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	// A group does a stage's work once it is routed for it, whatever its role.
	group("web/builders", "herdr", map[string]any{"kind": "claude", "prompt": "roles/builder.md"})
	next(stepBuilders)
	route("new", "web/builders")
	next(stepReviewers)
	group("web/reviewers", "herdr", map[string]any{"kind": "claude", "prompt": "roles/reviewer.md"})
	route("review", "web/reviewers")
	next(stepLanding)
	group("web/mergers", "manual", nil)
	next(stepLanding)
	route("approved", "web/mergers")
	next(stepFirstBead)

	b, err := st.CreateBead(ctx, store.NewBead{Title: "first", Depot: depot.Name}, "me")
	if err != nil {
		t.Fatal(err)
	}
	next(stepMerged)
	// Closed by hand is not down the line.
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

	// The yard runs: a daemon holds the lock, the yardmaster has a session.
	unlock, err := lockDaemon(filepath.Join(home, "daemon.lock"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := st.CrewStarted(ctx, "yardmaster", yardmasterHandle()); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "ok     server      running", "ok     daemon      running", "yardmaster is running (herdr agent y-yardmaster)",
		"the harness of the yard's default, crew member yardmaster, group web/builders, group web/reviewers",
		"done   a depot: web", "done   the depot's .yardr files on its base: web has its flow on its base main",
		"done   a group that builds: web/builders", "done   a group that reviews: web/reviewers",
		"done   a group or person that lands approved beads: web/mergers",
		"done   routes for new, review and approved: new to web/builders, review to web/reviewers, approved to web/mergers",
		"done   a first bead: "+b.ID, "done   a first merged bead: "+b.ID+" (merged)",
		"Nothing the yard needs is missing.", "The setup is complete.")
	if strings.Contains(out, "todo ") || strings.Contains(out, "FAULT") {
		t.Errorf("a set-up yard lacks something:\n%s", out)
	}
	rep, err := doctorJSON(t, st)
	if err != nil || rep.Next != nil || len(rep.Faults) != 0 || !rep.State.Daemon || !rep.State.Yardmaster.Running {
		t.Fatalf("json: %v %+v", err, rep)
	}

	// What is no fault: a held bead, a yardmaster stopped on purpose.
	held, err := st.CreateBead(ctx, store.NewBead{Title: "second", Depot: depot.Name}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetHold(ctx, held.ID, true, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.CrewStopped(ctx, "yardmaster", "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCrewEnabled(ctx, "yardmaster", false, "", "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "note   held        1 held, waiting for someone: "+held.ID, "note   yardmaster  yardmaster is stopped (disabled)")
}

// A program the yard needs and the machine lacks is a fault with where to
// get it: a harness a group names, like git and herdr themselves.
func TestDoctorMissingDependency(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"herdr": toolAnswers, "claude": toolAnswers})
	ctx := context.Background()
	// A manual group runs on no harness, whatever its config says: a kind
	// is no key of one, so this one is from before the store refused it.
	path := filepath.Join(t.TempDir(), "yard.db")
	old, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.PutGroup(ctx, store.Group{Name: "m", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(ctx); err != nil {
		t.Fatal(err)
	}
	storetest.OldConfig(t, path, "groups", "m", "kind", "kimi")
	st := storetest.Open(t, path)
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]string{"b": "codex", "c": "cursor"} {
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": kind}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out,
		"FAULT  git         not on PATH: a depot is a git repository: beads are worked on branches, in worktrees. Get it: https://git-scm.com/downloads",
		"FAULT  codex       not on PATH: the harness of group b. Get it: npm install -g @openai/codex",
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

// A test with fake tools on PATH does not run them under the limit a person
// gets (yardr-dywt): the limit is fakeTools' for as long as the test runs,
// and the one before it afterwards.
func TestFakeToolsVersionTimeout(t *testing.T) {
	before := versionTimeout
	t.Run("with fake tools", func(t *testing.T) {
		fakeTools(t, map[string]string{"claude": toolAnswers})
		if versionTimeout != fakeToolsTimeout || versionTimeout <= before {
			t.Errorf("versionTimeout = %s with fake tools, %s without", versionTimeout, before)
		}
	})
	if versionTimeout != before {
		t.Errorf("versionTimeout = %s after the test, %s before it", versionTimeout, before)
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
	// These are not about the timeout, and run under the long one fakeTools
	// set.
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
	// fault, and on macOS the hang names Gatekeeper. Two reports, for one
	// timeout is not right for both: the failing harness needs one long
	// enough to fail in on any machine, and a hanging one costs all of it.
	// First the failing one, with a yardmaster's harness that answers.
	newHome(t)
	st := storetest.New(t)
	tools := map[string]string{"git": toolAnswers, "herdr": toolAnswers, "kimi": toolAnswers, "codex": toolFails, "cursor-agent": toolHangs}
	fakeTools(t, tools) // and the long timeout again
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "kimi"); err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]string{"b": "codex", "c": "cursor"} {
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": kind}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	stub(t, &goos, "darwin")
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "ok     kimi ", "the harness of the yard's default, crew member yardmaster: answers its version flag (tool 1.2.3)",
		"FAULT  codex ", "on PATH, but its version flag fails: exit status 3: broken install",
		"ok     cursor-agent ", "not checked, yardr knows no version flag for cursor",
		// Beside each: the file yardr reads the kind from, or that it has none.
		"answers its version flag (tool 1.2.3); harness file v1", "broken install; harness file v1; always flags: --no-daemon --config=check_for_update_on_startup=false",
		"no version flag for cursor; no harness file (yardr harness list)")
	states := func() map[string]string {
		t.Helper()
		rep, _ := doctorJSON(t, st)
		out := map[string]string{}
		for _, n := range rep.Machine {
			if n.Version != nil {
				out[n.Harness] = n.Version.State
			}
			// Off macOS a hang is only a hang.
			stub(t, &goos, "linux")
			if l := n.line(); n.Version != nil && n.Version.State == versionHangs &&
				(l.tag != tagFault || strings.Contains(l.text, "Gatekeeper") || !strings.Contains(l.text, "within "+versionTimeout.String()+"; harness file v1")) {
				t.Errorf("off macOS: %+v", l)
			}
			stub(t, &goos, "darwin")
		}
		return out
	}
	if s := states(); s["kimi"] != versionAnswers || s["codex"] != versionFails || s["cursor"] != versionNotChecked {
		t.Fatalf("states = %v", s)
	}
	for _, n := range func() []need { rep, _ := doctorJSON(t, st); return rep.Machine }() {
		if n.Harness == "codex" && !strings.Contains(n.line().text, "always flags: --no-daemon --config=check_for_update_on_startup=false") {
			t.Fatalf("doctor omitted Codex's always flags: %+v", n.line())
		}
	}

	// Then the hanging one, with a timeout it is quick to run out of. Only
	// kimi is looked at: in so short a time the others may hang too.
	tools["kimi"] = toolHangs
	fakeTools(t, tools)
	stub(t, &versionTimeout, 300*time.Millisecond)
	out, err = run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "FAULT  kimi ", "on PATH, but it did not answer its version flag within 300ms. On macOS that can be a quarantined binary "+
		"waiting on a Gatekeeper dialog nobody sees: run kimi once in a terminal and answer it")
	if s := states(); s["kimi"] != versionHangs {
		t.Fatalf("states = %v", s)
	}
}

// A crew member whose starts fail is a fault, with the reason of the last.
func TestDoctorFailingCrew(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "scribe", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCrewEnabled(ctx, "scribe", true, "", "me"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"scribe", "yardmaster"} {
		if err := st.CrewStartFailed(ctx, name, "no workspace", time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "FAULT  crew        scribe is not running: 1 failed starts so far, the last with: no workspace",
		"FAULT  yardmaster  yardmaster is not running: 1 failed starts so far, the last with: no workspace",
		here("yardr crew start scribe"))
	if strings.Contains(out, "none failing") {
		t.Errorf("output:\n%s", out)
	}
}

// A crew member at its harness's login screen is a fault (yardr-m519): its
// session runs, and the harness is not logged in. The yardmaster's own line
// still says it runs; this one says the login, with the command that logs
// in, from the kind the handle names first.
func TestDoctorCrewNotLoggedIn(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers, "kimi": toolAnswers})
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "scribe", Runner: "herdr", Config: map[string]any{"kind": "codex"}}, "me"); err != nil {
		t.Fatal(err)
	}
	marked := func(kind, line string) map[string]any {
		h := yardmasterHandle()
		if kind != "" {
			h["kind"] = kind
		}
		h[dispatch.HarnessLoginKey] = line
		return h
	}
	if err := st.CrewStarted(ctx, "yardmaster", marked("", "Select login method:")); err != nil {
		t.Fatal(err)
	}
	if err := st.CrewStarted(ctx, "scribe", marked("kimi", "Error: [auth.login_required]")); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "ok     yardmaster  yardmaster is running (herdr agent y-yardmaster)",
		"FAULT  crew        yardmaster is not logged in: run claude in another terminal and log in there",
		"FAULT  crew        scribe is not logged in: run kimi in another terminal and log in there",
		here("yardr crew restart scribe"))
	var rep struct {
		State struct {
			Login []struct {
				Name, Kind, Line string
			} `json:"login"`
		} `json:"state"`
	}
	out, err = run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor", "--json")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, l := range rep.State.Login {
		byName[l.Name] = l.Kind + "|" + l.Line
	}
	if byName["yardmaster"] != "claude|Select login method:" || byName["scribe"] != "kimi|Error: [auth.login_required]" {
		t.Errorf("state.login = %+v, want both crew with kind and line", rep.State.Login)
	}
}

// Found live (yardr-05dd): a yardmaster whose harness exits at once is given up
// like any crew member, but it is the one who hears crew_gave_up, so doctor
// is where it is said, with how long the last session lived; crew list
// and show give the count, and the next try while there is one.
func TestDoctorYardmasterGivenUpAfterSessionsGoneAtOnce(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	st := storetest.New(t)
	ctx := context.Background()
	cmd := func(args ...string) string {
		t.Helper()
		out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), args...)
		if err != nil && !errors.Is(err, errCheckFailed) {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	cmd("init", "--harness", "claude")
	const reason = "the session ended 4s after its start (gone within 1m0s counts as a failed start)"
	gone := func() {
		t.Helper()
		if err := st.CrewStarted(ctx, "yardmaster", yardmasterHandle()); err != nil {
			t.Fatal(err)
		}
		if err := st.CrewStopped(ctx, "yardmaster", "the session is gone"); err != nil {
			t.Fatal(err)
		}
		if err := st.CrewStartFailed(ctx, "yardmaster", reason, 2*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	gone()
	wantText(t, cmd("crew", "list"), "stopped (1 failed start, next try in ")
	gone() // the start in between did not clear the count
	// The wait itself is a second or so under the two minutes by now.
	wantText(t, cmd("crew", "list"), "stopped (2 failed starts, next try in ")
	wantText(t, cmd("crew", "show", "yardmaster"), "  2 failed starts, next try in ", "s, the last with: "+reason,
		"  yardr doctor --try asks its harness for one real answer")
	wantText(t, cmd("doctor"), "FAULT  yardmaster  yardmaster is not running: 2 failed starts so far, the last with: "+reason,
		"note   harness     on PATH is not \"works\"", here("yardr doctor --try")+" makes one real call to each")
	for i := 2; i < store.MaxStartFails; i++ {
		gone()
	}
	wantText(t, cmd("crew", "list"), "failed (gave up after 5 failed starts; yardr crew start yardmaster tries again)")
	wantText(t, cmd("crew", "show", "yardmaster"), "gave up after 5 failed starts; yardr crew start yardmaster tries again, the last with: "+reason)
	wantText(t, cmd("doctor"), "FAULT  yardmaster  yardmaster is not running: the daemon gave up after 5 tries, the last with: "+reason)
}

// The depots a launchd agent cannot read are named once the daemon runs as a
// login service, on macOS, as a note and never as a fault.
func TestDoctorLoginServiceDepots(t *testing.T) {
	user := "/Users/jo"
	depots := []store.Depot{{Name: "a", Path: user + "/Documents/a"}, {Name: "b", Path: user + "/projects/b"},
		{Name: "c", Path: user + "/Desktop"}, {Name: "d", Path: user + "/Downloads/x/d"}, {Name: "e", Path: user + "/Documents2/e"}, {Name: "f"}}
	if got := protectedDepots(depots, user); !slices.Equal(got, []string{"a (" + user + "/Documents/a)", "c (" + user + "/Desktop)", "d (" + user + "/Downloads/x/d)"}) {
		t.Fatalf("protected = %v", got)
	}

	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	for _, r := range depots[:2] {
		if err := st.PutDepot(ctx, r, "me"); err != nil {
			t.Fatal(err)
		}
	}
	stub(t, &userHomeDir, func() (string, error) { return user, nil })
	stub(t, &goos, "darwin")
	note := "note            login service: depot a (" + user + "/Documents/a) is in a folder macOS protects"
	// No service: a terminal's daemon has the terminal's access.
	if out, _ := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor"); strings.Contains(out, note) || !strings.Contains(out, "not configured  login service") {
		t.Fatalf("without the service:\n%s", out)
	}
	stub(t, &loginService, func(*config.Config) bool { return true })
	out, _ := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	wantText(t, out, "configured      login service: the daemon starts at login", note, "a launchd agent needs its own access there")
	if strings.Contains(out, "depot b (") || strings.Contains(out, "FAULT  login") {
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

// Beside the login-service lines, doctor's Optional part has one line for
// herdr's server: whether a login service starts it, yardr's own or one
// that is not yardr's, and the command that installs it when none does. The
// line is there with or without a yard that answers, since it is read from
// the service files alone.
func TestDoctorHerdrLoginService(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	st := storetest.New(t)
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	out, _ := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	wantText(t, out, herdrLoginLine(),
		"not configured  login service for herdr's server: herdr's server does not start at login",
		here("yardr up --login")+" installs its login service and the daemon's",
		"docs/install.md, \"Keep it running after a reboot\"")
	if strings.Contains(out, "installs nothing for herdr") {
		t.Errorf("doctor still says yardr installs nothing for herdr:\n%s", out)
	}
	rep, _ := doctorJSON(t, st)
	var found *optionalPart
	for i := range rep.Optional {
		if rep.Optional[i].Key == "herdr_login_service" {
			found = &rep.Optional[i]
		}
	}
	if found == nil || found.Configured || found.Skipped || found.Title != "login service for herdr's server" ||
		!strings.Contains(found.Detail, "up --login") {
		t.Fatalf("herdr part = %+v", rep.Optional)
	}
	// On a yard whose server is not running the line stands as it does:
	// beside the login service, not skipped with the store's parts.
	stopped := stoppedYard(context.Background(), &config.Config{Home: config.Getenv("HOME")})
	if got := stopped.Optional[len(stopped.Optional)-1]; got.Key != "herdr_login_service" || got.Skipped {
		t.Fatalf("stopped yard ends in %+v", stopped.Optional)
	}

	// A service file of the person's own, under any name, is found by what
	// it runs; yardr's own by its first line.
	dir, _ := stubServices(t, "linux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(dir, "panes.service")
	if err := os.WriteFile(own, []byte("[Service]\nExecStart=/usr/local/bin/herdr server\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := herdrLoginPart(&config.Config{Home: config.Getenv("HOME")}); !p.Configured || !strings.Contains(p.Detail, "not yardr's") || !strings.Contains(p.Detail, own) {
		t.Fatalf("with a service of the person's own: %+v", p)
	}
	os.Remove(own)
	u, err := herdrUnit("/usr/local/bin/herdr")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(u.Path, []byte(u.render()), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := herdrLoginPart(&config.Config{Home: config.Getenv("HOME")}); !p.Configured || !strings.Contains(p.Detail, "herdr's server starts at login ("+u.Path+")") {
		t.Fatalf("with yardr's own: %+v", p)
	}
}

// A yard of dir depots lands nothing: no landing step, no route for approved,
// and its last step is a bead through to done.
func TestSetupStepsDirDepot(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	depot := store.Depot{Name: "docs", Path: dir, Kind: store.DepotDir}
	if err := st.PutDepot(ctx, depot, "me"); err != nil {
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
	if !slices.Equal(keys, []string{stepDepot, stepDepotFiles, stepBuilders, stepReviewers, stepRoutes, stepFirstBead, stepMerged}) {
		t.Fatalf("keys = %v", keys)
	}
	if n := nextStep(steps); n.Key != stepDepotFiles || n.Next != "yardr depot init docs" {
		t.Fatalf("next = %+v", n)
	}
	ownDepotFiles(t, depot)
	steps, err = setupSteps(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if !steps[1].Done || steps[4].Title != "routes for new and review" || steps[6].Title != "a first bead through to the flow's end" {
		t.Fatalf("steps = %+v", steps)
	}
	// The steps of no yard are those of a yard with a repository to come.
	if none := noSetup(); len(none) != 8 || none[4].Key != stepLanding || nextStep(none).Key != stepDepot {
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
func (brokenStore) Depots(context.Context) ([]store.Depot, error) {
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
	wantText(t, out, "ok     git ", "FAULT  server      cannot be reached: server gone", "1 fault")
	if strings.Contains(out, "Setup") || strings.Contains(out, "daemon") {
		t.Errorf("nothing else of the yard is known:\n%s", out)
	}
}

// doctor says what is wrong and starts nothing (yardr-sb74): it started a
// stopped server to read the yard, and reported that as ok. On a yard whose
// server is stopped it leaves it stopped and says so as a note, checks the
// machine and the daemon, lists what only the store knows as skipped, and
// exits 0 when nothing it could check is wrong. --try is refused: which
// harnesses the yard runs on is in the store.
func TestDoctorLeavesAStoppedServerStopped(t *testing.T) {
	home := newHome(t)
	cfg := &config.Config{Home: home}
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers, "grok": toolFails})
	// The store of a yard, which nobody serves, and its daemon.
	holdDaemon(t, home)
	if err := os.WriteFile(cfg.DBPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stub(t, &connect, func(context.Context, *config.Config) (store.API, error) {
		t.Error("doctor connected to the store, which starts a server")
		return nil, errors.New("no server in this test")
	})
	before, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}

	var code int
	out := capture(t, func() { code = execute(&app{}, []string{"doctor"}) })
	if code != 0 {
		t.Fatalf("a stopped server is no fault: exit %d:\n%s", code, out)
	}
	wantText(t, out, "yardr doctor: the yard in "+home+"\n",
		"  ok     git ", "  ok     herdr ",
		"claude, a harness on this machine; which ones the yard runs on is not known while its server is not running: answers its version flag",
		// A harness that does not work may be one the yard does not run on.
		"  note   grok ",
		"\nThe yard\n  note   server      not running ("+here("yardr server start")+", or any command but status and doctor, starts it)\n"+
			"  ok     daemon      running\n"+
			"  note   yardmaster  skipped: the server is not running\n"+
			"  note   crew        skipped: the server is not running\n"+
			"  note   held        skipped: the server is not running\n",
		"\nSetup\n  note   skipped: the server is not running\n",
		"  not configured  web view: not running",
		"  skipped         hooks: the server is not running\n",
		"  skipped         peers: the server is not running\n",
		"  skipped         packs: the server is not running\n",
		"  not configured  login service: ",
		"\nNothing that was checked is missing; what the yard's store holds was not checked (skipped above).\n")
	for _, not := range []string{"FAULT", "this command started", "Next step", "The setup is complete", "--try", "Nothing the yard needs"} {
		if strings.Contains(out, not) {
			t.Errorf("doctor says %q:\n%s", not, out)
		}
	}

	rep, err := doctorJSON(t, nil)
	if err != nil || !rep.Yard || rep.State == nil || rep.State.Server != "not_running" || !rep.State.Daemon || rep.State.Yardmaster != nil ||
		len(rep.Faults) != 0 || len(rep.Steps) != 0 || rep.Next != nil || !slices.Equal(rep.Skipped, storeChecks) {
		t.Fatalf("json: %v %+v", err, rep)
	}
	// The config notes read the store (configNotes), so a stopped yard
	// skips that check too, as docs/doctor.md says.
	if !slices.Contains(rep.Skipped, "config") {
		t.Errorf("stopped yard skips no config check: %v", rep.Skipped)
	}
	for _, p := range rep.Optional {
		if store := slices.Contains([]string{"hooks", "sources", "peers", "packs", "limits"}, p.Key); p.Skipped != store {
			t.Errorf("optional %s: skipped = %v", p.Key, p.Skipped)
		}
	}

	// --try needs the store: refused with why, nothing tried.
	out = capture(t, func() { code = execute(&app{}, []string{"doctor", "--try", "--json"}) })
	if code != 1 || !strings.Contains(out, "doctor --try needs the yard's server, which is not running") ||
		!strings.Contains(out, here("yardr server start")) || strings.Contains(out, "tried") {
		t.Fatalf("--try: exit %d:\n%s", code, out)
	}

	// No server afterwards: no socket, no pidfile, no log, nothing new in
	// the home at all.
	for _, p := range []string{cfg.Socket(), cfg.PIDFile(), cfg.LogFile()} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("doctor left %s: a server was started", p)
		}
	}
	after, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("the home changed: %v, was %v", after, before)
	}
	if server.Running(context.Background(), cfg) {
		t.Error("a server answers on the home")
	}

	// What is wrong without the store is still a fault: no daemon.
	other := newHome(t)
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile((&config.Config{Home: other}).DBPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out = capture(t, func() { code = execute(&app{}, []string{"doctor"}) })
	if code != 1 {
		t.Fatalf("no daemon: exit %d:\n%s", code, out)
	}
	wantText(t, out, "  note   server      not running", "  FAULT  daemon      not running", "\n1 fault: ")
}

// Stand-ins for herdr, by what `herdr agent list` does in each: a server
// that answers, no server at the socket (herdr's own error object, on
// stderr), and something else going wrong.
const (
	herdrServes   = "#!/bin/sh\necho '{\"id\":\"cli:x\",\"result\":{\"agents\":[]}}'\n"
	herdrNoServer = "#!/bin/sh\necho '{\"id\":\"cli:x\",\"error\":{\"code\":\"server_not_running\",\"message\":\"no herdr server is running\"}}' >&2\nexit 1\n"
	herdrBroken   = "#!/bin/sh\necho 'config.toml: bad key' >&2\nexit 2\n"
)

// Found live (yardr-aa7y): herdr was installed and its server was not
// running, doctor said `ok herdr`, and the yardmaster failed with herdr's
// server_not_running. herdr on PATH with no server is a fault that says how
// to start one; a herdr that cannot be asked is a note, not a fault.
func TestDoctorHerdrServer(t *testing.T) {
	newHome(t)
	stub(t, &herdrServer, askHerdrServer)
	st := storetest.New(t)
	dir := fakeTools(t, map[string]string{"git": toolAnswers, "herdr": herdrNoServer, "claude": toolAnswers})
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	herdr := filepath.Join(dir, "herdr")
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "FAULT  herdr       "+herdr+": on PATH, but its server is not running (server_not_running: no herdr server is running): "+
		"the daemon cannot start agents until it is. Start it: run herdr in a terminal (ctrl+b q detaches, the server stays); "+
		"on a machine without a desktop, run `herdr server`, its headless server, as a service (docs/install.md, \"herdr's server\")")
	rep, _ := doctorJSON(t, st)
	var got *serverCheck
	for _, n := range rep.Machine {
		if n.Program == "herdr" {
			got = n.Server
		} else if n.Server != nil {
			t.Errorf("%s was asked for a server: %+v", n.Program, n.Server)
		}
	}
	if got == nil || got.State != herdrDown || !slices.ContainsFunc(rep.Faults, func(f string) bool { return strings.HasPrefix(f, "herdr: ") }) {
		t.Fatalf("server = %+v, faults = %v", got, rep.Faults)
	}

	// A server that answers.
	scripttest.Write(t, herdr, herdrServes)
	out, _ = run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	wantText(t, out, "ok     herdr       "+herdr+", its server is running")

	// Anything else is not known to be a server that is down.
	scripttest.Write(t, herdr, herdrBroken)
	out, _ = run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	wantText(t, out, "note   herdr       "+herdr+": on PATH; whether its server is running could not be asked: ", "config.toml: bad key")
	if strings.Contains(out, "FAULT  herdr") {
		t.Errorf("a herdr that cannot be asked is no fault:\n%s", out)
	}
}

// tryTool is a stand-in harness: it answers its version flag, and on its
// one-shot form writes its arguments, how many files its directory holds
// and whether stdin is at its end to the file called, as one line, and its
// directory as a second, then does what real says.
func tryTool(called, real string) string {
	return "#!/bin/sh\n[ \"$1\" = --version ] && { echo 'tool 1.2.3'; exit 0; }\n" +
		"read line || stdin=closed\necho \"args: $*; files: $(ls -A | wc -l | tr -d ' '); stdin: $stdin\ndir: $(pwd -P)\" >>" + called + "\n" + real + "\n"
}

// Found live (yardr-aa7y): Codex was on PATH, answered its version flag and
// said it was logged in, and a real call failed on a spent token, so the
// yardmaster sat at a login screen. doctor --try makes that call and says what
// the harness printed; without --try no harness is called, and one line
// says so.
func TestDoctorTry(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	called := filepath.Join(t.TempDir(), "called")
	tools := map[string]string{"git": toolAnswers, "herdr": toolAnswers,
		"claude": tryTool(called, "echo 'some banner'; echo ok"),
		"codex": tryTool(called, "echo 'model: gpt' >&2\necho 'ERROR: refresh token was already used' >&2\n"+
			"echo 'Please log out and sign in again.' >&2\nexit 1"),
		"kimi":         tryTool(called, "echo ok"),
		"cursor-agent": tryTool(called, "echo ok"),
		"pi":           toolFails}
	fakeTools(t, tools)
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]string{"b": "codex", "c": "cursor", "k": "kimi", "p": "pi"} {
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": kind}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	// Two reports with --try, for one timeout is not right for both (as in
	// TestDoctorVersionCheck): a call that answers or fails needs one long
	// enough to do so on any machine, where a script's start alone took
	// over 2s (yardr-r4q3), and a call that hangs costs all of it. First
	// the ones that answer or fail, kimi's among them.
	stub(t, &tryTimeout, fakeToolsTimeout)
	stub(t, &tryHarnessCall, realTry)
	// The calls make their directories in a temp directory of this test's
	// own. In the one every go test on the machine shares, the directory of
	// another run that is in the middle of this test looked like one left
	// behind (yardr-1abw): two runs at once, and both were red.
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)

	// Without --try: nothing is called, and the report says what it did
	// not do.
	out, _ := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	wantText(t, out, "note   harness     on PATH is not \"works\": no harness was asked for an answer. "+
		here("yardr doctor --try")+" makes one real call to each (it costs a model call per harness)")
	if strings.Contains(out, "tried") {
		t.Errorf("without --try nothing is tried:\n%s", out)
	}
	rep, _ := doctorJSON(t, st)
	for _, n := range rep.Machine {
		if n.Try != nil {
			t.Errorf("%s was tried without --try: %+v", n.Program, n.Try)
		}
	}
	if b, err := os.ReadFile(called); err == nil || rep.Tried {
		t.Fatalf("a harness was called without --try (tried %v):\n%s", rep.Tried, b)
	}

	out, err = run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor", "--try")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out,
		"ok     claude      tried: one real call answered (ok)",
		"FAULT  codex       tried: one real call failed: exit status 1: model: gpt / ERROR: refresh token was already used / Please log out and sign in again.",
		"ok     kimi        tried: one real call answered (ok)",
		"note   cursor-agent  not tried: yardr knows no one-shot call for cursor",
		"FAULT  pi          tried: one real call failed: exit status 3: broken install")
	if strings.Contains(out, "no harness was asked") {
		t.Errorf("with --try the harnesses were asked:\n%s", out)
	}
	// Each was called once, in its one-shot form, with stdin closed, in a
	// directory with nothing in it; the one with no form was not.
	b, _ := os.ReadFile(called)
	for want, n := range map[string]int{"args: -p " + tryPrompt: 2, "args: exec --skip-git-repo-check " + tryPrompt: 1} {
		if strings.Count(string(b), want+"; files: 0; stdin: closed\n") != n {
			t.Errorf("calls lack %d of %q:\n%s", n, want, b)
		}
	}
	if n := strings.Count(string(b), "args: "); n != 3 {
		t.Errorf("%d calls, want 3 (claude, codex, kimi):\n%s", n, b)
	}
	// Each had a directory of its own, where this test looks for them, and
	// none is left.
	dirs := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if dir, ok := strings.CutPrefix(line, "dir: "); ok {
			dirs[dir] = true
			if !strings.HasPrefix(dir, filepath.Join(tmp, "yardr-try")) {
				t.Errorf("a call ran in %s, want a directory yardr-try* in %s", dir, tmp)
			}
		}
	}
	if len(dirs) != 3 {
		t.Errorf("the calls ran in %d directories, want 3: %v", len(dirs), dirs)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "yardr-try*")); len(left) > 0 {
		t.Errorf("the directories of the calls were left: %v", left)
	}
	rep, _ = doctorJSON(t, st, "--try")
	states := map[string]tryCheck{}
	for _, n := range rep.Machine {
		if n.Try != nil {
			states[n.Harness] = *n.Try
		}
	}
	if !rep.Tried || states["claude"] != (tryCheck{State: tryOK, Output: "ok"}) || states["codex"].State != tryFails || states["kimi"].State != tryOK ||
		states["cursor"].State != tryNotTried || states["pi"].State != tryFails {
		t.Fatalf("tried %v, states = %+v", rep.Tried, states)
	}
	if !slices.ContainsFunc(rep.Faults, func(f string) bool { return strings.HasPrefix(f, "codex: tried: one real call failed") }) {
		t.Errorf("faults = %v", rep.Faults)
	}

	// Then a call that hangs, with a timeout it is quick to run out of.
	// Only kimi is looked at: in so short a time the others may hang too.
	// Its output is not asserted because a slow start can time out before
	// the script prints anything.
	tools["kimi"] = tryTool(called, "echo 'thinking'; sleep 30")
	fakeTools(t, tools)
	stub(t, &tryTimeout, 2*time.Second)
	out, err = run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor", "--try")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "FAULT  kimi        tried: one real call did not end within 2s")
	rep, _ = doctorJSON(t, st, "--try")
	if !slices.ContainsFunc(rep.Machine, func(n need) bool { return n.Harness == "kimi" && n.Try != nil && n.Try.State == tryHangs }) {
		t.Fatalf("kimi's call is not one that hangs: %+v", rep.Machine)
	}
}

// loginShellStub is a stand-in login shell: it takes what yardr gives a
// login shell (-l -c <command>), writes that it ran to the file called, and
// runs the command on a PATH with dir in front, as a profile adds one.
func loginShellStub(t *testing.T, called, dir, before string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "loginsh")
	body := "#!/bin/sh\necho \"$*\" >>" + called + "\n[ \"$1\" = -l ] && [ \"$2\" = -c ] || exit 9\n" + before + "\nPATH=" + dir + ":/usr/bin:/bin\neval \"$3\"\n"
	scripttest.Write(t, path, body)
	return path
}

// Found live (yardr-aa7y): claude and codex were in ~/.local/bin, which a
// shell without a terminal did not have on PATH. "not on PATH" was right
// and no help. When the login shell finds the program, the line says where
// and what to do; when it does not, or cannot be asked, the line is as it
// was.
func TestDoctorLoginShellFinds(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(context.Background(), store.Group{Name: "b", Runner: "herdr", Config: map[string]any{"kind": "codex"}}, "me"); err != nil {
		t.Fatal(err)
	}
	// Where a profile's PATH has codex, and this shell's has not.
	local := filepath.Join(t.TempDir(), ".local", "bin")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	scripttest.Write(t, filepath.Join(local, "codex"), toolAnswers)
	called := filepath.Join(t.TempDir(), "called")
	doctor := func() string {
		t.Helper()
		out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
		if !errors.Is(err, errCheckFailed) {
			t.Fatalf("err = %v\n%s", err, out)
		}
		return out
	}
	const plain = "FAULT  codex       not on PATH: the harness of group b. Get it: npm install -g @openai/codex"

	// A profile that prints as it likes does not confuse it.
	sh := loginShellStub(t, called, local, "echo 'Welcome back'; echo /etc/motd")
	stub(t, &loginShell, func() string { return sh })
	out := doctor()
	wantText(t, out, "FAULT  codex       not on PATH: the harness of group b. Found at "+local+"/codex by your login shell, "+
		"which this shell's PATH does not have: add "+local+" to PATH here, or for the daemon's service run `yardr daemon install` from a login shell")
	if strings.Contains(out, "Get it: npm") {
		t.Errorf("what is installed is not to get:\n%s", out)
	}
	// The login shell ran once, with the one program that is missing.
	if b, _ := os.ReadFile(called); string(b) != "-l -c command -v codex\n" {
		t.Errorf("the login shell was run with:\n%s", b)
	}
	rep, _ := doctorJSON(t, st)
	for _, n := range rep.Machine {
		if want := map[string]string{"codex": local + "/codex"}[n.Program]; n.LoginPath != want || n.Program == "codex" && n.Found {
			t.Errorf("%s: login_shell_path %q, want %q (found %v)", n.Program, n.LoginPath, want, n.Found)
		}
	}

	// A login shell that does not find it either, one that hangs, one that
	// is not there and none at all: the line as it was, and no word of it.
	elsewhere := t.TempDir()
	stub(t, &loginShellTimeout, 500*time.Millisecond)
	for name, shell := range map[string]string{
		"does not find it": loginShellStub(t, called, elsewhere, ""),
		"hangs":            loginShellStub(t, called, local, "sleep 30"),
		"is not a shell":   filepath.Join(elsewhere, "gone"),
		"none":             "",
	} {
		stub(t, &loginShell, func() string { return shell })
		if out := doctor(); !strings.Contains(out, plain) || strings.Contains(out, "login shell,") {
			t.Errorf("a login shell that %s:\n%s", name, out)
		}
	}

	// Nothing missing: the login shell is not run at all.
	if err := os.Remove(called); err != nil {
		t.Fatal(err)
	}
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers, "codex": toolAnswers})
	stub(t, &loginShell, func() string { return sh })
	if out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor"); !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out) // the daemon and the yardmaster are still missing
	}
	if b, err := os.ReadFile(called); err == nil {
		t.Errorf("the login shell was run with nothing missing:\n%s", b)
	}
}

// On a home with no yard and no harness on PATH, the one the login shell
// finds is named, where "a harness is not on PATH" sent the person to
// install what they have.
func TestDoctorLoginShellFindsHarnessForNewYard(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers})
	local := t.TempDir()
	scripttest.Write(t, filepath.Join(local, "codex"), toolAnswers)
	sh := loginShellStub(t, filepath.Join(t.TempDir(), "called"), local, "")
	stub(t, &loginShell, func() string { return sh })
	var code int
	out := capture(t, func() { code = execute(&app{}, []string{"doctor"}) })
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	wantText(t, out, "FAULT  codex       not on PATH: a harness a yard can be created on. Found at "+local+"/codex by your login shell", "1 fault")
	if strings.Contains(out, "FAULT  a harness") {
		t.Errorf("a harness is installed:\n%s", out)
	}
}

// A store from before dep add refused cycles may hold one (yardr-9wq1):
// doctor names its beads in a line that is no fault, and dep rm, which
// yardr leaves to the person, ends it.
func TestDoctorBlockerCycle(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	path := filepath.Join(t.TempDir(), "yard.db")
	ctx := context.Background()
	old, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var uuid []string
	for _, id := range []string{"y-a", "y-b"} {
		b, err := old.CreateBead(ctx, store.NewBead{ID: id, Title: id}, "me")
		if err != nil {
			t.Fatal(err)
		}
		uuid = append(uuid, b.UUID)
	}
	if err := old.Close(ctx); err != nil {
		t.Fatal(err)
	}
	storetest.OldBlocks(t, path, uuid[0], uuid[1])
	storetest.OldBlocks(t, path, uuid[1], uuid[0])
	st := storetest.Open(t, path)
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}

	before, _ := doctorJSON(t, st)
	out, _ := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	wantText(t, out, "note   cycle       y-a blocks y-b blocks y-a: these beads wait on each other and are never ready. "+
		"Remove the edge that is wrong: "+here("yardr dep rm <blocker> <bead>"))
	if len(before.State.Cycles) != 1 || slices.ContainsFunc(before.Faults, func(f string) bool { return strings.Contains(f, "cycle") }) {
		t.Fatalf("json: cycles %v, faults %v", before.State.Cycles, before.Faults)
	}
	if out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "dep", "add", "y-a", "y-b"); err == nil || !strings.Contains(err.Error(), "already blocks") {
		t.Fatalf("dep add on the cycle: %v\n%s", err, out)
	}
	if out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "dep", "rm", "y-b", "y-a"); err != nil {
		t.Fatalf("dep rm: %v\n%s", err, out)
	}
	after, _ := doctorJSON(t, st)
	out, _ = run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	if len(after.State.Cycles) != 0 || strings.Contains(out, "cycle") {
		t.Fatalf("after dep rm: cycles %v\n%s", after.State.Cycles, out)
	}
	if ready, err := st.Ready(ctx); err != nil || len(ready) != 1 || ready[0].ID != "y-a" {
		t.Fatalf("ready = %+v, %v", ready, err)
	}
}
