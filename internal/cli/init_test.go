package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/roles"
)

// newHome points YARDR_HOME at a fresh directory with no yard in it.
func newHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "y")
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
		want := bytes.ReplaceAll(tmpl, []byte("{{operator}}"), []byte("Jo"))
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
	want := map[string]any{"kind": "claude", "prompt": filepath.Join(home, "roles", "mayor.md"),
		"args": []any{"--permission-mode", "auto"}}
	if !reflect.DeepEqual(r.Config, want) || r.Runner != "herdr" || r.MaxAge != "6h" ||
		r.Dir != filepath.Dir(home) || !reflect.DeepEqual(r.On, defaultOn) {
		t.Fatalf("mayor = %+v", r)
	}
	if got, _ := st.Settings(ctx); got["default_kind"] != "claude" || got["operator"] != "Jo" {
		t.Fatalf("settings = %v", got)
	}
	// The mayor takes it from here; the commands are in init --help.
	for _, s := range []string{"harness  claude", "naming Jo", ".yardr/roles/", "yardr daemon", "sets up the rest with you in its pane"} {
		if !strings.Contains(out, s) {
			t.Errorf("summary lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "yardr route add") {
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
		{"init"},
	} {
		if _, err := run(t, a.rootCmd(), args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if _, err := st.GetResident(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused init left a resident: %v", err)
	}
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

// A yard outside the default home gets YARDR_HOME=<home> in front of every
// command its summary prints, and the default home gets none (yard-0wys).
func TestInitSummaryNamesTheHome(t *testing.T) {
	home := newHome(t)
	out, err := run(t, (&app{st: storetest.New(t)}).rootCmd(), "init", "--harness", "gemini")
	if err != nil {
		t.Fatal(err)
	}
	env := "YARDR_HOME=" + home + " "
	for _, s := range []string{"\n  " + env + "yardr daemon\n", env + "yardr resident add mayor",
		"every yardr command for it needs\nYARDR_HOME=" + home + " in front of it, or exported"} {
		if !strings.Contains(out, s) {
			t.Errorf("summary lacks %q:\n%s", s, out)
		}
	}

	// The default home, named with a trailing slash: no variable.
	user := t.TempDir()
	t.Setenv("HOME", user)
	t.Setenv("YARDR_HOME", filepath.Join(user, ".yardr")+"/")
	out, err = run(t, (&app{st: storetest.New(t)}).rootCmd(), "init", "--harness", "gemini")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "YARDR_HOME") || !strings.Contains(out, "\n  yardr daemon\n") ||
		!strings.Contains(out, " yardr resident add mayor") {
		t.Errorf("default home summary:\n%s", out)
	}
	if got := (notInitialized{filepath.Join(user, ".yardr")}).Error(); strings.Contains(got, "YARDR_HOME") {
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
	if got := errs.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "`YARDR_HOME="+home+" yardr init --harness") {
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
// onboarding; on a set-up home, and with --help anywhere, it is help.
func TestBareYardr(t *testing.T) {
	newHome(t)
	setInteractive(t, true)
	stubLookPath(t, "claude")
	asked := 0
	stubAsk(t, func(...huh.Field) error { asked++; return nil })
	ctx := context.Background()

	// --help on a home that would onboard: help, nothing asked.
	st := storetest.New(t)
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
	if asked != 3 || !strings.Contains(out, "yard created") || strings.Contains(out, "Usage:") {
		t.Fatalf("store without default_kind: asked %d\n%s", asked, out)
	}
	if _, err := st.GetResident(ctx, "mayor"); err != nil {
		t.Fatalf("no mayor: %v", err)
	}

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
	if exitCode(err) != 2 || !strings.Contains(fmt.Sprint(err), "yardr init --harness") {
		t.Fatalf("err = %v (exit %d)", err, exitCode(err))
	}
}

// The onboarding form, answered with its defaults, creates the yard on the
// first installed harness and then runs the command asked for.
func TestOnboardingForm(t *testing.T) {
	home := newHome(t)
	setInteractive(t, true)
	stubLookPath(t, "codex")
	asked := 0
	stubAsk(t, func(...huh.Field) error { asked++; return nil })
	st := storetest.New(t)
	ctx := context.Background()

	out, err := run(t, (&app{st: st}).rootCmd(), "resident", "list")
	if err != nil {
		t.Fatal(err)
	}
	if asked != 3 {
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
