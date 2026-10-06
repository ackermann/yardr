package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/rigcheck"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
	"yardr.dev/yardr/roles"
)

// A yard is initialized once yardr init (or the onboarding) has stored its
// default_kind. The store file alone does not say so: the server creates it
// on the first command that connects, whatever that command was, so a home
// can hold a store nobody set up. A store without default_kind that already
// holds depots, groups, crew members or beads is a yard from before the
// onboarding, in use, and counts as set up too: onboarding it would refuse
// its agents' commands and the daemon (yard-w6np). Opening the store writes
// default_kind into such a yard (store.migrateSettings); this check is the
// belt to those braces, for a store a server of an older build still holds.
func (a *app) initialized(ctx context.Context, cfg *config.Config) (bool, error) {
	st, err := a.storeIfAny(ctx, cfg)
	if err != nil || st == nil {
		return false, err
	}
	if defaultKind(ctx, st) != "" {
		return true, nil
	}
	depots, err := st.Depots(ctx)
	if err != nil {
		return false, err
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		return false, err
	}
	crew, err := st.Crew(ctx)
	if err != nil {
		return false, err
	}
	if len(depots)+len(groups)+len(crew) > 0 {
		return true, nil
	}
	beads, err := st.ListBeads(ctx, store.BeadFilter{All: true})
	if err != nil {
		return false, err
	}
	return len(beads) > 0, nil
}

// storeIfAny is the home's store when the home has one, nil when it has no
// store file: there is nothing to connect to then, and connecting would
// create one.
func (a *app) storeIfAny(ctx context.Context, cfg *config.Config) (store.API, error) {
	if a.st == nil {
		if _, err := os.Stat(cfg.DBPath()); errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
	}
	return a.store(ctx)
}

// partYard is what a home that init never marked set up (no default_kind)
// holds of a yard all the same: what an init cut short left there. Init
// writes the yardmaster's role, then the operator and yardmaster settings, then
// the yardmaster crew member, and default_kind last, so a run that failed in
// between leaves the ones before it. The next run finishes from them and says so.
type partYard struct {
	// roles are the role prompts in the home, by their path under it: the
	// yardmaster's under packs/<pack>/roles, and under roles what an init
	// before the default pack wrote there.
	roles                []string
	operator, yardmaster string // the settings
	style, note          string // operator_style and operator_note, for the form to start at
	crew                 []string
}

