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
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"yardr.dev/yardr/contrib/assembly"
	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// The guided setup (yardr setup) takes the setup steps one at a time, each
// as a form page that ends in the command a person would type for it. It
// keeps no state of its own: before every page it reads the steps from the
// store (setupSteps) and takes the first one not done, so it goes on where
// the yard stands, whoever did the earlier steps: this form, the yardmaster in
// its pane, or a person on the command line.

// setupReport is what yardr setup prints where it runs no form.
type setupReport struct {
	Home  string      `json:"home"`
	Steps []setupStep `json:"steps"`
	// Next is the first step not done; absent when all are.
	Next *setupStep `json:"next,omitempty"`
}

func (a *app) setupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Go on with the yard's setup where it stands: the next step not done, as a form",
		Long: `Print the setup steps, each done or todo, and take the first one not done as
a form. The steps are the ones yardr doctor lists, read from the store: the
setup goes on where the yard stands, whoever did the earlier steps (this
form, the yardmaster in its pane, a person on the command line), and can be run
again at any time.

Every page ends in a command that exists, and shows its command line before
it runs:

  a depot                 the repository and its base branch: yardr depot add
  the depot's files       yardr depot init, which writes starter roles, a gate
                        and a flow under .yardr/; then it asks before it
                        commits them on the base branch
  builders, reviewers   the form of yardr group add, starting at a group on
                        the yard's default harness with the role's prompt
  landing               an assembly (yardr's merge-local, installed in
                        <home>/scripts/, as an exec group) or people who
                        merge by hand (a manual group, routed for approved)
  routes                new, review and approved, each to the group that
                        does its work: shown, confirmed, yardr route add
  a first bead          its title and goal: yardr bead create, then
                        yardr bead advance --to new; and how to watch it

After each page it asks how to go on: keep going here, talk to the yardmaster
(opens yardr chat on the yardmaster in this terminal; the yardmaster goes on
from the same steps), or stop here. With --no-chat that choice prints where
the yardmaster's pane is and brings it to the front instead. When the
yardmaster is not running it opens nothing and prints what yardr doctor says
of it. A page that is cancelled (ctrl+c, or no at its last question) has run
no command: the yard is as the finished pages made it. The one exception is
the depot's files, which yardr depot init has written into the checkout before the
question whether to commit them; they stay there, and depot init never
overwrites a file.

Where the forms end it asks a user whether to be told when beads land: a
yes is yardr subscribe <you> yardr --done --where depot=<depot>, and each bead
of the depot that is done is then a notification in your inbox (yardr inbox).

Without a terminal, with --json, and in an agent's session it runs no form:
it prints the steps and the command for the next one.`,
		Example: `  yardr setup
  yardr setup --json | jq -r '.next.next'     # the command for the next step`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if a.guided {
				return nil // the onboarding, run for this command, asked already
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			steps, err := setupSteps(c.Context(), st)
			if err != nil {
				return err
			}
			next := nextStep(steps)
			forms := !a.json && interactive() && !agentSession()
			if err := a.out(setupReport{Home: cfg.Home, Steps: steps, Next: next}, func() {
				fmt.Printf("The setup of the yard in %s\n\n", cfg.Home)
				printSteps(steps)
				if next != nil && !forms {
					fmt.Println()
					printNextStep(cfg.Home, next)
				}
			}); err != nil || !forms {
				return err
			}
			return a.guide(c.Context(), cfg, st)
		},
	}
	cmd.Flags().BoolVar(&a.noChat, "no-chat", false, "\"talk to the yardmaster\" prints where its pane is instead of opening yardr chat")
	return cmd
}

// agentSession reports whether this run is an agent's: a session yardr
// started, or a crew member. An agent's pane is a terminal too, and a form
// would sit there unanswered.
func agentSession() bool {
	return config.Getenv("SESSION") != "" || config.Getenv("CREW") != ""
}

// printSteps prints the setup steps, each done or todo.
func printSteps(steps []setupStep) {
	for _, s := range steps {
		tag := "todo"
		if s.Done {
			tag = "done"
		}
		fmt.Printf("  %-5s  %s: %s\n", tag, s.Title, s.Detail)
	}
}

