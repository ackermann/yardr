package runner

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// baseRepo makes a checkout whose main has .yardr/roles/builder.md, an
// empty role and one with a space in its name, tagged v1 there, and whose
// working tree then holds another builder.md and a role main does not have.
// It returns the checkout and main's commit.
func baseRepo(t *testing.T) (repo, sha string) {
	t.Helper()
	repo = t.TempDir()
	gitT := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(rel, text string) {
		t.Helper()
		path := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitT("init", "-q", "-b", "main")
	write(".yardr/roles/builder.md", "Base builder.\nNo newline at the end")
	write(".yardr/roles/empty.md", "")
	write(".yardr/roles/two words.md", "Spaced.\n")
	gitT("add", ".yardr")
	gitT("commit", "-q", "-m", "roles")
	gitT("tag", "-a", "-m", "v1", "v1")
	sha = gitT("rev-parse", "main")
	write(".yardr/roles/builder.md", "Edited in the working tree.")
	write(".yardr/roles/reviewer.md", "Only in the working tree.")
	return repo, sha
}

// countingGit puts a git first on PATH that appends its arguments to the
// file returned, one line a process, and then runs the real one.
func countingGit(t *testing.T) (calls string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls = filepath.Join(t.TempDir(), "calls")
	fake := "#!/bin/sh\necho \"$*\" >> " + calls + "\nexec " + real + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// scriptGit puts a git first on PATH that is the shell script given.
func scriptGit(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A role the base has is read as the base has it, byte for byte, with the
// commit it was read at, and the working tree's edit does not count; a base
// that is a tag is read at the commit it names. Each read is one git
// process (yardr-b98a: it was three).
func TestReadRigFileOnTheBase(t *testing.T) {
	repo, sha := baseRepo(t)
	calls := countingGit(t)
	reads := 0
	for _, tc := range []struct{ base, name, want string }{
		{"main", "builder.md", "Base builder.\nNo newline at the end"},
		{"v1", "builder.md", "Base builder.\nNo newline at the end"},
		{sha, "builder.md", "Base builder.\nNo newline at the end"},
		{"main", "empty.md", ""},
		{"main", "two words.md", "Spaced.\n"},
	} {
		rel := filepath.Join(".yardr", "roles", tc.name)
		text, where, got, err := readRigFile(&store.Rig{Name: "r", Path: repo, Base: tc.base}, rel)
		reads++
		if err != nil || string(text) != tc.want || got != sha || where != tc.base+":.yardr/roles/"+tc.name {
			t.Errorf("%s at %s = %q, where %q, sha %q, %v; want %q at %s", tc.name, tc.base, text, where, got, err, tc.want, sha)
		}
	}
	got, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(got), "\n"); n != reads {
		t.Errorf("%d reads started %d git processes, want one each:\n%s", reads, n, got)
	}
}

// A role the base does not have is fs.ErrNotExist, named as on the base,
// though the working tree holds one; and that too is one git process.
func TestReadRigFileNotOnTheBase(t *testing.T) {
	repo, _ := baseRepo(t)
	calls := countingGit(t)
	rig := &store.Rig{Name: "r", Path: repo, Base: "main"}
	for _, name := range []string{"reviewer.md", "nobody.md"} {
		text, where, sha, err := readRigFile(rig, filepath.Join(".yardr", "roles", name))
		if !errors.Is(err, fs.ErrNotExist) || text != nil || sha != "" || where != "main:.yardr/roles/"+name {
			t.Errorf("%s = %q, where %q, sha %q, %v; want fs.ErrNotExist on the base", name, text, where, sha, err)
		}
	}
	if has, err := RigRole(rig, "reviewer.md"); has || err != nil {
		t.Errorf("RigRole of a role only in the working tree = %v, %v", has, err)
	}
	got, _ := os.ReadFile(calls)
	if n := strings.Count(string(got), "\n"); n != 3 {
		t.Errorf("3 reads started %d git processes:\n%s", n, got)
	}
}

// The working tree is read when there is no base to read from: a base the
// checkout does not know, a directory that is no checkout, a dir rig (which
// starts no git at all), a rig without a base.
func TestReadRigFileWithoutABase(t *testing.T) {
	repo, _ := baseRepo(t)
	plain := t.TempDir()
	if err := os.MkdirAll(filepath.Join(plain, ".yardr", "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, ".yardr", "roles", "builder.md"), []byte("Plain."), 0o600); err != nil {
		t.Fatal(err)
	}
	// No checkout above the plain directory either, wherever TMPDIR is.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(plain))
	rel := filepath.Join(".yardr", "roles", "builder.md")
	for _, tc := range []struct {
		what string
		rig  store.Rig
		want string
		git  int
	}{
		{"unknown base", store.Rig{Path: repo, Base: "origin/gone"}, "Edited in the working tree.", 1},
		{"a base that is two lines", store.Rig{Path: repo, Base: "main\nmain"}, "Edited in the working tree.", 0},
		{"no checkout", store.Rig{Path: plain, Base: "main"}, "Plain.", 1},
		{"dir rig", store.Rig{Path: repo, Base: "main", Kind: store.RigDir}, "Edited in the working tree.", 0},
		{"no base", store.Rig{Path: repo}, "Edited in the working tree.", 0},
	} {
		t.Run(tc.what, func(t *testing.T) {
			calls := countingGit(t)
			text, where, sha, err := readRigFile(&tc.rig, rel)
			if err != nil || string(text) != tc.want || sha != "" || where != filepath.Join(tc.rig.Path, rel) {
				t.Errorf("= %q, where %q, sha %q, %v; want %q from the working tree", text, where, sha, err, tc.want)
			}
			_, _, _, err = readRigFile(&tc.rig, filepath.Join(".yardr", "roles", "nobody.md"))
			if !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a role the working tree does not have: %v", err)
			}
			got, _ := os.ReadFile(calls)
			if n := strings.Count(string(got), "\n"); n != 2*tc.git {
				t.Errorf("2 reads started %d git processes, want %d:\n%s", n, 2*tc.git, got)
			}
		})
	}
}

// A git that fails once it has named the base's commit is an error with
// what git said, not a role the base does not have, and the working tree's
// copy is not read in its place.
func TestReadRigFileGitFails(t *testing.T) {
	repo, sha := baseRepo(t)
	rig := &store.Rig{Name: "r", Path: repo, Base: "main"}
	rel := filepath.Join(".yardr", "roles", "builder.md")
	for what, script := range map[string]string{
		"dies before the file":  "read name\nprintf '%s commit 0\\n\\n' " + sha + "\nread name\necho 'fatal: object store is gone' >&2\nexit 128\n",
		"dies in the file":      "read name\nprintf '%s commit 0\\n\\n' " + sha + "\nread name\nprintf '%s blob 500\\nBase b' " + sha + "\necho 'fatal: object store is gone' >&2\nexit 128\n",
		"fails after the file":  "read name\nprintf '%s commit 0\\n\\n' " + sha + "\nread name\nprintf '%s blob 2\\nhi\\n' " + sha + "\necho 'fatal: object store is gone' >&2\nexit 128\n",
		"answers what git does": "read name\necho 'usage: git cat-file'\necho 'fatal: object store is gone' >&2\nexit 129\n",
	} {
		t.Run(what, func(t *testing.T) {
			scriptGit(t, script)
			text, _, _, err := readRigFile(rig, rel)
			if err == nil || errors.Is(err, fs.ErrNotExist) || text != nil {
				t.Fatalf("= %q, %v; want git's failure", text, err)
			}
			if !strings.Contains(err.Error(), "object store is gone") {
				t.Errorf("error does not say what git said: %v", err)
			}
			if ok, err := RigRole(rig, "builder.md"); ok || err == nil {
				t.Errorf("RigRole with a failing git = %v, %v", ok, err)
			}
		})
	}
}

// A path of the base that is a directory is an error, not its listing.
func TestReadRigFileOfADirectory(t *testing.T) {
	repo, _ := baseRepo(t)
	text, _, _, err := readRigFile(&store.Rig{Name: "r", Path: repo, Base: "main"}, filepath.Join(".yardr", "roles"))
	if err == nil || errors.Is(err, fs.ErrNotExist) || text != nil || !strings.Contains(err.Error(), "is a tree") {
		t.Fatalf("= %q, %v", text, err)
	}
}

// A git that hangs is cut off at gitTimeout and is an error, whether it
// hangs before it has named the base or after: neither is "not on the
// base", and neither falls back to the working tree.
func TestReadRigFileHasADeadline(t *testing.T) {
	repo, sha := baseRepo(t)
	was := gitTimeout
	gitTimeout = 2 * time.Second // see TestFetchBaseHasADeadline on the margin
	t.Cleanup(func() { gitTimeout = was })
	for what, script := range map[string]string{
		"at once":        "exec sleep 30\n",
		"after the base": "read name\nprintf '%s commit 0\\n\\n' " + sha + "\nexec sleep 30\n",
	} {
		t.Run(what, func(t *testing.T) {
			scriptGit(t, script)
			start := time.Now()
			text, _, _, err := readRigFile(&store.Rig{Name: "r", Path: repo, Base: "main"}, filepath.Join(".yardr", "roles", "builder.md"))
			if took := time.Since(start); took > 10*time.Second {
				t.Fatalf("returned after %s; the deadline did not hold", took)
			}
			if !errors.Is(err, context.DeadlineExceeded) || text != nil {
				t.Fatalf("= %q, %v; want the deadline", text, err)
			}
		})
	}
	if was != 30*time.Second {
		t.Fatalf("gitTimeout = %s", was)
	}
}
