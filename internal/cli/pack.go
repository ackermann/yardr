package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// packFlags are what pack check and pack apply both take: where the pack
// goes and what a pack cannot say for itself.
type packFlags struct {
	opts      pack.Options
	residents []string
}

func (f *packFlags) register(c *cobra.Command, verb string) {
	c.Flags().StringVar(&f.opts.Rig, "rig", "", "the rig the pack "+verb+" imported into")
	c.Flags().StringVar(&f.opts.Flow, "flow", "", "the pack's flow the rig follows (default: the one it was given by the last import, else the pack's first flow without a type)")
	c.Flags().StringArrayVar(&f.residents, "resident", nil, "<pack's name>=<yard's resident>: which resident takes a resident of the pack (repeatable; default: as at the last import)")
	c.Flags().BoolVar(&f.opts.RigRoles, "rig-roles", false, "leave a group or resident its rig's own .yardr/roles/<name>.md where the rig has one, instead of setting the pack's role (default: as at the last import, else the pack's)")
	c.Flags().BoolVar(&f.opts.PackRoles, "pack-roles", false, "set the pack's roles everywhere: the default now, needed only to undo an earlier --rig-roles")
	c.MarkFlagsMutuallyExclusive("rig-roles", "pack-roles")
	_ = c.MarkFlagRequired("rig")
}

// loadPack reads the pack in dir. With --json an invalid pack's mistakes
// are printed as data, and the error is the exit code alone: Execute adds
// nothing to what a red check has printed.
func (a *app) loadPack(dir string) (*pack.Pack, error) {
	p, err := pack.Load(dir)
	var errs pack.Errors
	if a.json && errors.As(err, &errs) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"error": "the pack is not valid", "errors": errs})
		return nil, errCheckFailed
	}
	return p, err
}

// packOptions completes f's options for this yard and returns its store
// and configuration.
func (a *app) packOptions(ctx context.Context, f *packFlags) (store.API, *config.Config, error) {
	if f.opts.PackRoles {
		// To stderr: --json's output stays the plan alone.
		fmt.Fprintln(os.Stderr, "--pack-roles: the pack's roles are the default now; the flag is only needed to undo an earlier --rig-roles")
	}
	f.opts.Residents = map[string]string{}
	for _, kv := range f.residents {
		from, to, ok := strings.Cut(kv, "=")
		if !ok || from == "" || to == "" {
			return nil, nil, fmt.Errorf("--resident %q: want <pack's name>=<yard's resident>", kv)
		}
		f.opts.Residents[from] = to
	}
	st, err := a.store(ctx)
	if err != nil {
		return nil, nil, err
	}
	cfg := a.cfg
	if cfg == nil {
		if cfg, err = config.Load(); err != nil {
			return nil, nil, err
		}
	}
	f.opts.Home = cfg.Home
	// Whom yardr init and rig init name in the roles they write.
	f.opts.Operator = yardOperator(ctx, st)
	return st, cfg, nil
}

