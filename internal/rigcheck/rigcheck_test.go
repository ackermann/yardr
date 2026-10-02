package rigcheck

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
