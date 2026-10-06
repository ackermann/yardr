package rigcheck

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitCheck(t *testing.T, dir, script string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, RepoPath), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", RepoPath)
	gitT(t, dir, "commit", "-q", "-m", "check")
}

// A depot on main with .yardr/check, and a bead worktree on yard/b1.
func depotWithBead(t *testing.T, baseScript string) (store.Depot, string) {
	t.Helper()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	if baseScript != "" {
		commitCheck(t, repo, baseScript)
	}
	wt := filepath.Join(t.TempDir(), "b1")
	gitT(t, repo, "worktree", "add", "-q", "-b", "yard/b1", wt)
	return store.Depot{Name: "r", Path: repo, Base: "main"}, wt
}

func clean(t *testing.T, dir string) {
	t.Helper()
	if st := gitT(t, dir, "status", "--porcelain", "--ignored"); st != "" {
		t.Fatalf("check left files in %s:\n%s", dir, st)
	}
}

// The gate is read from the base: a bead branch that rewrites .yardr/check
// to always pass (or always fail) changes nothing for itself.
func TestCheckReadsTheBaseNotTheBead(t *testing.T) {
	ctx := context.Background()
	depot, wt := depotWithBead(t, "#!/bin/sh\necho base gate in \"$(pwd)\"\nexit 3\n")
	commitCheck(t, wt, "#!/bin/sh\necho bead gate\nexit 0\n")

	res, err := Run(ctx, depot, wt)
	if err != nil {
		t.Fatal(err)
	}
	sha := gitT(t, depot.Path, "rev-parse", "main")
	if res.Pass || res.ExitCode != 3 || res.Source != "repo@"+sha[:12] {
		t.Fatalf("result = %+v", res)
	}
	real, _ := filepath.EvalSymlinks(wt)
	if !strings.Contains(res.Output, "base gate in "+real) || strings.Contains(res.Output, "bead gate") {
		t.Fatalf("ran the wrong gate, or not in the worktree:\n%s", res.Output)
	}
	clean(t, wt)

	// Once the change lands on the base, it is the gate.
	gitT(t, depot.Path, "merge", "-q", "--ff-only", "yard/b1")
	if res, err = Run(ctx, depot, wt); err != nil || !res.Pass || !strings.Contains(res.Output, "bead gate") {
		t.Fatalf("landed gate not used: %+v %v", res, err)
	}
}