// printNextStep names the step the setup goes on with and its command. The
// step is the store's; the home in front of its commands is the caller's.
func printNextStep(home string, next *setupStep) {
	fmt.Printf("Next step: %s.\n  %s\n", next.Title, strings.ReplaceAll(next.Next, "yardr ", yardrAt(home)+" "))
}

// How to go on after a page.
const (
	goHere       = "here"
	goYardmaster = "yardmaster"
	goStop       = "stop"
)

// handOff is the onboarding's last question, once the yardmaster is up: talk
// to the yardmaster, or keep going in this terminal. Both go by the same
// steps, so the answer is not final: the forms offer the yardmaster again
// after each page, and the yardmaster reads where the forms left the yard.
//
// Talking to the yardmaster is yardr chat, opened here on the yardmaster
// (toChat): the person is in the conversation in the window the form ran in.
// Where no chat can open it is the yardmaster's pane, brought to the front.
func (a *app) handOff(ctx context.Context, cfg *config.Config, yardmaster string, place *yardmasterPlace) error {
	st, err := a.store(ctx)
	if err != nil {
		return err
	}
	steps, err := setupSteps(ctx, st)
	if err != nil {
		return err
	}
	a.guided = true
	pick := goYardmaster
	talk := "Talk to the yardmaster: opens the chat with " + yardmaster + " here"
	if !a.chats() {
		talk = "Talk to the yardmaster: brings " + yardmaster + "'s pane to the front in herdr"
	}
	opts := []huh.Option[string]{huh.NewOption(talk, goYardmaster)}
	if next := nextStep(steps); next != nil {
		opts = append(opts, huh.NewOption("Keep going here: a short run of forms, next "+next.Title, goHere))
	}
	fmt.Println()
	err = ask(huh.NewSelect[string]().Key("go").Title("The rest of the setup").
		Description(fmt.Sprintf("A repository to work in, who builds and reviews, how work lands, a first bead.\n"+
			"%s does it with you as a conversation, or this terminal as forms. Both go by the\n"+
			"same steps (yardr doctor lists them), so you can change over after any of them.", yardmaster)).
		Options(opts...).Value(&pick))
	switch {
	case errors.Is(err, huh.ErrUserAborted):
		return nil
	case err != nil:
		return err
	case pick == goHere:
		return a.guide(ctx, cfg, st)
	}
	if a.chats() {
		if opened, err := a.toChat(ctx, cfg, st, yardmaster); err != nil || opened {
			return err
		}
	}
	openYardmaster(ctx, yardmaster, place)
	return nil
}

// guide is the guided setup at work on a yard.
type guide struct {
	a   *app
	cfg *config.Config
	st  store.API
}

// page does one setup step: it asks what the step's command needs, shows
// the command line and runs it. It reports false when it ran to its end
// without doing the step (an answer of no), having said why.
type page func(ctx context.Context, step setupStep) (done bool, err error)

// pages are the pages by the key of the step each does. The last step, a
// first merged bead, has none: it is the pipeline's to do.
func (g *guide) pages() map[string]page {
	return map[string]page{
		stepDepot:      g.depotPage,
		stepDepotFiles: g.filesPage,
		stepBuilders:   g.groupPage("builder", 2, "Builders implement the beads in stage new, each in a worktree of its own."),
		stepReviewers:  g.groupPage("reviewer", 1, "Reviewers check what the builders report done, in stage review."),
		stepLanding:    g.landingPage,
		stepRoutes:     g.routesPage,
		stepFirstBead:  g.beadPage,
	}
}

