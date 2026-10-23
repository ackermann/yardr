package assembly

// A train end to end, the way depot yard runs one: its children land on the
// train's branch through merge-local, the train waits in open until the last
// closes, integrate-train rebases and tests its branch and sends it to
// review, and merge-local lands it on main. The herdr side (branching the
// train and its children) is done by hand, as the herdr runner would.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// childFlow is depot yard's flow, with the outcomes merge-local reports.
const childFlow = `backlog -> new
new -> review on done | backlog
review -> approved on done | new on changes
approved -> merged on done | new on failed | merge on conflict
merge -> review on done | new on rework
merged terminal
`

// newTrainYard is an isolated yardr with depot r over a fresh repository (main
// checked out in it, pushed to a bare origin), train r-e in open under the
// repo's yard.train flow, and its children r-1 and r-2 in approved, each one
// commit ahead of the train's branch in its worktree. check is the depot's
// test command, which integrate-train and merge-local run.
func newTrainYard(t *testing.T, check string) *yard {
	t.Helper()
	t.Parallel() // as newYard
	home, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	y := &yard{t: t, home: home, repo: filepath.Join(t.TempDir(), "repo")}
	t.Cleanup(func() { os.RemoveAll(home) })
	// os.Environ names this binary to the servers a command starts
	// (TestMain), so a run killed by its timeout leaves none behind.
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	y.git(".", "init", "-q", "-b", "main", y.repo)
	y.commit(y.repo, "a.txt", "one\n")
	remote := filepath.Join(t.TempDir(), "origin.git")
	y.git(".", "init", "-q", "--bare", remote)
	y.git(y.repo, "remote", "add", "origin", remote)
	y.git(y.repo, "push", "-q", "origin", "main")
	y.push = "origin"

	// The yard's store API is served in this process; the setup goes over
	// rpc, as newYard's.
	serveYard(t, home)
	y.initStore()
	y.putDepot("r", y.repo, "main", check)
	y.putFlow("code", childFlow)
	y.pointFlow("r", "", "code")
	// The train flow exactly as the repo carries it, under the test's name.
	y.putFlowFile("yard.train", storetest.RepoFlow(t, "yardr.train"))
	y.pointFlow("r", "train", "yard.train")

	y.createBead("the train", "r-e", "r", "train", "open")
	y.git(y.repo, "branch", "yard/r-e") // the herdr runner branches it from main
	for i, id := range []string{"r-1", "r-2"} {
		y.createBead("child", id, "r", "", "approved")
		y.addParent("r-e", id)
		wt := filepath.Join(home, "worktrees", "r", id)
		y.git(y.repo, "worktree", "add", "-q", "-b", "yard/"+id, wt, "yard/r-e")
		y.commit(wt, "child"+string(rune('1'+i))+".txt", id+"\n")
	}
	return y
}

// newTrainYardCLI is newTrainYard through the real CLI end to end, for the
// one test that keeps that path: TestTrainEndToEnd.
func newTrainYardCLI(t *testing.T, check string) *yard {
	t.Helper()
	t.Parallel() // as newYard
	home, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	y := &yard{t: t, home: home, repo: filepath.Join(t.TempDir(), "repo")}
	t.Cleanup(func() { os.RemoveAll(home) })
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	serveYard(t, home)
	// A fresh home has no yard; without a terminal yardr refuses to work
	// in it until it is initialized.
	y.yardr("init", "--harness", "claude", "--dir", home, "--operator", "test")
	// Disk keeping is off: whether a keeping acts depends on the
	// machine's free space, and these landings are the same on any
	// machine.
	y.yardr("settings", "set", "disk_keep_gb", "0")
	y.git(".", "init", "-q", "-b", "main", y.repo)
	y.commit(y.repo, "a.txt", "one\n")
	remote := filepath.Join(t.TempDir(), "origin.git")
	y.git(".", "init", "-q", "--bare", remote)
	y.git(y.repo, "remote", "add", "origin", remote)
	y.git(y.repo, "push", "-q", "origin", "main")
	y.push = "origin"

	y.yardr("depot", "add", "r", y.repo, "--base", "main", "--test", check)
	y.yardrIn(childFlow, "flow", "set", "code", "-f", "-")
	y.yardr("depot", "flow", "r", "code")
	// The train flow exactly as the repo carries it, under the test's name.
	y.yardr("flow", "set", "yard.train", "-f", storetest.RepoFlow(t, "yardr.train"))
	y.yardr("depot", "flow", "r", "--type", "train", "yard.train")

	y.yardr("bead", "create", "the train", "--id", "r-e", "-d", "r", "-t", "train", "-s", "open")
	y.git(y.repo, "branch", "yard/r-e") // the herdr runner branches it from main
	for i, id := range []string{"r-1", "r-2"} {
		y.yardr("bead", "create", "child", "--id", id, "-d", "r", "-s", "approved")
		y.yardr("dep", "add", "r-e", id, "-k", "parent")
		wt := filepath.Join(home, "worktrees", "r", id)
		y.git(y.repo, "worktree", "add", "-q", "-b", "yard/"+id, wt, "yard/r-e")
		y.commit(wt, "child"+string(rune('1'+i))+".txt", id+"\n")
	}
	return y
}

