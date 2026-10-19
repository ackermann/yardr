package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/server"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// noHome is a machine where nothing names a yard: a user's home of its own,
// and neither variable set, both put back after the test. It returns the
// user's home.
func noHome(t *testing.T) string {
	t.Helper()
	user := shortDir(t, "yu")
	t.Setenv("HOME", user)
	t.Setenv("YARDR_HOME", "")
	return user
}

// doctorHome is the home yardr doctor reports on, run with args in front.
func doctorHome(t *testing.T, args ...string) string {
	t.Helper()
	return doctorHomeOf(t, &app{env: environMap(os.Environ())}, args...)
}

// doctorHomeOf is doctorHome run on a.
func doctorHomeOf(t *testing.T, a *app, args ...string) string {
	t.Helper()
	out, _ := executeOut(a, append(args, "doctor", "--json")...)
	var rep doctorReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("doctor --json: %v\n%s", err, out)
	}
	return rep.Home
}

// The home is --home, then YARDR_HOME, then ~/.yardr.
func TestHomeFlagOrder(t *testing.T) {
	user := noHome(t)
	setInteractive(t, false)
	fakeTools(t, map[string]string{"git": toolAnswers})
	flag, variable := filepath.Join(user, "flag"), filepath.Join(user, "variable")

	if got, want := doctorHome(t), filepath.Join(user, ".yardr"); got != want {
		t.Errorf("neither: home %s, want %s", got, want)
	}
	t.Setenv("YARDR_HOME", variable)
	if got := doctorHome(t); got != variable {
		t.Errorf("the variable: home %s, want %s", got, variable)
	}
	if got := doctorHome(t, "--home", flag); got != flag {
		t.Errorf("the flag over the variable: home %s, want %s", got, flag)
	}
	// After the command too: the flag is as good as --home=<dir>.
	t.Setenv("YARDR_HOME", variable)
	out, _ := executeOut(&app{}, "doctor", "--json", "--home="+flag)
	if !strings.Contains(out, `"home": "`+flag+`"`) {
		t.Errorf("--home after the command:\n%s", out)
	}
}

// A relative --home is made absolute once, and what the command starts gets
// it as YARDR_HOME: a child with the command's environment,
// a background daemon, a detached server. The flag is the run's: the
// process's own YARDR_HOME is left as it was.
func TestHomeFlagReachesChildren(t *testing.T) {
	user := noHome(t)
	setInteractive(t, false)
	fakeTools(t, map[string]string{"git": toolAnswers})
	variable := filepath.Join(user, "variable")
	t.Setenv("YARDR_HOME", variable)
	t.Chdir(user)
	want := filepath.Join(user, "y1")
	a := &app{env: environMap(os.Environ())}
	if got := doctorHomeOf(t, a, "--home", "y1/"); got != want {
		t.Fatalf("home %s, want %s", got, want)
	}
	if got := os.Getenv("YARDR_HOME"); got != variable {
		t.Errorf("--home set the process's YARDR_HOME to %s; it was %s", got, variable)
	}

	elsewhere := t.TempDir()
	child := exec.Command("/bin/sh", "-c", `printf '%s' "$YARDR_HOME"`)
	child.Dir = elsewhere
	child.Env = a.environ()
	out, err := child.Output()
	if err != nil || string(out) != want {
		t.Errorf("a child's home = %q, %v; want %s", out, err, want)
	}
	for name, env := range map[string][]string{
		"daemon":  daemonEnv(a.environ(), want),
		"server":  server.ServerEnv(a.environ(), want),
		"service": serviceEnv(a.environ(), want, kindOf(svcDaemon)),
	} {
		var homes []string
		for _, kv := range env {
			if k, v, _ := strings.Cut(kv, "="); config.IsVar(k, "HOME") {
				homes = append(homes, v)
			}
		}
		if !slices.Equal(homes, []string{want}) {
			t.Errorf("%s gets homes %v, want %s", name, homes, want)
		}
	}
}

