package peer

// These tests run return-branch the way its group does: started by the
// exec runner with the environment it builds, against a yardr server of
// their own (a short YARDR_HOME in a temp dir, for the unix socket path
// limit), a throwaway repository and a bare one for its remote.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
)

var yardrBin string

func TestMain(m *testing.M) {
	// Nothing from a yardr (or git hook) running these tests may leak into
	// the yardr, git and script they start, which inherit this environment.
	noSync := config.Getenv(store.NoSyncEnv)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "YARDR_") || strings.HasPrefix(k, "GIT_") {
			os.Unsetenv(k)
		}
	}
	os.Setenv("YARDR_"+store.NoSyncEnv, noSync)
	// With the home gone from the environment, a yardr started without a
	// test's own would take the default one, which on a machine that works
	// with yardr is the operator's yard, its server answering (yardr-j4vm).
	// So the home is one with no yard in it; each test's yard sets its own
	// over it (newYard).
	nowhere, err := os.MkdirTemp("", "ynone")
	if err != nil {
		panic(err)
	}
	if err := config.UseHome(filepath.Join(nowhere, "y")); err != nil {
		panic(err)
	}
	for _, kv := range []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t",
		"GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"} {
		k, v, _ := strings.Cut(kv, "=")
		os.Setenv(k, v)
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
	os.RemoveAll(nowhere)
	os.Exit(code)
}

// The flow of a yard that takes beads and gives their code back: what
// return-branch reports leads on from push.
const flow = `new -> push on done | returned on failed
push -> returned on done | new on failed
returned terminal
`

// yard is one isolated yardr with depot r, a clone of a bare repository, and
// bead r-1 in push with yard/r-1 one commit ahead of main.
type yard struct {
	t                  *testing.T
	home, repo, remote string
	env                []string
	commit             string
}

func newYard(t *testing.T) *yard {
	t.Helper()
	t.Parallel()
	home, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Stop(&config.Config{Home: home}); err != nil {
			t.Log(err)
		}
		os.RemoveAll(home)
	})
	dir := t.TempDir()
	y := &yard{t: t, home: home, repo: filepath.Join(dir, "repo"), remote: filepath.Join(dir, "remote.git")}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")
	y.yardr("init", "--harness", "claude", "--dir", home)
	y.git(dir, "init", "-q", "--bare", "-b", "main", y.remote)
	y.git(dir, "clone", "-q", y.remote, y.repo)
	y.git(y.repo, "checkout", "-q", "-b", "main")
	y.write("a.txt")
	y.git(y.repo, "push", "-q", "origin", "main")
	y.yardr("depot", "add", "r", y.repo, "--base", "main", "--check", "true")
	y.run(y.home, yardrBin, flow, "flow", "set", "f", "-f", "-")
	y.yardr("depot", "flow", "r", "f")
	y.yardr("bead", "create", "work", "--id", "r-1", "-d", "r", "-s", "push")
	y.git(y.repo, "checkout", "-q", "-b", "yard/r-1")
	y.commit = y.write("b.txt")
	y.git(y.repo, "checkout", "-q", "main")
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

func (y *yard) write(file string) string {
	y.t.Helper()
	if err := os.WriteFile(filepath.Join(y.repo, file), []byte(file+"\n"), 0o644); err != nil {
		y.t.Fatal(err)
	}
	y.git(y.repo, "add", file)
	y.git(y.repo, "commit", "-q", "-m", file)
	return y.git(y.repo, "rev-parse", "HEAD")
}

func (y *yard) bead() store.Bead {
	y.t.Helper()
	var b store.Bead
	if err := json.Unmarshal([]byte(y.yardr("bead", "show", "r-1", "--json")), &b); err != nil {
		y.t.Fatal(err)
	}
	return b
}

func (y *yard) notes() string { return y.yardr("prime", "--bead", "r-1") }

