package assembly

// Template yards: the setups every test repeats, built once and copied per
// test.
//
// Nearly every test in this package starts from one of a few identical
// states: a yard with a depot and a bead (newYard), a train yard with two
// children (newTrainYard), one with both children landed (landedTrainYard),
// one with its train integrated and its pull request open (landYard), or a
// bare repository with a branch in its worktree (newWatcherRun). Building
// each from git and the store API costs seconds under -race, and the suite
// builds them a hundred times over; the R1 budget (docs/testing.md) goes on
// that repeated setup, not on what the tests assert.
//
// So each state is built once, by the first test that needs it, and every
// test copies it into its own temp dir and fixes what names an absolute
// path: the depot's path in the store, the worktrees git registered, the
// origin remote, and the fake gh's call log. The copy is otherwise
// bit-identical to a fresh setup, so every assertion reads the same state:
// sessions are matched by id with no clock the tests trip over, the gh calls
// a landYard caller counts are only its own run's (pr merge is never called
// while the template is built), and no test asserts a depot's put history.
//
// The template lives until the tests end: borrowers overlap, but need not,
// so no borrower's cleanup removes it; TestMain takes every built root away
// after the run. A build that fails fails its borrowers with the template's
// name instead of building again.

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// yardTemplate is one shared setup, built into a root of its own by the
// first test that borrows it.
type yardTemplate struct {
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

// tplRoots are the template roots built in this run, for TestMain to remove
// after it.
var tplRoots struct {
	mu   sync.Mutex
	dirs []string
}

// removeTemplates takes away every template root built in this run. TestMain
// calls it after m.Run.
func removeTemplates() {
	tplRoots.mu.Lock()
	defer tplRoots.mu.Unlock()
	for _, dir := range tplRoots.dirs {
		os.RemoveAll(dir)
	}
	tplRoots.dirs = nil
}

// borrow returns the template's root, built by the first borrower. The root
// lives until the tests end; borrowers copy out of it, and never write to it.
func (tpl *yardTemplate) borrow(t *testing.T) string {
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
		root, err := os.MkdirTemp("", "yrtpl")
		if err != nil {
			t.Fatal(err)
		}
		tplRoots.mu.Lock()
		tplRoots.dirs = append(tplRoots.dirs, root)
		tplRoots.mu.Unlock()
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

// serveTemplate serves the template home's store on its socket, as serveYard
// does, and returns its closer. The server is closed before the template is
// copied: a copy of an open store is not a store. Nothing is registered on
// the test: the builder owns the template, and the close is explicit, since
// a cleanup would close it a second time when the builder ends.
func serveTemplate(t *testing.T, home string) func() {
	t.Helper()
	cfg := &config.Config{Home: home}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st.SetDaemonChecker(config.DaemonChecker(home))
	// This template owns the home, so a socket file left behind is stale.
	_ = os.Remove(cfg.Socket())
	ln, err := net.Listen("unix", cfg.Socket())
	if err != nil {
		_ = st.Close(context.Background())
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.Socket(), 0o600); err != nil {
		ln.Close()
		_ = st.Close(context.Background())
		t.Fatal(err)
	}
	watch := &reportWatch{}
	reportWatches.Store(home, watch)
	srv := &http.Server{Handler: watch.handler(rpc.Handler(st, nil)), ConnContext: rpc.ConnContext}
	go func() { _ = srv.Serve(ln) }()
	return func() {
		_ = srv.Close()
		reportWatches.Delete(home)
		_ = st.Close(context.Background())
		// The closed socket is a stale node, not a file to copy.
		_ = os.Remove(cfg.Socket())
	}
}

// pointAtCopy points each copied worktree's .git file at its record in the
// copied repository, under the record's own name, before the caller's git
// worktree repair. The copied file still names the template's record, and
// a repair follows it: a git as old as 2.43 then repoints the template's
// record at the copy and leaves the copy's own naming the template's
// worktree, so the copy's commits land in the shared template. With the
// file pointed first, the repair fixes the copy's record alone, on any git.
func pointAtCopy(t *testing.T, repo string, wts ...string) {
	t.Helper()
	for _, wt := range wts {
		dotgit := filepath.Join(wt, ".git")
		b, err := os.ReadFile(dotgit)
		if err != nil {
			t.Fatal(err)
		}
		gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
		if !ok {
			t.Fatalf("%s names no gitdir: %q", dotgit, b)
		}
		record := filepath.Join(repo, ".git", "worktrees", filepath.Base(gitdir))
		if err := os.WriteFile(dotgit, []byte("gitdir: "+record+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// copyTree copies src into dst, which must not exist: directories, regular
// files with their modes and mtimes (so git finds its index valid), and
// symlinks as links. Anything else is a bug, not a state: the templates
// hold no sockets past their close, and no fifos or devices at all. A file
// copies as a file, so a template's loose scripts copy beside its trees.
func copyTree(t *testing.T, src, dst string) {
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
			if err := os.WriteFile(out, b, info.Mode().Perm()); err != nil {
				return err
			}
			return os.Chtimes(out, info.ModTime(), info.ModTime())
		default:
			return fmt.Errorf("copyTree: %s is %v", path, info.Mode())
		}
	}); err != nil {
		t.Fatal(err)
	}
}

var yardTpl = &yardTemplate{name: "yard", build: buildYardTpl}

// buildYardTpl is newYard's setup, once, into root: a yard with depot r over
// a fresh repository and bead r-1 in approved on yard/r-1 in its worktree.
func buildYardTpl(t *testing.T, root string) {
	t.Helper()
	home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	y := &yard{t: t, home: home, repo: repo, wt: filepath.Join(home, "worktrees", "r", "r-1")}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	y.git(".", "init", "-q", "-b", "main", y.repo)
	y.commit(y.repo, "a.txt", "one\n")
	close := serveTemplate(t, home)
	defer close()
	y.initStore()
	y.putDepot("r", y.repo, "main", "test ! -e RED")
	y.putFlow("f", flow)
	y.pointFlow("r", "", "f")
	y.createBead("work", "r-1", "r", "", "approved")
	y.git(y.repo, "branch", "yard/r-1")
	y.git(y.repo, "worktree", "add", "-q", y.wt, "yard/r-1")
}

// borrowYard is newYard from the template: the same yard, copied into the
// test's own temp dir, with the worktree git registered repaired and the
// depot pointing at the copy.
func borrowYard(t *testing.T) *yard {
	t.Helper()
	t.Parallel()
	root := yardTpl.borrow(t)
	// The home is short, not the test's temp dir: it holds the store's
	// unix socket, whose path the kernel bounds.
	home, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	repo := filepath.Join(t.TempDir(), "repo")
	copyTree(t, filepath.Join(root, "home"), home)
	copyTree(t, filepath.Join(root, "repo"), repo)
	y := &yard{t: t, home: home, repo: repo, wt: filepath.Join(home, "worktrees", "r", "r-1")}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	// Repair names the copy's worktree: a bare repair fixes the worktrees
	// the copied admin registered, which are the template's, and every
	// commit after would land in the shared template.
	pointAtCopy(t, y.repo, y.wt)
	y.git(y.repo, "worktree", "repair", y.wt)
	serveYard(t, home)
	y.putDepot("r", y.repo, "main", "test ! -e RED")
	return y
}

var trainTpl = &yardTemplate{name: "train", build: buildTrainTpl}

// buildTrainTpl is newTrainYard's setup, once, into root: a yard with depot
// r over a fresh repository pushed to a bare origin, train r-e in open, and
// its children r-1 and r-2 in approved, each one commit ahead of the train's
// branch in its worktree.
func buildTrainTpl(t *testing.T, root string) {
	t.Helper()
	home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	y := &yard{t: t, home: home, repo: repo}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	y.git(".", "init", "-q", "-b", "main", y.repo)
	y.commit(y.repo, "a.txt", "one\n")
	remote := filepath.Join(root, "origin.git")
	y.git(".", "init", "-q", "--bare", remote)
	y.git(y.repo, "remote", "add", "origin", remote)
	y.git(y.repo, "push", "-q", "origin", "main")
	close := serveTemplate(t, home)
	defer close()
	y.initStore()
	y.putDepot("r", y.repo, "main", "test ! -e RED")
	y.putFlow("code", childFlow)
	y.pointFlow("r", "", "code")
	y.putFlowFile("yard.train", storetest.RepoFlow(t, "yardr.train"))
	y.pointFlow("r", "train", "yard.train")
	y.createBead("the train", "r-e", "r", "train", "open")
	y.git(y.repo, "branch", "yard/r-e")
	for i, id := range []string{"r-1", "r-2"} {
		y.createBead("child", id, "r", "", "approved")
		y.addParent("r-e", id)
		wt := filepath.Join(home, "worktrees", "r", id)
		y.git(y.repo, "worktree", "add", "-q", "-b", "yard/"+id, wt, "yard/r-e")
		y.commit(wt, "child"+string(rune('1'+i))+".txt", id+"\n")
	}
}

// borrowTrainTpl is a train-shaped template copied into the test's own temp
// dir: the worktrees repaired, the origin set to the copied remote, and the
// depot pointing at the copy with the test's own test command.
func borrowTrainTpl(t *testing.T, tpl *yardTemplate, check string) *yard {
	t.Helper()
	t.Parallel()
	root := tpl.borrow(t)
	// The home is short, not the test's temp dir: it holds the store's
	// unix socket, whose path the kernel bounds.
	home, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	repo := filepath.Join(t.TempDir(), "repo")
	origin := filepath.Join(t.TempDir(), "origin.git")
	copyTree(t, filepath.Join(root, "home"), home)
	copyTree(t, filepath.Join(root, "repo"), repo)
	copyTree(t, filepath.Join(root, "origin.git"), origin)
	y := &yard{t: t, home: home, repo: repo}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	y.push = "origin"
	// Repair names the copy's worktrees: a bare repair fixes the worktrees
	// the copied admin registered, which are the template's, and every
	// commit after would land in the shared template.
	pointAtCopy(t, y.repo,
		filepath.Join(home, "worktrees", "r", "r-1"),
		filepath.Join(home, "worktrees", "r", "r-2"))
	y.git(y.repo, "worktree", "repair",
		filepath.Join(home, "worktrees", "r", "r-1"),
		filepath.Join(home, "worktrees", "r", "r-2"))
	y.git(y.repo, "remote", "set-url", "origin", origin)
	serveYard(t, home)
	y.putDepot("r", y.repo, "main", check)
	return y
}

// borrowTrain is newTrainYard from the template.
func borrowTrain(t *testing.T, check string) *yard {
	t.Helper()
	return borrowTrainTpl(t, trainTpl, check)
}

var landedTpl = &yardTemplate{name: "landed", build: buildLandedTpl}

// buildLandedTpl is landedTrainYard's setup, once, into root: the train
// template with both children landed on the train's branch. The runs' logs
// are dropped: they belong to the template, and a builder that fails later
// must not show them as its own.
func buildLandedTpl(t *testing.T, root string) {
	t.Helper()
	train := trainTpl.borrow(t)
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	origin := filepath.Join(root, "origin.git")
	copyTree(t, filepath.Join(train, "home"), home)
	copyTree(t, filepath.Join(train, "repo"), repo)
	copyTree(t, filepath.Join(train, "origin.git"), origin)
	y := &yard{t: t, home: home, repo: repo}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	y.push = "origin"
	pointAtCopy(t, y.repo,
		filepath.Join(home, "worktrees", "r", "r-1"),
		filepath.Join(home, "worktrees", "r", "r-2"))
	y.git(y.repo, "worktree", "repair",
		filepath.Join(home, "worktrees", "r", "r-1"),
		filepath.Join(home, "worktrees", "r", "r-2"))
	y.git(y.repo, "remote", "set-url", "origin", origin)
	close := serveTemplate(t, home)
	defer close()
	y.putDepot("r", y.repo, "main", "true")
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	y.runs = nil
}

// borrowLanded is landedTrainYard from the template, with check as the depot's
// test command for the runs that follow. The children landed under the
// template's own, true: no test reads what that left in their notes, and the
// tests that borrow the yard assert on the train.
func borrowLanded(t *testing.T, check string) *yard {
	t.Helper()
	return borrowTrainTpl(t, landedTpl, check)
}

var landTpl = &yardTemplate{name: "land", build: buildLandTpl}

// buildLandTpl is landYard's setup, once, into root: the train template with
// both children landed, the train integrated and pushed, and its pull
// request open, the train in stage land. It layers on the train template,
// not the landed one: landYard lands its children with YARDR_PR=1 and the
// depot's own test command, as the template must. The fake gh lives in the
// template, not the builder's temp dir, which ends with the builder.
func buildLandTpl(t *testing.T, root string) {
	t.Helper()
	train := trainTpl.borrow(t)
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	origin := filepath.Join(root, "origin.git")
	gh := filepath.Join(root, "gh")
	copyTree(t, filepath.Join(train, "home"), home)
	copyTree(t, filepath.Join(train, "repo"), repo)
	copyTree(t, filepath.Join(train, "origin.git"), origin)
	if err := os.MkdirAll(filepath.Join(gh, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	scripttest.Write(t, filepath.Join(gh, "bin", "gh"), fakeGh)
	fakeGhs.Store(t, gh)
	t.Cleanup(func() { fakeGhs.Delete(t) })
	y := &yard{t: t, home: home, repo: repo}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	y.push = "origin"
	y.opts = "YARDR_PR=1"
	pointAtCopy(t, y.repo,
		filepath.Join(home, "worktrees", "r", "r-1"),
		filepath.Join(home, "worktrees", "r", "r-2"))
	y.git(y.repo, "worktree", "repair",
		filepath.Join(home, "worktrees", "r", "r-1"),
		filepath.Join(home, "worktrees", "r", "r-2"))
	y.git(y.repo, "remote", "set-url", "origin", origin)
	close := serveTemplate(t, home)
	defer close()
	y.putDepot("r", y.repo, "main", "test ! -e RED")
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	y.exec("integrate-train", "yard-integrators", "r-e")
	y.yardr("bead", "done", "r-e")
	y.yardr("bead", "done", "r-e")
	y.exec("pr-train", "yard-pr", "r-e")
	y.yardr("bead", "done", "r-e")
	if e := y.beadOf("r-e"); e.Stage != "land" {
		t.Fatalf("train in %s, want land", e.Stage)
	}
	y.runs = nil
}

// borrowLand is landYard from the template: the same yard and fake gh,
// copied, with the gh calls the template made truncated away. The callers
// count only their own run's calls, and the template never called pr merge.
func borrowLand(t *testing.T) (*yard, string) {
	t.Helper()
	t.Parallel()
	root := landTpl.borrow(t)
	// The home is short, not the test's temp dir: it holds the store's
	// unix socket, whose path the kernel bounds.
	home, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	repo := filepath.Join(t.TempDir(), "repo")
	origin := filepath.Join(t.TempDir(), "origin.git")
	gh := filepath.Join(t.TempDir(), "gh")
	copyTree(t, filepath.Join(root, "home"), home)
	copyTree(t, filepath.Join(root, "repo"), repo)
	copyTree(t, filepath.Join(root, "origin.git"), origin)
	copyTree(t, filepath.Join(root, "gh"), gh)
	y := &yard{t: t, home: home, repo: repo}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	y.push = "origin"
	y.opts = "YARDR_PR=1"
	pointAtCopy(t, y.repo,
		filepath.Join(home, "worktrees", "r", "r-1"),
		filepath.Join(home, "worktrees", "r", "r-2"))
	y.git(y.repo, "worktree", "repair",
		filepath.Join(home, "worktrees", "r", "r-1"),
		filepath.Join(home, "worktrees", "r", "r-2"))
	y.git(y.repo, "remote", "set-url", "origin", origin)
	serveYard(t, home)
	y.putDepot("r", y.repo, "main", "test ! -e RED")
	if err := os.WriteFile(filepath.Join(gh, "calls"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fakeGhs.Store(t, gh)
	t.Cleanup(func() { fakeGhs.Delete(t) })
	return y, gh
}

var watcherTpl = &yardTemplate{name: "watcher", build: buildWatcherTpl}

// buildWatcherTpl is newWatcherRun's repository, once, into root: base main
// with branch yard/r-1 one empty commit ahead in its worktree, the stub
// yardr and the recording sleep. The run's own temp dir, record and fds stay
// per test.
func buildWatcherTpl(t *testing.T, root string) {
	t.Helper()
	repo, wt := filepath.Join(root, "repo"), filepath.Join(root, "wt")
	watcherGit(t, root, "init", "-q", "-b", "main", repo)
	watcherGit(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	watcherGit(t, repo, "branch", "yard/r-1")
	watcherGit(t, repo, "worktree", "add", "-q", wt, "yard/r-1")
	watcherGit(t, wt, "commit", "-q", "--allow-empty", "-m", "bead")
	stub := filepath.Join(root, "stub")
	scripttest.Write(t, stub, "#!/bin/sh\ncat >/dev/null 2>&1; exit 0\n")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\n{ echo \"argv: $*\"; ls /dev/fd; } >\"$FAKE_SLEEP_RECORD.$$\"\nexec /bin/sleep \"$@\"\n"
	scripttest.Write(t, filepath.Join(bin, "sleep"), fake)
}
