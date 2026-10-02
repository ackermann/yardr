package refinery

// These tests run merge-local the way a refinery group does: started by the
// exec runner with the environment it builds, against a yardr server of
// their own (a short YARDR_HOME in a temp dir, for the unix socket path
// limit) and a throwaway repository.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

var yardrBin string

func TestMain(m *testing.M) {
	// Nothing from a yardr (or git hook) running these tests may leak into
	// the yardr, git and script they start, which inherit this environment.
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "YARDR_") || strings.HasPrefix(k, "YARD_") || strings.HasPrefix(k, "GIT_") {
			os.Unsetenv(k)
		}
	}
	dir, err := os.MkdirTemp("", "yrbin")
	if err != nil {
		panic(err)
	}
	yardrBin = filepath.Join(dir, "yardr")
	build := exec.Command("go", "build", "-o", yardrBin, "yardr.dev/yardr")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		panic("building yardr: " + err.Error())
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

const flow = `approved -> merged | new | merge
merge -> approved
new -> approved
merged terminal
`

// yard is one isolated yardr with rig r over a fresh repository whose base
// main is checked out in the rig, and bead r-1 in approved on yard/r-1, one
// commit ahead of main, in its worktree.
type yard struct {
	t              *testing.T
	home, repo, wt string
	env            []string
	push           string // YARDR_PUSH for the run
}

func newYard(t *testing.T) *yard {
	t.Helper()
	home, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Stop by pid (the pidfile), then remove the state.
		if err := server.Stop(&config.Config{Home: home}); err != nil {
			t.Log(err)
		}
		os.RemoveAll(home)
	})
	y := &yard{t: t, home: home, repo: filepath.Join(t.TempDir(), "repo"),
		wt: filepath.Join(home, "worktrees", "r", "r-1")}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@t")

	y.git(".", "init", "-q", "-b", "main", y.repo)
	y.commit(y.repo, "a.txt", "one\n")
	// The gate is red when the tree has a file RED.
	y.yardr("rig", "add", "r", y.repo, "--base", "main", "--check", "test ! -e RED")
	y.yardrIn(flow, "flow", "set", "f", "-f", "-")
	y.yardr("rig", "flow", "r", "f")
	y.yardr("bead", "create", "work", "--id", "r-1", "-r", "r", "-s", "approved")
	y.git(y.repo, "branch", "yard/r-1")
	y.git(y.repo, "worktree", "add", "-q", y.wt, "yard/r-1")
	return y
}

func (y *yard) run(dir, name string, stdin string, args ...string) string {
	y.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = dir, y.env
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		y.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (y *yard) git(dir string, args ...string) string { return y.run(dir, "git", "", args...) }
func (y *yard) yardr(args ...string) string           { return y.run(y.home, yardrBin, "", args...) }
func (y *yard) yardrIn(stdin string, args ...string) string {
	return y.run(y.home, yardrBin, stdin, args...)
}

func (y *yard) commit(dir, file, content string) string {
	y.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		y.t.Fatal(err)
	}
	y.git(dir, "add", file)
	y.git(dir, "commit", "-q", "-m", file)
	return y.git(dir, "rev-parse", "HEAD")
}

// refinery runs merge-local through the exec runner, as group refinery
// would, and waits for it to exit.
func (y *yard) refinery() {
	y.t.Helper()
	script, err := filepath.Abs("merge-local")
	if err != nil {
		y.t.Fatal(err)
	}
	// Reading the script makes it an input of the test cache: go test does
	// not see files a test only executes, and would keep a stale pass.
	if _, err := os.ReadFile(script); err != nil {
		y.t.Fatal(err)
	}
	logPath := filepath.Join(y.home, "refinery.log")
	cmd := script
	if y.push != "" {
		cmd = "YARDR_PUSH=" + y.push + " " + script
	}
	// The runner hands its own environment on: git's identity. YARDR_HOME
	// and YARDR_BIN come from the runner itself.
	for _, kv := range y.env[len(os.Environ()):] {
		if k, v, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "GIT_") {
			y.t.Setenv(k, v)
		}
	}
	e := &runner.Exec{Home: y.home, Bin: yardrBin}
	ctx := context.Background()
	h, err := e.Start(ctx, dispatch.Job{
		Bead:    store.Bead{ID: "r-1", Stage: "approved"},
		Group:   store.Group{Name: "refinery", Config: map[string]any{"cmd": cmd, "log": logPath}},
		Rig:     &store.Rig{Name: "r", Path: y.repo, Base: "main"},
		Session: store.Session{ID: "s1"},
	})
	if err != nil {
		y.t.Fatal(err)
	}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if alive, _ := e.Alive(ctx, h); !alive {
			break
		}
		if time.Now().After(deadline) {
			_ = e.Stop(ctx, h)
			y.t.Fatal("refinery did not finish")
		}
	}
	b, _ := os.ReadFile(logPath)
	y.t.Logf("refinery log:\n%s", b)
}