// partYard reads what the home holds of a yard; nil when it holds nothing,
// when default_kind is set (the yard is set up) and when the store has depots,
// groups or beads of work (a yard from before the onboarding, in use).
func (a *app) partYard(ctx context.Context, cfg *config.Config) (*partYard, error) {
	p := &partYard{}
	for _, glob := range []string{filepath.Join("packs", "*", "roles", "*.md"), filepath.Join("roles", "*.md")} {
		files, _ := filepath.Glob(filepath.Join(cfg.Home, glob))
		for _, f := range files {
			if rel, err := filepath.Rel(cfg.Home, f); err == nil {
				p.roles = append(p.roles, rel)
			}
		}
	}
	st, err := a.storeIfAny(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if st != nil {
		all, err := st.Settings(ctx)
		if err != nil {
			return nil, err
		}
		if all["default_kind"] != "" {
			return nil, nil
		}
		depots, err := st.Depots(ctx)
		if err != nil {
			return nil, err
		}
		groups, err := st.Groups(ctx)
		if err != nil {
			return nil, err
		}
		beads, err := st.ListBeads(ctx, store.BeadFilter{All: true})
		if err != nil {
			return nil, err
		}
		if len(depots)+len(groups) > 0 || slices.ContainsFunc(beads, work) {
			return nil, nil
		}
		crew, err := st.Crew(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range crew {
			p.crew = append(p.crew, r.Name)
		}
		p.operator, p.yardmaster = all["operator"], all["yardmaster"]
		p.style, p.note = all[styleSetting], all[noteSetting]
	}
	if len(p.found(cfg.Home)) == 0 {
		return nil, nil
	}
	return p, nil
}

// found is what the home holds, in words; nil for a nil partYard.
func (p *partYard) found(home string) []string {
	if p == nil {
		return nil
	}
	var out []string
	switch len(p.roles) {
	case 0:
	case 1:
		out = append(out, fmt.Sprintf("the role prompt %s in %s", p.roles[0], home))
	default:
		out = append(out, fmt.Sprintf("the role prompts %s in %s", list(p.roles), home))
	}
	var set []string
	for _, kv := range [][2]string{{"operator", p.operator}, {"yardmaster", p.yardmaster}} {
		if kv[1] != "" {
			set = append(set, kv[0]+" "+kv[1])
		}
	}
	switch len(set) {
	case 0:
	case 1:
		out = append(out, "the setting "+set[0])
	default:
		out = append(out, "the settings "+list(set))
	}
	if len(p.crew) > 0 {
		out = append(out, "the crew member "+list(p.crew))
	}
	return out
}

// notInitialized is a command run on a home with no yard yet and no
// terminal to onboard in (exit 2).
type notInitialized struct{ home string }

func (e notInitialized) Error() string {
	return fmt.Sprintf("no yard in %s yet: create one with `%s init --harness <kind>` "+
		"(or run yardr in a terminal for the onboarding)", e.home, yardrAt(e.home))
}

// yardrAt is yardr as a command for home is typed: yardr for the default
// home, yardr --home <home> for any other, since without the flag the
// command would go to ~/.yardr (yard-0wys).
func yardrAt(home string) string {
	if config.IsDefaultHome(home) {
		return "yardr"
	}
	return "yardr --home " + shellQuote(home)
}

// shellQuote is s as one shell word: as it is when nothing in it is special,
// single-quoted otherwise.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+:,@%=") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// interactive reports whether the onboarding form can run; tests replace it.
var interactive = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// lookPath finds an agent's executable; tests replace it.
var lookPath = exec.LookPath

// noOnboarding are the commands that run on a home with no yard, by top-level
// name or by path under yardr: the server (Connect starts it to create the
// store), init itself, the ones that never touch the store, the daemon (it
// has no terminal, and a refusal after it replaced itself would end it:
// yard-w6np), settings, the way out when the check is wrong about a yard,
// doctor, which says that there is no yard and creates none, status, which
// says the same and never starts an onboarding where a fact was asked for,
// and store compact, which needs the file to itself: the check would connect
// to the server, starting one, and the compaction would then always find
// the store in use.
var noOnboarding = []string{"server", "init", "daemon", "settings", "doctor", "status", "version", "help", "completion",
	cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd, "store compact"}

// firstRun runs before every command: on a home with no yard yet it runs the
// onboarding in a terminal, and refuses with how to initialize otherwise.
// Once onboarded, the command goes on against the new yard.
func (a *app) firstRun(c *cobra.Command) error {
	top := c
	for top.HasParent() && top.Parent().HasParent() {
		top = top.Parent()
	}
	if !top.HasParent() {
		return a.onboard(c)
	}
	path := strings.TrimPrefix(c.CommandPath(), top.Parent().Name()+" ")
	if slices.Contains(noOnboarding, top.Name()) || slices.Contains(noOnboarding, path) {
		return nil
	}
	return a.onboard(c)
}

// onboard is firstRun for a command that needs a yard.
func (a *app) onboard(c *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if ok, err := a.initialized(c.Context(), cfg); err != nil {
		return err
	} else if ok {
		if c.HasParent() {
			return nil
		}
		// Bare yardr on a yard: help, unless its onboarding is to finish.
		return a.resume(c.Context(), cfg)
	}
	if !interactive() {
		return notInitialized{cfg.Home}
	}
	part, err := a.partYard(c.Context(), cfg)
	if err != nil {
		return err
	}
	opts, err := onboardForm(cfg.Home, part)
	if errors.Is(err, huh.ErrUserAborted) {
		if part != nil {
			return fmt.Errorf("onboarding cancelled; nothing was added to %s", cfg.Home)
		}
		return fmt.Errorf("onboarding cancelled; nothing was created in %s", cfg.Home)
	} else if err != nil {
		return err
	}
	res, err := a.initYard(c.Context(), cfg, opts)
	if err != nil {
		return err
	}
	a.onboarded = true
	return a.goOn(c.Context(), cfg, res, opts)
}

// goOn is the onboarding after the yard is created: the summary, the start
// when asked for, and once the yardmaster is up the services and the hand-off.
// When the yardmaster is not up it says how the onboarding is taken up again.
func (a *app) goOn(ctx context.Context, cfg *config.Config, res *initResult, opts initOptions) error {
	if err := a.report(ctx, cfg, res, opts); err != nil {
		return err
	}
	if res.Start == nil || a.json {
		return nil
	}
	if !res.Start.Running {
		// No yardmaster, no chat: an empty view would wait for someone who is
		// not there. What doctor says of the yard is what there is to read.
		if err := a.doctorSays(ctx, cfg); err != nil {
			return err
		}
		fmt.Printf("\nOnce that is set right, run %s in this terminal again: it goes on from here.\n", yardrAt(cfg.Home))
		return nil
	}
	// What the yard runs without, before the rest of its setup: the
	// answers are the same wherever that goes on.
	if err := a.offerServices(ctx, cfg); errors.Is(err, huh.ErrUserAborted) {
		return nil
	} else if err != nil {
		return err
	}
	return a.handOff(ctx, cfg, res.Yardmaster, res.Start.Yardmaster)
}

// resume is bare yardr in a terminal on a yard whose onboarding did not reach
// its end: it is set up (or counts as it: an init cut short after the
// yardmaster was added), has no depot yet, and its default_kind is missing, its
// yardmaster is, no daemon runs, or the yardmaster is not up. It says what is
// there (the lines yardr doctor prints for the yard), asks, and then does
// what the onboarding does from that point: init again, which adds only what
// is missing, the start, the services, the hand-off. On any other yard, and
// where no form runs, it does nothing, and bare yardr is help.
//
// It goes by the yard as it is, not by a record of the earlier run: a yard
// made by yardr init and never started is taken up the same way.
func (a *app) resume(ctx context.Context, cfg *config.Config) error {
	if a.json || !interactive() || agentSession() {
		return nil
	}
	st, err := a.store(ctx)
	if err != nil {
		return err
	}
	steps, err := setupSteps(ctx, st)
	if err != nil {
		return err
	}
	if next := nextStep(steps); next == nil || next.Key != stepDepot {
		return nil // a yard with a depot is past its onboarding
	}
	crew, err := st.Crew(ctx)
	if err != nil {
		return err
	}
	state, err := readState(ctx, cfg, st, crew)
	if err != nil {
		return err
	}
	m := state.Yardmaster
	// A session that runs is not a yardmaster that is up (see waitYardmaster).
	silent := false
	for _, r := range crew {
		if r.Name == m.Name && r.Live() {
			silent = !up(ctx, r)
		}
	}
	noKind := defaultKind(ctx, st) == ""
	if !noKind && state.Daemon && m.CrewMember && (!m.Enabled || (m.Running && !silent)) {
		return nil
	}
	lines := state.lines(cfg.Home)
	if silent {
		lines = append(lines, silentYardmaster(m.Name))
	}
	if noKind {
		lines = append(lines, docLine{tagFault, "settings", "default_kind is not set: an earlier setup stopped before it marked the yard set up"})
	}
	yardr := yardrAt(cfg.Home)
	fmt.Printf("The yard in %s has no depot yet, and its onboarding did not reach its end. What is there:\n\n", cfg.Home)
	printLines(lines)
	fmt.Println()
	a.onboarded = true // from here on this run is the onboarding, not help
	finish := true
	what := "Waits for " + m.Name
	if !state.Daemon {
		what = "Starts the daemon in the background and waits for " + m.Name
	}
	if noKind || !m.CrewMember {
		what = "Adds what is missing. " + what
	}
	err = ask(huh.NewConfirm().Key("finish").Title("Finish the setup now?").
		Description(fmt.Sprintf("%s, then goes on as the onboarding does.\nNo leaves the yard as it is: %s init --start does the same later.", what, yardr)).
		Value(&finish))
	if err != nil && !errors.Is(err, huh.ErrUserAborted) {
		return err
	}
	if err != nil || !finish {
		fmt.Printf("Left as it is. To finish later: %s init --start, or %s in a terminal.\n", yardr, yardr)
		return nil
	}
	opts := initOptions{Yardmaster: m.Name, Start: true, resumed: true}
	res, err := a.initYard(ctx, cfg, opts)
	if err != nil {
		return err
	}
	return a.goOn(ctx, cfg, res, opts)
}

// report prints what init did and, when asked to, starts the yard in between:
// the summary first, so the wait for the yardmaster has something above it,
// then how the start ended. As JSON it is one object, at the end.
func (a *app) report(ctx context.Context, cfg *config.Config, res *initResult, o initOptions) error {
	if !a.json && !(o.resumed && res.YardmasterKept && len(res.Found) == 0) {
		// An onboarding taken up on a yard that lacked nothing has said
		// what is there (resume); the summary would say it again.
		res.printYard()
	}
	if o.Start && res.NoYardmaster {
		// To stderr: --json's output stays the result alone.
		fmt.Fprintf(stderr, "--start: nothing is started, since the yard has no yardmaster to wait for; %s daemon starts the yard\n", yardrAt(res.Home))
	} else if o.Start {
		st, err := a.store(ctx)
		if err != nil {
			return err
		}
		wait := o.Wait
		if wait <= 0 {
			wait = yardmasterWait
		}
		// One real call to the yardmaster's harness first, if the person says
		// so: one that is not logged in is named before any pane exists.
		if goOn, err := a.askHarness(ctx, cfg, st, res.Yardmaster); err != nil || !goOn {
			// Stopped: it said what is there and how to come back, and
			// nothing was started to report on.
			return err
		}
		res.Start = a.startYard(ctx, cfg, st, res.Yardmaster, wait)
		// A yardmaster whose harness waits at something only a person answers:
		// the way to its pane and back, before the start is reported.
		if err := a.offerPane(ctx, cfg, st, res.Yardmaster, wait, res.Start); err != nil {
			return err
		}
		res.Daemon = daemonRunning(cfg.DaemonLock())
	}
	return a.out(res, res.printNext)
}

// initOptions are the answers of the onboarding, from its form or from
// `yardr init` flags.
type initOptions struct {
	Harness    string // herdr kind: the yard's default_kind and the yardmaster's
	Yardmaster string // the yardmaster crew member's name
	Dir        string // where the yardmaster works; "" is the home's parent
	Model      string // "" is the harness's default
	// Operator is the person the role prompts name, where the templates say
	// {{operator}}; "" is the login name. Yardmaster goes where they say
	// {{yardmaster}}.
	Operator string
	// Style is how agents should work with the operator (workStyles), and
	// About what they should know about them: the operator_style and
	// operator_note settings. "" stores neither: only the form has a default.
	Style, About string
	// Start starts the daemon in the background after the yard is created
	// and waits for the yardmaster's session, up to Wait (0 is yardmasterWait).
	Start bool
	Wait  time.Duration
	// Given names the flags the command line set (harness, operator,
	// yardmaster, style, about). On a yard that is set up, a setting whose
	// flag was not given is kept as it is. The form never runs on a set-up
	// yard.
	Given map[string]bool
	// resumed: this init takes up an onboarding that did not reach its end
	// (resume).
	resumed bool
}

// initResult is what initYard did.
type initResult struct {
	Home       string `json:"home"`
	Harness    string `json:"harness"`
	Yardmaster string `json:"yardmaster"`
	Dir        string `json:"dir"`
	Operator   string `json:"operator"`
	// Style and Note are the operator_style and operator_note settings as
	// this init left them; absent on a yard that has none.
	Style string   `json:"operator_style,omitempty"`
	Note  string   `json:"operator_note,omitempty"`
	Args  []string `json:"args"`
	// Pack is the yard's default pack, which the yardmaster's role is taken
	// from, and Roles where its roles are installed in the home; Prompt is
	// the yardmaster's, "" when the pack gives its yardmaster no role. All
	// three are absent when the yardmaster was there already: init reads no
	// pack then.
	Pack   string `json:"pack,omitempty"`
	Roles  string `json:"roles,omitempty"`
	Prompt string `json:"prompt,omitempty"`
	// Written is the role prompt this init installed, when it did.
	Written []string `json:"written"`
	// NoYardmaster: the default pack has no crew member called yardmaster, so
	// init added none.
	NoYardmaster bool `json:"no_yardmaster,omitempty"`
	// OldRoles is <home>/roles when the home has it, from an init before
	// the default pack: read as before, and no longer written.
	OldRoles string `json:"old_roles,omitempty"`
	Daemon   bool   `json:"daemon"` // a daemon runs on this home
	// YardmasterKept: a crew member of the yardmaster's name was there already
	// and is left as it was.
	YardmasterKept bool `json:"yardmaster_kept"`
	// SettingsKept are the settings stored before that this init left as
	// they were ("operator Jo"), their flags not given.
	SettingsKept []string `json:"settings_kept"`
	// Found is what an earlier init, cut short, had left in the home, and
	// Added what this run added to finish the yard; both absent on a home
	// that held nothing and on a yard that was set up.
	Found []string `json:"found,omitempty"`
	Added []string `json:"added,omitempty"`
	// Start is how starting the yard ended, when init was asked to.
	Start *startResult `json:"start,omitempty"`
}

// initYard creates the yard: the store, the operator and default_kind
// settings and the yardmaster crew member, with its role from the yard's
// default pack installed in the home (yardmasterRole). Inputs are checked
// before anything is created. An existing crew member of the yardmaster's
// name is never replaced, and then no pack is read and no role written, so
// init runs again on a yard without harm and changes no prompt in it: an
// installed role is brought to a newer pack's by yardr pack apply, which
// shows the change first. <home>/roles, which init wrote before there was
// a default pack, is left as it is. The settings stored before stand in for
// the flags not given (keepSettings): init again changes only what its
// flags say.
//
// A run that fails leaves what it wrote before (the order is the yardmaster's
// role, operator and yardmaster, the yardmaster crew member, default_kind), and the next run
// finishes from it: the same steps skip or keep what is there, the stored
// settings and the yardmaster's kind stand in for flags not given, and the
// result says what was found and what was added (partYard).
func (a *app) initYard(ctx context.Context, cfg *config.Config, o initOptions) (*initResult, error) {
	part, err := a.partYard(ctx, cfg)
	if err != nil {
		return nil, err
	}
	o, kept, err := a.keepSettings(ctx, cfg, o)
	if err != nil {
		return nil, err
	}
	if o.Harness == "" {
		return nil, fmt.Errorf("--harness <kind> is required to create a yard; herdr starts: %s", strings.Join(agents.Kinds(), ", "))
	}
	if !agents.IsKind(o.Harness) {
		return nil, fmt.Errorf("unknown harness %q; herdr starts: %s", o.Harness, strings.Join(agents.Kinds(), ", "))
	}
	if o.Yardmaster == "" {
		o.Yardmaster = "yardmaster"
	}
	if !groupName.MatchString(o.Yardmaster) {
		return nil, fmt.Errorf("yardmaster name %q: letters, digits, '.', '_', '-', '/'", o.Yardmaster)
	}
	o.About = strings.TrimSpace(o.About)
	if o.Style != "" {
		if err := checkStyle(o.Style); err != nil {
			return nil, err
		}
	}
	if err := checkNote(o.About); err != nil {
		return nil, err
	}
	dir, err := absDir(or(o.Dir, filepath.Dir(cfg.Home)))
	if err != nil {
		return nil, err
	}
	st, err := a.store(ctx)
	if err != nil {
		return nil, err
	}
	// Init again on a yard is no harm: an existing crew member of the yardmaster's
	// name is kept as it is, never replaced.
	_, err = st.GetCrew(ctx, o.Yardmaster)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	yardmasterKept := err == nil
	res := &initResult{Home: cfg.Home, Harness: o.Harness, Yardmaster: o.Yardmaster, Dir: dir,
		Operator: or(o.Operator, defaultOperator()), Style: o.Style, Note: o.About,
		Args: agents.UnattendedArgs(o.Harness), Written: []string{}}
	res.YardmasterKept, res.SettingsKept = yardmasterKept, kept
	if old := filepath.Join(cfg.Home, "roles"); isDir(old) {
		res.OldRoles = old
	}
	if !yardmasterKept {
		if err := yardmasterRole(ctx, st, cfg.Home, o.Yardmaster, part != nil, res); err != nil {
			return nil, err
		}
	}
	// operator is who a pack's roles name at an import later, and yardmaster
	// the crew member a session is told to mail.
	for _, kv := range [][2]string{{"operator", res.Operator}, {"yardmaster", o.Yardmaster}} {
		if err := st.SetSetting(ctx, kv[0], kv[1], a.who()); err != nil {
			return nil, err
		}
	}
	// How to work with the operator is stored only when it was said (or kept):
	// a yard that was never asked has no style, and its primes none to give.
	for _, kv := range [][2]string{{styleSetting, o.Style}, {noteSetting, o.About}} {
		if kv[1] == "" {
			continue
		}
		if err := st.SetSetting(ctx, kv[0], kv[1], a.who()); err != nil {
			return nil, err
		}
	}
	if err := a.putYardmaster(ctx, st, o, res); err != nil {
		return nil, err
	}
	// default_kind last: it marks the yard initialized, so an init cut
	// short onboards again.
	if err := st.SetSetting(ctx, "default_kind", o.Harness, a.who()); err != nil {
		return nil, err
	}
	if res.Found = part.found(cfg.Home); len(res.Found) > 0 {
		if len(res.Written) > 0 {
			res.Added = append(res.Added, "the role prompt "+list(bases(res.Written)))
		}
		if part.operator == "" || part.yardmaster == "" {
			res.Added = append(res.Added, "the operator and yardmaster settings")
		}
		if !yardmasterKept && !res.NoYardmaster {
			res.Added = append(res.Added, "the crew member "+o.Yardmaster)
		}
		res.Added = append(res.Added, "the default_kind setting, which marks the yard set up")
	}
	res.Daemon = daemonRunning(cfg.DaemonLock())
	return res, nil
}

// keepSettings is o with the settings the home's store holds (default_kind,
// operator, yardmaster, operator_style, operator_note) in place of the flags
// not given, so init again does not reset them to the flags' defaults (the
// login name, "yardmaster"); it reports which were kept, as "<setting>
// <value>". Where no default_kind is stored and no harness given, the harness
// is the yardmaster crew member's kind: an init cut short after it added the
// yardmaster is finished on the harness it chose.
func (a *app) keepSettings(ctx context.Context, cfg *config.Config, o initOptions) (initOptions, []string, error) {
	st, err := a.storeIfAny(ctx, cfg)
	if err != nil || st == nil {
		return o, nil, err
	}
	all, err := st.Settings(ctx)
	if err != nil {
		return o, nil, err
	}
	var kept []string
	for _, s := range []struct {
		flag, key string
		value     *string
	}{{"harness", "default_kind", &o.Harness}, {"operator", "operator", &o.Operator}, {"yardmaster", "yardmaster", &o.Yardmaster},
		{"style", styleSetting, &o.Style}, {"about", noteSetting, &o.About}} {
		if !o.Given[s.flag] && all[s.key] != "" {
			*s.value = all[s.key]
			kept = append(kept, s.key+" "+all[s.key])
		}
	}
	if o.Harness == "" {
		if r, err := st.GetCrew(ctx, or(o.Yardmaster, "yardmaster")); err == nil {
			o.Harness, _ = r.Config["kind"].(string)
		} else if !errors.Is(err, store.ErrNotFound) {
			return o, nil, err
		}
	}
	return o, kept, nil
}

// packYardmaster is the name of the crew member in a pack whose role the
// yard's yardmaster takes: a crew member's name, and no role's. The yard may
// call its own otherwise (yardr init --yardmaster).
const packYardmaster = "yardmaster"

// yardPack is the pack a yard uses where none is named and no depot says
// which kind it is for: its default_pack setting, else the starter.
func yardPack(ctx context.Context, st store.API) (string, error) {
	all, err := st.Settings(ctx)
	if err != nil {
		return "", err
	}
	return or(all[defaultPackSetting], packs.Starter), nil
}

// yardmasterRole installs the role of the yardmaster init is about to add,
// and notes it in res: the role the yard's default pack gives its crew
// member called yardmaster, written where an import of the pack installs it
// (pack.InstallRole), so the first depot set up from the pack finds it
// there. The yardmaster exists before any depot, and an import needs one:
// this is the one file of a pack that init installs. A default pack with
// no such crew member gets the yard none (res.NoYardmaster).
//
// A file already there is kept as it is, since a crew member or an import
// may have it: a yard that is set up has no prompt changed by init. unfinished
// says the home is an init's that was cut short, and the file then that
// run's, naming whoever it was told: it is written again.
func yardmasterRole(ctx context.Context, st store.API, home, yardmaster string, unfinished bool, res *initResult) error {
	arg, err := yardPack(ctx, st)
	if err != nil {
		return err
	}
	p, err := resolvePack(ctx, st, arg, "")
	if err != nil {
		return fmt.Errorf("the yard's default pack, %s (yardr settings get %s): %w", arg, defaultPackSetting, err)
	}
	res.Pack, res.Roles = p.Name, filepath.Join(home, "packs", p.Name, "roles")
	if !slices.ContainsFunc(p.Crew, func(r pack.CrewMember) bool { return r.Name == packYardmaster }) {
		res.NoYardmaster = true
		return nil
	}
	path, wrote, err := pack.InstallRole(p, home, packYardmaster, yardmaster, res.Operator, unfinished)
	if err != nil {
		return err
	}
	if res.Prompt = path; wrote {
		res.Written = append(res.Written, path)
	}
	return nil
}

// isDir reports whether path is a directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// putYardmaster adds the yardmaster crew member res describes, unless one is
// kept or the default pack has none.
func (a *app) putYardmaster(ctx context.Context, st store.API, o initOptions, res *initResult) error {
	if res.YardmasterKept || res.NoYardmaster {
		return nil
	}
	// greet: its first session writes to the operator by mail, which is
	// where the onboarding takes them (yardr chat).
	conf := map[string]any{"kind": o.Harness, runner.GreetKey: res.Operator}
	if res.Prompt != "" {
		conf["prompt"] = res.Prompt
	}
	if o.Model != "" {
		conf["model"] = o.Model
	}
	if len(res.Args) > 0 {
		list := make([]any, len(res.Args))
		for i, s := range res.Args {
			list[i] = s
		}
		conf["args"] = list
	}
	_, err := st.PutCrew(ctx, store.CrewMember{Name: o.Yardmaster, Runner: "herdr", Config: conf, Dir: res.Dir,
		On: defaultOn, MaxAge: "6h", HandoffGrace: "15m"}, a.who())
	return err
}

// fallbackOperator is the operator of a yard that was told no name where
// the environment gives none.
const fallbackOperator = "operator"

// loginName is the OS's name for the user running yardr, "" when it has
// none (a uid without a passwd entry, as in a container). A variable so a
// test can be a machine that does not know its user.
var loginName = func() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.Username
}

// defaultOperator is who the role prompts name when the onboarding was not
// told: the login name ($USER, then $LOGNAME, then the OS's own). The
// operator is a user, so only a name a user may have is taken (Windows'
// DOMAIN\name is none), and without one it is "operator": init under
// env -i, in a container or in a service unit must not fail for want of a
// login name.
func defaultOperator() string {
	for _, name := range []string{os.Getenv("USER"), os.Getenv("LOGNAME"), loginName()} {
		if store.ValidUserName(name) {
			return name
		}
	}
	return fallbackOperator
}

// shippedManifest, in the home's roles directory, records the sha256 of
// each role as init wrote it. Init again (after an upgrade, say) compares
// each installed file with it: one still as written is an untouched copy of
// an older template, and is refreshed to this build's; one that differs was
// edited, and is kept. A file from before the manifest is kept too unless
// it already reads as this build's, which is when it starts being tracked.
// A depot's .yardr/roles/ gets no manifest: depot init never overwrites, and
// those files are committed, not refreshed.
const shippedManifest = ".shipped.json"

// roleInstall is what installRoles did with each embedded role, by path.
type roleInstall struct {
	Written   []string `json:"written"`             // not there before, installed now
	Refreshed []string `json:"refreshed,omitempty"` // untouched since installed, now this build's
	Current   []string `json:"current,omitempty"`   // already this build's text
	Kept      []string `json:"kept"`                // edited (or untracked), left as they are
}

// summary says what happened to the roles already there, for the init
// summary; "" when there were none.
func (r *roleInstall) summary() string {
	var parts []string
	if len(r.Refreshed) > 0 {
		parts = append(parts, strings.Join(bases(r.Refreshed), ", ")+" refreshed to this build's")
	}
	if len(r.Kept) > 0 {
		parts = append(parts, strings.Join(bases(r.Kept), ", ")+" edited, kept")
	}
	if len(parts) == 0 && len(r.Current) > 0 {
		parts = append(parts, "already this build's")
	}
	return strings.Join(parts, "; ")
}

func bases(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}

// installRoles writes the embedded role prompts into dir, naming operator
// where the templates say {{operator}} and yardmaster where they say
// {{yardmaster}}. A file already there is kept, except that with refresh one
// the manifest says was never edited is replaced with this build's (see
// shippedManifest). With only, it writes just those files (e.g.
// "builder.md").
//
// Each file is written whole or not at all, and the manifest is saved after
// every one: a run that fails among them leaves no half a prompt, and none
// it wrote that the next run would take for an edited one and keep, naming
// whoever the failed run was told.
func installRoles(dir, operator, yardmaster string, refresh bool, only ...string) (*roleInstall, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	files, err := fs.ReadDir(roles.FS, ".")
	if err != nil {
		return nil, err
	}
	manifest := map[string]string{}
	mpath := filepath.Join(dir, shippedManifest)
	if refresh {
		if b, err := os.ReadFile(mpath); err == nil {
			if err := json.Unmarshal(b, &manifest); err != nil {
				return nil, fmt.Errorf("%s: %w", mpath, err)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	save := func() error {
		if !refresh {
			return nil
		}
		b, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		return writeWhole(mpath, append(b, '\n'))
	}
	res := &roleInstall{}
	for _, f := range files {
		if len(only) > 0 && !slices.Contains(only, f.Name()) {
			continue
		}
		body, err := roles.FS.ReadFile(f.Name())
		if err != nil {
			return nil, err
		}
		want := fillRole(body, operator, yardmaster)
		name := roleFileIn(dir, f.Name())
		path := filepath.Join(dir, name)
		have, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			res.Written = append(res.Written, path)
		case err != nil:
			return nil, err
		case !refresh:
			res.Kept = append(res.Kept, path)
			continue
		case bytes.Equal(have, want):
			res.Current = append(res.Current, path)
			manifest[name] = digest(want)
			continue
		case manifest[name] == digest(have):
			res.Refreshed = append(res.Refreshed, path)
		default:
			res.Kept = append(res.Kept, path)
			continue
		}
		if err := writeWhole(path, want); err != nil {
			return nil, err
		}
		manifest[name] = digest(want)
		if err := save(); err != nil {
			return nil, err
		}
	}
	if err := save(); err != nil {
		return nil, err
	}
	return res, nil
}

// oldYardmasterRole is the file name the yardmaster's role had until this
// release, when the crew member was called the mayor.
const oldYardmasterRole = "mayor.md"

// roleFileIn is the name the embedded role called name has in dir: its own,
// except that a directory holding mayor.md and no yardmaster.md keeps the
// yardmaster's role under the old name, where the crew of a yard set up
// before the rename read it (their prompt is that path).
func roleFileIn(dir, name string) string {
	if name != "yardmaster.md" {
		return name
	}
	if _, err := os.Stat(filepath.Join(dir, name)); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Stat(filepath.Join(dir, oldYardmasterRole)); err == nil {
			return oldYardmasterRole
		}
	}
	return name
}

// writeWhole writes a file under its name only once it is complete: a
// write cut short leaves the file that was there, or none.
func writeWhole(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// digest is the manifest's hash of a role file's bytes.
func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fillRole puts the names into a role template's placeholders. {{mayor}} is
// what {{yardmaster}} was called until this release, and is still filled.
func fillRole(body []byte, operator, yardmaster string) []byte {
	body = bytes.ReplaceAll(body, []byte("{{operator}}"), []byte(operator))
	body = bytes.ReplaceAll(body, []byte("{{mayor}}"), []byte(yardmaster))
	return bytes.ReplaceAll(body, []byte("{{yardmaster}}"), []byte(yardmaster))
}

// oldFlag keeps a flag's earlier name working for this release: the same
// value under the old name, which help does not list. Given under the old
// name the flag is given: what asks whether it was (Changed), requires it
// or excludes another with it is answered as under the new.
func oldFlag(cmd *cobra.Command, old, name string) { oldFlagP(cmd, old, "", name) }

// oldFlagP is oldFlag for a flag whose one-letter name changed with it:
// short is the letter it had.
func oldFlagP(cmd *cobra.Command, old, short, name string) {
	f := cmd.Flags().Lookup(name)
	cmd.Flags().VarP(oldValue{f}, old, short, f.Usage)
	cmd.Flags().Lookup(old).NoOptDefVal = f.NoOptDefVal
	_ = cmd.Flags().MarkHidden(old)
}

// oldValue sets the flag an old name stands for.
type oldValue struct{ f *pflag.Flag }

func (v oldValue) String() string { return v.f.Value.String() }
func (v oldValue) Type() string   { return v.f.Value.Type() }
func (v oldValue) Set(s string) error {
	if err := v.f.Value.Set(s); err != nil {
		return err
	}
	v.f.Changed = true
	return nil
}

// daemonRunning reports whether a daemon holds the home's daemon lock,
// without taking it or creating the file.
func daemonRunning(lock string) bool {
	f, err := os.Open(lock)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// printYard is the summary of the yard init created or set up again.
func (r *initResult) printYard() {
	yardr := yardrAt(r.Home)
	switch {
	case len(r.Found) > 0:
		// An init cut short left part of it; this run finished it.
		fmt.Printf("yard in %s finished: part of it was there, from a setup that did not reach its end\n\n", r.Home)
		fmt.Printf("  found      %s\n", strings.Join(r.Found, "; "))
		fmt.Printf("  added      %s\n", strings.Join(r.Added, "; "))
	case r.YardmasterKept:
		fmt.Printf("yard in %s set up again\n\n", r.Home)
	default:
		fmt.Printf("yard created in %s\n\n", r.Home)
	}
	fmt.Printf("  harness    %s (the yard's default_kind: groups and crew members get it when --set kind is omitted)\n", r.Harness)
	if len(r.SettingsKept) > 0 {
		fmt.Printf("  kept       %s, as set before: --harness, --operator, --yardmaster, --style and --about change them\n", strings.Join(r.SettingsKept, ", "))
	}
	if s, ok := styleOf(r.Style); ok {
		fmt.Printf("  operator   %s, with whom agents work in the style %s (%q): every session reads it when it starts\n", r.Operator, s.key, s.label)
	}
	if r.YardmasterKept {
		fmt.Printf("  yardmaster %s, already a crew member: kept as it was\n", r.Yardmaster)
	} else if r.NoYardmaster {
		fmt.Printf("  yardmaster none: the yard's default pack, %s, has no crew member called %s. One is added with\n"+
			"             %s crew add %s\n", r.Pack, packYardmaster, yardr, r.Yardmaster)
	} else {
		fmt.Printf("  yardmaster %s, working in %s, rotating every 6h\n", r.Yardmaster, r.Dir)
		if len(r.Args) > 0 {
			fmt.Printf("             runs with %s so it does not stop to ask\n", strings.Join(r.Args, " "))
		} else {
			fmt.Printf("             runs with no unattended flags: %s may stop to ask; give it its own with\n"+
				"             %s crew set %s 'args=[...]' (see yardr group add --help)\n", r.Harness, yardr, r.Yardmaster)
		}
	}
	if r.Prompt != "" {
		fmt.Printf("  roles      %s (pack %s, the yard's default)\n"+
			"             the yardmaster's prompt is %s there, naming %s. A depot's builders and\n"+
			"             reviewers get theirs beside it when the depot is set up (%s depot init <depot>);\n"+
			"             the pack is where a role is changed, and %s pack apply brings the change here.\n",
			r.Roles, r.Pack, filepath.Base(r.Prompt), r.Operator, yardr, yardr)
	}
	if r.OldRoles != "" {
		fmt.Printf("  roles      %s, from an earlier yardr, is read as before by a group whose prompt is\n"+
			"             roles/<name>.md, and is yours: init no longer writes or refreshes it.\n", r.OldRoles)
	}
	fmt.Println()
	if !config.IsDefaultHome(r.Home) {
		// Not the default home: without the flag a command goes to
		// ~/.yardr, the daemon's first of all (yard-0wys).
		fmt.Printf("This yard is not in the default home: every yardr command for it needs\n"+
			"--home %s, or YARDR_HOME exported.\n", shellQuote(r.Home))
	}
}

// printNext is the way from the yard to the yardmaster: how the start ended
// when init started the yard, else how to start it.
func (r *initResult) printNext() {
	yardr := yardrAt(r.Home)
	if r.NoYardmaster {
		fmt.Printf("Start the daemon in a terminal pane of its own: %s daemon. The steps from here to a first bead: %s setup\n", yardr, yardr)
		return
	}
	switch {
	case r.Start != nil:
		r.Start.print(r.Yardmaster)
		if !r.Start.Running {
			return
		}
	case r.Daemon:
		fmt.Printf("The daemon is running; it starts %s shortly. Where %s is then: %s init --start\n", r.Yardmaster, r.Yardmaster, yardr)
	default:
		fmt.Printf("Start the daemon in a terminal pane of its own, to watch it; %s starts when it does:\n\n  %s daemon\n\n"+
			"Or in the background, waiting for %s and printing where it is:\n\n  %s init --start\n",
			r.Yardmaster, yardr, r.Yardmaster, yardr)
	}
	// The rest of the setup is a conversation, or a run of forms: the
	// yardmaster's first run and yardr setup go by the same steps (setupSteps).
	// The commands for scripts are in yardr init --help.
	fmt.Printf("\n%s sets up the rest with you: the repository to work in, who builds and\n"+
		"reviews, and a first bead to try it on. Talk to it in any terminal, or take\n"+
		"the same steps yourself as a short run of forms:\n\n  %s chat\n  %s setup\n", r.Yardmaster, yardr, yardr)
}

// harnessOptions are the kinds herdr starts, each marked with whether its
// executable is on PATH, and the default: the first installed one.
func harnessOptions() ([]huh.Option[string], string) {
	var opts []huh.Option[string]
	def := ""
	for _, k := range agents.Kinds() {
		label := k
		if p, ok := agents.Lookup(k); ok {
			label = p.Label
		}
		state := "not on PATH"
		if installed(k) {
			state = "installed"
			if def == "" {
				def = k
			}
		}
		opts = append(opts, huh.NewOption(fmt.Sprintf("%-12s %s", label, state), k))
	}
	if def == "" {
		def = agents.Kinds()[0]
	}
	return opts, def
}

// onboardForm asks for the harness, the yardmaster's name, its directory and
// its model, your name and how agents should work with you, and whether to
// start the yard. Each answer starts at its default, so accepting every page
// gives a working yard with its yardmaster running.
//
// part is what an earlier setup, cut short, left in the home: the form says
// so, and the yardmaster's name, yours and how agents work with you start at
// what that run stored.
func onboardForm(home string, part *partYard) (initOptions, error) {
	opts, def := harnessOptions()
	o := initOptions{Harness: def, Yardmaster: "yardmaster", Dir: filepath.Dir(home), Operator: defaultOperator(), Style: defaultStyle, Start: true,
		// The form's answers are given, whatever the store holds.
		Given: map[string]bool{"harness": true, "operator": true, "yardmaster": true, "style": true, "about": true}}
	fmt.Printf("No yard in %s yet. This sets one up: a default agent harness, and a yardmaster on it.\n\n", home)
	if part != nil {
		o.Yardmaster, o.Operator = or(part.yardmaster, o.Yardmaster), or(part.operator, o.Operator)
		if _, ok := styleOf(part.style); ok {
			o.Style = part.style
		}
		o.About = part.note
		fmt.Printf("An earlier setup of this home did not reach its end. It left %s.\n"+
			"This one goes on from that, and says at its end what it added.\n\n", strings.Join(part.found(home), "; "))
	}
	if err := preflight(); err != nil {
		return o, err
	}
	if err := ask(huh.NewSelect[string]().Title("Harness").
		Description("The agent yardr starts by default, for the yardmaster and for groups that name none.").
		Options(opts...).Height(min(len(opts)+2, 14)).Value(&o.Harness)); err != nil {
		return o, err
	}
	if err := ask(
		huh.NewInput().Title("Yardmaster").Description("The always-on crew member that coordinates the yard.").
			Value(&o.Yardmaster).Validate(func(s string) error {
			if !groupName.MatchString(s) {
				return errors.New("letters, digits, '.', '_', '-', '/'")
			}
			return nil
		}),
		huh.NewInput().Title("Directory").
			Description("Where the yardmaster's shell lives: its session starts there, and relative paths it uses resolve from there.").
			Value(&o.Dir).Validate(func(s string) error { _, err := absDir(s); return err }),
		huh.NewInput().Title("Model").Description("Optional; blank uses "+o.Harness+"'s default.").Value(&o.Model),
		huh.NewInput().Title("Your name").
			Description("How the role prompts name you: the person agents bring decisions to.").
			Value(&o.Operator),
	); err != nil {
		return o, err
	}
	if err := ask(styleFields(&o.Style, &o.About)...); err != nil {
		return o, err
	}
	style, _ := styleOf(o.Style)
	create := true
	summary := fmt.Sprintf("harness %s\nyardmaster %s in %s\nmodel %s\noperator %s, with whom agents work in the style %s (%q)\nroles %s (starting points; a depot's own go in its repo's .yardr/roles/)",
		o.Harness, o.Yardmaster, o.Dir, or(o.Model, "the harness's default"), or(o.Operator, defaultOperator()), style.key, style.label, filepath.Join(home, "roles"))
	if err := ask(huh.NewConfirm().Title("Create this yard?").Description(summary).Value(&create)); err != nil {
		return o, err
	}
	if !create {
		return o, huh.ErrUserAborted
	}
	if err := ask(huh.NewConfirm().Title("Start the yard now?").
		Description(fmt.Sprintf("Starts the daemon in the background and waits for %s. No leaves that to you: yardr daemon, in a pane of its own.", o.Yardmaster)).
		Value(&o.Start)); err != nil {
		return o, err
	}
	return o, nil
}

// styleFields are the form page after "Your name": how agents should work
// with you, each answer with its meaning, and what they should know about
// you. What is chosen is stored and read by every session when it starts.
func styleFields(style, about *string) []huh.Field {
	opts := make([]huh.Option[string], len(workStyles))
	for i, s := range workStyles {
		opts[i] = huh.NewOption(fmt.Sprintf("%-13s %s", s.label, s.means), s.key)
	}
	return []huh.Field{
		huh.NewSelect[string]().Key("style").Title("How should agents work with you?").
			Description("Every agent reads this when its session starts. `yardr settings set " + styleSetting + " <choice>` changes it later.").
			Options(opts...).Value(style),
		huh.NewInput().Key("about").Title("Anything agents should know about you?").
			Description("Optional, one line: it is read with the above. For example what you work in, or what you never want asked.").
			Value(about).Validate(checkNote),
	}
}

func (a *app) initCmd() *cobra.Command {
	var o initOptions
	cmd := &cobra.Command{
		Use:   "init [--harness <kind>]",
		Short: "Create the yard: default harness and the yardmaster, with its role from the default pack (what the onboarding does)",
		Long: `Create the yard in --home (else YARDR_HOME, default ~/.yardr) without the onboarding form,
for scripts and tests. It stores --harness as the yard's default_kind, and
adds the yardmaster crew member on that harness with its unattended flags,
rotating every 6h. The daemon starts the yardmaster.

The yardmaster's role is a pack's: the one the yard's default pack (the
default_pack setting, else starter, which yardr carries) gives its crew
member called yardmaster. Init installs that one file under
<home>/packs/<pack>/roles/, naming --operator as the person the yardmaster
brings decisions to, and makes it the yardmaster's prompt. A depot's builders
and reviewers get their roles beside it when the depot is set up (yardr
depot init). A default pack with no crew member called yardmaster gets the
yard none. Init again on a yard that has its yardmaster reads no pack and
writes no role: a role installed from a pack is changed in the pack and
brought to the yard by yardr pack apply, which shows the change first.

<home>/roles/, which init wrote before a yard had a default pack, is no
longer written. A yard that has it keeps it: a group whose prompt is
roles/<name>.md reads it as before (docs/configuration.md, "Role prompts"),
and init leaves every file there as it is.

Init does not start the daemon unless told to: --start starts one in the
background (none is started when one runs on the home already), logging to
<home>/daemon.log, and waits up to --wait for the yardmaster: its session
running and its first output in its pane, after its first prompt. It then
prints where the yardmaster is in herdr (agent, workspace, tab, pane) and how
to get there, or, when the yardmaster did not come up, what is known (the last
lines of its pane among it) and how to go on, and exits 1. It is what the
onboarding form does on "Start the yard now?".

In a terminal, --start first asks whether to check that the yardmaster's
harness answers, as the onboarding does: one real call to it outside a pane,
the one yardr doctor --try makes, which costs a model call. A harness that
does not answer is named with what it printed, and the call is made again, the
start goes on all the same, or it stops there and starts nothing. Without a
terminal, with --json and in an agent's session it asks nothing and makes no
call: yardr doctor --try is that call for a script.

In a terminal, --start then opens yardr chat on the yardmaster, as the
onboarding does: the yardmaster's first session is asked to greet the operator
by mail (yardr mail <operator>), and the chat is where that mail shows and
where the setup goes on as a conversation. Leaving it stops nothing. Without a
terminal, with --json, in an agent's session and with --no-chat it opens
nothing, and what it prints names yardr chat. Run it again on a running yard
to be told where the yardmaster is. yardr daemon in a terminal pane of its own
does the same in the foreground, to watch it.

Run yardr in a terminal on a home with no yard to get the same as a form. A
home counts as set up once init has stored default_kind, or once its store has
depots, groups, crew members or beads (a yard from before the onboarding); the
store file alone does not, since the server creates it on any first command.
Init again on a yard keeps its default_kind, operator and yardmaster settings
and an existing yardmaster crew member; only the flags given change them, so
--harness is required only to create a yard.

--style says how agents should work with you, and --about what they should
know about you (one line). They are stored as the settings operator_style
and operator_note, which every session reads when it starts: yardr prime
prints them as instructions, so yardr settings set changes what the next
session reads, with no init. Without --style a new yard has no style, and
the primes say nothing about it; init again keeps both. The styles:

` + styleHelp() + `

A run that failed leaves what it made before the failure (the order is: the
yardmaster's role, the operator and yardmaster settings, the yardmaster, default_kind), and init
again finishes from it. The settings stored stand in for the flags not given,
and the yardmaster's kind for --harness once the yardmaster is there; the summary then
says "finished", with what it found and what it added (--json: found, added).
On a yard that is set up and did not start, init --start starts and waits
again. Bare yardr in a terminal does the same on a yard with no depot yet,
after saying what is there and asking, and then goes on as the onboarding
does. docs/configuration.md has both, and how to remove a yard and start
over.

After init the yardmaster sets up the rest in conversation, or yardr setup does in
a terminal, as forms. For scripts, the same by hand:

  yardr depot add webapp ~/projects/webapp --base main
  yardr depot init webapp --yes --bind builders=claude --bind reviewers=claude \
    --members backlog= --members decisions=jo
  git -C ~/projects/webapp add .yardr/check && git -C ~/projects/webapp commit -m 'yardr: gate' -- .yardr/check

depot init sets the depot up from the yard's default pack: its groups, their
roles, the flow and the routes go into the yard, and the gate alone into the
repository, to be committed on the base (yardr depot init --help).

Unattended flags per harness:
` + agents.UnattendedHelp(),
		Example: `  yardr init --harness claude
  yardr init --harness claude --start              # and start the yard, waiting for the yardmaster
  yardr --home /tmp/y1 init --harness codex --yardmaster boss --dir ~/projects --model gpt-5.5 --operator Jo
  yardr init --harness claude --style work --about "I review every merge myself"`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			o.Given = map[string]bool{}
			for _, f := range []string{"harness", "operator", "yardmaster", "style", "about"} {
				o.Given[f] = c.Flags().Changed(f)
			}
			o.Given["yardmaster"] = o.Given["yardmaster"] || c.Flags().Changed("mayor")
			res, err := a.initYard(c.Context(), cfg, o)
			if err != nil {
				return err
			}
			if err := a.report(c.Context(), cfg, res, o); err != nil {
				return err
			}
			if res.Start != nil && !res.Start.Running {
				// The report said what is known; a script reads the exit.
				return fmt.Errorf("%s is not up", res.Yardmaster)
			}
			if res.Start != nil && a.chats() {
				st, err := a.store(c.Context())
				if err != nil {
					return err
				}
				_, err = a.toChat(c.Context(), cfg, st, res.Yardmaster)
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&o.Harness, "harness", "", "agent kind herdr starts: "+strings.Join(agents.Kinds(), ", ")+
		" (required to create a yard; a set-up yard keeps its default_kind)")
	cmd.Flags().StringVar(&o.Yardmaster, "yardmaster", "yardmaster", "the yardmaster crew member's name (a set-up yard keeps its yardmaster setting)")
	oldFlag(cmd, "mayor", "yardmaster")
	cmd.Flags().StringVar(&o.Dir, "dir", "", "where the yardmaster works (default: the home's parent)")
	cmd.Flags().StringVar(&o.Model, "model", "", "the yardmaster's model (default: the harness's own)")
	cmd.Flags().BoolVar(&o.Start, "start", false, "start the daemon in the background, unless one runs, and wait for the yardmaster's session")
	cmd.Flags().DurationVar(&o.Wait, "wait", yardmasterWait, "with --start: how long to wait for the yardmaster")
	cmd.Flags().BoolVar(&a.noChat, "no-chat", false, "with --start in a terminal: do not open yardr chat on the yardmaster once it is up")
	cmd.Flags().StringVar(&o.Operator, "operator", "", "your name, as the role prompts use it (default: your login name, else operator; a set-up yard keeps its operator setting)")
	cmd.Flags().StringVar(&o.Style, "style", "", "how agents should work with you: "+strings.Join(styleKeys(), ", ")+
		" (default: none; a set-up yard keeps its operator_style setting)")
	cmd.Flags().StringVar(&o.About, "about", "", "one line agents should know about you (a set-up yard keeps its operator_note setting)")
	return cmd
}

// styleHelp lists the styles with their meanings, for help texts.
func styleHelp() string {
	var b strings.Builder
	for _, s := range workStyles {
		fmt.Fprintf(&b, "  %-7s %q: %s\n", s.key, s.label, s.means)
	}
	return strings.TrimRight(b.String(), "\n")
}

// defaultPackSetting names the pack a depot is set up from when depot init
// is given none (defaultPack), and the one init takes the yardmaster's role
// from (yardPack).
const defaultPackSetting = "default_pack"

// checkDefaultPack takes what names a pack wherever a later command runs:
// a name, or a directory written absolute. A relative directory would mean
// another pack in every directory depot init is run from.
func checkDefaultPack(v string) error {
	if !packDir(v) {
		if !groupName.MatchString(v) {
			return fmt.Errorf("%s %q: a pack's name, or its directory as an absolute path", defaultPackSetting, v)
		}
		return nil
	}
	if !filepath.IsAbs(v) {
		return fmt.Errorf("%s %q: a directory is given as an absolute path, since the setting is read wherever yardr depot init is run", defaultPackSetting, v)
	}
	if _, err := os.Stat(filepath.Join(v, pack.File)); err != nil {
		return fmt.Errorf("%s %q: no pack there: %w", defaultPackSetting, v, err)
	}
	return nil
}

// settings are the yard's settings: what each means and what it accepts.
var settings = map[string]struct {
	help  string
	check func(string) error
}{
	"operator": {"who the role prompts name, the person agents bring decisions to (a pack's roles are installed naming them)",
		func(string) error { return nil }},
	store.YardmasterSetting: {"the yardmaster crew member's name, whom the role prompts tell agents to mail", func(v string) error {
		if !groupName.MatchString(v) {
			return fmt.Errorf("yardmaster name %q: letters, digits, '.', '_', '-', '/'", v)
		}
		return nil
	}},
	styleSetting: {"how agents should work with the operator, which every session reads when it starts (yardr prime): " +
		strings.Join(styleKeys(), ", "), checkStyle},
	noteSetting: {"one line agents should know about the operator, read with operator_style", checkNote},
	"default_kind": {"the agent kind of herdr groups and crew members whose config names none", func(v string) error {
		if !agents.IsKind(v) {
			return fmt.Errorf("default_kind %q: herdr starts %s", v, strings.Join(agents.Kinds(), ", "))
		}
		return nil
	}},
	defaultPackSetting: {"the pack a depot is set up from when yardr depot init names none, and the pack yardr init takes the yardmaster's role from: " +
		"a pack's name (one the yard has imported, or one yardr carries) or its directory, absolute; default: starter, and dir for a directory depot", checkDefaultPack},
	hooks.FromSetting: {"the address the server takes webhooks on, such as 127.0.0.1:8790, when YARDR_HOOK_ADDR is not set where it starts; " +
		"the server reads it when it starts", hooks.CheckAddr},
	store.TimerLimitSetting: {fmt.Sprintf("how many live timers one crew member or user may have set (yardr timer); default %d", store.DefaultTimerLimit),
		checkTimerLimit},
	store.TimerMinEverySetting: {fmt.Sprintf("the shortest period of a recurring timer (yardr timer add --every), such as 30m or 1d; default %s",
		store.Span(store.DefaultTimerMinEvery)), checkTimerMinEvery},
	rigcheck.SlotsSetting: {fmt.Sprintf("how many full gates (yardr depot check) run at once on the machine, counting every yard of the user; the others wait; 0 for no limit; default %d",
		rigcheck.DefaultSlots), func(v string) error {
		_, err := rigcheck.ParseSlots(rigcheck.SlotsSetting, v)
		return err
	}},
	"name": {"the yard's name, which its crew's herdr agents and workspaces carry (<name>-yardmaster); default: the home's directory name", func(v string) error {
		if !groupName.MatchString(v) {
			return fmt.Errorf("yard name %q: letters, digits, '.', '_', '-', '/'", v)
		}
		return nil
	}},
}

func (a *app) settingsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "settings", Short: "The yard's own defaults (default_kind, default_pack, gate_slots, hook_addr, yardmaster, name, operator, operator_style, operator_note, timer_limit, timer_min_every)",
		Long: `The yard's own defaults. yardr settings get lists them with what each means.

hook_addr is the address the server takes webhooks on (yardr hook --help).
The server reads it when it starts, so a change takes its next start (yardr
server stop; the next command starts it again), and YARDR_HOOK_ADDR in the
environment the server starts in is used instead of it. yardr doctor says
which address is in use.

operator_style is how agents should work with the operator, and
operator_note one line they should know about them. Every session reads both
when it starts (yardr prime --bead and --crew print them as
instructions), so a change reaches the next session; unset, the primes say
nothing. The styles:

` + styleHelp() + `

gate_slots is how many full gates run at once on the machine (yardr depot
check --help): the limit is the machine's, shared by every yard of the user,
and a gate that finds no slot free waits. 0 is no limit.

default_pack is the pack yardr depot init sets a depot up from when it is
given no --pack: a name (of a pack the yard has imported, or of one yardr
carries) or a pack's directory as an absolute path. Unset, a git depot gets
starter and a directory depot dir, the two packs yardr carries. yardr init
takes the yardmaster's role from it. Changing it moves nothing: it is read
when a depot is set up (docs/packs.md).

timer_limit and timer_min_every bound the yard's timers (yardr timer): how
many live ones one crew member or user may have set, and the shortest period of
a recurring one. They hold for timers set from then on.`}
	// known is the key as it is stored and printed: an earlier release's
	// name for a setting still names it.
	known := func(args []string) error {
		args[0] = store.SettingKey(args[0])
		if _, ok := settings[args[0]]; !ok {
			return fmt.Errorf("no setting %q; there is: %s", args[0], strings.Join(sortedKeys(settings), ", "))
		}
		return nil
	}
	get := &cobra.Command{
		Use:   "get [<key>]",
		Short: "Print a setting, or all of them",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				if err := known(args); err != nil {
					return err
				}
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			all, err := st.Settings(c.Context())
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return a.out(all, func() {
					rows := [][]string{}
					for _, k := range sortedKeys(settings) {
						rows = append(rows, []string{k, or(all[k], "-"), settings[k].help})
					}
					table("KEY\tVALUE\tMEANING", rows)
				})
			}
			v, ok := all[args[0]]
			if !ok {
				return fmt.Errorf("%s is not set: %w", args[0], store.ErrNotFound)
			}
			return a.out(map[string]string{args[0]: v}, func() { fmt.Println(v) })
		},
	}
	set := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Change a setting; an empty value unsets it",
		Example: `  yardr settings set default_kind codex
  yardr settings set default_pack ~/packs/research
  yardr settings set operator_style report
  yardr settings set hook_addr 127.0.0.1:8790
  yardr settings set operator_note ""        # unset`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			if err := known(args); err != nil {
				return err
			}
			if args[1] != "" {
				if err := settings[args[0]].check(args[1]); err != nil {
					return err
				}
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := st.SetSetting(c.Context(), args[0], args[1], a.who()); err != nil {
				return err
			}
			return a.out(map[string]string{args[0]: args[1]}, func() {
				fmt.Printf("%s = %s\n", args[0], or(args[1], "(unset)"))
				if args[0] != hooks.FromSetting {
					return
				}
				cfg := a.cfg
				if cfg == nil {
					if cfg, err = config.Load(); err != nil {
						return
					}
				}
				fmt.Println(hookAddrTakes(cfg))
			})
		},
	}
	cmd.AddCommand(get, set)
	return cmd
}

// defaultKind is the yard's default_kind setting, "" when unset or unread.
func defaultKind(ctx context.Context, st store.API) string {
	all, err := st.Settings(ctx)
	if err != nil {
		return ""
	}
	return all["default_kind"]
}

// withDefaultKind gives a herdr config with no kind the yard's default_kind.
func withDefaultKind(ctx context.Context, st store.API, runner string, cfg map[string]any) {
	if runner != "herdr" || cfg["kind"] != nil {
		return
	}
	if k := defaultKind(ctx, st); k != "" {
		cfg["kind"] = k
	}
}