// guide takes the steps not done one page at a time, asking after each how
// to go on, until none is left, a page is left undone, or the person stops
// or goes to the yardmaster.
func (a *app) guide(ctx context.Context, cfg *config.Config, st store.API) error {
	g := &guide{a: a, cfg: cfg, st: st}
	yardr := yardrAt(cfg.Home)
	for {
		steps, err := setupSteps(ctx, st)
		if err != nil {
			return err
		}
		next := nextStep(steps)
		if next == nil {
			fmt.Println("\nThe setup is complete.")
			if err := g.offerLanded(ctx); err != nil {
				return err
			}
			return a.offerLink(ctx, cfg)
		}
		run := g.pages()[next.Key]
		if run == nil {
			return g.watch(ctx)
		}
		fmt.Printf("\nSetup: %s\n", next.Title)
		done, err := run(ctx, *next)
		switch {
		case errors.Is(err, huh.ErrUserAborted):
			fmt.Printf("\nCancelled at: %s. The yard is as the steps before it left it; `%s setup` goes on from here.\n", next.Title, yardr)
			return nil
		case err != nil:
			return err
		case !done:
			fmt.Printf("\nThe setup waits at: %s. `%s setup` goes on from here.\n", next.Title, yardr)
			return nil
		}
		// Read again: what the page did is what the store shows, not what
		// the page thinks it did.
		if steps, err = setupSteps(ctx, st); err != nil {
			return err
		}
		after := nextStep(steps)
		switch {
		case after == nil:
			continue
		case after.Key == next.Key:
			// Answers that leave the step undone (a builders' group without
			// the builder's role, say): going on would ask the same again.
			fmt.Printf("\nStill to do: %s: %s.\n  %s\n", after.Title, after.Detail, strings.ReplaceAll(after.Next, "yardr ", yardr+" "))
			return nil
		}
		pick, err := g.goOn(ctx, *after)
		switch {
		case errors.Is(err, huh.ErrUserAborted) || (err == nil && pick == goStop):
			fmt.Printf("\nStopped. Next is %s: `%s setup` goes on from here.\n", after.Title, yardr)
			return nil
		case err != nil:
			return err
		case pick == goYardmaster:
			return g.toYardmaster(ctx)
		case g.pages()[after.Key] == nil:
			return g.watch(ctx)
		}
	}
}

// goOn asks, after a page, how to go on with the step that is next.
func (g *guide) goOn(ctx context.Context, next setupStep) (string, error) {
	yardmaster := yardmasterOf(ctx, g.st)
	pick := goHere
	err := ask(huh.NewSelect[string]().Key("go").Title("Next: "+next.Title).Options(
		huh.NewOption("Keep going here", goHere),
		huh.NewOption("Talk to the yardmaster: "+yardmaster+" goes on from this step, in "+g.yardmasterAt(), goYardmaster),
		huh.NewOption("Stop here: yardr setup goes on from this step later", goStop),
	).Value(&pick))
	return pick, err
}

// yardmasterAt is where "talk to the yardmaster" leads: the chat, or its pane.
func (g *guide) yardmasterAt() string {
	if g.a.chats() {
		return "yardr chat"
	}
	return "its pane"
}

// toYardmaster opens the chat on the yardmaster, as the onboarding does once
// the yardmaster is up; with --no-chat it prints where the yardmaster is and
// brings its pane to the front. The yardmaster needs no word of what the
// forms did: its first run reads the same steps. A yardmaster that is not
// running is no one to talk to: it says what yardr doctor says of it, and
// opens no empty chat. One whose session runs without an answer is offered as
// its pane first.
func (g *guide) toYardmaster(ctx context.Context) error {
	yardr := yardrAt(g.cfg.Home)
	yardmaster := yardmasterOf(ctx, g.st)
	r, err := g.st.GetCrew(ctx, yardmaster)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	silent := err == nil && r.Live() && g.a.chats() && runner.Greeted(*r) && !up(ctx, *r)
	if silent {
		// Its harness may wait at something in its pane: the way there and
		// back, as after a start (offerPane).
		res := &startResult{said: true}
		res.describe(ctx, g.cfg, g.st, r, yardmaster, fmt.Sprintf("%s has not answered its first prompt, so there is nobody to talk to yet.", yardmaster))
		res.printProblem()
		if err := g.a.offerPane(ctx, g.cfg, g.st, yardmaster, yardmasterWait, res); err != nil {
			return err
		}
		if silent = !res.Running; !silent {
			if r, err = g.st.GetCrew(ctx, yardmaster); err != nil {
				return err
			}
		}
	}
	if err != nil || !r.Live() || silent {
		if err != nil || !r.Live() {
			fmt.Printf("\n%s is not running, so there is nobody to talk to yet.\n", yardmaster)
		}
		if err := g.a.doctorSays(ctx, g.cfg); err != nil {
			return err
		}
		fmt.Printf("\nThis starts the yard, waits for %s and opens the chat with it:\n\n  %s init --start\n", yardmaster, yardr)
		return nil
	}
	if g.a.chats() {
		if opened, err := g.a.toChat(ctx, g.cfg, g.st, yardmaster); err != nil || opened {
			return err
		}
	}
	all, _ := g.st.Settings(ctx)
	place := placeOf(*r, runner.YardName(g.cfg.Home, all))
	fmt.Println()
	place.print(yardmaster)
	fmt.Println()
	openYardmaster(ctx, yardmaster, place)
	fmt.Printf("%s goes on from the step that is next: it reads `%s doctor`. Write to it there.\n", yardmaster, yardr)
	return nil
}

