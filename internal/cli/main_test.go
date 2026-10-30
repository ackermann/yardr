package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/chat"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

// errNoServer is what a command gets in a test when it reaches for the
// store of a yard whose server does not run.
var errNoServer = errors.New("tests start no server")

// askHerdrServer is herdrServer as yardr has it, for a test that gives it a
// stand-in herdr to ask.
var askHerdrServer func(context.Context, string) serverCheck

// realTry is tryHarnessCall as yardr has it, for a test that gives it a
// stand-in harness to call.
var realTry func(context.Context, string, string, []string) tryCheck

// realAnswers is answers as yardr has it, for a test whose listener is real.
var realAnswers func(string) bool

func TestMain(m *testing.M) {
	// Code that starts a part of yardr runs its own binary again (as
	// `server run`, `daemon`, `web serve`), and in a test that binary is
	// this one. The testing package takes such arguments for none and runs
	// the whole suite once more, detached, where a test of a command without
	// arguments then runs `server run` from os.Args and serves for good
	// (yardr-ahgv). So this binary, started as yardr, ends at once.
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "%s is a test binary, not yardr: it does not run %q\n", os.Args[0], strings.Join(flag.Args(), " "))
		os.Exit(2)
	}
	// A builder's pane and a assembly script run the tests with their own
	// session's identity in the environment. Commands read it (a move from
	// YARDR_SESSION expects that session to own the bead), so it must not
	// leak into these tests; a test that wants it sets it.
	for _, name := range []string{"SESSION", "BEAD", "CREW", "ACTOR"} {
		config.Unsetenv(name)
	}
	// The pane may be a crew member's, with the credentials of its env_file,
	// and a command that speaks to a source reads them: the thread tests set
	// GITHUB_TOKEN and left the GitHub App of the pane, which goes first
	// (yardr-6avw). So GitHub's are blank, once, for every test: a call made
	// with what the environment happens to hold finds none in any pane, and
	// a test that wants one sets the credential it is about (t.Setenv: a
	// credential is read from the process, so that test runs alone). Linear's
	// is one no test wants.
	for _, name := range []string{"GITHUB_TOKEN", "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY_FILE"} {
		os.Setenv(name, "")
	}
	os.Setenv("LINEAR_API_KEY", "lin_api_not_this_tests")
	// The settings' variables too: store.Setting.Effective reads them from
	// the process, and a run's own environment (testApp) holds none, so both
	// say the same. A test of one sets it (t.Setenv) and runs alone.
	for _, s := range store.KnownSettings {
		if s.Env != "" {
			os.Setenv(config.Key(s.Env), "")
		}
	}
	// Nor does a command reach the yard of this machine. The tests run in a
	// pane of that yard, with its YARDR_HOME, and its server answers: a
	// command that asked for a store no test gave it got the operator's
	// (yardr-j4vm). So the home is one with no yard in it, under every name
	// config reads, and the user's home, where the default one lies, is
	// empty too; a test that wants a yard names its own (testApp, noHome).
	nowhere, err := os.MkdirTemp("", "ynone")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv(config.Key("HOME"), filepath.Join(nowhere, "y")); err != nil {
		panic(err)
	}
	os.Setenv("HOME", nowhere)
	// No test starts a real daemon or talks to a real herdr: the test
	// binary is not yardr, and the herdr on PATH is somebody's own. A test
	// of the start replaces these (stubStart).
	spawnDaemon = func([]string, *config.Config) (<-chan error, error) {
		return nil, errors.New("tests start no daemon")
	}
	// Nor a server, for the same reason: a command reaches the store of a
	// yard whose server a test runs itself (server.Run), and of no other.
	connect = func(ctx context.Context, cfg *config.Config) (store.API, error) {
		if !server.Running(ctx, cfg) {
			return nil, errNoServer
		}
		return server.Connect(ctx, cfg)
	}
	paneTail = func(context.Context, map[string]any, int) (string, error) {
		return "", errors.New("tests read no pane")
	}
	paneHistory = func(context.Context, map[string]any, int) (string, error) {
		return "", errors.New("tests read no pane")
	}
	agentStatus = func(context.Context, map[string]any) (string, error) {
		return "", errors.New("tests ask no herdr")
	}
	focusAgent = func(context.Context, string) error { return errors.New("tests focus no pane") }
	attachAgent = func(context.Context, string) error { return errors.New("tests attach to no pane") }
	// Nor does doctor ask that herdr for its server or run this machine's
	// login shell: a test of either replaces these.
	askHerdrServer = herdrServer
	herdrServer = func(context.Context, string) serverCheck { return serverCheck{State: herdrUp} }
	loginShell = func() string { return "" }
	// Nor does a test call a harness that is somebody's own, and costs
	// them a model call: a test of the call puts it back (realTry).
	realTry = tryHarnessCall
	tryHarnessCall = func(context.Context, string, string, []string) tryCheck {
		return tryCheck{State: tryNotTried, Output: "tests call no harness"}
	}
	// Nor does a test take over the terminal with the chat's view: a test
	// of the hand-over replaces this (stubChat).
	runChat = func(context.Context, store.API, *chat.Model, uint64) error { return nil }
	listModels = func(context.Context, string) ([]string, error) { return nil, errors.New("tests run no agent") }
	// Nor does a test load a service into this machine's launchd or systemd,
	// write into its LaunchAgents, start a web view, dial its port or signal a
	// process: a
	// test of the services replaces these (stubServices).
	services, err := os.MkdirTemp("", "ysvc")
	if err != nil {
		panic(err)
	}
	serviceDir = func() (string, error) { return services, nil }
	// Nor does a test read this machine's own services for one of herdr's.
	systemServiceDirs = func() []string { return nil }
	serviceCtl = func(context.Context, string, ...string) (string, error) {
		return "", errors.New("tests ask no service manager")
	}
	signalStop = func(int) error { return errors.New("tests signal no process") }
	spawn = func([]string, *config.Config, *longRunning, []string) (int, <-chan error, func() error, error) {
		return 0, nil, nil, errors.New("tests start nothing in the background")
	}
	realAnswers = answers
	answers = func(string) bool { return false }
	// Nor is a server stopped: a test's pidfile names the test itself.
	restartServer = func(context.Context, *config.Config) error {
		return errors.New("tests restart no server")
	}
	pollEvery = 5 * time.Millisecond
	// The repositories the guide and stage tests copy per test, made once
	// here and read only after: three git runs each, not three per test.
	if setupTemplateDir, err = buildSetupTemplate(); err != nil {
		panic(err)
	}
	if stageTemplateDir, err = buildStageTemplate(); err != nil {
		panic(err)
	}
	// The depot with worktrees the purge and uninstall tests copy per
	// test: five git runs each, not five per test.
	if purgeTemplateDir, err = buildWorktreeTemplate("cloned-1", "cloned-2"); err != nil {
		panic(err)
	}
	if uninstallTemplateDir, err = buildWorktreeTemplate("web-1", "web-2"); err != nil {
		panic(err)
	}
	// The committed depot committedDepot copies per test: three git runs,
	// not three per test.
	if committedTemplateDir, err = buildCommittedTemplate(); err != nil {
		panic(err)
	}
	code := m.Run()
	scripttest.Remove()
	os.RemoveAll(services)
	os.RemoveAll(nowhere)
	os.RemoveAll(setupTemplateDir)
	os.RemoveAll(stageTemplateDir)
	os.RemoveAll(purgeTemplateDir)
	os.RemoveAll(uninstallTemplateDir)
	os.RemoveAll(committedTemplateDir)
	// A process a test started and did not stop outlives the run, and the
	// next run, and the one after: the run that leaves one fails, and says
	// where it ran. It is ended here, since nothing else will end it.
	for _, c := range leftBehind(2 * time.Second) {
		fmt.Fprintf(os.Stderr, "FAIL: a test left process %d running, in %s: %s\n", c.pid, c.dir, c.command)
		killStray(c.pid)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// strayKill signals a stray test process; tests replace it.
var strayKill = syscall.Kill

var strayDescribe = peer.Describe

// killStray ends one stray process, but only while it is still this
// run's child: between the listing and the signal its pid may have been
// freed and reused by an unrelated process, which must never be killed.
func killStray(pid int) {
	if !stillOurs(pid) {
		return
	}
	_ = strayKill(pid, syscall.SIGKILL)
}

// stillOurs reports whether pid is still this process's running child.
// When the OS cannot identify it, the process listing is not enough to
// risk signalling a pid that may have been reused.
func stillOurs(pid int) bool {
	st, err := strayDescribe(pid)
	return err == nil && st.PPID == os.Getpid() && st.State != 'Z'
}

// child is a process this test binary started.
type child struct {
	pid          int
	command, dir string
}

// leftBehind is the children of this process that still run after wait, each
// with its working directory: for a server that is its home, which a test
// made and which mostly carries the test's name. The wait is for a child a
// test has signalled in its cleanup and not waited for.
func leftBehind(wait time.Duration) []child {
	var left []child
	for deadline := time.Now().Add(wait); ; time.Sleep(50 * time.Millisecond) {
		var err error
		if left, err = children(); err != nil {
			fmt.Fprintln(os.Stderr, "the processes this run left were not checked:", err)
			return nil
		}
		if len(left) == 0 || time.Now().After(deadline) {
			break
		}
	}
	for i := range left {
		left[i].dir = cwd(left[i].pid)
	}
	return left
}

// children lists the running children of this process, as ps has them. One
// that has ended and that nobody waited for (a zombie) is not running.
func children() ([]child, error) {
	cmd := exec.Command("ps", "-axo", "pid=,ppid=,stat=,command=")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ps := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	var all []child
	for _, line := range strings.Split(out.String(), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		if ppid != os.Getpid() || pid == ps || strings.HasPrefix(f[2], "Z") {
			continue
		}
		all = append(all, child{pid: pid, command: strings.Join(f[3:], " ")})
	}
	return all, nil
}

// cwd is the working directory of pid, from /proc or, where there is none
// (macOS), from lsof; "an unknown directory" when neither says.
func cwd(pid int) string {
	if dir, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd"); err == nil {
		return dir
	}
	out, _ := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	for _, line := range strings.Split(string(out), "\n") {
		if dir, ok := strings.CutPrefix(line, "n"); ok {
			return dir
		}
	}
	return "an unknown directory"
}

// needPackTools skips the test when the pack's commands cannot run: they
// are written in bash, jq and curl.
func needPackTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"bash", "jq", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed, and the pack's commands are written in it", tool)
		}
	}
}

// declareGitHubPack declares the pack's github source in st, pointed at
// base ("" keeps the pack's own), with its command files under home (""
// declares the name alone, for a test that runs none of them): what the
// thread commands run. actor is who declares it. The pack is the embedded
// one, so finding it needs neither the working directory, which a test may
// change, nor the path of this file, which a build with -trimpath (the
// gate's) does not keep.
func declareGitHubPack(t *testing.T, st store.API, home, base, actor string) {
	t.Helper()
	text, err := packs.FS.ReadFile("github/github.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if base != "" {
		text = []byte(strings.Replace(string(text), "base: https://api.github.com", "base: "+base, 1))
	}
	d, err := source.Parse("packs/github/github.yaml", text)
	if err != nil {
		t.Fatal(err)
	}
	if home != "" {
		if err := d.CopyCommandFilesFS(packs.FS, "github", home); err != nil {
			t.Fatal(err)
		}
		scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	}
	if _, err := st.PutSource(context.Background(), "github.yaml", string(text), actor); err != nil {
		t.Fatal(err)
	}
}