// ready reports whether bead id is ready: open, not held, no open blocker.
func (y *yard) ready(id string) bool {
	y.t.Helper()
	bs, err := rpc.NewClient((&config.Config{Home: y.home}).Socket()).Ready(context.Background())
	if err != nil {
		y.t.Fatal(err)
	}
	return slices.ContainsFunc(bs, func(b store.Bead) bool { return b.ID == id })
}

func TestTrainEndToEnd(t *testing.T) {
	// The test command records the bead it runs for and the beads it is told
	// it tests, which the depot's gate reads for its flaky log.
	record := filepath.Join(t.TempDir(), "runs")
	y := newTrainYardCLI(t, `test ! -e RED && echo "$YARDR_BEAD ${YARDR_TEST_BEADS-unset}" >>`+shellQuote(record))
	if y.ready("r-e") {
		t.Fatal("the train is ready while its children are open")
	}

	// Each child lands on the train's branch, not on main.
	y.exec("merge-local", "assembly", "r-1")
	if y.ready("r-e") {
		t.Fatal("the train is ready with r-2 still open")
	}
	y.exec("merge-local", "assembly", "r-2")
	for _, id := range []string{"r-1", "r-2"} {
		if b := y.beadOf(id); b.Stage != "merged" || b.Status != "closed" {
			t.Fatalf("child %s %s/%s, want merged and closed", id, b.Stage, b.Status)
		}
	}
	if !y.ready("r-e") {
		t.Fatal("the train is not ready once its last child closed")
	}
	// main moved meanwhile: the integrator must rebase the train onto it.
	main := y.commit(y.repo, "c.txt", "main moved\n")

	y.exec("integrate-train", "yard-integrators", "r-e")

	e := y.beadOf("r-e")
	if e.Stage != "review" || e.Status != "open" {
		t.Fatalf("train %s/%s after integrating, want review", e.Stage, e.Status)
	}
	train := y.git(y.repo, "rev-parse", "yard/r-e")
	if y.git(y.repo, "merge-base", "main", "yard/r-e") != main {
		t.Fatal("the train branch was not rebased onto main")
	}
	if n := y.git(y.repo, "rev-list", "--count", "main..yard/r-e"); n != "2" {
		t.Fatalf("train branch is %s commits ahead of main, want its 2 children", n)
	}
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/yard/r-e"); !strings.HasPrefix(pushed, train) {
		t.Fatalf("origin yard/r-e is %q, want the rebased %s", pushed, train)
	}
	if notes := y.notesOf("r-e"); !strings.Contains(notes, "tests passed") {
		t.Fatalf("no integration note:\n%s", notes)
	}

	// The reviewer approves the whole diff, and the security review passes it.
	y.yardr("bead", "done", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "review-security" {
		t.Fatalf("train in %s after review, want review-security", e.Stage)
	}
	y.yardr("bead", "done", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "approved" {
		t.Fatalf("train in %s after the security review, want approved", e.Stage)
	}

	// The assembly lands it on main as one fast-forward.
	y.exec("merge-local", "assembly", "r-e")
	e = y.beadOf("r-e")
	if e.Stage != "merged" || e.Status != "closed" {
		t.Fatalf("train %s/%s, want merged and closed", e.Stage, e.Status)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != train {
		t.Fatalf("main at %s, want the train branch %s", got, train)
	}
	for _, f := range []string{"child1.txt", "child2.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(y.repo, f)); err != nil {
			t.Fatalf("checkout of main lacks %s: %v", f, err)
		}
	}
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(pushed, train) {
		t.Fatalf("origin main is %q, want %s", pushed, train)
	}

	// A child's landing is of its own bead and says nothing more. The train's
	// integration names the train in YARDR_BEAD, which is no change of its own,
	// and its children in YARDR_TEST_BEADS, sorted. The train's landing by
	// merge-local is the fourth run; what it says is not pinned here.
	runs, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(runs)), "\n")
	// The lookup is jq's: a machine without it tells the integration no beads,
	// which TestIntegrateTrainTellsTheTestCommandItsBeads pins.
	integrated := "r-e r-1,r-2"
	if _, err := exec.LookPath("jq"); err != nil {
		integrated = "r-e "
	}
	if want := []string{"r-1 unset", "r-2 unset", integrated}; len(got) != 4 || !slices.Equal(got[:3], want) || !strings.HasPrefix(got[3], "r-e ") {
		t.Fatalf("the test command ran for %q, want the children's landings, the train's integration told %q, and the train's landing", got, integrated)
	}
}

