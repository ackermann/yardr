package deploy

// These tests run deploy-dokku the way a deploy group does: started by the
// exec runner with the environment it builds, against a yardr server of
// their own (a short YARDR_HOME in a temp dir, for the unix socket path
// limit), a throwaway repository, and a stand-in for ssh that serves a local
// bare repository as the Dokku host and records what it was run with.

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// gitEnv keeps git off the developer's configuration and gives it a name
// to commit as.
var gitEnv = []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=t",
	"GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}

// As contrib/assembly's: nothing of a yardr or a git running these tests
// reaches what they start, the home is one with no yard in it, and one
// yardr is built for all of them.
func TestMain(m *testing.M) {
	noSync := config.Getenv(store.NoSyncEnv)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "YARDR_") || strings.HasPrefix(k, "GIT_") || strings.HasPrefix(k, "DOKKU_") {
			os.Unsetenv(k)
		}
	}
	os.Setenv("YARDR_"+store.NoSyncEnv, noSync)
	nowhere, err := os.MkdirTemp("", "ynone")
	if err != nil {
		panic(err)
	}
	if err := config.UseHome(filepath.Join(nowhere, "y")); err != nil {
		panic(err)
	}
	for _, kv := range gitEnv {
		k, v, _ := strings.Cut(kv, "=")
		os.Setenv(k, v)
	}
	flag.Set("test.parallel", "4")
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

// The deploy stages of the design (section 3.1), behind a stage that
// previews a bead and one where a person looks at the preview.
const flow = `preview -> look on done | decide on failed
look -> merged | preview
merged -> live on done | decide on failed
decide -> merged
live terminal
`

// fakeSSH stands in for ssh. It writes its arguments to $FAKE_SSH/calls, one
// call per line, skips the options, and runs the command git gives it in
// $FAKE_SSH/host, where the app's bare repository is: the push is a real
// one, to a local repository. With $FAKE_SSH/hostkey it ends as OpenSSH does
// under strict checking when the host's key is not the pinned one. The
// host's apps are the repositories there: apps:list names them under
// Dokku's heading, apps:create makes one, and apps:destroy with --force
// removes one. With $FAKE_SSH/nodokku the host refuses every dokku command
// and takes pushes only, with $FAKE_SSH/nocreate it makes no app, and with
// $FAKE_SSH/nodestroy it refuses to remove one.
const fakeSSH = `#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_SSH/calls"
for a; do [ "$a" != -G ] || exit 0; done
while [ $# -gt 0 ]; do
  case $1 in
  -i | -o | -p | -F) shift 2 ;;
  -*) shift ;;
  *) break ;;
  esac
done
shift
[ "${1:-}" != -- ] || shift
if [ -e "$FAKE_SSH/hostkey" ]; then
  echo "No ED25519 host key is known for dokku.test and you have requested strict checking." >&2
  echo "Host key verification failed." >&2
  exit 255
fi
if [ -e "$FAKE_SSH/changedhostkey" ]; then
  echo "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@" >&2
  echo "@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @" >&2
  echo "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@" >&2
  echo "Host key verification failed." >&2
  exit 255
fi
unset GIT_DIR GIT_WORK_TREE
case "$*" in
git-receive-pack*) ;;
*)
  if [ -e "$FAKE_SSH/nodokku" ] || { [ -e "$FAKE_SSH/nocreate" ] && [ "$1" = apps:create ]; } ||
    { [ -e "$FAKE_SSH/nodestroy" ] && [ "$1" = --force ] && [ "${2:-}" = apps:destroy ]; }; then
    echo " !     $1 is not allowed for this key" >&2
    exit 1
  fi
  case "$*" in
  apps:list) echo "=====> My Apps" && ls "$FAKE_SSH/host" ;;
  "apps:create "*) git init -q --bare -b main "$FAKE_SSH/host/$2" ;;
  "--force apps:destroy "*) rm -r "$FAKE_SSH/host/$3" ;;
  *) echo " !     no such command: $*" >&2 && exit 1 ;;
  esac
  exit
  ;;
esac
[ ! -e "$FAKE_SSH/signal" ] || kill -TERM $PPID
cd "$FAKE_SSH/host" && exec sh -c "$*"
`

