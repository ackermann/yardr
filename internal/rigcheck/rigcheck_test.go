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

// A rig on main with .yardr/check, and a bead worktree on yard/b1.
func rigWithBead(t *testing.T, baseScript string) (store.Rig, string) {
	t.Helper()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	if baseScript != "" {
		commitCheck(t, repo, baseScript)
	}
	wt := filepath.Join(t.TempDir(), "b1")
	gitT(t, repo, "worktree", "add", "-q", "-b", "yard/b1", wt)
	return store.Rig{Name: "r", Path: repo, Base: "main"}, wt
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
	rig, wt := rigWithBead(t, "#!/bin/sh\necho base gate in \"$(pwd)\"\nexit 3\n")
	commitCheck(t, wt, "#!/bin/sh\necho bead gate\nexit 0\n")

	res, err := Run(ctx, rig, wt)
	if err != nil {
		t.Fatal(err)
	}
	sha := gitT(t, rig.Path, "rev-parse", "main")
	if res.Pass || res.ExitCode != 3 || res.Source != "repo@"+sha[:12] {
		t.Fatalf("result = %+v", res)
	}
	real, _ := filepath.EvalSymlinks(wt)
	if !strings.Contains(res.Output, "base gate in "+real) || strings.Contains(res.Output, "bead gate") {
		t.Fatalf("ran the wrong gate, or not in the worktree:\n%s", res.Output)
	}
	clean(t, wt)

	// Once the change lands on the base, it is the gate.
	gitT(t, rig.Path, "merge", "-q", "--ff-only", "yard/b1")
	if res, err = Run(ctx, rig, wt); err != nil || !res.Pass || !strings.Contains(res.Output, "bead gate") {
		t.Fatalf("landed gate not used: %+v %v", res, err)
	}
}