func (a *app) packCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pack", Short: "A yard's way of working as a directory: flows, groups, roles, residents"}

	var cf packFlags
	check := &cobra.Command{
		Use:   "check <dir> --rig <rig>",
		Short: "Read a pack and show what importing it into a rig would change",
		Long: `Reads the pack in <dir> (pack.yaml, roles/, scripts/), checks it, and prints
what importing it into the rig would do to this yard, object by object:
create, change (field: old -> new), unchanged, not in the pack (the yard
has it, the pack does not, and an import leaves it alone), or no longer in
the pack (an earlier import brought it, the pack has dropped it since, and
pack apply --prune removes it). Then what an import would ask, which flow
would govern the rig, what each source a resident subscribes to still
lacks, and which sources the pack asks that a resident may open threads in
(open_threads): an import asks the operator, and only about a source the
yard has a hook and a credential for. For each source the pack carries a
stage map for (sources.<source>: as, hears), the plan shows the map, the
yard's resident it would be acted on as, whether the yard has a hook of
that source and whether that resident may set a status there (set_state),
with the command for whatever is missing. Nothing is written.

The pack's role is set on every agent group and resident it names, also
where the rig has its own .yardr/roles/<name>.md: the plan names that file
as shadowed. --rig-roles keeps the rig's own role instead.

A role or stage file is installed with {{operator}} filled with the yard's
operator and {{<resident>}} with the yard's resident that takes that
resident of the pack; any other {{...}} in one makes the pack invalid.

It exits 1 on a pack that is not valid, naming every mistake by file, line
and key, and on a plan an import would refuse (open beads in a stage the
pack's flow lacks). --json prints the plan as data. See docs/packs.md.`,
		Example: `  yardr pack check packs/starter --rig yardr
  yardr pack check ~/packs/starter --rig yardr --resident mayor=yardmaster`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := a.loadPack(args[0])
			if err != nil {
				return err
			}
			st, _, err := a.packOptions(c.Context(), &cf)
			if err != nil {
				return err
			}
			plan, err := pack.Build(c.Context(), st, p, cf.opts)
			if err != nil {
				return err
			}
			if err := a.out(plan, func() { printPlan(plan, nil, nil, nil) }); err != nil {
				return err
			}
			if len(plan.Refused) > 0 {
				return errCheckFailed
			}
			return nil
		},
	}
	cf.register(check, "would be")

	var (
		af      packFlags
		binds   []string
		members []string
		opens   []string
		yes     bool
		prune   bool
	)
	apply := &cobra.Command{
		Use:   "apply <dir> --rig <rig>",
		Short: "Import a pack into a rig: set the yard to what the pack says",
		Long: `Does what pack check shows: prints the plan, with what the flags answer
filled in, asks for what a pack cannot say, then sets the yard to the pack's
state. Everything is matched by name,
so an object the yard has is overwritten in what the pack carries, never
added a second time, and keeps what the pack does not carry (a group's
agent, model, arguments and members; a resident's identity and directory).
Applying the same pack again changes nothing and logs one pack_unchanged
event.

The pack's roles win: a group or resident gets the pack's copied role also
where the rig has its own .yardr/roles/<name>.md, which the plan names as
shadowed. --rig-roles keeps the rig's own role instead; the import records
the choice and the next one repeats it. A copy under <home>/packs/<pack>/
that was edited by hand is overwritten only with --yes: the edit belongs in
the pack's source.

What it asks, and the flag that answers instead:
  the agent and model of a group the yard lacks   --bind <group>=<kind>[:<model>][,<preset>]
  the members of a people group the yard lacks    --members <group>=a,b   (empty: anyone)
  which resident takes a resident of the pack     --resident <pack's>=<yard's>
  the agent of a resident the yard lacks          --bind resident:<pack's>=<kind>[:<model>][,<preset>]
  which flow, when the pack has several           --flow <name>
  whether a resident may open threads in the      --open-threads <pack's>=linear,github
  sources the pack asks for (open_threads)        (=none for no)
<group> is the pack's name for it (builders). Without a terminal nothing is
asked: a missing answer is an error, never a default, and --yes is needed
to apply.

A pack may ask that its resident may open threads in a source. That is the
operator's to grant, so the import asks, last and with no as the default,
and only about a source this yard has a hook and a credential for; the plan
says what any other source lacks. Without a terminal, with --json or with
--yes, nothing is granted unless --open-threads says so, and the output
names the flag. Run by anyone but the operator the import does the rest and
grants nothing. An answer is recorded, so it is asked once.

A pack may carry a stage map for a source (sources.<source>: as, hears):
which status an issue goes to when the bead tracking it enters a stage. The
import sets it, and the resident it is acted on as, on the yard's hook of
that source; a hook that has another map gets the pack's, and the plan shows
both. It never makes a hook: without one the import goes through, sets
nothing for that source and names the command, and the next apply sets it.
It never lets the resident set a status either: until the operator has
(yardr resident set <resident> set_state=<source>), the map moves nothing, and
the plan and the output say so. As with yardr hook add --as, only the
operator or that resident's own session may have a hook act as a resident:
run by anyone else the import does the rest and sets no map.

The one answer with a default is what a new group's or resident's agent may
do without asking. <preset> names one of the agent's permission presets by
the label the group form shows (claude: auto, accept-edits, bypass; none
for no arguments). Left out, a terminal asks with the same list; without a
terminal, or with --yes, the agent gets the arguments yardr init gives it:
` + agents.UnattendedHelp() + `
The plan prints the arguments each new group and resident gets. Those of a
group or resident the yard already has are never touched.

Refused, changing nothing, while open beads sit in a stage the pack's flow
lacks; the message names them. Objects an earlier import brought and the
pack has dropped since are listed, and removed only with --prune. A running
resident whose role or subscriptions change is not restarted; the output
says so. The import is recorded (yardr pack list). See docs/packs.md.`,
		Example: `  yardr pack apply packs/starter --rig web
  yardr pack apply packs/starter --rig web --yes --resident mayor=yardmaster \
    --bind builders=claude:opus --bind reviewers=codex,approve-for-me --members backlog= --members decisions=mckean`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			p, err := a.loadPack(args[0])
			if err != nil {
				return err
			}
			st, cfg, err := a.packOptions(ctx, &af)
			if err != nil {
				return err
			}
			asking := interactive() && !a.json
			plan, err := pack.Build(ctx, st, p, af.opts)
			if err != nil {
				return err
			}
			// Which flow and which residents shape the plan itself, so they
			// are settled first and the plan made again.
			if open := shaping(plan); len(open) > 0 {
				if !asking {
					return fmt.Errorf("pack apply cannot ask without a terminal: %s", strings.Join(flagsFor(plan, open), "; "))
				}
				if err := a.askShape(ctx, st, p, plan, &af.opts); err != nil {
					return cancelled(err)
				}
				if plan, err = pack.Build(ctx, st, p, af.opts); err != nil {
					return err
				}
			}
			if len(plan.Refused) > 0 {
				_ = a.out(plan, func() { printPlan(plan, nil, nil, nil) })
				return errCheckFailed
			}
			// The flags' answers go into the printed plan, so it shows what
			// each new group and resident gets and lists only what is still
			// open. Where nothing can be asked, or --yes says not to, a new
			// agent gets its kind's unattended arguments.
			answers, err := packAnswers(plan, binds, members, cfg.Home, !asking || yes)
			if err != nil {
				return err
			}
			// Whether a resident may open threads is asked last, and only
			// where a no can be said: --yes says not to ask.
			open, err := a.openThreads(ctx, st, plan, opens, asking && !yes)
			if err != nil {
				return err
			}
			// A hook that acts as a resident speaks as it: the line yardr
			// hook add --as draws, and the rest of the import is done.
			refused := map[string]string{}
			for _, o := range plan.Objects {
				if o.Kind != "map" || o.Map == nil || (o.Action != pack.Create && o.Action != pack.Change) {
					continue
				}
				if err := a.maySpeakAs(ctx, st, "pack apply: the stage map of "+o.Map.Source, o.Map.As); err != nil {
					refused[o.Map.Source] = err.Error()
				}
			}
			if !a.json {
				printPlan(plan, answers, open.Answers, refused)
			}
			// A copy someone edited is not overwritten on a confirmation
			// alone: the edit is lost for good, and its place is the pack.
			if edited := plan.EditedFiles(); len(edited) > 0 && !yes {
				return fmt.Errorf("nothing was changed: %s; --yes proceeds", strings.Join(edited, "; "))
			}
			if missing := plan.Missing(answers); len(missing) > 0 {
				if !asking {
					return fmt.Errorf("pack apply cannot ask without a terminal: %s", strings.Join(flagsFor(plan, missing), "; "))
				}
				fmt.Println()
				if err := a.askAnswers(ctx, st, plan, answers); err != nil {
					return cancelled(err)
				}
			}
			todo := 0
			for _, o := range plan.Objects {
				if o.Kind == "map" && o.Map != nil && refused[o.Map.Source] != "" {
					continue
				}
				if o.Action == pack.Create || o.Action == pack.Change || (prune && o.Action == pack.Dropped && o.Held == "") {
					todo++
				}
			}
			if !asking && !yes && todo+len(open.Answers) > 0 {
				return errors.New("pack apply changes the yard: give --yes, or run it in a terminal to be asked")
			}
			if todo > 0 && !yes {
				ok := true
				title := fmt.Sprintf("Apply pack %s to rig %s?", plan.Pack, plan.Rig)
				if err := ask(huh.NewConfirm().Title(title).Description(fmt.Sprintf("%d objects to write or remove.", todo)).Value(&ok)); err != nil {
					return cancelled(err)
				}
				if !ok {
					fmt.Println("not applied")
					return nil
				}
			}
			if err := open.ask(); err != nil {
				return cancelled(err)
			}
			res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: answers, OpenThreads: open.Answers, MapRefused: refused, Prune: prune, Overwrite: yes, Actor: a.who()})
			if res != nil {
				if oerr := a.out(map[string]any{"plan": plan, "answers": answers, "open_threads": open, "maps_refused": refused, "result": res}, func() { printResult(plan, res, prune, answers, open, refused) }); oerr != nil {
					return oerr
				}
			}
			return err
		},
	}
	af.register(apply, "is")
	apply.Flags().StringArrayVar(&binds, "bind", nil, "<group>=<kind>[:<model>][,<preset>]: the agent, model and permission preset of a group the yard lacks; resident:<name>=… for a resident (repeatable)")
	apply.Flags().StringArrayVar(&members, "members", nil, "<group>=a,b: the members of a people group the yard lacks; empty for anyone (repeatable)")
	apply.Flags().StringArrayVar(&opens, "open-threads", nil, "<pack's resident>=linear,github: let the resident that takes it open threads in these of the sources the pack asks for, and in no other; =none for no (repeatable)")
	apply.Flags().BoolVar(&yes, "yes", false, "apply without asking for confirmation, and overwrite an installed file that was edited by hand")
	apply.Flags().BoolVar(&prune, "prune", false, "remove what an earlier import of this pack brought and the pack has dropped since")

	list := &cobra.Command{
		Use: "list", Short: "List the packs imported into this yard", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			recs, err := st.Packs(c.Context())
			if err != nil {
				return err
			}
			return a.out(recs, func() {
				if len(recs) == 0 {
					fmt.Println("no pack is imported (yardr pack apply <dir> --rig <rig>)")
				}
				for i, r := range recs {
					if i > 0 {
						fmt.Println()
					}
					printRecord(r)
				}
			})
		},
	}

	cmd.AddCommand(check, apply, list)
	return cmd
}

