package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	c.Flags().BoolVar(&f.opts.PackRoles, "pack-roles", false, "set the pack's roles even where the rig has its own .yardr/roles/<name>.md, which otherwise wins")
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
	f.opts.Home, f.opts.HookAddr = cfg.Home, cfg.HookAddr
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
would govern the rig, and what each source a resident subscribes to still
lacks. Nothing is written.

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
			if err := a.out(plan, func() { printPlan(plan, nil) }); err != nil {
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

What it asks, and the flag that answers instead:
  the agent and model of a group the yard lacks   --bind <group>=<kind>[:<model>][,<preset>]
  the members of a people group the yard lacks    --members <group>=a,b   (empty: anyone)
  which resident takes a resident of the pack     --resident <pack's>=<yard's>
  the agent of a resident the yard lacks          --bind resident:<pack's>=<kind>[:<model>][,<preset>]
  which flow, when the pack has several           --flow <name>
<group> is the pack's name for it (builders). Without a terminal nothing is
asked: a missing answer is an error, never a default, and --yes is needed
to apply.

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
				_ = a.out(plan, func() { printPlan(plan, nil) })
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
			if !a.json {
				printPlan(plan, answers)
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
				if o.Action == pack.Create || o.Action == pack.Change || (prune && o.Action == pack.Dropped && o.Held == "") {
					todo++
				}
			}
			if todo > 0 && !yes {
				if !asking {
					return errors.New("pack apply changes the yard: give --yes, or run it in a terminal to be asked")
				}
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
			res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: answers, Prune: prune, Actor: a.who()})
			if res != nil {
				if oerr := a.out(map[string]any{"plan": plan, "answers": answers, "result": res}, func() { printResult(plan, res, prune, answers) }); oerr != nil {
					return oerr
				}
			}
			return err
		},
	}
	af.register(apply, "is")
	apply.Flags().StringArrayVar(&binds, "bind", nil, "<group>=<kind>[:<model>][,<preset>]: the agent, model and permission preset of a group the yard lacks; resident:<name>=… for a resident (repeatable)")
	apply.Flags().StringArrayVar(&members, "members", nil, "<group>=a,b: the members of a people group the yard lacks; empty for anyone (repeatable)")
	apply.Flags().BoolVar(&yes, "yes", false, "apply without asking for confirmation")
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

func printResult(plan *pack.Plan, res *pack.Result, pruning bool, answers pack.Answers) {
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
}

// planKinds are the kinds of object in a plan, in its order, with the
// heading each is printed under.
var planKinds = [][2]string{{"file", "files"}, {"group", "groups"}, {"flow", "flows"}, {"pointer", "flow pointers"},
	{"route", "routes"}, {"resident", "residents"}, {"subscription", "subscriptions"}}

// printPlan prints the plan. answers are those already given: an answered
// question is printed as what its object gets, and only the open ones are
// listed as what an import would ask.
func printPlan(p *pack.Plan, answers pack.Answers) {
	fmt.Printf("pack %s (%s) into rig %s\n", p.Pack, p.Dir, p.Rig)
	fmt.Printf("its roles and scripts would be copied to %s\n", p.Install)
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
			for _, k := range o.Keeps {
				fmt.Printf("  %-15s    kept: %s\n", "", k)
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
	if open := p.Missing(answers); len(open) > 0 {
		fmt.Println("\nan import would ask")
		for _, q := range open {
			line := fmt.Sprintf("  %s: %s", q.ID, q.Ask)
			if q.Default != "" {
				line += " [" + q.Default + "]"
			}
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