func (y *yard) bead() store.Bead {
	y.t.Helper()
	var b store.Bead
	if err := json.Unmarshal([]byte(y.yardr("bead", "show", "r-1", "--json")), &b); err != nil {
		y.t.Fatal(err)
	}
	return b
}

// notes is the bead as prime shows it, notes included.
func (y *yard) notes() string { return y.yardr("prime", "--bead", "r-1") }

func TestMergeLocalClean(t *testing.T) {
	y := newYard(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	y.git(".", "init", "-q", "--bare", remote)
	y.git(y.repo, "remote", "add", "origin", remote)
	y.push = "origin"
	y.commit(y.wt, "b.txt", "bead\n")
	y.commit(y.repo, "c.txt", "main moved\n") // a rebase is needed, cleanly

	y.refinery()

	b := y.bead()
	if b.Stage != "merged" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want merged and closed", b.Stage, b.Status)
	}
	main := y.git(y.repo, "rev-parse", "main")
	if tip := y.git(y.wt, "rev-parse", "HEAD"); tip != main {
		t.Fatalf("main %s is not the rebased branch %s", main, tip)
	}
	if y.git(y.repo, "status", "--porcelain") != "" {
		t.Fatal("the checkout of main is not clean after landing")
	}
	for _, f := range []string{"b.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(y.repo, f)); err != nil {
			t.Fatalf("checkout of main lacks %s: %v", f, err)
		}
	}
	if pushed := y.git(remote, "rev-parse", "main"); pushed != main {
		t.Fatalf("origin main %s, want %s", pushed, main)
	}
	if short := y.git(y.repo, "rev-parse", "--short", "main"); !strings.Contains(y.notes(), "merged "+short) {
		t.Fatalf("no merged note:\n%s", y.notes())
	}
}

// With main checked out nowhere, landing moves only the ref. A flow without
// a merged stage gets the bead closed instead.
func TestMergeLocalBaseNotCheckedOut(t *testing.T) {
	y := newYard(t)
	y.yardrIn("approved -> new | merge\n", "flow", "set", "f", "-f", "-")
	y.git(y.repo, "checkout", "-q", "--detach")
	tip := y.commit(y.wt, "b.txt", "bead\n")

	y.refinery()

	short := y.git(y.repo, "rev-parse", "--short", tip)
	if b := y.bead(); b.Status != "closed" || b.CloseReason == nil || *b.CloseReason != "merged "+short {
		t.Fatalf("bead %s/%s, want closed as merged %s", b.Stage, b.Status, short)
	}
	if main := y.git(y.repo, "rev-parse", "main"); main != tip {
		t.Fatalf("main %s, want the branch tip %s", main, tip)
	}
}

func TestMergeLocalConflict(t *testing.T) {
	y := newYard(t)
	tip := y.commit(y.wt, "a.txt", "bead's\n")
	main := y.commit(y.repo, "a.txt", "main's\n")

	y.refinery()

	b := y.bead()
	if b.Stage != "merge" || b.Status == "closed" {
		t.Fatalf("bead %s/%s, want open in merge", b.Stage, b.Status)
	}
	if !strings.Contains(y.notes(), "conflicts in: a.txt") {
		t.Fatalf("no note naming the conflict:\n%s", y.notes())
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on a conflict")
	}
	if got := y.git(y.wt, "rev-parse", "HEAD"); got != tip {
		t.Fatal("the branch was left rebased")
	}
	if y.git(y.wt, "status", "--porcelain") != "" {
		t.Fatal("the rebase was not aborted")
	}
}

func TestMergeLocalRedGate(t *testing.T) {
	y := newYard(t)
	main := y.git(y.repo, "rev-parse", "main")
	y.commit(y.wt, "RED", "red\n")

	y.refinery()

	if b := y.bead(); b.Stage != "new" {
		t.Fatalf("bead in %s, want new", b.Stage)
	}
	if !strings.Contains(y.notes(), "the gate failed") {
		t.Fatalf("no note with the gate's output:\n%s", y.notes())
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on a red gate")
	}
}

func TestMergeLocalDirtyCheckoutHolds(t *testing.T) {
	y := newYard(t)
	main := y.git(y.repo, "rev-parse", "main")
	y.commit(y.wt, "b.txt", "bead\n")
	dirty := filepath.Join(y.repo, "a.txt")
	if err := os.WriteFile(dirty, []byte("someone's work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	y.refinery()

	b := y.bead()
	if !b.Hold || b.Stage != "approved" {
		t.Fatalf("bead %s hold=%v, want held in approved", b.Stage, b.Hold)
	}
	if !strings.Contains(y.notes(), "uncommitted changes") {
		t.Fatalf("no note on the hold:\n%s", y.notes())
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved under a dirty checkout")
	}
	if got, _ := os.ReadFile(dirty); string(got) != "someone's work\n" {
		t.Fatalf("the dirty tree was touched: %q", got)
	}
}
