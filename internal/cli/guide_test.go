package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// former answers the setup's forms as keys would. A field is found by its
// key; one without an answer keeps its default, as enter does.
type former struct {
	t *testing.T
	// answers by field key: a string is typed into an input (replacing what
	// is there) or picked in a select, a bool answers a confirm.
	answers map[string]any
	// goes are the answers to "how to go on", one per time it is asked;
	// past its end the answer is stop.
	goes []string
	// panes are the answers to "open the yardmaster's pane?", one per time it
	// is asked; past its end the answer is not now.
	panes []string
	// tries are the answers to "<harness> did not answer", one per time it
	// is asked; past its end the answer is stop. Whether to ask the harness
	// at all ("try") is answered no unless answers says otherwise: the
	// tests of the question say yes.
	tries []string
	// abort, when it holds for a field, cancels the form there (ctrl+c).
	abort func(huh.Field) bool
	// asked are the keys of the fields asked, in order ("" for a field of
	// the group form, which has none).
	asked []string
}

func (f *former) ask(fields ...huh.Field) error {
	f.t.Helper()
	for _, fld := range fields {
		key := fld.GetKey()
		f.asked = append(f.asked, key)
		if f.abort != nil && f.abort(fld) {
			return huh.ErrUserAborted
		}
		var want any
		if key == "go" {
			want = goStop
			if len(f.goes) > 0 {
				want, f.goes = f.goes[0], f.goes[1:]
			}
		} else if key == "pane" {
			want = paneLater
			if len(f.panes) > 0 {
				want, f.panes = f.panes[0], f.panes[1:]
			}
		} else if key == "tried" {
			want = tryStop
			if len(f.tries) > 0 {
				want, f.tries = f.tries[0], f.tries[1:]
			}
		} else if _, ok := f.answers[key]; key == "try" && !ok {
			want = false
		} else if w, ok := f.answers[key]; ok && key != "" {
			want = w
		} else {
			continue
		}
		fld.WithKeyMap(huh.NewDefaultKeyMap())
		fld.Focus()
		switch x := fld.(type) {
		case *huh.Input:
			x.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
			x.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(want.(string))})
		case *huh.Text:
			x.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(want.(string))})
		case *huh.Confirm:
			r := 'n'
			if want.(bool) {
				r = 'y'
			}
			x.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		case *huh.Select[string]:
			for i := 0; i < 10 && x.GetValue() != want; i++ {
				x.Update(tea.KeyMsg{Type: tea.KeyDown})
			}
		case *huh.MultiSelect[string]:
			// From the first option down: each is toggled, and toggled
			// back when that left it other than wanted.
			x.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
			toggle := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}
			for i := 0; i < 10; i++ {
				before := slices.Clone(x.GetValue().([]string))
				x.Update(toggle)
				for _, v := range append(before, x.GetValue().([]string)...) {
					now := slices.Contains(x.GetValue().([]string), v)
					if now != slices.Contains(before, v) && now != slices.Contains(want.([]string), v) {
						x.Update(toggle)
					}
				}
				x.Update(tea.KeyMsg{Type: tea.KeyDown})
			}
		}
		if got := fld.GetValue(); !reflect.DeepEqual(got, want) {
			f.t.Fatalf("field %q: answered %v, it holds %v", key, want, got)
		}
	}
	return nil
}

// setupTemplateDir is the repository setupYard gives every test, made once
// by TestMain: one commit with a README on main. Making it per test spent
// three git runs each; a test gets a copy, since a setup reads from it and
// a test may stage or tag on top. Made before the tests run, read only
// after, so no test assigns it.
var setupTemplateDir string

