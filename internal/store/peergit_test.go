package store_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// Two yards' URLs for one remote compare equal whatever each wrote of
// credentials, of .git and of the way to say ssh; another scheme, host or
// path is another remote.
func TestRemoteForm(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/web.git":                      "https://github.com/acme/web",
		"https://jo:s3cret@GitHub.com/acme/web":                "https://github.com/acme/web",
		"https://github.com/acme/web/":                         "https://github.com/acme/web",
		"git@github.com:acme/web.git":                          "ssh://github.com/acme/web",
		"ssh://git@github.com/acme/web.git":                    "ssh://github.com/acme/web",
		"ssh://git@git.example.com:2222/acme/web":              "ssh://git.example.com:2222/acme/web",
		"/srv/git/web.git":                                     "file:///srv/git/web",
		"file:///srv/git/web.git":                              "file:///srv/git/web",
		"  https://github.com/acme/web.git\n":                  "https://github.com/acme/web",
		"":                                                     "",
		"https://github.com/acme/web\x00--upload":              "",
		"https://github.com/acme/" + strings.Repeat("w", 2100): "",
	} {
		if got := store.RemoteForm(in); got != want {
			t.Errorf("RemoteForm(%q) = %q, want %q", in, got, want)
		}
	}
	if store.RemoteForm("https://github.com/acme/web") == store.RemoteForm("ssh://github.com/acme/web") ||
		store.RemoteForm("https://github.com/acme/web") == store.RemoteForm("https://github.com/acme/api") {
		t.Error("two remotes have one form")
	}
	// A link on the way to a directory is not another remote, also while
	// the directory itself is away.
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if a, b := store.RemoteForm(filepath.Join(dir, "link", "gone.git")), store.RemoteForm(filepath.Join(real, "gone.git")); a != b {
		t.Errorf("through a link %q, without %q", a, b)
	}
}

func TestIsBeadBranch(t *testing.T) {
	for name, want := range map[string]bool{
		"yard/web-0wzb": true, "yard/yardr-4pya": true, "yard/a.b_c-1": true,
		"main": false, "yard/": false, "yard": false, "yard/a/b": false, "refs/heads/yard/x": false, "yard/-x": false,
		"yard/a..b": false, "yard/x.lock": false, "yard/x y": false, "--upload-pack=x": false, "": false,
	} {
		if got := store.IsBeadBranch(name); got != want {
			t.Errorf("IsBeadBranch(%q) = %v, want %v", name, got, want)
		}
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", file)
	git(t, dir, "commit", "-q", "-m", file)
	return git(t, dir, "rev-parse", "HEAD")
}

// A returned branch is taken into the bead's own by fast-forward only: a
// branch that is there at an earlier commit moves, with its tree where it
// is checked out, and one that the returned commit does not come after
// stays, with a refusal no further try changes.
func TestTakePeerBranchFastForwardOnly(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	remote, theirs, ours := filepath.Join(dir, "remote.git"), filepath.Join(dir, "theirs"), filepath.Join(dir, "ours")
	git(t, dir, "init", "-q", "--bare", "-b", "main", remote)
	git(t, dir, "clone", "-q", remote, theirs)
	git(t, theirs, "checkout", "-q", "-b", "main")
	base := commit(t, theirs, "a.txt")
	git(t, theirs, "push", "-q", "origin", "main")
	git(t, dir, "clone", "-q", remote, ours)
	git(t, theirs, "checkout", "-q", "-b", "yard/t-1")
	first := commit(t, theirs, "b.txt")
	git(t, theirs, "push", "-q", "origin", "yard/t-1")

	ctx := context.Background()
	fetch := func(commit string) store.PeerFetch {
		return store.PeerFetch{Bead: "o-1", Rig: store.Rig{Name: "web", Path: ours, Kind: store.RigGit, Base: "main"}, Remote: "origin", Into: "yard/o-1",
			Git: store.PeerGit{Branch: "yard/t-1", Commit: commit, Remote: remote}}
	}
	at := func() string { return git(t, ours, "rev-parse", "refs/heads/yard/o-1") }

	// There already, at the base, and checked out: moved, tree and all.
	wt := filepath.Join(dir, "wt")
	git(t, ours, "worktree", "add", "-q", "-b", "yard/o-1", wt, base)
	if why, err := store.TakePeerBranch(ctx, fetch(first)); why != "" || err != nil {
		t.Fatalf("taking it over an earlier commit = %q, %v", why, err)
	}
	if _, err := os.Stat(filepath.Join(wt, "b.txt")); at() != first || err != nil {
		t.Fatalf("the branch is at %s, want %s; its worktree has the file: %v", at(), first, err)
	}
	// Again: nothing to do.
	if why, err := store.TakePeerBranch(ctx, fetch(first)); why != "" || err != nil {
		t.Fatalf("taking it twice = %q, %v", why, err)
	}
	// A commit of this yard's own on it since: what comes back next does
	// not come after it.
	own := commit(t, wt, "own.txt")
	git(t, theirs, "checkout", "-q", "yard/t-1")
	second := commit(t, theirs, "c.txt")
	git(t, theirs, "push", "-q", "origin", "yard/t-1")
	why, err := store.TakePeerBranch(ctx, fetch(second))
	if why != "" || err == nil || !store.FinalFetch(err) || !strings.Contains(err.Error(), "it is taken by fast-forward only") || at() != own {
		t.Fatalf("taking it over a branch that went its own way = %q, %v; the branch is at %s, want %s", why, err, at(), own)
	}
	// The name of a remote is no option of git's.
	f := fetch(second)
	f.Remote = "--upload-pack=touch /tmp/x"
	if why, err := store.TakePeerBranch(ctx, f); err != nil || !strings.Contains(why, "is not the name of a remote") {
		t.Fatalf("a remote that is an option = %q, %v", why, err)
	}
	if left := git(t, ours, "for-each-ref", "refs/yardr"); left != "" {
		t.Fatalf("a ref of the fetch is left: %s", left)
	}
}

// A peer group's remote is the name of one, and nothing git would read as
// anything else.
func TestPeerGroupRemoteSetting(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.AddPeer(ctx, store.Peer{Name: "north", Key: strings.Repeat("k", 43)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AllowPeer(ctx, "north", store.PeerAllow{Send: []string{"bead"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	g := store.Group{Name: "at-north", Runner: store.PeerRunner, Config: map[string]any{store.GroupPeer: "north", store.GroupRemote: "upstream"}}
	if err := st.PutGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"--upload-pack=x", "https://example.com/x.git", "a b", ""} {
		g.Config[store.GroupRemote] = bad
		if err := st.PutGroup(ctx, g); err == nil || !strings.Contains(err.Error(), "remote is the name of the remote") {
			t.Errorf("remote=%q: %v", bad, err)
		}
	}
}