// A service installed with --home runs for that home, whatever the variable
// said, and is removed with the flag.
func TestHomeFlagInstallsTheServiceForThatHome(t *testing.T) {
	user := noHome(t)
	dir, _ := stubServices(t, "linux")
	stubLookPath(t, "git", "herdr", "claude", "systemctl")
	home := filepath.Join(user, "a")
	t.Setenv("YARDR_HOME", filepath.Join(user, "variable"))
	st := storetest.New(t)
	a := func() *app { return &app{st: st, actor: "me"} }
	if _, err := run(t, a().rootCmd(), "init", "--home", home, "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YARDR_HOME", filepath.Join(user, "variable"))
	out, err := run(t, a().rootCmd(), "--home", home, "daemon", "install")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "yardr-daemon-"+homeID(home)+".service"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantText(t, string(body), `Environment="YARDR_HOME=`+home+`"`)
	if strings.Contains(string(body), "variable") {
		t.Errorf("the unit names the variable's home:\n%s", body)
	}
	wantText(t, out, "yardr --home "+home+" daemon uninstall")
}

// yardr --home <dir> in a terminal, on a home with no yard, runs the
// onboarding form there.
func TestHomeFlagOnboards(t *testing.T) {
	user := noHome(t)
	home := filepath.Join(user, "a")
	setInteractive(t, true)
	stubLookPath(t, "codex", "git", "herdr")
	asked := 0
	stubAsk(t, func(...huh.Field) error { asked++; return nil })
	st := storetest.New(t)
	stubStart(t, st, yardmasterHandle())
	stubPane(t, answeredPane)
	stubFocus(t)

	out, err := run(t, (&app{st: st}).rootCmd(), "--home", home)
	if err != nil {
		t.Fatal(err)
	}
	if asked == 0 {
		t.Fatal("no form was asked")
	}
	r, err := st.GetCrew(context.Background(), "yardmaster")
	if err != nil || r.Dir != user {
		t.Fatalf("yardmaster = %+v, %v", r, err)
	}
	wantText(t, out, "yard created in "+home, "every yardr command for it needs\n--home "+home, "yardr --home "+home+" setup")
	if _, err := os.Stat(filepath.Join(user, ".yardr")); err == nil {
		t.Error("the default home was created")
	}
}

// Without a terminal the refusal gives the command with --home for another
// home, and with neither flag nor variable for the default one.
func TestHomeFlagInTheRefusal(t *testing.T) {
	user := noHome(t)
	setInteractive(t, false)
	var errs bytes.Buffer
	home := filepath.Join(user, "my yard")

	if code := execute(&app{stderr: &errs}, []string{"--home", home, "bead", "list"}); code != 2 {
		t.Fatalf("exit %d: %s", code, &errs)
	}
	if got := errs.String(); !strings.Contains(got, "`yardr --home '"+home+"' init --harness <kind>`") || strings.Contains(got, "YARDR_HOME") {
		t.Errorf("another home: %s", got)
	}
	errs.Reset()
	t.Setenv("YARDR_HOME", "")
	if code := execute(&app{stderr: &errs}, []string{"bead", "list"}); code != 2 {
		t.Fatalf("exit %d: %s", code, &errs)
	}
	if got := errs.String(); !strings.Contains(got, "`yardr init --harness <kind>`") || strings.Contains(got, "--home") || strings.Contains(got, "YARDR_HOME") {
		t.Errorf("the default home: %s", got)
	}
}

func TestYardrAt(t *testing.T) {
	user := noHome(t)
	for home, want := range map[string]string{
		filepath.Join(user, ".yardr"):       "yardr",
		filepath.Join(user, ".yardr") + "/": "yardr",
		"/tmp/y1":                           "yardr --home /tmp/y1",
		"/tmp/my yard":                      "yardr --home '/tmp/my yard'",
	} {
		if got := yardrAt(home); got != want {
			t.Errorf("yardrAt(%q) = %s, want %s", home, got, want)
		}
	}
}