// remoteAt is the commit of branch on the remote, "" when it has none.
func (y *yard) remoteAt(branch string) string {
	out, err := exec.Command("git", "-C", y.remote, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// returns runs return-branch through the exec runner, as its group would
// with opts in front of the script in its cmd, and waits for it to exit.
func (y *yard) returns(opts string, job func(*dispatch.Job)) {
	y.t.Helper()
	script, err := filepath.Abs("return-branch")
	if err != nil {
		y.t.Fatal(err)
	}
	// Reading the script makes it an input of the test cache: go test does
	// not see files a test only executes, and would keep a stale pass.
	if _, err := os.ReadFile(script); err != nil {
		y.t.Fatal(err)
	}
	logPath := filepath.Join(y.home, "return.log")
	ctx := context.Background()
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if err := st.PutGroup(ctx, store.Group{Name: "return", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		y.t.Fatal(err)
	}
	b := y.bead()
	sess, err := st.Claim(ctx, b.ID, "return", b.Stage, "yard")
	if err != nil {
		y.t.Fatal(err)
	}
	j := dispatch.Job{
		Bead:    store.Bead{ID: b.ID, Stage: b.Stage, Type: b.Type},
		Group:   store.Group{Name: "return", Config: map[string]any{"cmd": strings.TrimSpace(opts + " " + script), "log": logPath}},
		Depot:   &store.Depot{Name: "r", Path: y.repo, Kind: store.DepotGit, Base: "main"},
		Session: *sess,
		Base:    "main",
	}
	if job != nil {
		job(&j)
	}
	e := &runner.Exec{Home: y.home, Bin: yardrBin}
	h, err := e.Start(ctx, j)
	if err != nil {
		y.t.Fatal(err)
	}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if alive, _ := e.Alive(ctx, h); !alive {
			break
		}
		if time.Now().After(deadline) {
			_ = e.Stop(ctx, h)
			y.t.Fatal("return-branch did not finish")
		}
	}
	out, _ := os.ReadFile(logPath)
	y.t.Logf("return-branch log:\n%s", out)
	_ = os.Remove(logPath)
}

// The bead's branch is pushed under its own name, where it is and at which
// commit is written on the bead, and the stage ends as done.
func TestReturnBranchPushes(t *testing.T) {
	y := newYard(t)
	y.git(y.repo, "remote", "set-url", "origin", "file://"+y.remote)
	y.returns("YARDR_PUSH=origin", nil)
	b := y.bead()
	if b.Stage != "returned" || b.Status != "closed" || b.Hold {
		t.Fatalf("bead after the push = %+v", b)
	}
	if got := y.remoteAt("yard/r-1"); got != y.commit {
		t.Fatalf("the remote's yard/r-1 is at %q, want %s", got, y.commit)
	}
	if b.Metadata[store.MetaResultBranch] != "yard/r-1" || b.Metadata[store.MetaResultCommit] != y.commit || b.Metadata[store.MetaResultRemote] != "file://"+y.remote {
		t.Fatalf("what is written on the bead = %+v", b.Metadata)
	}
	if notes := y.notes(); !strings.Contains(notes, "return-branch: pushed yard/r-1 at "+y.commit[:7]) {
		t.Fatalf("no note of the push:\n%s", notes)
	}
}

// A remote's URL goes on the bead without the credentials in it.
func TestReturnBranchWritesNoCredentials(t *testing.T) {
	y := newYard(t)
	y.git(y.repo, "config", "url."+y.remote+".insteadOf", "https://jo:s3cret@git.example.com/acme/web.git")
	y.git(y.repo, "config", "url."+y.remote+".pushInsteadOf", "https://jo:s3cret@git.example.com/acme/web.git")
	y.git(y.repo, "config", "remote.origin.url", "https://jo:s3cret@git.example.com/acme/web.git")
	y.returns("YARDR_PUSH=origin", nil)
	b := y.bead()
	if got := y.remoteAt("yard/r-1"); got != y.commit || b.Stage != "returned" {
		t.Fatalf("the remote's yard/r-1 is at %q, bead %+v", got, b)
	}
	if raw, _ := json.Marshal(b.Metadata); strings.Contains(string(raw), "s3cret") || strings.Contains(y.notes(), "s3cret") {
		t.Fatalf("a credential is on the bead: %s", raw)
	}
}

func TestReturnBranchWritesNoScpLogin(t *testing.T) {
	y := newYard(t)
	y.git(y.repo, "config", "url."+y.remote+".pushInsteadOf", "git@github.example.com:acme/web.git")
	y.git(y.repo, "config", "remote.origin.url", "git@github.example.com:acme/web.git")
	y.returns("YARDR_PUSH=origin", nil)
	b := y.bead()
	if got := y.remoteAt("yard/r-1"); got != y.commit || b.Stage != "returned" {
		t.Fatalf("the remote's yard/r-1 is at %q, bead %+v", got, b)
	}
	if got := b.Metadata[store.MetaResultRemote]; got != "ssh://github.example.com/acme/web.git" {
		t.Fatalf("remote on bead = %q, want ssh URL without login", got)
	}
	if raw, _ := json.Marshal(b.Metadata); strings.Contains(string(raw), "git@") || strings.Contains(y.notes(), "git@") {
		t.Fatalf("a scp-style login is on the bead: %s", raw)
	}
}

// A push that fails reports failed with git's words, and nothing is
// written on the bead: the flow sends it back to who can mend it.
func TestReturnBranchPushFails(t *testing.T) {
	y := newYard(t)
	if err := os.Rename(y.remote, y.remote+".away"); err != nil {
		t.Fatal(err)
	}
	y.returns("YARDR_PUSH=origin", nil)
	b := y.bead()
	if b.Stage != "new" || b.Metadata[store.MetaResultCommit] != nil {
		t.Fatalf("bead after a push that failed = %+v", b)
	}
	if notes := y.notes(); !strings.Contains(notes, "pushing yard/r-1 to origin failed (not forced)") || !strings.Contains(notes, "remote.git") {
		t.Fatalf("the note has not git's words:\n%s", notes)
	}
}

// It is never forced: a branch of that name on the remote that this one
// does not come after stays as it is.
func TestReturnBranchNeverForces(t *testing.T) {
	y := newYard(t)
	y.git(y.repo, "push", "-q", "origin", "yard/r-1")
	y.git(y.repo, "checkout", "-q", "yard/r-1")
	y.git(y.repo, "commit", "-q", "--amend", "-m", "rewritten")
	y.git(y.repo, "checkout", "-q", "main")
	y.returns("YARDR_PUSH=origin", nil)
	if got := y.remoteAt("yard/r-1"); got != y.commit {
		t.Fatalf("the remote's yard/r-1 moved to %q", got)
	}
	if b := y.bead(); b.Stage != "new" {
		t.Fatalf("bead after a push that was no fast-forward = %+v", b)
	}
}

// It pushes a bead's branch and nothing else: without a remote named, or
// handed the base for a branch, it holds the bead and pushes nothing.
func TestReturnBranchHolds(t *testing.T) {
	for name, tc := range map[string]struct {
		opts string
		job  func(*dispatch.Job)
		want string
	}{
		"no remote named": {want: "no remote to push to: set YARDR_PUSH"},
		"no such remote":  {opts: "YARDR_PUSH=upstream", want: "depot r has no remote upstream"},
		"the base":        {opts: "YARDR_PUSH=origin YARDR_BRANCH=main", want: "main is not a bead's branch"},
	} {
		t.Run(name, func(t *testing.T) {
			y := newYard(t)
			before := y.remoteAt("main")
			y.returns(tc.opts, tc.job)
			b := y.bead()
			if !b.Hold || b.Stage != "push" || y.remoteAt("yard/r-1") != "" || y.remoteAt("main") != before {
				t.Fatalf("bead = %+v, remote yard/r-1 %q", b, y.remoteAt("yard/r-1"))
			}
			if notes := y.notes(); !strings.Contains(notes, tc.want) {
				t.Fatalf("want a note with %q:\n%s", tc.want, notes)
			}
		})
	}
}

// Nothing was committed for the bead: failed, back to the builder.
func TestReturnBranchWithoutABranch(t *testing.T) {
	y := newYard(t)
	y.git(y.repo, "branch", "-q", "-D", "yard/r-1")
	y.returns("YARDR_PUSH=origin", nil)
	if b := y.bead(); b.Stage != "new" || !strings.Contains(y.notes(), "branch yard/r-1 does not exist") {
		t.Fatalf("bead = %+v\n%s", b, y.notes())
	}
}

// A yardr started with the environment of these tests and no yard of a
// test's own finds none: not the one of the machine the tests run on,
// whose home TestMain took out of the environment (yardr-j4vm).
func TestAYardrWithoutATestsYardFindsNone(t *testing.T) {
	home := config.Getenv("HOME")
	if home == "" {
		t.Fatal("no home in the environment: a yardr started from here takes the default one, the machine's")
	}
	cmd := exec.Command(yardrBin, "bead", "list")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(string(out), "no yard in "+home) {
		t.Fatalf("yardr bead list: %v, want exit 2 and no yard in %s\n%s", err, home, out)
	}
}
