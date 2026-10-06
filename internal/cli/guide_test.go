package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/contrib/refinery"
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

// setupYard is an initialized yard in a terminal whose forms f answers,
// with a repository that has one commit on main. It returns the home, the
// store and the repository.
func setupYard(t *testing.T, f *former) (home string, st store.API, repo string) {
	t.Helper()
	home = newHome(t)
	st = storetest.New(t)
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude", "--operator", "Jo"); err != nil {
		t.Fatal(err)
	}
	// The commit the form makes needs an author, and no signing.
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull} {
		t.Setenv(k, v)
	}
	repo = filepath.Join(shortDir(t, "repo"), "Web App")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", "first")
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
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setup runs yardr setup on st and returns what it printed.
func setup(t *testing.T, st store.API, args ...string) string {
	t.Helper()
	out, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), append([]string{"setup"}, args...)...)
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
// command, and asks how to go on after every page.
func TestSetupForms(t *testing.T) {
	f := &former{answers: map[string]any{"title": "Say hello", "goal": "HELLO.md says hello. Done when it is committed."},
		goes: []string{goHere, goHere, goHere, goHere, goHere, goHere, goHere}}
	home, st, repo := setupYard(t, f)
	ctx := context.Background()
	out := setup(t, st)

	yardr := "yardr --home " + home
	script := filepath.Join(home, "scripts", "merge-local")
	quoted := shellQuote(repo)
	lines := []string{
		"  todo   a depot: no repository is a depot of this yard yet",
		"Setup: a depot",
		"$ " + yardr + " depot add web-app " + quoted + " --base main",
		"Setup: the depot's .yardr files on its base",
		"$ " + yardr + " depot init web-app",
		"The gate passes everything until its TODO is filled in.",
		"$ git -C " + quoted + " add .yardr && git -C " + quoted + " commit -m 'yardr: starter roles, gate, flow and stage files' -- .yardr",
		"Setup: a group that builds",
		"$ " + yardr + " group add web-app/builders --runner herdr --limit 2 --set 'args=[\"--permission-mode\",\"auto\"]' --set kind=claude --set prompt=roles/builder.md",
		"Setup: a group that reviews",
		"$ " + yardr + " group add web-app/reviewers --runner herdr --set 'args=[\"--permission-mode\",\"auto\"]' --set kind=claude --set prompt=roles/reviewer.md",
		"Setup: a group or person that lands approved beads",
		"merge-local installed: " + script,
		"$ " + yardr + " group add web-app/refinery --runner exec --set cmd=" + script + " --set log=" + filepath.Join(home, "logs", "refinery.log"),
		"Setup: routes for new, review and approved",
		"$ " + yardr + " route add --stage new --depot web-app --group web-app/builders",
		"$ " + yardr + " route add --stage review --depot web-app --group web-app/reviewers",
		"$ " + yardr + " route add --stage approved --depot web-app --group web-app/refinery",
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

	depot, err := st.GetDepot(ctx, "web-app")
	if err != nil || resolve(depot.Path) != resolve(repo) || depot.Base != "main" || depot.IsDir() {
		t.Fatalf("depot = %+v, %v", depot, err)
	}
	// Committed on the base, and nothing else with it.
	if got := gitIn(t, repo, "log", "-1", "--format=%s"); got != starterCommit {
		t.Errorf("last commit = %q", got)
	}
	if got := gitIn(t, repo, "status", "--porcelain"); got != "" {
		t.Errorf("the checkout is not clean:\n%s", got)
	}
	want := map[string]store.Group{
		"web-app/builders": {Runner: "herdr", Limit: 2, Config: map[string]any{"kind": "claude", "prompt": "roles/builder.md",
			"args": []any{"--permission-mode", "auto"}}},
		"web-app/reviewers": {Runner: "herdr", Limit: 1, Config: map[string]any{"kind": "claude", "prompt": "roles/reviewer.md",
			"args": []any{"--permission-mode", "auto"}}},
		"web-app/refinery": {Runner: "exec", Limit: 1, Config: map[string]any{"cmd": script,
			"log": filepath.Join(home, "logs", "refinery.log")}},
	}
	for name, w := range want {
		g, err := st.GetGroup(ctx, name)
		if err != nil || g.Runner != w.Runner || g.Limit != w.Limit || !reflect.DeepEqual(g.Config, w.Config) {
			t.Errorf("group %s = %+v, %v", name, g, err)
		}
	}
	// The refinery's script is this build's, executable, and its log has a
	// directory to be in.
	if b, err := os.ReadFile(script); err != nil || !bytes.Equal(b, refinery.MergeLocal) {
		t.Errorf("merge-local: %v", err)
	}
	if info, err := os.Stat(script); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("merge-local mode: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(home, "logs")); err != nil {
		t.Error(err)
	}
	routes, _ := st.Routes(ctx)
	var got []string
	for _, r := range routes {
		got = append(got, r.Stage+" "+deref(r.Depot)+" "+r.Group)
	}
	slices.Sort(got)
	if w := []string{"approved web-app web-app/refinery", "new web-app web-app/builders", "review web-app web-app/reviewers"}; !slices.Equal(got, w) {
		t.Errorf("routes = %v", got)
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
	if n := len(slices.DeleteFunc(slices.Clone(f.asked), func(k string) bool { return k != "go" })); n != 7 || len(f.goes) != 0 {
		t.Errorf("asked how to go on %d times, want 7: %v", n, f.asked)
	}

	// Run again, there is no page left: it says where the bead is.
	f.asked = nil
	if out := setup(t, st); len(f.asked) != 0 || !strings.Contains(out, "The forms end here") || !strings.Contains(out, "  done   a first bead: ") {
		t.Errorf("asked %v on a yard with every page done:\n%s", f.asked, out)
	}
}

// The setup goes on where the yard stands: stopped after every page and run
// again, each run takes exactly the next step's page and none before it,
// whoever did the steps before (here the form, and for one a command).
func TestSetupGoesOnWhereTheYardStands(t *testing.T) {
	f := &former{answers: map[string]any{"name": "web", "title": "Say hello", "goal": "HELLO.md says hello."}}
	home, st, _ := setupYard(t, f)
	ctx := context.Background()
	for _, tc := range []struct {
		step  string
		asked []string // the page's keyed fields, then how to go on
		next  string
	}{
		{stepDepot, []string{"path", "name", "base", "go"}, stepDepotFiles},
		{stepDepotFiles, []string{"commit", "go"}, stepBuilders},
		{stepBuilders, []string{"go"}, stepReviewers},
		{stepReviewers, nil, stepLanding}, // by a command, below
		{stepLanding, []string{"landing", "go"}, stepRoutes},
		{stepRoutes, []string{"routes"}, stepFirstBead},
		{stepFirstBead, []string{"title", "goal", "go"}, stepMerged},
	} {
		if got := todo(t, st); got != tc.step {
			t.Fatalf("next step = %q, want %q", got, tc.step)
		}
		if tc.step == stepReviewers {
			// Someone on the command line, or the yardmaster.
			if _, err := run(t, (&app{st: st}).rootCmd(), "group", "add", "web/reviewers", "--runner", "herdr", "--set", "prompt=roles/reviewer.md"); err != nil {
				t.Fatal(err)
			}
			continue
		}
		f.asked = nil
		if tc.step == stepRoutes {
			f.goes = []string{goHere} // on to the bead's page, which is cancelled
			f.abort = func(fld huh.Field) bool { return fld.GetKey() == "title" }
		}
		if tc.step == stepFirstBead {
			f.goes = []string{goHere} // after the last form page, show how to watch the bead
		}
		out := setup(t, st)
		f.abort = nil
		keyed := slices.DeleteFunc(slices.Clone(f.asked), func(k string) bool { return k == "" })
		if tc.step == stepRoutes {
			keyed = keyed[:1] // then how to go on, and the bead's title
		}
		if !slices.Equal(keyed, tc.asked) {
			t.Fatalf("%s: asked %v, want %v\n%s", tc.step, keyed, tc.asked, out)
		}
		if n := strings.Count(out, "\nSetup: "); n != 1 && tc.step != stepRoutes {
			t.Fatalf("%s: %d pages in one run:\n%s", tc.step, n, out)
		}
		if tc.next != stepMerged && !strings.Contains(out, "`yardr --home "+home+" setup` goes on from here") {
			t.Errorf("%s: not told how to go on:\n%s", tc.step, out)
		}
		if got := todo(t, st); got != tc.next {
			t.Fatalf("after %s the next step = %q, want %q\n%s", tc.step, got, tc.next, out)
		}
	}
	if g, err := st.GetGroup(ctx, "web/builders"); err != nil || g.Limit != 2 {
		t.Fatalf("builders = %+v, %v", g, err)
	}
}

// Cancelling on any page leaves the yard as the finished pages made it: no
// command of the cancelled page has run. The depot's files are the one thing
// a cancelled page leaves behind, in the checkout and uncommitted.
func TestSetupCancelled(t *testing.T) {
	f := &former{answers: map[string]any{"name": "web", "title": "Say hello", "goal": "HELLO.md says hello."}}
	_, st, repo := setupYard(t, f)
	lastConfirm := func(fld huh.Field) bool { _, ok := fld.(*huh.Confirm); return ok }
	key := func(k string) func(huh.Field) bool {
		return func(fld huh.Field) bool { return fld.GetKey() == k }
	}
	for _, tc := range []struct {
		step  string
		abort func(huh.Field) bool // at the page's last question
	}{
		{stepDepot, key("base")},
		{stepDepotFiles, key("commit")},
		{stepBuilders, lastConfirm},
		{stepReviewers, lastConfirm},
		{stepLanding, key("landing")},
		{stepRoutes, key("routes")},
		{stepFirstBead, key("goal")},
	} {
		if got := todo(t, st); got != tc.step {
			t.Fatalf("next step = %q, want %q", got, tc.step)
		}
		before := yardSnapshot(t, st)
		f.abort = tc.abort
		out := setup(t, st)
		if !strings.Contains(out, "Cancelled at: ") || strings.Contains(out, "$ YARDR_HOME") && tc.step != stepDepotFiles {
			t.Errorf("%s cancelled:\n%s", tc.step, out)
		}
		if after := yardSnapshot(t, st); after != before {
			t.Errorf("%s cancelled changed the yard:\n%s\n%s", tc.step, before, after)
		}
		if tc.step == stepDepotFiles {
			if got := gitIn(t, repo, "status", "--porcelain"); got != "?? .yardr/" {
				t.Errorf("after depot init, cancelled at the commit: %q", got)
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

// "No" to the commit leaves the files uncommitted and the setup waiting
// there, with the command to commit them by hand; the page again, with yes,
// commits them, depot init keeping what it wrote. A base that is not the
// branch checked out is never committed on.
func TestSetupCommitByHand(t *testing.T) {
	f := &former{answers: map[string]any{"name": "web", "commit": false}, goes: []string{goHere}}
	_, st, repo := setupYard(t, f)
	out := setup(t, st)
	wantText(t, out, "Not committed. The files are in "+repo+"/.yardr; to commit them:",
		"git -C "+shellQuote(repo)+" add .yardr && git -C "+shellQuote(repo)+" commit -m '"+starterCommit+"' -- .yardr",
		"The setup waits at: the depot's .yardr files on its base.")
	if got := todo(t, st); got != stepDepotFiles {
		t.Fatalf("next step = %q", got)
	}
	if got := gitIn(t, repo, "log", "--format=%s"); got != "first" {
		t.Fatalf("committed: %s", got)
	}

	gitIn(t, repo, "checkout", "-q", "-b", "work")
	f.asked = nil
	out = setup(t, st)
	wantText(t, out, "The base, main, is not the branch checked out in "+repo+" (work), so this form does not commit there.")
	if slices.Contains(f.asked, "commit") || todo(t, st) != stepDepotFiles {
		t.Fatalf("asked to commit off the base: %v\n%s", f.asked, out)
	}

	gitIn(t, repo, "checkout", "-q", "main")
	// Something else staged in the checkout is not swept into the commit.
	if err := os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "wip.txt")
	f.answers["commit"] = true
	out = setup(t, st)
	wantText(t, out, "nothing written, every file is already in "+repo)
	if got := todo(t, st); got != stepBuilders {
		t.Fatalf("next step = %q\n%s", got, out)
	}
	if got := gitIn(t, repo, "status", "--porcelain"); got != "A  wip.txt" {
		t.Errorf("status after the commit: %q", got)
	}
}

// By hand is a manual group of the people named, routed for approved in the
// same page, so the step shows done; the routes page then has two left. A
// base that is no local branch starts the question at by hand and says why.
func TestSetupLandsByHand(t *testing.T) {
	f := &former{answers: map[string]any{"members": "jo, sam"}, goes: []string{goHere}}
	home, st, _ := setupYard(t, f)
	ctx := context.Background()
	depot := gitDepot(t, st)
	commitDepot(t, depot)
	// A tag resolves as a base and is no branch a refinery could land on.
	gitIn(t, depot.Path, "tag", "v1")
	depot.Base = "v1"
	if err := st.PutDepot(ctx, depot, "me"); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"builder", "reviewer"} {
		if err := st.PutGroup(ctx, store.Group{Name: "web/" + role + "s", Runner: "herdr", Config: map[string]any{"prompt": "roles/" + role + ".md"}}); err != nil {
			t.Fatal(err)
		}
	}
	var desc string
	f.abort = func(fld huh.Field) bool {
		if fld.GetKey() == "landing" {
			desc = fld.View()
		}
		return false
	}
	out := setup(t, st)
	yardr := "yardr --home " + home
	wantText(t, out, "$ "+yardr+" group add web/mergers --runner manual --members jo,sam",
		"$ "+yardr+" route add --stage approved --depot web --group web/mergers",
		"$ "+yardr+" route add --stage new --depot web --group web/builders",
		"$ "+yardr+" route add --stage review --depot web --group web/reviewers")
	if strings.Count(out, "--stage approved") != 1 {
		t.Errorf("approved routed twice:\n%s", out)
	}
	if !strings.Contains(desc, "is not a local branch") {
		t.Errorf("the question does not say why by hand:\n%s", desc)
	}
	g, err := st.GetGroup(ctx, "web/mergers")
	if err != nil || g.Runner != "manual" || !slices.Equal(g.Members, []string{"jo", "sam"}) {
		t.Fatalf("mergers = %+v, %v", g, err)
	}
	if _, err := os.Stat(filepath.Join(home, "scripts", "merge-local")); err == nil {
		t.Error("merge-local installed for a yard that lands by hand")
	}
	if got := todo(t, st); got != stepFirstBead {
		t.Fatalf("next step = %q\n%s", got, out)
	}
}

// A merge-local already in the home's scripts is kept as it is.
func TestSetupKeepsAnInstalledMergeLocal(t *testing.T) {
	f := &former{}
	home, st, _ := setupYard(t, f)
	depot := gitDepot(t, st)
	commitDepot(t, depot)
	ctx := context.Background()
	for _, role := range []string{"builder", "reviewer"} {
		if err := st.PutGroup(ctx, store.Group{Name: "web/" + role + "s", Runner: "herdr", Config: map[string]any{"prompt": "roles/" + role + ".md"}}); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(home, "scripts", "merge-local")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\n# the yard's own\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := setup(t, st)
	wantText(t, out, "differs from this build's: "+script+" is kept as it is")
	if b, _ := os.ReadFile(script); string(b) != "#!/bin/sh\n# the yard's own\n" {
		t.Errorf("merge-local replaced:\n%s", b)
	}
	if got := todo(t, st); got != stepRoutes {
		t.Fatalf("next step = %q\n%s", got, out)
	}
}

// "Talk to the yardmaster" after a page opens the chat on the yardmaster
// (yardr-8j3n; it printed where the yardmaster's pane is before, and with
// --no-chat it still does). With no yardmaster running it opens no empty
// chat: it says what yardr doctor says of the yardmaster, and how to start
// the yard.
func TestSetupTalkToTheYardmaster(t *testing.T) {
	f := &former{answers: map[string]any{"name": "web"}, goes: []string{goYardmaster}}
	home, st, _ := setupYard(t, f)
	mustAs(t, st, "Jo", "user", "add", "me")
	focused := stubFocus(t)
	chats := stubChat(t)
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
	if len(*focused) != 0 || !slices.Equal(*chats, []string{"yardmaster, expecting yardmaster"}) || todo(t, st) != stepReviewers {
		t.Fatalf("focused %v, chats %v, next %s\n%s", *focused, *chats, todo(t, st), out)
	}

	// --no-chat: the pane, as before there was a chat.
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
	_, st, _ := setupYard(t, f)
	check := func(out string) {
		t.Helper()
		wantText(t, out, "  todo   a depot: ", "Next step: a depot.", here("yardr depot add <depot> <path> --base <branch>"))
		if len(f.asked) != 0 || todo(t, st) != stepDepot {
			t.Fatalf("asked %v", f.asked)
		}
	}
	setInteractive(t, false)
	check(setup(t, st))
	setInteractive(t, true)
	t.Setenv("YARDR_CREW", "yardmaster")
	check(setup(t, st))
	t.Setenv("YARDR_CREW", "")

	var rep setupReport
	if err := json.Unmarshal([]byte(setup(t, st, "--json")), &rep); err != nil {
		t.Fatal(err)
	}
	if len(f.asked) != 0 || len(rep.Steps) != 8 || rep.Next == nil || rep.Next.Key != stepDepot {
		t.Fatalf("asked %v, json %+v", f.asked, rep)
	}
}

// The onboarding ends in the same forms: once the yardmaster is up it asks
// whether to talk to the yardmaster or keep going here, and "here" is the first
// page of the setup. Run for yardr setup itself, the command asks no second
// time.
func TestOnboardingKeepsGoing(t *testing.T) {
	home := newHome(t)
	setInteractive(t, true)
	stubLookPath(t, "claude", "git", "herdr")
	st := storetest.New(t)
	stubStart(t, st, yardmasterHandle())
	stubPane(t, answeredPane)
	focused := stubFocus(t)
	f := &former{t: t, goes: []string{goHere}, abort: func(fld huh.Field) bool { return fld.GetKey() == "path" }}
	stubAsk(t, f.ask)

	out, err := run(t, (&app{st: st}).rootCmd(), "setup")
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

// groupArgs is the command line that stores the group: given back to
// yardr group add it stores the same, whatever the config's types.
func TestGroupArgsRoundTrip(t *testing.T) {
	newHome(t)
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, "default_kind", "claude", "me"); err != nil {
		t.Fatal(err)
	}
	for _, g := range []store.Group{
		{Name: "a", Runner: "herdr", Limit: 3, Config: map[string]any{"kind": "codex", "model": "5", "prompt": "roles/builder.md",
			"args": []any{"--ask", "never"}, "env": map[string]any{"A": "b c"}}},
		{Name: "b", Runner: "exec", Limit: 1, Config: map[string]any{"cmd": "YARDR_PUSH=origin '/a b/merge-local'", "log": "/l/r.log"}},
		{Name: "c", Runner: "manual", Limit: 1, Members: []string{"jo", "sam"}, Config: map[string]any{"notify": true, "note": "true"}},
	} {
		if _, err := run(t, (&app{st: st}).rootCmd(), groupArgs(g)...); err != nil {
			t.Fatalf("%v: %v", groupArgs(g), err)
		}
		got, err := st.GetGroup(ctx, g.Name)
		if err != nil || got.Runner != g.Runner || got.Limit != g.Limit || !slices.Equal(got.Members, g.Members) || !reflect.DeepEqual(got.Config, g.Config) {
			t.Errorf("%v\nstored %+v, want %+v (%v)", groupArgs(g), got, g, err)
		}
	}
}

func TestDepotNameFor(t *testing.T) {
	for dir, want := range map[string]string{"/a/Web App": "web-app", "/a/yardr": "yardr", "/a/_x.Y_": "x.y", "/a/über": "ber"} {
		if got := depotNameFor(dir); got != want {
			t.Errorf("depotNameFor(%q) = %q, want %q", dir, got, want)
		}
	}
}

// The step for routes names the group each stage goes to once the yard has
// one, so its command can be run as it is printed.
func TestRoutesStepNamesTheGroup(t *testing.T) {
	s := setupState{depots: []store.Depot{{Name: "web", Kind: store.DepotGit}},
		groups: []store.Group{{Name: "web/builders", Runner: "herdr", Config: map[string]any{"prompt": "roles/builder.md"}}}}
	step := s.routesStep("web", true)
	if step.Next != "yardr route add --stage new --depot web --group web/builders" || len(step.args) != 1 {
		t.Fatalf("step = %+v", step)
	}
	s.routes = []store.Route{{Stage: "new", Group: "web/builders"}}
	if step = s.routesStep("web", true); step.Next != "yardr route add --stage review --depot web --group <group>" || len(step.args) != 0 {
		t.Fatalf("step = %+v", step)
	}
}