// cancelled turns a form the user left into a plain ending.
func cancelled(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		fmt.Println("cancelled; nothing was changed")
		return nil
	}
	return err
}

// shaping are the plan's questions whose answers change the plan itself:
// which flow the rig follows, and which resident takes a pack's.
func shaping(p *pack.Plan) []pack.Question {
	var out []pack.Question
	for _, q := range p.Questions {
		if q.ID == pack.FlowQuestion || q.Field == "resident" {
			out = append(out, q)
		}
	}
	return out
}

// packName is the pack's name for the yard's object: its key in names
// (pack name -> yard name).
func packName(names map[string]string, yard string) string {
	for k, v := range names {
		if v == yard {
			return k
		}
	}
	return yard
}

// flagsFor names, for each open question, the flag that answers it.
func flagsFor(p *pack.Plan, open []pack.Question) []string {
	var out []string
	for _, q := range open {
		kind, name, _ := strings.Cut(q.Object, " ")
		var flag string
		switch {
		case q.ID == pack.FlowQuestion:
			flag = "--flow <name> (" + q.Ask + ")"
		case q.Field == "resident":
			flag = fmt.Sprintf("--resident %s=<resident> (%s)", packName(p.Residents, name), q.Ask)
		case kind == "group" && q.Field == "members":
			flag = fmt.Sprintf("--members %s=<a,b> for the new group %s (empty: anyone)", packName(p.Groups, name), name)
		case kind == "group":
			flag = fmt.Sprintf("--bind %s=<kind>[:<model>] for the new group %s", packName(p.Groups, name), name)
		case kind == "resident":
			flag = fmt.Sprintf("--bind resident:%s=<kind>[:<model>] for the new resident %s", packName(p.Residents, name), name)
		default:
			flag = q.ID + ": " + q.Ask
		}
		if !slices.Contains(out, flag) {
			out = append(out, flag)
		}
	}
	return out
}