// yard is one isolated yardr with depot r over a fresh repository whose base
// main is checked out in the depot, bead r-1 in merged, and a Dokku host
// with the app "app" behind the fake ssh.
type yard struct {
	t          *testing.T
	home, repo string
	env        []string
	state      string            // the fake ssh's directory: bin/ssh, calls, host/app.git
	opts       map[string]string // the assignments of the group's cmd
	mode       string            // the script's argument: "" for a deploy, or preview
	depot      string            // the depot of the beads deploy runs for
	log        string            // what the last run wrote to its group's log
}

func newYard(t *testing.T) *yard {
	t.Helper()
	// A test with a yard shares nothing with another's (TestMain), so it
	// runs beside them from here on.
	t.Parallel()
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
	y := &yard{t: t, home: home, repo: filepath.Join(t.TempDir(), "repo"), state: t.TempDir(), depot: "r"}
	y.env = append(os.Environ(), "YARDR_HOME="+home, "YARDR_ACTOR=test")

	y.yardr("init", "--harness", "claude", "--dir", home, "--operator", "test")
	y.git(".", "init", "-q", "-b", "main", y.repo)
	y.commit(y.repo, "a.txt", "one\n")
	y.yardr("depot", "add", "r", y.repo, "--base", "main")
	y.yardrIn(flow, "flow", "set", "f", "-f", "-")
	y.yardr("depot", "flow", "r", "f")
	y.yardr("bead", "create", "work", "--id", "r-1", "-d", "r", "-s", "merged")

	for _, d := range []string{"bin", "host"} {
		if err := os.Mkdir(filepath.Join(y.state, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	y.write("bin/ssh", fakeSSH, 0o755)
	y.write("known_hosts", "dokku.test ssh-ed25519 AAAA\n", 0o600)
	y.git(".", "init", "-q", "--bare", "-b", "main", y.app())
	y.opts = map[string]string{
		"DOKKU_HOST":        "dokku.test",
		"DOKKU_APP":         "app",
		"DOKKU_KNOWN_HOSTS": filepath.Join(y.state, "known_hosts"),
		"DOKKU_URL":         "https://app.example.test",
	}
	return y
}

// app is the app's repository on the host.
func (y *yard) app() string { return y.hostApp("app") }

// hostApp is the repository of the app of that name on the host.
func (y *yard) hostApp(name string) string { return filepath.Join(y.state, "host", name) }

// apps is the names of the host's apps, in order.
func (y *yard) apps() string {
	y.t.Helper()
	entries, err := os.ReadDir(filepath.Join(y.state, "host"))
	if err != nil {
		y.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, " ")
}

// makeApp puts an app of that name on the host, as an earlier run left it.
func (y *yard) makeApp(name string) {
	y.t.Helper()
	y.git(".", "init", "-q", "--bare", "-b", "main", y.hostApp(name))
}

// write puts a file in the fake ssh's directory.
func (y *yard) write(name, content string, mode os.FileMode) string {
	y.t.Helper()
	path := filepath.Join(y.state, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		y.t.Fatal(err)
	}
	return path
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

// deploy runs deploy-dokku for bead id through the exec runner, as the
// group would whose cmd it is, and waits for it to exit.
func (y *yard) deploy(id string) {
	y.t.Helper()
	script, err := filepath.Abs("deploy-dokku")
	if err != nil {
		y.t.Fatal(err)
	}
	// Reading the script makes it an input of the test cache: go test does
	// not see files a test only executes, and would keep a stale pass.
	if _, err := os.ReadFile(script); err != nil {
		y.t.Fatal(err)
	}
	logPath := filepath.Join(y.home, "deploy.log")
	// The script finds this test's fake ssh by its command line: the
	// environment is every test's.
	cmd := "PATH=" + shellQuote(filepath.Join(y.state, "bin")) + ":$PATH FAKE_SSH=" + shellQuote(y.state)
	for k, v := range y.opts {
		cmd += " " + k + "=" + shellQuote(v)
	}
	cmd += " " + shellQuote(script) + " " + y.mode
	e := &runner.Exec{Home: y.home, Bin: yardrBin}
	ctx := context.Background()
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	// The train and base as the dispatcher passes them, read the way bead
	// show --json shows them.
	b, fam := y.bead(id), y.family(id)
	depot, err := st.GetDepot(ctx, y.depot)
	if err != nil {
		y.t.Fatal(err)
	}
	// The bead is the group's, as the dispatcher would have claimed it: a
	// move from a session that does not own it is refused as stale.
	if err := st.PutGroup(ctx, store.Group{Name: "deploy", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		y.t.Fatal(err)
	}
	sess, err := st.Claim(ctx, id, "deploy", b.Stage, "yard")
	if err != nil {
		y.t.Fatal(err)
	}
	h, err := e.Start(ctx, dispatch.Job{
		Bead:     store.Bead{ID: id, Stage: b.Stage, Type: b.Type},
		Group:    store.Group{Name: "deploy", Config: map[string]any{"cmd": cmd, "log": logPath}},
		Depot:    depot,
		Session:  *sess,
		Train:    fam.Train,
		Base:     fam.Base,
		Children: fam.Children,
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
			y.t.Fatal("deploy-dokku did not finish")
		}
	}
	out, _ := os.ReadFile(logPath)
	y.log = string(out)
	y.t.Logf("deploy log:\n%s", out)
	_ = os.Remove(logPath)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func (y *yard) bead(id string) store.Bead {
	y.t.Helper()
	var b store.Bead
	if err := json.Unmarshal([]byte(y.yardr("bead", "show", id, "--json")), &b); err != nil {
		y.t.Fatal(err)
	}
	return b
}

func (y *yard) family(id string) store.Family {
	y.t.Helper()
	var f store.Family
	if err := json.Unmarshal([]byte(y.yardr("bead", "show", id, "--json")), &f); err != nil {
		y.t.Fatal(err)
	}
	return f
}

// notes is the bead as prime shows it, notes included.
func (y *yard) notes(id string) string { return y.yardr("prime", "--bead", id) }

// pushes is what the fake ssh was run with for each push: git's own probe
// of the ssh it has (-G) is no push.
func (y *yard) pushes() []string { return y.callsWith("git-receive-pack") }

// calls is what the fake ssh was run with, each time.
func (y *yard) calls() []string { return y.callsWith("") }

func (y *yard) callsWith(part string) []string {
	y.t.Helper()
	b, err := os.ReadFile(filepath.Join(y.state, "calls"))
	if err != nil && !os.IsNotExist(err) {
		y.t.Fatal(err)
	}
	var out []string
	for _, c := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if c != "" && strings.Contains(c, part) {
			out = append(out, c)
		}
	}
	return out
}

// deployed is the commit the app's main is at on the host, "" for none.
func (y *yard) deployed() string { return y.at("app") }

// at is the commit the main of the app of that name is at on the host, ""
// for none or for no such app.
func (y *yard) at(name string) string {
	y.t.Helper()
	cmd := exec.Command("git", "-C", y.hostApp(name), "rev-parse", "-q", "--verify", "refs/heads/main")
	cmd.Env = y.env
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

// lead gives the yard a yardmaster of its own name, which YARDR_NOTIFY,
// unset here, leaves to the yard's setting; mailed says whether it has mail.
func (y *yard) lead() {
	y.t.Helper()
	y.yardr("crew", "add", "lead", "--runner", "manual")
	y.yardr("settings", "set", "yardmaster", "lead")
}

func (y *yard) mailed() bool {
	return strings.Contains(y.yardr("crew", "show", "lead"), "inbox 1 unread")
}

// refusedBeforePush fails the test unless r-1 went to decide with want in
// its note, the yardmaster was mailed, and ssh was never run.
func (y *yard) refusedBeforePush(want string) { y.t.Helper(); y.refused("r-1", want) }

// refused is refusedBeforePush for the bead of that id.
func (y *yard) refused(id, want string) {
	y.t.Helper()
	if b := y.bead(id); b.Stage != "decide" || b.Hold {
		y.t.Fatalf("bead %s hold=%v, want decide", b.Stage, b.Hold)
	}
	if notes := y.notes(id); !strings.Contains(notes, want) || !strings.Contains(notes, "nothing was pushed") {
		y.t.Fatalf("the note does not say %q and that nothing was pushed:\n%s", want, notes)
	}
	if !y.mailed() {
		y.t.Fatal("the yardmaster was not mailed")
	}
	if _, err := os.Stat(filepath.Join(y.state, "calls")); !os.IsNotExist(err) {
		y.t.Fatalf("ssh was run for a deploy refused before the push: %v", y.calls())
	}
	if got := y.deployed(); got != "" {
		y.t.Fatalf("the host has %s, want nothing pushed", got)
	}
}

// The base's commit is pushed to the app's main, by URL, and the bead goes
// to live with the URL, the commit and the seconds in its note. The depot
// gains no remote and its checkout is not touched. ssh is run with the
// pinned known-hosts file alone, strict checking and nothing to ask.
func TestDeployPushesTheBase(t *testing.T) {
	y := newYard(t)
	sha := y.commit(y.repo, "b.txt", "landed\n")
	if err := os.WriteFile(filepath.Join(y.repo, "wip.txt"), []byte("someone's\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "live" || b.Status != "closed" {
		t.Fatalf("bead %s/%s, want live and closed", b.Stage, b.Status)
	}
	if got := y.deployed(); got != sha {
		t.Fatalf("the host's main is at %q, want the base %s", got, sha)
	}
	if !regexp.MustCompile(`live https://app\.example\.test at ` + sha[:7] + ` in \d+ s`).MatchString(y.notes("r-1")) {
		t.Fatalf("the note does not name the URL, the commit and the seconds:\n%s", y.notes("r-1"))
	}
	if got := y.git(y.repo, "remote"); got != "" {
		t.Fatalf("the depot gained a remote: %q", got)
	}
	if got := y.git(y.repo, "status", "--porcelain"); got != "?? wip.txt" {
		t.Fatalf("the checkout was touched: %q", got)
	}
	pushes := y.pushes()
	if len(pushes) != 1 {
		t.Fatalf("ssh pushes: %q, want one", pushes)
	}
	for _, want := range []string{
		"-o BatchMode=yes", "-o StrictHostKeyChecking=yes",
		"-o UserKnownHostsFile=" + y.opts["DOKKU_KNOWN_HOSTS"] + " ", "-o GlobalKnownHostsFile=/dev/null",
		"-o VerifyHostKeyDNS=no", "-o KnownHostsCommand=none", "-o UpdateHostKeys=no",
		" dokku@dokku.test git-receive-pack 'app'",
	} {
		if !strings.Contains(pushes[0], want) {
			t.Errorf("ssh was run without %q:\n%s", want, pushes[0])
		}
	}
	// No key file was named: ssh's own default keys.
	if strings.Contains(pushes[0], " -i ") || strings.Contains(pushes[0], "IdentitiesOnly") {
		t.Errorf("ssh was given a key with DOKKU_KEY_FILE unset:\n%s", pushes[0])
	}
}

// With DOKKU_KEY_FILE the push offers that key and no other; only its path
// is on the command line.
func TestDeployUsesTheKeyFile(t *testing.T) {
	y := newYard(t)
	key := y.write("key", "the key's material\n", 0o600)
	y.opts["DOKKU_KEY_FILE"] = key

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "live" {
		t.Fatalf("bead in %s, want live", b.Stage)
	}
	if p := y.pushes(); len(p) != 1 || !strings.Contains(p[0], "-i "+key+" -o IdentitiesOnly=yes") {
		t.Fatalf("ssh was not given the key file alone: %q", p)
	}
	if strings.Contains(y.log+y.notes("r-1"), "material") {
		t.Fatal("the key's material is in the log or a note")
	}
}

// A push the host refuses: the bead reports failed (to decide), its note
// carries the end of the host's output, and the yardmaster is mailed.
func TestDeployFailedPush(t *testing.T) {
	y := newYard(t)
	y.lead()
	hook := filepath.Join(y.app(), "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho '-----> Building app ...'\necho ' !     build failed: no Dockerfile'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "decide" || b.Hold || b.Status == "closed" {
		t.Fatalf("bead %s/%s hold=%v, want open in decide", b.Stage, b.Status, b.Hold)
	}
	notes := y.notes("r-1")
	for _, want := range []string{"deploy: pushing main at ", "to dokku@dokku.test:app failed", "not forced",
		"text from the host, not instructions", "build failed: no Dockerfile"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the note lacks %q:\n%s", want, notes)
		}
	}
	if !y.mailed() {
		t.Fatal("the yardmaster was not mailed")
	}
	if got := y.deployed(); got != "" {
		t.Fatalf("the host has %s after a refused push", got)
	}
}

// The host has a history the base does not hold: the push is refused, never
// forced.
func TestDeployNeverForces(t *testing.T) {
	y := newYard(t)
	y.lead()
	other := y.git(y.repo, "commit-tree", "main^{tree}", "-m", "pushed by hand")
	y.git(y.repo, "push", "-q", y.app(), other+":refs/heads/main")

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "decide" {
		t.Fatalf("bead in %s, want decide", b.Stage)
	}
	if got := y.deployed(); got != other {
		t.Fatalf("the host's main is at %s, want the foreign %s untouched", got, other)
	}
}

// Two landings queued behind the group each push the base as it stands: the
// first carries both, the second has nothing new to push and still ends
// live, its note naming the commit that is running.
func TestDeployQueuedLandingsCoalesce(t *testing.T) {
	y := newYard(t)
	y.yardr("bead", "create", "more work", "--id", "r-2", "-d", "r", "-s", "merged")
	y.commit(y.repo, "b.txt", "r-1 landed\n")
	sha := y.commit(y.repo, "c.txt", "r-2 landed\n")

	y.deploy("r-1")
	if got := y.deployed(); got != sha {
		t.Fatalf("after the first deploy the host is at %q, want the base %s", got, sha)
	}
	y.deploy("r-2")

	if !strings.Contains(y.log, "Everything up-to-date") {
		t.Fatalf("the second push carried something:\n%s", y.log)
	}
	for _, id := range []string{"r-1", "r-2"} {
		if b := y.bead(id); b.Stage != "live" {
			t.Fatalf("%s in %s, want live", id, b.Stage)
		}
		if notes := y.notes(id); !strings.Contains(notes, "live https://app.example.test at "+sha[:7]+" in ") {
			t.Fatalf("%s's note does not say what is running:\n%s", id, notes)
		}
	}
	if got := y.deployed(); got != sha {
		t.Fatalf("the host is at %q, want %s", got, sha)
	}
}

// A branch cannot change how it is deployed: the script that runs is the
// one the group's cmd names, and a deploy-dokku the landed tree carries is
// a file like any other, pushed and never run.
func TestDeployRunsTheInstalledScriptNotTheTrees(t *testing.T) {
	y := newYard(t)
	marker := filepath.Join(y.state, "ran-the-trees")
	for _, dir := range []string{".", "contrib/deploy"} {
		if err := os.MkdirAll(filepath.Join(y.repo, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "deploy-dokku")
		if err := os.WriteFile(filepath.Join(y.repo, file), []byte("#!/bin/sh\ntouch "+shellQuote(marker)+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		y.git(y.repo, "add", file)
	}
	y.git(y.repo, "commit", "-q", "-m", "a deploy script of the branch's own")
	sha := y.git(y.repo, "rev-parse", "HEAD")

	y.deploy("r-1")

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("the tree's own deploy-dokku ran")
	}
	if b := y.bead("r-1"); b.Stage != "live" || y.deployed() != sha {
		t.Fatalf("bead in %s, host at %q: want live at %s", b.Stage, y.deployed(), sha)
	}
}

// trainYard makes r-1 a child of train r-e, whose branch yard/r-e is one
// commit ahead of main: r-1's landing.
func trainYard(t *testing.T) *yard {
	y := newYard(t)
	y.yardr("bead", "create", "the train", "--id", "r-e", "-d", "r", "-t", "train", "-s", "merged")
	y.yardr("dep", "add", "r-e", "r-1", "-k", "parent")
	y.git(y.repo, "branch", "yard/r-e")
	wt := filepath.Join(t.TempDir(), "train")
	y.git(y.repo, "worktree", "add", "-q", wt, "yard/r-e")
	y.commit(wt, "child.txt", "r-1 landed on the train\n")
	return y
}

// A child of a train landed on the train's branch: nothing is pushed, and
// it goes on to live with a note that names the train and says so.
func TestDeployChildOfATrainPushesNothing(t *testing.T) {
	y := trainYard(t)
	if fam := y.family("r-1"); fam.Train != "r-e" || fam.Base != "yard/r-e" {
		t.Fatalf("r-1 has train %q and base %q, want r-e and yard/r-e", fam.Train, fam.Base)
	}
	tip := y.git(y.repo, "rev-parse", "--short", "yard/r-e")

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "live" {
		t.Fatalf("child in %s, want live", b.Stage)
	}
	if notes := y.notes("r-1"); !strings.Contains(notes, "not deployed: landed on train r-e's branch yard/r-e (at "+tip+")") {
		t.Fatalf("the note does not name the train and its tip:\n%s", notes)
	}
	if p := y.pushes(); len(p) != 0 || y.deployed() != "" {
		t.Fatalf("a child of a train was pushed: %q, host at %q", p, y.deployed())
	}
}

// The train itself has no train and the depot's base as its own: its run,
// once its child is through, pushes the base like any landing.
func TestDeployTrainItselfPushesTheBase(t *testing.T) {
	y := trainYard(t)
	y.deploy("r-1")
	y.git(y.repo, "merge", "-q", "--ff-only", "yard/r-e") // the train landed
	sha := y.git(y.repo, "rev-parse", "main")

	y.deploy("r-e")

	if b := y.bead("r-e"); b.Stage != "live" {
		t.Fatalf("train in %s, want live", b.Stage)
	}
	if got := y.deployed(); got != sha {
		t.Fatalf("the host is at %q, want the base %s", got, sha)
	}
	if notes := y.notes("r-e"); !strings.Contains(notes, "live https://app.example.test at "+sha[:7]) {
		t.Fatalf("the train's note does not say what is running:\n%s", notes)
	}
}

// The host's key is not the pinned one, as ssh says under strict checking:
// the push fails, and the note says that it is the key and which file pins
// it.
func TestDeployUnknownHostKeyFailsThePush(t *testing.T) {
	y := newYard(t)
	y.lead()
	y.write("hostkey", "", 0o644)

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "decide" {
		t.Fatalf("bead in %s, want decide", b.Stage)
	}
	notes := y.notes("r-1")
	for _, want := range []string{"the ssh key of dokku.test is unknown or has changed",
		"not the one pinned in " + y.opts["DOKKU_KNOWN_HOSTS"], "Nothing was pushed", "Host key verification failed"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the note lacks %q:\n%s", want, notes)
		}
	}
	if !y.mailed() || y.deployed() != "" {
		t.Fatalf("mailed %v, host at %q: want mail and nothing pushed", y.mailed(), y.deployed())
	}
}

// A changed host key is rejected like an unknown one, with the note naming
// the pinned file and the host's warning.
func TestDeployChangedHostKeyFailsThePush(t *testing.T) {
	y := newYard(t)
	y.lead()
	y.write("changedhostkey", "", 0o644)

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "decide" {
		t.Fatalf("bead in %s, want decide", b.Stage)
	}
	notes := y.notes("r-1")
	for _, want := range []string{"the ssh key of dokku.test is unknown or has changed",
		"not the one pinned in " + y.opts["DOKKU_KNOWN_HOSTS"], "Nothing was pushed", "REMOTE HOST IDENTIFICATION HAS CHANGED"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the note lacks %q:\n%s", want, notes)
		}
	}
	if !y.mailed() || y.deployed() != "" {
		t.Fatalf("mailed %v, host at %q: want mail and nothing pushed", y.mailed(), y.deployed())
	}
}

// What must be there is asked for before anything is pushed: the host, the
// app, the pinned known-hosts file, and the key file where one is named.
func TestDeployRefusesBeforeThePush(t *testing.T) {
	gone := "/nonexistent/deploy-dokku-test"
	for name, c := range map[string]struct {
		set  map[string]string
		want string
	}{
		"no host":                   {map[string]string{"DOKKU_HOST": ""}, "DOKKU_HOST is not set"},
		"a host that is an option":  {map[string]string{"DOKKU_HOST": "-oProxyCommand=x"}, "DOKKU_HOST is no host name"},
		"no app":                    {map[string]string{"DOKKU_APP": ""}, "DOKKU_APP is not set"},
		"an app with a quote in it": {map[string]string{"DOKKU_APP": "a';x"}, "DOKKU_APP is no app name"},
		"no known-hosts variable":   {map[string]string{"DOKKU_KNOWN_HOSTS": ""}, "DOKKU_KNOWN_HOSTS is not set"},
		"no known-hosts file":       {map[string]string{"DOKKU_KNOWN_HOSTS": gone}, "DOKKU_KNOWN_HOSTS (" + gone + ") is missing"},
		"a known-hosts directory":   {map[string]string{"DOKKU_KNOWN_HOSTS": "/"}, "DOKKU_KNOWN_HOSTS (/) is missing"},
		"a known-hosts path of two": {map[string]string{"DOKKU_KNOWN_HOSTS": "/dev/null /etc/hosts"}, "DOKKU_KNOWN_HOSTS names a path ssh would read as another"},
		// a.txt is a file of the depot's tree, where the script and ssh run.
		"a known-hosts file of the tree": {map[string]string{"DOKKU_KNOWN_HOSTS": "a.txt"}, "DOKKU_KNOWN_HOSTS is not an absolute path (a.txt)"},
		"a key file of the tree":         {map[string]string{"DOKKU_KEY_FILE": "a.txt"}, "DOKKU_KEY_FILE is not an absolute path (a.txt)"},
		"no key file":                    {map[string]string{"DOKKU_KEY_FILE": gone}, "DOKKU_KEY_FILE (" + gone + ") is missing"},
		"a key path with a token":        {map[string]string{"DOKKU_KEY_FILE": "/tmp/%h"}, "DOKKU_KEY_FILE names a path ssh would read as another"},
	} {
		t.Run(name, func(t *testing.T) {
			y := newYard(t)
			y.lead()
			for k, v := range c.set {
				y.opts[k] = v
			}
			y.deploy("r-1")
			y.refusedBeforePush(c.want)
		})
	}
}

// A key file that cannot be read is refused like a missing one.
func TestDeployRefusesAnUnreadableKeyFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every file")
	}
	y := newYard(t)
	y.lead()
	y.opts["DOKKU_KEY_FILE"] = y.write("key", "k\n", 0o000)
	y.deploy("r-1")
	y.refusedBeforePush("is missing or cannot be read")
}

// A push ended by a signal says nothing about the host: the bead is held
// where it is, to be deployed again.
func TestDeployPushEndedBySignalHolds(t *testing.T) {
	y := newYard(t)
	y.write("signal", "", 0o644)

	y.deploy("r-1")

	if b := y.bead("r-1"); b.Stage != "merged" || !b.Hold {
		t.Fatalf("bead %s hold=%v, want held in merged", b.Stage, b.Hold)
	}
	if notes := y.notes("r-1"); !strings.Contains(notes, "was ended by signal 15") {
		t.Fatalf("the note does not name the signal:\n%s", notes)
	}
}