// integrate-train tells the test command which beads it tests, where the
// command's YARDR_BEAD names the train: the train's children, sorted and once
// each, or none when it cannot name them (a train without any, or yardr not
// answering), as a variable that is set and empty, so that the depot's
// command, which keeps a flaky test's sightings by change, does not take the
// train for one. The run is the direct one of integrate_train_watcher_test.go
// with a yardr that answers for the train; TestTrainEndToEnd has the real
// yardr.
func TestIntegrateTrainTellsTheTestCommandItsBeads(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		train string // what the stub's bead show prints, empty for a stub that fails
		want  string // what the test command made of its YARDR_BEAD and YARDR_TEST_BEADS
		said  string // what the log says of it, if anything
		jq    bool   // the lookup is jq's: the row wants it on the machine
	}{
		{"the children, sorted and once each", `{"id":"r-1","children":["r-2","r-10","r-1","r-2"]}`, "r-1 r-1,r-10,r-2", "", true},
		{"a train with no children", `{"id":"r-1"}`, "r-1 ", "", true},
		{"yardr not answering", "", "r-1 ", "could not read the children of r-1", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, err := exec.LookPath("jq"); c.jq && err != nil {
				t.Skip("jq is not installed")
			}
			record := filepath.Join(t.TempDir(), "run")
			w := newIntegrateTrainWatcherRun(t, `echo "$YARDR_BEAD ${YARDR_TEST_BEADS-unset}" >`+shellQuote(record), "1779", 0)
			t.Cleanup(func() { killWatcherSleepers(t, w.record, "1779") })
			// bead show --json is how integrate-train reads the children;
			// everything else is the report it ends with, whose note is its
			// input.
			stub := filepath.Join(filepath.Dir(w.tmp), "yardr-stub")
			text := "#!/bin/sh\ncase \"$*\" in\n\"bead show r-1 --json\") [ -n \"$FAKE_TRAIN\" ] || exit 1; printf '%s\\n' \"$FAKE_TRAIN\" ;;\n*) cat >/dev/null 2>&1 ;;\nesac\n"
			if err := os.WriteFile(stub, []byte(text), 0o755); err != nil {
				t.Fatal(err)
			}
			w.env = append(w.env, "YARDR_BIN="+stub, "FAKE_TRAIN="+c.train)
			// Into a file, not a pipe, as the runs in watcher_test.go: see there.
			log, err := os.Create(filepath.Join(t.TempDir(), "out"))
			if err != nil {
				t.Fatal(err)
			}
			cmd := w.cmd()
			cmd.Stdout, cmd.Stderr = log, log
			err = cmd.Run()
			log.Close()
			b, _ := os.ReadFile(log.Name())
			out := string(b)
			if err != nil || !strings.Contains(out, "== r-1 done") {
				t.Fatalf("integrate-train: %v\n%s", err, out)
			}
			awaitNoWatcherSleepers(t, w.record, "1779")
			ran, err := os.ReadFile(record)
			if err != nil {
				t.Fatalf("the test command did not run: %v\n%s", err, out)
			}
			if got := strings.TrimSuffix(string(ran), "\n"); got != c.want {
				t.Errorf("the test command saw %q, want %q\n%s", got, c.want, out)
			}
			if c.said != "" && !strings.Contains(out, c.said) {
				t.Errorf("the log lacks %q:\n%s", c.said, out)
			}
			if c.said == "" && strings.Contains(out, "could not read the children") {
				t.Errorf("the log says the children could not be read:\n%s", out)
			}
			if !strings.Contains(out, "with YARDR_LANDING=1 and YARDR_TEST_BEADS="+strings.TrimPrefix(c.want, "r-1 ")) {
				t.Errorf("the log does not say which beads the tests were told:\n%s", out)
			}
		})
	}
}