// A home too long for its socket is refused before anything is created,
// and the message names the flag as well as the variable. A --home with no
// directory is refused too, rather than taken for the default home.
func TestHomeFlagTooLong(t *testing.T) {
	user := noHome(t)
	var errs bytes.Buffer
	long := filepath.Join(user, strings.Repeat("h", config.MaxSocket))
	fakeTools(t, map[string]string{"git": toolAnswers})
	for _, args := range [][]string{{"--home", long, "bead", "list"}, {"init", "--home", long, "--harness", "claude"}, {"--home", long, "doctor"}} {
		errs.Reset()
		if code := execute(&app{stderr: &errs}, args); code != 1 {
			t.Fatalf("%v: exit %d", args, code)
		}
		if got := errs.String(); !strings.Contains(got, "too long") || !strings.Contains(got, "--home") || !strings.Contains(got, "YARDR_HOME") {
			t.Errorf("%v: %s", args, got)
		}
	}
	if _, err := os.Stat(long); err == nil {
		t.Error("the refused home was created")
	}

	errs.Reset()
	if code := execute(&app{stderr: &errs}, []string{"--home", "", "bead", "list"}); code != 1 || !strings.Contains(errs.String(), "--home needs a directory") {
		t.Errorf("--home '': exit %d, %s", code, &errs)
	}
	if _, err := os.Stat(filepath.Join(user, ".yardr")); err == nil {
		t.Error("--home '' went to the default home")
	}
}

// said writes what a running server said about its receiver into the home:
// its state, and a pidfile naming the same process.
func said(t *testing.T, cfg *config.Config, l hooks.Listener) {
	t.Helper()
	if err := os.MkdirAll(cfg.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := hooks.WriteListener(cfg, l); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.PIDFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// hook_addr takes an address a server can listen on and nothing else, and
// setting it says when the server takes it, with the home in the command
// and with the variable where the running server has one.
func TestSettingHookAddr(t *testing.T) {
	home := newHome(t)
	cfg := &config.Config{Home: home}
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	for _, bad := range []string{"8790", "localhost", "127.0.0.1:http", "127.0.0.1:0", "127.0.0.1:70000", "http://127.0.0.1:8790"} {
		if _, err := run(t, a.settingsCmd(), "set", "hook_addr", bad); err == nil || !strings.Contains(err.Error(), "127.0.0.1:8790") {
			t.Errorf("hook_addr %s: %v", bad, err)
		}
	}
	if got, _ := st.Settings(ctx); len(got) != 1 || got[store.FilesMaxSetting] != "64" {
		t.Fatalf("a refused address was kept: %v", got)
	}

	out, err := run(t, a.settingsCmd(), "set", "hook_addr", "127.0.0.1:8790")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "hook_addr = 127.0.0.1:8790\n", "The server takes it at its next start: yardr --home "+home+" server stop")
	if strings.Contains(out, "YARDR_HOOK_ADDR") {
		t.Errorf("no server runs with the variable:\n%s", out)
	}
	if got, _ := st.Settings(ctx); got["hook_addr"] != "127.0.0.1:8790" {
		t.Fatalf("settings = %v", got)
	}

	said(t, cfg, hooks.Listener{Addr: "127.0.0.1:9001", From: hooks.FromVariable})
	out, err = run(t, a.settingsCmd(), "set", "hook_addr", ":8791")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "hook_addr = :8791\n", "at its next start",
		"The running server was started with YARDR_HOOK_ADDR=127.0.0.1:9001, which is used instead of the setting")

	out, err = run(t, a.settingsCmd(), "get")
	if err != nil || !strings.Contains(out, "hook_addr") || !strings.Contains(out, "the address the server takes webhooks on") {
		t.Fatalf("settings get: %v\n%s", err, out)
	}
}