// packAnswers reads --bind and --members into answers to the plan's
// questions. A flag must name a group or resident of the pack; one for an
// object the yard already has answers nothing, since the yard's binding is
// kept, and is not an error, so the same command can be run again. A new
// resident works in the yard's home, as one made by resident add does.
// With unattended, a new agent whose kind is answered and whose --bind names
// no preset gets its kind's unattended arguments, as yardr init gives them.
func packAnswers(p *pack.Plan, binds, members []string, home string, unattended bool) (pack.Answers, error) {
	answers := pack.Answers{}
	asked := map[string]bool{}
	for _, q := range p.Questions {
		asked[q.ID] = true
		if strings.HasPrefix(q.ID, "resident.") && q.Field == "dir" {
			answers[q.ID] = home
		}
	}
	set := func(id, v string) {
		if asked[id] {
			answers[id] = v
		}
	}
	for _, kv := range binds {
		name, v, ok := strings.Cut(kv, "=")
		// The preset goes after a comma, not a second colon: a model may
		// have colons of its own (openrouter/…:free).
		v, preset, hasPreset := strings.Cut(v, ",")
		kind, model, _ := strings.Cut(v, ":")
		if !ok || name == "" || kind == "" {
			return nil, fmt.Errorf("--bind %q: want <group>=<kind>[:<model>][,<preset>]", kv)
		}
		obj, names := "group", p.Groups
		if rest, is := strings.CutPrefix(name, "resident:"); is {
			obj, names, name = "resident", p.Residents, rest
		}
		yard, ok := names[name]
		if !ok {
			return nil, fmt.Errorf("--bind %q: the pack has no %s %s (it has: %s)", kv, obj, name, strings.Join(sortedKeys(names), ", "))
		}
		set(obj+"."+yard+".kind", kind)
		set(obj+"."+yard+".model", model)
		if hasPreset {
			args, err := presetArgs(kind, preset)
			if err != nil {
				return nil, fmt.Errorf("--bind %q: %w", kv, err)
			}
			set(obj+"."+yard+".args", strings.Join(args, " "))
		}
	}
	for _, kv := range members {
		name, v, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("--members %q: want <group>=a,b", kv)
		}
		yard, ok := p.Groups[name]
		if !ok {
			return nil, fmt.Errorf("--members %q: the pack has no group %s (it has: %s)", kv, name, strings.Join(sortedKeys(p.Groups), ", "))
		}
		set("group."+yard+".members", v)
	}
	if unattended {
		for _, q := range p.Questions {
			kind, bound := answers[strings.TrimSuffix(q.ID, "args")+"kind"]
			if _, done := answers[q.ID]; q.Field == "args" && bound && !done {
				answers[q.ID] = strings.Join(agents.UnattendedArgs(kind), " ")
			}
		}
	}
	return answers, nil
}

// openAnswers is what an import does about the sources a pack asks that
// its residents may open threads in.
type openAnswers struct {
	// Answers are the operator's, by the yard's resident and source: what
	// pack.Apply grants and records.
	Answers map[string]map[string]bool `json:"answers"`
	// Notes say what was not granted and why, each naming who or which flag
	// would grant it.
	Notes []string `json:"notes"`
	// open are the residents still to ask about, in a terminal.
	open []pack.OpenThreads
}

