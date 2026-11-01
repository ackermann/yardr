package testload

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// holds is what a case's exec path directory has as its git.
type holds int

const (
	executable holds = iota // a file git can run
	linked                  // a link to the git beside it, as the developer directory's is
	plain                   // a file that is not executable
	directory               // a directory
	dangling                // a link to nothing
	absent                  // no git at all
	missing                 // no such directory
)

// makeDir builds, under a fresh temp dir, the exec path directory that h
// describes, laid out as the developer directory is (usr/libexec/git-core beside
// usr/bin), and returns it. Nothing in it is ever run, only looked at.
func makeDir(t *testing.T, h holds) string {
	t.Helper()
	root := t.TempDir()
	dir, bin := filepath.Join(root, "usr", "libexec", "git-core"), filepath.Join(root, "usr", "bin")
	for _, d := range []string{dir, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	switch h {
	case executable:
		err = os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"), 0o755)
	case linked:
		if err = os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"), 0o755); err == nil {
			err = os.Symlink("../../bin/git", filepath.Join(dir, "git"))
		}
	case plain:
		err = os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"), 0o644)
	case directory:
		err = os.Mkdir(filepath.Join(dir, "git"), 0o755)
	case dangling:
		err = os.Symlink("../../bin/git", filepath.Join(dir, "git"))
	case missing:
		dir = filepath.Join(root, "nowhere")
	}
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// The choice of a directory is a function of the system, of the git PATH found
// and of what that git says of itself, so each case is made up here and none runs
// git.
func TestRealGitDir(t *testing.T) {
	t.Parallel()
	said := func(dir string) (string, error) { return dir, nil }
	fails := func(dir string) (string, error) { return dir, errors.New("exit status 1") }
	nothing := func(string) (string, error) { return "", nil }
	for _, c := range []struct {
		name string
		goos string
		git  string                           // the git PATH found
		says func(dir string) (string, error) // what it says its exec path is, given the case's directory
		has  holds
		want bool // the case's directory is the answer
	}{
		{"the shim on macOS", "darwin", systemGit, said, executable, true},
		{"a link to the git the shim runs, as the developer directory has it", "darwin", systemGit, said, linked, true},
		{"on linux the git PATH found is the real one", "linux", systemGit, said, executable, false},
		{"a person's own git first", "darwin", "/usr/local/bin/git", said, executable, false},
		{"a test's stand-in first", "darwin", filepath.Join(t.TempDir(), "bin", "git"), said, executable, false},
		{"a shim that fails", "darwin", systemGit, fails, executable, false},
		// Joined with "git", no directory at all is the git PATH finds, which is
		// executable: it must still choose no directory.
		{"a shim that says nothing", "darwin", systemGit, nothing, executable, false},
		{"no git in the directory", "darwin", systemGit, said, absent, false},
		{"a git that is not executable", "darwin", systemGit, said, plain, false},
		{"a git that is a directory", "darwin", systemGit, said, directory, false},
		{"a link to nothing", "darwin", systemGit, said, dangling, false},
		{"no such directory", "darwin", systemGit, said, missing, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			dir := makeDir(t, c.has)
			execPath := func(string) (string, error) { return c.says(dir) }
			want := ""
			if c.want {
				want = dir
			}
			if got := realGitDir(c.goos, c.git, execPath); got != want {
				t.Errorf("realGitDir(%q, %q) = %q, want %q", c.goos, c.git, got, want)
			}
		})
	}
}

// gitSays is what the git PATH finds reports of itself: its version, and where
// its programs are.
func gitSays(t *testing.T) (version, execPath string) {
	t.Helper()
	said := make([]string, 2)
	for i, arg := range []string{"--version", "--exec-path"} {
		out, err := exec.Command("git", arg).Output()
		if err != nil {
			t.Fatalf("git %s: %v", arg, err)
		}
		said[i] = strings.TrimSpace(string(out))
	}
	return said[0], said[1]
}

// standIn puts a git that is never run into a new directory and returns it.
func standIn(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// PreferRealGit makes that choice on the PATH of this process. The git a test
// finds afterwards is the same git, on a Mac with the shim first and on any other
// machine, and a stand-in is still found first.
func TestPreferRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	sep := string(os.PathListSeparator)

	t.Run("the same git", func(t *testing.T) {
		before := os.Getenv("PATH")
		t.Setenv("PATH", before) // so that it is restored, whatever PreferRealGit does
		version, execPath := gitSays(t)
		// Where this is a Mac with the shim first and a git among git's programs,
		// the swap must be made: it is the whole of what this is for.
		git, _ := exec.LookPath("git")
		_, err := exec.LookPath(filepath.Join(execPath, "git"))
		swapped := runtime.GOOS == "darwin" && git == systemGit && err == nil

		PreferRealGit()

		if v, e := gitSays(t); v != version || e != execPath {
			t.Errorf("git says %q and %q after PreferRealGit, %q and %q before", v, e, version, execPath)
		}
		after := os.Getenv("PATH")
		if swapped && after == before {
			t.Errorf("PATH is as it was, with the shim's git first and a git in %s", execPath)
		}
		if after != before {
			// All that changes is a new first entry, which holds the git found now.
			dir, rest, _ := strings.Cut(after, sep)
			if rest != before {
				t.Errorf("PATH = %q after PreferRealGit, want %q after its first entry", after, before)
			}
			if got, err := exec.LookPath("git"); err != nil || got != filepath.Join(dir, "git") {
				t.Errorf("git is %q (%v) after PATH gained %q, want the git in it", got, err, dir)
			}
		}
	})

	t.Run("a stand-in first is left first", func(t *testing.T) {
		stand := standIn(t)
		t.Setenv("PATH", stand+sep+os.Getenv("PATH"))
		before := os.Getenv("PATH")

		PreferRealGit()

		if got := os.Getenv("PATH"); got != before {
			t.Errorf("PATH = %q after PreferRealGit with a stand-in git first, want %q", got, before)
		}
		// And a test's stand-in put first afterwards is first.
		later := standIn(t)
		t.Setenv("PATH", later+sep+os.Getenv("PATH"))
		if got, err := exec.LookPath("git"); err != nil || got != filepath.Join(later, "git") {
			t.Errorf("git is %q (%v), want the stand-in put first", got, err)
		}
	})
}
