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
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/roles"
)

// A yard is initialized once yardr init (or the onboarding) has stored its
// default_kind. The store file alone does not say so: the server creates it
// on the first command that connects, whatever that command was, so a home
// can hold a store nobody set up. A store without default_kind that already
// holds rigs, groups, residents or beads is a yard from before the
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
	rigs, err := st.Rigs(ctx)
	if err != nil {
		return false, err
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		return false, err
	}
	residents, err := st.Residents(ctx)
	if err != nil {
		return false, err
	}
	if len(rigs)+len(groups)+len(residents) > 0 {
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
// writes the roles, then the operator and mayor settings, then the mayor
// resident, and default_kind last, so a run that failed in between leaves
// the ones before it. The next run finishes from them and says so.
type partYard struct {
	roles           []string // the role prompts in <home>/roles
	operator, mayor string   // the settings
	residents       []string
}

// partYard reads what the home holds of a yard; nil when it holds nothing,
// when default_kind is set (the yard is set up) and when the store has rigs,
// groups or beads of work (a yard from before the onboarding, in use).
func (a *app) partYard(ctx context.Context, cfg *config.Config) (*partYard, error) {
	p := &partYard{}
	if files, err := os.ReadDir(filepath.Join(cfg.Home, "roles")); err == nil {
		for _, f := range files {
			if strings.HasSuffix(f.Name(), ".md") {
				p.roles = append(p.roles, f.Name())
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
		rigs, err := st.Rigs(ctx)
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
		if len(rigs)+len(groups) > 0 || slices.ContainsFunc(beads, work) {
			return nil, nil
		}
		residents, err := st.Residents(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range residents {
			p.residents = append(p.residents, r.Name)
		}
		p.operator, p.mayor = all["operator"], all["mayor"]
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
	if len(p.roles) > 0 {
		out = append(out, fmt.Sprintf("the role prompts %s in %s", list(p.roles), filepath.Join(home, "roles")))
	}
	var set []string
	for _, kv := range [][2]string{{"operator", p.operator}, {"mayor", p.mayor}} {
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
	if len(p.residents) > 0 {
		out = append(out, "the resident "+list(p.residents))
	}
	return out
}

// notInitialized is a command run on a home with no yard yet and no
// terminal to onboard in (exit 2).
type notInitialized struct{ home string }

func (e notInitialized) Error() string {
	return fmt.Sprintf("no yard in %s yet: create one with `%syardr init --harness <kind>` "+
		"(or run yardr in a terminal for the onboarding)", e.home, homeEnv(e.home))
}

// homeEnv is what a command for home needs in front of it: nothing for the
// default home, YARDR_HOME=<home> and a space for any other, since without it
// the command would go to ~/.yardr (yard-0wys).
func homeEnv(home string) string {
	if config.IsDefaultHome(home) {
		return ""
	}
	return "YARDR_HOME=" + shellQuote(home) + " "
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
// doctor, which says that there is no yard and creates none, and store compact, which needs the file to itself: the check would connect
// to the server, starting one, and the compaction would then always find
// the store in use.
var noOnboarding = []string{"server", "init", "daemon", "settings", "doctor", "version", "help", "completion",
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
// when asked for, and once the mayor is up the services and the hand-off.
// When the mayor is not up it says how the onboarding is taken up again.
func (a *app) goOn(ctx context.Context, cfg *config.Config, res *initResult, opts initOptions) error {
	if err := a.report(ctx, cfg, res, opts); err != nil {
		return err
	}
	if res.Start == nil || a.json {
		return nil
	}
	if !res.Start.Running {
		fmt.Printf("\nOnce that is set right, run %syardr in this terminal again: it goes on from here.\n", homeEnv(cfg.Home))
		return nil
	}
	// What the yard runs without, before the rest of its setup: the
	// answers are the same wherever that goes on.
	if err := a.offerServices(ctx, cfg); errors.Is(err, huh.ErrUserAborted) {
		return nil
	} else if err != nil {
		return err
	}
	return a.handOff(ctx, cfg, res.Mayor, res.Start.Mayor)
}

// resume is bare yardr in a terminal on a yard whose onboarding did not
// reach its end: it is set up (or counts as it: an init cut short after the
// mayor was added), has no rig yet, and its default_kind is missing, its
// mayor is, no daemon runs, or the mayor is not up. It says what is there
// (the lines yardr doctor prints for the yard), asks, and then does what
// the onboarding does from that point: init again, which adds only what is
// missing, the start, the services, the hand-off. On any other yard, and
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
	if next := nextStep(steps); next == nil || next.Key != stepRig {
		return nil // a yard with a rig is past its onboarding
	}
	residents, err := st.Residents(ctx)
	if err != nil {
		return err
	}
	state, err := readState(ctx, cfg, st, residents)
	if err != nil {
		return err
	}
	m := state.Mayor
	// A session that runs is not a mayor that is up (see waitMayor).
	silent := false
	for _, r := range residents {
		if r.Name == m.Name && r.Live() {
			silent = !up(ctx, r)
		}
	}
	noKind := defaultKind(ctx, st) == ""
	if !noKind && state.Daemon && m.Resident && (!m.Enabled || (m.Running && !silent)) {
		return nil
	}
	lines := state.lines(cfg.Home)
	if silent {
		lines = append(lines, docLine{tagNote, "mayor", m.Name + "'s session runs, and it has not answered its first prompt"})
	}
	if noKind {
		lines = append(lines, docLine{tagFault, "settings", "default_kind is not set: an earlier setup stopped before it marked the yard set up"})
	}
	env := homeEnv(cfg.Home)
	fmt.Printf("The yard in %s has no rig yet, and its onboarding did not reach its end. What is there:\n\n", cfg.Home)
	printLines(lines)
	fmt.Println()
	a.onboarded = true // from here on this run is the onboarding, not help
	finish := true
	what := "Waits for " + m.Name
	if !state.Daemon {
		what = "Starts the daemon in the background and waits for " + m.Name
	}
	if noKind || !m.Resident {
		what = "Adds what is missing. " + what
	}
	err = ask(huh.NewConfirm().Key("finish").Title("Finish the setup now?").
		Description(fmt.Sprintf("%s, then goes on as the onboarding does.\nNo leaves the yard as it is: %syardr init --start does the same later.", what, env)).
		Value(&finish))
	if err != nil && !errors.Is(err, huh.ErrUserAborted) {
		return err
	}
	if err != nil || !finish {
		fmt.Printf("Left as it is. To finish later: %syardr init --start, or %syardr in a terminal.\n", env, env)
		return nil
	}
	opts := initOptions{Mayor: m.Name, Start: true, resumed: true}
	res, err := a.initYard(ctx, cfg, opts)
	if err != nil {
		return err
	}
	return a.goOn(ctx, cfg, res, opts)
}

// report prints what init did and, when asked to, starts the yard in
// between: the summary first, so the wait for the mayor has something above
// it, then how the start ended. As JSON it is one object, at the end.
func (a *app) report(ctx context.Context, cfg *config.Config, res *initResult, o initOptions) error {
	if !a.json && !(o.resumed && res.MayorKept && len(res.Found) == 0) {
		// An onboarding taken up on a yard that lacked nothing has said
		// what is there (resume); the summary would say it again.
		res.printYard()
	}
	if o.Start {
		st, err := a.store(ctx)
		if err != nil {
			return err
		}
		wait := o.Wait
		if wait <= 0 {
			wait = mayorWait
		}
		res.Start = a.startYard(ctx, cfg, st, res.Mayor, wait)
		res.Daemon = daemonRunning(cfg.DaemonLock())
	}
	return a.out(res, res.printNext)
}

// initOptions are the answers of the onboarding, from its form or from
// `yardr init` flags.
type initOptions struct {
	Harness string // herdr kind: the yard's default_kind and the mayor's
	Mayor   string // the mayor resident's name
	Dir     string // where the mayor works; "" is the home's parent
	Model   string // "" is the harness's default
	// Operator is the person the role prompts name, where the templates say
	// {{operator}}; "" is $USER. Mayor goes where they say {{mayor}}.
	Operator string
	// Start starts the daemon in the background after the yard is created
	// and waits for the mayor's session, up to Wait (0 is mayorWait).
	Start bool
	Wait  time.Duration
	// Given names the flags the command line set (harness, operator,
	// mayor). On a yard that is set up, a setting whose flag was not given
	// is kept as it is. The form never runs on a set-up yard.
	Given map[string]bool
	// resumed: this init takes up an onboarding that did not reach its end
	// (resume).
	resumed bool
}

// initResult is what initYard did.
type initResult struct {
	Home     string   `json:"home"`
	Harness  string   `json:"harness"`
	Mayor    string   `json:"mayor"`
	Dir      string   `json:"dir"`
	Operator string   `json:"operator"`
	Args     []string `json:"args"`
	Roles    string   `json:"roles"`
	*roleInstall
	Daemon bool `json:"daemon"` // a daemon runs on this home
	// MayorKept: a resident of the mayor's name was there already and is
	// left as it was.
	MayorKept bool `json:"mayor_kept"`
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

// initYard creates the yard: the store, the default roles in the home, the
// operator and default_kind settings and the mayor resident. Inputs are
// checked before anything is created; roles already installed are kept when
// edited (the mayor edits its own) and refreshed to this build's when not,
// and an existing resident of the mayor's name is never replaced, so init
// runs again on a yard without harm, and is how an upgrade reaches the
// templates. The settings stored before stand in for the flags not given
// (keepSettings): init again changes only what its flags say.
//
// A run that fails leaves what it wrote before (the order is roles,
// operator and mayor, the mayor resident, default_kind), and the next run
// finishes from it: the same steps skip or keep what is there, the stored
// settings and the mayor's kind stand in for flags not given, and the result
// says what was found and what was added (partYard).
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
	if o.Mayor == "" {
		o.Mayor = "mayor"
	}
	if !groupName.MatchString(o.Mayor) {
		return nil, fmt.Errorf("mayor name %q: letters, digits, '.', '_', '-', '/'", o.Mayor)
	}
	dir, err := absDir(or(o.Dir, filepath.Dir(cfg.Home)))
	if err != nil {
		return nil, err
	}
	st, err := a.store(ctx)
	if err != nil {
		return nil, err
	}
	// Init again on a yard is no harm: an existing resident of the mayor's
	// name is kept as it is, never replaced.
	_, err = st.GetResident(ctx, o.Mayor)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	mayorKept := err == nil
	res := &initResult{Home: cfg.Home, Harness: o.Harness, Mayor: o.Mayor, Dir: dir,
		Operator: or(o.Operator, defaultOperator()),
		Args:     agents.UnattendedArgs(o.Harness), Roles: filepath.Join(cfg.Home, "roles")}
	if res.roleInstall, err = installRoles(res.Roles, res.Operator, o.Mayor, true); err != nil {
		return nil, err
	}
	res.MayorKept, res.SettingsKept = mayorKept, kept
	// operator and mayor are what rig init fills the roles with later.
	for _, kv := range [][2]string{{"operator", res.Operator}, {"mayor", o.Mayor}} {
		if err := st.SetSetting(ctx, kv[0], kv[1], a.who()); err != nil {
			return nil, err
		}
	}
	if err := a.putMayor(ctx, st, o, res); err != nil {
		return nil, err
	}
	// default_kind last: it marks the yard initialized, so an init cut
	// short onboards again.
	if err := st.SetSetting(ctx, "default_kind", o.Harness, a.who()); err != nil {
		return nil, err
	}
	if res.Found = part.found(cfg.Home); len(res.Found) > 0 {
		if len(res.Written) > 0 {
			res.Added = append(res.Added, "the role prompts "+list(bases(res.Written)))
		}
		if part.operator == "" || part.mayor == "" {
			res.Added = append(res.Added, "the operator and mayor settings")
		}
		if !mayorKept {
			res.Added = append(res.Added, "the resident "+o.Mayor)
		}
		res.Added = append(res.Added, "the default_kind setting, which marks the yard set up")
	}
	res.Daemon = daemonRunning(cfg.DaemonLock())
	return res, nil
}

// keepSettings is o with the settings the home's store holds (default_kind,
// operator, mayor) in place of the flags not given, so init again does not
// reset them to the flags' defaults (the login name, "mayor"); it reports
// which were kept, as "<setting> <value>". Where no default_kind is stored
// and no harness given, the harness is the mayor resident's kind: an init
// cut short after it added the mayor is finished on the harness it chose.
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
	}{{"harness", "default_kind", &o.Harness}, {"operator", "operator", &o.Operator}, {"mayor", "mayor", &o.Mayor}} {
		if !o.Given[s.flag] && all[s.key] != "" {
			*s.value = all[s.key]
			kept = append(kept, s.key+" "+all[s.key])
		}
	}
	if o.Harness == "" {
		if r, err := st.GetResident(ctx, or(o.Mayor, "mayor")); err == nil {
			o.Harness, _ = r.Config["kind"].(string)
		} else if !errors.Is(err, store.ErrNotFound) {
			return o, nil, err
		}
	}
	return o, kept, nil
}

// putMayor adds the mayor resident res describes, unless one is kept.
func (a *app) putMayor(ctx context.Context, st store.API, o initOptions, res *initResult) error {
	if res.MayorKept {
		return nil
	}
	conf := map[string]any{"kind": o.Harness, "prompt": filepath.Join(res.Roles, "mayor.md")}
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
	_, err := st.PutResident(ctx, store.Resident{Name: o.Mayor, Runner: "herdr", Config: conf, Dir: res.Dir,
		On: defaultOn, MaxAge: "6h", HandoffGrace: "15m"}, a.who())
	return err
}

// defaultOperator is who the role prompts name when the onboarding was not
// told: the login name, or a neutral phrase without one.
func defaultOperator() string {
	return or(os.Getenv("USER"), "the operator")
}

// shippedManifest, in the home's roles directory, records the sha256 of
// each role as init wrote it. Init again (after an upgrade, say) compares
// each installed file with it: one still as written is an untouched copy of
// an older template, and is refreshed to this build's; one that differs was
// edited, and is kept. A file from before the manifest is kept too unless
// it already reads as this build's, which is when it starts being tracked.
// A rig's .yardr/roles/ gets no manifest: rig init never overwrites, and
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
// where the templates say {{operator}} and mayor where they say {{mayor}}.
// A file already there is kept, except that with refresh one the manifest
// says was never edited is replaced with this build's (see shippedManifest).
// With only, it writes just those files (e.g. "builder.md").
//
// Each file is written whole or not at all, and the manifest is saved after
// every one: a run that fails among them leaves no half a prompt, and none
// it wrote that the next run would take for an edited one and keep, naming
// whoever the failed run was told.
func installRoles(dir, operator, mayor string, refresh bool, only ...string) (*roleInstall, error) {
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
		want := fillRole(body, operator, mayor)
		path := filepath.Join(dir, f.Name())
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
			manifest[f.Name()] = digest(want)
			continue
		case manifest[f.Name()] == digest(have):
			res.Refreshed = append(res.Refreshed, path)
		default:
			res.Kept = append(res.Kept, path)
			continue
		}
		if err := writeWhole(path, want); err != nil {
			return nil, err
		}
		manifest[f.Name()] = digest(want)
		if err := save(); err != nil {
			return nil, err
		}
	}
	if err := save(); err != nil {
		return nil, err
	}
	return res, nil
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

// fillRole puts the names into a role template's placeholders.
func fillRole(body []byte, operator, mayor string) []byte {
	body = bytes.ReplaceAll(body, []byte("{{operator}}"), []byte(operator))
	return bytes.ReplaceAll(body, []byte("{{mayor}}"), []byte(mayor))
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
	env := homeEnv(r.Home)
	switch {
	case len(r.Found) > 0:
		// An init cut short left part of it; this run finished it.
		fmt.Printf("yard in %s finished: part of it was there, from a setup that did not reach its end\n\n", r.Home)
		fmt.Printf("  found    %s\n", strings.Join(r.Found, "; "))
		fmt.Printf("  added    %s\n", strings.Join(r.Added, "; "))
	case r.MayorKept:
		fmt.Printf("yard in %s set up again\n\n", r.Home)
	default:
		fmt.Printf("yard created in %s\n\n", r.Home)
	}
	fmt.Printf("  harness  %s (the yard's default_kind: groups and residents get it when --set kind is omitted)\n", r.Harness)
	if len(r.SettingsKept) > 0 {
		fmt.Printf("  kept     %s, as set before: --harness, --operator and --mayor change them\n", strings.Join(r.SettingsKept, ", "))
	}
	if r.MayorKept {
		fmt.Printf("  mayor    %s, already a resident: kept as it was\n", r.Mayor)
	} else {
		fmt.Printf("  mayor    %s, working in %s, rotating every 6h\n", r.Mayor, r.Dir)
		if len(r.Args) > 0 {
			fmt.Printf("           runs with %s so it does not stop to ask\n", strings.Join(r.Args, " "))
		} else {
			fmt.Printf("           runs with no unattended flags: %s may stop to ask; give it its own with\n"+
				"           %syardr resident add %s --set 'args=[...]' (see yardr group add --help)\n", r.Harness, env, r.Mayor)
		}
	}
	fmt.Printf("  roles    %s", r.Roles)
	if s := r.summary(); s != "" {
		fmt.Printf(" (%s)", s)
	}
	fmt.Printf("\n           starting points naming %s: edit them to fit your yard; the mayor's prompt is mayor.md.\n"+
		"           A rig's own roles go in its repo under .yardr/roles/, which a group's\n"+
		"           prompt=roles/<name>.md reads first (from the rig's base branch).\n\n", r.Operator)
	if env != "" {
		// Not the default home: without the variable a command goes to
		// ~/.yardr, the daemon's first of all (yard-0wys).
		fmt.Printf("This yard is not in the default home: every yardr command for it needs\n"+
			"YARDR_HOME=%s in front of it, or exported.\n", shellQuote(r.Home))
	}
}

// printNext is the way from the yard to the mayor: how the start ended
// when init started the yard, else how to start it.
func (r *initResult) printNext() {
	env := homeEnv(r.Home)
	switch {
	case r.Start != nil:
		r.Start.print(r.Mayor)
		if !r.Start.Running {
			return
		}
	case r.Daemon:
		fmt.Printf("The daemon is running; it starts %s shortly. Where %s is then: %syardr init --start\n", r.Mayor, r.Mayor, env)
	default:
		fmt.Printf("Start the daemon in a terminal pane of its own, to watch it; %s starts when it does:\n\n  %syardr daemon\n\n"+
			"Or in the background, waiting for %s and printing where it is:\n\n  %syardr init --start\n",
			r.Mayor, env, r.Mayor, env)
	}
	// The rest of the setup is a conversation, or a run of forms: the
	// mayor's first run and yardr setup go by the same steps (setupSteps).
	// The commands for scripts are in yardr init --help.
	fmt.Printf("\n%s sets up the rest with you in its pane: the repository to work in, who\n"+
		"builds and reviews, and a first bead to try it on. Or here, as a short run of\n"+
		"forms: %syardr setup\n", r.Mayor, env)
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

// onboardForm asks for the harness, the mayor's name, its directory and its
// model, and whether to start the yard. Each answer starts at its default,
// so accepting every page gives a working yard with its mayor running.
//
// part is what an earlier setup, cut short, left in the home: the form says
// so, and the mayor's name and yours start at what that run stored.
func onboardForm(home string, part *partYard) (initOptions, error) {
	opts, def := harnessOptions()
	o := initOptions{Harness: def, Mayor: "mayor", Dir: filepath.Dir(home), Operator: defaultOperator(), Start: true,
		// The form's answers are given, whatever the store holds.
		Given: map[string]bool{"harness": true, "operator": true, "mayor": true}}
	fmt.Printf("No yard in %s yet. This sets one up: a default agent harness, and a mayor on it.\n\n", home)
	if part != nil {
		o.Mayor, o.Operator = or(part.mayor, o.Mayor), or(part.operator, o.Operator)
		fmt.Printf("An earlier setup of this home did not reach its end. It left %s.\n"+
			"This one goes on from that, and says at its end what it added.\n\n", strings.Join(part.found(home), "; "))
	}
	if err := preflight(); err != nil {
		return o, err
	}
	if err := ask(huh.NewSelect[string]().Title("Harness").
		Description("The agent yardr starts by default, for the mayor and for groups that name none.").
		Options(opts...).Height(min(len(opts)+2, 14)).Value(&o.Harness)); err != nil {
		return o, err
	}
	if err := ask(
		huh.NewInput().Title("Mayor").Description("The always-on resident that coordinates the yard.").
			Value(&o.Mayor).Validate(func(s string) error {
			if !groupName.MatchString(s) {
				return errors.New("letters, digits, '.', '_', '-', '/'")
			}
			return nil
		}),
		huh.NewInput().Title("Directory").
			Description("Where the mayor's shell lives: its session starts there, and relative paths it uses resolve from there.").
			Value(&o.Dir).Validate(func(s string) error { _, err := absDir(s); return err }),
		huh.NewInput().Title("Model").Description("Optional; blank uses "+o.Harness+"'s default.").Value(&o.Model),
		huh.NewInput().Title("Your name").
			Description("How the role prompts name you: the person agents bring decisions to.").
			Value(&o.Operator),
	); err != nil {
		return o, err
	}
	create := true
	summary := fmt.Sprintf("harness %s\nmayor %s in %s\nmodel %s\noperator %s\nroles %s (starting points; a rig's own go in its repo's .yardr/roles/)",
		o.Harness, o.Mayor, o.Dir, or(o.Model, "the harness's default"), or(o.Operator, defaultOperator()), filepath.Join(home, "roles"))
	if err := ask(huh.NewConfirm().Title("Create this yard?").Description(summary).Value(&create)); err != nil {
		return o, err
	}
	if !create {
		return o, huh.ErrUserAborted
	}
	if err := ask(huh.NewConfirm().Title("Start the yard now?").
		Description(fmt.Sprintf("Starts the daemon in the background and waits for %s. No leaves that to you: yardr daemon, in a pane of its own.", o.Mayor)).
		Value(&o.Start)); err != nil {
		return o, err
	}
	return o, nil
}

func (a *app) initCmd() *cobra.Command {
	var o initOptions
	cmd := &cobra.Command{
		Use:   "init [--harness <kind>]",
		Short: "Create the yard: default harness, roles and the mayor (what the onboarding does)",
		Long: `Create the yard in YARDR_HOME (default ~/.yardr) without the onboarding form,
for scripts and tests. It writes the default role prompts to <home>/roles/,
naming --operator as the person they bring decisions to and --mayor as the
resident they mail about what blocks them. A prompt already there is kept
when it was edited, and replaced with this build's when it still reads as
init wrote it (<home>/roles/.shipped.json records what was written), so
init again after an upgrade refreshes the templates; the summary says which.
They are generic starting points: a rig's own roles belong in
its repo under .yardr/roles/, which groups find first. It stores --harness as the yard's default_kind, and
adds the mayor resident on that harness with its unattended flags, rotating
every 6h. The daemon starts the mayor.

Init does not start the daemon unless told to: --start starts one in the
background (none is started when one runs on the home already), logging to
<home>/daemon.log, and waits up to --wait for the mayor: its session running
and its first output in its pane, after its first prompt. It then
prints where the mayor is in herdr (agent, workspace, tab, pane) and how to
get there, or, when the mayor did not come up, what is known (the last lines
of its pane among it) and how to go on, and exits 1. It is what the onboarding form does on "Start the yard now?".
Run it again on a running yard to be told where the mayor is. yardr daemon in
a terminal pane of its own does the same in the foreground, to watch it.

Run yardr in a terminal on a home with no yard to get the same as a form. A
home counts as set up once init has stored default_kind, or once its store
has rigs, groups, residents or beads (a yard from before the onboarding); the
store file alone does not, since the server creates it on any first command.
Init again on a yard keeps its default_kind, operator and mayor settings and
an existing mayor resident; only the flags given change them, so --harness is
required only to create a yard.

A run that failed leaves what it made before the failure (the order is: the
roles, the operator and mayor settings, the mayor, default_kind), and init
again finishes from it. The settings stored stand in for the flags not given,
and the mayor's kind for --harness once the mayor is there; the summary then
says "finished", with what it found and what it added (--json: found, added).
On a yard that is set up and did not start, init --start starts and waits
again. Bare yardr in a terminal does the same on a yard with no rig yet,
after saying what is there and asking, and then goes on as the onboarding
does. docs/configuration.md has both, and how to remove a yard and start
over.

After init the mayor sets up the rest in conversation, or yardr setup does in
a terminal, as forms. For scripts, the same by hand:

  yardr rig add webapp ~/projects/webapp --base origin/main
  yardr rig init webapp        # starter roles, gate and flow; commit them on the base
  yardr group add webapp/builders --runner herdr --limit 2 --set prompt=roles/builder.md
  yardr route add --stage new --rig webapp --group webapp/builders

Unattended flags per harness:
` + agents.UnattendedHelp(),
		Example: `  yardr init --harness claude
  yardr init --harness claude --start              # and start the yard, waiting for the mayor
  YARDR_HOME=/tmp/y1 yardr init --harness codex --mayor boss --dir ~/projects --model gpt-5.5 --operator Jo`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			o.Given = map[string]bool{}
			for _, f := range []string{"harness", "operator", "mayor"} {
				o.Given[f] = c.Flags().Changed(f)
			}
			res, err := a.initYard(c.Context(), cfg, o)
			if err != nil {
				return err
			}
			if err := a.report(c.Context(), cfg, res, o); err != nil {
				return err
			}
			if res.Start != nil && !res.Start.Running {
				// The report said what is known; a script reads the exit.
				return fmt.Errorf("%s is not up", res.Mayor)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&o.Harness, "harness", "", "agent kind herdr starts: "+strings.Join(agents.Kinds(), ", ")+
		" (required to create a yard; a set-up yard keeps its default_kind)")
	cmd.Flags().StringVar(&o.Mayor, "mayor", "mayor", "the mayor resident's name (a set-up yard keeps its mayor setting)")
	cmd.Flags().StringVar(&o.Dir, "dir", "", "where the mayor works (default: the home's parent)")
	cmd.Flags().StringVar(&o.Model, "model", "", "the mayor's model (default: the harness's own)")
	cmd.Flags().BoolVar(&o.Start, "start", false, "start the daemon in the background, unless one runs, and wait for the mayor's session")
	cmd.Flags().DurationVar(&o.Wait, "wait", mayorWait, "with --start: how long to wait for the mayor")
	cmd.Flags().StringVar(&o.Operator, "operator", "", "your name, as the role prompts use it (default: $USER; a set-up yard keeps its operator setting)")
	return cmd
}

// settings are the yard's settings: what each means and what it accepts.
var settings = map[string]struct {
	help  string
	check func(string) error
}{
	"operator": {"who the role prompts name, the person agents bring decisions to (yardr rig init's default)",
		func(string) error { return nil }},
	"mayor": {"the mayor resident's name, whom the role prompts tell agents to mail (yardr rig init's default)", func(v string) error {
		if !groupName.MatchString(v) {
			return fmt.Errorf("mayor name %q: letters, digits, '.', '_', '-', '/'", v)
		}
		return nil
	}},
	"default_kind": {"the agent kind of herdr groups and residents whose config names none", func(v string) error {
		if !agents.IsKind(v) {
			return fmt.Errorf("default_kind %q: herdr starts %s", v, strings.Join(agents.Kinds(), ", "))
		}
		return nil
	}},
	"name": {"the yard's name, which its residents' herdr agents and workspaces carry (<name>-mayor); default: the home's directory name", func(v string) error {
		if !groupName.MatchString(v) {
			return fmt.Errorf("yard name %q: letters, digits, '.', '_', '-', '/'", v)
		}
		return nil
	}},
}

func (a *app) settingsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "settings", Short: "The yard's own defaults (default_kind, mayor, name, operator)"}
	known := func(key string) error {
		if _, ok := settings[key]; !ok {
			return fmt.Errorf("no setting %q; there is: %s", key, strings.Join(sortedKeys(settings), ", "))
		}
		return nil
	}
	get := &cobra.Command{
		Use:   "get [<key>]",
		Short: "Print a setting, or all of them",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				if err := known(args[0]); err != nil {
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
		Use:     "set <key> <value>",
		Short:   "Change a setting; an empty value unsets it",
		Example: `  yardr settings set default_kind codex`,
		Args:    cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			if err := known(args[0]); err != nil {
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