// The store's --check wins over the repo's file.
func TestCheckStoreOverrideWins(t *testing.T) {
	ctx := context.Background()
	rig, wt := rigWithBead(t, "#!/bin/sh\nexit 1\n")
	rig.Check = "echo override in $YARDR_RIG; test -d .git || test -f .git"
	res, err := Run(ctx, rig, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass || res.Source != "store" || !strings.Contains(res.Output, "override in r") {
		t.Fatalf("result = %+v", res)
	}
	rig.Check = "exit 5"
	if res, err = Run(ctx, rig, wt); err != nil || res.Pass || res.ExitCode != 5 {
		t.Fatalf("failing override: %+v %v", res, err)
	}
}

func TestCheckNoneAndNoShebang(t *testing.T) {
	ctx := context.Background()
	rig, _ := rigWithBead(t, "")
	res, err := Run(ctx, rig, "")
	if err != nil || res.Pass || res.Source != "" || res.Dir != rig.Path || !strings.Contains(res.Output, "no gate") {
		t.Fatalf("a rig without a check must fail with no gate: %+v %v", res, err)
	}
	rig.AllowNoCheck = true
	if res, err = Run(ctx, rig, ""); err != nil || !res.Pass || res.Source != "" {
		t.Fatalf("allow_no_check: a rig without a check must pass: %+v %v", res, err)
	}
	if res.From != "main (no remote)" {
		t.Fatalf("from = %q, want the local base with no remote", res.From)
	}
	rig.AllowNoCheck = false

	// A script without #! runs with sh; empty dir means the rig itself.
	commitCheck(t, rig.Path, "echo plain sh\n")
	if res, err = Run(ctx, rig, ""); err != nil || !res.Pass || !strings.Contains(res.Output, "plain sh") {
		t.Fatalf("no-shebang script: %+v %v", res, err)
	}
	clean(t, rig.Path)

	rig.Base = "nope"
	if _, err := Run(ctx, rig, ""); err == nil {
		t.Fatal("unresolvable base not reported")
	}
}

// A rig whose base has a remote: a bare "origin" with main, cloned into the
// rig, and a bead worktree on yard/b1. base is "main" or "origin/main".
func rigWithRemote(t *testing.T, base, baseScript string) (store.Rig, string, string) {
	t.Helper()
	seed := t.TempDir()
	gitT(t, seed, "init", "-q", "-b", "main")
	gitT(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	commitCheck(t, seed, baseScript)
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitT(t, seed, "clone", "-q", "--bare", seed, origin)
	repo := filepath.Join(t.TempDir(), "rig")
	gitT(t, seed, "clone", "-q", origin, repo)
	wt := filepath.Join(t.TempDir(), "b1")
	gitT(t, repo, "worktree", "add", "-q", "-b", "yard/b1", wt)
	return store.Rig{Name: "r", Path: repo, Base: base}, wt, seed
}

// With a remote, the gate is read from the fetched tracking ref: a bead that
// moves the shared local base (or the tracking ref itself) to a commit with
// a passing check changes nothing, and a gate pushed to the remote applies
// without anyone pulling it.
func TestCheckReadsTheFetchedRemote(t *testing.T) {
	for _, base := range []string{"main", "origin/main"} {
		t.Run(base, func(t *testing.T) {
			ctx := context.Background()
			rig, wt, seed := rigWithRemote(t, base, "#!/bin/sh\necho remote gate\nexit 3\n")
			commitCheck(t, wt, "#!/bin/sh\necho bead gate\nexit 0\n")
			// Refs are shared by every worktree: the bead points them at itself.
			gitT(t, wt, "update-ref", "refs/heads/main", "HEAD")
			gitT(t, wt, "update-ref", "refs/remotes/origin/main", "HEAD")

			res, err := Run(ctx, rig, wt)
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
			gitT(t, seed, "push", "-q", gitT(t, rig.Path, "remote", "get-url", "origin"), "main")
			if res, err = Run(ctx, rig, wt); err != nil || !res.Pass || !strings.Contains(res.Output, "new remote gate") {
				t.Fatalf("pushed gate not used: %+v %v", res, err)
			}
		})
	}
}

// A remote that cannot be reached leaves the tracking ref as last fetched,
// and the result says so; a base with no remote reads the local branch.
func TestCheckFetchFailsOrNoRemote(t *testing.T) {
	ctx := context.Background()
	rig, wt, _ := rigWithRemote(t, "main", "#!/bin/sh\necho remote gate\n")
	gitT(t, rig.Path, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	res, err := Run(ctx, rig, wt)
	if err != nil || !res.Pass || !strings.HasPrefix(res.From, "origin/main (fetch failed, as last fetched") {
		t.Fatalf("unreachable remote: %+v %v", res, err)
	}

	gitT(t, rig.Path, "remote", "remove", "origin")
	commitCheck(t, rig.Path, "#!/bin/sh\necho local gate\n")
	if res, err = Run(ctx, rig, wt); err != nil || !res.Pass || res.From != "main (no remote)" || !strings.Contains(res.Output, "local gate") {
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
	rig := store.Rig{Name: "r", Path: t.TempDir(), Check: "echo started; sleep 30 & sleep 30"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	began := time.Now()
	res, err := Run(ctx, rig, "")
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
		res, err := Run(ctx, store.Rig{Name: "r", Path: t.TempDir(), Check: c.check}, "")
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
	rig, wt := rigWithBead(t, "#!/bin/sh\nexit 0\n")
	old := GitTimeout
	GitTimeout = time.Nanosecond
	defer func() { GitTimeout = old }()
	if _, err := Run(context.Background(), rig, wt); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Run with git timing out = %v", err)
	}
}

// A dir rig's gate is its directory's .yardr/check, run in place; without
// one it passes, unlike a git rig: there is nothing to merge.
func TestCheckDirRig(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rig := store.Rig{Name: "docs", Path: dir, Kind: store.RigDir}
	res, err := Run(ctx, rig, "")
	if err != nil || !res.Pass || res.Source != "" || res.Dir != dir || res.Kind != store.RigDir {
		t.Fatalf("dir rig without a check must pass with no gate: %+v %v", res, err)
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
	write("pwd; echo $YARDR_RIG; exit 3\n", 0o644)
	sub := filepath.Join(dir, "2025-09")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	res, err = Run(ctx, rig, sub)
	if err != nil || res.Pass || res.ExitCode != 3 || res.Source != "dir" || !strings.Contains(res.Output, sub+"\ndocs") {
		t.Fatalf("failing dir check: %+v %v", res, err)
	}
	// Executable with #!: run as is, edits count at once.
	write("#!/bin/sh\necho shebang\n", 0o755)
	if res, err = Run(ctx, rig, ""); err != nil || !res.Pass || !strings.Contains(res.Output, "shebang") || res.Dir != dir {
		t.Fatalf("passing dir check: %+v %v", res, err)
	}
	// The store override wins here too.
	rig.Check = "exit 7"
	if res, err = Run(ctx, rig, ""); err != nil || res.Pass || res.Source != "store" || res.ExitCode != 7 {
		t.Fatalf("override on a dir rig: %+v %v", res, err)
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
	rig, wt := rigWithBead(t, "#!/bin/sh\necho full gate\nexit 4\n")
	commitLight(t, rig.Path, "#!/bin/sh\necho base light in \"$(pwd)\"\nexit 3\n")
	commitLight(t, wt, "#!/bin/sh\necho bead light\nexit 0\n")

	res, err := RunLight(ctx, rig, wt)
	if err != nil {
		t.Fatal(err)
	}
	sha := gitT(t, rig.Path, "rev-parse", "main")
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
	if res, err = Run(ctx, rig, wt); err != nil || res.ExitCode != 4 || res.Light || !strings.Contains(res.Output, "full gate") {
		t.Fatalf("full gate beside a light one: %+v %v", res, err)
	}
}

// A rig without a light gate runs its full gate when asked for the light
// one, and says so: a light gate only a bead's branch holds is no gate, and
// asking for one never passes for want of a file.
func TestLightFallsBackToTheFullGate(t *testing.T) {
	ctx := context.Background()
	rig, wt := rigWithBead(t, "#!/bin/sh\necho full gate\nexit 4\n")
	commitLight(t, wt, "#!/bin/sh\necho bead light\nexit 0\n")
	res, err := RunLight(ctx, rig, wt)
	if err != nil {
		t.Fatal(err)
	}
	want := "rig r has no light gate (no --check-light, no .yardr/check-light on main (no remote)): running the full gate"
	if res.Pass || res.ExitCode != 4 || res.Light || res.Fallback != want || !strings.Contains(res.Output, "full gate") ||
		strings.Contains(res.Output, "bead light") {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(res.Fallback, "\n") {
		t.Fatalf("the fallback is said in one line: %q", res.Fallback)
	}

	// No gate of either kind still fails.
	bare, _ := rigWithBead(t, "")
	if res, err = RunLight(ctx, bare, ""); err != nil || res.Pass || res.Source != "" || !strings.Contains(res.Output, "no gate") {
		t.Fatalf("a rig with no gate at all must fail under --light too: %+v %v", res, err)
	}
}

// The store's light override wins over the repo's file; a rig whose full
// gate is a store override, with no light one beside it, runs that full
// gate, not a light one from its repository.
func TestLightStoreOverride(t *testing.T) {
	ctx := context.Background()
	rig, wt := rigWithBead(t, "#!/bin/sh\nexit 1\n")
	commitLight(t, rig.Path, "#!/bin/sh\necho repo light\nexit 0\n")
	rig.CheckLight = "echo store light; exit 6"
	res, err := RunLight(ctx, rig, wt)
	if err != nil || res.Pass || res.ExitCode != 6 || res.Source != "store" || !res.Light || res.Fallback != "" {
		t.Fatalf("light override: %+v %v", res, err)
	}
	// It is not the full gate's override.
	if res, err = Run(ctx, rig, wt); err != nil || res.ExitCode != 1 || res.Light || strings.Contains(res.Output, "store light") {
		t.Fatalf("full gate with a light override: %+v %v", res, err)
	}
	rig.CheckLight, rig.Check = "", "echo store full; exit 7"
	res, err = RunLight(ctx, rig, wt)
	if err != nil || res.ExitCode != 7 || res.Light || !strings.Contains(res.Fallback, "store override") ||
		strings.Contains(res.Output, "repo light") {
		t.Fatalf("a store gate without a light one: %+v %v", res, err)
	}
}

// The light gate runs under the full one's rules: killed at the deadline,
// and a signal that ends it is told apart from a failing check.
func TestLightDeadlineAndSignal(t *testing.T) {
	rig := store.Rig{Name: "r", Path: t.TempDir(), Check: "exit 0", CheckLight: "echo started; sleep 30 & sleep 30"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	res, err := RunLight(ctx, rig, "")
	if err != nil || res.Pass || !res.TimedOut || res.ExitCode != -1 || !res.Light {
		t.Fatalf("deadline: %+v %v", res, err)
	}
	rig.CheckLight = "sh -c 'kill -TERM $$'; exit $?"
	res, err = RunLight(context.Background(), rig, "")
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

	rig, wt, _ := rigWithRemote(t, "main", say)
	commitLight(t, wt, "#!/bin/sh\nexit 0\n")
	want := gitT(t, rig.Path, "rev-parse", "origin/main")
	gitT(t, rig.Path, "update-ref", "refs/heads/main", gitT(t, wt, "rev-parse", "HEAD"))
	for _, runIt := range []func(context.Context, store.Rig, string) (*Result, error){Run, RunLight} {
		res, err := runIt(ctx, rig, wt)
		if err != nil || !res.Pass || res.Base != want || !strings.Contains(res.Output, "base="+want+"\n") {
			t.Fatalf("base of a repo gate: %+v %v, want %s", res, err, want)
		}
	}

	local, lwt := rigWithBead(t, "")
	local.Check = "echo base=${" + BaseVar + "-unset}"
	want = gitT(t, local.Path, "rev-parse", "main")
	if res, err := Run(ctx, local, lwt); err != nil || res.Base != want || !strings.Contains(res.Output, "base="+want+"\n") {
		t.Fatalf("base of a store gate: %+v %v, want %s", res, err, want)
	}
	local.Base = "nope"
	if res, err := Run(ctx, local, lwt); err != nil || !res.Pass || res.Base != "" || !strings.Contains(res.Output, "base=unset") {
		t.Fatalf("a store gate whose base does not resolve: %+v %v", res, err)
	}

	dir := store.Rig{Name: "docs", Path: t.TempDir(), Kind: store.RigDir, Check: local.Check}
	if res, err := Run(ctx, dir, ""); err != nil || res.Base != "" || !strings.Contains(res.Output, "base=unset") {
		t.Fatalf("a dir rig has no base: %+v %v", res, err)
	}
}

// A dir rig's light gate is its directory's .yardr/check-light, else its
// .yardr/check with the fallback said, else no gate, which passes.
func TestLightDirRig(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	rig := store.Rig{Name: "docs", Path: dir, Kind: store.RigDir}
	if res, err := RunLight(ctx, rig, ""); err != nil || !res.Pass || res.Source != "" || res.Fallback != "" {
		t.Fatalf("dir rig without any check: %+v %v", res, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, RepoPath), []byte("echo full; exit 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := RunLight(ctx, rig, "")
	if err != nil || res.ExitCode != 3 || res.Light || !strings.Contains(res.Fallback, "running the full gate") {
		t.Fatalf("dir rig with only a full check: %+v %v", res, err)
	}
	if err := os.WriteFile(filepath.Join(dir, LightPath), []byte("echo light; exit 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err = RunLight(ctx, rig, ""); err != nil || res.ExitCode != 5 || !res.Light || res.Fallback != "" {
		t.Fatalf("dir rig with a light check: %+v %v", res, err)
	}
}