// commandLine is a yardr command as a person types it for this home.
func commandLine(home string, args []string) string {
	words := make([]string, len(args))
	for i, a := range args {
		words[i] = shellQuote(a)
	}
	return yardrAt(home) + " " + strings.Join(words, " ")
}

// do shows a yardr command line, then runs it.
func (g *guide) do(ctx context.Context, args ...string) error {
	g.show(ctx, commandLine(g.cfg.Home, args))
	return g.a.sub(ctx, args...)
}

// show prints the command line a page is about to run, except to an operator
// who asked to be guided (operator_style guide): no raw commands unless
// asked for. The page's questions and what the command prints say what
// happens. It reads the setting each time, as the steps are read.
func (g *guide) show(ctx context.Context, line string) {
	if guided(ctx, g.st) {
		fmt.Println()
		return
	}
	fmt.Printf("\n  $ %s\n\n", line)
}

// sub runs a yardr command line in this process, as typed. The guided
// setup's pages end in the commands themselves, not in a second way of
// doing what they do, so a page cannot drift from the command it shows.
func (a *app) sub(ctx context.Context, args ...string) error {
	asJSON, actor, noChat := a.json, a.actor, a.noChat
	root := a.rootCmd() // binds its flags to a, which resets them
	a.json, a.actor, a.noChat = asJSON, actor, noChat
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// git runs git in dir and returns its trimmed output.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// notDepotName is what a directory's name may hold and a depot's may not.
var notDepotName = regexp.MustCompile(`[^a-z0-9._-]+`)

// depotNameFor is a depot name from a directory's: lowercase, with what a depot
// name cannot hold made a dash.
func depotNameFor(dir string) string {
	return strings.Trim(notDepotName.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-._")
}

// depotPage asks for the repository, the depot's name and its base branch, and
// adds the depot. The answers start at the directory yardr was run in, when
// that is a checkout's top, and the branch checked out there.
func (g *guide) depotPage(ctx context.Context, _ setupStep) (bool, error) {
	path := ""
	if wd, err := os.Getwd(); err == nil {
		if top, err := git(ctx, wd, "rev-parse", "--show-toplevel"); err == nil && top == resolve(wd) {
			path = wd
		}
	}
	if err := ask(huh.NewInput().Key("path").Title("Repository").
		Description("The checkout the work happens in: a depot. Every bead gets a branch of it and a worktree of its own.\n" +
			"A directory that is no git checkout becomes a dir depot, worked in as it is (docs/dir-depots.md).").
		Value(&path).Validate(func(s string) error {
		dir, err := absDir(strings.TrimSpace(s))
		if err != nil {
			return err
		}
		if info, err := os.Stat(dir); dir == "" || err != nil || !info.IsDir() {
			return errors.New("a directory that exists")
		}
		return nil
	})); err != nil {
		return false, err
	}
	path, err := absDir(strings.TrimSpace(path))
	if err != nil {
		return false, err
	}
	name := depotNameFor(path)
	fields := []huh.Field{huh.NewInput().Key("name").Title("Depot name").
		Description("How beads, groups and routes name it.").Value(&name).Validate(func(s string) error {
		if notDepotName.MatchString(s) || s == "" {
			return errors.New("lowercase letters, digits, '.', '_', '-'")
		}
		return nil
	})}
	isGit := detectDepotKind(path) == store.DepotGit
	base := ""
	if isGit {
		// The branch checked out is a local one, which an assembly can land
		// on; depot add's own default, origin/main, it cannot.
		if base, _ = git(ctx, path, "symbolic-ref", "--short", "-q", "HEAD"); base == "" {
			base = "origin/main"
		}
		fields = append(fields, huh.NewInput().Key("base").Title("Base branch").
			Description("Where a bead's branch starts and where approved work lands. yardr reads the depot's roles, gate and flow from it.\n"+
				"A local branch (main) is one yardr's assembly can land on; a remote's (origin/main) only people can.").
			Value(&base).Validate(func(s string) error {
			if _, err := git(ctx, path, "rev-parse", "--verify", "--quiet", s+"^{commit}"); err != nil {
				return fmt.Errorf("%q does not resolve in %s (a branch needs a commit)", s, path)
			}
			return nil
		}))
	}
	if err := ask(fields...); err != nil {
		return false, err
	}
	args := []string{"depot", "add", name, path}
	if isGit {
		args = append(args, "--base", base)
	}
	return true, g.do(ctx, args...)
}

// filesPage seeds the depot with yardr depot init, which says what it wrote and
// which gate it guessed, and asks before it commits the files on the base.
// It commits only where that is plain: the base is the branch checked out in
// the depot. Anywhere else the commit is the person's.
func (g *guide) filesPage(ctx context.Context, step setupStep) (bool, error) {
	depot, err := g.st.GetDepot(ctx, step.Depot)
	if err != nil {
		return false, err
	}
	if err := g.do(ctx, "depot", "init", depot.Name); err != nil {
		return false, err
	}
	if depot.IsDir() {
		return true, nil // read from the directory as they are
	}
	yardr := yardrAt(g.cfg.Home)
	byHand := commitCommand(depot.Path)
	if head, _ := git(ctx, depot.Path, "symbolic-ref", "--short", "-q", "HEAD"); head != depot.Base {
		fmt.Printf("\nThe base, %s, is not the branch checked out in %s (%s), so this form does not commit there.\n"+
			"Get .yardr/ onto %s yourself (commit it there; push it, when the base is a remote's branch).\n"+
			"yardr reads the files from the base only, so the setup waits for that.\n",
			depot.Base, depot.Path, or(head, "no branch"), depot.Base)
		return false, nil
	}
	commit := true
	fmt.Println()
	if err := ask(huh.NewConfirm().Key("commit").Title("Commit these files on " + depot.Base + "?").
		Description(fmt.Sprintf("yardr reads the roles, the gate and the flow from %s, not from the files in the checkout:\n"+
			"they take effect once they are committed there. Only .yardr/ is committed; other changes\n"+
			"in the checkout stay as they are.\n\n"+
			"No leaves the files where they are, uncommitted, and the setup waits. Read them, edit the\n"+
			"gate (it is a guess), commit them yourself, and `%s setup` goes on from there.", depot.Base, yardr)).
		Value(&commit)); err != nil {
		return false, err
	}
	if !commit {
		fmt.Printf("Not committed. The files are in %s/.yardr; to commit them:\n\n  %s\n", depot.Path, byHand)
		return false, nil
	}
	g.show(ctx, byHand)
	for _, args := range [][]string{{"add", ".yardr"}, {"commit", "-m", starterCommit, "--", ".yardr"}} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", depot.Path}, args...)...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return false, fmt.Errorf("git %s in %s: %w", args[0], depot.Path, err)
		}
	}
	return true, nil
}

