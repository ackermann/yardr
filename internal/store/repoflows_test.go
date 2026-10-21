package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitRun is runGit for the internal tests: git in dir, failing the test.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A resolve runs on nearly every store call on a git depot, so resolveRef
// answers a loose branch tip from its file instead of starting git. What
// comes back must be git's in every case: the same bytes, the same errors,
// from the fast path or from git as ever.
func TestResolveRefMatchesGit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "first")
	gitRun(t, dir, "branch", "foo/bar")
	gitRun(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitRun(t, dir, "tag", "-a", "-m", "v1", "v1")
	gitRun(t, dir, "tag", "-a", "-m", "shadow", "shadow")
	gitRun(t, dir, "branch", "shadow")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	refs := []string{
		"main", "main^{commit}", "refs/heads/main",
		"foo/bar", "refs/remotes/origin/main",
		"v1^{commit}", "shadow", "shadow^{commit}",
		"missing", "missing^{commit}", "HEAD", head[:7],
		"main~1", "..", "-b", "refs/tags/v1",
	}
	for _, ref := range refs {
		wantOut, wantErr := git(ctx, dir, "rev-parse", "--verify", "--quiet", ref)
		gotOut, gotErr := ResolveRef(ctx, dir, ref)
		if string(gotOut) != string(wantOut) {
			t.Errorf("%s: out %q, want %q", ref, gotOut, wantOut)
		}
		if (gotErr == nil) != (wantErr == nil) ||
			errors.Is(gotErr, errGitNotFound) != errors.Is(wantErr, errGitNotFound) {
			t.Errorf("%s: err %v, want %v", ref, gotErr, wantErr)
		}
	}
	// The fast path answers the branch tips loose, and stands aside for
	// git where a tag resolves first, for HEAD, and for no such branch.
	for _, ref := range []string{"main", "main^{commit}", "refs/heads/main", "foo/bar"} {
		if sha, ok := looseRef(dir, ref); !ok || sha != head {
			t.Errorf("%s: fast (%q, %v), want (%q, true)", ref, sha, ok, head)
		}
	}
	for _, ref := range []string{"shadow", "HEAD", "missing", "..", "main~1", "refs/tags/v1", "main.lock"} {
		if sha, ok := looseRef(dir, ref); ok {
			t.Errorf("%s: fast %q, want git", ref, sha)
		}
	}
	// Packed away, the tip is no longer loose: git answers from packed-refs.
	packed := t.TempDir()
	gitRun(t, packed, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(packed, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, packed, "add", "-A")
	gitRun(t, packed, "commit", "-q", "-m", "first")
	gitRun(t, packed, "pack-refs", "--all", "--prune")
	if sha, ok := looseRef(packed, "main"); ok {
		t.Errorf("packed main: fast %q, want git", sha)
	}
	if out, err := ResolveRef(ctx, packed, "main"); err != nil || string(out) != gitRun(t, packed, "rev-parse", "HEAD")+"\n" {
		t.Errorf("packed main: resolveRef (%q, %v)", out, err)
	}
	// A worktree keeps its refs with the main checkout, and no checkout
	// has none at all: git answers the former, and refuses the latter.
	wt := filepath.Join(dir, "wt")
	gitRun(t, dir, "worktree", "add", "--detach", wt)
	if sha, ok := looseRef(wt, "main"); ok {
		t.Errorf("worktree main: fast %q, want git", sha)
	}
	if out, err := ResolveRef(ctx, wt, "main"); err != nil || string(out) != head+"\n" {
		t.Errorf("worktree main: resolveRef (%q, %v)", out, err)
	}
	bare := t.TempDir()
	if _, err := ResolveRef(ctx, bare, "main"); err == nil || errors.Is(err, errGitNotFound) {
		t.Errorf("no checkout: resolveRef err %v, want git's refusal", err)
	}
}
