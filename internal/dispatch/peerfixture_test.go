package dispatch_test

// Template git remote: the setup every git-backed peer test repeats, built
// once and copied per test.
//
// Every test of code that crosses with a bead starts from a bare repository
// with one commit on main (sharedRemote), of which each yard takes a clone
// of its own. Building it costs six git subprocesses, and the suite builds
// it over twenty times, once per test and subtest; the profile's slowest
// peer tests are all these (TestPeerCodeOneRemoteTwoSpellings,
// TestPeerCodeNotTaken, TestPeerCodeNotWhatTheReturnNames,
// TestPeerBaseKeptByARef, TestPeerBeadSentAgain and the rest of the
// 1-2 s peer tests).
//
// So it is built once, by the first test that needs it, and every test
// copies it into its own temp dir. A bare repository names no absolute
// path, so the copy needs no fixup, and each test's clones stay real
// clones of its own copy, so every assertion reads the same state: the
// remote's content is what a fresh setup builds, and what a test pushes
// or renames lands in its own copy alone.
//
// The template lives until the tests end: borrowers overlap, but need
// not, so no borrower's cleanup removes it; TestMain takes every built
// root away after the run. A build that fails fails its borrowers with
// the template's name instead of building again.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// peerGitTemplate is one shared setup, built into a root of its own by
// the first test that borrows it.
type peerGitTemplate struct {
	name  string
	build func(t *testing.T, root string)
	once  sync.Once
	mu    sync.Mutex
	// attempted is set by the build even when it fails the builder, so a
	// later borrower fails fast instead of building again: the failure is
	// systemic, and a rebuild would fail the same way.
	attempted bool
	ok        bool
	root      string
}

// peerTplRoots are the template roots built in this run, for TestMain to
// remove after it.
var peerTplRoots struct {
	mu   sync.Mutex
	dirs []string
}

// removePeerTemplates takes away every template root built in this run.
// TestMain calls it after m.Run.
func removePeerTemplates() {
	peerTplRoots.mu.Lock()
	defer peerTplRoots.mu.Unlock()
	for _, dir := range peerTplRoots.dirs {
		os.RemoveAll(dir)
	}
	peerTplRoots.dirs = nil
}

// borrow returns the template's root, built by the first borrower. The
// root lives until the tests end; borrowers copy out of it, and never
// write to it.
func (tpl *peerGitTemplate) borrow(t *testing.T) string {
	t.Helper()
	tpl.once.Do(func() {
		tpl.mu.Lock()
		failed := tpl.attempted && !tpl.ok
		tpl.mu.Unlock()
		if failed {
			t.Fatalf("template %s did not build", tpl.name)
		}
		// A build that fails the builder runs this too: t.Fatal ends the
		// goroutine through its defers, so the flag is set either way.
		defer func() {
			tpl.mu.Lock()
			tpl.attempted = true
			tpl.mu.Unlock()
		}()
		root, err := os.MkdirTemp("", "peertpl")
		if err != nil {
			t.Fatal(err)
		}
		peerTplRoots.mu.Lock()
		peerTplRoots.dirs = append(peerTplRoots.dirs, root)
		peerTplRoots.mu.Unlock()
		tpl.mu.Lock()
		tpl.root = root
		tpl.mu.Unlock()
		tpl.build(t, root)
		tpl.mu.Lock()
		tpl.ok = true
		tpl.mu.Unlock()
	})
	tpl.mu.Lock()
	defer tpl.mu.Unlock()
	if !tpl.ok {
		t.Fatalf("template %s did not build", tpl.name)
	}
	return tpl.root
}

// copyDir copies src into dst, which must not exist: directories, regular
// files with their modes, and symlinks as links. Anything else is a bug,
// not a state: a bare repository holds no sockets, fifos or devices.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	if err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(out, info.Mode().Perm())
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(target, out)
		case info.Mode().IsRegular():
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(out, b, info.Mode().Perm())
		default:
			return fmt.Errorf("copyDir: %s is %v", path, info.Mode())
		}
	}); err != nil {
		t.Fatal(err)
	}
}

var peerRemoteTpl = &peerGitTemplate{name: "remote", build: buildPeerRemoteTpl}

// buildPeerRemoteTpl is sharedRemote's setup, once, into root: a bare
// repository with one commit on main. The seed clone lives in the
// builder's own temp dir, which ends with the builder; the template keeps
// the bare repository alone.
func buildPeerRemoteTpl(t *testing.T, root string) {
	t.Helper()
	remote := filepath.Join(root, "remote.git")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "--bare", "-b", "main", remote)
	seed := filepath.Join(dir, "seed")
	gitIn(t, dir, "clone", "-q", remote, seed)
	gitIn(t, seed, "checkout", "-q", "-b", "main")
	commitIn(t, seed, "a.txt", "one\n")
	gitIn(t, seed, "push", "-q", "origin", "main")
}

// borrowPeerRemote is sharedRemote from the template: the same bare
// repository, copied into the test's own temp dir.
func borrowPeerRemote(t *testing.T) string {
	t.Helper()
	root := peerRemoteTpl.borrow(t)
	dst := filepath.Join(t.TempDir(), "remote.git")
	copyDir(t, filepath.Join(root, "remote.git"), dst)
	return dst
}
