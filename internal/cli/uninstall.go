package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// yardr uninstall takes a yard off the machine it runs on, and is no second
// way to do any of it: it is yardr down, then the removal of the yard's
// login services as yardr <kind> uninstall and yardr down --login remove
// them (downLogin). What it adds is the list of what it did not touch, so
// that someone who leaves does not hunt for it, and --purge, the one thing
// here that deletes a person's work: the yard's home.

// purgeAsk is --purge given no name: the name is asked for. No yard is
// named so (store.NameSetting takes no '?').
const purgeAsk = "?"

// nothingStopped is the sentence a refusal of --purge ends with: it came
// before anything was stopped or deleted. It is a sentence of its own, passed
// through %s, so the error strings that carry it do not end in a period.
const nothingStopped = "Nothing was stopped or deleted."

// purgeFlag is --purge: whether it was given, and the name it was given.
type purgeFlag struct {
	set  bool
	name string
}

func (p *purgeFlag) String() string { return p.name }
func (p *purgeFlag) Type() string   { return "name" }
func (p *purgeFlag) Set(v string) error {
	p.set, p.name = true, v
	return nil
}

// The kinds of thing uninstall leaves, as leftThing names them.
const (
	leftHome     = "home"
	leftDepot    = "depot"
	leftYardrDir = "yardr_dir"
	leftWorktree = "worktree"
	leftBranches = "branches"
	leftService  = "login_service"
	leftYardr    = "yardr"
	leftHerdr    = "herdr"
	leftHarness  = "harness"
)

// leftThing is one thing uninstall did not touch: what it is, where it is,
// and how the person removes it, where that is theirs to do.
type leftThing struct {
	Kind string `json:"kind"` // home depot yardr_dir worktree branches login_service yardr herdr harness
	// Name is the depot it is of, the harness's kind, or the part a login
	// service is for.
	Name   string `json:"name,omitempty"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Uncommitted is a worktree with changes that are in no commit.
	Uncommitted bool `json:"uncommitted,omitempty"`
	// Branches are the depot's branches under yard/.
	Branches []string `json:"branches,omitempty"`
	Remove   string   `json:"remove,omitempty"`
}

// keptPath is something in the home that --purge did not delete.
type keptPath struct {
	Path string `json:"path"`
	Why  string `json:"why"`
}

// purgeReport is what --purge did to the yard's home.
type purgeReport struct {
	Home  string `json:"home"`
	Beads int    `json:"beads"`
	// Deleted is whether the home's content was deleted, and Gone whether
	// the directory itself is: it stays for what was kept in it.
	Deleted bool       `json:"deleted"`
	Gone    bool       `json:"gone"`
	Kept    []keptPath `json:"kept"`
	Detail  string     `json:"detail,omitempty"`
	Error   string     `json:"error,omitempty"`
	Do      string     `json:"do,omitempty"`
}

// uninstallReport is the answer of yardr uninstall.
type uninstallReport struct {
	Home string `json:"home"`
	// Yard is whether a yard is in the home, and Name and Beads its name
	// and how many beads its store holds.
	Yard  bool   `json:"yard"`
	Name  string `json:"name,omitempty"`
	Beads int    `json:"beads"`
	// Uninstalled is whether it is all done: nothing of the yard runs, no
	// login service of it is left that was yardr's to remove, and the home
	// is deleted where that was asked for.
	Uninstalled bool `json:"uninstalled"`
	// Confirm is yardr down's: nothing was stopped or removed because the
	// agents in Down have to be agreed to first, with --yes.
	Confirm bool `json:"confirm"`
	// Down is yardr down's own answer; none on a home with no yard.
	Down *downReport `json:"down,omitempty"`
	// Login are the login services: removed, absent or left, as yardr down
	// --login says them.
	Login []loginUnit  `json:"login"`
	Purge *purgeReport `json:"purge,omitempty"`
	// Left is what was not touched, and where each is.
	Left []leftThing `json:"left"`
}

func (a *app) uninstallCmd() *cobra.Command {
	var yes bool
	var purge purgeFlag
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the yard, remove its login services, and say what is left and where",
		Long: `Take this yard off the machine, in this order:

  stop it         as yardr down does, with its warning, its question and its
                  --yes: the daemon, every agent this yard started, the web
                  view, the BDP server, the telemetry export, the server
  login services  every one yardr wrote for this yard: the daemon's, the web
                  view's, the BDP server's and the telemetry export's, as
                  yardr <kind> uninstall removes each. yardr's service for
                  herdr's server goes only when no other yard of yours starts
                  at login (it is left, and that yard named); one that is
                  not yardr's is never touched. herdr's server runs on.
  what is left    one list of what was not touched, and where each is

Nothing of your work is deleted. What uninstall leaves, and lists:

  the yard's home   the store with every bead, the roles, flows and logs
  each depot        your repository, the .yardr directory in it, the
                    worktrees of the yard's beads (said where one has
                    uncommitted changes) and the branches under yard/
  yardr             this program: it does not delete itself
  herdr, harnesses  where each is, and that it is removed as it was installed

