package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// uninstallJSON runs yardr uninstall --json with flags, and returns its
// answer and its error.
func uninstallJSON(t *testing.T, a *app, flags ...string) (uninstallReport, error) {
	t.Helper()
	out, err := run(t, a.rootCmd(), append([]string{"uninstall", "--json", "--handoff-wait", "0"}, flags...)...)
	var rep uninstallReport
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		t.Fatalf("uninstall --json %v: %v (%v)\n%s", flags, jerr, err, out)
	}
	return rep, err
}

// leftOf are the things of kind in a report's list.
func leftOf(rep uninstallReport, kind string) []leftThing {
	var out []leftThing
	for _, l := range rep.Left {
		if l.Kind == kind {
			out = append(out, l)
		}
	}
	return out
}

// yardWorktree adds a worktree of depot for a bead's branch where yardr
// puts one, with a file in no commit when dirty.
func yardWorktree(t *testing.T, home string, depot store.Depot, bead string, dirty bool) string {
	t.Helper()
	path := filepath.Join(config.At(home).Worktrees(), depot.Name, bead)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, depot.Path, "worktree", "add", "-q", path, "-b", store.Branch(bead))
	if dirty {
		if err := os.WriteFile(filepath.Join(path, "half-done.txt"), []byte("not committed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// uninstall of a yard that runs and starts at login stops it as down does,
// removes every login service written for it, the web view's that yardr web
// install wrote too, and herdr's, and lists what it left: each kind of
// thing, with its path. A second uninstall finds nothing to do, says so and
// is no failure.
func TestUninstallStopsTheYardAndRemovesItsServices(t *testing.T) {
	for _, system := range []string{"darwin", "linux"} {
		t.Run(system, func(t *testing.T) {
			home, st, a, herdrStarts, _ := upYard(t, false)
			rig := stubLogin(t, system, herdrStarts)
			stubLookPath(t, "claude", "git", "herdr", "systemctl")
			ctx := context.Background()
			depot := gitDepot(t, st)
			commitDepot(t, depot)
			dirty := resolve(yardWorktree(t, home, depot, "web-1", true))
			yardWorktree(t, home, depot, "web-2", false)

			if rep := upJSON(t, a(true), "--login"); !rep.Up {
				t.Fatalf("up --login: %+v", rep)
			}
			// The web view's service, on a yard that names no address for
			// it: yardr web install's, which down --login leaves.
			web, err := newUnit(os.Environ(), config.At(home), kindOf(svcWeb), []string{"--addr", "127.0.0.1:1"})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(web.Path, []byte(web.render()), 0o644); err != nil {
				t.Fatal(err)
			}
			daemon, _, herdrFile := "", "", filepath.Join(rig.dir, "dev.yardr.herdr.plist")
			if _, _, daemon, err = unitFile(config.At(home), svcDaemon); err != nil {
				t.Fatal(err)
			}
			if system == "linux" {
				herdrFile = filepath.Join(rig.dir, "yardr-herdr.service")
			}
			if err := st.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "yardmaster", "tab": "w1:t1"}); err != nil {
				t.Fatal(err)
			}

			calls := len(rig.m.calls)
			out, err := run(t, a(false).rootCmd(), "uninstall", "--yes", "--handoff-wait", "0")
			if err != nil {
				t.Fatalf("uninstall: %v\n%s", err, out)
			}
			name := runner.YardName(home, nil)
			wantText(t, out,
				"daemon          stopped (pid ",
				"server          stopped (pid 4711)\n",
				"herdr's server  left running: other yards and your own panes are in it\n",
				"\nAt login:\ndaemon          removed: "+daemon+"\n",
				"web view        removed: "+web.Path+"\n",
				"herdr's server  removed: "+herdrFile+": herdr's server is left running, and no longer starts at login\n",
				"\nNot touched, and where each is:\n",
				"  the yard's home    "+home+": the yard "+name+": its store with 0 beads, its roles, flows and logs\n",
				"                       to remove: "+here("yardr uninstall --purge")+"\n",
				"  depot web          "+depot.Path+": your repository: yardr deletes nothing in it\n",
				"    .yardr           "+filepath.Join(depot.Path, ".yardr")+": the depot's roles, flows and test command",
				"    worktree         "+dirty+": branch yard/web-1, uncommitted changes\n",
				"                       to remove: git -C "+depot.Path+" worktree remove "+dirty+"\n",
				": branch yard/web-2\n",
				"    branches         "+depot.Path+": 2 under yard/: yard/web-1, yard/web-2\n",
				"  yardr              /opt/yardr/bin/yardr: this program: uninstall does not delete itself\n",
				"  herdr              /bin/herdr: its server is left running",
				"  harness claude     /bin/claude: ",
				"\nThe yard "+name+" in "+home+" is uninstalled: nothing of it runs or starts at login, and all of your work is where it was.\n",
				"To use it again: "+here("yardr up")+". To delete its home too: "+here("yardr uninstall --purge")+"\n")
			for _, f := range []string{daemon, web.Path, herdrFile} {
				if _, err := os.Stat(f); err == nil {
					t.Errorf("%s is still there", f)
				}
			}
			if config.DaemonRunning(config.At(home).DaemonLock()) {
				t.Error("the daemon still runs")
			}
			// herdr's server is never stopped: its service is not unloaded.
			for _, c := range rig.m.calls[calls:] {
				if strings.Contains(c, "herdr") && (strings.Contains(c, "bootout") || strings.Contains(c, "--now") || strings.Contains(c, " stop ")) {
					t.Errorf("uninstall stopped herdr's server: %s", c)
				}
			}
			// Nothing of the person's work was touched.
			if _, err := os.Stat(filepath.Join(dirty, "half-done.txt")); err != nil {
				t.Error(err)
			}
			if got := gitIn(t, depot.Path, "branch", "--list", "yard/*"); !strings.Contains(got, "yard/web-1") || !strings.Contains(got, "yard/web-2") {
				t.Errorf("branches after uninstall: %s", got)
			}

			// Once more: nothing runs, no service is there.
			stub(t, &serverRunning, func(context.Context, *config.Config) bool { return false })
			out, err = run(t, a(false).rootCmd(), "uninstall")
			if err != nil {
				t.Fatalf("a second uninstall: %v\n%s", err, out)
			}
			wantText(t, out, "daemon          was not running\n", "agents          none was running\n",
				"daemon          was not installed\n", "herdr's server  was not installed\n",
				"\nNothing of the yard was running, and no login service of it was installed.\n",
				"\nNot touched, and where each is:\n", "  depot web          ")
			if strings.Contains(out, "web view") {
				t.Errorf("a second uninstall speaks of a service that is not there:\n%s", out)
			}

			rep, err := uninstallJSON(t, a(true))
			if err != nil || !rep.Uninstalled || !rep.Yard || rep.Name != name || rep.Purge != nil || rep.Down == nil || !rep.Down.Down {
				t.Fatalf("uninstall --json: %v %+v", err, rep)
			}
			if loginOf(t, rep.Login, svcDaemon).Result != loginAbsent || loginOf(t, rep.Login, partHerdr).Result != loginAbsent {
				t.Fatalf("login: %+v", rep.Login)
			}
			for _, kind := range []string{leftHome, leftDepot, leftYardrDir, leftWorktree, leftBranches, leftYardr, leftHerdr, leftHarness} {
				for _, l := range leftOf(rep, kind) {
					if l.Path == "" {
						t.Errorf("%s has no path: %+v", kind, l)
					}
				}
				if len(leftOf(rep, kind)) == 0 {
					t.Errorf("the list names no %s: %+v", kind, rep.Left)
				}
			}
			wt := leftOf(rep, leftWorktree)
			if len(wt) != 2 || !wt[0].Uncommitted || wt[1].Uncommitted || wt[0].Path != dirty {
				t.Errorf("worktrees: %+v", wt)
			}
			if b := leftOf(rep, leftBranches); !slices.Equal(b[0].Branches, []string{"yard/web-1", "yard/web-2"}) {
				t.Errorf("branches: %+v", b)
			}
		})
	}
}

// Another yard's login services are not this yard's to remove, and herdr's
// is shared: uninstall in one yard leaves both, names the other yard, and
// lists herdr's service among what it did not touch. One for herdr's server
// that yardr did not write is never touched.
func TestUninstallLeavesAnotherYardsServices(t *testing.T) {
	homeA, _, a, herdrStarts, _ := upYard(t, false)
	rig := stubLogin(t, "darwin", herdrStarts)
	herdrFile := filepath.Join(rig.dir, "dev.yardr.herdr.plist")
	if rep := upJSON(t, a(true), "--login"); !rep.Up {
		t.Fatalf("the first yard: %+v", rep)
	}
	_, _, daemonA, _ := unitFile(config.At(homeA), svcDaemon)
	before, err := os.ReadFile(daemonA)
	if err != nil {
		t.Fatal(err)
	}

	homeB, _, b, startsB, _ := upYard(t, true)
	rig.herdrStarts = startsB
	rig.use(t)
	if rep := upJSON(t, b(true), "--login"); !rep.Up {
		t.Fatalf("the second yard: %+v", rep)
	}
	_, _, daemonB, _ := unitFile(config.At(homeB), svcDaemon)
	calls := len(rig.m.calls)
	rep, err := uninstallJSON(t, b(true), "--yes")
	if err != nil || !rep.Uninstalled {
		t.Fatalf("uninstall in the second yard: %v %+v", err, rep)
	}
	h := loginOf(t, rep.Login, partHerdr)
	if loginOf(t, rep.Login, svcDaemon).Result != loginRemoved || h.Result != loginLeft || !strings.Contains(h.Detail, homeA) {
		t.Fatalf("login: %+v", rep.Login)
	}
	if s := leftOf(rep, leftService); len(s) != 1 || s[0].Name != partHerdr || s[0].Path != herdrFile {
		t.Errorf("the list has no service of herdr's server: %+v", rep.Left)
	}
	if _, err := os.Stat(daemonB); err == nil {
		t.Error("the second yard's daemon service is still there")
	}
	if now, err := os.ReadFile(daemonA); err != nil || string(now) != string(before) {
		t.Errorf("the first yard's daemon service was touched: %v", err)
	}
	if _, err := os.Stat(herdrFile); err != nil {
		t.Errorf("herdr's service is gone while the first yard needs it: %v", err)
	}
	for _, c := range rig.m.calls[calls:] {
		if strings.Contains(c, homeID(homeA)) || strings.Contains(c, "herdr") && !strings.Contains(c, " print ") {
			t.Errorf("uninstall in the second yard told launchd: %s", c)
		}
	}

	// A service for herdr's server that is the person's own. The runs
	// name their yard's home in their maps (upYard), but the login rig
	// keys its daemon locks by the process's home, so the first yard's
	// is restored for its uninstall: without it the rig never releases
	// the first yard's lock and down reports the yard was not stopped.
	t.Setenv("YARDR_HOME", homeA)
	if err := os.WriteFile(herdrFile, []byte("<plist><string>/opt/herdr</string>\n<string>server</string></plist>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err = uninstallJSON(t, a(true), "--yes")
	h = loginOf(t, rep.Login, partHerdr)
	if err != nil || !rep.Uninstalled || h.Result != loginLeft || !slices.Equal(h.Foreign, []string{herdrFile}) {
		t.Fatalf("uninstall beside a service of the person's own: %v %+v", err, rep.Login)
	}
	if _, err := os.Stat(herdrFile); err != nil {
		t.Errorf("a service yardr did not write was removed: %v", err)
	}
	if s := leftOf(rep, leftService); len(s) != 1 || s[0].Path != herdrFile {
		t.Errorf("the list: %+v", rep.Left)
	}
}

// Nothing is removed while nothing is stopped: with an agent at work and
// nobody to ask uninstall refuses as down does, and --json hands a program
// the agents to ask about.
func TestUninstallRemovesNothingBeforeItStops(t *testing.T) {
	home, _, panes, _, a, serverStops := downYard(t, map[string]string{"b": "working"}, "b")
	stubServices(t, "darwin")
	d, err := newUnit(os.Environ(), config.At(home), kindOf(svcDaemon), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(d.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.Path, []byte(d.render()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a(false).rootCmd(), "uninstall")
	if !errors.As(err, new(errDownRefused)) || !strings.Contains(err.Error(), here("yardr uninstall --yes")) {
		t.Fatalf("uninstall with an agent at work: %v\n%s", err, out)
	}
	rep, err := uninstallJSON(t, a(true))
	if err != nil || !rep.Confirm || rep.Uninstalled || len(rep.Login) != 1 || len(rep.Down.Agents) != 2 || len(leftOf(rep, leftHome)) != 1 {
		t.Fatalf("uninstall --json without --yes: %v %+v", err, rep)
	}
	daemon := loginOf(t, rep.Login, svcDaemon)
	if daemon.Result != loginLeft || daemon.File != d.Path {
		// The service remains written until a later run can stop the yard.
		t.Fatalf("login service not reported as left in place: %+v", daemon)
	}
	if s := leftOf(rep, leftService); len(s) != 1 || s[0].Path != daemon.File {
		t.Fatalf("the list omits the login service left in place: %+v", rep.Left)
	}
	if _, err := os.Stat(d.Path); err != nil || len(panes.stopped)+*serverStops != 0 {
		t.Fatalf("an uninstall that stopped nothing removed the service (%v), stopped %v, the server %d times", err, panes.stopped, *serverStops)
	}
}

// A service removal failure leaves its file and its path in the one list of
// what uninstall did not touch.
func TestUninstallListsAServiceItCouldNotRemove(t *testing.T) {
	home, _, a, herdrStarts, _ := upYard(t, false)
	rig := stubLogin(t, "darwin", herdrStarts)
	if rep := upJSON(t, a(true), "--login"); !rep.Up {
		t.Fatalf("up --login: %+v", rep)
	}
	_, _, daemon, err := unitFile(config.At(home), svcDaemon)
	if err != nil {
		t.Fatal(err)
	}
	rig.m.fail = func(line string) bool { return strings.Contains(line, "bootout") && strings.Contains(line, "daemon") }
	rep, err := uninstallJSON(t, a(true), "--yes")
	if err == nil || rep.Uninstalled {
		t.Fatalf("uninstall with a service manager refusal: %v %+v", err, rep)
	}
	var found *leftThing
	for i := range rep.Left {
		if rep.Left[i].Kind == leftService && rep.Left[i].Name == svcDaemon {
			found = &rep.Left[i]
			break
		}
	}
	if found == nil || found.Path != daemon || !strings.Contains(found.Detail, "exit status 5") {
		t.Fatalf("the daemon service is not named with its path and error: %+v", rep.Left)
	}
	if _, err := os.Stat(daemon); err != nil {
		t.Fatalf("the service file was removed despite manager refusal: %v", err)
	}
}

// purgeYard is downYard with a home that holds a yard's files, a depot
// cloned into it, and worktrees of that depot: one with a file in no
// commit, one without. It returns the yard's name beside downYard's.
// asOperator makes the commands of the test the operator's, whose alone
// --purge is.
func asOperator(t *testing.T, st store.API) {
	t.Helper()
	settings, err := st.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	yardEnv(t, st)["YARDR_ACTOR"] = or(settings["operator"], defaultOperator())
}

func purgeYard(t *testing.T) (home, name string, depot store.Depot, panes *paneRunner, a func(json bool) *app, serverStops *int, st store.API) {
	t.Helper()
	home, st, panes, _, a, serverStops = downYard(t, map[string]string{"yardmaster": "idle", "b": "idle"}, "b")
	stubServices(t, "darwin")
	asOperator(t, st)
	for _, f := range []string{"yard.db", "daemon.log", "roles/yardmaster.md", "flows/default/flow.yaml"} {
		p := filepath.Join(home, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("the yard's\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// As yardr depot add --clone leaves one: in the home.
	depot = store.Depot{Name: "cloned", Path: filepath.Join(resolve(home), "depots", "cloned"), Base: "main", Kind: store.DepotGit}
	if err := os.MkdirAll(depot.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, depot.Path, "init", "-q", "-b", "main")
	ownDepotFiles(t, depot)
	commitDepot(t, depot)
	if err := st.PutDepot(context.Background(), depot, "me"); err != nil {
		t.Fatal(err)
	}
	yardWorktree(t, home, depot, "cloned-1", true)
	yardWorktree(t, home, depot, "cloned-2", false)
	settings, err := st.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return home, runner.YardName(home, settings), depot, panes, a, serverStops, st
}

// --purge deletes a person's yard, so no caller does it by a habit of
// passing --yes: with nobody to ask it is refused without the yard's name,
// with a wrong one, and with the right one alone, and each refusal stops
// and deletes nothing. With both it stops the yard and deletes its home.
func TestPurgeNeedsTheYardsName(t *testing.T) {
	home, name, _, panes, a, serverStops, _ := purgeYard(t)
	for _, c := range []struct {
		flags []string
		want  string
	}{
		{[]string{"--purge", "--yes"}, "With nobody to ask (no terminal) it takes the yard's name as its value, and --yes: " + here("yardr uninstall --purge="+name+" --yes")},
		{[]string{"--purge"}, "With nobody to ask"},
		{[]string{"--purge=someone-elses", "--yes"}, "--purge=someone-elses does not name this yard"},
		{[]string{"--purge=", "--yes"}, "--purge= does not name this yard"},
		{[]string{"--purge=" + name}, "it needs --yes beside the yard's name"},
		{[]string{"--purge=" + name, "--json"}, "it needs --yes beside the yard's name"},
	} {
		out, err := run(t, a(slices.Contains(c.flags, "--json")).rootCmd(), append([]string{"uninstall"}, c.flags...)...)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "Nothing was stopped or deleted.") ||
			!strings.Contains(err.Error(), home+", with 1 bead in its store") {
			t.Errorf("uninstall %v: %v\n%s", c.flags, err, out)
		}
		if _, err := os.Stat(filepath.Join(home, "yard.db")); err != nil || len(panes.stopped)+*serverStops != 0 {
			t.Fatalf("a refused uninstall %v: the store %v, stopped %v, the server %d times", c.flags, err, panes.stopped, *serverStops)
		}
	}
	// The name is not a positional argument.
	if out, err := run(t, a(false).rootCmd(), "uninstall", "--purge", name, "--yes"); err == nil || !strings.Contains(err.Error(), "--purge=<name>") {
		t.Fatalf("uninstall --purge %s: %v\n%s", name, err, out)
	}
	if _, err := os.Stat(filepath.Join(home, "yard.db")); err != nil {
		t.Fatal(err)
	}
}

// With the yard's name and --yes, --purge stops the yard and deletes its
// home, and nothing else of the person's: the depot cloned into it stays
// whole with every branch, and so does the worktree with a file in no
// commit. The worktree without is removed as git removes one. The home
// stays for what was kept, and the list says where that is.
func TestPurgeDeletesTheHomeAndKeepsTheWork(t *testing.T) {
	home, name, depot, panes, a, serverStops, _ := purgeYard(t)
	dirty := filepath.Join(resolve(home), "worktrees", "cloned", "cloned-1")
	clean := filepath.Join(resolve(home), "worktrees", "cloned", "cloned-2")
	out, err := run(t, a(false).rootCmd(), "uninstall", "--purge="+name, "--yes", "--handoff-wait", "0")
	if err != nil {
		t.Fatalf("uninstall --purge: %v\n%s", err, out)
	}
	wantText(t, out,
		"agents          stopped 2: 1 bead session",
		"home            deleted but for what is kept: "+home+", with 1 bead\n",
		"                  kept "+depot.Path+" (depot cloned)\n",
		"                  kept "+dirty+" (a worktree: uncommitted changes)\n",
		"  the yard's home    "+home+": no yard is in it any more: it stays for what the purge kept\n",
		"  depot cloned       "+depot.Path+": your repository",
		"    worktree         "+dirty+": branch yard/cloned-1, uncommitted changes\n",
		"    branches         "+depot.Path+": 2 under yard/: yard/cloned-1, yard/cloned-2\n",
		"\nThe yard "+name+" is deleted, and nothing of it runs or starts at login.\n")
	if len(panes.stopped) != 2 || *serverStops != 1 {
		t.Errorf("stopped %v, the server %d times", panes.stopped, *serverStops)
	}
	for _, gone := range []string{"yard.db", "daemon.log", "roles", "flows", "worktrees/cloned/cloned-2"} {
		if _, err := os.Stat(filepath.Join(home, gone)); err == nil {
			t.Errorf("%s is still in the home", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(dirty, "half-done.txt")); err != nil {
		t.Errorf("the worktree with uncommitted work: %v", err)
	}
	if _, err := os.Stat(filepath.Join(depot.Path, ".yardr")); err != nil {
		t.Errorf("the depot's own files: %v", err)
	}
	if got := gitIn(t, depot.Path, "worktree", "list"); strings.Contains(got, clean) || !strings.Contains(got, dirty) {
		t.Errorf("the depot's worktrees after the purge:\n%s", got)
	}
	if got := gitIn(t, depot.Path, "branch", "--list", "yard/*"); !strings.Contains(got, "yard/cloned-1") || !strings.Contains(got, "yard/cloned-2") {
		t.Errorf("a branch was deleted: %s", got)
	}
}

// A home with nothing of the person's in it is gone whole, and the answer
// as data says so.
func TestPurgeRemovesAHomeWithNothingKept(t *testing.T) {
	home, st, _, _, a, _ := downYard(t, map[string]string{"yardmaster": "idle"})
	stubServices(t, "linux")
	asOperator(t, st)
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "yard.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := uninstallJSON(t, a(true), "--yes", "--purge="+runner.YardName(home, nil))
	if err != nil || !rep.Uninstalled || rep.Purge == nil || !rep.Purge.Deleted || !rep.Purge.Gone || len(rep.Purge.Kept) != 0 || len(leftOf(rep, leftHome)) != 0 {
		t.Fatalf("uninstall --purge --json: %v %+v\npurge %+v", err, rep, rep.Purge)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the home after the purge: %v", err)
	}
}

// In a terminal --purge names the directory and its beads and asks for the
// yard's name: anything else leaves the yard as it is, the name deletes it.
func TestPurgeAsksForTheYardsNameInATerminal(t *testing.T) {
	home, name, _, panes, a, serverStops, _ := purgeYard(t)
	stub(t, &interactive, func() bool { return true })
	typed := "yes"
	stub(t, &ask, func(fields ...huh.Field) error {
		if len(fields) != 1 {
			t.Errorf("asked %d fields", len(fields))
		}
		in := fields[0].(*huh.Input)
		in.WithKeyMap(huh.NewDefaultKeyMap())
		in.Focus()
		in.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(typed)})
		return nil
	})
	// --yes answers down's question, never this one.
	out, err := run(t, a(false).rootCmd(), "uninstall", "--purge", "--yes")
	if err == nil || !strings.Contains(err.Error(), `"yes" is not the yard's name. Nothing was stopped or deleted.`) || len(panes.stopped)+*serverStops != 0 {
		t.Fatalf("a wrong name typed: %v, stopped %v\n%s", err, panes.stopped, out)
	}
	wantText(t, out, "yardr uninstall --purge deletes the home of the yard "+name+", "+home+", with 1 bead in its store.\n",
		"Kept: a depot's repository, a worktree with uncommitted changes, every branch, and this program.\n")
	if _, err := os.Stat(filepath.Join(home, "yard.db")); err != nil {
		t.Fatal(err)
	}
	typed = name
	out, err = run(t, a(false).rootCmd(), "uninstall", "--purge", "--handoff-wait", "0")
	if err != nil {
		t.Fatalf("the name typed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, "yard.db")); err == nil || len(panes.stopped) != 2 {
		t.Fatalf("the name typed: the store is there (%v), stopped %v\n%s", err, panes.stopped, out)
	}
}

// --purge deletes a yard's home and no more than that: a home a depot lies
// around, or the person's own home directory, is refused before anything
// is stopped.
func TestPurgeRefusesAHomeThatIsMoreThanAYard(t *testing.T) {
	home, st, panes, _, a, _ := downYard(t, map[string]string{"yardmaster": "idle"})
	stubServices(t, "darwin")
	asOperator(t, st)
	name := runner.YardName(home, nil)
	if err := os.MkdirAll(filepath.Join(home, "jo"), 0o755); err != nil {
		t.Fatal(err)
	}
	stub(t, &userHomeDir, func() (string, error) { return filepath.Join(home, "jo"), nil })
	out, err := run(t, a(false).rootCmd(), "uninstall", "--purge="+name, "--yes", "--handoff-wait", "0")
	if err == nil || !strings.Contains(err.Error(), "your home directory") || len(panes.stopped) != 0 {
		t.Fatalf("a home around the user's: %v\n%s", err, out)
	}
	stub(t, &userHomeDir, func() (string, error) { return "/Users/jo", nil })
	if err := st.PutDepot(context.Background(), store.Depot{Name: "around", Path: filepath.Dir(home), Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a(false).rootCmd(), "uninstall", "--purge="+name, "--yes", "--handoff-wait", "0")
	if err == nil || !strings.Contains(err.Error(), "it is in depot around") || len(panes.stopped) != 0 {
		t.Fatalf("a home in a depot: %v\n%s", err, out)
	}
}

// On a home with no yard there is nothing to stop, and that is no failure:
// what a yard that was deleted by hand left at login is removed all the
// same, since a service carries its home in its name, and no server is
// started to find that out. --purge deletes nothing there.
func TestUninstallOnNoYard(t *testing.T) {
	home := newHomeDir(t)
	a := testApp(t, nil, home)
	// The directory is there, and no store in it.
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "kept"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir, _ := stubServices(t, "darwin")
	stubLookPath(t, "git")
	stub(t, &connect, func(context.Context, *config.Config) (store.API, error) {
		t.Error("connected to the store, which starts a server")
		return nil, errNoServer
	})
	d, err := newUnit(a.environ(), config.At(home), kindOf(svcDaemon), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.Path, []byte(d.render()), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.rootCmd(), "uninstall", "--purge=y", "--yes")
	if err != nil {
		t.Fatalf("uninstall on no yard: %v\n%s", err, out)
	}
	wantText(t, out, "No yard is in "+home+": nothing to stop.\n", "daemon          removed: "+d.Path+"\n",
		"home            "+home+": no yard is in it: nothing was deleted\n",
		"  the yard's home    "+home+": no yard is in it; it holds kept\n",
		"  yardr              /opt/yardr/bin/yardr", "\nNothing of yardr runs or starts at login for "+home+".\n")
	if _, err := os.Stat(d.Path); err == nil {
		t.Error("the daemon's service is still there")
	}
}

// Where yardr writes no login service there is none to remove: uninstall
// stops the yard and lists what is left.
func TestUninstallWhereThereIsNoLoginService(t *testing.T) {
	_, _, panes, _, a, _ := downYard(t, map[string]string{"yardmaster": "idle"})
	stubServices(t, "freebsd")
	rep, err := uninstallJSON(t, a(true), "--yes")
	if err != nil || !rep.Uninstalled || len(rep.Login) != 0 || len(panes.stopped) != 1 {
		t.Fatalf("uninstall on freebsd: %v %+v", err, rep)
	}
}

// The store, and with it everything the store keeps for the operator (its
// users, its peers, its settings), is the operator's to delete: --purge as
// anyone else, an agent in a pane with the yard's name and --yes, is
// refused before anything is stopped.
func TestPurgeIsTheOperators(t *testing.T) {
	home, name, _, panes, a, serverStops, _ := purgeYard(t)
	for _, json := range []bool{false, true} {
		out, err := run(t, a(json).rootCmd(), "uninstall", "--purge="+name, "--yes", "--handoff-wait", "0", "--actor", "web-1-new")
		if err == nil || !strings.Contains(err.Error(), "may delete the yard's home; this is web-1-new. Nothing was stopped or deleted.") {
			t.Errorf("uninstall --purge as an agent: %v\n%s", err, out)
		}
		if _, err := os.Stat(filepath.Join(home, "yard.db")); err != nil || len(panes.stopped)+*serverStops != 0 {
			t.Fatalf("a refused purge: the store %v, stopped %v, the server %d times", err, panes.stopped, *serverStops)
		}
	}
}

// Where the file system takes two spellings for one directory (letter case
// on macOS and Windows), a home named in another spelling than the store
// has its depot under is the same home: the depot in it is kept whole, not
// taken for one that lies elsewhere and deleted with the rest.
func TestPurgeKeepsADepotUnderAnotherSpellingOfTheHome(t *testing.T) {
	home, name, depot, _, a, _, st := purgeYard(t)
	odd := filepath.Join(strings.ToUpper(filepath.Dir(home)), filepath.Base(home))
	if _, err := os.Stat(odd); err != nil || odd == home {
		t.Skip("this file system tells letter case apart")
	}
	yardEnv(t, st)["YARDR_HOME"] = odd
	out, err := run(t, a(false).rootCmd(), "uninstall", "--purge="+name, "--yes", "--handoff-wait", "0")
	if err != nil {
		t.Fatalf("uninstall --purge: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(home, "yard.db")); err == nil {
		t.Errorf("the store is still in the home\n%s", out)
	}
	if strings.Contains(out, "cloned-2 (a worktree") {
		t.Errorf("the worktree that was removed is said to be kept\n%s", out)
	}
	if got := gitIn(t, depot.Path, "log", "--oneline", "-1"); got == "" {
		t.Errorf("the depot in the home has no commit any more\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, "worktrees", "cloned", "cloned-1", "half-done.txt")); err != nil {
		t.Errorf("the worktree with uncommitted work: %v\n%s", err, out)
	}
}