// The store's --check wins over the repo's file.
func TestCheckStoreOverrideWins(t *testing.T) {
	ctx := context.Background()
	depot, wt := depotWithBead(t, "#!/bin/sh\nexit 1\n")
	depot.Check = "echo override in $YARDR_DEPOT; test -d .git || test -f .git"
	res, err := Run(ctx, depot, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass || res.Source != "store" || !strings.Contains(res.Output, "override in r") {
		t.Fatalf("result = %+v", res)
	}
	depot.Check = "exit 5"
	if res, err = Run(ctx, depot, wt); err != nil || res.Pass || res.ExitCode != 5 {
		t.Fatalf("failing override: %+v %v", res, err)
	}
}

func TestCheckNoneAndNoShebang(t *testing.T) {
	ctx := context.Background()
	depot, _ := depotWithBead(t, "")
	res, err := Run(ctx, depot, "")
	if err != nil || res.Pass || res.Source != "" || res.Dir != depot.Path || !strings.Contains(res.Output, "no gate") {
		t.Fatalf("a depot without a check must fail with no gate: %+v %v", res, err)
	}
	depot.AllowNoCheck = true
	if res, err = Run(ctx, depot, ""); err != nil || !res.Pass || res.Source != "" {
		t.Fatalf("allow_no_check: a depot without a check must pass: %+v %v", res, err)
	}
	if res.From != "main (no remote)" {
		t.Fatalf("from = %q, want the local base with no remote", res.From)
	}
	depot.AllowNoCheck = false

	// A script without #! runs with sh; empty dir means the depot itself.
	commitCheck(t, depot.Path, "echo plain sh\n")
	if res, err = Run(ctx, depot, ""); err != nil || !res.Pass || !strings.Contains(res.Output, "plain sh") {
		t.Fatalf("no-shebang script: %+v %v", res, err)
	}
	clean(t, depot.Path)

	depot.Base = "nope"
	if _, err := Run(ctx, depot, ""); err == nil {
		t.Fatal("unresolvable base not reported")
	}
}

// A depot whose base has a remote: a bare "origin" with main, cloned into the
// depot, and a bead worktree on yard/b1. base is "main" or "origin/main".
func depotWithRemote(t *testing.T, base, baseScript string) (store.Depot, string, string) {
	t.Helper()
	seed := t.TempDir()
	gitT(t, seed, "init", "-q", "-b", "main")
	gitT(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	commitCheck(t, seed, baseScript)
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitT(t, seed, "clone", "-q", "--bare", seed, origin)
	repo := filepath.Join(t.TempDir(), "depot")
	gitT(t, seed, "clone", "-q", origin, repo)
	wt := filepath.Join(t.TempDir(), "b1")
	gitT(t, repo, "worktree", "add", "-q", "-b", "yard/b1", wt)
	return store.Depot{Name: "r", Path: repo, Base: base}, wt, seed
}

// With a remote, the gate is read from the fetched tracking ref: a bead that
// moves the shared local base (or the tracking ref itself) to a commit with
// a passing check changes nothing, and a gate pushed to the remote applies
// without anyone pulling it.
func TestCheckReadsTheFetchedRemote(t *testing.T) {
	for _, base := range []string{"main", "origin/main"} {
		t.Run(base, func(t *testing.T) {
			ctx := context.Background()
			depot, wt, seed := depotWithRemote(t, base, "#!/bin/sh\necho remote gate\nexit 3\n")
			commitCheck(t, wt, "#!/bin/sh\necho bead gate\nexit 0\n")
			// Refs are shared by every worktree: the bead points them at itself.
			gitT(t, wt, "update-ref", "refs/heads/main", "HEAD")
			gitT(t, wt, "update-ref", "refs/remotes/origin/main", "HEAD")

			res, err := Run(ctx, depot, wt)
			if err != nil {
				t.Fatal(err)
			}
			sha := gitT(t, seed, "rev-parse", "main")
			if res.Pass || res.ExitCode != 3 || res.Source != "repo@"+sha[:12] || res.From != "origin/main (fetched)" {
				t.Fatalf("moved base ref changed the gate: %+v", res)
			}
			if !strings.Contains(res.Output, "remote gate") || strings.Contains(res.Output, "bead gate") {
				t.Fatalf("ran the wrong gate:\n%s", res.Output)
			}
			clean(t, wt)

			// A new gate on the remote applies at the next check.
			commitCheck(t, seed, "#!/bin/sh\necho new remote gate\n")
			gitT(t, seed, "push", "-q", gitT(t, depot.Path, "remote", "get-url", "origin"), "main")
			if res, err = Run(ctx, depot, wt); err != nil || !res.Pass || !strings.Contains(res.Output, "new remote gate") {
				t.Fatalf("pushed gate not used: %+v %v", res, err)
			}
		})
	}
}

// A remote that cannot be reached leaves the tracking ref as last fetched,
// and the result says so; a base with no remote reads the local branch.
func TestCheckFetchFailsOrNoRemote(t *testing.T) {
	ctx := context.Background()
	depot, wt, _ := depotWithRemote(t, "main", "#!/bin/sh\necho remote gate\n")
	gitT(t, depot.Path, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	res, err := Run(ctx, depot, wt)
	if err != nil || !res.Pass || !strings.HasPrefix(res.From, "origin/main (fetch failed, as last fetched") {
		t.Fatalf("unreachable remote: %+v %v", res, err)
	}

	gitT(t, depot.Path, "remote", "remove", "origin")
	commitCheck(t, depot.Path, "#!/bin/sh\necho local gate\n")
	if res, err = Run(ctx, depot, wt); err != nil || !res.Pass || res.From != "main (no remote)" || !strings.Contains(res.Output, "local gate") {
		t.Fatalf("no remote: %+v %v", res, err)
	}
}

func TestTail(t *testing.T) {
	if got := Tail("a\nb\nc\n", 2); got != "b\nc" {
		t.Fatalf("Tail = %q", got)
	}
	if got := Tail("a\n", -1); got != "" {
		t.Fatalf("Tail with a negative count = %q", got)
	}
	if got := Tail("a\n", 5); got != "a" {
		t.Fatalf("Tail = %q", got)
	}
}

// A check past its deadline is killed with everything it started (a child
// holding the output open included) and fails as timed out.
func TestCheckDeadlineKillsTheGroup(t *testing.T) {
	depot := store.Depot{Name: "r", Path: t.TempDir(), Check: "echo started; sleep 30 & sleep 30"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	began := time.Now()
	res, err := Run(ctx, depot, "")
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 3*time.Second {
		t.Fatalf("Run took %s past a 300ms deadline", took)
	}
	if res.Pass || !res.TimedOut || res.ExitCode != -1 || res.Signal != 0 || !strings.Contains(res.Output, "started") ||
		!strings.Contains(res.Output, "deadline") {
		t.Fatalf("result = %+v", res)
	}
}

// A check ended by a signal says which: its own process killed, or a
// command of its script killed and the shell exiting 128+N. The kill at the
// deadline (above) is not one, nor is an ordinary failure.
func TestCheckEndedBySignal(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		check        string
		exit, signal int
	}{
		{"echo ok; kill -TERM $$", -1, 15},
		{"sh -c 'kill -TERM $$'; exit $?", 143, 15},
		{"exit 137", 137, 9},
		{"exit 1", 1, 0},
		{"exit 128", 128, 0},
		{"exit 255", 255, 0},
	} {
		res, err := Run(ctx, store.Depot{Name: "r", Path: t.TempDir(), Check: c.check}, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Pass || res.TimedOut || res.ExitCode != c.exit || res.Signal != c.signal {
			t.Errorf("%q: exit %d signal %d (pass %v, timed out %v), want exit %d signal %d",
				c.check, res.ExitCode, res.Signal, res.Pass, res.TimedOut, c.exit, c.signal)
		}
	}
}

// Each git call finding the gate is bounded: a wedged git fails the run
// instead of hanging it.
func TestGitCallsAreBounded(t *testing.T) {
	depot, wt := depotWithBead(t, "#!/bin/sh\nexit 0\n")
	old := GitTimeout
	GitTimeout = time.Nanosecond
	defer func() { GitTimeout = old }()
	if _, err := Run(context.Background(), depot, wt); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Run with git timing out = %v", err)
	}
}

// A dir depot's gate is its directory's .yardr/check, run in place; without
// one it passes, unlike a git depot: there is nothing to merge.
func TestCheckDirDepot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	depot := store.Depot{Name: "docs", Path: dir, Kind: store.DepotDir}
	res, err := Run(ctx, depot, "")
	if err != nil || !res.Pass || res.Source != "" || res.Dir != dir || res.Kind != store.DepotDir {
		t.Fatalf("dir depot without a check must pass with no gate: %+v %v", res, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(script string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, RepoPath), []byte(script), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, RepoPath), mode); err != nil {
			t.Fatal(err)
		}
	}
	// Not executable, no #!: run with sh, in the directory given.
	write("pwd; echo $YARDR_DEPOT; exit 3\n", 0o644)
	sub := filepath.Join(dir, "2025-09")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err = Run(ctx, depot, sub)
	if err != nil || res.Pass || res.ExitCode != 3 || res.Source != "dir" || !strings.Contains(res.Output, sub+"\ndocs") {
		t.Fatalf("failing dir check: %+v %v", res, err)
	}
	// Executable with #!: run as is, edits count at once.
	write("#!/bin/sh\necho shebang\n", 0o755)
	if res, err = Run(ctx, depot, ""); err != nil || !res.Pass || !strings.Contains(res.Output, "shebang") || res.Dir != dir {
		t.Fatalf("passing dir check: %+v %v", res, err)
	}
	// The store override wins here too.
	depot.Check = "exit 7"
	if res, err = Run(ctx, depot, ""); err != nil || res.Pass || res.Source != "store" || res.ExitCode != 7 {
		t.Fatalf("override on a dir depot: %+v %v", res, err)
	}
}

// commitLight commits .yardr/check-light in dir.
func commitLight(t *testing.T, dir, script string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, LightPath), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", LightPath)
	gitT(t, dir, "commit", "-q", "-m", "check-light")
}