Each has its path, and the command that removes it where one does. yardr
uninstall is safe to repeat: a yard that does not run and has no login
service is no error, and the list is printed all the same. After it the
yard is as yardr down --login leaves it, and yardr up starts it again.
Nothing is removed while nothing was stopped (a question not agreed to, a
daemon that could not be stopped).

--purge deletes the yard's home too, and nothing else of yours. It names the
directory, says how many beads are in its store, and asks you to type the
yard's name (yardr status prints it) before anything is stopped. With nobody
to ask (stdin closed, a script, --json) it needs both --yes and the name as
the flag's value, --purge=<name>: --yes alone deletes no yard, and a wrong
name stops nothing. Only the yard's operator may: anyone else, an agent of
the yard too, is refused before anything is stopped. It never deletes a depot's repository, though one cloned
into the home (yardr depot add --clone) is inside it; nor a worktree with
uncommitted changes, or one git cannot read; nor a branch; nor this program.
What it kept is listed, and the home stays for it. A worktree without
changes is removed as git worktree remove does: its commits are on its
branch, in the depot.

--json prints the same as data: down (yardr down's answer), login (each
service with part, result: removed, absent or left, file and error), purge
(home, beads, deleted, gone, kept) and left: each thing with kind (home,
depot, yardr_dir, worktree, branches, login_service, yardr, herdr, harness),
name, path, detail and remove. Without --yes, on a yard with any agent, it
stops nothing and answers "confirm": true with the agents in down, as yardr
down --json does. The exit status is 0 when uninstalled is true or confirm
is, else 1.`,
		Example: `  yardr uninstall
  yardr uninstall --yes                    # a script: no question about agents at work
  yardr uninstall --json | jq '.left'      # what is left, as data
  yardr uninstall --purge                  # and delete the yard's home: asks for its name
  yardr uninstall --purge=yardr --yes      # the same with nobody to ask`,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("yardr uninstall takes no argument (%s); the yard's name is --purge's value, after \"=\": --purge=<name>", strings.Join(args, " "))
			}
			return nil
		},
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			rep, err := a.uninstallYard(c.OutOrStdout(), c.ErrOrStderr(), c.Context(), cfg, yes, wait, purge)
			if err != nil {
				return err
			}
			if err := a.out(c.OutOrStdout(), rep, rep.print); err != nil {
				return err
			}
			if !rep.Uninstalled && !rep.Confirm {
				return errCheckFailed
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "stop the agents without asking, working ones too")
	cmd.Flags().DurationVar(&wait, "handoff-wait", handoffWait, "how long the crew gets to write its handoffs before it is stopped; 0 asks for none")
	cmd.Flags().Var(&purge, "purge", "delete the yard's home too, with every bead in its store: asks for the yard's `name`, or takes it as --purge=<name> beside --yes")
	cmd.Flags().Lookup("purge").NoOptDefVal = purgeAsk
	return cmd
}

// yardFacts is what uninstall reads from the yard's store while it answers:
// once the yard is down nothing can be asked.
type yardFacts struct {
	name   string
	beads  int
	depots []store.Depot
	// kinds are the harnesses the yard's groups and crew run on.
	kinds []string
}

