package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

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
	if a.st == nil {
		// No store file: nothing to connect to, and connecting would create one.
		if _, err := os.Stat(cfg.DBPath()); errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
	}
	st, err := a.store(ctx)
	if err != nil {
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
// and store compact, which needs the file to itself: the check would connect
// to the server, starting one, and the compaction would then always find
// the store in use.
var noOnboarding = []string{"server", "init", "daemon", "settings", "version", "help", "completion",
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
	if ok, err := a.initialized(c.Context(), cfg); err != nil || ok {
		return err
	}
	if !interactive() {
		return notInitialized{cfg.Home}
	}
	opts, err := onboardForm(cfg.Home)
	if errors.Is(err, huh.ErrUserAborted) {
		return fmt.Errorf("onboarding cancelled; nothing was created in %s", cfg.Home)
	} else if err != nil {
		return err
	}
	res, err := a.initYard(c.Context(), cfg, opts)
	if err != nil {
		return err
	}
	a.onboarded = true
	return a.out(res, res.print)
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
	// Given names the flags the command line set (harness, operator,
	// mayor). On a yard that is set up, a setting whose flag was not given
	// is kept as it is. The form never runs on a set-up yard.
	Given map[string]bool
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
	Written  []string `json:"written"` // role files installed now
	Kept     []string `json:"kept"`    // role files already there, left as they were
	Daemon   bool     `json:"daemon"`  // a daemon runs on this home
	// MayorKept: a resident of the mayor's name was there already and is
	// left as it was.
	MayorKept bool `json:"mayor_kept"`
	// SettingsKept are the settings of a set-up yard this init left as they
	// were ("operator Jo"), their flags not given.
	SettingsKept []string `json:"settings_kept"`
}

// initYard creates the yard: the store, the default roles in the home, the
// operator and default_kind settings and the mayor resident. Inputs are
// checked before anything is created; roles already installed are kept (the
// mayor edits its own), and an existing resident of the mayor's name is never
// replaced, so init runs again on a yard without harm. On a yard that is
// set up, the settings stored before stand in for the flags not given
// (keepSettings): init again changes only what its flags say.
func (a *app) initYard(ctx context.Context, cfg *config.Config, o initOptions) (*initResult, error) {
	setUp, err := a.initialized(ctx, cfg)
	if err != nil {
		return nil, err
	}
	var kept []string
	if setUp {
		if o, kept, err = a.keepSettings(ctx, o); err != nil {
			return nil, err
		}
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
	if res.Written, res.Kept, err = installRoles(res.Roles, res.Operator, o.Mayor); err != nil {
		return nil, err
	}
	res.MayorKept, res.SettingsKept = mayorKept, kept
	// operator and mayor are what rig init fills the roles with later.
	for k, v := range map[string]string{"operator": res.Operator, "mayor": o.Mayor} {
		if err := st.SetSetting(ctx, k, v, a.who()); err != nil {
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
	res.Daemon = daemonRunning(cfg.DaemonLock())
	return res, nil
}

// keepSettings is o with the settings a set-up yard holds (default_kind,
// operator, mayor) in place of the flags not given, so init again does not
// reset them to the flags' defaults (the login name, "mayor"); it reports
// which were kept, as "<setting> <value>".
func (a *app) keepSettings(ctx context.Context, o initOptions) (initOptions, []string, error) {
	st, err := a.store(ctx)
	if err != nil {
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

// installRoles writes the embedded role prompts into dir, naming operator
// where the templates say {{operator}} and mayor where they say {{mayor}},
// and keeps any file already there.
func installRoles(dir, operator, mayor string) (written, kept []string, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	files, err := fs.ReadDir(roles.FS, ".")
	if err != nil {
		return nil, nil, err
	}
	for _, f := range files {
		body, err := roles.FS.ReadFile(f.Name())
		if err != nil {
			return nil, nil, err
		}
		path := filepath.Join(dir, f.Name())
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			kept = append(kept, path)
			continue
		} else if err != nil {
			return nil, nil, err
		}
		_, err = out.Write(fillRole(body, operator, mayor))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, nil, err
		}
		written = append(written, path)
	}
	return written, kept, nil
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

func (r *initResult) print() {
	env := homeEnv(r.Home)
	if r.MayorKept {
		fmt.Printf("yard in %s set up again\n\n", r.Home)
	} else {
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
	if len(r.Kept) > 0 {
		fmt.Printf(" (%d already there, kept)", len(r.Kept))
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
	if r.Daemon {
		fmt.Printf("The daemon is running; it starts %s shortly.\n", r.Mayor)
	} else {
		fmt.Printf("Start the daemon in a terminal pane of its own; %s starts when it does:\n\n  %syardr daemon\n", r.Mayor, env)
	}
	// The rest of the setup is a conversation: the mayor's role has a first
	// run checklist (rig, starter files, groups, routes). The commands for
	// scripts are in yardr init --help.
	fmt.Printf("\n%s sets up the rest with you in its pane: the repository to work in, who\n"+
		"builds and reviews, and a first bead to try it on.\n", r.Mayor)
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
		if _, err := lookPath(agents.Executable(k)); err == nil {
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
// model. Each answer starts at its default, so accepting every page gives a
// working yard.
func onboardForm(home string) (initOptions, error) {
	opts, def := harnessOptions()
	o := initOptions{Harness: def, Mayor: "mayor", Dir: filepath.Dir(home), Operator: defaultOperator()}
	fmt.Printf("No yard in %s yet. This sets one up: a default agent harness, and a mayor on it.\n\n", home)
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
	return o, nil
}

func (a *app) initCmd() *cobra.Command {
	var o initOptions
	cmd := &cobra.Command{
		Use:   "init [--harness <kind>]",
		Short: "Create the yard: default harness, roles and the mayor (what the onboarding does)",
		Long: `Create the yard in YARDR_HOME (default ~/.yardr) without the onboarding form,
for scripts and tests. It writes the default role prompts to <home>/roles/
(keeping any already there), naming --operator as the person they bring
decisions to and --mayor as the resident they mail about what blocks them.
They are generic starting points: a rig's own roles belong in
its repo under .yardr/roles/, which groups find first. It stores --harness as the yard's default_kind, and
adds the mayor resident on that harness with its unattended flags, rotating
every 6h. The daemon starts the mayor.

Run yardr in a terminal on a home with no yard to get the same as a form. A
home counts as set up once init has stored default_kind, or once its store
has rigs, groups, residents or beads (a yard from before the onboarding); the
store file alone does not, since the server creates it on any first command.
Init again on a yard keeps its default_kind, operator and mayor settings and
an existing mayor resident; only the flags given change them, so --harness is
required only to create a yard.

After init the mayor sets up the rest in conversation. For scripts, the same
by hand:

  yardr rig add webapp ~/projects/webapp --base origin/main
  yardr rig init webapp        # starter roles, gate and flow; commit them on the base
  yardr group add webapp/builders --runner herdr --limit 2 --set prompt=roles/builder.md
  yardr route add --stage new --rig webapp --group webapp/builders

Unattended flags per harness:
` + agents.UnattendedHelp(),
		Example: `  yardr init --harness claude
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
			return a.out(res, res.print)
		},
	}
	cmd.Flags().StringVar(&o.Harness, "harness", "", "agent kind herdr starts: "+strings.Join(agents.Kinds(), ", ")+
		" (required to create a yard; a set-up yard keeps its default_kind)")
	cmd.Flags().StringVar(&o.Mayor, "mayor", "mayor", "the mayor resident's name (a set-up yard keeps its mayor setting)")
	cmd.Flags().StringVar(&o.Dir, "dir", "", "where the mayor works (default: the home's parent)")
	cmd.Flags().StringVar(&o.Model, "model", "", "the mayor's model (default: the harness's own)")
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