// buildSetupTemplate makes the repository setupYard copies per test.
func buildSetupTemplate() (string, error) {
	var err error
	dir, merr := os.MkdirTemp("", "ysetup")
	if merr != nil {
		return "", merr
	}
	run := func(args ...string) {
		if err != nil {
			return
		}
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		if out, cerr := cmd.CombinedOutput(); cerr != nil {
			err = fmt.Errorf("git %v: %v %s", args, cerr, out)
		}
	}
	run("init", "-q", "-b", "main")
	if werr := os.WriteFile(filepath.Join(dir, "README"), []byte("hello\n"), 0o644); err == nil && werr != nil {
		err = werr
	}
	run("add", "-A")
	run("commit", "-q", "-m", "first")
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// setupRepo is a fresh copy of the setup repository: one commit with a
// README on main, in a directory whose name needs quoting.
func setupRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(shortDir(t, "repo"), "Web App")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(repo, os.DirFS(setupTemplateDir)); err != nil {
		t.Fatal(err)
	}
	return repo
}

// setupYard is an initialized yard in a terminal whose forms f answers,
// with a repository that has one commit on main. It returns the home, the
// store and the repository.
func setupYard(t *testing.T, f *former) (home string, st store.API, repo string) {
	t.Helper()
	home = newHomeDir(t)
	st = storetest.New(t)
	// Commands run through the root resolve no struct actor: they act as
	// the login. The setup runs as the operator Jo, as a setup in the
	// operator's terminal does: its import writes the pack's groups,
	// which are the operator's to write. Naming it here keeps the setup
	// hermetic.
	testYard(t, st, home, "YARDR_ACTOR=Jo")
	a := appOn(st)
	a.actor = "me"
	if _, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--operator", "Jo"); err != nil {
		t.Fatal(err)
	}
	repo = setupRepo(t)
	f.t = t
	if f.answers == nil {
		f.answers = map[string]any{}
	}
	if _, ok := f.answers["path"]; !ok {
		f.answers["path"] = repo
	}
	setInteractive(t, true)
	stubAsk(t, f.ask)
	return home, st, repo
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	// The commit a test makes needs an author, and no signing: named on
	// the command's own environment, since a run's map does not reach
	// what the test itself starts.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setup runs yardr setup on st and returns what it printed.
func setup(t *testing.T, st store.API, args ...string) string {
	t.Helper()
	a := appOn(st)
	a.actor = "me"
	out, err := run(t, a.rootCmd(), append([]string{"setup"}, args...)...)
	if err != nil {
		t.Fatalf("yardr setup: %v\n%s", err, out)
	}
	return out
}