// openThreads settles what the plan's OpenThreads come to: --open-threads
// answers for the pack's resident it names, a terminal is asked for the
// rest (mayAsk), and without either nothing is granted and a note names the
// flag. The safe answer exists here, and it is no, so a missing flag is no
// error as it is for the import's other questions. Anyone but the operator
// answers nothing, by flag or by form: the check is the one yardr resident
// add makes (mayAllowOpening), and the import does the rest.
func (a *app) openThreads(ctx context.Context, st store.API, p *pack.Plan, flags []string, mayAsk bool) (*openAnswers, error) {
	out := &openAnswers{Answers: map[string]map[string]bool{}, Notes: []string{}}
	named := map[string][]string{}
	for _, kv := range flags {
		name, v, ok := strings.Cut(kv, "=")
		if !ok || name == "" || v == "" {
			return nil, fmt.Errorf("--open-threads %q: want <pack's resident>=<source>[,<source>], or =none", kv)
		}
		if _, ok := p.Residents[name]; !ok {
			return nil, fmt.Errorf("--open-threads %q: the pack has no resident %s (it has: %s)", kv, name, strings.Join(sortedKeys(p.Residents), ", "))
		}
		i := slices.IndexFunc(p.OpenThreads, func(ot pack.OpenThreads) bool { return ot.Pack == name })
		if i < 0 {
			return nil, fmt.Errorf("--open-threads %q: the pack asks for no source its resident %s may open threads in (open_threads)", kv, name)
		}
		if _, twice := named[name]; twice {
			return nil, fmt.Errorf("--open-threads %q: %s is answered twice", kv, name)
		}
		named[name] = []string{}
		if v == "none" {
			continue
		}
		for _, src := range strings.Split(v, ",") {
			if src = strings.TrimSpace(src); !slices.Contains(p.OpenThreads[i].Wants, src) {
				return nil, fmt.Errorf("--open-threads %q: the pack does not ask that %s may open threads in %q (it asks for: %s)", kv, name, src, strings.Join(p.OpenThreads[i].Wants, ", "))
			}
			named[name] = append(named[name], src)
		}
	}
	var operator string
	var is, checked bool
	for _, ot := range p.OpenThreads {
		yes, flagged := named[ot.Pack]
		answers := map[string]bool{}
		// What this run would have to be the operator for.
		var about []string
		for _, src := range ot.Wants {
			switch {
			case flagged && slices.Contains(yes, src) && ot.Grantable(src):
				answers[src] = true
				about = append(about, src)
			case flagged && slices.Contains(yes, src) && !slices.Contains(ot.Has, src):
				out.Notes = append(out.Notes, fmt.Sprintf("--open-threads %s=%s grants nothing: the pack wants %s, this yard lacks %s (yardr pack check says what to set up; the import asks once the yard has it)",
					ot.Pack, src, src, lacking(ot, src)))
			case slices.Contains(ot.Asks, src):
				// A flag that leaves a source out says no to it.
				if flagged {
					answers[src] = false
				}
				about = append(about, src)
			}
		}
		if len(about) == 0 {
			continue
		}
		if !checked {
			var err error
			if operator, is, err = a.operator(ctx, st); err != nil {
				return nil, err
			}
			checked = true
		}
		switch {
		case !is:
			out.Notes = append(out.Notes, fmt.Sprintf("resident %s was not let open threads in %s: only the operator (%s) may grant that, and this is %s. The rest of the import does not depend on it; %s applies the pack again to be asked",
				ot.Resident, strings.Join(about, ", "), operator, a.who(), operator))
		case flagged:
			out.Answers[ot.Resident] = answers
		case mayAsk:
			out.open = append(out.open, ot)
		default:
			out.Notes = append(out.Notes, fmt.Sprintf("resident %s was not let open threads in %s: nothing is asked here, and the answer without a flag is no. --open-threads %s=%s grants it, --open-threads %s=none says no for good",
				ot.Resident, strings.Join(about, ", "), ot.Pack, strings.Join(about, ","), ot.Pack))
		}
	}
	return out, nil
}

// lacking names what of hook and credential the yard has not for source.
func lacking(ot pack.OpenThreads, source string) string {
	var out []string
	for _, l := range ot.Lacks {
		if l.Source == source {
			for _, n := range l.Missing {
				out = append(out, n.What)
			}
		}
	}
	return strings.Join(out, " and ")
}

// ask asks the operator, once per resident, whether it may open threads in
// the sources still open, and takes the one answer for all of them. No is
// the default: leaving the form at its first key grants nothing.
func (o *openAnswers) ask() error {
	for _, ot := range o.open {
		yes := false
		if err := ask(huh.NewConfirm().Title(fmt.Sprintf("Let %s open threads in %s?", ot.Resident, strings.Join(ot.Asks, ", "))).
			Description("The pack asks for it. Yes lets the resident start a thread there unasked (yardr thread open); no leaves its setting as it is. Either answer is recorded and not asked again.").
			Value(&yes)); err != nil {
			return err
		}
		answers := map[string]bool{}
		for _, src := range ot.Asks {
			answers[src] = yes
		}
		o.Answers[ot.Resident] = answers
	}
	return nil
}

// presetLabel is a permission preset's label as --bind takes it: one word.
func presetLabel(label string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(label)), " ", "-")
}

// presetArgs are the arguments of kind's permission preset called label, as
// the group form lists them; none is no arguments.
func presetArgs(kind, label string) ([]string, error) {
	want, have := presetLabel(label), []string{}
	if want == "none" {
		return nil, nil
	}
	preset, _ := agents.Lookup(kind)
	for _, perm := range preset.Permissions {
		if presetLabel(perm.Label) == want {
			return perm.Args, nil
		}
		have = append(have, presetLabel(perm.Label))
	}
	return nil, fmt.Errorf("%s has no permission preset %q (it has: %s)", kind, label, strings.Join(append(have, "none"), ", "))
}

// askShape asks which flow the rig follows and which resident takes each
// of the pack's, and puts the answers into o.
func (a *app) askShape(ctx context.Context, st store.API, p *pack.Pack, plan *pack.Plan, o *pack.Options) error {
	for _, q := range shaping(plan) {
		if q.ID == pack.FlowQuestion {
			var opts []huh.Option[string]
			for _, f := range p.Flows {
				if f.Type == "" {
					opts = append(opts, huh.NewOption(f.Name, f.Name))
				}
			}
			o.Flow = q.Default
			if err := ask(huh.NewSelect[string]().Title(fmt.Sprintf("Which of the pack's flows does rig %s follow?", plan.Rig)).Options(opts...).Value(&o.Flow)); err != nil {
				return err
			}
			continue
		}
		_, yard, _ := strings.Cut(q.Object, " ")
		name := packName(plan.Residents, yard)
		have, err := st.Residents(ctx)
		if err != nil {
			return err
		}
		pick := name
		opts := []huh.Option[string]{huh.NewOption("A new resident called "+name, name)}
		for _, r := range have {
			opts = append(opts, huh.NewOption(r.Name+"  ("+residentAgent(r)+")", r.Name))
		}
		if err := ask(huh.NewSelect[string]().Title("Which resident takes the pack's " + name + "?").
			Description("It gets the pack's role and what the pack says it hears; its identity, directory, agent and model stay.").
			Options(opts...).Value(&pick)); err != nil {
			return err
		}
		o.Residents[name] = pick
	}
	return nil
}

