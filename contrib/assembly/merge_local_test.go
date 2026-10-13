package assembly

// These tests run merge-local the way an assembly group does: started by the
// exec runner with the environment it builds, against a yardr server of
// their own (a short YARDR_HOME in a temp dir, for the unix socket path
// limit) and a throwaway repository.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

var yardrBin string

// gitEnv keeps git off the developer's configuration and gives it a name
// to commit as.
var gitEnv = []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t",
	"GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}

func TestMain(m *testing.M) {
	// Nothing from a yardr (or git hook) running these tests may leak into
	// the yardr, git and script they start, which inherit this environment.
	// What stays is how the stores are opened (storetest set it): the
	// yardr of each test is a real one, and its commits need not wait for
	// the disk either.
	noSync := config.Getenv(store.NoSyncEnv)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "YARDR_") || strings.HasPrefix(k, "GIT_") {
			os.Unsetenv(k)
		}
	}
	os.Setenv("YARDR_"+store.NoSyncEnv, noSync)
	// With the home gone from the environment, a yardr started without a
	// test's own would take the default one, which on a machine that works
	// with yardr is the operator's yard, its server answering (yardr-j4vm).
	// So the home is one with no yard in it; each test's yard sets its own
	// over it (newYard).
	nowhere, err := os.MkdirTemp("", "ynone")
	if err != nil {
		panic(err)
	}
	if err := config.UseHome(filepath.Join(nowhere, "y")); err != nil {
		panic(err)
	}
	// Git's identity and configuration are the same for every test, and
	// the exec runner hands its process's environment on to the scripts: set
	// here once, not by each test, the tests can run side by side.
	for _, kv := range gitEnv {
		k, v, _ := strings.Cut(kv, "=")
		os.Setenv(k, v)
	}
	// Each test has a yard, a repository and a server of its own, and most
	// of its time is waiting for the processes it starts. Four at once is
	// what a machine running two more suites still has room for.
	flag.Set("test.parallel", "4")
	dir, err := os.MkdirTemp("", "yrbin")
	if err != nil {
		panic(err)
	}
	yardrBin = filepath.Join(dir, "yardr")
	build := exec.Command("go", "build", "-o", yardrBin, "yardr.dev/yardr")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		panic("building yardr: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.RemoveAll(nowhere)
	os.Exit(code)
}

const flow = `approved -> merged | new | merge
merge -> approved
new -> approved
merged terminal
`

// yard is one isolated yardr with depot r over a fresh repository whose base
// main is checked out in the depot, and bead r-1 in approved on yard/r-1, one
// commit ahead of main, in its worktree.
type yard struct {
	t              *testing.T
	home, repo, wt string
	env            []string
	push           string // YARDR_PUSH for the run
	opts           string // more assignments for the script's cmd, e.g. YARDR_PR=1
	bin            string // the script's YARDR_BIN, when not yardrBin itself
	log            string // what the last run wrote to its group's log
}

func newYard(t *testing.T) *yard {
	t.Helper()
	// A test with a yard shares nothing with another's (TestMain), so it
	// runs beside them from here on.
	t.Parallel()
	home, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Stop by pid (the pidfile), then remove the state.
		if err := server.Stop(&config.Config{Home: home}); err != nil {
			t.Log(err)
		}
		os.RemoveAll(home)
	})
	y := &yard{t: t, home: home, repo: filepath.Join(t.TempDir(), "repo"),
		wt: filepath.Join(home, "worktrees", "r", "r-1")}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")

	// A fresh home has no yard; without a terminal yardr refuses to work
	// in it until it is initialized.
	y.yardr("init", "--harness", "claude", "--dir", home, "--operator", "test")
	y.git(".", "init", "-q", "-b", "main", y.repo)
	y.commit(y.repo, "a.txt", "one\n")
	// The tests are red when the tree has a file RED.
	y.yardr("depot", "add", "r", y.repo, "--base", "main", "--test", "test ! -e RED")
	y.yardrIn(flow, "flow", "set", "f", "-f", "-")
	y.yardr("depot", "flow", "r", "f")
	y.yardr("bead", "create", "work", "--id", "r-1", "-d", "r", "-s", "approved")
	y.git(y.repo, "branch", "yard/r-1")
	y.git(y.repo, "worktree", "add", "-q", y.wt, "yard/r-1")
	return y
}

