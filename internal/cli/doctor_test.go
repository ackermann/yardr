package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// Fake executables, by what `--version` does in each.
const (
	toolAnswers = "#!/bin/sh\necho \"tool 1.2.3\"\n"
	toolFails   = "#!/bin/sh\necho \"broken install\" >&2\nexit 3\n"
	toolHangs   = "#!/bin/sh\nsleep 30\n"
	// claudeAnswers and codexAnswers are toolAnswers for the two kinds whose
	// file says what the flag must print (version_contains): what the real
	// programs print. A claude or codex that prints toolAnswers' line is
	// another program, which is what the tests of that use it for.
	claudeAnswers = "#!/bin/sh\necho \"2.1.310 (Claude Code)\"\n"
	codexAnswers  = "#!/bin/sh\necho \"codex-cli 0.160.1\"\n"
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

// answeredTools is fakeTools for a test of what doctor says past the
// versions: the tools are found, and every version flag answers at once,
// as toolAnswers would, without starting a program. A test of the versions
// themselves (a failure, a hang, what the flag prints, the timeout) uses
// fakeTools, which runs the real probe against its scripts.
func answeredTools(t *testing.T, found ...string) {
	t.Helper()
	stubLookPath(t, found...)
	stub(t, &probe, func(context.Context, string, string) versionCheck {
		return versionCheck{State: versionAnswers, Output: "tool 1.2.3"}
	})
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
	a := appOn(st)
	a.actor = "me"
	return doctorJSONOn(t, a, flags...)
}

// doctorJSONOn is doctorJSON run on a: for a run with a home of its own
// and no store to name it by.
func doctorJSONOn(t *testing.T, a *app, flags ...string) (doctorReport, error) {
	t.Helper()
	out, err := run(t, a.rootCmd(), append([]string{"doctor", "--json"}, flags...)...)
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
	home := newHomeDir(t)
	requireNonInteractive(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": claudeAnswers, "grok": toolFails})
	// A run of its own each time, in an environment naming the home.
	newApp := func() *app {
		a := testApp(t, nil, home)
		a.actor = "me"
		return a
	}

	out, code := executeOut(newApp(), "doctor")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	wantText(t, out, "no yard in "+home+" yet", hereAt(home, "yardr init --harness <kind>"), "Nothing was created.",
		"ok     git ", "ok     herdr ", "a harness a yard can be created on: answers its version flag (2.1.310 (Claude Code))",
		// A harness nothing runs on yet that does not work is said, and is no fault.
		"note   grok ", "its version flag fails: exit status 3: broken install",
		"todo   a depot: ", "todo   a first merged bead: ", "Nothing the yard needs is missing.")
	if strings.Contains(out, herdrInstall) {
		t.Errorf("doctor says to install herdr when it is already on PATH:\n%s", out)
	}
	if strings.Contains(out, "The yard\n") || strings.Contains(out, "Optional") || strings.Contains(out, "Next step") {
		t.Errorf("a home with no yard has no yard to report on:\n%s", out)
	}
	if strings.Contains(out, "Tell us it landed") {
		t.Errorf("no merged bead, yet doctor says to tell us it landed:\n%s", out)
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
	out, code = executeOut(newApp(), "doctor")
	if code != 1 {
		t.Fatalf("no herdr, no harness: exit %d:\n%s", code, out)
	}
	wantText(t, out, "FAULT  herdr ", "Get it: curl -fsSL https://herdr.dev/install.sh | sh (more: https://herdr.dev)",
		"    curl -fsSL https://herdr.dev/install.sh | sh\n",
		"FAULT  a harness ", "2 faults")
	rep, err := doctorJSONOn(t, newApp())
	if !errors.Is(err, errCheckFailed) || rep.Yard || rep.State != nil || len(rep.Faults) != 2 || len(rep.Steps) == 0 || rep.Steps[0].Done {
		t.Fatalf("json: %v %+v", err, rep)
	}
	if _, err := os.Stat(filepath.Join(home, "yard.db")); err == nil {
		t.Fatal("doctor --json created a store")
	}
	if _, err := os.Stat(home); err == nil {
		t.Fatal("doctor --json created the home directory")
	}

	// A claude that is another program is no harness to create a yard on:
	// it is said, as a note since nothing runs on it yet, and it does not
	// answer for the harness the yard lacks, which is a fault of its own. A
	// window reads the same of it: not found, and what it printed.
	dir := fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	out, code = executeOut(newApp(), "doctor")
	if code != 1 {
		t.Fatalf("a claude that is another program: exit %d:\n%s", code, out)
	}
	wantText(t, out, "note   claude ", filepath.Join(dir, "claude")+", a harness a yard can be created on: on PATH, but it is not Claude Code: "+
		`its version flag printed "tool 1.2.3", which does not say "Claude Code". herdr starts the first claude on PATH, which is this one, `+
		"so a Claude Code installed behind it never runs: take this one off PATH, or put Claude Code's directory before "+dir+". Get it: ",
		"FAULT  a harness ", "1 fault: ")
	if strings.Contains(out, "answers its version flag") {
		t.Errorf("a program that is not Claude Code answers for it:\n%s", out)
	}
	rep, err = doctorJSONOn(t, newApp())
	if !errors.Is(err, errCheckFailed) || len(rep.Faults) != 1 || !strings.HasPrefix(rep.Faults[0], "a harness: ") {
		t.Fatalf("json: %v %+v", err, rep)
	}
	var claude, noHarness *need
	for i, n := range rep.Machine {
		switch n.Program {
		case "claude":
			claude = &rep.Machine[i]
		case "a harness":
			noHarness = &rep.Machine[i]
		}
	}
	if claude == nil || claude.Found || claude.Path != filepath.Join(dir, "claude") || claude.Get == "" || claude.Harness != "claude" ||
		claude.Version == nil || *claude.Version != (versionCheck{State: versionNotHarness, Output: "tool 1.2.3"}) {
		t.Errorf("claude, a program that is not Claude Code: %+v", claude)
	}
	if noHarness == nil || noHarness.Found {
		t.Errorf("the harness a yard lacks: %+v", noHarness)
	}
}

// After init and before anything else: the daemon and the yardmaster are what
// is missing (exit 1), no step is done, and the next one is the depot. The
// store is as it was.
func TestDoctorFreshYard(t *testing.T) {
	home := newHomeDir(t)
	answeredTools(t, "git", "herdr", "claude")
	st := storetest.New(t)
	testYard(t, st, home)
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	ctx := context.Background()
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	before, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a().rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) || exitCode(err) != 1 {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "yardr doctor: the yard in ", "Needed on this machine", "The yard", "Setup", "Optional",
		"the harness of the yard's default, crew member yardmaster: answers its version flag (tool 1.2.3)",
		"FAULT  daemon ", hereAt(home, "yardr init --start"), "FAULT  yardmaster  ", "yardmaster is not running: the daemon starts it",
		"ok     crew ", "ok     held ", "todo   a depot: no repository is a depot of this yard yet",
		"todo   routes for new, review and approved: no route for new, review and approved",
		"not configured  web view: ", "not configured  hooks: ", "not configured  sources: ", "not configured  packs: ", "not configured  login service: ",
		"2 faults", "Next step: a depot.\n  ", hereAt(home, "yardr depot add <depot> <path> --base <branch>"))
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

// dirDepot makes a directory that keeps its own .yardr files
// (ownDepotFiles) and adds it as dir depot web: for a test whose depot is a
// bead's home and nothing more. A git depot would spend three git runs on
// its setup and read its base on nearly every store call after; a dir
// depot's flows are read from the directory as they are on disk.
func dirDepot(t *testing.T, st store.API) store.Depot {
	t.Helper()
	dir := t.TempDir()
	depot := store.Depot{Name: "web", Path: dir, Kind: store.DepotDir}
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

// committedTemplateDir is the repository committedDepot gives every test,
// made once by TestMain: one commit with the depot's own files on main,
// what gitDepot and commitDepot leave. Making it per test spent three git
// runs each; a test gets a copy. Made before the tests run, read only
// after, so no test assigns it.
var committedTemplateDir string

// buildCommittedTemplate makes the repository committedDepot copies per test.
func buildCommittedTemplate() (string, error) {
	var err error
	dir, merr := os.MkdirTemp("", "ycommit")
	if merr != nil {
		return "", merr
	}
	run := func(args ...string) {
		if err != nil {
			return
		}
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		if out, cerr := cmd.CombinedOutput(); cerr != nil {
			err = fmt.Errorf("git %v: %v %s", args, cerr, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err == nil {
		err = copyOwnDepotFiles(dir, "git")
	}
	run("add", "-A")
	run("commit", "-q", "-m", "yardr files")
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// committedDepot is gitDepot committed: depot web with its own .yardr
// files on its base, from the template TestMain built, for a test that
// commits straight after. A test that reads the depot uncommitted first
// keeps gitDepot.
func committedDepot(t *testing.T, st store.API) store.Depot {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(committedTemplateDir)); err != nil {
		t.Fatal(err)
	}
	depot := store.Depot{Name: "web", Path: dir, Base: "main", Kind: store.DepotGit}
	if err := st.PutDepot(context.Background(), depot, "me"); err != nil {
		t.Fatal(err)
	}
	return depot
}

// A yard set up step by step: each step turns done as the store shows it,
// the next step follows, and with the daemon and the yardmaster running and a
// bead merged there is nothing left: exit 0.
func TestDoctorSetUpYard(t *testing.T) {
	home := newHomeDir(t)
	answeredTools(t, "git", "herdr", "claude")
	st := storetest.New(t)
	testYard(t, st, home)
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	ctx := context.Background()
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude", "--operator", "me"); err != nil {
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
	// A yard with a bead but none merged says nothing of telling us.
	mid, _ := run(t, a().rootCmd(), "doctor")
	wantText(t, mid, "todo   a first merged bead: ")
	if strings.Contains(mid, "Tell us it landed") {
		t.Errorf("no merged bead, yet doctor says to tell us it landed:\n%s", mid)
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
	out, err := run(t, a().rootCmd(), "doctor")
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
		"Tell us it landed: mail yardmaster@yardr.dev with \"landed\".",
		// The subscriptions' limits are no step the setup waits for: not a
		// todo, and not done until aiquokka's source is declared.
		"note   the subscriptions' limits, from aiquokka (optional): no source named aiquokka is declared",
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
	out, err = run(t, a().rootCmd(), "doctor")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "note   held        1 held, waiting for someone: "+held.ID, "note   yardmaster  yardmaster is stopped (disabled)")

	// Declaring the source turns the optional step done.
	text, _, _ := source.Known(source.Aiquokka)
	if _, err := st.PutSource(ctx, "aiquokka.yaml", string(text), "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a().rootCmd(), "doctor")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "done   the subscriptions' limits, from aiquokka (optional): source aiquokka", "The setup is complete.")
}

// Where aiquokka is on PATH and the yard declares no source of that name,
// doctor proposes the one command that adds it, whatever other sources the
// yard has; it says nothing where aiquokka is not found, or the source is
// declared.
func TestDoctorOffersAiquokka(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	cfg := &config.Config{Home: t.TempDir()}
	offered := func() []string {
		t.Helper()
		part, err := sourcesPart(ctx, cfg, st, false)
		if err != nil {
			t.Fatal(err)
		}
		return part.Notes
	}
	stubLookPath(t)
	if got := offered(); len(got) != 0 {
		t.Fatalf("with aiquokka off PATH: %q", got)
	}
	stubLookPath(t, "aiquokka")
	want := "aiquokka is on PATH and no source named aiquokka is declared, so the yard does not read what the subscriptions of its agents still carry: " +
		yardrAt(cfg.Home) + " source add aiquokka (docs/integrations.md)"
	if got := offered(); len(got) != 1 || got[0] != want {
		t.Fatalf("with aiquokka on PATH and no source: %q, want %q", got, want)
	}
	// A source of another name is no aiquokka.
	if _, err := st.PutSource(ctx, "queue.yaml", "name: queue\nlimits:\n  command: [/opt/queue/limits]\n", "me"); err != nil {
		t.Fatal(err)
	}
	if got := offered(); len(got) != 1 || got[0] != want {
		t.Fatalf("with another source declared: %q, want %q", got, want)
	}
	text, _, _ := source.Known(source.Aiquokka)
	if _, err := st.PutSource(ctx, "aiquokka.yaml", string(text), "me"); err != nil {
		t.Fatal(err)
	}
	if got := offered(); len(got) != 0 {
		t.Fatalf("with the source declared: %q", got)
	}
}

// A script, people or another yard burns no subscription, so a user named
// on a route to one protects nothing and leaves everyone else's beads waiting
// at its stage (yardr-xg8j). Doctor notes such a route, and one that names
// nobody, which takes the operator's alone (route set leaves it so when it
// moves a route from agents to a script), with the commands that restate it:
// a route's originator is not changed in place. A route already for every
// originator, and a route to agents, named or not, are not noted.
func TestDoctorNotesARouteNarrowedOnAGroupWithoutAgents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := userYard(t)
	y := yardrAt(yardEnv(t, st)["YARDR_HOME"])
	mustAs(t, st, "jo", "user", "add", "hal")
	mustAs(t, st, "jo", "group", "add", "agents", "--runner", "herdr", "--set", "kind=claude")
	mustAs(t, st, "jo", "group", "add", "scripts", "--runner", "exec", "--set", `cmd="true"`)
	mustAs(t, st, "jo", "group", "add", "desk", "--runner", "manual", "--members", "jo")
	route := func(args ...string) string {
		t.Helper()
		return strings.TrimSpace(mustAs(t, st, "jo", append([]string{"route", "add"}, args...)...))
	}
	notes := func() []string {
		t.Helper()
		rep, _ := doctorJSON(t, st)
		return rep.State.Config
	}

	// Not noted: any, by default and by the flag, and agents, named or not.
	route("--stage", "verify", "--group", "scripts")
	route("--stage", "publish", "--group", "scripts", "--originator", "any")
	route("--stage", "new", "--group", "agents", "--originator", "hal")
	route("--stage", "review", "--group", "agents")
	if got := notes(); len(got) != 0 {
		t.Fatalf("notes on routes that take every originator's beads or start agents: %v", got)
	}

	// Noted: a user named on a route to a script or to people, with the
	// route's filters and note to restate; and a route moved from
	// agents to a script, which still names nobody.
	build := route("--stage", "build", "--group", "scripts", "--originator", "hal", "--label", "release", "--note", "hal's own release")
	sign := route("--stage", "sign", "--group", "desk", "--originator", "hal")
	land := route("--stage", "land", "--group", "agents")
	mustAs(t, st, "jo", "route", "set", land, "--group", "scripts")
	why := ", which starts no agents and bills no subscription, so narrowing it saves nothing and strands everyone else's beads at "
	want := []string{
		"route " + build + " (stage build, label release, originator hal) takes only hal's beads to exec group scripts" + why + "build unless another route takes them. " +
			"Change it: " + y + " route add --stage build --label release --group scripts --note 'hal'\\''s own release' --originator any, then " + y + " route rm " + build,
		"route " + sign + " (stage sign, originator hal) takes only hal's beads to manual group desk" + why + "sign unless another route takes them. " +
			"Change it: " + y + " route add --stage sign --group desk --originator any, then " + y + " route rm " + sign,
		"route " + land + " (stage land) takes only the operator's beads to exec group scripts" + why + "land unless another route takes them. " +
			"Change it: " + y + " route add --stage land --group scripts --originator any, then " + y + " route rm " + land,
	}
	got := notes()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("doctor's config notes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	out, _ := as(t, st, "jo", "doctor")
	for _, w := range want {
		if !strings.Contains(out, "note   config") || !strings.Contains(out, w) {
			t.Fatalf("doctor has no note %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "FAULT  config") {
		t.Fatalf("doctor makes a fault of a narrowed route:\n%s", out)
	}

	// What the notes say restates each route as it was, for every
	// originator, and removes the narrow one.
	was := map[string]store.Route{}
	routes, err := st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		was[r.ID] = r
	}
	for _, n := range got {
		_, fix, _ := strings.Cut(n, "Change it: ")
		add, rm, _ := strings.Cut(fix, ", then ")
		for _, cmd := range []string{add, rm} {
			mustAs(t, st, "jo", shellWords(strings.TrimPrefix(cmd, y+" "))...)
		}
	}
	if got := notes(); len(got) != 0 {
		t.Fatalf("notes after the routes were restated as they say: %v", got)
	}
	routes, err = st.Routes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{build, sign, land} {
		old := was[id]
		var restated *store.Route
		for i, r := range routes {
			if r.ID == id {
				t.Errorf("route %s stands after the note removed it", id)
			}
			if r.Stage == old.Stage {
				restated = &routes[i]
			}
		}
		if restated == nil || restated.Originator == nil || *restated.Originator != store.OriginatorAny {
			t.Errorf("stage %s after the restatement: %+v, want a route for any originator", old.Stage, restated)
			continue
		}
		a, b := old, *restated
		a.ID, b.ID, a.Originator, b.Originator = "", "", nil, nil
		wantJSON, _ := json.Marshal(a)
		gotJSON, _ := json.Marshal(b)
		if !bytes.Equal(wantJSON, gotJSON) {
			t.Errorf("stage %s restated as %s, want %s", old.Stage, gotJSON, wantJSON)
		}
	}

	// Another yard burns no subscription either. A group of one needs its
	// link to exist before the store takes it, so this one is handed over
	// whole; and a group that is gone has no runner to read.
	hal := "hal"
	narrow := []store.Route{{ID: "r1", Stage: "build", Group: "at-north", Originator: &hal}}
	if got := narrowedRouteNotes(narrow, []store.Group{{Name: "at-north", Runner: store.PeerRunner}}, "jo", "yardr"); len(got) != 1 ||
		!strings.Contains(got[0], "takes only hal's beads to peer group at-north") {
		t.Errorf("notes on a route narrowed on a peer group: %v", got)
	}
	if got := narrowedRouteNotes(narrow, nil, "jo", "yardr"); len(got) != 0 {
		t.Errorf("notes on a route whose group is gone: %v", got)
	}
}

// shellWords splits a line as shellLine wrote it: words apart by spaces, a
// word in single quotes whole, and a quote inside one as shellQuote writes it.
func shellWords(line string) []string {
	var words []string
	var cur strings.Builder
	inWord, quoted := false, false
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case quoted && c == '\'':
			quoted = false
		case quoted:
			cur.WriteByte(c)
		case c == '\'':
			quoted, inWord = true, true
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			inWord = true
		case c == ' ':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

// A program the yard needs and the machine lacks is a fault with where to
// get it: a harness a group names, like git and herdr themselves.
func TestDoctorMissingDependency(t *testing.T) {
	answeredTools(t, "herdr", "claude")
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
	testYard(t, st, newHomeDir(t))
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude", "--operator", "me"); err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]string{"b": "codex", "c": "cursor"} {
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": kind}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, a().rootCmd(), "doctor")
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
		fakeTools(t, map[string]string{"claude": claudeAnswers})
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
	dir := fakeTools(t, map[string]string{"claude": claudeAnswers, "codex": toolFails, "kimi": toolHangs, "cursor-agent": toolHangs})
	ctx := context.Background()
	check := func(kind string, want versionCheck) {
		t.Helper()
		if got := probe(ctx, kind, harnessNeed(kind, "x").Path); got != want {
			t.Errorf("%s: %+v, want %+v", kind, got, want)
		}
	}
	// These are not about the timeout, and run under the long one fakeTools
	// set. Claude Code's line is what the real program prints: it is the
	// harness, and a false refusal of it would stop a yard from being made.
	check("claude", versionCheck{versionAnswers, "2.1.310 (Claude Code)"})
	check("codex", versionCheck{versionFails, "exit status 3: broken install"})
	check("cursor", versionCheck{versionNotChecked, ""}) // would hang, were it run

	// Who it is: a kind whose file says what the flag prints (claude and
	// codex) has a program of its name taken for the harness only if the
	// output says it, anywhere in it; any program can be called claude.
	// Only a program that answers is told by it: one that fails says nothing
	// of who it is, and a kind with no such text takes any answer.
	as := func(kind, body string, want versionCheck) {
		t.Helper()
		scripttest.Write(t, filepath.Join(dir, store.Executable(kind)), body)
		check(kind, want)
	}
	as("claude", toolAnswers, versionCheck{versionNotHarness, "tool 1.2.3"})
	as("claude", "#!/bin/sh\nexit 0\n", versionCheck{versionNotHarness, ""})
	as("claude", "#!/bin/sh\necho 'a banner'\necho '2.1.310 (Claude Code)'\n", versionCheck{versionAnswers, "a banner"})
	as("claude", "#!/bin/sh\necho '2.1.310 (Claude Code)' >&2\n", versionCheck{versionAnswers, "2.1.310 (Claude Code)"})
	as("claude", "#!/bin/sh\necho '2.1.310 (claude code)'\n", versionCheck{versionNotHarness, "2.1.310 (claude code)"})
	as("claude", "#!/bin/sh\necho 'usage: claude [options]' >&2\nexit 2\n", versionCheck{versionFails, "exit status 2: usage: claude [options]"})
	as("codex", codexAnswers, versionCheck{versionAnswers, "codex-cli 0.160.1"})
	as("codex", toolAnswers, versionCheck{versionNotHarness, "tool 1.2.3"})
	as("kimi", toolAnswers, versionCheck{versionAnswers, "tool 1.2.3"})
	scripttest.Write(t, filepath.Join(dir, "claude"), claudeAnswers)
	scripttest.Write(t, filepath.Join(dir, "kimi"), toolHangs)
	// A path that cannot be run fails, and says why.
	if got := probe(ctx, "claude", filepath.Join(dir, "gone")); got.State != versionFails || !strings.Contains(got.Output, "no such file") {
		t.Errorf("missing executable: %+v", got)
	}
	// A long first line is cut on a character, not in the middle of one.
	if got := versionLine("\n  " + strings.Repeat("─", 90) + "\nmore"); got != strings.Repeat("─", 60)+"…" {
		t.Errorf("versionLine = %q", got)
	}
	// A hang ends on the timeout, the script's sleep with it. The script
	// sleeps 30 s, so the verdict does not depend on how long the timeout
	// is: a short one only hurries it.
	stub(t, &versionTimeout, 100*time.Millisecond)
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
	st := storetest.New(t)
	testYard(t, st, newHomeDir(t))
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	tools := map[string]string{"git": toolAnswers, "herdr": toolAnswers, "kimi": toolAnswers, "codex": toolFails, "cursor-agent": toolHangs,
		"claude": toolAnswers} // a claude that is another program
	fakeTools(t, tools) // and the long timeout again
	if _, err := run(t, a().rootCmd(), "init", "--harness", "kimi", "--operator", "me"); err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]string{"b": "codex", "c": "cursor", "d": "claude"} {
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": kind}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	stub(t, &goos, "darwin")
	out, err := run(t, a().rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "ok     kimi ", "the harness of the yard's default, crew member yardmaster: answers its version flag (tool 1.2.3)",
		"FAULT  codex ", "on PATH, but its version flag fails: exit status 3: broken install",
		"ok     cursor-agent ", "not checked, yardr knows no version flag for cursor",
		// A program that answers as another is no harness the yard can run
		// on, and a fault where a group does: it is said which program it is
		// and what it printed.
		"FAULT  claude ", `the harness of group d: on PATH, but it is not Claude Code: its version flag printed "tool 1.2.3", which does not say "Claude Code". `,
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
	if s := states(); s["kimi"] != versionAnswers || s["codex"] != versionFails || s["cursor"] != versionNotChecked || s["claude"] != versionNotHarness {
		t.Fatalf("states = %v", s)
	}
	for _, n := range func() []need { rep, _ := doctorJSON(t, st); return rep.Machine }() {
		if n.Harness == "codex" && !strings.Contains(n.line().text, "always flags: --no-daemon --config=check_for_update_on_startup=false") {
			t.Fatalf("doctor omitted Codex's always flags: %+v", n.line())
		}
		// What a window reads of a program that is not the harness: not
		// found, though it is on PATH, with where it is, what it printed and
		// where the real one is to be had.
		if n.Harness == "claude" && (n.Found || n.Path == "" || n.Get == "" || n.Version.Output != "tool 1.2.3") {
			t.Fatalf("claude, a program that is not Claude Code: %+v", n)
		}
	}

	// Then the hanging one, with a timeout it is quick to run out of. Only
	// kimi is looked at: in so short a time the others may hang too.
	tools["kimi"] = toolHangs
	fakeTools(t, tools)
	stub(t, &versionTimeout, 100*time.Millisecond)
	out, err = run(t, a().rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "FAULT  kimi ", "on PATH, but it did not answer its version flag within 100ms. On macOS that can be a quarantined binary "+
		"waiting on a Gatekeeper dialog nobody sees: run kimi once in a terminal and answer it")
	if s := states(); s["kimi"] != versionHangs {
		t.Fatalf("states = %v", s)
	}
}

// herdr starts a harness by its name on PATH, so the first program of that
// name is the one that runs, whatever is installed behind it: a claude that
// is another program would shadow a Claude Code fetched into ~/.local/bin if
// that directory comes after its own. Doctor goes by PATH as it is (no
// lookPath here is a stand-in's): with the other program first it names that
// one and finds no harness, and says what keeps the real one from running;
// with the real one first it is the harness.
func TestDoctorFirstOnPath(t *testing.T) {
	home := newHomeDir(t)
	requireNonInteractive(t)
	stub(t, &loginShell, func() string { return "" })
	other, fetched := t.TempDir(), t.TempDir()
	scripttest.Write(t, filepath.Join(other, "claude"), toolAnswers)
	scripttest.Write(t, filepath.Join(fetched, "claude"), claudeAnswers)
	machine := func(path string) map[string]need {
		t.Helper()
		t.Setenv("PATH", path)
		a := testApp(t, nil, home)
		a.actor = "me"
		rep, _ := doctorJSONOn(t, a)
		out := map[string]need{}
		for _, n := range rep.Machine {
			out[n.Program] = n
		}
		return out
	}

	m := machine(other + string(os.PathListSeparator) + fetched)
	claude := m["claude"]
	if claude.Found || claude.Path != filepath.Join(other, "claude") || claude.Version == nil || claude.Version.State != versionNotHarness {
		t.Errorf("the other claude first: %+v", claude)
	}
	if _, none := m["a harness"]; !none {
		t.Errorf("a claude that is another program, with the real one behind it, is a harness the yard has: %v", m)
	}
	if l := claude.line(); l.tag != tagNote || !strings.Contains(l.text, "herdr starts the first claude on PATH, which is this one, so a Claude Code installed behind it never runs: "+
		"take this one off PATH, or put Claude Code's directory before "+other) {
		t.Errorf("its line: %+v", l)
	}

	m = machine(fetched + string(os.PathListSeparator) + other)
	claude = m["claude"]
	if !claude.Found || claude.Path != filepath.Join(fetched, "claude") || claude.Version == nil || *claude.Version != (versionCheck{State: versionAnswers, Output: "2.1.310 (Claude Code)"}) {
		t.Errorf("the real claude first: %+v", claude)
	}
	if _, none := m["a harness"]; none {
		t.Errorf("the real claude first, and the yard lacks a harness: %v", m)
	}
}

// A crew member whose starts fail is a fault, with the reason of the last.
func TestDoctorFailingCrew(t *testing.T) {
	home := newHomeDir(t)
	answeredTools(t, "git", "herdr", "claude")
	st := storetest.New(t)
	testYard(t, st, home)
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	ctx := context.Background()
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude", "--operator", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "scribe", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	stored, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCrewEnabled(ctx, "scribe", true, "", stored[store.OperatorSetting]); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"scribe", "yardmaster"} {
		if err := st.CrewStartFailed(ctx, name, "no workspace", time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, a().rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "FAULT  crew        scribe is not running: 1 failed starts so far, the last with: no workspace",
		"FAULT  yardmaster  yardmaster is not running: 1 failed starts so far, the last with: no workspace",
		hereAt(home, "yardr crew start scribe"))
	if strings.Contains(out, "none failing") {
		t.Errorf("output:\n%s", out)
	}
}

// A crew member at its harness's login screen is a fault (yardr-m519): its
// session runs, and the harness is not logged in. The yardmaster's own line
// still says it runs; this one says the login, with the command that logs
// in, from the kind the handle names first.
func TestDoctorCrewNotLoggedIn(t *testing.T) {
	home := newHomeDir(t)
	answeredTools(t, "git", "herdr", "claude", "kimi")
	st := storetest.New(t)
	testYard(t, st, home)
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	ctx := context.Background()
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude", "--operator", "me"); err != nil {
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
	out, err := run(t, a().rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "ok     yardmaster  yardmaster is running (herdr agent y-yardmaster)",
		"FAULT  crew        yardmaster is not logged in: run claude in another terminal and log in there",
		"FAULT  crew        scribe is not logged in: run kimi in another terminal and log in there",
		hereAt(home, "yardr crew restart scribe"))
	var rep struct {
		State struct {
			Login []struct {
				Name, Kind, Line string
			} `json:"login"`
		} `json:"state"`
	}
	out, err = run(t, a().rootCmd(), "doctor", "--json")
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

// While a daemon_lost event stands with no start after it, the crew
// displays read unknown since the daemon's last pass: crew list, crew
// show and doctor's own yardmaster line, instead of a stale running. A
// start ends the state, and the stored state reads again.
func TestDoctorDaemonLost(t *testing.T) {
	home := newHomeDir(t)
	answeredTools(t, "git", "herdr", "claude")
	st, own := storetest.NewOwned(t)
	testYard(t, st, home)
	ctx := context.Background()
	cmd := func(args ...string) string {
		t.Helper()
		a := appOn(st)
		a.actor = "me"
		out, err := run(t, a.rootCmd(), args...)
		if err != nil && !errors.Is(err, errCheckFailed) {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}
	cmd("init", "--harness", "claude")
	if err := st.CrewStarted(ctx, "yardmaster", yardmasterHandle()); err != nil {
		t.Fatal(err)
	}
	stopped := time.Now().Add(-time.Hour)
	if _, _, err := own.NoteDaemonLost(ctx, stopped); err != nil {
		t.Fatal(err)
	}
	want := store.UnknownSince(stopped)
	wantText(t, cmd("crew", "list"), "yardmaster", want)
	wantText(t, cmd("crew", "show", "yardmaster"), want)
	wantText(t, cmd("doctor"), "note   yardmaster  yardmaster is "+want+": the daemon's last pass ended then")
	if err := own.LogDispatch(ctx, "", "daemon_started", "daemon", map[string]any{"to": "new"}); err != nil {
		t.Fatal(err)
	}
	if out := cmd("crew", "list"); strings.Contains(out, want) || !strings.Contains(out, "running") {
		t.Errorf("after a start the crew reads:\n%s", out)
	}
}

// Found live (yardr-05dd): a yardmaster whose harness exits at once is given up
// like any crew member, but it is the one who hears crew_gave_up, so doctor
// is where it is said, with how long the last session lived; crew list
// and show give the count, and the next try while there is one.
func TestDoctorYardmasterGivenUpAfterSessionsGoneAtOnce(t *testing.T) {
	home := newHomeDir(t)
	answeredTools(t, "git", "herdr", "claude")
	st := storetest.New(t)
	testYard(t, st, home)
	ctx := context.Background()
	cmd := func(args ...string) string {
		t.Helper()
		a := appOn(st)
		a.actor = "me"
		out, err := run(t, a.rootCmd(), args...)
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
		"note   harness     on PATH is not \"works\"", hereAt(home, "yardr doctor --try")+" makes one real call to each")
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

	answeredTools(t, "git", "herdr", "claude")
	st := storetest.New(t)
	testYard(t, st, newHomeDir(t))
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	ctx := context.Background()
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
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
	if out, _ := run(t, a().rootCmd(), "doctor"); strings.Contains(out, note) || !strings.Contains(out, "not configured  login service") {
		t.Fatalf("without the service:\n%s", out)
	}
	stub(t, &loginService, func(*config.Config) bool { return true })
	out, _ := run(t, a().rootCmd(), "doctor")
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
	if out, _ := run(t, a().rootCmd(), "doctor"); strings.Contains(out, "macOS protects") {
		t.Errorf("off macOS:\n%s", out)
	}
}

// Beside the login-service lines, doctor's Optional part has one line for
// herdr's server: whether a login service starts it, yardr's own or one
// that is not yardr's, and the command that installs it when none does. The
// line is there with or without a yard that answers, since it is read from
// the service files alone.
func TestDoctorHerdrLoginService(t *testing.T) {
	home := newHomeDir(t)
	answeredTools(t, "git", "herdr", "claude")
	st := storetest.New(t)
	testYard(t, st, home)
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	out, _ := run(t, a().rootCmd(), "doctor")
	wantText(t, out, herdrLoginPart(&config.Config{Home: home}).line(),
		"not configured  login service for herdr's server: herdr's server does not start at login",
		hereAt(home, "yardr up --login")+" installs its login service and the daemon's",
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
	stopped := stoppedYard(context.Background(), &config.Config{Home: home})
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
	if p := herdrLoginPart(&config.Config{Home: home}); !p.Configured || !strings.Contains(p.Detail, "not yardr's") || !strings.Contains(p.Detail, own) {
		t.Fatalf("with a service of the person's own: %+v", p)
	}
	os.Remove(own)
	u, err := herdrUnit(nil, "/usr/local/bin/herdr")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(u.Path, []byte(u.render()), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := herdrLoginPart(&config.Config{Home: home}); !p.Configured || !strings.Contains(p.Detail, "herdr's server starts at login ("+u.Path+")") {
		t.Fatalf("with yardr's own: %+v", p)
	}
}

// A yard of dir depots lands nothing: no landing step, no route for approved,
// and its last step is a bead through to done.
func TestSetupStepsDirDepot(t *testing.T) {
	t.Parallel()
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
	if !slices.Equal(keys, []string{stepDepot, stepDepotFiles, stepBuilders, stepReviewers, stepRoutes, stepLimits, stepFirstBead, stepMerged}) {
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
	if !steps[1].Done || steps[4].Title != "routes for new and review" || steps[7].Title != "a first bead through to the flow's end" {
		t.Fatalf("steps = %+v", steps)
	}
	// A dir depot's flow can finish, but no bead was landed.
	steps[7].Done = true
	var out strings.Builder
	(&doctorReport{Steps: steps}).print(&out)
	if strings.Contains(out.String(), "Tell us it landed") {
		t.Fatalf("a dir depot landed nothing, yet doctor says to tell us it landed:\n%s", out.String())
	}
	// The steps of no yard are those of a yard with a repository to come.
	if none := noSetup(); len(none) != 9 || none[4].Key != stepLanding || nextStep(none).Key != stepDepot {
		t.Fatalf("no yard: %+v", none)
	}
}

// The onboarding asks about the same programs with the same words, and
// says where to get them. herdr's command is a line of its own, to be copied
// whole, and the screen ends by saying how to come back once it is installed;
// git's page is one line, as it was; and with herdr found the screen says
// nothing of herdr.
func TestPreflightSaysWhereToGet(t *testing.T) {
	stubAsk(t, func(...huh.Field) error { return nil })
	stubLookPath(t)
	needs := basicNeeds()
	herdr, git := needs[0], needs[1]
	if herdr.Program != "herdr" || git.Program != "git" || herdr.Found || git.Found {
		t.Fatalf("needs = %+v, %+v", herdr, git)
	}
	herdrLines := "herdr is not on PATH: " + herdr.For + ".\n" +
		"  Get it with this command (more: https://herdr.dev):\n\n" +
		"    curl -fsSL https://herdr.dev/install.sh | sh\n\n"
	gitLines := "git is not on PATH: " + git.For + ".\n  Get it: " + git.Get + "\n\n"
	const again = "Once herdr is installed, run yardr again.\n\n"
	for _, tc := range []struct {
		name    string
		found   []string
		want    string
		aborted bool // the answer the form starts at is to stop
	}{
		{"no herdr", []string{"git"}, herdrLines + again, true},
		{"neither", nil, herdrLines + gitLines + again, true},
		{"no git", []string{"herdr"}, gitLines, false},
		{"both", []string{"git", "herdr"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubLookPath(t, tc.found...)
			var buf bytes.Buffer
			err := preflight(context.Background(), &buf)
			if buf.String() != tc.want || errors.Is(err, huh.ErrUserAborted) != tc.aborted || (err != nil) != tc.aborted {
				t.Fatalf("preflight said\n%s\nwant\n%s\nand the answer to stop is %v; it returned %v", buf.String(), tc.want, tc.aborted, err)
			}
		})
	}

	// A command Get does not start with is not taken out of it.
	if got := (need{Get: "see https://example.com", cmd: "curl example.com"}).getLines(); got != "  Get it: see https://example.com\n" {
		t.Errorf("a command that is not in Get: %q", got)
	}

	// doctor's line for the same program carries the same command.
	line := herdr.line()
	if line.tag != tagFault || !strings.Contains(line.text, herdr.For) ||
		!strings.Contains(line.text, "Get it: curl -fsSL https://herdr.dev/install.sh | sh (more: https://herdr.dev)") {
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
	answeredTools(t, "git", "herdr", "claude")
	a := testApp(t, brokenStore{storetest.New(t)}, newHomeDir(t))
	a.actor = "me"
	out, err := run(t, a.rootCmd(), "doctor")
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
	home := newHomeDir(t)
	// A run of its own each time, in an environment naming the home.
	newApp := func() *app {
		a := testApp(t, nil, home)
		a.actor = "me"
		return a
	}
	cfg := &config.Config{Home: home}
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": claudeAnswers, "grok": toolFails})
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

	out, code := executeOut(newApp(), "doctor")
	if code != 0 {
		t.Fatalf("a stopped server is no fault: exit %d:\n%s", code, out)
	}
	wantText(t, out, "yardr doctor: the yard in "+home+"\n",
		"  ok     git ", "  ok     herdr ",
		"claude, a harness on this machine; which ones the yard runs on is not known while its server is not running: answers its version flag",
		// A harness that does not work may be one the yard does not run on.
		"  note   grok ",
		"\nThe yard\n  note   server      not running ("+hereAt(home, "yardr server start")+", or any command but status and doctor, starts it)\n"+
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

	rep, err := doctorJSONOn(t, newApp())
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
	out, code = executeOut(newApp(), "doctor", "--try", "--json")
	if code != 1 || !strings.Contains(out, "doctor --try needs the yard's server, which is not running") ||
		!strings.Contains(out, hereAt(home, "yardr server start")) || strings.Contains(out, "tried") {
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
	other := newHomeDir(t)
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile((&config.Config{Home: other}).DBPath(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	otherApp := testApp(t, nil, other)
	otherApp.actor = "me"
	out, code = executeOut(otherApp, "doctor")
	if code != 1 {
		t.Fatalf("no daemon: exit %d:\n%s", code, out)
	}
	wantText(t, out, "  note   server      not running", "  FAULT  daemon      not running", "\n1 fault: ")
}

// Stand-ins for herdr, by what `herdr agent list` does in each: a server
// that answers, no server at the socket (herdr's own error object, on
// stderr), and something else going wrong. herdr073 is herdr 0.7.3 with
// no server: it answers its version flag, and says what went wrong in its
// own words rather than in an error object.
const (
	herdrServes   = "#!/bin/sh\necho '{\"id\":\"cli:x\",\"result\":{\"agents\":[]}}'\n"
	herdrNoServer = "#!/bin/sh\necho '{\"id\":\"cli:x\",\"error\":{\"code\":\"server_not_running\",\"message\":\"no herdr server is running\"}}' >&2\nexit 1\n"
	herdrBroken   = "#!/bin/sh\necho 'config.toml: bad key' >&2\nexit 2\n"
	herdr073      = "#!/bin/sh\n[ \"$1\" = --version ] && { echo 'herdr 0.7.3'; exit 0; }\n" +
		"echo 'Error: Os { code: 111, kind: ConnectionRefused, message: \"Connection refused\" }' >&2\nexit 1\n"
)

// Found live (yardr-aa7y): herdr was installed and its server was not
// running, doctor said `ok herdr`, and the yardmaster failed with herdr's
// server_not_running. herdr on PATH with no server is a fault that says how
// to start one; a herdr that cannot be asked is a note, not a fault.
func TestDoctorHerdrServer(t *testing.T) {
	stub(t, &herdrServer, askHerdrServer)
	// A herdr pane names its own session and socket, and herdr's words
	// below are of the one the environment chose: clear both, so the test
	// reads the same in a pane as in CI.
	t.Setenv("HERDR_SOCKET_PATH", "")
	t.Setenv("HERDR_SESSION", "")
	st := storetest.New(t)
	testYard(t, st, newHomeDir(t))
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	dir := fakeTools(t, map[string]string{"git": toolAnswers, "herdr": herdrNoServer, "claude": claudeAnswers})
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	herdr := filepath.Join(dir, "herdr")
	out, err := run(t, a().rootCmd(), "doctor")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	// The yard names no session: herdr's words come with the one the
	// environment chose, and how the yard names its own.
	wantText(t, out, "FAULT  herdr       "+herdr+": on PATH, but its server is not running (herdr is not running in session default, socket ",
		"; if the yard's agents run in another herdr session, name it (herdr session list; yardr settings set herdr_session <name>): "+
			"server_not_running: no herdr server is running): "+
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
	out, _ = run(t, a().rootCmd(), "doctor")
	wantText(t, out, "ok     herdr       "+herdr+", its server is running")

	// Anything else is not known to be a server that is down.
	scripttest.Write(t, herdr, herdrBroken)
	out, _ = run(t, a().rootCmd(), "doctor")
	wantText(t, out, "note   herdr       "+herdr+": on PATH; whether its server is running could not be asked: ", "config.toml: bad key")
	if strings.Contains(out, "FAULT  herdr") {
		t.Errorf("a herdr that cannot be asked is no fault:\n%s", out)
	}

	// A herdr older than yardr starts agents with is a fault that says so
	// and how to update it, where its own words made it one that could not
	// be asked; one new enough is asked for its server as before.
	scripttest.Write(t, herdr, herdr073)
	out, _ = run(t, a().rootCmd(), "doctor")
	wantText(t, out, "FAULT  herdr       "+herdr+": herdr 0.7.3 is older than "+herdrOldest+", the oldest yardr starts agents with. Update it: "+herdrUpdate)
	scripttest.Write(t, herdr, "#!/bin/sh\n[ \"$1\" = --version ] && { echo 'herdr 0.9.4'; exit 0; }\n"+strings.TrimPrefix(herdrNoServer, "#!/bin/sh\n"))
	out, _ = run(t, a().rootCmd(), "doctor")
	wantText(t, out, "FAULT  herdr       "+herdr+": on PATH, but its server is not running (herdr is not running in session default, socket ")
}

// tryTool is a stand-in harness: it answers its version flag with the line
// given, and on its one-shot form writes its arguments, how many files its
// directory holds and whether stdin is at its end to the file called, as one
// line, and its directory as a second, then does what real says.
func tryTool(called, version, real string) string {
	return "#!/bin/sh\n[ \"$1\" = --version ] && { echo '" + version + "'; exit 0; }\n" +
		"read line || stdin=closed\necho \"args: $*; files: $(ls -A | wc -l | tr -d ' '); stdin: $stdin\ndir: $(pwd -P)\" >>" + called + "\n" + real + "\n"
}

// Found live (yardr-aa7y): Codex was on PATH, answered its version flag and
// said it was logged in, and a real call failed on a spent token, so the
// yardmaster sat at a login screen. doctor --try makes that call and says what
// the harness printed; without --try no harness is called, and one line
// says so.
func TestDoctorTry(t *testing.T) {
	home := newHomeDir(t)
	st := storetest.New(t)
	testYard(t, st, home)
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	ctx := context.Background()
	called := filepath.Join(t.TempDir(), "called")
	tools := map[string]string{"git": toolAnswers, "herdr": toolAnswers,
		"claude": tryTool(called, "2.1.310 (Claude Code)", "echo 'some banner'; echo ok"),
		"codex": tryTool(called, "codex-cli 0.160.1", "echo 'model: gpt' >&2\necho 'ERROR: refresh token was already used' >&2\n"+
			"echo 'Please log out and sign in again.' >&2\nexit 1"),
		"kimi":         tryTool(called, "tool 1.2.3", "echo ok"),
		"cursor-agent": tryTool(called, "tool 1.2.3", "echo ok"),
		"pi":           toolFails}
	fakeTools(t, tools)
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude", "--operator", "me"); err != nil {
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
	out, _ := run(t, a().rootCmd(), "doctor")
	wantText(t, out, "note   harness     on PATH is not \"works\": no harness was asked for an answer. "+
		hereAt(home, "yardr doctor --try")+" makes one real call to each (it costs a model call per harness)")
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

	out, err = run(t, a().rootCmd(), "doctor", "--try")
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

	// A program that answers its version flag as another is not the harness,
	// and a call to it would be a call to something else: it is not made,
	// and the report says so beside its line.
	tools["codex"] = tryTool(called, "tool 1.2.3", "exit 1")
	fakeTools(t, tools)
	codexCalls := func() int {
		b, _ := os.ReadFile(called)
		return strings.Count(string(b), "args: exec ")
	}
	was := codexCalls()
	out, err = run(t, a().rootCmd(), "doctor", "--try")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "FAULT  codex ", "on PATH, but it is not Codex: its version flag printed \"tool 1.2.3\", which does not say \"codex-cli\".",
		"note   codex       not tried: it is not Codex")
	if n := codexCalls(); n != was {
		t.Errorf("a program that is not Codex was called: %d calls, was %d", n, was)
	}
	rep, _ = doctorJSON(t, st, "--try")
	for _, n := range rep.Machine {
		if n.Harness == "codex" && (n.Found || n.Try == nil || *n.Try != (tryCheck{State: tryNotTried, Output: "it is not Codex"})) {
			t.Errorf("codex, a program that is not Codex, under --try: %+v", n)
		}
	}

	// Then a call that hangs, with a timeout it is quick to run out of.
	// Only kimi is looked at: in so short a time the others may hang too.
	// Its output is not asserted because a slow start can time out before
	// the script prints anything. The hanging script sleeps 30 s, so any
	// short timeout names the hang; 300 ms keeps this test quick.
	tools["kimi"] = tryTool(called, "tool 1.2.3", "echo 'thinking'; sleep 30")
	fakeTools(t, tools)
	stub(t, &tryTimeout, 300*time.Millisecond)
	out, err = run(t, a().rootCmd(), "doctor", "--try")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantText(t, out, "FAULT  kimi        tried: one real call did not end within 300ms")
	rep, _ = doctorJSON(t, st, "--try")
	if !slices.ContainsFunc(rep.Machine, func(n need) bool { return n.Harness == "kimi" && n.Try != nil && n.Try.State == tryHangs }) {
		t.Fatalf("kimi's call is not one that hangs: %+v", rep.Machine)
	}
}

// loginShellStub is a stand-in login shell: it takes what yardr gives a
// login shell (-l -c <command>), writes that it ran to the file called, and
// runs the command on a PATH of dir alone, the directory a profile adds.
// None of the machine's own directories is on it: a harness installed in
// /usr/bin would be found in every case, those where the login shell is to
// find nothing too.
func loginShellStub(t *testing.T, called, dir, before string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "loginsh")
	body := "#!/bin/sh\necho \"$*\" >>" + called + "\n[ \"$1\" = -l ] && [ \"$2\" = -c ] || exit 9\n" + before + "\nPATH=" + dir + "\neval \"$3\"\n"
	scripttest.Write(t, path, body)
	return path
}

// Found live (yardr-aa7y): claude and codex were in ~/.local/bin, which a
// shell without a terminal did not have on PATH. "not on PATH" was right
// and no help. When the login shell finds the program, the line says where
// and what to do; when it does not, or cannot be asked, the line is as it
// was.
func TestDoctorLoginShellFinds(t *testing.T) {
	st := storetest.New(t)
	testYard(t, st, newHomeDir(t))
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	answeredTools(t, "git", "herdr", "claude")
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude", "--operator", "me"); err != nil {
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
		out, err := run(t, a().rootCmd(), "doctor")
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
	answeredTools(t, "git", "herdr", "claude", "codex")
	stub(t, &loginShell, func() string { return sh })
	if out, err := run(t, a().rootCmd(), "doctor"); !errors.Is(err, errCheckFailed) {
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
	answeredTools(t, "git", "herdr")
	local := t.TempDir()
	scripttest.Write(t, filepath.Join(local, "codex"), toolAnswers)
	sh := loginShellStub(t, filepath.Join(t.TempDir(), "called"), local, "")
	stub(t, &loginShell, func() string { return sh })
	out, code := executeOut(testApp(t, nil, newHomeDir(t)), "doctor")
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
	home := newHomeDir(t)
	answeredTools(t, "git", "herdr", "claude")
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
	testYard(t, st, home)
	a := func() *app {
		a := appOn(st)
		a.actor = "me"
		return a
	}
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}

	before, _ := doctorJSON(t, st)
	out, _ := run(t, a().rootCmd(), "doctor")
	wantText(t, out, "note   cycle       y-a blocks y-b blocks y-a: these beads wait on each other and are never ready. "+
		"Remove the edge that is wrong: "+hereAt(home, "yardr dep rm <blocker> <bead>"))
	if len(before.State.Cycles) != 1 || slices.ContainsFunc(before.Faults, func(f string) bool { return strings.Contains(f, "cycle") }) {
		t.Fatalf("json: cycles %v, faults %v", before.State.Cycles, before.Faults)
	}
	if out, err := run(t, a().rootCmd(), "dep", "add", "y-a", "y-b"); err == nil || !strings.Contains(err.Error(), "already blocks") {
		t.Fatalf("dep add on the cycle: %v\n%s", err, out)
	}
	if out, err := run(t, a().rootCmd(), "dep", "rm", "y-b", "y-a"); err != nil {
		t.Fatalf("dep rm: %v\n%s", err, out)
	}
	after, _ := doctorJSON(t, st)
	out, _ = run(t, a().rootCmd(), "doctor")
	if len(after.State.Cycles) != 0 || strings.Contains(out, "cycle") {
		t.Fatalf("after dep rm: cycles %v\n%s", after.State.Cycles, out)
	}
	if ready, err := st.Ready(ctx); err != nil || len(ready) != 1 || ready[0].ID != "y-a" {
		t.Fatalf("ready = %+v, %v", ready, err)
	}
}

// yardr-2eka: doctor names, one informational line per crew member of kind
// claude, the markdown files its harness's own memory holds for its working
// dir. Every markdown file counts and nothing else; other kinds, empty dirs
// and no home report nothing. The line is a note, never a fault.
func TestDoctorStrayMemory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	mem := func(dir string, files ...string) string {
		t.Helper()
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		path, ok := agents.MemoryDir("claude", home, real)
		if !ok {
			t.Fatal("claude names no memory dir")
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(path, f), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	work := t.TempDir()
	workMem := mem(work, "MEMORY.md", "a-rule.md", "notes.txt")
	if err := os.Mkdir(filepath.Join(workMem, "sub.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	crew := []store.CrewMember{
		{Name: "yardmaster", Dir: work, Config: map[string]any{"kind": "claude"}},
		{Name: "coder", Dir: t.TempDir(), Config: map[string]any{"kind": "codex"}},
		{Name: "empty", Dir: t.TempDir(), Config: map[string]any{"kind": "claude"}},
	}
	got := strayMemoryNotes(home, "", t.TempDir(), crew)
	want := "crew yardmaster: 2 memory files in " + workMem + " that the yard has no memory for; move them with yardr remember"
	if !slices.Equal(got, []string{want}) {
		t.Errorf("notes = %v, want %q", got, want)
	}
	// Claude shares the repository's memory directory across linked
	// worktrees and subdirectories. Doctor must look up the common project
	// path rather than the worktree path.
	repo := t.TempDir()
	gitInDir := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitInDir(repo, "init", "-q", "-b", "main")
	cmd := exec.Command("git", "-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	worktree := filepath.Join(t.TempDir(), "checkout")
	gitInDir(repo, "worktree", "add", "--detach", "-q", worktree, "HEAD")
	subdir := filepath.Join(worktree, "nested")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	realRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := claudeProjectDir(subdir); got != realRepo {
		t.Errorf("claude project for worktree subdirectory = %q, want %q", got, realRepo)
	}
	repoMem := mem(realRepo, "MEMORY.md")
	worktreeNote := strayMemoryNotes(home, "", t.TempDir(), []store.CrewMember{{
		Name: "worktree", Dir: subdir, Config: map[string]any{"kind": "claude"},
	}})
	want = "crew worktree: 1 memory file in " + repoMem + " that the yard has no memory for; move it with yardr remember"
	if !slices.Equal(worktreeNote, []string{want}) {
		t.Errorf("worktree note = %v, want %q", worktreeNote, want)
	}
	// One file reads singular, and a member without a dir runs in the
	// yard home.
	yardHome := t.TempDir()
	yardMem := mem(yardHome, "only.md")
	single := strayMemoryNotes(home, "", yardHome, []store.CrewMember{{Name: "line", Config: map[string]any{"kind": "claude"}}})
	want = "crew line: 1 memory file in " + yardMem + " that the yard has no memory for; move it with yardr remember"
	if !slices.Equal(single, []string{want}) {
		t.Errorf("single = %v, want %q", single, want)
	}
	if got := strayMemoryNotes("", "", yardHome, crew); got != nil {
		t.Errorf("no home reports %v", got)
	}
	for _, l := range (&yardState{Server: serverUp, Daemon: true, StrayMemory: single}).lines(home) {
		if l.tag == tagFault {
			t.Errorf("the note is a fault: %+v", l)
		}
	}
}