func readYard(ctx context.Context, cfg *config.Config, st store.API) (*yardFacts, error) {
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	f := &yardFacts{name: runner.YardName(cfg.Home, settings)}
	beads, err := st.ListBeads(ctx, store.BeadFilter{All: true})
	if err != nil {
		return nil, err
	}
	f.beads = len(beads)
	if f.depots, err = st.Depots(ctx); err != nil {
		return nil, err
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	crew, err := st.Crew(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range harnessUses(settings[store.DefaultKindSetting], groups, crew) {
		f.kinds = append(f.kinds, u.kind)
	}
	return f, nil
}

// uninstallYard stops the yard, removes its login services and lists what
// is left. An error is one that kept it from doing anything: a --purge that
// was not agreed to among them.
func (a *app) uninstallYard(w, warn io.Writer, ctx context.Context, cfg *config.Config, yes bool, wait time.Duration, purge purgeFlag) (*uninstallReport, error) {
	rep := &uninstallReport{Home: cfg.Home, Login: []loginUnit{}}
	var facts *yardFacts
	down, err := a.down(w, warn, ctx, cfg, yes, wait, loginAll, func(st store.API) error {
		f, err := readYard(ctx, cfg, st)
		if err != nil {
			return err
		}
		facts = f
		if purge.set {
			// The store goes with the home, and in it what only the operator
			// may change: a line, asked of the store, before anything stops.
			if err := st.May(ctx, a.who(), store.ActDeleteYardHome, store.Object{Kind: store.KindYard}); err != nil {
				return fmt.Errorf("%w. %s", err, nothingStopped)
			}
			return a.agreePurge(w, cfg, f, purge.name, yes)
		}
		return nil
	})
	switch {
	case errors.As(err, new(notInitialized)):
		// No yard to stop. Its services carry the home in their names, so
		// those of a home that was deleted by hand are found all the same.
		if managerOf() != "" {
			rep.Login = downLogin(ctx, cfg, nil, true)
		}
		rep.Uninstalled = !loginFailed(rep.Login)
		if purge.set {
			rep.Purge = &purgeReport{Home: cfg.Home, Kept: []keptPath{}, Detail: "no yard is in it: nothing was deleted"}
		}
	case err != nil:
		if refused, ok := err.(errDownRefused); ok {
			refused.cmd = "uninstall"
			return nil, refused
		}
		return nil, err
	default:
		rep.Yard, rep.Name, rep.Beads, rep.Down, rep.Confirm = true, facts.name, facts.beads, down, down.Confirm
		if down.Login == nil && managerOf() != "" {
			// down returns before looking at login services when it cannot stop
			// the daemon, and its JSON confirmation path deliberately removes
			// nothing. Still say which services this leaves in place.
			rep.Login = loginStillInstalled(ctx, cfg)
		}
		if down.Login != nil {
			// Said once, under the report's own name for them.
			rep.Login, down.Login = down.Login, nil
		}
		done := down.Down && !loginFailed(rep.Login)
		switch {
		case !purge.set:
		case done:
			rep.Purge = purgeHome(ctx, cfg, facts)
			done = rep.Purge.Error == ""
		case !rep.Confirm:
			rep.Purge = &purgeReport{Home: cfg.Home, Beads: facts.beads, Kept: []keptPath{},
				Error: "not deleted: the yard is not all stopped, or a login service of it is left",
				Do:    yardrAt(cfg.Home) + " uninstall --purge, once more"}
		}
		rep.Uninstalled = done
	}
	rep.Left = whatIsLeft(ctx, cfg, facts, rep.Login, rep.Purge != nil && rep.Purge.Deleted)
	return rep, nil
}

// loginStillInstalled reports the yard's services without changing them.
// It is used when down could not get as far as removing services.
func loginStillInstalled(ctx context.Context, cfg *config.Config) []loginUnit {
	var out []loginUnit
	yardr := yardrAt(cfg.Home)
	for _, k := range longRunnings {
		manager, name, file, err := unitFile(cfg, k.Name)
		if err != nil {
			continue
		}
		if _, err := os.Stat(file); err != nil {
			continue
		}
		out = append(out, loginUnit{Part: k.Name, Manager: manager, Name: name, File: file,
			Result: loginLeft, Detail: "not removed: the yard was not stopped", Do: yardr + " uninstall --yes"})
	}

	state := herdrLoginState(ctx, false)
	if state.Installed || len(state.Foreign) > 0 {
		l := loginUnit{Part: partHerdr, Result: loginLeft}
		if state.Installed {
			l.Manager, l.Name, l.File = state.Manager, state.Name, state.File
			l.Detail, l.Do = "not removed: the yard was not stopped", yardr+" uninstall --yes"
		} else {
			l.Foreign = state.Foreign
			l.Detail = fmt.Sprintf("the service for herdr's server is not yardr's (%s)", strings.Join(state.Foreign, ", "))
		}
		out = append(out, l)
	}
	return out
}

// within reports whether p is dir or under it, as the file system has them
// and not as they are spelled: where it takes two spellings for one
// directory (letter case on macOS and Windows), a home named in one and a
// depot stored in the other are still one in the other. What --purge keeps
// goes by this, so a spelling must not decide it.
func within(p, dir string) bool {
	if rel, err := filepath.Rel(dir, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return true
	}
	if !filepath.IsAbs(p) || !filepath.IsAbs(dir) {
		return false
	}
	for {
		if sameFile(p, dir) {
			return true
		}
		up := filepath.Dir(p)
		if up == p {
			return false
		}
		p = up
	}
}

// sameFile reports whether p and q are one file. A link is itself, not
// what it leads to.
func sameFile(p, q string) bool {
	pi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	qi, err := os.Lstat(q)
	return err == nil && os.SameFile(pi, qi)
}

// purgeable refuses a home whose deletion would take more than a yard: one
// a depot's repository lies around, the person's own home directory, or
// what is above it.
func purgeable(cfg *config.Config, f *yardFacts) error {
	home := resolve(cfg.Home)
	const by = "Delete what is the yard's in it by hand."
	if user, err := userHomeDir(); err == nil && within(resolve(user), home) {
		return fmt.Errorf("--purge does not delete %s: your home directory %s is in it. %s", cfg.Home, user, by)
	}
	if filepath.Dir(home) == home {
		return fmt.Errorf("--purge does not delete %s. %s", cfg.Home, by)
	}
	for _, d := range f.depots {
		if within(home, resolve(d.Path)) {
			return fmt.Errorf("--purge does not delete %s: it is in depot %s (%s), and a depot is never deleted. %s", cfg.Home, d.Name, d.Path, by)
		}
	}
	return nil
}

// agreePurge is the agreement --purge needs before anything is stopped:
// the yard's name, typed in a terminal or given as the flag's value, and
// with nobody to ask --yes beside it. An error is a refusal.
func (a *app) agreePurge(w io.Writer, cfg *config.Config, f *yardFacts, given string, yes bool) error {
	if err := purgeable(cfg, f); err != nil {
		return err
	}
	what := fmt.Sprintf("the home of the yard %s, %s, with %s in its store", f.name, cfg.Home, plural(f.beads, "bead"))
	both := fmt.Sprintf("%s uninstall --purge=%s --yes", yardrAt(cfg.Home), shellQuote(f.name))
	asks := interactive() && !a.json
	switch {
	case given != purgeAsk && given != f.name:
		return fmt.Errorf("--purge=%s does not name this yard: --purge deletes %s. %s", given, what, nothingStopped)
	case given != purgeAsk && (yes || asks):
		// Named on the command line: by a person at a terminal, or by a
		// caller that says --yes too.
		return nil
	case given != purgeAsk:
		return fmt.Errorf("--purge deletes %s. With nobody to ask (no terminal) it needs --yes beside the yard's name: %s. %s", what, both, nothingStopped)
	case !asks:
		return fmt.Errorf("--purge deletes %s. With nobody to ask (no terminal) it takes the yard's name as its value, and --yes: %s. %s", what, both, nothingStopped)
	}
	fmt.Fprintf(w, "yardr uninstall --purge deletes %s.\n", what)
	fmt.Fprintln(w, "Kept: a depot's repository, a worktree with uncommitted changes, every branch, and this program.")
	typed := ""
	if err := ask(huh.NewInput().Title("Type the yard's name to delete its home").
		Description(fmt.Sprintf("%s, with %s. Anything else leaves the yard as it is.", cfg.Home, plural(f.beads, "bead"))).Value(&typed)); err != nil && !errors.Is(err, huh.ErrUserAborted) {
		return err
	}
	if strings.TrimSpace(typed) != f.name {
		return fmt.Errorf("%q is not the yard's name. %s", strings.TrimSpace(typed), nothingStopped)
	}
	return nil
}

// depotWorktree is a worktree of a depot's repository beside its main one.
type depotWorktree struct {
	path, branch string
}

// depotWorktrees asks git for them. One whose directory is gone is none.
func depotWorktrees(ctx context.Context, repo string) []depotWorktree {
	out, err := git(ctx, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	var all []depotWorktree
	for i, block := range strings.Split(out, "\n\n") {
		var w depotWorktree
		for _, line := range strings.Split(block, "\n") {
			if p, ok := strings.CutPrefix(line, "worktree "); ok {
				w.path = p
			} else if b, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
				w.branch = b
			}
		}
		// The first is the repository's own.
		if i == 0 || w.path == "" {
			continue
		}
		if _, err := os.Stat(w.path); err != nil {
			continue
		}
		all = append(all, w)
	}
	return all
}

// uncommitted is what keeps a worktree from being removed, "" for one with
// nothing in it that is in no commit: what yardr's own cleanup asks before
// it removes one (runner.Herdr.Finish). A directory git cannot read as a
// worktree counts as one with changes: nobody can say it has none.
func uncommitted(ctx context.Context, path string) string {
	top, err := git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil || resolve(top) != resolve(path) {
		return "git does not read it as a worktree"
	}
	switch out, err := git(ctx, path, "status", "--porcelain"); {
	case err != nil:
		return "git status failed in it: " + err.Error()
	case out != "":
		return "uncommitted changes"
	}
	return ""
}

// yardBranch is the prefix of the branches beads are worked on.
var yardBranch = store.Branch("")

// whatIsLeft is what uninstall did not touch, read from the machine as it
// is now: after a purge, without what that deleted. f is nil on a home with
// no yard, and purged says that its home was deleted in this run.
func whatIsLeft(ctx context.Context, cfg *config.Config, f *yardFacts, login []loginUnit, purged bool) []leftThing {
	left := []leftThing{}
	yardr := yardrAt(cfg.Home)
	if _, err := os.Stat(cfg.Home); err == nil {
		l := leftThing{Kind: leftHome, Path: cfg.Home, Detail: "no yard is in it"}
		if names := dirNames(cfg.Home); len(names) > 0 {
			// What an earlier purge kept, or what is not a yard's at all.
			l.Detail += "; it holds " + strings.Join(names, ", ")
		}
		switch {
		case purged:
			l.Detail = "no yard is in it any more: it stays for what the purge kept"
		case f != nil:
			l.Detail = fmt.Sprintf("the yard %s: its store with %s, its roles, flows and logs", f.name, plural(f.beads, "bead"))
			l.Remove = yardr + " uninstall --purge"
		}
		left = append(left, l)
	}
	var depots []store.Depot
	if f != nil {
		depots = f.depots
	}
	for _, d := range depots {
		if d.Path == "" {
			continue // nothing of it is on this machine
		}
		l := leftThing{Kind: leftDepot, Name: d.Name, Path: d.Path, Detail: "your repository: yardr deletes nothing in it"}
		if d.IsDir() {
			l.Detail = "your directory: yardr deletes nothing in it"
		}
		if _, err := os.Stat(d.Path); err != nil {
			l.Detail = "not there any more"
			left = append(left, l)
			continue
		}
		left = append(left, l)
		if dir := filepath.Join(d.Path, ".yardr"); dirNames(dir) != nil {
			left = append(left, leftThing{Kind: leftYardrDir, Name: d.Name, Path: dir,
				Detail: "the depot's roles, flows and test command: files of the depot",
				Remove: "delete it yourself, in a commit where the depot is a repository"})
		}
		if d.IsDir() {
			continue
		}
		for _, w := range depotWorktrees(ctx, d.Path) {
			if !strings.HasPrefix(w.branch, yardBranch) && !within(resolve(w.path), resolve(cfg.Worktrees())) {
				continue // one of the person's own
			}
			l := leftThing{Kind: leftWorktree, Name: d.Name, Path: w.path, Detail: "no branch"}
			if w.branch != "" {
				l.Detail = "branch " + w.branch
			}
			if why := uncommitted(ctx, w.path); why != "" {
				l.Uncommitted = true
				l.Detail += ", " + why
			}
			l.Remove = shellLine([]string{"git", "-C", d.Path, "worktree", "remove", w.path})
			left = append(left, l)
		}
		if out, err := git(ctx, d.Path, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+yardBranch); err == nil && out != "" {
			names := strings.Split(out, "\n")
			left = append(left, leftThing{Kind: leftBranches, Name: d.Name, Path: d.Path, Branches: names,
				Detail: fmt.Sprintf("%d under %s: %s", len(names), yardBranch, strings.Join(names, ", ")),
				Remove: shellLine([]string{"git", "-C", d.Path, "branch", "-D"}) + " <branch>"})
		}
	}
	for _, l := range login {
		if l.Result != loginLeft && l.Error == "" {
			continue
		}
		file := l.File
		if file == "" && l.Part != partHerdr {
			_, _, file, _ = unitFile(cfg, l.Part)
		}
		detail := l.Detail
		if l.Error != "" {
			detail = "not removed: " + l.Error
		}
		left = append(left, leftThing{Kind: leftService, Name: l.Part, Path: or(file, strings.Join(l.Foreign, ", ")),
			Detail: detail, Remove: l.Do})
	}
	if self, err := executable(); err == nil {
		left = append(left, leftThing{Kind: leftYardr, Path: self, Detail: "this program: uninstall does not delete itself",
			Remove: shellLine([]string{"rm", self})})
	}
	if bin, _ := herdrProgram(ctx); bin != "" {
		left = append(left, leftThing{Kind: leftHerdr, Path: bin,
			Detail: "its server is left running: other yards and your own panes are in it",
			Remove: "herdr server stop ends its server and every pane in it; the program is removed as it was installed (https://herdr.dev)"})
	}
	kinds := agents.Kinds()
	if f != nil {
		kinds = f.kinds
	}
	for _, kind := range kinds {
		n := harnessNeed(kind, "")
		if !n.Found {
			continue
		}
		left = append(left, leftThing{Kind: leftHarness, Name: kind, Path: n.Path,
			Detail: "with its own login and settings, which yardr does not keep",
			Remove: "as it was installed: " + agents.InstallHint(kind)})
	}
	return left
}

// dirNames are the names in dir, nil for one that is not there.
func dirNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// purgeHome deletes the yard's home, which is down, and nothing that is the
// person's beside the yard: a depot in it stays, and so does a worktree
// that has changes in no commit, or that git cannot say is without. A
// worktree without is removed as yardr's own cleanup removes one (git
// worktree remove): its commits are on its branch, in the depot.
func purgeHome(ctx context.Context, cfg *config.Config, f *yardFacts) *purgeReport {
	rep := &purgeReport{Home: cfg.Home, Beads: f.beads, Kept: []keptPath{}}
	home := resolve(cfg.Home)
	keep := map[string]string{}
	for _, d := range f.depots {
		if p := resolve(d.Path); within(p, home) {
			keep[p] = "depot " + d.Name
		}
	}
	if self, err := executable(); err == nil {
		if p := resolve(self); within(p, home) {
			keep[p] = "this program"
		}
	}
	// The worktrees in the home: those each depot's git knows, and what is
	// where yardr puts them, whichever depot it was of.
	// One is taken once, whatever it is spelled as.
	var worktrees []string
	add := func(p string) {
		if !slices.ContainsFunc(worktrees, func(w string) bool { return w == p || sameFile(w, p) }) {
			worktrees = append(worktrees, p)
		}
	}
	for _, d := range f.depots {
		if d.IsDir() {
			continue
		}
		for _, w := range depotWorktrees(ctx, d.Path) {
			if p := resolve(w.path); within(p, home) {
				add(p)
			}
		}
	}
	found, _ := filepath.Glob(filepath.Join(resolve(cfg.Worktrees()), "*", "*"))
	for _, p := range found {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			add(resolve(p))
		}
	}
	for _, p := range worktrees {
		if slices.ContainsFunc(f.depots, func(d store.Depot) bool {
			return d.Path != "" && keep[resolve(d.Path)] != "" && within(p, resolve(d.Path))
		}) {
			continue // in a depot that stays whole
		}
		why := uncommitted(ctx, p)
		if why == "" {
			if _, err := git(ctx, p, "worktree", "remove", p); err != nil {
				why = "git worktree remove failed: " + err.Error()
			}
		}
		if why != "" {
			keep[p] = "a worktree: " + why
		}
	}
	paths := make([]string, 0, len(keep))
	for p, why := range keep {
		paths = append(paths, p)
		rep.Kept = append(rep.Kept, keptPath{Path: p, Why: why})
	}
	slices.SortFunc(rep.Kept, func(x, y keptPath) int { return cmp.Compare(x.Path, y.Path) })
	_, err := removeExcept(home, paths)
	rep.Deleted = true
	if err != nil {
		rep.Error, rep.Do = "not all of it could be deleted: "+err.Error(), yardrAt(cfg.Home)+" uninstall --purge, once more"
	}
	_, statErr := os.Stat(home)
	rep.Gone = errors.Is(statErr, os.ErrNotExist)
	return rep
}

// removeExcept deletes what is in dir but the paths of keep and what leads
// to them, and dir itself when nothing in it was kept. A link is removed,
// never followed.
func removeExcept(dir string, keep []string) (kept bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true, err
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		switch {
		case slices.ContainsFunc(keep, func(k string) bool { return k == p || sameFile(k, p) }):
			kept = true
		case e.IsDir() && slices.ContainsFunc(keep, func(k string) bool { return within(k, p) }):
			k, rerr := removeExcept(p, keep)
			kept, err = kept || k, cmp.Or(err, rerr)
		default:
			if rerr := os.RemoveAll(p); rerr != nil {
				kept, err = true, cmp.Or(err, rerr)
			}
		}
	}
	if !kept {
		err = cmp.Or(err, os.Remove(dir))
	}
	return kept, err
}