// With YARDR_LAND=squash a child of a train lands on the train's branch as one
// commit written from the bead, a wagon as any other type, and the train
// itself lands by rebase whatever the mode: main gets the commit of each
// child, not one for the train (docs/line.md, "Wagons"). The train is
// approved from the start: its own flow is not what is tested here.
func TestMergeLocalSquashLandsEachChildAsOneCommitAndTheTrainByRebase(t *testing.T) {
	y := newYard(t)
	y.opts = "YARDR_LAND=squash"
	y.retype("r-1", "wagon")
	y.createBead("the train", "r-e", "r", "train", "approved")
	y.addParent("r-e", "r-1")
	y.beadWith("r-2", "child", "task", nil)
	y.addParent("r-e", "r-2")
	main := y.git(y.repo, "rev-parse", "main")
	y.git(y.repo, "branch", "yard/r-e")
	wt := filepath.Join(y.home, "worktrees", "r", "r-2")
	y.git(y.repo, "worktree", "add", "-q", "-b", "yard/r-2", wt, "main")
	y.commit(y.wt, "b.txt", "wagon\n")
	y.commit(wt, "c.txt", "child\n")
	subjects := func(rng string) string { return y.git(y.repo, "log", "--reverse", "--format=%s", rng) }

	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")

	const children = "chore: work (r-1)\nchore: child (r-2)"
	if got := subjects(main + "..yard/r-e"); got != children {
		t.Fatalf("the train's branch gained\n%s\nwant a commit written from each child", got)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved when children landed on the train's branch")
	}
	train := y.git(y.repo, "rev-parse", "yard/r-e")

	y.exec("merge-local", "assembly", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "merged" || e.Status != "closed" {
		t.Fatalf("train %s/%s, want merged and closed:\n%s", e.Stage, e.Status, y.notesOf("r-e"))
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != train {
		t.Fatalf("main at %s, want the train's branch %s: the train lands as it is", got, train)
	}
	if got := subjects(main + "..main"); got != children {
		t.Fatalf("main gained\n%s\nwant the commit of each child and none for the train", got)
	}
}

// A red run on the integrated train sends it back to open with the end of
// its output, and adds a child to fix it, which blocks the train: it is not
// integrated again until the fix lands. main does not move.
func TestTrainRedTestsGoBackToOpen(t *testing.T) {
	// Red when the train's work meets what main gained since it branched.
	y := newTrainYard(t, `! { test -e child1.txt && test -e clash.txt; } || { echo "child1 clashes with main"; exit 1; }`)
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	main := y.commit(y.repo, "clash.txt", "main moved\n")

	y.exec("integrate-train", "yard-integrators", "r-e")

	e := y.beadOf("r-e")
	if e.Stage != "open" || e.Status != "open" || e.Hold {
		t.Fatalf("train %s/%s hold=%v, want back in open, not held", e.Stage, e.Status, e.Hold)
	}
	notes := y.notesOf("r-e")
	if !strings.Contains(notes, "the tests failed") || !strings.Contains(notes, "child1 clashes with main") {
		t.Fatalf("no note with the end of the output:\n%s", notes)
	}
	f := y.familyOf("r-e")
	if len(f.Children) != 3 {
		t.Fatalf("train children %v, want r-1, r-2 and a fix", f.Children)
	}
	fix := y.beadOf(f.Children[2])
	if fix.Stage != "backlog" || fix.Type != "bug" || !strings.Contains(fix.Body, "child1 clashes with main") {
		t.Fatalf("fix child %+v, want a bug in backlog with the end of the output", fix)
	}
	if y.ready("r-e") {
		t.Fatal("the train is ready again before its fix landed")
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on a red run")
	}
}

// A rebase of the train onto main that conflicts is no child's to fix: like
// a bead's, it goes to the mergers (open -> merge on conflict) with the files
// and both tips, and neither branch moves. The merger's done sends it back
// to open, where the integrator rebases it cleanly and sends it to review.
func TestTrainRebaseConflictGoesToTheMergers(t *testing.T) {
	y := newTrainYard(t, "test ! -e RED")
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	train := y.git(y.repo, "rev-parse", "yard/r-e")
	main := y.commit(y.repo, "child1.txt", "main's own\n")

	y.exec("integrate-train", "yard-integrators", "r-e")

	e := y.beadOf("r-e")
	if e.Hold || e.Stage != "merge" || e.Status != "open" {
		t.Fatalf("train %s/%s hold=%v, want in merge, not held", e.Stage, e.Status, e.Hold)
	}
	notes := y.notesOf("r-e")
	for _, want := range []string{"rebase of yard/r-e onto main conflicts in: child1.txt",
		"yard/r-e is at " + train[:7], "main at " + main[:7], "do not push it"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("conflict note lacks %q:\n%s", want, notes)
		}
	}
	if got := y.git(y.repo, "rev-parse", "yard/r-e"); got != train {
		t.Fatal("the train branch was left rebased")
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved")
	}

	// The merger rebases the branch onto main in its worktree (here by
	// hand: main's child1.txt, r-2's commit replayed), notes the tip it
	// started from, as its brief says, and reports done: the train is back
	// in open for the integrator.
	wt := filepath.Join(y.home, "worktrees", "r", "r-e")
	y.git(wt, "reset", "-q", "--hard", "main")
	resolved := y.commit(wt, "child2.txt", "r-2\n")
	y.yardr("bead", "note", "r-e", "merge re-review: old tip: "+train+"\nchild1.txt: kept main's")
	y.yardr("bead", "done", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("resolved train %s hold=%v, want back in open", e.Stage, e.Hold)
	}
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/yard/r-e"); !strings.HasPrefix(pushed, train) {
		t.Fatalf("origin yard/r-e is %q before re-integration, want the old tip %s the children landed", pushed, train)
	}
	y.exec("integrate-train", "yard-integrators", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "review" {
		t.Fatalf("re-integrated train in %s, want review", e.Stage)
	}
	// The remote branch was the old tip, which the merger's rebase rewrote:
	// everything on it is in the tip the merger noted, so the lease holds
	// and the push replaces it with the resolved branch.
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/yard/r-e"); !strings.HasPrefix(pushed, resolved) {
		t.Fatalf("origin yard/r-e is %q after re-integration, want the resolved %s", pushed, resolved)
	}
	if notes := y.notesOf("r-e"); strings.Contains(notes, "not forced") {
		t.Fatalf("the push was refused:\n%s", notes)
	}
}

