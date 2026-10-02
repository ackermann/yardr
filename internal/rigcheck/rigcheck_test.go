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
	if err != nil || !res.Pass || res.Source != "" || res.Dir != rig.Path {
		t.Fatalf("a rig without a check must pass: %+v %v", res, err)
	}

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

func TestTail(t *testing.T) {
	if got := Tail("a\nb\nc\n", 2); got != "b\nc" {
		t.Fatalf("Tail = %q", got)
	}
	if got := Tail("a\n", 5); got != "a" {
		t.Fatalf("Tail = %q", got)
	}
}
