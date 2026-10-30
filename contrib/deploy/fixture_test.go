package deploy

// Template yards: the setups every test repeats, built once and copied per
// test.
//
// Nearly every test in this package starts from one of two identical
// states: a yard with depot r over a fresh repository and bead r-1 in
// merged (newYard), or that yard with bead r-2 in preview on its branch
// yard/r-2 (previewYard). Building each from git and the CLI costs seconds,
// and the suite builds them a hundred times over; the R1 budget
// (docs/testing.md) goes on that repeated setup, not on what the tests
// assert.
//
// So each state is built once, by the first test that needs it, and every
// test copies it into its own temp dir and fixes what names an absolute
// path: the depot's path in the store. The copy is otherwise bit-identical
// to a fresh setup, so every assertion reads the same state: the preview
// template's branch carries the same commit every borrower checks its push
// against, and the fake host (bin/ssh, the pinned known-hosts file, the
// app's bare repository) is copied per test, so the calls one test counts
// are only its own run's. The preview template layers on the base one, as
// contrib/assembly's templates layer on each other.
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
	srv := &http.Server{Handler: rpc.Handler(st, nil), ConnContext: rpc.ConnContext}
	go func() { _ = srv.Serve(ln) }()
	return func() {
		_ = srv.Close()
		_ = st.Close(context.Background())
		// The closed socket is a stale node, not a file to copy.
		_ = os.Remove(cfg.Socket())
	}
}

// serveYard serves the test's own store on its socket, in this process: the
// driven scripts call the built yardr, which dials it like any server. The
// server stops, and the store closes, when the test ends.
func serveYard(t *testing.T, home string) {
	t.Helper()
	cfg := &config.Config{Home: home}
	st := storetest.Open(t, cfg.DBPath())
	// This test owns the home, so a socket file left behind is stale.
	_ = os.Remove(cfg.Socket())
	ln, err := net.Listen("unix", cfg.Socket())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfg.Socket(), 0o600); err != nil {
		ln.Close()
		t.Fatal(err)
	}
	srv := &http.Server{Handler: rpc.Handler(st, nil), ConnContext: rpc.ConnContext}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// copyTree copies src into dst, which must not exist: directories, regular
// files with their modes and mtimes (so git finds its index valid), and
// symlinks as links. Anything else is a bug, not a state: the templates
// hold no sockets past their close, and no fifos or devices at all.
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

// client is the yard's store API over its socket: the same store.API the
// CLI uses, with the same actor the tests pass it.
func (y *yard) client() *rpc.Client {
	return rpc.NewClient((&config.Config{Home: y.home}).Socket())
}

// initStore is `yardr init --harness claude --dir <home> --operator test`
// through the store API: the settings and the yardmaster crew, in init's
// order. The role file is not installed: nothing here reads it (the daemon
// never runs in this package), and the crew's prompt and unattended args
// go with it.
func (y *yard) initStore() {
	y.t.Helper()
	ctx := context.Background()
	st := y.client()
	if err := st.SetSetting(ctx, store.OperatorSetting, "test", "test"); err != nil {
		y.t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.YardmasterSetting, "yardmaster", "test"); err != nil {
		y.t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "greet": "test"}, Dir: y.home,
		On: []string{store.KindAttention}, MaxAge: "6h", HandoffGrace: "15m"}, "test"); err != nil {
		y.t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.DefaultKindSetting, "claude", "test"); err != nil {
		y.t.Fatal(err)
	}
	// Disk keeping is off unless a test turns it on: whether a keeping
	// acts depends on the machine's free space, and these tests land the
	// same on any machine.
	if err := st.SetSetting(ctx, store.DiskKeepGBSetting, "0", "test"); err != nil {
		y.t.Fatal(err)
	}
}