func (y *yard) run(dir, name string, stdin string, args ...string) string {
	y.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, y.env
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		y.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (y *yard) git(dir string, args ...string) string { return y.run(dir, "git", "", args...) }
func (y *yard) yardr(args ...string) string           { return y.run(y.home, yardrBin, "", args...) }
func (y *yard) yardrIn(stdin string, args ...string) string {
	return y.run(y.home, yardrBin, stdin, args...)
}

func (y *yard) commit(dir, file, content string) string {
	y.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		y.t.Fatal(err)
	}
	y.git(dir, "add", file)
	y.git(dir, "commit", "-q", "-m", file)
	return y.git(dir, "rev-parse", "HEAD")
}

// assembly runs merge-local through the exec runner, as group assembly
// would, and waits for it to exit.
func (y *yard) assembly() { y.exec("merge-local", "assembly", "r-1") }

// exec runs script (in this directory) for bead id through the exec
// runner, as the group would whose cmd it is, and waits for it to exit.
func (y *yard) exec(script, group, id string) {
	y.t.Helper()
	script, err := filepath.Abs(script)
	if err != nil {
		y.t.Fatal(err)
	}
	// Reading the script makes it an input of the test cache: go test does
	// not see files a test only executes, and would keep a stale pass.
	if _, err := os.ReadFile(script); err != nil {
		y.t.Fatal(err)
	}
	logPath := filepath.Join(y.home, group+".log")
	cmd := script
	if y.push != "" {
		cmd = "YARDR_PUSH=" + y.push + " " + script
	}
	if y.opts != "" {
		cmd = y.opts + " " + cmd
	}
	// The script finds this test's fake gh, if it has one, by its command
	// line: the environment is every test's.
	if state, ok := fakeGhs.Load(y.t); ok {
		stateDir := state.(string)
		cmd = "PATH=" + shellQuote(filepath.Join(stateDir, "bin")) + ":$PATH FAKE_GH=" + shellQuote(stateDir) + " " + cmd
	}
	// The runner hands its own environment on: git's identity (TestMain).
	// YARDR_HOME and YARDR_BIN come from the runner itself.
	e := &runner.Exec{Home: y.home, Bin: yardrBin}
	if y.bin != "" {
		e.Bin = y.bin
	}
	ctx := context.Background()
	// The train and base as the dispatcher passes them, read the way bead
	// show --json shows them.
	b, fam := y.beadOf(id), y.familyOf(id)
	// The depot as the store has it: its test command is the script's
	// YARDR_TEST.
	depot, err := rpc.NewClient((&config.Config{Home: y.home}).Socket()).GetDepot(ctx, "r")
	if err != nil {
		y.t.Fatal(err)
	}
	h, err := e.Start(ctx, dispatch.Job{
		Bead:     store.Bead{ID: id, Stage: b.Stage, Type: b.Type},
		Group:    store.Group{Name: group, Config: map[string]any{"cmd": cmd, "log": logPath}},
		Depot:    depot,
		Session:  y.claimFor(ctx, group, id),
		Train:    fam.Train,
		Base:     fam.Base,
		Children: fam.Children,
	})
	if err != nil {
		y.t.Fatal(err)
	}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if alive, _ := e.Alive(ctx, h); !alive {
			break
		}
		if time.Now().After(deadline) {
			_ = e.Stop(ctx, h)
			y.t.Fatalf("%s did not finish", script)
		}
	}
	out, _ := os.ReadFile(logPath)
	y.log = string(out)
	y.t.Logf("%s log:\n%s", group, out)
	_ = os.Remove(logPath)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// claimFor gives bead id to group, as the dispatcher would, so the