// groupPage is the page of a group of agents with a role: the group form
// (askGroup), starting at <depot>/<role>s on the yard's default harness with
// its unattended flags and the role's prompt, and ending in yardr group add.
func (g *guide) groupPage(role string, limit int, what string) page {
	return func(ctx context.Context, step setupStep) (bool, error) {
		groups, err := g.st.Groups(ctx)
		if err != nil {
			return false, err
		}
		kind := or(defaultKind(ctx, g.st), "claude")
		ans := groupAnswers{Name: step.Depot + "/" + role + "s", Runner: "herdr", Kind: kind, PermArgs: agents.UnattendedArgs(kind),
			Prompt: "roles/" + role + ".md", Limit: strconv.Itoa(limit), Other: map[string]any{}}
		fmt.Printf("%s\nThe form starts at a group on %s with the prompt %s (the depot's own\n"+
			".yardr/roles/%s.md), which is what makes the group the %ss, and with the flags that keep\n"+
			"%s from stopping to ask, where it has them. Change what you like.\n\n", what, kind, ans.Prompt, role, role, kind)
		note := func(grp store.Group) string {
			return "As a command:\n" + commandLine(g.cfg.Home, groupArgs(grp))
		}
		if guided(ctx, g.st) {
			note = nil // the summary above it says the same in words
		}
		grp, err := g.a.askGroup(ctx, g.st, groups, ans, false, note)
		if err != nil || grp == nil {
			return false, err
		}
		return true, g.do(ctx, groupArgs(*grp)...)
	}
}