// doctor says what the server takes webhooks on and whether that is the
// variable or the setting, and where the server could not listen (another
// yard has the address) it says so with the setting to change, for a yard
// with no hook yet too.
func TestDoctorSaysWhatTheReceiverListensOn(t *testing.T) {
	home := newHome(t)
	cfg := &config.Config{Home: home}
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	st := storetest.New(t)
	ctx := context.Background()
	a := func() *app { return &app{st: st, actor: "me"} }
	declareGitHubPack(t, st, "", "", "me")
	if _, err := run(t, a().rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	doctor := func() string {
		t.Helper()
		out, _ := run(t, a().rootCmd(), "doctor")
		return out
	}
	const note = "note            hooks: "

	// No hook and no address: nothing to say about a receiver.
	said(t, cfg, hooks.Listener{})
	if out := doctor(); strings.Contains(out, note) {
		t.Errorf("no hook, no address:\n%s", out)
	}
	// No hook, and an address the server could not take.
	if err := st.SetSetting(ctx, "hook_addr", "127.0.0.1:8790", yardOperator(ctx, st)); err != nil {
		t.Fatal(err)
	}
	taken := hooks.Listener{Addr: "127.0.0.1:8790", From: hooks.FromSetting, Error: "listen tcp 127.0.0.1:8790: bind: address already in use"}
	said(t, cfg, taken)
	wantText(t, doctor(), "not configured  hooks: ", note+"the server could not listen on 127.0.0.1:8790 (the hook_addr setting): "+
		"listen tcp 127.0.0.1:8790: bind: address already in use. Another yard or program may have the address; "+
		"give this yard its own with yardr --home "+home+" settings set hook_addr <host:port>, then yardr --home "+home+" server stop")

	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutHook(ctx, store.Hook{Name: "gh", Source: "github", SecretFile: secret}, yardOperator(ctx, st)); err != nil {
		t.Fatal(err)
	}
	wantText(t, doctor(), "configured      hooks: gh (github)", note+"the server could not listen on 127.0.0.1:8790")

	// The setting was changed since: the server takes it at its next start.
	if err := st.SetSetting(ctx, "hook_addr", "127.0.0.1:8791", yardOperator(ctx, st)); err != nil {
		t.Fatal(err)
	}
	wantText(t, doctor(), "hook_addr is 127.0.0.1:8791 now, which the server takes at its next start: yardr --home "+home+" server stop")

	said(t, cfg, hooks.Listener{Addr: "127.0.0.1:8791", From: hooks.FromSetting})
	out := doctor()
	wantText(t, out, note+"the server takes webhooks on 127.0.0.1:8791 (the hook_addr setting)\n")

	// The variable, where the server started with it, is the one in use.
	said(t, cfg, hooks.Listener{Addr: "127.0.0.1:9001", From: hooks.FromVariable})
	wantText(t, doctor(), note+"the server takes webhooks on 127.0.0.1:9001 (YARDR_HOOK_ADDR, set where the server started); "+
		"the hook_addr setting (127.0.0.1:8791) is not used while the variable is set there")

	// A hook and no address at all.
	if err := st.SetSetting(ctx, "hook_addr", "", yardOperator(ctx, st)); err != nil {
		t.Fatal(err)
	}
	said(t, cfg, hooks.Listener{})
	wantText(t, doctor(), note+"the server has no address to take webhooks on: yardr --home "+home+" settings set hook_addr 127.0.0.1:8790")

	// hook add says the same where the receiver does not listen.
	a2 := a()
	a2.cfg = cfg
	a2.actor = yardOperator(ctx, st)
	out, err := run(t, a2.hookCmd(), "add", "gh2", "--source", "github", "--secret-file", secret)
	if err != nil || !strings.Contains(out, "note: the server has no address to take webhooks on") {
		t.Fatalf("hook add: %v\n%s", err, out)
	}

	// No server has said anything (one that stopped, or an older build's).
	if err := os.Remove(cfg.HookState()); err != nil {
		t.Fatal(err)
	}
	wantText(t, doctor(), note+"the server has not said what it takes webhooks on")
}
