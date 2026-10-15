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
	"yardr.dev/yardr/internal/server"
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
	// The test plays the dispatcher (claimFor claims over the socket, as
	// the dispatcher would), so it holds the daemon lock as the daemon
	// would: without a holder the daemon's writes are refused.
	storetest.HoldDaemon(t, home)
	y := &yard{t: t, home: home, repo: filepath.Join(t.TempDir(), "repo")}
	t.Cleanup(func() {
		// Stop by pid (the pidfile), then remove the state.
		if err := server.Stop(&config.Config{Home: home}); err != nil {
			t.Log(err)
		}
		os.RemoveAll(home)
	})
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	// A fresh home has no yard; without a terminal yardr refuses to work
	// in it until it is initialized.
	y.yardr("init", "--harness", "claude", "--dir", home, "--operator", "test")
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
	y := newTrainYard(t, "test ! -e RED")
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
	if notes := y.yardr("prime", "--bead", "r-e"); !strings.Contains(notes, "tests passed") {
		t.Fatalf("no integration note:\n%s", notes)
	}

	// The reviewer approves the whole diff.
	y.yardr("bead", "done", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "approved" {
		t.Fatalf("train in %s after review, want approved", e.Stage)
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
	notes := y.yardr("prime", "--bead", "r-e")
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
	notes := y.yardr("prime", "--bead", "r-e")
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
	if notes := y.yardr("prime", "--bead", "r-e"); strings.Contains(notes, "not forced") {
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
	notes := y.yardr("prime", "--bead", "r-e")
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
	y.yardr("bead", "done", "r-e") // review -> approved
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