// leftNames are the kinds of leftThing as the list's lines begin; one of a
// depot is under its depot's line.
var leftNames = map[string]string{leftHome: "the yard's home", leftYardrDir: "  .yardr", leftWorktree: "  worktree", leftBranches: "  branches",
	leftService: "login service", leftYardr: "yardr", leftHerdr: "herdr"}

func (l leftThing) label() string {
	switch l.Kind {
	case leftDepot:
		return "depot " + l.Name
	case leftHarness:
		return "harness " + l.Name
	case leftService:
		return yardPart{Part: l.Name}.name() + " at login"
	}
	return leftNames[l.Kind]
}

func (r *uninstallReport) print(w io.Writer) {
	yardr := yardrAt(r.Home)
	if r.Yard {
		r.Down.lines(w)
	} else {
		fmt.Fprintf(w, "No yard is in %s: nothing to stop.\n", r.Home)
	}
	printLogin(w, r.Login)
	if r.Yard && r.Down.Down && !slices.ContainsFunc(r.Down.Parts, func(p yardPart) bool { return p.Stopped }) &&
		!slices.ContainsFunc(r.Down.Agents, func(g yardAgent) bool { return g.Stopped }) &&
		!slices.ContainsFunc(r.Login, func(l loginUnit) bool { return l.Result == loginRemoved }) {
		fmt.Fprintln(w, "\nNothing of the yard was running, and no login service of it was installed.")
	}
	if p := r.Purge; p != nil {
		fmt.Fprintln(w)
		switch {
		case p.Detail != "":
			fmt.Fprintf(w, "%-15s %s: %s\n", "home", p.Home, p.Detail)
		case !p.Deleted:
			fmt.Fprintf(w, "%-15s NOT %s\n%-15s   %s\n", "home", p.Error, "", p.Do)
		default:
			did := fmt.Sprintf("deleted: %s, with %s", p.Home, plural(p.Beads, "bead"))
			if !p.Gone {
				did = fmt.Sprintf("deleted but for what is kept: %s, with %s", p.Home, plural(p.Beads, "bead"))
			}
			fmt.Fprintf(w, "%-15s %s\n", "home", did)
			for _, k := range p.Kept {
				fmt.Fprintf(w, "%-15s   kept %s (%s)\n", "", k.Path, k.Why)
			}
			if p.Error != "" {
				fmt.Fprintf(w, "%-15s NOT %s\n%-15s   %s\n", "home", p.Error, "", p.Do)
			}
		}
	}
	fmt.Fprintln(w, "\nNot touched, and where each is:")
	for _, l := range r.Left {
		line := l.Path
		if l.Detail != "" {
			line += ": " + l.Detail
		}
		fmt.Fprintf(w, "  %-18s %s\n", l.label(), line)
		if l.Remove != "" {
			fmt.Fprintf(w, "  %-18s   to remove: %s\n", "", l.Remove)
		}
	}
	switch {
	case !r.Uninstalled:
		fmt.Fprintf(w, "\nNot all of it is done: see NOT above. Then: %s uninstall, once more\n", yardr)
	case !r.Yard:
		fmt.Fprintf(w, "\nNothing of yardr runs or starts at login for %s.\n", r.Home)
	case r.Purge != nil:
		fmt.Fprintf(w, "\nThe yard %s is deleted, and nothing of it runs or starts at login.\n", r.Name)
	default:
		fmt.Fprintf(w, "\nThe yard %s in %s is uninstalled: nothing of it runs or starts at login, and all of your work is where it was.\n"+
			"To use it again: %s up. To delete its home too: %s uninstall --purge\n", r.Name, r.Home, yardr, yardr)
	}
}
