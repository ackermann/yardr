package refinery

// An epic end to end, the way rig yard runs one: its children land on the
// epic's branch through merge-local, the epic waits in open until the last
// closes, integrate-epic rebases and gates its branch and sends it to
// review, and merge-local lands it on main. The herdr side (branching the
// epic and its children) is done by hand, as the herdr runner would.

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

// childFlow is rig yard's flow, with the outcomes merge-local reports.
const childFlow = `backlog -> new
new -> review on done | backlog
review -> approved on done | new on changes
approved -> merged on done | new on failed | merge on conflict
merge -> review on done | new on rework
merged terminal
`

// newEpicYard is an isolated yardr with rig r over a fresh repository (main
// checked out in it, pushed to a bare origin), epic r-e in open under the
// repo's yard.epic flow, and its children r-1 and r-2 in approved, each one
// commit ahead of the epic's branch in its worktree. check is the rig's gate.
func newEpicYard(t *testing.T, check string) *yard {
	t.Helper()
	home, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	y := &yard{t: t, home: home, repo: filepath.Join(t.TempDir(), "repo")}
	t.Cleanup(func() {
		// Stop by pid (the pidfile), then remove the state.
		if err := server.Stop(&config.Config{Home: home}); err != nil {
			t.Log(err)
		}
		os.RemoveAll(home)
	})
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t")
	// A fresh home has no yard; without a terminal yardr refuses to work
	// in it until it is initialized.
	y.yardr("init", "--harness", "claude", "--dir", home)
	y.git(".", "init", "-q", "-b", "main", y.repo)
	y.commit(y.repo, "a.txt", "one\n")
	remote := filepath.Join(t.TempDir(), "origin.git")
	y.git(".", "init", "-q", "--bare", remote)
	y.git(y.repo, "remote", "add", "origin", remote)
	y.git(y.repo, "push", "-q", "origin", "main")
	y.push = "origin"

	y.yardr("rig", "add", "r", y.repo, "--base", "main", "--check", check)
	y.yardrIn(childFlow, "flow", "set", "code", "-f", "-")
	y.yardr("rig", "flow", "r", "code")
	// The epic flow exactly as the repo carries it.
	epicFlow, err := filepath.Abs("../../.yardr/flows/yard.epic.flow")
	if err != nil {
		t.Fatal(err)
	}
	y.yardr("flow", "set", "yard.epic", "-f", epicFlow)
	y.yardr("rig", "flow", "r", "--type", "epic", "yard.epic")

	y.yardr("bead", "create", "the epic", "--id", "r-e", "-r", "r", "-t", "epic", "-s", "open")
	y.git(y.repo, "branch", "yard/r-e") // the herdr runner branches it from main
	for i, id := range []string{"r-1", "r-2"} {
		y.yardr("bead", "create", "child", "--id", id, "-r", "r", "-s", "approved")
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

func TestEpicEndToEnd(t *testing.T) {
	y := newEpicYard(t, "test ! -e RED")
	if y.ready("r-e") {
		t.Fatal("the epic is ready while its children are open")
	}

	// Each child lands on the epic's branch, not on main.
	y.exec("merge-local", "refinery", "r-1")
	if y.ready("r-e") {
		t.Fatal("the epic is ready with r-2 still open")
	}
	y.exec("merge-local", "refinery", "r-2")
	for _, id := range []string{"r-1", "r-2"} {
		if b := y.beadOf(id); b.Stage != "merged" || b.Status != "closed" {
			t.Fatalf("child %s %s/%s, want merged and closed", id, b.Stage, b.Status)
		}
	}
	if !y.ready("r-e") {
		t.Fatal("the epic is not ready once its last child closed")
	}
	// main moved meanwhile: the integrator must rebase the epic onto it.
	main := y.commit(y.repo, "c.txt", "main moved\n")

	y.exec("integrate-epic", "yard-integrators", "r-e")

	e := y.beadOf("r-e")
	if e.Stage != "review" || e.Status != "open" {
		t.Fatalf("epic %s/%s after integrating, want review", e.Stage, e.Status)
	}
	epic := y.git(y.repo, "rev-parse", "yard/r-e")
	if y.git(y.repo, "merge-base", "main", "yard/r-e") != main {
		t.Fatal("the epic branch was not rebased onto main")
	}
	if n := y.git(y.repo, "rev-list", "--count", "main..yard/r-e"); n != "2" {
		t.Fatalf("epic branch is %s commits ahead of main, want its 2 children", n)
	}
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/yard/r-e"); !strings.HasPrefix(pushed, epic) {
		t.Fatalf("origin yard/r-e is %q, want the rebased %s", pushed, epic)
	}
	if notes := y.yardr("prime", "--bead", "r-e"); !strings.Contains(notes, "gate passed") {
		t.Fatalf("no integration note:\n%s", notes)
	}

	// The reviewer approves the whole diff.
	y.yardr("bead", "done", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "approved" {
		t.Fatalf("epic in %s after review, want approved", e.Stage)
	}

	// The refinery lands it on main as one fast-forward.
	y.exec("merge-local", "refinery", "r-e")
	e = y.beadOf("r-e")
	if e.Stage != "merged" || e.Status != "closed" {
		t.Fatalf("epic %s/%s, want merged and closed", e.Stage, e.Status)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != epic {
		t.Fatalf("main at %s, want the epic branch %s", got, epic)
	}
	for _, f := range []string{"child1.txt", "child2.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(y.repo, f)); err != nil {
			t.Fatalf("checkout of main lacks %s: %v", f, err)
		}
	}
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/main"); !strings.HasPrefix(pushed, epic) {
		t.Fatalf("origin main is %q, want %s", pushed, epic)
	}
}

// A red gate on the integrated epic sends it back to open with the gate's
// tail, and adds a child to fix it, which blocks the epic: it is not
// integrated again until the fix lands. main does not move.
func TestEpicRedGateGoesBackToOpen(t *testing.T) {
	// Red when the epic's work meets what main gained since it branched.
	y := newEpicYard(t, `! { test -e child1.txt && test -e clash.txt; } || { echo "child1 clashes with main"; exit 1; }`)
	y.exec("merge-local", "refinery", "r-1")
	y.exec("merge-local", "refinery", "r-2")
	main := y.commit(y.repo, "clash.txt", "main moved\n")

	y.exec("integrate-epic", "yard-integrators", "r-e")

	e := y.beadOf("r-e")
	if e.Stage != "open" || e.Status != "open" || e.Hold {
		t.Fatalf("epic %s/%s hold=%v, want back in open, not held", e.Stage, e.Status, e.Hold)
	}
	notes := y.yardr("prime", "--bead", "r-e")
	if !strings.Contains(notes, "the gate failed") || !strings.Contains(notes, "child1 clashes with main") {
		t.Fatalf("no note with the gate's tail:\n%s", notes)
	}
	f := y.familyOf("r-e")
	if len(f.Children) != 3 {
		t.Fatalf("epic children %v, want r-1, r-2 and a fix", f.Children)
	}
	fix := y.beadOf(f.Children[2])
	if fix.Stage != "backlog" || fix.Type != "bug" || !strings.Contains(fix.Body, "child1 clashes with main") {
		t.Fatalf("fix child %+v, want a bug in backlog with the gate's tail", fix)
	}
	if y.ready("r-e") {
		t.Fatal("the epic is ready again before its fix landed")
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on a red gate")
	}
}

// A rebase of the epic onto main that conflicts is no child's to fix: like
// a bead's, it goes to the mergers (open -> merge on conflict) with the files
// and both tips, and neither branch moves. The merger's done sends it back
// to open, where the integrator rebases it cleanly and sends it to review.
func TestEpicRebaseConflictGoesToTheMergers(t *testing.T) {
	y := newEpicYard(t, "test ! -e RED")
	y.exec("merge-local", "refinery", "r-1")
	y.exec("merge-local", "refinery", "r-2")
	epic := y.git(y.repo, "rev-parse", "yard/r-e")
	main := y.commit(y.repo, "child1.txt", "main's own\n")

	y.exec("integrate-epic", "yard-integrators", "r-e")

	e := y.beadOf("r-e")
	if e.Hold || e.Stage != "merge" || e.Status != "open" {
		t.Fatalf("epic %s/%s hold=%v, want in merge, not held", e.Stage, e.Status, e.Hold)
	}
	notes := y.yardr("prime", "--bead", "r-e")
	for _, want := range []string{"rebase of yard/r-e onto main conflicts in: child1.txt",
		"yard/r-e is at " + epic[:7], "main at " + main[:7], "do not push it"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("conflict note lacks %q:\n%s", want, notes)
		}
	}
	if got := y.git(y.repo, "rev-parse", "yard/r-e"); got != epic {
		t.Fatal("the epic branch was left rebased")
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved")
	}

	// The merger rebases the branch onto main in its worktree (here by
	// hand: main's child1.txt, r-2's commit replayed) and reports done: the
	// epic is back in open for the integrator.
	wt := filepath.Join(y.home, "worktrees", "r", "r-e")
	y.git(wt, "reset", "-q", "--hard", "main")
	y.commit(wt, "child2.txt", "r-2\n")
	y.yardr("bead", "done", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("resolved epic %s hold=%v, want back in open", e.Stage, e.Hold)
	}
	y.exec("integrate-epic", "yard-integrators", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "review" {
		t.Fatalf("re-integrated epic in %s, want review", e.Stage)
	}
}

// merge-local sends a bead whose gate is red back by the flow's failed
// outcome: an epic goes to open (its flow has no new), a plain bead to new.
func TestMergeLocalRedGateSendsAnEpicToOpen(t *testing.T) {
	y := newEpicYard(t, "test ! -e RED")
	y.exec("merge-local", "refinery", "r-1")
	y.exec("merge-local", "refinery", "r-2")
	y.exec("integrate-epic", "yard-integrators", "r-e")
	y.yardr("bead", "done", "r-e") // review -> approved
	// Something red reached the epic branch after review.
	y.commit(filepath.Join(y.home, "worktrees", "r", "r-e"), "RED", "red\n")
	main := y.git(y.repo, "rev-parse", "main")

	y.exec("merge-local", "refinery", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("epic %s hold=%v, want back in open", e.Stage, e.Hold)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on a red gate")
	}
}