// script's moves carry a session that owns it (a move from a session that
// does not is refused as stale). A bead an earlier run left owned (held,
// or not moved) keeps its session.
func (y *yard) claimFor(ctx context.Context, group, id string) store.Session {
	y.t.Helper()
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if err := st.PutGroup(ctx, store.Group{Name: group, Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		y.t.Fatal(err)
	}
	b := y.beadOf(id)
	if b.Session != nil {
		return store.Session{ID: *b.Session}
	}
	sess, err := st.Claim(ctx, b.ID, group, b.Stage, "yard")
	if err != nil {
		y.t.Fatal(err)
	}
	return *sess
}

func (y *yard) bead() store.Bead { return y.beadOf("r-1") }

func (y *yard) beadOf(id string) store.Bead {
	y.t.Helper()
	// bead show --json carries the notes whole under notes, where the
	// bead has their count; take the rest as a bead.
	var b struct {
		store.Bead
		Notes []store.Note `json:"notes"`
	}
	if err := json.Unmarshal([]byte(y.yardr("bead", "show", id, "--json")), &b); err != nil {
		y.t.Fatal(err)
	}
	return b.Bead
}

func (y *yard) family() store.Family { return y.familyOf("r-1") }

func (y *yard) familyOf(id string) store.Family {
	y.t.Helper()
	var f store.Family
	if err := json.Unmarshal([]byte(y.yardr("bead", "show", id, "--json")), &f); err != nil {
		y.t.Fatal(err)
	}
	return f
}

// notes is the bead as prime shows it, notes included.
func (y *yard) notes() string { return y.yardr("prime", "--bead", "r-1") }

// refused is the move_refused events on r-1: moves the yard refused to the
// assembly's session, each a fault to the crew. A script that tries only
// moves the flow allows leaves none.
func (y *yard) refused() []string {
	y.t.Helper()
	var hist []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(y.yardr("bead", "history", "r-1", "--json")), &hist); err != nil {
		y.t.Fatal(err)
	}
	var out []string
	for _, h := range hist {
		if h.Kind == "move_refused" {
			out = append(out, h.Text)
		}
	}
	return out
}

func TestMergeLocalClean(t *testing.T) {
	y := newYard(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	y.git(".", "init", "-q", "--bare", remote)
	y.git(y.repo, "remote", "add", "origin", remote)
	y.push = "origin"
	y.commit(y.wt, "b.txt", "bead\n")
	y.commit(y.repo, "c.txt", "main moved\n") // a rebase is needed, cleanly

	y.assembly()

	b := y.bead()
	if b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	main := y.git(y.repo, "rev-parse", "main")
	if tip := y.git(y.wt, "rev-parse", "HEAD"); tip != main {
		t.Fatalf("main %s is not the rebased branch %s", main, tip)
	}
	if y.git(y.repo, "status", "--porcelain") != "" {
		t.Fatal("the checkout of main is not clean after landing")
	}
	for _, f := range []string{"b.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(y.repo, f)); err != nil {
			t.Fatalf("checkout of main lacks %s: %v", f, err)
		}
	}
	if pushed := y.git(remote, "rev-parse", "main"); pushed != main {
		t.Fatalf("origin main %s, want %s", pushed, main)
	}
	if short := y.git(y.repo, "rev-parse", "--short", "main"); !strings.Contains(y.notes(), "merged "+short+", tested with the command test ! -e RED in ") {
		t.Fatalf("no merged note saying what tested it:\n%s", y.notes())
	}
	// One line of the log has the seconds of each step, in their order.
	if !regexp.MustCompile(`(?m)^== r-1 took: rebase \d+ s, the tests \d+ s, push \d+ s$`).MatchString(y.log) {
		t.Fatalf("no line of the log says how long the rebase, the tests and the push took:\n%s", y.log)
	}
}

