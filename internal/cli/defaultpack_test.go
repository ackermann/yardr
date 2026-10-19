package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/packs"
)

// ownPack is a copy of the pack yardr embeds as from, in a directory of its
// own and under another name: an operator's pack.
func ownPack(t *testing.T, from, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	sub, err := fs.Sub(packs.FS, from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dir, sub); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "pack.yaml")
	writeFile(t, manifest, strings.Replace(string(mustRead(t, manifest)), "name: "+from+"\n", "name: "+name+"\n", 1))
	return dir
}

// depot init without --pack sets a depot up from the yard's default pack:
// starter for a git depot and dir for a directory depot while default_pack
// is unset, the setting's pack once it is set, and starter again when it is
// unset. Nothing of yardr's goes into a checkout, and nothing into a
// directory.
func TestDepotInitUsesTheDefaultPack(t *testing.T) {
	a, repo := packYard(t, nil)
	setInteractive(t, false)
	// The command's error stream, where depot init says the pack is the
	// setting's; every init run below shares the buffer.
	var errs bytes.Buffer
	runInit := func(args ...string) (string, error) {
		c := a.depotCmd()
		c.SetErr(&errs)
		return run(t, c, args...)
	}
	ctx := context.Background()
	before := snapshot(t, repo)
	out, err := runInit(append([]string{"init", "web", "--yes"}, starterFlags...)...)
	if err != nil || !strings.Contains(out, "\napplied pack starter to depot web: ") {
		t.Fatalf("depot init web: %v\n%s", err, out)
	}
	if after := snapshot(t, repo); !maps.Equal(before, after) {
		t.Errorf("the checkout changed: %v", slices.Sorted(maps.Keys(after)))
	}
	if errs.Len() != 0 {
		t.Errorf("with no setting, depot init says: %s", errs.String())
	}

	// A directory depot, with the setting unset: the dir pack.
	notes := t.TempDir()
	if err := a.st.PutDepot(ctx, store.Depot{Name: "notes", Path: notes, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = runInit(append([]string{"init", "notes", "--yes"}, dirFlags...)...)
	if err != nil || !strings.Contains(out, "\napplied pack dir to depot notes: ") {
		t.Fatalf("depot init notes: %v\n%s", err, out)
	}
	if files := snapshot(t, notes); len(files) != 0 {
		t.Errorf("depot init wrote into the directory: %v", slices.Sorted(maps.Keys(files)))
	}

	// The setting names another pack: a second git depot gets that one, and
	// the command says where the pack came from.
	own := ownPack(t, packs.Starter, "research")
	if out, err := run(t, a.settingsCmd(), "set", "default_pack", own); err != nil {
		t.Fatalf("settings set default_pack: %v\n%s", err, out)
	}
	second := func(name string) string {
		t.Helper()
		dir := t.TempDir()
		gitIn(t, dir, "init", "-q", "-b", "main")
		gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "a repository")
		if err := a.st.PutDepot(ctx, store.Depot{Name: name, Path: dir, Base: "main"}, "me"); err != nil {
			t.Fatal(err)
		}
		out, err := runInit(append([]string{"init", name, "--yes"}, starterFlags...)...)
		if err != nil {
			t.Fatalf("depot init %s: %v\n%s", name, err, out)
		}
		return out
	}
	out = second("api")
	if !strings.Contains(out, "\napplied pack research to depot api: ") {
		t.Fatalf("with default_pack set, depot init api did not apply it:\n%s", out)
	}
	if strings.Contains(out, "the yard's default_pack setting") {
		t.Errorf("depot init says the pack is the setting's on stdout:\n%s", out)
	}
	if got := errs.String(); got != "pack "+own+": the yard's default_pack setting\n" {
		t.Errorf("depot init does not say the pack is the setting's: %q", got)
	}
	if g, err := a.st.GetGroup(ctx, "api-builders"); err != nil || g.Config["prompt"] != filepath.Join(config.At(a.cfg.Home).PackRoles("research"), "builder.md") {
		t.Fatalf("api-builders = %+v, %v", g, err)
	}
	// --pack still names another, and the setting is then not read. (It
	// gives the one yardmaster the starter's role again, which the other
	// pack had taken: TestSecondPackShowsTheYardmastersPromptChange.)
	errs.Reset()
	if out, err := runInit("init", "web", "--pack", "starter", "--yes"); err != nil || !strings.Contains(out, "\napplied pack starter to depot web: ") || errs.Len() != 0 {
		t.Fatalf("--pack beside the setting: %v\n%s%s", err, out, errs.String())
	}

	// Unset again: starter.
	if _, err := run(t, a.settingsCmd(), "set", "default_pack", ""); err != nil {
		t.Fatal(err)
	}
	if out := second("site"); !strings.Contains(out, "\napplied pack starter to depot site: ") {
		t.Fatalf("with default_pack unset again, depot init site did not apply starter:\n%s", out)
	}
	if _, err := run(t, a.depotCmd(), "init", "nope"); exitCode(err) != 3 {
		t.Fatalf("unknown depot: %v", err)
	}
}

// default_pack takes what means the same pack wherever depot init is run: a
// name, a directory that is absolute and holds a pack, or a pack's URL.
func TestDefaultPackSetting(t *testing.T) {
	a := &app{st: storetest.New(t), actor: "me"}
	own := ownPack(t, packs.Dir, "ledgers")
	t.Chdir(filepath.Dir(own))
	for _, tc := range []struct{ value, want string }{
		{"./ledgers", "a directory is given as an absolute path"},
		{filepath.Join(own, "nothing"), "no pack there"},
		{"two words", "a pack's name, or its directory as an absolute path"},
	} {
		if _, err := run(t, a.settingsCmd(), "set", "default_pack", tc.value); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("default_pack %q: err = %v, want %q", tc.value, err, tc.want)
		}
	}
	for _, ok := range []string{"starter", "ledgers", own, "https://yardr.dev/packs/starter/", ""} {
		if _, err := run(t, a.settingsCmd(), "set", "default_pack", ok); err != nil {
			t.Errorf("default_pack %q: %v", ok, err)
		}
	}
	if out, err := run(t, a.settingsCmd(), "get"); err != nil || !strings.Contains(out, "default_pack") {
		t.Errorf("settings get does not list default_pack: %v\n%s", err, out)
	}
}