// The integrator's lease: a remote branch is replaced only when everything
// on it was in the branch before the rebase or in a tip a merger noted.
// Commits on the remote that are in neither (someone pushed to the train's
// branch) are never forced over: the branch is integrated, the push is
// refused with a note and the yardmaster is mailed.
func TestTrainPushRefusesForeignRemoteCommits(t *testing.T) {
	y := newTrainYard(t, "test ! -e RED")
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	train := y.git(y.repo, "rev-parse", "yard/r-e")
	y.commit(y.repo, "child1.txt", "main's own\n")
	y.exec("integrate-train", "yard-integrators", "r-e") // conflicts: to the mergers

	wt := filepath.Join(y.home, "worktrees", "r", "r-e")
	y.git(wt, "reset", "-q", "--hard", "main")
	y.commit(wt, "child2.txt", "r-2\n")
	y.yardr("bead", "note", "r-e", "merge re-review: old tip: "+train)
	y.yardr("bead", "done", "r-e")
	// Meanwhile someone pushed a commit of their own on top of the remote
	// branch: in no tip the integrator knows.
	foreign := y.git(y.repo, "commit-tree", train+"^{tree}", "-p", train, "-m", "someone's push")
	y.git(y.repo, "push", "-q", "origin", foreign+":refs/heads/yard/r-e")
	// The yard's yardmaster has a name of its own, which YARDR_NOTIFY, unset
	// here, leaves to the yard's setting.
	y.yardr("crew", "add", "lead", "--runner", "manual")
	y.yardr("settings", "set", "yardmaster", "lead")

	y.exec("integrate-train", "yard-integrators", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "review" || e.Hold {
		t.Fatalf("train %s hold=%v, want review (integrated, push refused)", e.Stage, e.Hold)
	}
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/yard/r-e"); !strings.HasPrefix(pushed, foreign) {
		t.Fatalf("origin yard/r-e is %q, want the foreign %s untouched", pushed, foreign)
	}
	notes := y.notesOf("r-e")
	if !strings.Contains(notes, "did not have before the rebase") || !strings.Contains(notes, "not forced") {
		t.Fatalf("no note on the refused push:\n%s", notes)
	}
	if lead := y.yardr("crew", "show", "lead"); !strings.Contains(lead, "inbox 1 unread") {
		t.Fatalf("the yardmaster was not mailed:\n%s", lead)
	}
}