// todo is the key of the first setup step not done, "" when all are.
func todo(t *testing.T, st store.API) string {
	t.Helper()
	steps, err := setupSteps(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if n := nextStep(steps); n != nil {
		return n.Key
	}
	return ""
}

// snapshot is everything the setup writes to the store, to compare.
func yardSnapshot(t *testing.T, st store.API) string {
	t.Helper()
	ctx := context.Background()
	depots, _ := st.Depots(ctx)
	groups, _ := st.Groups(ctx)
	routes, _ := st.Routes(ctx)
	beads, _ := st.ListBeads(ctx, store.BeadFilter{All: true})
	head, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal([]any{depots, groups, routes, beads, head})
	return string(b)
}

// The forms alone, every page accepted, take an initialized yard to a first
// bead in stage new: each page shows its command line and runs that
// command, and asks how to go on after every page. One page sets the depot
// up from the pack: the flow, the groups and the routes are its import's.
func TestSetupForms(t *testing.T) {
	f := &former{answers: map[string]any{"title": "Say hello", "goal": "HELLO.md says hello. Done when it is committed."},
		goes: []string{goHere, goHere, goHere}}
	home, st, repo := setupYard(t, f)
	ctx := context.Background()
	// Something staged in the checkout stays as it is: the setup commits nothing.
	if err := os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "wip.txt")
	out := setup(t, st)

	yardr := "yardr --home " + home
	quoted := shellQuote(repo)
	lines := []string{
		"  todo   a depot: no repository is a depot of this yard yet",
		"Setup: a depot",
		"$ " + yardr + " depot add web-app " + quoted + " --base main",
		"Setup: the depot's .yardr files on its base",
		"This sets depot web-app up from the pack starter",
		"$ " + yardr + " depot init web-app",
		"pack starter (",
		"  create           web-app-builders\n",
		"applied pack starter to depot web-app: ",
		"test: none set. Nothing in " + repo + " says how depot web-app is tested",
		"Setup: a first bead",
		"$ " + yardr + " bead create 'Say hello' --depot web-app -b 'HELLO.md says hello. Done when it is committed.'",
		"--to new",
		"The forms end here. The first bead, ",
		yardr + " session list",
		yardr + " web serve",
	}
	rest := out
	for _, l := range lines {
		i := strings.Index(rest, l)
		if i < 0 {
			t.Fatalf("output lacks, in this order, %q:\n%s", l, out)
		}
		rest = rest[i+len(l):]
	}
	// One page did what five did: no page of its own for a group or a route.
	if n := strings.Count(out, "\nSetup: "); n != 3 {
		t.Errorf("%d pages, want 3:\n%s", n, out)
	}

	depot, err := st.GetDepot(ctx, "web-app")
	if err != nil || resolve(depot.Path) != resolve(repo) || depot.Base != "main" || depot.IsDir() {
		t.Fatalf("depot = %+v, %v", depot, err)
	}
	// Nothing is committed and nothing of yardr's is in the checkout: what
	// was staged is staged still.
	if got := gitIn(t, repo, "log", "--format=%s"); got != "first" {
		t.Errorf("committed: %q", got)
	}
	if got := gitIn(t, repo, "status", "--porcelain"); got != "A  wip.txt" {
		t.Errorf("status after the setup: %q", got)
	}
	if slices.Contains(f.asked, "commit") {
		t.Errorf("asked to commit: %v", f.asked)
	}
	// The groups are the pack's, with its roles and its script under the home.
	roles := config.At(home).PackRoles("starter")
	for name, prompt := range map[string]string{"web-app-builders": "builder.md", "web-app-reviewers": "reviewer.md"} {
		g, err := st.GetGroup(ctx, name)
		if err != nil || g.Runner != "herdr" || g.Config["kind"] != "claude" || g.Config["prompt"] != filepath.Join(roles, prompt) {
			t.Errorf("group %s = %+v, %v", name, g, err)
		}
	}
	script := filepath.Join(config.At(home).Pack("starter"), "scripts", "merge-local")
	if g, err := st.GetGroup(ctx, "web-app-assembly"); err != nil || g.Runner != "exec" || !strings.Contains(fmt.Sprint(g.Config["cmd"]), script) {
		t.Errorf("assembly = %+v, %v", g, err)
	}
	if info, err := os.Stat(script); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("merge-local mode: %v %v", info, err)
	}
	routes, _ := st.Routes(ctx)
	var got []string
	for _, r := range routes {
		got = append(got, r.Stage+" "+deref(r.Depot)+" "+r.Group)
	}
	slices.Sort(got)
	for _, w := range []string{"approved web-app web-app-assembly", "new web-app web-app-builders", "review web-app web-app-reviewers"} {
		if !slices.Contains(got, w) {
			t.Errorf("routes = %v, without %q", got, w)
		}
	}
	if ptrs, err := st.FlowPointers(ctx); err != nil || len(ptrs) != 2 || ptrs[0].Depot != "web-app" || ptrs[0].Flow != "starter.default" || ptrs[1].Depot != "web-app" || ptrs[1].Type != store.TypeTrain || ptrs[1].Flow != "starter.train" {
		t.Errorf("flow pointers = %+v, %v", ptrs, err)
	}
	beads, _ := st.ListBeads(ctx, store.BeadFilter{Depot: "web-app"})
	if len(beads) != 1 || beads[0].Title != "Say hello" || beads[0].Stage != "new" || !strings.Contains(beads[0].Body, "HELLO.md") {
		t.Fatalf("beads = %+v", beads)
	}
	if next := todo(t, st); next != stepMerged {
		t.Errorf("next step = %q, want the merged bead", next)
	}
	// How to go on was asked after every page, including the first bead;
	// keeping going from that page prints how to watch it.
	if n := len(slices.DeleteFunc(slices.Clone(f.asked), func(k string) bool { return k != "go" })); n != 3 || len(f.goes) != 0 {
		t.Errorf("asked how to go on %d times, want 3: %v", n, f.asked)
	}

	// Run again, there is no form page left: it says where the bead is,
	// and offers the operator its pages (to be told, and the link).
	f.asked = nil
	if out := setup(t, st); !slices.Equal(f.asked, []string{"landed", "link"}) || !strings.Contains(out, "The forms end here") || !strings.Contains(out, "  done   a first bead: ") {
		t.Errorf("asked %v on a yard with every page done:\n%s", f.asked, out)
	}
}