// groupArgs is the yardr group add command line that stores g.
func groupArgs(g store.Group) []string {
	args := []string{"group", "add", g.Name, "--runner", g.Runner}
	if g.Limit > 1 {
		args = append(args, "--limit", strconv.Itoa(g.Limit))
	}
	if len(g.Members) > 0 {
		args = append(args, "--members", strings.Join(g.Members, ","))
	}
	keys := make([]string, 0, len(g.Config))
	for k := range g.Config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "--set", k+"="+setValue(g.Config[k]))
	}
	return args
}

// setValue is v as --set reads it back (parseSets): a string as it is,
// unless it would read as JSON of another type ("3", "true"), and anything
// else as JSON.
func setValue(v any) string {
	if s, ok := v.(string); ok {
		var js any
		if json.Unmarshal([]byte(s), &js) != nil {
			return s
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// How approved beads land.
const (
	landAssembly = "assembly"
	landByHand   = "hand"
)

// landingPage asks how approved beads land. An assembly is one answer: the
// page installs merge-local, which this build carries, into the home's
// scripts and adds the exec group that runs it. By hand is a manual group
// of the people who merge, and their route for approved with it: a group of
// people shows as landing only once it is routed (see doing).
func (g *guide) landingPage(ctx context.Context, step setupStep) (bool, error) {
	depot, err := g.st.GetDepot(ctx, step.Depot)
	if err != nil {
		return false, err
	}
	how := landAssembly
	desc := fmt.Sprintf("An assembly is yardr's merge queue: for each approved bead it rebases the branch, runs the depot's gate\n"+
		"on exactly what will land and fast-forwards %s, one bead at a time. It is the script merge-local,\n"+
		"which this installs in %s (docs/pipeline.md, section 4).\n"+
		"By hand, approved beads wait for the people of a group, who merge them and move them on.",
		depot.Base, filepath.Join(g.cfg.Home, "scripts"))
	// merge-local lands on local branches only, and holds every bead of a
	// depot whose base is a remote's.
	if _, err := git(ctx, depot.Path, "show-ref", "--verify", "--quiet", "refs/heads/"+depot.Base); err != nil {
		how = landByHand
		desc += fmt.Sprintf("\n\nThe base of %s, %s, is not a local branch: the assembly lands on local branches only,\n"+
			"and would hold every bead.", depot.Name, depot.Base)
	}
	if err := ask(huh.NewSelect[string]().Key("landing").Title("How do approved beads land?").Description(desc).Options(
		huh.NewOption("An assembly: yardr lands them, once the gate passes", landAssembly),
		huh.NewOption("By hand: people merge them", landByHand),
	).Value(&how)); err != nil {
		return false, err
	}
	if how == landAssembly {
		script, err := g.installMergeLocal()
		if err != nil {
			return false, err
		}
		logs := filepath.Join(g.cfg.Home, "logs")
		if err := os.MkdirAll(logs, 0o755); err != nil {
			return false, err
		}
		if err := g.do(ctx, "group", "add", depot.Name+"/assembly", "--runner", "exec",
			"--set", "cmd="+shellQuote(script), "--set", "log="+filepath.Join(logs, "assembly.log")); err != nil {
			return false, err
		}
		fmt.Printf("It lands on %s by fast-forward and pushes nothing; YARDR_PUSH=origin in front of the cmd makes it push\n"+
			"the base after each landing (contrib/assembly/README.md). A checkout of %s with uncommitted changes holds\n"+
			"the bead rather than touch them.\n", depot.Base, depot.Base)
		return true, nil
	}
	members := g.a.who()
	if err := ask(huh.NewInput().Key("members").Title("Who merges?").
		Description("Comma-separated, as yardr knows them: $USER, or what YARDR_ACTOR says. Only they may move a bead out of approved.").
		Value(&members).Validate(nonEmpty)); err != nil {
		return false, err
	}
	group := depot.Name + "/mergers"
	if err := g.do(ctx, "group", "add", group, "--runner", "manual", "--members", strings.Join(splitMembers(members), ",")); err != nil {
		return false, err
	}
	if err := g.do(ctx, "route", "add", "--stage", "approved", "--depot", depot.Name, "--group", group); err != nil {
		return false, err
	}
	fmt.Printf("An approved bead now waits for them: they merge its branch, yard/<bead>, and report it with\n"+
		"`%s bead done <bead>`. `%s review` goes through what waits for a person.\n", yardrAt(g.cfg.Home), yardrAt(g.cfg.Home))
	return true, nil
}

// installMergeLocal writes this build's merge-local into the home's scripts
// directory and returns its path. One already there is kept as it is, this
// build's or not: it may be the yard's own, and a script a group runs is not
// replaced under it.
func (g *guide) installMergeLocal() (string, error) {
	path := filepath.Join(g.cfg.Home, "scripts", "merge-local")
	have, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if _, err := writeNew(path, string(assembly.MergeLocal), 0o755); err != nil {
			return "", err
		}
		fmt.Printf("\nmerge-local installed: %s (this build's contrib/assembly/merge-local)\n", path)
	case err != nil:
		return "", err
	case bytes.Equal(have, assembly.MergeLocal):
		fmt.Printf("\nmerge-local is installed already: %s\n", path)
	default:
		fmt.Printf("\nmerge-local is there already and differs from this build's: %s is kept as it is\n", path)
	}
	return path, nil
}

// routesPage asks nothing but a yes: the routes follow from the groups the
// yard has (the step's args), and are shown as the commands they are.
func (g *guide) routesPage(ctx context.Context, step setupStep) (bool, error) {
	if len(step.args) == 0 {
		return true, nil // no group to route to: the caller says what is left
	}
	lines := make([]string, len(step.args))
	for i, args := range step.args {
		lines[i] = commandLine(g.cfg.Home, args)
		if guided(ctx, g.st) {
			lines[i] = routeWords(args)
		}
	}
	add := true
	if err := ask(huh.NewConfirm().Key("routes").Title("Add these routes?").
		Description("A route sends the open beads of a stage to a group. These follow from the groups the yard has:\n\n" +
			strings.Join(lines, "\n")).Value(&add)); err != nil {
		return false, err
	}
	if !add {
		return false, nil
	}
	for _, args := range step.args {
		if err := g.do(ctx, args...); err != nil {
			return false, err
		}
	}
	return true, nil
}

// routeWords is a yardr route add command line in words, for an operator who
// asked for no raw commands: "stage new of web goes to web/builders".
func routeWords(args []string) string {
	flag := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if name, ok := strings.CutPrefix(args[i], "--"); ok {
			flag[name] = args[i+1]
		}
	}
	depot := ""
	if flag["depot"] != "" {
		depot = " of " + flag["depot"]
	}
	return fmt.Sprintf("stage %s%s goes to %s", flag["stage"], depot, flag["group"])
}

// beadPage asks for a first bead's title and goal, creates it and advances
// it to new, where the builders' route picks it up.
func (g *guide) beadPage(ctx context.Context, step setupStep) (bool, error) {
	var title, goal string
	if err := ask(
		huh.NewInput().Key("title").Title("A first bead").
			Description(fmt.Sprintf("Something small in %s to try the pipeline on, end to end: a builder implements it,\n"+
				"a reviewer checks it, and it lands.", step.Depot)).
			Placeholder("Say what the project is in the README's first paragraph").Value(&title).Validate(nonEmpty),
		huh.NewText().Key("goal").Title("Goal").
			Description("What is true when it is done, and how to tell. The builder gets this and nothing else.").
			Value(&goal).Validate(nonEmpty),
	); err != nil {
		return false, err
	}
	title, goal = strings.TrimSpace(title), strings.TrimSpace(goal)
	before, err := g.st.ListBeads(ctx, store.BeadFilter{All: true, Depot: step.Depot})
	if err != nil {
		return false, err
	}
	if err := g.do(ctx, "bead", "create", title, "--depot", step.Depot, "-b", goal); err != nil {
		return false, err
	}
	after, err := g.st.ListBeads(ctx, store.BeadFilter{All: true, Depot: step.Depot})
	if err != nil {
		return false, err
	}
	for _, b := range after {
		if !slices.ContainsFunc(before, func(o store.Bead) bool { return o.ID == b.ID }) {
			// The flow's first stage is backlog, where a bead waits to be
			// shaped; this one is.
			return true, g.do(ctx, "bead", "advance", b.ID, "--to", "new")
		}
	}
	return false, fmt.Errorf("yardr bead create printed no error, and depot %s has no new bead", step.Depot)
}

// watch is where the forms end: every step but the last is done, and the
// last, a first merged bead, is the pipeline's. It says where the first
// bead is and how to watch it.
func (g *guide) watch(ctx context.Context) error {
	yardr := yardrAt(g.cfg.Home)
	beads, err := g.st.ListBeads(ctx, store.BeadFilter{All: true})
	if err != nil {
		return err
	}
	id := "<id>"
	if i := slices.IndexFunc(beads, work); i >= 0 {
		b := beads[i]
		id = b.ID
		fmt.Printf("\nThe forms end here. The first bead, %s (%s), is in stage %s.", b.ID, b.Title, b.Stage)
		if b.Hold {
			fmt.Printf(" It is held: `%s bead show %s` says why.", yardr, b.ID)
		}
		fmt.Println()
	}
	if !daemonRunning(g.cfg.DaemonLock()) {
		fmt.Printf("No daemon is running, so nothing picks it up yet: `%s init --start` starts one in the background.\n", yardr)
	}
	web := "serves the yard in a browser while it runs, at http://127.0.0.1:8791/"
	if webService(g.cfg) {
		web = "runs as a login service already: the yard in a browser" + at(webServiceAddr(g.cfg))
	}
	fmt.Printf("\nTo watch it:\n\n"+
		"  %s session list       # who works on what now\n"+
		"  %s bead show %s   # where it is, with the notes of whoever worked it\n"+
		"  %s watch              # every event as it happens\n"+
		"  %s web serve          # %s\n\n"+
		"The setup is over once it has landed: `%s doctor` then says so.\n", yardr, yardr, id, yardr, yardr, web, yardr)
	if err := g.offerLanded(ctx); err != nil {
		return err
	}
	return g.a.offerLink(ctx, g.cfg)
}

// offerLanded asks, where the forms end, whether the person wants to be
// told when beads land: a subscription of their own to the beads of the
// setup's depot becoming done, which the yard then writes into their inbox
// as notifications. It is asked of a user, since a subscription is the
// user's own to make, and not of one who is told of done beads already. A
// subscribe that fails says so and the setup goes on.
func (g *guide) offerLanded(ctx context.Context) error {
	u, err := g.a.actingUser(ctx, g.st)
	if err != nil || u == nil {
		return err
	}
	if slices.ContainsFunc(u.Subscriptions, func(s store.Subscription) bool {
		return s.Source == store.SourceYardr && s.Where["state"] == store.StateDone
	}) {
		return nil
	}
	depots, err := g.st.Depots(ctx)
	if err != nil || len(depots) == 0 {
		return err
	}
	args := []string{"subscribe", u.Name, store.SourceYardr, "--done", "--where", "depot=" + depots[0].Name}
	line, told := commandLine(g.cfg.Home, args), false
	if err := ask(huh.NewConfirm().Key("landed").Title("Be told when beads land?").
		Description(fmt.Sprintf("Yes subscribes you to the beads of %s that are done: the yard then writes a line for each\n"+
			"into your inbox, as a notification, apart from your mail (%s inbox).\n"+
			"Later: %s", depots[0].Name, yardrAt(g.cfg.Home), line)).Value(&told)); err != nil || !told {
		return err
	}
	if err := g.do(ctx, args...); err != nil {
		fmt.Printf("yardr: %v\nThe yard runs without it; `%s` tries again.\n", err, line)
	}
	return nil
}