// merge-local sends a bead whose tests are red back by the flow's failed
// outcome: a train goes to open (its flow has no new), a plain bead to new.
func TestMergeLocalRedTestsSendATrainToOpen(t *testing.T) {
	y := newTrainYard(t, "test ! -e RED")
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	y.exec("integrate-train", "yard-integrators", "r-e")
	y.yardr("bead", "done", "r-e") // review -> review-security
	y.yardr("bead", "done", "r-e") // review-security -> approved
	// Something red reached the train branch after review.
	y.commit(filepath.Join(y.home, "worktrees", "r", "r-e"), "RED", "red\n")
	main := y.git(y.repo, "rev-parse", "main")

	y.exec("merge-local", "assembly", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("train %s hold=%v, want back in open", e.Stage, e.Hold)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on a red run")
	}
}

// The assembly's scripts work on branches, which a dir depot has none of: an
// exec group routed one by mistake refuses it as could not run (exit 2),
// before any yardr or git call.
func TestScriptsRefuseDirDepots(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	bin := filepath.Join(t.TempDir(), "yardr")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> "+calls+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"integrate-train", "pr-train", "pr-merge", "merge-local"} {
		cmd := exec.Command("sh", script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "YARDR_BEAD=d-e", "YARDR_DEPOT=d", "YARDR_DEPOT_PATH=" + dir,
			"YARDR_DEPOT_KIND=dir", "YARDR_BIN=" + bin, "YARDR_HOME=" + t.TempDir()}
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 || !strings.Contains(string(out), "dir depot") {
			t.Errorf("%s on a dir depot: %v\n%s", script, err, out)
		}
	}
	if b, err := os.ReadFile(calls); err == nil {
		t.Fatalf("scripts called yardr for a dir depot:\n%s", b)
	}
}

// Each script's log starts with a header carrying the time of the run. The
// date it runs has to work on BSD date as well as GNU: macOS has no -Is, so
// the header lost its time and the log got date's complaint instead.
func TestScriptsLogHeaderHasTheTime(t *testing.T) {
	header := regexp.MustCompile(`(?m)^echo "== \$\((date [^)]*)\) `)
	stamp := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{4}\n$`)
	for _, script := range []string{"integrate-train", "pr-train", "pr-merge", "merge-local"} {
		s := readFile(t, script)
		if strings.Contains(s, "date -"+"I") {
			t.Errorf("%s uses an unsupported date flag", script)
		}
		m := header.FindStringSubmatch(s)
		if m == nil {
			t.Errorf("%s: no log header with a date", script)
			continue
		}
		var stdout, stderr strings.Builder
		cmd := exec.Command("sh", "-c", m[1])
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil || stderr.Len() != 0 || !stamp.MatchString(stdout.String()) {
			t.Errorf("%s: %s: %v\nstdout: %q\nstderr: %q", script, m[1], err, stdout.String(), stderr.String())
		}
	}
}