// The setup goes on where the yard stands: stopped after every page and run
// again, each run takes exactly the next step's page and none before it.
func TestSetupGoesOnWhereTheYardStands(t *testing.T) {
	f := &former{answers: map[string]any{"name": "web", "title": "Say hello", "goal": "HELLO.md says hello."}}
	home, st, _ := setupYard(t, f)
	for _, tc := range []struct {
		step  string
		asked []string // the page's keyed fields, then how to go on
		next  string
	}{
		{stepDepot, []string{"path", "name", "base", "go"}, stepDepotFiles},
		{stepDepotFiles, []string{"go"}, stepFirstBead},
		{stepFirstBead, []string{"title", "goal", "go", "landed", "link"}, stepMerged},
	} {
		if got := todo(t, st); got != tc.step {
			t.Fatalf("next step = %q, want %q", got, tc.step)
		}
		f.asked = nil
		if tc.step == stepFirstBead {
			f.goes = []string{goHere} // after the last form page, show how to watch the bead
		}
		out := setup(t, st)
		keyed := slices.DeleteFunc(slices.Clone(f.asked), func(k string) bool { return k == "" })
		if !slices.Equal(keyed, tc.asked) {
			t.Fatalf("%s: asked %v, want %v\n%s", tc.step, keyed, tc.asked, out)
		}
		if n := strings.Count(out, "\nSetup: "); n != 1 {
			t.Fatalf("%s: %d pages in one run:\n%s", tc.step, n, out)
		}
		if tc.next != stepMerged && !strings.Contains(out, "`yardr --home "+home+" setup` goes on from here") {
			t.Errorf("%s: not told how to go on:\n%s", tc.step, out)
		}
		if got := todo(t, st); got != tc.next {
			t.Fatalf("after %s the next step = %q, want %q\n%s", tc.step, got, tc.next, out)
		}
	}
}

// A depot someone started by hand (depot init without a pack, committed; a
// builders' group, not routed) gets the same page for what is left: a group
// counts once it is routed, and the import's plan names the depot's own
// role as shadowed. Nothing in its checkout says how it is tested, so it
// gets no test command.
func TestSetupAfterAStartByHand(t *testing.T) {
	f := &former{}
	home, st, _ := setupYard(t, f)
	ctx := context.Background()
	depot := committedDepot(t, st)
	if err := st.PutGroup(ctx, store.Group{Name: "web/builders", Runner: "herdr", Config: map[string]any{"prompt": "roles/builder.md"}}, "Jo"); err != nil {
		t.Fatal(err)
	}
	if got := todo(t, st); got != stepBuilders {
		t.Fatalf("next step = %q", got)
	}
	out := setup(t, st)
	wantText(t, out, "  done   the depot's .yardr files on its base: web has its flow on its base main",
		"  todo   a group that builds: no group is routed for stage new",
		"Setup: a group that builds", "$ yardr --home "+home+" depot init web",
		"shadows .yardr/roles/builder.md", "test: none set. Nothing in "+depot.Path+" says how depot web is tested")
	if got := todo(t, st); got != stepFirstBead {
		t.Fatalf("next step = %q\n%s", got, out)
	}
	if got := gitIn(t, depot.Path, "status", "--porcelain"); got != "" {
		t.Errorf("the checkout changed:\n%s", got)
	}
}