// putDepot is `yardr depot add <name> <path> --base <base>` through the
// store API.
func (y *yard) putDepot(name, path, base string) {
	y.t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		y.t.Fatal(err)
	}
	st := y.client()
	if err := st.PutDepot(context.Background(), store.Depot{Name: name, Path: abs, Kind: store.DepotGit, Base: base}, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// repointDepot moves the depot's path to the borrower's copy: the template
// names its own, which the borrower must never write to.
func (y *yard) repointDepot(name, path string) {
	y.t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		y.t.Fatal(err)
	}
	st := y.client()
	d, err := st.GetDepot(context.Background(), name)
	if err != nil {
		y.t.Fatal(err)
	}
	d.Path = abs
	if err := st.PutDepot(context.Background(), *d, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// putFlow is `yardr flow set <name> -f -` through the store API.
func (y *yard) putFlow(name, text string) {
	y.t.Helper()
	f, err := store.ParseFlow(text)
	if err != nil {
		y.t.Fatal(err)
	}
	f.Name = name
	st := y.client()
	if _, err := st.PutFlow(context.Background(), *f, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// pointFlow is `yardr depot flow <depot> <flow>` through the store API.
func (y *yard) pointFlow(depot, typ, flow string) {
	y.t.Helper()
	st := y.client()
	if err := st.SetFlowPointer(context.Background(), depot, typ, flow, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// createBead is `yardr bead create <title> --id <id> -d <depot> -t <typ> -s
// <stage>` through the store API.
func (y *yard) createBead(title, id, depot, typ, stage string) {
	y.t.Helper()
	st := y.client()
	if _, err := st.CreateBead(context.Background(), store.NewBead{Title: title, ID: id,
		Depot: depot, Type: typ, Stage: stage, Priority: store.DefaultPriority}, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// addParent is `yardr dep add <train> <child> -k parent` through the store
// API.
func (y *yard) addParent(train, child string) {
	y.t.Helper()
	st := y.client()
	if err := st.AddDep(context.Background(), train, child, store.DepParent, "", "", "test"); err != nil {
		y.t.Fatal(err)
	}
}

// advanceBead is `yardr bead advance <id> --to <stage>` through the store
// API.
func (y *yard) advanceBead(id, stage string) {
	y.t.Helper()
	st := y.client()
	if _, err := st.Advance(context.Background(), id, stage, store.Expect{}, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// closeBead is `yardr bead close <id> --reason <reason>` through the store
// API.
func (y *yard) closeBead(id, reason string) {
	y.t.Helper()
	st := y.client()
	if _, err := st.CloseBead(context.Background(), id, reason, store.Expect{}, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// addCrew is `yardr crew add <name> --runner manual` through the store API.
func (y *yard) addCrew(name string) {
	y.t.Helper()
	st := y.client()
	if _, err := st.PutCrew(context.Background(), store.CrewMember{Name: name, Runner: "manual",
		Config: map[string]any{}, On: []string{store.KindAttention}, HandoffGrace: "15m"}, "test"); err != nil {
		y.t.Fatal(err)
	}
}

var baseTpl = &yardTemplate{name: "base", build: buildBaseTpl}

// buildBaseTpl is newYard's setup, once, into root: a yard with depot r over
// a fresh repository, bead r-1 in merged, and the fake host with the app's
// bare repository behind the stand-in ssh.
func buildBaseTpl(t *testing.T, root string) {
	t.Helper()
	home, repo, state := filepath.Join(root, "home"), filepath.Join(root, "repo"), filepath.Join(root, "state")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	y := &yard{t: t, home: home, repo: repo, state: state, bin: filepath.Join(state, "bin")}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	y.git(".", "init", "-q", "-b", "main", y.repo)
	y.commit(y.repo, "a.txt", "one\n")
	close := serveTemplate(t, home)
	defer close()
	y.initStore()
	y.putDepot("r", y.repo, "main")
	y.putFlow("f", flow)
	y.pointFlow("r", "", "f")
	y.createBead("work", "r-1", "r", "", "merged")
	if err := os.MkdirAll(y.state, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"bin", "host"} {
		if err := os.Mkdir(filepath.Join(y.state, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scripttest.Write(t, filepath.Join(y.bin, "ssh"), fakeSSH)
	y.write("known_hosts", "dokku.test ssh-ed25519 AAAA\n", 0o600)
	y.git(".", "init", "-q", "--bare", "-b", "main", y.app())
}

// borrowBase is newYard from the template: the same yard, copied into the
// test's own temp dirs, served in this process, with the depot pointing at
// the copy.
func borrowBase(t *testing.T) *yard {
	t.Helper()
	t.Parallel()
	root := baseTpl.borrow(t)
	// The home is short, not the test's temp dir: it holds the store's
	// unix socket, whose path the kernel bounds.
	home, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	dir := t.TempDir()
	repo, state := filepath.Join(dir, "repo"), filepath.Join(dir, "state")
	copyTree(t, filepath.Join(root, "home"), home)
	copyTree(t, filepath.Join(root, "repo"), repo)
	copyTree(t, filepath.Join(root, "state"), state)
	y := &yard{t: t, home: home, repo: repo, state: state, bin: filepath.Join(state, "bin"),
		depot: "r", script: "deploy-dokku"}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	serveYard(t, home)
	y.repointDepot("r", y.repo)
	return y
}

var previewTpl = &yardTemplate{name: "preview", build: buildPreviewTpl}

// buildPreviewTpl is previewYard's setup, once, into root: the base template
// with bead r-2 in preview on its branch yard/r-2, one commit ahead of main.
// The branch is committed through a worktree that is removed again: a
// registered worktree names the template's absolute path, and every commit
// after would land in the shared template.
func buildPreviewTpl(t *testing.T, root string) {
	t.Helper()
	base := baseTpl.borrow(t)
	home, repo, state := filepath.Join(root, "home"), filepath.Join(root, "repo"), filepath.Join(root, "state")
	copyTree(t, filepath.Join(base, "home"), home)
	copyTree(t, filepath.Join(base, "repo"), repo)
	copyTree(t, filepath.Join(base, "state"), state)
	y := &yard{t: t, home: home, repo: repo, state: state, bin: filepath.Join(state, "bin")}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	close := serveTemplate(t, home)
	defer close()
	y.createBead("a page", "r-2", "r", "", "preview")
	wt := filepath.Join(root, "wt")
	y.git(y.repo, "worktree", "add", "-q", "-b", "yard/r-2", wt, "main")
	y.commit(wt, "page.txt", "a first try\n")
	y.git(y.repo, "worktree", "remove", "--force", wt)
	if got := y.worktrees(); got != 1 {
		t.Fatalf("the template has %d checkouts, want only its own", got)
	}
}

// borrowPreview is previewYard from the template: the same yard, copied into
// the test's own temp dirs, served in this process, with the depot pointing
// at the copy. The branch is in the copy; its worktree is made per test, by
// the tests that commit on it.
func borrowPreview(t *testing.T) *yard {
	t.Helper()
	t.Parallel()
	root := previewTpl.borrow(t)
	// The home is short, not the test's temp dir: it holds the store's
	// unix socket, whose path the kernel bounds.
	home, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	dir := t.TempDir()
	repo, state := filepath.Join(dir, "repo"), filepath.Join(dir, "state")
	copyTree(t, filepath.Join(root, "home"), home)
	copyTree(t, filepath.Join(root, "repo"), repo)
	copyTree(t, filepath.Join(root, "state"), state)
	y := &yard{t: t, home: home, repo: repo, state: state, bin: filepath.Join(state, "bin"),
		depot: "r", script: "deploy-dokku"}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	serveYard(t, home)
	y.repointDepot("r", y.repo)
	return y
}