// The light gate is read from the base like the full one: a bead branch that
// adds or rewrites .yardr/check-light changes nothing for itself, and the
// full gate is not what runs.
func TestLightReadsTheBaseNotTheBead(t *testing.T) {
	ctx := context.Background()
	depot, wt := depotWithBead(t, "#!/bin/sh\necho full gate\nexit 4\n")
	commitLight(t, depot.Path, "#!/bin/sh\necho base light in \"$(pwd)\"\nexit 3\n")
	commitLight(t, wt, "#!/bin/sh\necho bead light\nexit 0\n")

	res, err := RunLight(ctx, depot, wt)
	if err != nil {
		t.Fatal(err)
	}
	sha := gitT(t, depot.Path, "rev-parse", "main")
	if res.Pass || res.ExitCode != 3 || res.Source != "repo@"+sha[:12] || !res.Light || res.Fallback != "" || res.Base != sha {
		t.Fatalf("result = %+v", res)
	}
	real, _ := filepath.EvalSymlinks(wt)
	if !strings.Contains(res.Output, "base light in "+real) || strings.Contains(res.Output, "bead light") ||
		strings.Contains(res.Output, "full gate") {
		t.Fatalf("ran the wrong gate, or not in the worktree:\n%s", res.Output)
	}
	clean(t, wt)

	// The full gate is still the full gate.
	if res, err = Run(ctx, depot, wt); err != nil || res.ExitCode != 4 || res.Light || !strings.Contains(res.Output, "full gate") {
		t.Fatalf("full gate beside a light one: %+v %v", res, err)
	}
}