// askAnswers asks the plan's open questions, object by object: the agent,
// model and permissions from the lists the group form uses, the members of
// a people group.
func (a *app) askAnswers(ctx context.Context, st store.API, plan *pack.Plan, answers pack.Answers) error {
	for _, o := range plan.Objects {
		id := func(field string) string { return o.Kind + "." + o.Name + "." + field }
		open := func(field string) bool {
			_, done := answers[id(field)]
			return slices.Contains(o.Asks, id(field)) && !done
		}
		if open("kind") || open("model") {
			ans := groupAnswers{Kind: or(defaultKind(ctx, st), "claude"), For: " for " + o.Kind + " " + o.Name}
			if err := a.agentKindModel(ctx, &ans); err != nil {
				return err
			}
			answers[id("kind")], answers[id("model")] = ans.Kind, ans.Model
		}
		if open("args") {
			ans := groupAnswers{Kind: answers[id("kind")], For: " for " + o.Kind + " " + o.Name}
			ans.PermArgs = agents.UnattendedArgs(ans.Kind)
			if err := a.agentPermissions(&ans); err != nil {
				return err
			}
			answers[id("args")] = strings.Join(ans.PermArgs, " ")
		}
		if open("members") {
			var v string
			if err := ask(huh.NewInput().Title("Members of group " + o.Name).Placeholder("mckean, mayor").
				Description("Optional, comma-separated actors (YARDR_ACTOR). If set, only they may advance beads out of this group's stages.").
				Value(&v)); err != nil {
				return err
			}
			answers[id("members")] = v
		}
	}
	return nil
}

func printResult(plan *pack.Plan, res *pack.Result, pruning bool, answers pack.Answers, open *openAnswers, refused map[string]string) {
	fmt.Println()
	if res.Wrote() {
		fmt.Printf("applied pack %s to rig %s: %d created, %d changed, %d unchanged", res.Pack, res.Rig, res.Created, res.Changed, res.Unchanged)
		if len(res.Pruned) > 0 {
			fmt.Printf(", %d pruned", len(res.Pruned))
		}
		fmt.Println()
	} else {
		fmt.Printf("pack %s is already applied to rig %s: nothing changed\n", res.Pack, res.Rig)
	}
	for _, s := range res.Pruned {
		fmt.Println("  pruned  " + s)
	}
	if len(res.Left) > 0 {
		hint := "yardr pack apply … --prune removes them"
		if pruning {
			hint = "held, or their removal failed"
		}
		fmt.Printf("no longer in the pack and left in the yard (%s):\n", hint)
		for _, s := range res.Left {
			fmt.Println("  " + s)
		}
	}
	for _, e := range res.PruneErrors {
		fmt.Println("  not removed: " + e)
	}
	for _, o := range plan.Objects {
		id := o.Kind + "." + o.Name + ".args"
		if !slices.Contains(o.Asks, id) || strings.TrimSpace(answers[id]) != "" {
			continue
		}
		fmt.Printf("%s %s is new and has no agent arguments: an unattended agent stops at every permission prompt until it is given them (yardr %s add, the form)\n", o.Kind, o.Name, o.Kind)
	}
	for _, g := range res.Granted {
		fmt.Printf("resident %s may now open threads in %s\n", g.Resident, strings.Join(g.Sources, ", "))
	}
	// A no changes nothing in the yard, so it is said: it is on record.
	for _, name := range sortedKeys(open.Answers) {
		var no []string
		for _, src := range sortedKeys(open.Answers[name]) {
			if !open.Answers[name][src] {
				no = append(no, src)
			}
		}
		if len(no) > 0 {
			fmt.Printf("resident %s was not let open threads in %s: the no is recorded and not asked again (--open-threads %s=%s grants it after all)\n",
				name, strings.Join(no, ", "), packName(plan.Residents, name), strings.Join(no, ","))
		}
	}
	for _, n := range open.Notes {
		fmt.Println(n)
	}
	for _, m := range res.Mapped {
		fmt.Printf("hook %s now moves tracked %s issues as %s: %s\n", m.Hook, m.Source, m.As, strings.Join(pack.MapLines(m.Map), " "))
	}
	// What the import did not do for a stage map, each with who does it and
	// how: it makes no hook, gives no identity and grants no set_state.
	for _, m := range plan.Maps {
		if why := refused[m.Source]; why != "" {
			fmt.Printf("the stage map of %s was not set on hook %s: %s. The rest of the import does not depend on it\n", m.Source, m.Hook, why)
		}
		for _, what := range []string{"hook", "identity"} {
			if detail := m.Missing(what); detail != "" {
				fmt.Printf("the stage map of %s was not set: %s\n", m.Source, detail)
			}
		}
		if detail := m.Missing(store.SetState); detail != "" {
			fmt.Println("note: " + detail)
		}
	}
	for _, r := range res.Restart {
		fmt.Printf("resident %s is running and was not restarted: its role or subscriptions changed, so restart it (yardr resident restart %s)\n", r, r)
	}
}

