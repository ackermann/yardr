package refinery

// An epic end to end, the way rig yard runs one: its children land on the
// epic's branch through merge-local, the epic waits in open until the last
// closes, integrate-epic rebases and gates its branch and sends it to
// review, and merge-local lands it on main. The herdr side (branching the
// epic and its children) is done by hand, as the herdr runner would.

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
	t.Parallel() // as newYard
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
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
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
	// The epic flow exactly as the repo carries it, under the test's name.
	y.yardr("flow", "set", "yard.epic", "-f", storetest.RepoFlow(t, "yardr.epic"))
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
	// hand: main's child1.txt, r-2's commit replayed), notes the tip it
	// started from, as its brief says, and reports done: the epic is back
	// in open for the integrator.
	wt := filepath.Join(y.home, "worktrees", "r", "r-e")
	y.git(wt, "reset", "-q", "--hard", "main")
	resolved := y.commit(wt, "child2.txt", "r-2\n")
	y.yardr("bead", "note", "r-e", "merge re-review: old tip: "+epic+"\nchild1.txt: kept main's")
	y.yardr("bead", "done", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "open" || e.Hold {
		t.Fatalf("resolved epic %s hold=%v, want back in open", e.Stage, e.Hold)
	}
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/yard/r-e"); !strings.HasPrefix(pushed, epic) {
		t.Fatalf("origin yard/r-e is %q before re-integration, want the old tip %s the children landed", pushed, epic)
	}
	y.exec("integrate-epic", "yard-integrators", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "review" {
		t.Fatalf("re-integrated epic in %s, want review", e.Stage)
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
// Commits on the remote that are in neither (someone pushed to the epic's
// branch) are never forced over: the branch is integrated, the push is
// refused with a note and the yardmaster is mailed.
func TestEpicPushRefusesForeignRemoteCommits(t *testing.T) {
	y := newEpicYard(t, "test ! -e RED")
	y.exec("merge-local", "refinery", "r-1")
	y.exec("merge-local", "refinery", "r-2")
	epic := y.git(y.repo, "rev-parse", "yard/r-e")
	y.commit(y.repo, "child1.txt", "main's own\n")
	y.exec("integrate-epic", "yard-integrators", "r-e") // conflicts: to the mergers

	wt := filepath.Join(y.home, "worktrees", "r", "r-e")
	y.git(wt, "reset", "-q", "--hard", "main")
	y.commit(wt, "child2.txt", "r-2\n")
	y.yardr("bead", "note", "r-e", "merge re-review: old tip: "+epic)
	y.yardr("bead", "done", "r-e")
	// Meanwhile someone pushed a commit of their own on top of the remote
	// branch: in no tip the integrator knows.
	foreign := y.git(y.repo, "commit-tree", epic+"^{tree}", "-p", epic, "-m", "someone's push")
	y.git(y.repo, "push", "-q", "origin", foreign+":refs/heads/yard/r-e")
	// The yard's yardmaster has a name of its own, which YARDR_NOTIFY, unset
	// here, leaves to the yard's setting.
	y.yardr("resident", "add", "lead", "--runner", "manual")
	y.yardr("settings", "set", "yardmaster", "lead")

	y.exec("integrate-epic", "yard-integrators", "r-e")

	if e := y.beadOf("r-e"); e.Stage != "review" || e.Hold {
		t.Fatalf("epic %s hold=%v, want review (integrated, push refused)", e.Stage, e.Hold)
	}
	if pushed := y.git(y.repo, "ls-remote", "origin", "refs/heads/yard/r-e"); !strings.HasPrefix(pushed, foreign) {
		t.Fatalf("origin yard/r-e is %q, want the foreign %s untouched", pushed, foreign)
	}
	notes := y.yardr("prime", "--bead", "r-e")
	if !strings.Contains(notes, "did not have before the rebase") || !strings.Contains(notes, "not forced") {
		t.Fatalf("no note on the refused push:\n%s", notes)
	}
	if lead := y.yardr("resident", "show", "lead"); !strings.Contains(lead, "inbox 1 unread") {
		t.Fatalf("the yardmaster was not mailed:\n%s", lead)
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

// The refinery's scripts work on branches, which a dir rig has none of: an
// exec group routed one by mistake refuses it as could not run (exit 2),
// before any yardr or git call.
func TestScriptsRefuseDirRigs(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	bin := filepath.Join(t.TempDir(), "yardr")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> "+calls+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"integrate-epic", "pr-epic", "pr-merge", "merge-local"} {
		cmd := exec.Command("sh", script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "YARDR_BEAD=d-e", "YARDR_RIG=d", "YARDR_RIG_PATH=" + dir,
			"YARDR_RIG_KIND=dir", "YARDR_BIN=" + bin, "YARDR_HOME=" + t.TempDir()}
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 || !strings.Contains(string(out), "dir rig") {
			t.Errorf("%s on a dir rig: %v\n%s", script, err, out)
		}
	}
	if b, err := os.ReadFile(calls); err == nil {
		t.Fatalf("scripts called yardr for a dir rig:\n%s", b)
	}
}

// Each script's log starts with a header carrying the time of the run. The
// date it runs has to work on BSD date as well as GNU: macOS has no -Is, so
// the header lost its time and the log got date's complaint instead.
func TestScriptsLogHeaderHasTheTime(t *testing.T) {
	header := regexp.MustCompile(`(?m)^echo "== \$\((date [^)]*)\) `)
	stamp := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{4}\n$`)
	for _, script := range []string{"integrate-epic", "pr-epic", "pr-merge", "merge-local"} {
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