// A directory that is no checkout becomes a dir depot, and its page sets it
// up from the pack for one: no test command, no commit, nothing to land.
func TestSetupDirDepot(t *testing.T) {
	dir := filepath.Join(shortDir(t, "docs"), "notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &former{answers: map[string]any{"path": dir}, goes: []string{goHere}}
	home, st, _ := setupYard(t, f)
	out := setup(t, st)
	wantText(t, out, "$ yardr --home "+home+" depot init notes", "applied pack dir to depot notes: ", "test: none set. Depot notes is a directory depot")
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("the directory holds %v, %v", entries, err)
	}
	if got := todo(t, st); got != stepFirstBead {
		t.Fatalf("next step = %q\n%s", got, out)
	}
	if _, err := st.GetGroup(context.Background(), "notes-assembly"); err == nil {
		t.Error("a dir depot got an assembly")
	}
}

// Cancelling on any page leaves the yard as the finished pages made it: no
// command of the cancelled page has run. Leaving the import at its last
// question has written nothing, and set no test command.
func TestSetupCancelled(t *testing.T) {
	f := &former{answers: map[string]any{"name": "web", "title": "Say hello", "goal": "HELLO.md says hello."}}
	home, st, repo := setupYard(t, f)
	key := func(k string) func(huh.Field) bool {
		return func(fld huh.Field) bool { return fld.GetKey() == k }
	}
	for _, tc := range []struct {
		step  string
		abort func(huh.Field) bool // at the page's last question
		says  string
	}{
		{stepDepot, key("base"), "Cancelled at: a depot."},
		// The import's own confirmation, which is the command's and has no key.
		{stepDepotFiles, func(fld huh.Field) bool { _, ok := fld.(*huh.Confirm); return ok },
			"Still to do: the depot's .yardr files on its base: depot web follows no flow of a pack, and has none under .yardr/flows/ on its base main.\n  yardr --home " + home + " depot init web"},
		{stepFirstBead, key("goal"), "Cancelled at: a first bead."},
	} {
		if got := todo(t, st); got != tc.step {
			t.Fatalf("next step = %q, want %q", got, tc.step)
		}
		before := yardSnapshot(t, st)
		f.abort = tc.abort
		out := setup(t, st)
		wantText(t, out, tc.says)
		if after := yardSnapshot(t, st); after != before {
			t.Errorf("%s cancelled changed the yard:\n%s\n%s", tc.step, before, after)
		}
		if got := gitIn(t, repo, "status", "--porcelain"); got != "" {
			t.Errorf("%s cancelled left in the checkout: %q", tc.step, got)
		}
		if tc.step == stepDepotFiles {
			wantText(t, out, "cancelled; nothing was changed")
			// The yardmaster's role is init's, there before the import.
			var files []string
			for p := range snapshot(t, config.At(home).Packs()) {
				files = append(files, strings.TrimPrefix(p, home+"/"))
			}
			if !slices.Equal(files, []string{"packs/starter/roles/yardmaster.md"}) {
				t.Errorf("a cancelled import installed the pack's files: %v", files)
			}
		}
		// The page again, finished, then stop.
		f.abort = nil
		setup(t, st)
		if got := todo(t, st); got == tc.step {
			t.Fatalf("%s is still to do after its page", tc.step)
		}
	}
}

// A base that is no local branch is named before the import as one the
// pack's assembly cannot land on.
func TestSetupBaseThatIsNoLocalBranch(t *testing.T) {
	f := &former{answers: map[string]any{"name": "web"}, goes: []string{goHere}}
	_, st, repo := setupYard(t, f)
	// A tag resolves as a base and is no branch an assembly could land on.
	gitIn(t, repo, "tag", "v1")
	f.answers["base"] = "v1"
	out := setup(t, st)
	wantText(t, out, "The base of web, v1, is not a local branch: the pack's assembly lands on local branches only")
	if got := gitIn(t, repo, "log", "--format=%s"); got != "first" {
		t.Fatalf("committed: %s", got)
	}
	if strings.Index(out, "is not a local branch") > strings.Index(out, "depot init web") {
		t.Errorf("the base is named after the import ran:\n%s", out)
	}
}