func printRecord(r store.PackImport) {
	fmt.Printf("%s in rig %s  %s by %s\n", r.Pack, r.Rig, r.At.Local().Format("2006-01-02 15:04"), r.By)
	src := r.Source
	if r.Commit != "" {
		src += " at " + r.Commit[:min(12, len(r.Commit))]
		if r.Dirty {
			src += " with uncommitted changes"
		}
	}
	fmt.Printf("  source     %s\n", src)
	if r.Flow != "" {
		fmt.Printf("  flow       %s (stored as %s)\n", r.Flow, pack.FlowName(r.Pack, r.Flow))
	}
	pairs := func(m map[string]string) string {
		var out []string
		for _, k := range sortedKeys(m) {
			out = append(out, k+" -> "+m[k])
		}
		return or(strings.Join(out, ", "), "none")
	}
	roles := "the pack's"
	if r.RigRoles {
		roles = "the rig's own where it has them (--rig-roles)"
	}
	fmt.Printf("  roles      %s\n", roles)
	fmt.Printf("  groups     %s\n", pairs(r.Groups))
	fmt.Printf("  residents  %s\n", pairs(r.Residents))
	count := map[string]int{}
	for _, o := range r.Objects {
		count[o.Kind]++
	}
	var parts []string
	for _, kind := range planKinds {
		if n := count[kind[0]]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, kind[1]))
		}
	}
	fmt.Printf("  objects    %s\n", or(strings.Join(parts, ", "), "none"))
	for _, name := range sortedKeys(r.OpenThreads) {
		var said []string
		for _, src := range sortedKeys(r.OpenThreads[name]) {
			said = append(said, src+" "+map[bool]string{true: "yes", false: "no"}[r.OpenThreads[name][src]])
		}
		fmt.Printf("  may open   %s: %s\n", name, strings.Join(said, ", "))
	}
}

// fills says what a plan puts for each placeholder, the operator first.
func fills(fill map[string]string) string {
	names := []string{pack.Operator}
	for _, name := range slices.Sorted(maps.Keys(fill)) {
		if name != pack.Operator {
			names = append(names, name)
		}
	}
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = fmt.Sprintf("{{%s}} would read %s", name, fill[name])
	}
	return strings.Join(parts, ", ")
}

// planKinds are the kinds of object in a plan, in its order, with the
// heading each is printed under.
var planKinds = [][2]string{{"file", "files"}, {"group", "groups"}, {"flow", "flows"}, {"pointer", "flow pointers"},
	{"route", "routes"}, {"resident", "residents"}, {"subscription", "subscriptions"}, {"map", "stage maps"}}

