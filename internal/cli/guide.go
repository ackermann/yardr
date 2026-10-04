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

	"yardr.dev/yardr/contrib/refinery"
	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// The guided setup (yardr setup) takes the setup steps one at a time, each
// as a form page that ends in the command a person would type for it. It
// keeps no state of its own: before every page it reads the steps from the
// store (setupSteps) and takes the first one not done, so it goes on where
// the yard stands, whoever did the earlier steps: this form, the mayor in
// its pane, or a person on the command line.

// setupReport is what yardr setup prints where it runs no form.
type setupReport struct {
	Home  string      `json:"home"`
	Steps []setupStep `json:"steps"`
	// Next is the first step not done; absent when all are.
	Next *setupStep `json:"next,omitempty"`
}

func (a *app) setupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Go on with the yard's setup where it stands: the next step not done, as a form",
		Long: `Print the setup steps, each done or todo, and take the first one not done as
a form. The steps are the ones yardr doctor lists, read from the store: the
setup goes on where the yard stands, whoever did the earlier steps (this
form, the mayor in its pane, a person on the command line), and can be run
again at any time.

Every page ends in a command that exists, and shows its command line before
it runs:

  a rig                 the repository and its base branch: yardr rig add
  the rig's files       yardr rig init, which writes starter roles, a gate
                        and a flow under .yardr/; then it asks before it
                        commits them on the base branch
  builders, reviewers   the form of yardr group add, starting at a group on
                        the yard's default harness with the role's prompt
  landing               a refinery (yardr's merge-local, installed in
                        <home>/scripts/, as an exec group) or people who
                        merge by hand (a manual group, routed for approved)
  routes                new, review and approved, each to the group that
                        does its work: shown, confirmed, yardr route add
  a first bead          its title and goal: yardr bead create, then
                        yardr bead advance --to new; and how to watch it

After each page it asks how to go on: keep going here, talk to the mayor
(prints where the mayor is and brings its pane to the front; the mayor goes
on from the same steps), or stop here. A page that is cancelled (ctrl+c, or
no at its last question) has run no command: the yard is as the finished
pages made it. The one exception is the rig's files, which yardr rig init
has written into the checkout before the question whether to commit them;
they stay there, and rig init never overwrites a file.

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
}

// agentSession reports whether this run is an agent's: a session yardr
// started, or a resident. An agent's pane is a terminal too, and a form
// would sit there unanswered.
func agentSession() bool {
	return config.Getenv("SESSION") != "" || config.Getenv("RESIDENT") != ""
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
	fmt.Printf("Next step: %s.\n  %s\n", next.Title, strings.ReplaceAll(next.Next, "yardr ", homeEnv(home)+"yardr "))
}

// How to go on after a page.
const (
	goHere  = "here"
	goMayor = "mayor"
	goStop  = "stop"
)

// handOff is the onboarding's last question, once the mayor is up: talk to
// the mayor, or keep going in this terminal. Both go by the same steps, so
// the answer is not final: the forms offer the mayor again after each page,
// and the mayor reads where the forms left the yard.
func (a *app) handOff(ctx context.Context, cfg *config.Config, mayor string, place *mayorPlace) error {
	st, err := a.store(ctx)
	if err != nil {
		return err
	}
	steps, err := setupSteps(ctx, st)
	if err != nil {
		return err
	}
	a.guided = true
	pick := goMayor
	opts := []huh.Option[string]{huh.NewOption("Talk to the mayor: brings "+mayor+"'s pane to the front in herdr", goMayor)}
	if next := nextStep(steps); next != nil {
		opts = append(opts, huh.NewOption("Keep going here: a short run of forms, next "+next.Title, goHere))
	}
	fmt.Println()
	err = ask(huh.NewSelect[string]().Key("go").Title("The rest of the setup").
		Description(fmt.Sprintf("A repository to work in, who builds and reviews, how work lands, a first bead.\n"+
			"%s does it with you as a conversation, or this terminal as forms. Both go by the\n"+
			"same steps (yardr doctor lists them), so you can change over after any of them.", mayor)).
		Options(opts...).Value(&pick))
	switch {
	case errors.Is(err, huh.ErrUserAborted):
		return nil
	case err != nil:
		return err
	case pick == goHere:
		return a.guide(ctx, cfg, st)
	}
	openMayor(ctx, mayor, place)
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
		stepRig:       g.rigPage,
		stepRigFiles:  g.filesPage,
		stepBuilders:  g.groupPage("builder", 2, "Builders implement the beads in stage new, each in a worktree of its own."),
		stepReviewers: g.groupPage("reviewer", 1, "Reviewers check what the builders report done, in stage review."),
		stepLanding:   g.landingPage,
		stepRoutes:    g.routesPage,
		stepFirstBead: g.beadPage,
	}
}

// guide takes the steps not done one page at a time, asking after each how
// to go on, until none is left, a page is left undone, or the person stops
// or goes to the mayor.
func (a *app) guide(ctx context.Context, cfg *config.Config, st store.API) error {
	g := &guide{a: a, cfg: cfg, st: st}
	env := homeEnv(cfg.Home)
	for {
		steps, err := setupSteps(ctx, st)
		if err != nil {
			return err
		}
		next := nextStep(steps)
		if next == nil {
			fmt.Println("\nThe setup is complete.")
			return nil
		}
		run := g.pages()[next.Key]
		if run == nil {
			return g.watch(ctx)
		}
		fmt.Printf("\nSetup: %s\n", next.Title)
		done, err := run(ctx, *next)
		switch {
		case errors.Is(err, huh.ErrUserAborted):
			fmt.Printf("\nCancelled at: %s. The yard is as the steps before it left it; `%syardr setup` goes on from here.\n", next.Title, env)
			return nil
		case err != nil:
			return err
		case !done:
			fmt.Printf("\nThe setup waits at: %s. `%syardr setup` goes on from here.\n", next.Title, env)
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
			fmt.Printf("\nStill to do: %s: %s.\n  %s\n", after.Title, after.Detail, strings.ReplaceAll(after.Next, "yardr ", env+"yardr "))
			return nil
		}
		pick, err := g.goOn(ctx, *after)
		switch {
		case errors.Is(err, huh.ErrUserAborted) || (err == nil && pick == goStop):
			fmt.Printf("\nStopped. Next is %s: `%syardr setup` goes on from here.\n", after.Title, env)
			return nil
		case err != nil:
			return err
		case pick == goMayor:
			return g.toMayor(ctx)
		case g.pages()[after.Key] == nil:
			return g.watch(ctx)
		}
	}
}

// goOn asks, after a page, how to go on with the step that is next.
func (g *guide) goOn(ctx context.Context, next setupStep) (string, error) {
	mayor := yardMayor(ctx, g.st)
	pick := goHere
	err := ask(huh.NewSelect[string]().Key("go").Title("Next: "+next.Title).Options(
		huh.NewOption("Keep going here", goHere),
		huh.NewOption("Talk to the mayor: "+mayor+" goes on from this step, in its pane", goMayor),
		huh.NewOption("Stop here: yardr setup goes on from this step later", goStop),
	).Value(&pick))
	return pick, err
}

// toMayor prints where the mayor is and brings its pane to the front, as
// the onboarding does once the mayor is up. The mayor needs no word of what
// the forms did: its first run reads the same steps.
func (g *guide) toMayor(ctx context.Context) error {
	env := homeEnv(g.cfg.Home)
	mayor := yardMayor(ctx, g.st)
	r, err := g.st.GetResident(ctx, mayor)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err != nil || !r.Live() {
		fmt.Printf("\n%s is not running. This starts the yard, waits for it and prints where it is:\n\n  %syardr init --start\n", mayor, env)
		return nil
	}
	all, _ := g.st.Settings(ctx)
	place := placeOf(*r, runner.YardName(g.cfg.Home, all))
	fmt.Println()
	place.print(mayor)
	fmt.Println()
	openMayor(ctx, mayor, place)
	fmt.Printf("%s goes on from the step that is next: it reads `%syardr doctor`. Write to it there.\n", mayor, env)
	return nil
}

// commandLine is a yardr command as a person types it for this home.
func commandLine(home string, args []string) string {
	words := make([]string, len(args))
	for i, a := range args {
		words[i] = shellQuote(a)
	}
	return homeEnv(home) + "yardr " + strings.Join(words, " ")
}

// do shows a yardr command line, then runs it.
func (g *guide) do(ctx context.Context, args ...string) error {
	fmt.Printf("\n  $ %s\n\n", commandLine(g.cfg.Home, args))
	return g.a.sub(ctx, args...)
}

// sub runs a yardr command line in this process, as typed. The guided
// setup's pages end in the commands themselves, not in a second way of
// doing what they do, so a page cannot drift from the command it shows.
func (a *app) sub(ctx context.Context, args ...string) error {
	asJSON, actor := a.json, a.actor
	root := a.rootCmd() // binds its flags to a, which resets them
	a.json, a.actor = asJSON, actor
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// git runs git in dir and returns its trimmed output.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// notRigName is what a directory's name may hold and a rig's may not.
var notRigName = regexp.MustCompile(`[^a-z0-9._-]+`)

// rigNameFor is a rig name from a directory's: lowercase, with what a rig
// name cannot hold made a dash.
func rigNameFor(dir string) string {
	return strings.Trim(notRigName.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-._")
}

// rigPage asks for the repository, the rig's name and its base branch, and
// adds the rig. The answers start at the directory yardr was run in, when
// that is a checkout's top, and the branch checked out there.
func (g *guide) rigPage(ctx context.Context, _ setupStep) (bool, error) {
	path := ""
	if wd, err := os.Getwd(); err == nil {
		if top, err := git(ctx, wd, "rev-parse", "--show-toplevel"); err == nil && top == resolve(wd) {
			path = wd
		}
	}
	if err := ask(huh.NewInput().Key("path").Title("Repository").
		Description("The checkout the work happens in: a rig. Every bead gets a branch of it and a worktree of its own.\n" +
			"A directory that is no git checkout becomes a dir rig, worked in as it is (docs/dir-rigs.md).").
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
	name := rigNameFor(path)
	fields := []huh.Field{huh.NewInput().Key("name").Title("Rig name").
		Description("How beads, groups and routes name it.").Value(&name).Validate(func(s string) error {
		if notRigName.MatchString(s) || s == "" {
			return errors.New("lowercase letters, digits, '.', '_', '-'")
		}
		return nil
	})}
	isGit := detectRigKind(path) == store.RigGit
	base := ""
	if isGit {
		// The branch checked out is a local one, which a refinery can land
		// on; rig add's own default, origin/main, it cannot.
		if base, _ = git(ctx, path, "symbolic-ref", "--short", "-q", "HEAD"); base == "" {
			base = "origin/main"
		}
		fields = append(fields, huh.NewInput().Key("base").Title("Base branch").
			Description("Where a bead's branch starts and where approved work lands. yardr reads the rig's roles, gate and flow from it.\n"+
				"A local branch (main) is one yardr's refinery can land on; a remote's (origin/main) only people can.").
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
	args := []string{"rig", "add", name, path}
	if isGit {
		args = append(args, "--base", base)
	}
	return true, g.do(ctx, args...)
}

// filesPage seeds the rig with yardr rig init, which says what it wrote and
// which gate it guessed, and asks before it commits the files on the base.
// It commits only where that is plain: the base is the branch checked out in
// the rig. Anywhere else the commit is the person's.
func (g *guide) filesPage(ctx context.Context, step setupStep) (bool, error) {
	rig, err := g.st.GetRig(ctx, step.Rig)
	if err != nil {
		return false, err
	}
	if err := g.do(ctx, "rig", "init", rig.Name); err != nil {
		return false, err
	}
	if rig.IsDir() {
		return true, nil // read from the directory as they are
	}
	env := homeEnv(g.cfg.Home)
	byHand := commitCommand(rig.Path)
	if head, _ := git(ctx, rig.Path, "symbolic-ref", "--short", "-q", "HEAD"); head != rig.Base {
		fmt.Printf("\nThe base, %s, is not the branch checked out in %s (%s), so this form does not commit there.\n"+
			"Get .yardr/ onto %s yourself (commit it there; push it, when the base is a remote's branch).\n"+
			"yardr reads the files from the base only, so the setup waits for that.\n",
			rig.Base, rig.Path, or(head, "no branch"), rig.Base)
		return false, nil
	}
	commit := true
	fmt.Println()
	if err := ask(huh.NewConfirm().Key("commit").Title("Commit these files on " + rig.Base + "?").
		Description(fmt.Sprintf("yardr reads the roles, the gate and the flow from %s, not from the files in the checkout:\n"+
			"they take effect once they are committed there. Only .yardr/ is committed; other changes\n"+
			"in the checkout stay as they are.\n\n"+
			"No leaves the files where they are, uncommitted, and the setup waits. Read them, edit the\n"+
			"gate (it is a guess), commit them yourself, and `%syardr setup` goes on from there.", rig.Base, env)).
		Value(&commit)); err != nil {
		return false, err
	}
	if !commit {
		fmt.Printf("Not committed. The files are in %s/.yardr; to commit them:\n\n  %s\n", rig.Path, byHand)
		return false, nil
	}
	fmt.Printf("\n  $ %s\n\n", byHand)
	for _, args := range [][]string{{"add", ".yardr"}, {"commit", "-m", starterCommit, "--", ".yardr"}} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", rig.Path}, args...)...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return false, fmt.Errorf("git %s in %s: %w", args[0], rig.Path, err)
		}
	}
	return true, nil
}

// groupPage is the page of a group of agents with a role: the group form
// (askGroup), starting at <rig>/<role>s on the yard's default harness with
// its unattended flags and the role's prompt, and ending in yardr group add.
func (g *guide) groupPage(role string, limit int, what string) page {
	return func(ctx context.Context, step setupStep) (bool, error) {
		groups, err := g.st.Groups(ctx)
		if err != nil {
			return false, err
		}
		kind := or(defaultKind(ctx, g.st), "claude")
		ans := groupAnswers{Name: step.Rig + "/" + role + "s", Runner: "herdr", Kind: kind, PermArgs: agents.UnattendedArgs(kind),
			Prompt: "roles/" + role + ".md", Limit: strconv.Itoa(limit), Other: map[string]any{}}
		fmt.Printf("%s\nThe form starts at a group on %s with the prompt %s (the rig's own\n"+
			".yardr/roles/%s.md), which is what makes the group the %ss, and with the flags that keep\n"+
			"%s from stopping to ask, where it has them. Change what you like.\n\n", what, kind, ans.Prompt, role, role, kind)
		grp, err := g.a.askGroup(ctx, g.st, groups, ans, false, func(grp store.Group) string {
			return "As a command:\n" + commandLine(g.cfg.Home, groupArgs(grp))
		})
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
	landRefinery = "refinery"
	landByHand   = "hand"
)

// landingPage asks how approved beads land. A refinery is one answer: the
// page installs merge-local, which this build carries, into the home's
// scripts and adds the exec group that runs it. By hand is a manual group
// of the people who merge, and their route for approved with it: a group of
// people shows as landing only once it is routed (see doing).
func (g *guide) landingPage(ctx context.Context, step setupStep) (bool, error) {
	rig, err := g.st.GetRig(ctx, step.Rig)
	if err != nil {
		return false, err
	}
	how := landRefinery
	desc := fmt.Sprintf("A refinery is yardr's merge queue: for each approved bead it rebases the branch, runs the rig's gate\n"+
		"on exactly what will land and fast-forwards %s, one bead at a time. It is the script merge-local,\n"+
		"which this installs in %s (docs/pipeline.md, section 4).\n"+
		"By hand, approved beads wait for the people of a group, who merge them and move them on.",
		rig.Base, filepath.Join(g.cfg.Home, "scripts"))
	// merge-local lands on local branches only, and holds every bead of a
	// rig whose base is a remote's.
	if _, err := git(ctx, rig.Path, "show-ref", "--verify", "--quiet", "refs/heads/"+rig.Base); err != nil {
		how = landByHand
		desc += fmt.Sprintf("\n\nThe base of %s, %s, is not a local branch: the refinery lands on local branches only,\n"+
			"and would hold every bead.", rig.Name, rig.Base)
	}
	if err := ask(huh.NewSelect[string]().Key("landing").Title("How do approved beads land?").Description(desc).Options(
		huh.NewOption("A refinery: yardr lands them, once the gate passes", landRefinery),
		huh.NewOption("By hand: people merge them", landByHand),
	).Value(&how)); err != nil {
		return false, err
	}
	if how == landRefinery {
		script, err := g.installMergeLocal()
		if err != nil {
			return false, err
		}
		logs := filepath.Join(g.cfg.Home, "logs")
		if err := os.MkdirAll(logs, 0o755); err != nil {
			return false, err
		}
		if err := g.do(ctx, "group", "add", rig.Name+"/refinery", "--runner", "exec",
			"--set", "cmd="+shellQuote(script), "--set", "log="+filepath.Join(logs, "refinery.log")); err != nil {
			return false, err
		}
		fmt.Printf("It lands on %s by fast-forward and pushes nothing; YARDR_PUSH=origin in front of the cmd makes it push\n"+
			"the base after each landing (contrib/refinery/README.md). A checkout of %s with uncommitted changes holds\n"+
			"the bead rather than touch them.\n", rig.Base, rig.Base)
		return true, nil
	}
	members := g.a.who()
	if err := ask(huh.NewInput().Key("members").Title("Who merges?").
		Description("Comma-separated, as yardr knows them: $USER, or what YARDR_ACTOR says. Only they may move a bead out of approved.").
		Value(&members).Validate(nonEmpty)); err != nil {
		return false, err
	}
	group := rig.Name + "/mergers"
	if err := g.do(ctx, "group", "add", group, "--runner", "manual", "--members", strings.Join(splitMembers(members), ",")); err != nil {
		return false, err
	}
	if err := g.do(ctx, "route", "add", "--stage", "approved", "--rig", rig.Name, "--group", group); err != nil {
		return false, err
	}
	fmt.Printf("An approved bead now waits for them: they merge its branch, yard/<bead>, and report it with\n"+
		"`%syardr bead done <bead>`. `%syardr review` goes through what waits for a person.\n", homeEnv(g.cfg.Home), homeEnv(g.cfg.Home))
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
		if _, err := writeNew(path, string(refinery.MergeLocal), 0o755); err != nil {
			return "", err
		}
		fmt.Printf("\nmerge-local installed: %s (this build's contrib/refinery/merge-local)\n", path)
	case err != nil:
		return "", err
	case bytes.Equal(have, refinery.MergeLocal):
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

// beadPage asks for a first bead's title and goal, creates it and advances
// it to new, where the builders' route picks it up.
func (g *guide) beadPage(ctx context.Context, step setupStep) (bool, error) {
	var title, goal string
	if err := ask(
		huh.NewInput().Key("title").Title("A first bead").
			Description(fmt.Sprintf("Something small in %s to try the pipeline on, end to end: a builder implements it,\n"+
				"a reviewer checks it, and it lands.", step.Rig)).
			Placeholder("Say what the project is in the README's first paragraph").Value(&title).Validate(nonEmpty),
		huh.NewText().Key("goal").Title("Goal").
			Description("What is true when it is done, and how to tell. The builder gets this and nothing else.").
			Value(&goal).Validate(nonEmpty),
	); err != nil {
		return false, err
	}
	title, goal = strings.TrimSpace(title), strings.TrimSpace(goal)
	before, err := g.st.ListBeads(ctx, store.BeadFilter{All: true, Rig: step.Rig})
	if err != nil {
		return false, err
	}
	if err := g.do(ctx, "bead", "create", title, "--rig", step.Rig, "-b", goal); err != nil {
		return false, err
	}
	after, err := g.st.ListBeads(ctx, store.BeadFilter{All: true, Rig: step.Rig})
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
	return false, fmt.Errorf("yardr bead create printed no error, and rig %s has no new bead", step.Rig)
}

// watch is where the forms end: every step but the last is done, and the
// last, a first merged bead, is the pipeline's. It says where the first
// bead is and how to watch it.
func (g *guide) watch(ctx context.Context) error {
	env := homeEnv(g.cfg.Home)
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
			fmt.Printf(" It is held: `%syardr bead show %s` says why.", env, b.ID)
		}
		fmt.Println()
	}
	if !daemonRunning(g.cfg.DaemonLock()) {
		fmt.Printf("No daemon is running, so nothing picks it up yet: `%syardr init --start` starts one in the background.\n", env)
	}
	web := "serves the yard in a browser while it runs, at http://127.0.0.1:8791/"
	if webService(g.cfg) {
		web = "is kept running: the yard in a browser"
	}
	fmt.Printf("\nTo watch it:\n\n"+
		"  %syardr session list       # who works on what now\n"+
		"  %syardr bead show %s   # where it is, with the notes of whoever worked it\n"+
		"  %syardr watch              # every event as it happens\n"+
		"  %syardr web serve          # %s\n\n"+
		"The setup is over once it has landed: `%syardr doctor` then says so.\n", env, env, id, env, env, web, env)
	return nil
}