// "Talk to the yardmaster" after a page opens the chat on the yardmaster
// (yardr-8j3n; it printed where the yardmaster's pane is before, and with
// --no-chat it still does). With no yardmaster running it opens no empty
// chat: it says what yardr doctor says of the yardmaster, and how to start
// the yard.
func TestSetupTalkToTheYardmaster(t *testing.T) {
	f := &former{answers: map[string]any{"name": "app", "title": "Say hello", "goal": "HELLO.md says hello."}, goes: []string{goYardmaster}}
	home, st, _ := setupYard(t, f)
	mustAs(t, st, "Jo", "user", "add", "me")
	// The setup runs as me, who is its operator here: the import
	// subscribes the yard it makes.
	if err := st.SetSetting(context.Background(), store.OperatorSetting, "me", "Jo"); err != nil {
		t.Fatal(err)
	}
	focused := stubFocus(t)
	chats := stubChat(t)
	// In herdr twice over: the run's map for the commands that read the
	// run, and the process for openYardmaster, which reads it there.
	yardEnv(t, st)["HERDR_ENV"] = "1"
	t.Setenv("HERDR_ENV", "1")
	out := setup(t, st, "--actor", "me")
	wantText(t, out, "yardmaster is not running, so there is nobody to talk to yet.", "`yardr --home "+home+" doctor` says of it:",
		"  FAULT  yardmaster  yardmaster is not running: the daemon starts it", "yardr --home "+home+" init --start")
	if len(*focused) != 0 || len(*chats) != 0 || todo(t, st) != stepDepotFiles {
		t.Fatalf("focused %v, chats %v, next %s\n%s", *focused, *chats, todo(t, st), out)
	}

	if err := st.CrewStarted(context.Background(), "yardmaster", yardmasterHandle()); err != nil {
		t.Fatal(err)
	}
	// A live pane at a login prompt has not answered the onboarding prompt:
	// say what doctor knows and do not open an empty chat.
	stubPane(t, "Not logged in · Run /login")
	f.goes = []string{goYardmaster}
	out = setup(t, st, "--actor", "me")
	wantText(t, out, "has not answered its first prompt", "`yardr --home "+home+" doctor` says of it:",
		"  ok     yardmaster  yardmaster is running (herdr agent y-yardmaster)",
		"  note   yardmaster  yardmaster's session runs, and it has not answered its first prompt")
	if len(*chats) != 0 || len(*focused) != 0 {
		t.Fatalf("a chat or focus was opened for a silent yardmaster: chats %v, focused %v\n%s", *chats, *focused, out)
	}

	// This session has answered its first prompt; it is ready for a chat.
	stubPane(t, answeredPane)
	f.goes = []string{goYardmaster}
	out = setup(t, st, "--actor", "me")
	wantText(t, out, "You left the chat; yardmaster keeps running. To come back: yardr --home "+home+" chat\n")
	if strings.Contains(out, "yardmaster is running, in herdr:") {
		t.Errorf("the chat was opened, and the pane is named all the same:\n%s", out)
	}
	if len(*focused) != 0 || !slices.Equal(*chats, []string{"yardmaster, expecting yardmaster"}) || todo(t, st) != stepMerged {
		t.Fatalf("focused %v, chats %v, next %s\n%s", *focused, *chats, todo(t, st), out)
	}

	// --no-chat: the pane, as before there was a chat. A second depot gives
	// the forms a page to ask after.
	gitDepot(t, st)
	f.goes = []string{goYardmaster}
	out = setup(t, st, "--actor", "me", "--no-chat")
	wantText(t, out, "yardmaster is running, in herdr:", "agent      y-yardmaster", "herdr agent attach y-yardmaster", "herdr shows yardmaster now.",
		"yardmaster goes on from the step that is next: it reads `yardr --home "+home+" doctor`")
	if !slices.Equal(*focused, []string{"y-yardmaster"}) || len(*chats) != 1 {
		t.Fatalf("focused %v, chats %v\n%s", *focused, *chats, out)
	}
}