// printPlan prints the plan. answers are those already given: an answered
// question is printed as what its object gets, and only the open ones are
// listed as what an import would ask. open are the answers to whether a
// resident may open threads, by resident and source. refused says, by
// source, why a stage map the plan would set is not set by this run.
func printPlan(p *pack.Plan, answers pack.Answers, open map[string]map[string]bool, refused map[string]string) {
	fmt.Printf("pack %s (%s) into rig %s\n", p.Pack, p.Dir, p.Rig)
	fmt.Printf("its roles and scripts would be copied to %s\n", p.Install)
	if slices.ContainsFunc(p.Objects, func(o pack.Object) bool { return o.File != nil && o.File.Stage != "" }) {
		fmt.Printf("its stage files would be copied to %s\n", filepath.Join(p.StageFiles, p.Pack+".<flow>"))
	}
	if slices.ContainsFunc(p.Objects, func(o pack.Object) bool { return o.File != nil && o.File.Fill }) {
		fmt.Printf("in its roles and stage files %s\n", fills(p.Fill))
	}
	count := map[string]int{}
	label := map[string]string{pack.Create: "create", pack.Change: "change", pack.Unchanged: "unchanged", pack.NotInPack: "not in the pack", pack.Dropped: "no longer in it"}
	for _, kind := range planKinds {
		first := true
		for _, o := range p.Objects {
			if o.Kind != kind[0] {
				continue
			}
			if first {
				fmt.Printf("\n%s\n", kind[1])
				first = false
			}
			count[o.Action]++
			line := fmt.Sprintf("  %-15s  %s", label[o.Action], o.Name)
			if o.Kind == "file" && o.File != nil && o.Action != pack.NotInPack {
				line += " -> " + o.File.To
			}
			switch {
			case o.Action == pack.NotInPack:
				line += " (would be left alone)"
			case o.Action == pack.Dropped && o.Held != "":
				line += " (an earlier import brought it; left even by --prune: " + o.Held + ")"
			case o.Action == pack.Dropped:
				line += " (an earlier import brought it; left alone unless --prune)"
			}
			fmt.Println(line)
			if o.Edited {
				fmt.Printf("  %-15s    edited by hand since the last import: overwritten or removed only with --yes; the edit belongs in the pack's source\n", "")
			}
			for _, k := range o.Keeps {
				fmt.Printf("  %-15s    kept: %s\n", "", k)
			}
			for _, s := range o.Shadows {
				fmt.Printf("  %-15s    shadows %s\n", "", s)
			}
			for _, d := range o.Changes {
				fmt.Printf("  %-15s    %s: %s -> %s\n", "", d.Field, planValue(d.Old), planValue(d.New))
			}
			for _, id := range o.Asks {
				if v, ok := answers[id]; ok {
					fmt.Printf("  %-15s    %s\n", "", answered(id, v))
				} else {
					fmt.Printf("  %-15s    asks: %s\n", "", id)
				}
			}
		}
	}
	var asks []string
	for _, q := range p.Missing(answers) {
		line := fmt.Sprintf("  %s: %s", q.ID, q.Ask)
		if q.Default != "" {
			line += " [" + q.Default + "]"
		}
		asks = append(asks, line)
	}
	for _, ot := range p.OpenThreads {
		if _, done := open[ot.Resident]; !done && len(ot.Asks) > 0 {
			asks = append(asks, fmt.Sprintf("  the operator: let %s open threads in %s (--open-threads %s=%s, or =none)? [no]",
				ot.Resident, strings.Join(ot.Asks, ", "), ot.Pack, strings.Join(ot.Asks, ",")))
		}
	}
	if len(asks) > 0 {
		fmt.Println("\nan import would ask")
		for _, line := range asks {
			fmt.Println(line)
		}
	}
	if len(p.Governs) > 0 {
		fmt.Printf("\nflows governing rig %s afterwards\n", p.Rig)
		for _, g := range p.Governs {
			who := "the rig's beads"
			if g.Type != "" {
				who = "type " + g.Type
			}
			line := fmt.Sprintf("  %s: %s (%s)", who, g.Flow, g.Source)
			if !g.InPack {
				line += ", not from the pack"
			}
			fmt.Printf("%s; now: %s\n", line, or(g.Now, "none"))
		}
	}
	for _, s := range p.Sources {
		fmt.Printf("\nsource %s, for resident %s\n", s.Source, s.Resident)
		for _, n := range s.Needs {
			fmt.Printf("  %-8s %-12s %s\n", n.Status, strings.ReplaceAll(n.What, "_", " "), n.Detail)
		}
	}
	for _, ot := range p.OpenThreads {
		fmt.Printf("\nopen threads, for resident %s\n", ot.Resident)
		fmt.Printf("  the pack asks for %s\n", strings.Join(ot.Wants, ", "))
		row := func(what string, sources []string, detail string) {
			if len(sources) > 0 {
				fmt.Printf("  %-9s %s%s\n", what, strings.Join(sources, ", "), detail)
			}
		}
		row("has", ot.Has, ": it may already, and nothing is asked")
		var yes, no, asked []string
		for _, src := range ot.Wants {
			switch said, done := open[ot.Resident][src]; {
			case done && said:
				yes = append(yes, src)
			case done:
				no = append(no, src)
			case slices.Contains(ot.Asks, src):
				asked = append(asked, src)
			}
		}
		row("grants", yes, ": --open-threads says so")
		row("declines", no, ": --open-threads leaves it out")
		row("asks", asked, ": the operator is asked, and the answer without one is no")
		row("declined", slices.DeleteFunc(slices.Clone(ot.Declined), func(s string) bool { return slices.Contains(yes, s) }),
			": an earlier import was told no (--open-threads "+ot.Pack+"=<source> grants it)")
		for _, l := range ot.Lacks {
			fmt.Printf("  the pack wants %s, this yard lacks %s: nothing is asked or granted\n", l.Source, lacking(ot, l.Source))
			for _, n := range l.Missing {
				fmt.Printf("    %-8s %-12s %s\n", n.Status, strings.ReplaceAll(n.What, "_", " "), n.Detail)
			}
		}
	}
	for _, m := range p.Maps {
		fmt.Printf("\nstage map of source %s, as resident %s\n", m.Source, m.As)
		fmt.Printf("  the pack says %s\n", strings.Join(pack.MapLines(m.Map), " "))
		switch {
		case refused[m.Source] != "":
			fmt.Printf("  not set on hook %s: %s\n", m.Hook, refused[m.Source])
		case m.Action == pack.Create:
			fmt.Printf("  would be set on hook %s, which has none\n", m.Hook)
		case m.Action == pack.Change:
			fmt.Printf("  would replace what hook %s has: as %s, %s\n", m.Hook, or(m.Was.As, "(none)"), or(strings.Join(pack.MapLines(m.Was.Map), " "), "(no map)"))
		case m.Action == pack.Unchanged:
			fmt.Printf("  hook %s has it\n", m.Hook)
		default:
			fmt.Println("  nothing would be set: the import goes through without it, and the next one sets it once the yard has what is missing")
		}
		for _, n := range m.Needs {
			fmt.Printf("  %-8s %-12s %s\n", n.Status, strings.ReplaceAll(n.What, "_", " "), n.Detail)
		}
	}
	if len(p.Notes) > 0 {
		fmt.Println("\nnotes")
		for _, n := range p.Notes {
			fmt.Println("  " + n)
		}
	}
	fmt.Printf("\n%d to create, %d to change, %d unchanged, %d not in the pack",
		count[pack.Create], count[pack.Change], count[pack.Unchanged], count[pack.NotInPack])
	if n := count[pack.Dropped]; n > 0 {
		fmt.Printf(", %d no longer in it", n)
	}
	fmt.Println()
	if len(p.Refused) > 0 {
		fmt.Println("\nan import would be refused")
		for _, r := range p.Refused {
			fmt.Println("  " + r)
		}
	}
}

// answered prints the answer v to question id as what its object gets.
func answered(id, v string) string {
	v = strings.TrimSpace(v)
	switch field := id[strings.LastIndex(id, ".")+1:]; field {
	case "kind":
		return "agent: " + v
	case "model":
		return "model: " + or(v, "the agent's own default")
	case "args":
		return "arguments: " + or(v, "none; its agent stops at every permission prompt")
	case "members":
		return "members: " + or(v, "anyone")
	case "dir":
		return "directory: " + v
	default:
		return field + ": " + v
	}
}

// planValue prints a changed field's value: a string as it is, anything
// else as JSON.
func planValue(v any) string {
	switch v := v.(type) {
	case nil:
		return "(none)"
	case string:
		if v == "" {
			return "(none)"
		}
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