// A default pack that is not there, or is for the other kind of depot, is
// refused as --pack would be, after the line that says it is the setting's,
// and writes nothing.
func TestDepotInitDefaultPackThatDoesNotFit(t *testing.T) {
	a, repo := packYard(t, nil)
	setInteractive(t, false)
	// The command's error stream, where depot init says the pack is the
	// setting's.
	var errs bytes.Buffer
	ctx := context.Background()
	notes := t.TempDir()
	if err := a.st.PutDepot(ctx, store.Depot{Name: "notes", Path: notes, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	yard := yardOf(t, a)
	for _, tc := range []struct{ value, depot, want string }{
		{"nosuch", "web", `no pack named "nosuch"`},
		// The setting is the yard's, and one pack of yardr's is for one kind.
		{"starter", "notes", "depot notes is a directory depot, and pack starter is for a git depot"},
		{"dir", "web", "depot web is a git depot, and pack dir is for a directory depot"},
	} {
		if err := a.st.SetSetting(ctx, defaultPackSetting, tc.value, "me"); err != nil {
			t.Fatal(err)
		}
		errs.Reset()
		c := a.depotCmd()
		c.SetErr(&errs)
		out, err := run(t, c, "init", tc.depot, "--yes")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("default_pack %s on %s: err = %v, want %q\n%s", tc.value, tc.depot, err, tc.want, out)
		}
		if got := errs.String(); got != "pack "+tc.value+": the yard's default_pack setting\n" {
			t.Errorf("default_pack %s: stderr = %q", tc.value, got)
		}
		if strings.Contains(out, "the yard's default_pack setting") {
			t.Errorf("default_pack %s: stdout says the pack is the setting's:\n%s", tc.value, out)
		}
	}
	if err := a.st.SetSetting(ctx, defaultPackSetting, "", "me"); err != nil {
		t.Fatal(err)
	}
	if got := yardOf(t, a); got != yard {
		t.Errorf("a refused depot init changed the yard:\n%s", got)
	}
	for _, d := range []string{repo, notes} {
		if _, err := os.Stat(filepath.Join(d, ".yardr")); err == nil {
			t.Errorf("a refused depot init wrote into %s", d)
		}
	}
}

// A yard with two packs that each have a yardmaster (the design's question
// 7): setting a second depot up from the other pack would give the yard's
// one yardmaster that pack's role, and the plan says so, with both paths,
// before anything is written.
func TestSecondPackShowsTheYardmastersPromptChange(t *testing.T) {
	a, _ := packYard(t, nil)
	setInteractive(t, false)
	ctx := context.Background()
	if out, err := run(t, a.depotCmd(), append([]string{"init", "web", "--yes"}, starterFlags...)...); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	notes := t.TempDir()
	gitIn(t, notes, "init", "-q", "-b", "main")
	if err := a.st.PutDepot(ctx, store.Depot{Name: "notes", Path: notes, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	own := ownPack(t, packs.Starter, "research")
	yard := yardOf(t, a)
	was := filepath.Join(config.At(a.cfg.Home).PackRoles("starter"), "yardmaster.md")
	args := []string{"init", "notes", "--pack", own, "--crew", "yardmaster=boss", "--bind", "builders=claude", "--bind", "reviewers=codex"}
	out, err := run(t, a.depotCmd(), args...)
	if err == nil || !strings.Contains(err.Error(), "depot init changes the yard: give --yes") {
		t.Fatalf("without --yes: %v\n%s", err, out)
	}
	if want := "  change           boss\n                     prompt: " + was + " -> " + filepath.Join(config.At(a.cfg.Home).PackRoles("research"), "yardmaster.md") + "\n"; !strings.Contains(out, want) {
		t.Errorf("the plan lacks %q:\n%s", want, out)
	}
	if got := yardOf(t, a); got != yard {
		t.Errorf("the plan alone changed the yard:\n%s", got)
	}
	if r, err := a.st.GetCrew(ctx, "boss"); err != nil || r.Config["prompt"] != was {
		t.Fatalf("boss = %+v, %v", r, err)
	}
	// Said yes to, it is what the plan showed. The first depot's groups
	// still read the first pack.
	if out, err := run(t, a.depotCmd(), append(args, "--yes")...); err != nil {
		t.Fatalf("with --yes: %v\n%s", err, out)
	}
	if r, _ := a.st.GetCrew(ctx, "boss"); r.Config["prompt"] != filepath.Join(config.At(a.cfg.Home).PackRoles("research"), "yardmaster.md") {
		t.Errorf("boss's prompt after the import = %v", r.Config["prompt"])
	}
	if g, _ := a.st.GetGroup(ctx, "web-builders"); g.Config["prompt"] != filepath.Join(config.At(a.cfg.Home).PackRoles("starter"), "builder.md") {
		t.Errorf("web-builders' prompt after the second pack = %v", g.Config["prompt"])
	}
}

// yardr init takes the yardmaster's role from the yard's default pack: with
// default_pack set, that pack's, installed under its name. The first depot
// set up from the pack then finds the role as it would write it, and the
// yardmaster's prompt does not change.
func TestInitTakesTheYardmasterFromTheDefaultPack(t *testing.T) {
	home := newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	// Commands run through the root act as the login, so the yard's
	// operator is who the login would have to be. Naming it here keeps
	// the test hermetic.
	t.Setenv("YARDR_ACTOR", "Jo")
	a := &app{st: st, actor: "me"}
	own := ownPack(t, packs.Starter, "research")
	if err := st.SetSetting(ctx, defaultPackSetting, own, "me"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--operator", "Jo", "--yardmaster", "boss")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	prompt := filepath.Join(config.At(home).PackRoles("research"), "yardmaster.md")
	r, err := st.GetCrew(ctx, "boss")
	if err != nil || r.Config["prompt"] != prompt {
		t.Fatalf("boss = %+v, %v", r, err)
	}
	if b := mustRead(t, prompt); string(b) != string(packRole(t, "starter", "roles/yardmaster.md", "Jo", "boss")) {
		t.Errorf("the installed role does not name Jo and boss:\n%s", b)
	}
	if !strings.Contains(out, "(pack research, the yard's default)") {
		t.Errorf("the summary does not name the pack:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "roles")); err == nil {
		t.Error("init wrote <home>/roles")
	}

	// The first depot: the role is there as the import would write it.
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	if err := st.PutDepot(ctx, store.Depot{Name: "web", Path: repo, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	setInteractive(t, false)
	out, err = run(t, a.rootCmd(), "--json", "depot", "init", "web", "--yes", "--crew", "yardmaster=boss",
		"--bind", "builders=claude", "--bind", "reviewers=claude", "--members", "backlog=", "--members", "decisions=Jo")
	var got struct {
		Plan struct {
			Pack    string
			Objects []struct{ Kind, Name, Action string }
		}
	}
	if err != nil || json.Unmarshal([]byte(out), &got) != nil || got.Plan.Pack != "research" {
		t.Fatalf("depot init web: %v\n%s", err, out)
	}
	for _, o := range got.Plan.Objects {
		if (o.Kind == "file" && o.Name == "roles/yardmaster.md" || o.Kind == "crew" && o.Name == "boss") && o.Action != "unchanged" {
			t.Errorf("the import would %s %s %s, which init made", o.Action, o.Kind, o.Name)
		}
	}
	if again, _ := st.GetCrew(ctx, "boss"); again.Config["prompt"] != prompt {
		t.Errorf("the import changed the yardmaster's prompt to %v", again.Config["prompt"])
	}
}

// A default pack with no crew member called yardmaster gets the yard none:
// init makes the yard, says so, and installs nothing.
func TestInitWithADefaultPackWithoutAYardmaster(t *testing.T) {
	home := newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	if err := st.SetSetting(ctx, defaultPackSetting, ownPack(t, packs.Dir, "ledgers"), "me"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--operator", "Jo")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if rs, _ := st.Crew(ctx); len(rs) != 0 {
		t.Errorf("crew = %+v, want none", rs)
	}
	if !strings.Contains(out, "yardmaster none: the yard's default pack, ledgers, has no crew member called yardmaster") {
		t.Errorf("the summary does not say there is no yardmaster:\n%s", out)
	}
	if files := snapshot(t, home); len(files) != 0 {
		t.Errorf("init wrote into the home: %v", slices.Sorted(maps.Keys(files)))
	}
	if got, _ := st.Settings(ctx); got["default_kind"] != "claude" {
		t.Errorf("the yard is not marked set up: %v", got)
	}
	// --start has nobody to wait for: it says so and starts nothing.
	errs := captureStderr(t)
	a.stderr = errs
	out, err = run(t, a.rootCmd(), "init", "--start")
	if err != nil || !strings.Contains(errs.String(), "--start: nothing is started, since the yard has no yardmaster to wait for") {
		t.Errorf("init --start without a yardmaster: %v\n%s%s", err, out, errs)
	}
	if rs, _ := st.Crew(ctx); len(rs) != 0 || config.DaemonRunning(filepath.Join(home, "daemon.lock")) {
		t.Errorf("init --start without a yardmaster added one (%+v) or started a daemon", rs)
	}
	// A default pack that is gone is named with the setting, before
	// anything is written.
	st2 := storetest.New(t)
	if err := st2.SetSetting(ctx, defaultPackSetting, "nosuch", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, (&app{st: st2, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err == nil ||
		!strings.Contains(err.Error(), "the yard's default pack, nosuch (yardr settings get default_pack): no pack named") {
		t.Errorf("a default pack that is not there: %v", err)
	}
	if got, _ := st2.Settings(ctx); got["operator"] != "" || got["default_kind"] != "" {
		t.Errorf("a refused init stored settings: %v", got)
	}
}