// The landing note ends with the seconds the tests took, the log's line has
// the same, and a step that did not run (no rebase, no push) is not in it.
func TestMergeLocalSaysHowLongTheTestsTook(t *testing.T) {
	y := newYard(t)
	y.tests("sleep 1")
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	short := y.git(y.repo, "rev-parse", "--short", "main")
	m := regexp.MustCompile(`(?m)^.*merged ` + short + `, tested with the command sleep 1 in (\d+) s$`).FindStringSubmatch(y.notes())
	if m == nil {
		t.Fatalf("the merged note does not end with the seconds the tests took:\n%s", y.notes())
	}
	if m[1] == "0" {
		t.Fatalf("a test command that sleeps a second took %s s", m[1])
	}
	if want := "== r-1 took: the tests " + m[1] + " s\n"; !strings.Contains(y.log, want) {
		t.Fatalf("the log lacks %q:\n%s", want, y.log)
	}
}

// A failed push is recorded before the landing note's trailing test time.
func TestMergeLocalPushFailureLeavesTestTimeAtEndOfNote(t *testing.T) {
	y := newYard(t)
	y.tests("true")
	y.push = "missing-remote"
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	notes := y.notes()
	if !regexp.MustCompile(`(?m)^.*merged .*, tested with the command true; pushing main to missing-remote failed \(not forced\) in \d+ s$`).MatchString(notes) {
		t.Fatalf("the failed push note does not end with the seconds the tests took:\n%s", notes)
	}
	if !regexp.MustCompile(`(?m)^== r-1 took: the tests \d+ s, push \d+ s$`).MatchString(y.log) {
		t.Fatalf("the log does not time the failed push:\n%s", y.log)
	}
}

// A red run's seconds are in the log too, before the line that says it
// failed.
func TestMergeLocalRedRunSaysHowLongItTook(t *testing.T) {
	y := newYard(t)
	y.commit(y.wt, "RED", "red\n")

	y.assembly()

	if b := y.bead(); b.Stage != "new" {
		t.Fatalf("bead in %s, want back in new", b.Stage)
	}
	if !regexp.MustCompile(`(?m)^== r-1 took: the tests \d+ s\n== r-1 failed$`).MatchString(y.log) {
		t.Fatalf("the log does not say how long the red run took:\n%s", y.log)
	}
}

// A depot with no test command lands untested, and the note says so.
func TestMergeLocalNoTestCommandLandsUntested(t *testing.T) {
	y := newYard(t)
	y.tests("")
	tip := y.commit(y.wt, "RED", "red\n")

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	if main := y.git(y.repo, "rev-parse", "main"); main != tip {
		t.Fatalf("main %s, want the branch tip %s", main, tip)
	}
	if want := "merged " + tip[:7] + ", untested: no test command on the depot"; !strings.Contains(y.notes(), want) {
		t.Fatalf("no note saying %q:\n%s", want, y.notes())
	}
}