// A depot without a light gate runs its full gate when asked for the light
// one, and says so: a light gate only a bead's branch holds is no gate, and
// asking for one never passes for want of a file.
func TestLightFallsBackToTheFullGate(t *testing.T) {
	ctx := context.Background()
	depot, wt := depotWithBead(t, "#!/bin/sh\necho full gate\nexit 4\n")
	commitLight(t, wt, "#!/bin/sh\necho bead light\nexit 0\n")
	res, err := RunLight(ctx, depot, wt)
	if err != nil {
		t.Fatal(err)
	}
	want := "depot r has no light gate (no --check-light, no .yardr/check-light on main (no remote)): running the full gate"
	if res.Pass || res.ExitCode != 4 || res.Light || res.Fallback != want || !strings.Contains(res.Output, "full gate") ||
		strings.Contains(res.Output, "bead light") {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(res.Fallback, "\n") {
		t.Fatalf("the fallback is said in one line: %q", res.Fallback)
	}

	// No gate of either kind still fails.
	bare, _ := depotWithBead(t, "")
	if res, err = RunLight(ctx, bare, ""); err != nil || res.Pass || res.Source != "" || !strings.Contains(res.Output, "no gate") {
		t.Fatalf("a depot with no gate at all must fail under --light too: %+v %v", res, err)
	}
}

// The store's light override wins over the repo's file; a depot whose full
// gate is a store override, with no light one beside it, runs that full
// gate, not a light one from its repository.
func TestLightStoreOverride(t *testing.T) {
	ctx := context.Background()
	depot, wt := depotWithBead(t, "#!/bin/sh\nexit 1\n")
	commitLight(t, depot.Path, "#!/bin/sh\necho repo light\nexit 0\n")
	depot.CheckLight = "echo store light; exit 6"
	res, err := RunLight(ctx, depot, wt)
	if err != nil || res.Pass || res.ExitCode != 6 || res.Source != "store" || !res.Light || res.Fallback != "" {
		t.Fatalf("light override: %+v %v", res, err)
	}
	// It is not the full gate's override.
	if res, err = Run(ctx, depot, wt); err != nil || res.ExitCode != 1 || res.Light || strings.Contains(res.Output, "store light") {
		t.Fatalf("full gate with a light override: %+v %v", res, err)
	}
	depot.CheckLight, depot.Check = "", "echo store full; exit 7"
	res, err = RunLight(ctx, depot, wt)
	if err != nil || res.ExitCode != 7 || res.Light || !strings.Contains(res.Fallback, "store override") ||
		strings.Contains(res.Output, "repo light") {
		t.Fatalf("a store gate without a light one: %+v %v", res, err)
	}
}

// The light gate runs under the full one's rules: killed at the deadline,
// and a signal that ends it is told apart from a failing check.
func TestLightDeadlineAndSignal(t *testing.T) {
	depot := store.Depot{Name: "r", Path: t.TempDir(), Check: "exit 0", CheckLight: "echo started; sleep 30 & sleep 30"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	res, err := RunLight(ctx, depot, "")
	if err != nil || res.Pass || !res.TimedOut || res.ExitCode != -1 || !res.Light {
		t.Fatalf("deadline: %+v %v", res, err)
	}
	depot.CheckLight = "sh -c 'kill -TERM $$'; exit $?"
	res, err = RunLight(context.Background(), depot, "")
	if err != nil || res.Pass || res.ExitCode != 143 || res.Signal != 15 {
		t.Fatalf("signal: %+v %v", res, err)
	}
}

// A check is told the commit it is compared with: where the repo's gate was
// read, which is the fetched remote's tip, not a local base a bead moved;
// under a store override the base as it resolves locally; nothing where
// there is none, whatever this process was itself started with.
func TestCheckBaseInEnvironment(t *testing.T) {
	ctx := context.Background()
	t.Setenv(BaseVar, "an-outer-checks-base")
	say := "#!/bin/sh\necho base=${" + BaseVar + "-unset}\n"

	depot, wt, _ := depotWithRemote(t, "main", say)
	commitLight(t, wt, "#!/bin/sh\nexit 0\n")
	want := gitT(t, depot.Path, "rev-parse", "origin/main")
	gitT(t, depot.Path, "update-ref", "refs/heads/main", gitT(t, wt, "rev-parse", "HEAD"))
	for _, runIt := range []func(context.Context, store.Depot, string) (*Result, error){Run, RunLight} {
		res, err := runIt(ctx, depot, wt)
		if err != nil || !res.Pass || res.Base != want || !strings.Contains(res.Output, "base="+want+"\n") {
			t.Fatalf("base of a repo gate: %+v %v, want %s", res, err, want)
		}
	}

	local, lwt := depotWithBead(t, "")
	local.Check = "echo base=${" + BaseVar + "-unset}"
	want = gitT(t, local.Path, "rev-parse", "main")
	if res, err := Run(ctx, local, lwt); err != nil || res.Base != want || !strings.Contains(res.Output, "base="+want+"\n") {
		t.Fatalf("base of a store gate: %+v %v, want %s", res, err, want)
	}
	local.Base = "nope"
	if res, err := Run(ctx, local, lwt); err != nil || !res.Pass || res.Base != "" || !strings.Contains(res.Output, "base=unset") {
		t.Fatalf("a store gate whose base does not resolve: %+v %v", res, err)
	}

	dir := store.Depot{Name: "docs", Path: t.TempDir(), Kind: store.DepotDir, Check: local.Check}
	if res, err := Run(ctx, dir, ""); err != nil || res.Base != "" || !strings.Contains(res.Output, "base=unset") {
		t.Fatalf("a dir depot has no base: %+v %v", res, err)
	}
}

// A dir depot's light gate is its directory's .yardr/check-light, else its
// .yardr/check with the fallback said, else no gate, which passes.
func TestLightDirDepot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	depot := store.Depot{Name: "docs", Path: dir, Kind: store.DepotDir}
	if res, err := RunLight(ctx, depot, ""); err != nil || !res.Pass || res.Source != "" || res.Fallback != "" {
		t.Fatalf("dir depot without any check: %+v %v", res, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, RepoPath), []byte("echo full; exit 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RunLight(ctx, depot, "")
	if err != nil || res.ExitCode != 3 || res.Light || !strings.Contains(res.Fallback, "running the full gate") {
		t.Fatalf("dir depot with only a full check: %+v %v", res, err)
	}
	if err := os.WriteFile(filepath.Join(dir, LightPath), []byte("echo light; exit 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err = RunLight(ctx, depot, ""); err != nil || res.ExitCode != 5 || !res.Light || res.Fallback != "" {
		t.Fatalf("dir depot with a light check: %+v %v", res, err)
	}
}

// Options.Since compares the light gate with another branch's tip, as the
// directory it runs in has it; the gate is still the base's. The full gate
// keeps the base, the one run for want of a light one too, and so does a
// light gate whose branch is not there.
func TestLightGateSinceABranch(t *testing.T) {
	ctx := context.Background()
	say := "#!/bin/sh\necho base=${" + BaseVar + "-unset}\n"
	depot, wt := depotWithBead(t, say)
	main := gitT(t, depot.Path, "rev-parse", "main")
	// The train's branch is a commit ahead of main: an earlier car's.
	gitT(t, wt, "commit", "-q", "--allow-empty", "-m", "an earlier car")
	gitT(t, depot.Path, "branch", "yard/e", "yard/b1")
	train := gitT(t, depot.Path, "rev-parse", "yard/e")
	gitT(t, wt, "commit", "-q", "--allow-empty", "-m", "this car")
	run := func(o Options) *Result {
		t.Helper()
		res, err := RunWith(ctx, depot, wt, o)
		if err != nil || !res.Pass || !strings.Contains(res.Output, "base="+res.Base+"\n") {
			t.Fatalf("%+v: %+v %v", o, res, err)
		}
		return res
	}

	// No light gate yet: the full one runs under --light, with the base.
	if res := run(Options{Light: true, Since: "yard/e"}); res.Base != main || res.BaseRef != "main" || res.Fallback == "" {
		t.Fatalf("full gate for want of a light one: base %s (%s), want main %s", res.Base, res.BaseRef, main)
	}
	commitLight(t, depot.Path, say)
	main = gitT(t, depot.Path, "rev-parse", "main")
	if res := run(Options{Light: true, Since: "yard/e"}); res.Base != train || res.BaseRef != "yard/e" || res.Source != "repo@"+main[:12] {
		t.Fatalf("light gate since yard/e: base %s (%s) read at %s, want %s, read at main", res.Base, res.BaseRef, res.Source, train)
	}
	if res := run(Options{Light: true}); res.Base != main || res.BaseRef != "main" {
		t.Fatalf("light gate: base %s (%s), want main %s", res.Base, res.BaseRef, main)
	}
	if res := run(Options{Since: "yard/e"}); res.Base != main || res.BaseRef != "main" {
		t.Fatalf("full gate: base %s (%s), want main %s", res.Base, res.BaseRef, main)
	}
	if res := run(Options{Light: true, Since: "yard/nope"}); res.Base != main || res.BaseRef != "main" {
		t.Fatalf("light gate since a branch that is not there: base %s (%s), want main %s", res.Base, res.BaseRef, main)
	}

	// A store override's light gate is compared the same way.
	depot.Check, depot.CheckLight = "exit 1", "echo base=${"+BaseVar+"-unset}"
	if res := run(Options{Light: true, Since: "yard/e"}); res.Base != train || res.BaseRef != "yard/e" {
		t.Fatalf("store light gate since yard/e: base %s (%s), want %s", res.Base, res.BaseRef, train)
	}
}

// A gate's command does not get the settings of the yard's server from
// whoever ran the gate (the daemon's assembly, an agent, a shell they were
// exported in): its tests start servers, and one that read this yard's
// YARDR_HOOK_ADDR listened on the yard's webhook address and answered its
// deliveries with 404 (yardr-t9n1). What else the starter had still reaches it.
func TestCheckDoesNotInheritServerSettings(t *testing.T) {
	names := []string{"YARDR_HOOK_ADDR", "YARD_HOOK_ADDR", "YARDR_EVENT_RETENTION", "YARD_EVENT_RETENTION",
		"YARDR_HOOK_PAYLOAD_RETENTION", "YARD_HOOK_PAYLOAD_RETENTION"}
	for _, n := range names {
		t.Setenv(n, "the-yards")
	}
	t.Setenv("YARDR_T9N1_KEPT", "kept")
	depot, wt := depotWithBead(t, "")
	depot.Check, depot.CheckLight = "env", "env"
	for _, runIt := range []func(context.Context, store.Depot, string) (*Result, error){Run, RunLight} {
		res, err := runIt(context.Background(), depot, wt)
		if err != nil || !res.Pass {
			t.Fatalf("%+v %v", res, err)
		}
		for _, n := range names {
			if strings.Contains(res.Output, n+"=") {
				t.Errorf("the gate's command got %s:\n%s", n, res.Output)
			}
		}
		if !strings.Contains(res.Output, "YARDR_T9N1_KEPT=kept\n") || !strings.Contains(res.Output, "YARDR_DEPOT="+depot.Name+"\n") ||
			!strings.Contains(res.Output, "YARDR_RIG="+depot.Name+"\n") {
			t.Errorf("the gate's command lost the rest of its environment:\n%s", res.Output)
		}
	}
}
