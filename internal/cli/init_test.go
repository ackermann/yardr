package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/packs"
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
	t.Setenv("YARDR_WEB_ADDR", "")
	t.Setenv("YARDR_BDP_ADDR", "")
	home := filepath.Join(shortDir(t, "yh"), "y")
	t.Setenv("YARDR_HOME", home)
	return home
}

// packRole is the role file rel of the pack yardr embeds as name, as an
// import installs it: naming operator, and yardmaster as the crew member
// that takes the pack's.
func packRole(t *testing.T, name, rel, operator, yardmaster string) []byte {
	t.Helper()
	tmpl, err := packs.FS.ReadFile(name + "/" + rel)
	if err != nil {
		t.Fatalf("pack %s: %v", name, err)
	}
	return pack.Fill(tmpl, map[string]string{pack.Operator: operator, "yardmaster": yardmaster})
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

	// The yardmaster's role alone, from the default pack and where an
	// import of it installs its roles, naming the operator. No <home>/roles.
	prompt := filepath.Join(config.At(home).PackRoles("starter"), "yardmaster.md")
	got, err := os.ReadFile(prompt)
	if err != nil || !bytes.Equal(got, packRole(t, "starter", "roles/yardmaster.md", "Jo", "yardmaster")) {
		t.Fatalf("yardmaster.md installed: %v\n%s", err, got)
	}
	if bytes.Contains(got, []byte("{{")) || !bytes.Contains(got, []byte("bring to Jo")) {
		t.Errorf("yardmaster.md keeps a placeholder, or does not name Jo:\n%s", got)
	}
	var files []string
	for p := range snapshot(t, home) {
		files = append(files, strings.TrimPrefix(p, home+"/"))
	}
	if !slices.Equal(files, []string{"packs/starter/roles/yardmaster.md"}) {
		t.Fatalf("init wrote into the home: %v; want the yardmaster's role alone", files)
	}
	// The yardmaster, on the harness with its unattended flags.
	r, err := st.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	// greet: its first session writes to the operator by mail (yardr-8j3n).
	want := map[string]any{"kind": "claude", "prompt": prompt,
		"args": []any{"--permission-mode", "auto"}, "greet": "Jo"}
	if !reflect.DeepEqual(r.Config, want) || r.Runner != "herdr" || r.MaxAge != "6h" ||
		r.Dir != filepath.Dir(home) || !reflect.DeepEqual(r.On, defaultOn) {
		t.Fatalf("yardmaster = %+v", r)
	}
	// The roles tell a stuck agent that a hold reaches the yardmaster's
	// inbox, so the yardmaster must hear held, whatever else the class it
	// hears carries.
	if !slices.Equal(r.On, []string{store.KindAttention}) || !slices.Contains(store.AttentionKinds, "held") {
		t.Fatalf("yardmaster does not hear held: on = %v, the class = %v", r.On, store.AttentionKinds)
	}
	// A bead stranded by a flow change is routed to nobody: the yardmaster is
	// the one who must hear of it, and the form must offer the kind.
	if !slices.Contains(store.AttentionKinds, "stranded") || !slices.Contains(knownKinds, "stranded") || !slices.Contains(store.DispatchKinds, "stranded") {
		t.Fatalf("stranded is not heard: on = %v", r.On)
	}
	if got, _ := st.Settings(ctx); got["default_kind"] != "claude" || got["operator"] != "Jo" || got["yardmaster"] != "yardmaster" {
		t.Fatalf("settings = %v", got)
	}
	// The yardmaster takes it from here; the commands are in init --help.
	for _, s := range []string{"harness    claude", "naming Jo", "roles      " + filepath.Dir(prompt) + " (pack starter, the yard's default)", here("yardr daemon"), "\n  " + here("yardr chat") + "\n  " + here("yardr setup") + "\n"} {
		if !strings.Contains(out, s) {
			t.Errorf("summary lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "route add") {
		t.Errorf("summary still lists the setup commands:\n%s", out)
	}
	if help, _ := run(t, a.rootCmd(), "init", "--help"); !strings.Contains(help, "yardr depot init webapp --yes") ||
		!strings.Contains(help, "yardr depot add webapp") {
		t.Errorf("init --help lacks the commands for scripts:\n%s", help)
	}

	// Again: init is idempotent; the yardmaster is not replaced, and its role
	// is not written again, edited or not: a yard's prompts are not init's
	// to change.
	yardmaster := prompt
	if err := os.WriteFile(yardmaster, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--operator", "Jo"); err != nil ||
		!strings.Contains(out, "already a crew member: kept") {
		t.Fatalf("second init: %v\n%s", err, out)
	}
	if again, _ := st.GetCrew(ctx, "yardmaster"); !reflect.DeepEqual(again, r) {
		t.Fatalf("second init changed the yardmaster:\n%+v\n%+v", again, r)
	}
	// A second init that changes settings is the operator's.
	if _, err := run(t, a.rootCmd(), "init", "--harness", "kimi", "--yardmaster", "boss", "--model", "k2", "--dir", home, "--actor", "Jo"); err != nil {
		t.Fatal(err)
	}
	// A second crew member by init finds the role there and leaves it: the
	// first one reads it.
	if b, _ := os.ReadFile(yardmaster); string(b) != "mine" {
		t.Fatalf("edited yardmaster.md overwritten: %q", b)
	}
	boss, err := st.GetCrew(ctx, "boss")
	if err != nil {
		t.Fatal(err)
	}
	if boss.Config["prompt"] != prompt || boss.Config["model"] != "k2" || boss.Dir != home || !reflect.DeepEqual(boss.Config["args"], []any{"--yolo"}) {
		t.Fatalf("boss = %+v", boss)
	}

	// Bad input is refused before anything is written.
	for _, args := range [][]string{
		{"init", "--harness", "nope", "--yardmaster", "x"},
		{"init", "--harness", "pi", "--yardmaster", "x", "--dir", filepath.Join(home, "missing")},
		{"init", "--harness", "pi", "--yardmaster", "bad name"},
	} {
		if _, err := run(t, a.rootCmd(), args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if _, err := st.GetCrew(ctx, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("refused init left a crew member: %v", err)
	}
}

// Init again on a set-up yard keeps default_kind, operator and yardmaster
// unless the flags say otherwise, and says so; --harness is required only to
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
	if _, err := run(t, a.rootCmd(), "init", "--harness", "codex", "--operator", "Jo", "--yardmaster", "boss"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"default_kind": "codex", "operator": "Jo", "yardmaster": "boss"}
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
	if !strings.Contains(out, "  kept       default_kind codex, operator Jo, yardmaster boss, as set before") ||
		!strings.Contains(out, "harness    codex") || !strings.Contains(out, "boss, already a crew member: kept") {
		t.Errorf("init again does not say what it kept:\n%s", out)
	}
	if rs, _ := st.Crew(ctx); len(rs) != 1 {
		t.Errorf("init again added a crew member: %v", rs)
	}
	// An operator recorded before default_kind means setup was interrupted.
	// A different caller cannot use init's bootstrap path to replace them.
	if err := st.SetSetting(ctx, "default_kind", "", "Jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.rootCmd(), "init", "--harness", "kimi", "--actor", "kim"); err == nil ||
		!strings.Contains(err.Error(), "only the operator (Jo) may change a setting; this is kim") {
		t.Fatalf("init on interrupted setup as kim: %v", err)
	}
	if got, _ := st.Settings(ctx); got["operator"] != "Jo" || got["default_kind"] != "" {
		t.Fatalf("the refused init changed settings: %v", got)
	}
	if _, err := run(t, a.rootCmd(), "init", "--harness", "codex", "--actor", "Jo"); err != nil {
		t.Fatalf("operator could not finish interrupted setup: %v", err)
	}
	// A flag given changes its setting alone, for the operator: anyone
	// else is refused, and nothing is changed.
	if _, err := run(t, a.rootCmd(), "init", "--operator", "Kim"); err == nil ||
		!strings.Contains(err.Error(), "only the operator (Jo) may change a setting; this is kim") {
		t.Fatalf("init --operator Kim as kim: %v", err)
	}
	settings("init --operator, refused")
	if out, err = run(t, a.rootCmd(), "init", "--operator", "Kim", "--actor", "Jo"); err != nil {
		t.Fatal(err)
	}
	want["operator"] = "Kim"
	settings("--operator")
	if !strings.Contains(out, "  kept       default_kind codex, yardmaster boss, as set before") {
		t.Errorf("--operator:\n%s", out)
	}
	if out, err = run(t, a.rootCmd(), "init", "--harness", "kimi", "--actor", "Kim"); err != nil {
		t.Fatal(err)
	}
	want["default_kind"] = "kimi"
	settings("--harness")
	if !strings.Contains(out, "  kept       operator Kim, yardmaster boss, as set before") {
		t.Errorf("--harness:\n%s", out)
	}
	// A bad harness is refused on a set-up yard as on a fresh one.
	if _, err := run(t, a.rootCmd(), "init", "--harness", "nope"); err == nil {
		t.Error("init --harness nope accepted")
	}
	settings("refused")
}

// A yard that names no operator takes its first one from no agent of its
// own. Init names it as who the terminal acts as, not as the operator it
// proposes: a crew member is refused there as at `settings set`.
func TestInitNamesTheFirstOperatorAsItsCaller(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "boss", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	head, _ := st.Head(ctx)
	a := &app{st: st}
	if _, err := run(t, a.rootCmd(), "init", "--harness", "codex", "--operator", "Jo", "--yardmaster", "boss", "--actor", "boss"); err == nil ||
		!strings.Contains(err.Error(), "only the operator may change a setting; this is the crew member boss") {
		t.Fatalf("init --operator Jo as the crew member boss: %v", err)
	}
	if got, _ := st.Settings(ctx); len(got) != 0 {
		t.Fatalf("the refused init stored settings: %v", got)
	}
	if now, _ := st.Head(ctx); now != head {
		t.Fatalf("the refused init wrote events: head %d, was %d", now, head)
	}
	// Anyone who is no agent of the yard names it, and the rest of the new
	// yard's settings are then written as that operator.
	if _, err := run(t, a.rootCmd(), "init", "--harness", "codex", "--operator", "Jo", "--yardmaster", "boss", "--actor", "kim"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Settings(ctx); got["operator"] != "Jo" || got["default_kind"] != "codex" {
		t.Fatalf("settings after init: %v", got)
	}
}

// Without --operator the role names the login user.
func TestInitOperatorDefaultsToUser(t *testing.T) {
	home := newHome(t)
	t.Setenv("USER", "kim")
	if _, err := run(t, (&app{st: storetest.New(t)}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(config.At(home).PackRoles("starter"), "yardmaster.md")); !bytes.Contains(b, []byte("bring to kim")) {
		t.Fatalf("yardmaster.md does not name $USER:\n%s", b)
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

// typeName answers the form's "Your name" as keys would: the name typed over
// what is there, then enter. It returns what the field then says in place,
// nil when it took the name.
func typeName(t *testing.T, name string) (was string, refused error) {
	t.Helper()
	var operator = config.Actor()
	was = operator
	f := nameField(&operator)
	f.WithKeyMap(huh.NewDefaultKeyMap())
	f.Focus()
	f.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)})
	f.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if f.Error() == nil && operator != name {
		t.Fatalf("typed %q, the field holds %q", name, operator)
	}
	return was, f.Error()
}

// The form's "Your name" is who commands in the terminal act as, and takes no
// other name: the operator is a user, and under a name nobody acts as the
// person who made the yard was no user of it, with no chat and no inbox
// (yardr-cj66). It says so in place, with how the other name is acted as,
// and takes that name once YARDR_ACTOR is set to it.
func TestYourNameIsWhoCommandsActAs(t *testing.T) {
	var name string
	wantText(t, nameField(&name).View(), "Your name (who your commands act as: your login, or $YARDR_ACTOR)")

	login := config.Actor()
	if !store.ValidUserName(login) || login == "chris" {
		t.Skipf("the login name %q is no user's, or is the other name of this test", login)
	}
	if _, err := typeName(t, "chris"); err == nil ||
		err.Error() != "commands in this terminal act as "+login+"; keep it, or set YARDR_ACTOR=chris before yardr" {
		t.Errorf("another name than the login name: %v", err)
	}
	if was, err := typeName(t, login); was != login || err != nil {
		t.Errorf("the login name: the form starts at %q, %v", was, err)
	}

	t.Setenv("YARDR_ACTOR", "chris")
	if was, err := typeName(t, "chris"); was != "chris" || err != nil {
		t.Errorf("YARDR_ACTOR=chris: the form starts at %q, %v", was, err)
	}
	if _, err := typeName(t, login); err == nil ||
		err.Error() != "commands in this terminal act as chris; keep it, or set YARDR_ACTOR="+login+" before yardr" {
		t.Errorf("YARDR_ACTOR=chris, the login name: %v", err)
	}
	// No name a user may have is none to set YARDR_ACTOR to.
	for _, bad := range []string{"", "chris scott", `CORP\chris`} {
		if _, err := typeName(t, bad); err == nil || strings.Contains(err.Error(), "YARDR_ACTOR") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// A login name no user may have cannot be kept.
	t.Setenv("YARDR_ACTOR", `CORP\chris`)
	if _, err := typeName(t, "chris"); err == nil ||
		err.Error() != `commands in this terminal act as CORP\chris, which no user can be called: set YARDR_ACTOR=chris before yardr` {
		t.Errorf("a login name that is no user name: %v", err)
	}
}

// The onboarding run as YARDR_ACTOR makes that name the operator, whatever
// the login name is, and its last question opens the chat: who runs it is a
// user of the yard it made.
func TestOnboardingAsYardrActor(t *testing.T) {
	newHome(t)
	stubServices(t, "darwin")
	stubWeb(t)
	chats := stubChat(t)
	t.Setenv("USER", "someone")
	t.Setenv("YARDR_ACTOR", "chris")
	st := storetest.New(t)
	out, _ := onboardTo(t, st, nil)
	ctx := context.Background()
	if all, _ := st.Settings(ctx); all["operator"] != "chris" {
		t.Fatalf("operator setting = %q\n%s", all["operator"], out)
	}
	if u, err := st.GetUser(ctx, "chris"); err != nil || u == nil {
		t.Fatalf("user chris: %v", err)
	}
	if len(*chats) != 1 || strings.Contains(out, "who is none") {
		t.Fatalf("chats %v\n%s", *chats, out)
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
	if b, _ := os.ReadFile(filepath.Join(config.At(home).PackRoles("starter"), "yardmaster.md")); !bytes.Contains(b, []byte("bring to operator")) {
		t.Fatalf("yardmaster.md does not name the operator:\n%s", b)
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
	r, _ := st.GetCrew(context.Background(), "yardmaster")
	if _, ok := r.Config["args"]; ok || r.Config["kind"] != "gemini" {
		t.Fatalf("yardmaster config = %v", r.Config)
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
	for _, s := range []string{"\n  " + yardr + " daemon\n", yardr + " crew set yardmaster 'args=[...]'",
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
		!strings.Contains(out, " yardr crew set yardmaster 'args=[...]'") {
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
	if err := old.PutDepot(ctx, store.Depot{Name: "r", Path: "/x", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if ok, err := (&app{st: old}).initialized(ctx, cfg); !ok || err != nil {
		t.Fatalf("store with a depot, without default_kind: %v %v", ok, err)
	}
	for name, fill := range map[string]func(store.API) error{
		"group": func(st store.API) error { return st.PutGroup(ctx, store.Group{Name: "g", Runner: "manual"}, "me") },
		"crew": func(st store.API) error {
			_, err := st.PutCrew(ctx, store.CrewMember{Name: "m", Runner: "herdr"}, "me")
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

// A yard from before the onboarding (one depot, no settings) keeps working
// without a terminal: its commands run and bare yardr is help. Only an
// empty store refuses (yard-w6np: an upgrade locked ~/.yardr out).
func TestOldYardIsNotOnboarded(t *testing.T) {
	newHome(t)
	setInteractive(t, false)
	ctx := context.Background()
	st := storetest.New(t)
	if err := st.PutDepot(ctx, store.Depot{Name: "r", Path: "/x", Base: "main"}, "me"); err != nil {
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

// The escape hatch is never behind the check it unlocks: settings works on a
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
	stubStart(t, st, yardmasterHandle())
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
	if _, err := st.GetCrew(ctx, "yardmaster"); err != nil {
		t.Fatalf("no yardmaster: %v", err)
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
	stubStart(t, st, yardmasterHandle())
	stubPane(t, answeredPane)
	stubFocus(t)
	ctx := context.Background()

	out, err := run(t, (&app{st: st}).rootCmd(), "crew", "list")
	if err != nil {
		t.Fatal(err)
	}
	if asked != 10 {
		t.Fatalf("%d form pages", asked)
	}
	r, err := st.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if r.Config["kind"] != "codex" || r.Dir != filepath.Dir(home) ||
		!reflect.DeepEqual(r.Config["args"], []any{"--dangerously-bypass-approvals-and-sandbox"}) {
		t.Fatalf("yardmaster = %+v", r)
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
	if rs, _ := st.Crew(context.Background()); len(rs) != 0 {
		t.Fatalf("crew members = %v", rs)
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

// group add and crew add without --set kind take the default_kind.
func TestDefaultKind(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	if _, err := run(t, a.settingsCmd(), "set", "default_kind", "nope"); err == nil {
		t.Fatal("unknown kind accepted")
	}
	// Unset, it reads as what herdr starts when a config names no kind.
	if out, err := run(t, a.settingsCmd(), "get", "default_kind"); err != nil || out != "claude\n" {
		t.Fatalf("unset get: %q, %v", out, err)
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
	if _, err := run(t, a.crewCmd(), "add", "r"); err != nil {
		t.Fatal(err)
	}
	if r, _ := st.GetCrew(ctx, "r"); r.Config["kind"] != "kimi" {
		t.Fatalf("crew member config = %v", r.Config)
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

// init --json on a new yard prints settings_kept as an empty list, as it
// prints written: a caller that reads the object into a type takes a list
// there, and null is none.
func TestInitJSONKeepsNoSettingAsAnEmptyList(t *testing.T) {
	newHome(t)
	a := &app{st: storetest.New(t), actor: "me"}
	out, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--operator", "Jo", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if string(got["settings_kept"]) != "[]" || string(got["written"]) == "null" {
		t.Fatalf("settings_kept = %s, written = %s; want lists", got["settings_kept"], got["written"])
	}
}
