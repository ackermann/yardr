package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/roles"
)

// shortDir is a temp directory a yard's socket path fits in: a unix socket
// path is at most config.MaxSocket bytes, and t.TempDir (TMPDIR plus the
// test's name) is already past that on macOS.
func shortDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// here is a command as yardr prints it for the test's home: with --home in
// it, since that home is not the default one.
func here(cmd string) string {
	return strings.Replace(cmd, "yardr", yardrAt(config.Getenv("HOME")), 1)
}

// newHome points YARDR_HOME at a fresh directory with no yard in it.
func newHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(shortDir(t, "yh"), "y")
	t.Setenv("YARDR_HOME", home)
	return home
}

func TestInitHarnessClaude(t *testing.T) {
	home := newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--operator", "Jo")
	if err != nil {
		t.Fatal(err)
	}

	// The default roles, as embedded, naming the operator.
	for _, name := range []string{"mayor.md", "builder.md", "merger.md", "reviewer.md"} {
		tmpl, err := roles.FS.ReadFile(name)
		if err != nil {
			t.Fatalf("%s not embedded: %v", name, err)
		}
		want := fillRole(tmpl, "Jo", "mayor")
		got, err := os.ReadFile(filepath.Join(home, "roles", name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s installed: %v", name, err)
		}
		if bytes.Contains(got, []byte("{{")) {
			t.Errorf("%s keeps a placeholder:\n%s", name, got)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(home, "roles", "mayor.md")); !bytes.Contains(b, []byte("bring to Jo")) {
		t.Errorf("mayor.md does not name Jo:\n%s", b)
	}
	// The mayor, on the harness with its unattended flags.
	r, err := st.GetResident(ctx, "mayor")
	if err != nil {
		t.Fatal(err)
	}
	// greet: its first session writes to the operator by mail (yardr-8j3n).
	want := map[string]any{"kind": "claude", "prompt": filepath.Join(home, "roles", "mayor.md"),
		"args": []any{"--permission-mode", "auto"}, "greet": "Jo"}
	if !reflect.DeepEqual(r.Config, want) || r.Runner != "herdr" || r.MaxAge != "6h" ||
		r.Dir != filepath.Dir(home) || !reflect.DeepEqual(r.On, defaultOn) {
		t.Fatalf("mayor = %+v", r)
	}
	// The roles tell a stuck agent that a hold reaches the mayor's inbox, so
	// the mayor must hear held, whatever else the default list carries.
	if !slices.Contains(r.On, "held") {
		t.Fatalf("mayor does not hear held: on = %v", r.On)
	}
	// A bead stranded by a flow change is routed to nobody: the mayor is
	// the one who must hear of it, and the form must offer the kind.
	if !slices.Contains(r.On, "stranded") || !slices.Contains(knownKinds, "stranded") || !slices.Contains(store.DispatchKinds, "stranded") {
		t.Fatalf("stranded is not heard: on = %v", r.On)
	}
	if got, _ := st.Settings(ctx); got["default_kind"] != "claude" || got["operator"] != "Jo" || got["mayor"] != "mayor" {
		t.Fatalf("settings = %v", got)
	}
	// The mayor takes it from here; the commands are in init --help.
	for _, s := range []string{"harness  claude", "naming Jo", ".yardr/roles/", here("yardr daemon"), "\n  " + here("yardr chat") + "\n  " + here("yardr setup") + "\n"} {
		if !strings.Contains(out, s) {
			t.Errorf("summary lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "route add") {
		t.Errorf("summary still lists the setup commands:\n%s", out)
	}
	if help, _ := run(t, a.rootCmd(), "init", "--help"); !strings.Contains(help, "yardr rig init webapp") ||
		!strings.Contains(help, "yardr route add") {
		t.Errorf("init --help lacks the commands for scripts:\n%s", help)
	}

	// Again: init is idempotent; the mayor is not replaced, and roles the
	// operator edited are kept.
	mayor := filepath.Join(home, "roles", "mayor.md")
	if err := os.WriteFile(mayor, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--operator", "Jo"); err != nil ||
		!strings.Contains(out, "already a resident: kept") {
		t.Fatalf("second init: %v\n%s", err, out)
	}
	if again, _ := st.GetResident(ctx, "mayor"); !reflect.DeepEqual(again, r) {
		t.Fatalf("second init changed the mayor:\n%+v\n%+v", again, r)
	}
	if _, err := run(t, a.rootCmd(), "init", "--harness", "kimi", "--mayor", "boss", "--model", "k2", "--dir", home); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(mayor); string(b) != "mine" {
		t.Fatalf("edited mayor.md overwritten: %q", b)
	}
	boss, err := st.GetResident(ctx, "boss")
	if err != nil {
		t.Fatal(err)
	}
	if boss.Config["model"] != "k2" || boss.Dir != home || !reflect.DeepEqual(boss.Config["args"], []any{"--yolo"}) {
		t.Fatalf("boss = %+v", boss)
	}

	// Bad input is refused before anything is written.
	for _, args := range [][]string{
		{"init", "--harness", "nope", "--mayor", "x"},
		{"init", "--harness", "pi", "--mayor", "x", "--dir", filepath.Join(home, "missing")},
		{"init", "--harness", "pi", "--mayor", "bad name"},
	} {
		if _, err := run(t, a.rootCmd(), args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if _, err := st.GetResident(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused init left a resident: %v", err)
	}
}

// Init again on a set-up yard keeps default_kind, operator and mayor unless
// the flags say otherwise, and says so; --harness is required only to
// create a yard.
func TestInitAgainKeepsSettings(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	t.Setenv("USER", "kim")
	if _, err := run(t, a.rootCmd(), "init"); err == nil || !strings.Contains(err.Error(), "--harness") {
		t.Fatalf("init without --harness on a fresh home: %v", err)
	}
	if _, err := run(t, a.rootCmd(), "init", "--harness", "codex", "--operator", "Jo", "--mayor", "boss"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"default_kind": "codex", "operator": "Jo", "mayor": "boss"}
	settings := func(step string) {
		t.Helper()
		got, _ := st.Settings(ctx)
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", step, k, got[k], v)
			}
		}
	}
	out, err := run(t, a.rootCmd(), "init")
	if err != nil {
		t.Fatal(err)
	}
	settings("init again")
	if !strings.Contains(out, "  kept     default_kind codex, operator Jo, mayor boss, as set before") ||
		!strings.Contains(out, "harness  codex") || !strings.Contains(out, "boss, already a resident: kept") {
		t.Errorf("init again does not say what it kept:\n%s", out)
	}
	if rs, _ := st.Residents(ctx); len(rs) != 1 {
		t.Errorf("init again added a resident: %v", rs)
	}
	// A flag given changes its setting alone.
	if out, err = run(t, a.rootCmd(), "init", "--operator", "Kim"); err != nil {
		t.Fatal(err)
	}
	want["operator"] = "Kim"
	settings("--operator")
	if !strings.Contains(out, "  kept     default_kind codex, mayor boss, as set before") {
		t.Errorf("--operator:\n%s", out)
	}
	if out, err = run(t, a.rootCmd(), "init", "--harness", "kimi"); err != nil {
		t.Fatal(err)
	}
	want["default_kind"] = "kimi"
	settings("--harness")
	if !strings.Contains(out, "  kept     operator Kim, mayor boss, as set before") {
		t.Errorf("--harness:\n%s", out)
	}
	// A bad harness is refused on a set-up yard as on a fresh one.
	if _, err := run(t, a.rootCmd(), "init", "--harness", "nope"); err == nil {
		t.Error("init --harness nope accepted")
	}
	settings("refused")
}

// Without --operator the roles name the login user.
func TestInitOperatorDefaultsToUser(t *testing.T) {
	home := newHome(t)
	t.Setenv("USER", "kim")
	if _, err := run(t, (&app{st: storetest.New(t)}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "roles", "builder.md")); !bytes.Contains(b, []byte("never kim's own session")) {
		t.Fatalf("builder.md does not name $USER:\n%s", b)
	}
}

// noLogin is an environment that names no user and a machine that knows
// none: env -i in a container.
func noLogin(t *testing.T) {
	t.Helper()
	t.Setenv("USER", "")
	t.Setenv("LOGNAME", "")
	was := loginName
	loginName = func() string { return "" }
	t.Cleanup(func() { loginName = was })
}

// The default operator is a name a user may have, wherever it comes from:
// $USER, then $LOGNAME, then the OS's own, and "operator" when none of
// them gives one. It was "the operator" without $USER, which is no user
// name, so init failed there.
func TestDefaultOperator(t *testing.T) {
	for _, c := range []struct{ user, logname, login, want string }{
		{"kim", "lee", "sam", "kim"},
		{"", "lee", "sam", "lee"},
		{"", "", "sam", "sam"},
		{"", "", "", "operator"},
		{`CORP\kim`, "", "sam", "sam"},
		{"kim lee", "the operator", "-x", "operator"},
	} {
		noLogin(t)
		t.Setenv("USER", c.user)
		t.Setenv("LOGNAME", c.logname)
		loginName = func() string { return c.login }
		got := defaultOperator()
		if got != c.want {
			t.Errorf("USER=%q LOGNAME=%q login %q: %q, want %q", c.user, c.logname, c.login, got, c.want)
		}
		if !store.ValidUserName(got) {
			t.Errorf("USER=%q LOGNAME=%q login %q: %q is no user name", c.user, c.logname, c.login, got)
		}
	}
}

// Init where nothing names the user (env -i, a container, a service unit)
// makes the yard, with the operator "operator": a user, whom the roles and
// a prime name, and as whom one may act.
func TestInitWithoutLoginName(t *testing.T) {
	home := newHome(t)
	noLogin(t)
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude", "--about", "reads mail at noon"); err != nil {
		t.Fatalf("init without a login name: %v", err)
	}
	if all, _ := st.Settings(ctx); all["operator"] != "operator" {
		t.Fatalf("operator setting = %q", all["operator"])
	}
	users, err := st.Users(ctx)
	if err != nil || len(users) != 1 || users[0].Name != "operator" {
		t.Fatalf("users = %+v, %v", users, err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "roles", "builder.md")); !bytes.Contains(b, []byte("never operator's own session")) {
		t.Fatalf("builder.md does not name the operator:\n%s", b)
	}
	if got := yardOperator(ctx, st); got != "operator" {
		t.Errorf("yardOperator = %q", got)
	}
	if p, err := primeOperator(ctx, st); err != nil || p == nil || p.Name != "operator" {
		t.Errorf("prime's operator = %+v, %v", p, err)
	}
	if name, is, err := (&app{st: st, actor: "operator"}).operator(ctx, st); err != nil || !is || name != "operator" {
		t.Errorf("acting as operator: %q, %v, %v", name, is, err)
	}
	// Again, as after an upgrade: the setting is kept, nothing fails.
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init"); err != nil {
		t.Fatalf("init again: %v", err)
	}
}

// A harness without unattended flags gets none, and the summary says so.
func TestInitHarnessWithoutUnattendedFlags(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	out, err := run(t, (&app{st: st}).rootCmd(), "init", "--harness", "gemini")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := st.GetResident(context.Background(), "mayor")
	if _, ok := r.Config["args"]; ok || r.Config["kind"] != "gemini" {
		t.Fatalf("mayor config = %v", r.Config)
	}
	if !strings.Contains(out, "gemini may stop to ask") {
		t.Fatalf("summary:\n%s", out)
	}
}

// A yard outside the default home gets --home <home> in every
// command its summary prints, and the default home gets none (yard-0wys).
func TestInitSummaryNamesTheHome(t *testing.T) {
	home := newHome(t)
	out, err := run(t, (&app{st: storetest.New(t)}).rootCmd(), "init", "--harness", "gemini")
	if err != nil {
		t.Fatal(err)
	}
	yardr := "yardr --home " + home
	for _, s := range []string{"\n  " + yardr + " daemon\n", yardr + " resident set mayor 'args=[...]'",
		"every yardr command for it needs\n--home " + home + ", or YARDR_HOME exported"} {
		if !strings.Contains(out, s) {
			t.Errorf("summary lacks %q:\n%s", s, out)
		}
	}

	// The default home, named with a trailing slash: neither flag nor variable.
	user := shortDir(t, "yu")
	t.Setenv("HOME", user)
	t.Setenv("YARDR_HOME", filepath.Join(user, ".yardr")+"/")
	out, err = run(t, (&app{st: storetest.New(t)}).rootCmd(), "init", "--harness", "gemini")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "YARDR_HOME") || strings.Contains(out, "--home") || !strings.Contains(out, "\n  yardr daemon\n") ||
		!strings.Contains(out, " yardr resident set mayor 'args=[...]'") {
		t.Errorf("default home summary:\n%s", out)
	}
	if got := (notInitialized{filepath.Join(user, ".yardr")}).Error(); strings.Contains(got, "YARDR_HOME") || strings.Contains(got, "--home") {
		t.Errorf("default home hint = %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{"/tmp/y3": "/tmp/y3", "/tmp/my yard": "'/tmp/my yard'",
		"/tmp/it's": `'/tmp/it'\''s'`, "": "''"} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

// With no terminal, a first run prints how to initialize and exits 2,
// creating nothing; the commands that never need a yard still run.
func TestFirstRunWithoutTerminal(t *testing.T) {
	home := newHome(t)
	setInteractive(t, false)
	var errs bytes.Buffer
	old := stderr
	stderr = &errs
	t.Cleanup(func() { stderr = old })

	if code := execute(&app{}, []string{"bead", "list"}); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if got := errs.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "`yardr --home "+home+" init --harness") {
		t.Fatalf("stderr = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, "yard.db")); err == nil {
		t.Fatal("a store was created")
	}
	errs.Reset()
	for _, args := range [][]string{{"help"}, {"init", "--help"}, {"server", "--help"}} {
		var code int
		capture(t, func() { code = execute(&app{}, args) })
		if code != 0 {
			t.Errorf("%v: exit %d: %s", args, code, errs.String())
		}
	}
}

// A home is set up once init stored default_kind: not by its store file,
// which the server creates on any first command, and not by an empty store.
// A store already in use without the setting (a yard from before the
// onboarding) is set up too (yard-w6np).
func TestInitialized(t *testing.T) {
	newHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, err := (&app{}).initialized(ctx, cfg); ok || err != nil {
		t.Fatalf("empty home: %v %v", ok, err)
	}
	if _, err := os.Stat(cfg.DBPath()); err == nil {
		t.Fatal("asking created a store")
	}
	st := storetest.New(t)
	a := &app{st: st}
	if ok, err := a.initialized(ctx, cfg); ok || err != nil {
		t.Fatalf("store without default_kind: %v %v", ok, err)
	}
	if err := st.SetSetting(ctx, "default_kind", "claude", "me"); err != nil {
		t.Fatal(err)
	}
	if ok, err := a.initialized(ctx, cfg); !ok || err != nil {
		t.Fatalf("store with default_kind: %v %v", ok, err)
	}
	old := storetest.New(t)
	if err := old.PutRig(ctx, store.Rig{Name: "r", Path: "/x", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if ok, err := (&app{st: old}).initialized(ctx, cfg); !ok || err != nil {
		t.Fatalf("store with a rig, without default_kind: %v %v", ok, err)
	}
	for name, fill := range map[string]func(store.API) error{
		"group": func(st store.API) error { return st.PutGroup(ctx, store.Group{Name: "g", Runner: "manual"}) },
		"resident": func(st store.API) error {
			_, err := st.PutResident(ctx, store.Resident{Name: "m", Runner: "herdr"}, "me")
			return err
		},
		"bead": func(st store.API) error {
			_, err := st.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
			return err
		},
	} {
		st := storetest.New(t)
		if err := fill(st); err != nil {
			t.Fatal(err)
		}
		if ok, err := (&app{st: st}).initialized(ctx, cfg); !ok || err != nil {
			t.Fatalf("store with a %s, without default_kind: %v %v", name, ok, err)
		}
	}
}

// A yard from before the onboarding (one rig, no settings) keeps working
// without a terminal: its commands run and bare yardr is help. Only an
// empty store refuses (yard-w6np: an upgrade locked ~/.yardr out).
func TestOldYardIsNotOnboarded(t *testing.T) {
	newHome(t)
	setInteractive(t, false)
	ctx := context.Background()
	st := storetest.New(t)
	if err := st.PutRig(ctx, store.Rig{Name: "r", Path: "/x", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, (&app{st: st}).rootCmd(), "session", "list"); err != nil {
		t.Fatalf("session list: %v", err)
	}
	if out, err := run(t, (&app{st: st}).rootCmd()); err != nil || !strings.Contains(out, "Usage:") {
		t.Fatalf("bare yardr: %v\n%s", err, out)
	}
	if _, err := run(t, (&app{st: storetest.New(t)}).rootCmd(), "session", "list"); exitCode(err) != 2 {
		t.Fatalf("empty store: %v", err)
	}
}

// The escape hatch is never behind the gate it unlocks: settings works on a
// store the check calls not set up, and setting default_kind sets it up.
func TestSettingsWithoutYard(t *testing.T) {
	newHome(t)
	setInteractive(t, false)
	st := storetest.New(t)
	if _, err := run(t, (&app{st: st}).rootCmd(), "settings", "set", "default_kind", "codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, (&app{st: st}).rootCmd(), "session", "list"); err != nil {
		t.Fatalf("after settings set: %v", err)
	}
}

// The daemon runs on a store without default_kind, empty even, and without a
// terminal: after it replaced itself into a build with the onboarding check
// it was refused and ended (yard-w6np).
func TestDaemonWithoutYard(t *testing.T) {
	newHome(t)
	setInteractive(t, false)
	// herdr is not running in an empty config dir: the daemon only warns.
	xdg, err := os.MkdirTemp("", "yw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(xdg) })
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "c"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(xdg, "s"))
	t.Setenv("HERDR_SOCKET_PATH", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: storetest.New(t), cfg: cfg}
	root := a.rootCmd()
	root.SetArgs([]string{"daemon", "--interval", "50ms"})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = root.ExecuteContext(ctx)
	if errors.As(err, new(notInitialized)) || time.Since(start) < 400*time.Millisecond {
		t.Fatalf("daemon ended after %v: %v", time.Since(start), err)
	}
}

// Bare yardr on a store nobody set up (the server made it) runs the
// onboarding; on a set-up home whose yard runs, and with --help anywhere, it
// is help.
func TestBareYardr(t *testing.T) {
	home := newHome(t)
	setInteractive(t, true)
	stubLookPath(t, "claude", "git", "herdr")
	asked := 0
	stubAsk(t, func(...huh.Field) error { asked++; return nil })
	ctx := context.Background()

	// --help on a home that would onboard: help, nothing asked.
	st := storetest.New(t)
	stubStart(t, st, mayorHandle())
	stubPane(t, answeredPane)
	stubFocus(t)
	for _, h := range []string{"--help", "-h"} {
		out, err := run(t, (&app{st: st}).rootCmd(), h)
		if err != nil || asked != 0 || !strings.Contains(out, "Usage:") {
			t.Fatalf("%s: asked %d, %v\n%s", h, asked, err, out)
		}
	}

	out, err := run(t, (&app{st: st}).rootCmd())
	if err != nil {
		t.Fatal(err)
	}
	// The form's five pages, whether to ask the harness first, the two
	// services it offers once the yard runs, and a link with another yard.
	if asked != 10 || !strings.Contains(out, "yard created") || strings.Contains(out, "Usage:") {
		t.Fatalf("store without default_kind: asked %d\n%s", asked, out)
	}
	if _, err := st.GetResident(ctx, "mayor"); err != nil {
		t.Fatalf("no mayor: %v", err)
	}

	// The daemon the onboarding started runs.
	holdDaemon(t, home)
	asked = 0
	out, err = run(t, (&app{st: st}).rootCmd())
	if err != nil || asked != 0 || !strings.Contains(out, "Usage:") {
		t.Fatalf("set-up home: asked %d, %v\n%s", asked, err, out)
	}
}

// With no terminal, a store nobody set up refuses like a home with none.
func TestStoreWithoutSettingWithoutTerminal(t *testing.T) {
	newHome(t)
	setInteractive(t, false)
	_, err := run(t, (&app{st: storetest.New(t)}).rootCmd(), "bead", "list")
	if exitCode(err) != 2 || !strings.Contains(fmt.Sprint(err), here("yardr init --harness")) {
		t.Fatalf("err = %v (exit %d)", err, exitCode(err))
	}
}

// The onboarding form, answered with its defaults, creates the yard on the
// first installed harness and then runs the command asked for.
func TestOnboardingForm(t *testing.T) {
	home := newHome(t)
	setInteractive(t, true)
	stubLookPath(t, "codex", "git", "herdr")
	asked := 0
	stubAsk(t, func(...huh.Field) error { asked++; return nil })
	st := storetest.New(t)
	stubStart(t, st, mayorHandle())
	stubPane(t, answeredPane)
	stubFocus(t)
	ctx := context.Background()

	out, err := run(t, (&app{st: st}).rootCmd(), "resident", "list")
	if err != nil {
		t.Fatal(err)
	}
	if asked != 10 {
		t.Fatalf("%d form pages", asked)
	}
	r, err := st.GetResident(ctx, "mayor")
	if err != nil {
		t.Fatal(err)
	}
	if r.Config["kind"] != "codex" || r.Dir != filepath.Dir(home) ||
		!reflect.DeepEqual(r.Config["args"], []any{"--dangerously-bypass-approvals-and-sandbox"}) {
		t.Fatalf("mayor = %+v", r)
	}
	if got, _ := st.Settings(ctx); got["default_kind"] != "codex" {
		t.Fatalf("settings = %v", got)
	}
	if !strings.Contains(out, "yard created") || !strings.Contains(out, "herdr · codex") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestOnboardingCancelled(t *testing.T) {
	newHome(t)
	setInteractive(t, true)
	stubLookPath(t)
	stubAsk(t, func(...huh.Field) error { return huh.ErrUserAborted })
	st := storetest.New(t)
	_, err := run(t, (&app{st: st}).rootCmd(), "bead", "list")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v", err)
	}
	if rs, _ := st.Residents(context.Background()); len(rs) != 0 {
		t.Fatalf("residents = %v", rs)
	}
}

// The harness list marks what is installed and defaults to the first of it.
func TestHarnessOptions(t *testing.T) {
	stubLookPath(t, "kimi", "cursor-agent")
	opts, def := harnessOptions()
	if def != "kimi" {
		t.Fatalf("default = %s", def)
	}
	labels := map[string]string{}
	for _, o := range opts {
		labels[o.Value] = o.Key
	}
	if !strings.HasSuffix(labels["kimi"], "installed") || !strings.HasSuffix(labels["cursor"], "installed") ||
		!strings.HasSuffix(labels["claude"], "not on PATH") {
		t.Fatalf("labels = %v", labels)
	}
	stubLookPath(t)
	if _, def := harnessOptions(); def != "claude" {
		t.Fatalf("nothing installed: default %s", def)
	}
}

// group add and resident add without --set kind take the default_kind.
func TestDefaultKind(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	if _, err := run(t, a.settingsCmd(), "set", "default_kind", "nope"); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if _, err := run(t, a.settingsCmd(), "get", "default_kind"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unset get: %v", err)
	}
	if _, err := run(t, a.settingsCmd(), "set", "default_kind", "kimi"); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.settingsCmd(), "get", "default_kind"); err != nil || out != "kimi\n" {
		t.Fatalf("get = %q %v", out, err)
	}
	if _, err := run(t, a.settingsCmd(), "get", "nope"); err == nil {
		t.Fatal("unknown key read")
	}

	if _, err := run(t, a.groupCmd(), "add", "b", "--runner", "herdr"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.groupCmd(), "add", "c", "--runner", "herdr", "--set", "kind=claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.groupCmd(), "add", "m", "--runner", "manual"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]any{"b": "kimi", "c": "claude", "m": nil} {
		if g, _ := st.GetGroup(ctx, name); g.Config["kind"] != want {
			t.Errorf("group %s kind = %v, want %v", name, g.Config["kind"], want)
		}
	}
	if _, err := run(t, a.residentCmd(), "add", "r"); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.GetResident(ctx, "r"); r.Config["kind"] != "kimi" {
		t.Fatalf("resident config = %v", r.Config)
	}

	if _, err := run(t, a.settingsCmd(), "set", "default_kind", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Settings(ctx); len(got) != 0 {
		t.Fatalf("unset left %v", got)
	}
}

func setInteractive(t *testing.T, v bool) {
	old := interactive
	interactive = func() bool { return v }
	t.Cleanup(func() { interactive = old })
}

func stubAsk(t *testing.T, fn func(...huh.Field) error) {
	old := ask
	ask = fn
	t.Cleanup(func() { ask = old })
}

// stubLookPath finds only the named executables.
func stubLookPath(t *testing.T, found ...string) {
	old := lookPath
	lookPath = func(name string) (string, error) {
		for _, f := range found {
			if f == name {
				return "/bin/" + name, nil
			}
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = old })
}

// Init again refreshes a role the operator never touched to this build's
// template and keeps an edited one, telling which: the manifest holds the
// hash of each file as init wrote it. A file from before the manifest is
// kept unless it already reads as this build's.
func TestInitRefreshesUntouchedRoles(t *testing.T) {
	home := newHome(t)
	a := &app{st: storetest.New(t), actor: "me"}
	if _, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--operator", "Jo"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "roles")
	manifest := func() map[string]string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, shippedManifest))
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v\n%s", shippedManifest, err, b)
		}
		return m
	}
	m := manifest()
	for _, name := range []string{"mayor.md", "builder.md", "merger.md", "reviewer.md"} {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		if m[name] != digest(b) {
			t.Fatalf("manifest %s = %q, file hashes to %q", name, m[name], digest(b))
		}
	}

	// builder.md: as an older init wrote it (the manifest agrees with the
	// file), so it is refreshed. mayor.md: edited, kept. merger.md: from
	// before the manifest and not this build's, kept. reviewer.md: from
	// before the manifest but already this build's, tracked from now on.
	old := []byte("# Builder\n\nAn older template.\n")
	if err := os.WriteFile(filepath.Join(dir, "builder.md"), old, 0o644); err != nil {
		t.Fatal(err)
	}
	m["builder.md"] = digest(old)
	if err := os.WriteFile(filepath.Join(dir, "mayor.md"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "merger.md"), []byte("theirs"), 0o644); err != nil {
		t.Fatal(err)
	}
	delete(m, "merger.md")
	delete(m, "reviewer.md")
	mb, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, shippedManifest), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--operator", "Jo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "builder.md refreshed to this build's; mayor.md, merger.md edited, kept") {
		t.Fatalf("summary does not say which:\n%s", out)
	}
	tmpl, _ := roles.FS.ReadFile("builder.md")
	if b, _ := os.ReadFile(filepath.Join(dir, "builder.md")); !bytes.Equal(b, fillRole(tmpl, "Jo", "mayor")) {
		t.Fatalf("builder.md not refreshed:\n%s", b)
	}
	for name, want := range map[string]string{"mayor.md": "mine", "merger.md": "theirs"} {
		if b, _ := os.ReadFile(filepath.Join(dir, name)); string(b) != want {
			t.Fatalf("%s overwritten: %q", name, b)
		}
	}
	m = manifest()
	tmpl, _ = roles.FS.ReadFile("reviewer.md")
	if m["reviewer.md"] != digest(fillRole(tmpl, "Jo", "mayor")) || m["merger.md"] != "" || m["mayor.md"] == digest([]byte("mine")) {
		t.Fatalf("manifest after refresh = %v", m)
	}
	// --json names them too.
	var res struct {
		Refreshed []string `json:"refreshed"`
		Kept      []string `json:"kept"`
		Current   []string `json:"current"`
	}
	js, err := run(t, a.rootCmd(), "--json", "init", "--harness", "claude", "--operator", "Jo")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(js), &res); err != nil {
		t.Fatalf("%v\n%s", err, js)
	}
	if len(res.Refreshed) != 0 || len(res.Kept) != 2 || len(res.Current) != 2 {
		t.Fatalf("third init: %+v", res)
	}
}

// A rig's .yardr/roles/ is never refreshed: the files are committed, and rig
// init writes no manifest there.
func TestRigInitWritesNoManifest(t *testing.T) {
	st := storetest.New(t)
	dir := t.TempDir()
	if err := st.PutRig(context.Background(), store.Rig{Name: "web", Path: dir, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	if _, err := run(t, a.rigCmd(), "init", "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".yardr", "roles", shippedManifest)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rig init wrote a manifest: %v", err)
	}
	old := []byte("older template, untouched")
	if err := os.WriteFile(filepath.Join(dir, ".yardr", "roles", "builder.md"), old, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.rigCmd(), "init", "web"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".yardr", "roles", "builder.md")); !bytes.Equal(b, old) {
		t.Fatalf("rig init refreshed a committed role:\n%s", b)
	}
}