// Where no form can run (no terminal, --json, an agent's own pane) setup
// prints the steps and the command for the next one, and asks nothing.
func TestSetupWithoutForms(t *testing.T) {
	f := &former{}
	home, st, _ := setupYard(t, f)
	check := func(out string) {
		t.Helper()
		wantText(t, out, "  todo   a depot: ", "Next step: a depot.", hereAt(home, "yardr depot add <depot> <path> --base <branch>"))
		if len(f.asked) != 0 || todo(t, st) != stepDepot {
			t.Fatalf("asked %v", f.asked)
		}
	}
	setInteractive(t, false)
	check(setup(t, st))
	setInteractive(t, true)
	env := yardEnv(t, st)
	env["YARDR_CREW"] = "yardmaster"
	check(setup(t, st))
	env["YARDR_CREW"] = ""

	var rep setupReport
	if err := json.Unmarshal([]byte(setup(t, st, "--json")), &rep); err != nil {
		t.Fatal(err)
	}
	if len(f.asked) != 0 || len(rep.Steps) != 8 || rep.Next == nil || rep.Next.Key != stepDepot {
		t.Fatalf("asked %v, json %+v", f.asked, rep)
	}
}

// Once the first bead has merged, yardr setup lists herdr's server at
// login as optional; before that it lists nothing of it, and never a todo
// that would hold the setup open.
func TestSetupListsHerdrLoginWhenComplete(t *testing.T) {
	t.Parallel()
	home := newHomeDir(t)
	requireNonInteractive(t)
	st := storetest.New(t)
	testYard(t, st, home)
	ctx := context.Background()
	a := appOn(st)
	a.actor = "me"
	if _, err := run(t, a.rootCmd(), "init", "--harness", "claude", "--operator", "me"); err != nil {
		t.Fatal(err)
	}
	if out := setup(t, st); strings.Contains(out, "Optional") || strings.Contains(out, "herdr's server") {
		t.Fatalf("listed on a fresh yard:\n%s", out)
	}
	depot := committedDepot(t, st)
	group := func(name, runner string, cfg map[string]any) {
		t.Helper()
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: runner, Config: cfg}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	route := func(stage, group string) {
		t.Helper()
		if _, err := st.AddRoute(ctx, store.Route{Stage: stage, Depot: &depot.Name, Group: group}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	group("web/builders", "herdr", map[string]any{"kind": "claude", "prompt": "roles/builder.md"})
	route("new", "web/builders")
	group("web/reviewers", "herdr", map[string]any{"kind": "claude", "prompt": "roles/reviewer.md"})
	route("review", "web/reviewers")
	group("web/mergers", "manual", nil)
	route("approved", "web/mergers")
	b, err := st.CreateBead(ctx, store.NewBead{Title: "first", Depot: depot.Name}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// The first bead is on its way, not merged: still nothing.
	if got := todo(t, st); got != stepMerged {
		t.Fatalf("next step = %q", got)
	}
	if out := setup(t, st); strings.Contains(out, "Optional") || strings.Contains(out, "herdr's server") {
		t.Fatalf("listed before the first bead merged:\n%s", out)
	}
	for _, stage := range []string{"new", "review", "approved", "merged"} {
		if _, err := st.Advance(ctx, b.ID, stage, store.Expect{}, "me"); err != nil {
			t.Fatalf("to %s: %v", stage, err)
		}
	}
	if got := todo(t, st); got != "" {
		t.Fatalf("next step = %q", got)
	}
	out := setup(t, st)
	wantText(t, out, "\nOptional\n", herdrLoginPart(&config.Config{Home: home}).line())
	for _, not := range []string{"todo ", "Next step"} {
		if strings.Contains(out, not) {
			t.Errorf("the setup is held open by %q:\n%s", not, out)
		}
	}
	var rep setupReport
	if err := json.Unmarshal([]byte(setup(t, st, "--json")), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Next != nil {
		t.Errorf("json next = %+v", rep.Next)
	}
}

// The onboarding ends in the same forms: once the yardmaster is up it asks
// whether to talk to the yardmaster or keep going here, and "here" is the first
// page of the setup. Run for yardr setup itself, the command asks no second
// time.
func TestOnboardingKeepsGoing(t *testing.T) {
	home := newHomeDir(t)
	setInteractive(t, true)
	stubLookPath(t, "claude", "git", "herdr")
	st := storetest.New(t)
	stubStart(t, st, yardmasterHandle())
	stubPane(t, answeredPane)
	focused := stubFocus(t)
	f := &former{t: t, goes: []string{goHere}, abort: func(fld huh.Field) bool { return fld.GetKey() == "path" }}
	stubAsk(t, f.ask)

	out, err := run(t, testApp(t, st, home).rootCmd(), "setup")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "yard created", "yardmaster is running, in herdr", "Setup: a depot",
		"Cancelled at: a depot.", "`yardr --home "+home+" setup` goes on from here")
	if len(*focused) != 0 {
		t.Errorf("focused %v for an answer of here", *focused)
	}
	if n := strings.Count(out, "Setup: a depot"); n != 1 {
		t.Errorf("the depot's page ran %d times:\n%s", n, out)
	}
	if n := len(slices.DeleteFunc(slices.Clone(f.asked), func(k string) bool { return k != "go" })); n != 1 {
		t.Errorf("asked how to go on %d times: %v", n, f.asked)
	}
}

func TestDepotNameFor(t *testing.T) {
	t.Parallel()
	for dir, want := range map[string]string{"/a/Web App": "web-app", "/a/yardr": "yardr", "/a/_x.Y_": "x.y", "/a/über": "ber"} {
		if got := depotNameFor(dir); got != want {
			t.Errorf("depotNameFor(%q) = %q, want %q", dir, got, want)
		}
	}
}

// A group does a stage's work once a route sends the stage to it, whatever
// its prompt: the steps name no role. Every step the import does names the
// one command, with the pack for the depot's kind.
func TestStepsGoByRoutes(t *testing.T) {
	t.Parallel()
	s := setupState{depots: []store.Depot{{Name: "docs", Kind: store.DepotDir}, {Name: "web", Kind: store.DepotGit}},
		pointed: map[string]string{"docs": "dir.default", "web": "starter.default"}}
	byKey := func() map[string]setupStep {
		m := map[string]setupStep{}
		for _, step := range s.steps() {
			m[step.Key] = step
		}
		return m
	}
	steps := byKey()
	for key, next := range map[string]string{stepBuilders: "yardr depot init docs", stepReviewers: "yardr depot init docs",
		stepLanding: "yardr depot init web", stepRoutes: "yardr depot init docs"} {
		if step := steps[key]; step.Done || step.Next != next {
			t.Errorf("%s = %+v, want todo with %q", key, step, next)
		}
	}
	if step := steps[stepLanding]; step.Depot != "web" || step.Detail != "no group is routed for stage approved" {
		t.Errorf("landing = %+v", step)
	}
	s.routes = []store.Route{{Stage: "new", Group: "anyone"}, {Stage: "new", Group: "anyone"}, {Stage: "approved", Group: "people"}}
	steps = byKey()
	if step := steps[stepBuilders]; !step.Done || step.Detail != "anyone" {
		t.Errorf("builders = %+v", step)
	}
	if step := steps[stepLanding]; !step.Done || step.Detail != "people" {
		t.Errorf("landing = %+v", step)
	}
	if step := steps[stepRoutes]; step.Done || step.Detail != "no route for review" {
		t.Errorf("routes = %+v", step)
	}
	if none := noSetup(); none[1].Next != "yardr depot init <depot>" || none[2].Next != none[1].Next {
		t.Errorf("no yard: %+v", none)
	}
}