// A test command that names a file the base has runs the base's copy of it,
// in the bead's worktree: a bead that rewrites the script to pass is still
// judged by the one that was there, and the note says which copy ran.
func TestMergeLocalRunsTheBasesCopyOfTheTestScript(t *testing.T) {
	y := newYard(t)
	main := y.commit(y.repo, "check.sh", "echo \"the copy on the base, in $(pwd), landing=$YARDR_LANDING\"\ntest ! -e RED\n")
	y.tests("check.sh")
	y.git(y.wt, "merge", "-q", "--ff-only", "main")
	y.commit(y.wt, "RED", "red\n")
	y.commit(y.wt, "check.sh", "echo the copy on the branch\nexit 0\n")

	y.assembly()

	if b := y.bead(); b.Stage != "new" || b.Hold {
		t.Fatalf("bead %s hold=%v, want back in new: the base's script is red on it", b.Stage, b.Hold)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on a branch that rewrote its own test script")
	}
	notes := y.notes()
	for _, want := range []string{"the tests failed on yard/r-1", "(the base's copy of check.sh, exit 1)", "the copy on the base, in " + y.wt + ", landing=1"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("note lacks %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "the copy on the branch") {
		t.Fatalf("the branch's copy of the script ran:\n%s", notes)
	}
}

// A script only the branch has is no file of the base: it runs as a command,
// the branch's copy, which is how a depot's first test script lands.
func TestMergeLocalTestScriptNewOnTheBranchRunsAsACommand(t *testing.T) {
	y := newYard(t)
	y.tests("./check.sh")
	y.commit(y.wt, "check.sh", "#!/bin/sh\necho the copy on the branch\n")
	y.git(y.wt, "update-index", "--chmod=+x", "check.sh")
	y.git(y.wt, "commit", "-q", "-m", "executable")
	y.git(y.wt, "checkout", "-q", "--", "check.sh")
	if err := os.Chmod(filepath.Join(y.wt, "check.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed:\n%s", b.Stage, b.Status, y.notes())
	}
	if !strings.Contains(y.notes(), "tested with the command ./check.sh") {
		t.Fatalf("the note does not say a command ran:\n%s", y.notes())
	}
}

// With main checked out nowhere, landing moves only the ref. A flow without
// a merged stage gets the bead closed instead.
func TestMergeLocalBaseNotCheckedOut(t *testing.T) {
	y := newYard(t)
	y.yardrIn("approved -> new | merge\n", "flow", "set", "f", "-f", "-")
	y.git(y.repo, "checkout", "-q", "--detach")
	tip := y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	short := y.git(y.repo, "rev-parse", "--short", tip)
	if b := y.bead(); b.Status != "closed" || b.CloseReason == nil || !regexp.MustCompile(`^merged `+short+`, tested with the command test ! -e RED in \d+ s$`).MatchString(*b.CloseReason) {
		t.Fatalf("bead %s/%s, want closed as merged %s", b.Stage, b.Status, short)
	}
	if main := y.git(y.repo, "rev-parse", "main"); main != tip {
		t.Fatalf("main %s, want the branch tip %s", main, tip)
	}
}

func TestMergeLocalConflict(t *testing.T) {
	y := newYard(t)
	tip := y.commit(y.wt, "a.txt", "bead's\n")
	main := y.commit(y.repo, "a.txt", "main's\n")

	y.assembly()

	b := y.bead()
	if b.Stage != "merge" || b.Status == "closed" {
		t.Fatalf("bead %s/%s, want open in merge", b.Stage, b.Status)
	}
	if !strings.Contains(y.notes(), "conflicts in: a.txt") {
		t.Fatalf("no note naming the conflict:\n%s", y.notes())
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on a conflict")
	}
	if got := y.git(y.wt, "rev-parse", "HEAD"); got != tip {
		t.Fatal("the branch was left rebased")
	}
	if y.git(y.wt, "status", "--porcelain") != "" {
		t.Fatal("the rebase was not aborted")
	}
	if refused := y.refused(); len(refused) != 0 {
		t.Fatalf("move_refused events = %v, want none", refused)
	}
}

// A conflict in a flow with no merge stage goes back to new with the note,
// without trying the move the flow has not: no move_refused fault.
func TestMergeLocalConflictWithoutMergeStage(t *testing.T) {
	y := newYard(t)
	y.yardrIn("approved -> merged | new\nnew -> approved\nmerged terminal\n", "flow", "set", "f", "-f", "-")
	y.commit(y.wt, "a.txt", "bead's\n")
	main := y.commit(y.repo, "a.txt", "main's\n")

	y.assembly()

	b := y.bead()
	if b.Stage != "new" || b.Status == "closed" {
		t.Fatalf("bead %s/%s, want open in new", b.Stage, b.Status)
	}
	if !strings.Contains(y.notes(), "conflicts in: a.txt") {
		t.Fatalf("no note naming the conflict:\n%s", y.notes())
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on a conflict")
	}
	if refused := y.refused(); len(refused) != 0 {
		t.Fatalf("move_refused events = %v, want none: the refused advance to merge is a false fault", refused)
	}
}

// Same, where the flow names outcomes (as default does): the conflict
// reports failed to new, still without a refused move.
func TestMergeLocalConflictWithoutMergeStageOutcomes(t *testing.T) {
	y := newYard(t)
	y.yardrIn("approved -> merged on done | new on failed\nnew -> approved on done\nmerged terminal\n", "flow", "set", "f", "-f", "-")
	y.commit(y.wt, "a.txt", "bead's\n")
	main := y.commit(y.repo, "a.txt", "main's\n")

	y.assembly()

	b := y.bead()
	if b.Stage != "new" || b.Status == "closed" {
		t.Fatalf("bead %s/%s, want open in new", b.Stage, b.Status)
	}
	if !strings.Contains(y.notes(), "conflicts in: a.txt") {
		t.Fatalf("no note naming the conflict:\n%s", y.notes())
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on a conflict")
	}
	if refused := y.refused(); len(refused) != 0 {
		t.Fatalf("move_refused events = %v, want none", refused)
	}
}

func TestMergeLocalRedTests(t *testing.T) {
	y := newYard(t)
	main := y.git(y.repo, "rev-parse", "main")
	y.commit(y.wt, "RED", "red\n")

	y.assembly()

	if b := y.bead(); b.Stage != "new" {
		t.Fatalf("bead in %s, want new", b.Stage)
	}
	if short := y.git(y.repo, "rev-parse", "--short", "yard/r-1"); !strings.Contains(y.notes(),
		"the tests failed on yard/r-1 at "+short+" rebased onto main (the command test ! -e RED, exit 1); its output:") {
		t.Fatalf("no note of the red run:\n%s", y.notes())
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on a red run")
	}
}

// A test command that says it could not run at all (exit 2) is no builder's
// to fix: the bead is held where it is, not sent back to new.
func TestMergeLocalTestsNotRunHolds(t *testing.T) {
	y := newYard(t)
	main := y.git(y.repo, "rev-parse", "main")
	y.commit(y.wt, "b.txt", "bead\n")
	y.tests("echo no compiler here; exit 2")

	y.assembly()

	b := y.bead()
	if !b.Hold || b.Stage != "approved" {
		t.Fatalf("bead %s hold=%v, want held in approved", b.Stage, b.Hold)
	}
	if notes := y.notes(); !strings.Contains(notes, "the tests could not run") || !strings.Contains(notes, "no compiler here") {
		t.Fatalf("no note on the hold:\n%s", notes)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved without its tests")
	}
}

func TestMergeLocalDirtyCheckoutHolds(t *testing.T) {
	y := newYard(t)
	main := y.git(y.repo, "rev-parse", "main")
	y.commit(y.wt, "b.txt", "bead\n")
	dirty := filepath.Join(y.repo, "a.txt")
	if err := os.WriteFile(dirty, []byte("someone's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	y.assembly()

	b := y.bead()
	if !b.Hold || b.Stage != "approved" {
		t.Fatalf("bead %s hold=%v, want held in approved", b.Stage, b.Hold)
	}
	if !strings.Contains(y.notes(), "uncommitted changes") {
		t.Fatalf("no note on the hold:\n%s", y.notes())
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved under a dirty checkout")
	}
	if got, _ := os.ReadFile(dirty); string(got) != "someone's work\n" {
		t.Fatalf("the dirty tree was touched: %q", got)
	}
}

// What lands is the commit the tests ran on, by its sha, never the branch by
// name: a commit that reaches the branch while they run has not been tested,
// so nothing lands and the bead goes back (the flow's failed outcome) to be
// tested again as it is now.
func TestMergeLocalLandsOnlyWhatItTested(t *testing.T) {
	y := newYard(t)
	main := y.git(y.repo, "rev-parse", "main")
	tested := y.commit(y.wt, "b.txt", "bead\n")
	// The test command itself commits to the branch: the branch moves while
	// it runs, as a push during the tests would move it.
	y.tests("git commit -q --allow-empty -m during-the-tests")

	y.assembly()

	if b := y.bead(); b.Stage != "new" || b.Hold {
		t.Fatalf("bead %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatalf("main moved to %s on a branch that moved during the tests", got)
	}
	if tip := y.git(y.wt, "rev-parse", "HEAD"); tip == tested {
		t.Fatal("the test command did not move the branch; the test proves nothing")
	}
	notes := y.notes()
	for _, want := range []string{"moved while the tests ran", "tested " + tested[:7], "Nothing landed"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("note lacks %q:\n%s", want, notes)
		}
	}
}

// A child of a train lands on the train's branch, which is pushed; main does
// not move, here or on the remote.
func TestMergeLocalChildLandsOnItsTrain(t *testing.T) {
	y := newYard(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	y.git(".", "init", "-q", "--bare", remote)
	y.git(y.repo, "remote", "add", "origin", remote)
	y.git(y.repo, "push", "-q", "origin", "main")
	y.push = "origin"
	y.yardr("bead", "create", "the train", "--id", "r-e", "-d", "r", "-t", "train", "-s", "approved")
	y.yardr("dep", "add", "r-e", "r-1", "-k", "parent")
	if f := y.family(); f.Train != "r-e" || f.Base != "yard/r-e" {
		t.Fatalf("bead show --json: %+v, want train r-e and base yard/r-e", f)
	}
	pushedMain := y.git(remote, "rev-parse", "main")
	// The herdr runner made the train branch from main when r-1 started;
	// since then an earlier child landed on it and main moved on.
	y.git(y.repo, "checkout", "-q", "-b", "yard/r-e")
	trainWas := y.commit(y.repo, "e.txt", "earlier child\n")
	y.git(y.repo, "checkout", "-q", "main")
	main := y.commit(y.repo, "c.txt", "main moved\n")
	y.commit(y.wt, "b.txt", "bead\n")

	y.assembly()

	b := y.bead()
	if b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	train := y.git(y.repo, "rev-parse", "yard/r-e")
	if tip := y.git(y.wt, "rev-parse", "HEAD"); tip != train {
		t.Fatalf("train branch %s is not the landed branch %s", train, tip)
	}
	if y.git(y.repo, "rev-list", "--count", trainWas+"..yard/r-e") != "1" {
		t.Fatal("the child did not land on top of the train branch")
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatalf("main moved to %s, want %s", got, main)
	}
	if _, err := os.Stat(filepath.Join(y.repo, "b.txt")); err == nil {
		t.Fatal("the child's work reached the checkout of main")
	}
	if pushed := y.git(remote, "rev-parse", "yard/r-e"); pushed != train {
		t.Fatalf("origin yard/r-e %s, want %s", pushed, train)
	}
	if pushed := y.git(remote, "rev-parse", "main"); pushed != pushedMain {
		t.Fatalf("origin main moved to %s", pushed)
	}
}

// A yardr started with the environment of these tests and no yard of a
// test's own finds none: not the one of the machine the tests run on,
// whose home TestMain took out of the environment (yardr-j4vm).
func TestAYardrWithoutATestsYardFindsNone(t *testing.T) {
	home := config.Getenv("HOME")
	if home == "" {
		t.Fatal("no home in the environment: a yardr started from here takes the default one, the machine's")
	}
	cmd := exec.Command(yardrBin, "bead", "list")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "no yard in "+home) {
		t.Fatalf("yardr bead list: %v, want exit 2 and no yard in %s\n%s", err, home, out)
	}
}
