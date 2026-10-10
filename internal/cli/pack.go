package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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
	"yardr.dev/yardr/internal/version"
	"yardr.dev/yardr/packs"
)

// packFlags are what pack check and pack apply both take: where the pack
// goes and what a pack cannot say for itself.
type packFlags struct {
	opts   pack.Options
	crew   []string
	groups []string
}

func (f *packFlags) register(c *cobra.Command, verb string) {
	c.Flags().StringVar(&f.opts.Depot, "depot", "", "the depot the pack "+verb+" imported into")
	f.registerShape(c)
	c.Flags().BoolVar(&f.opts.PackRoles, "pack-roles", false, "set the pack's roles everywhere: the default now, needed only to undo an earlier --depot-roles")
	c.MarkFlagsMutuallyExclusive("depot-roles", "pack-roles")
	_ = c.MarkFlagRequired("depot")
}

// registerShape registers what shapes an import's plan wherever the depot
// comes from: depot init takes these and has the depot as its
// argument.
func (f *packFlags) registerShape(c *cobra.Command) {
	c.Flags().StringVar(&f.opts.Flow, "flow", "", "the pack's flow the depot follows (default: the one it was given by the last import, else the pack's first flow without a type)")
	c.Flags().StringArrayVar(&f.crew, "crew", nil, "<pack's name>=<yard's crew member>: which crew member takes a crew member of the pack (repeatable; default: as at the last import)")
	c.Flags().StringArrayVar(&f.groups, "group", nil, "<pack's group>=<yard's group>: a group the yard has takes a group of the pack as it is, and no <depot>-<group> is made; an agent group for an agent group, people for people (repeatable; default: as at the last import)")
	c.Flags().BoolVar(&f.opts.DepotRoles, "depot-roles", false, "leave a group or crew member its depot's own .yardr/roles/<name>.md where the depot has one, instead of setting the pack's role (default: as at the last import, else the pack's)")
}

// importFlags are what an import takes: pack apply's flags, and depot
// init's with --pack.
type importFlags struct {
	packFlags
	binds   []string
	members []string
	opens   []string
	calls   []string
	yes     bool
	prune   bool // pack apply's alone
}

// registerAnswers registers the flags that answer what an import asks.
func (f *importFlags) registerAnswers(c *cobra.Command) {
	c.Flags().StringArrayVar(&f.binds, "bind", nil, "<group>=<kind>[:<model>][,<preset>]: the agent, model and permission preset of a group the yard lacks; crew:<name>=… for a crew member (repeatable)")
	c.Flags().StringArrayVar(&f.members, "members", nil, "<group>=a,b: the members of a people group the yard lacks; empty for anyone (repeatable)")
	c.Flags().StringArrayVar(&f.opens, "open-threads", nil, "<pack's crew member>=linear,github: let the crew member that takes it open threads in these of the sources the pack asks for, and in no other; =none for no (repeatable)")
	c.Flags().StringArrayVar(&f.calls, "calls", nil, "<group>=<source>:read,<call>: let the group's sessions make these of the calls the pack asks for of a declared source, and no other; crew:<pack's crew member>=… for a crew member; <source>:none for no (repeatable)")
	c.Flags().BoolVar(&f.yes, "yes", false, "apply without asking for confirmation, and overwrite an installed file that was edited by hand")
}

// loadPack reads the pack arg names (resolvePack). With --json an invalid
// pack's mistakes are printed as data, and the error is the exit code alone:
// Execute adds nothing to what a red check has printed.
func (a *app) loadPack(ctx context.Context, st store.API, arg, depot string) (*pack.Pack, error) {
	p, err := resolvePack(ctx, st, arg, depot)
	var errs pack.Errors
	if a.json && errors.As(err, &errs) {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"error": "the pack is not valid", "errors": errs})
		return nil, errCheckFailed
	}
	return p, err
}

// resolvePack reads the pack a command was given as arg. A URL is the
// pack's directory on a web server, fetched when the command runs. With a
// path separator in it, arg is the pack's directory. A bare word is a name:
// of a pack this yard has imported, read again from where the import
// recorded it was (the import into depot when there are several, else the
// latest), and otherwise of a pack yardr embeds. A name is never tried as
// a directory, so what `starter` means does not depend on where the command
// is run: ./starter is the directory.
func resolvePack(ctx context.Context, st store.API, arg, depot string) (*pack.Pack, error) {
	if pack.IsURL(arg) {
		return pack.Load(pack.URLFS(arg), arg)
	}
	if packDir(arg) {
		return pack.LoadDir(arg)
	}
	recs, err := st.Packs(ctx)
	if err != nil {
		return nil, err
	}
	var rec *store.PackImport
	for i, r := range recs {
		if r.Pack == arg && (rec == nil || r.Depot == depot || (rec.Depot != depot && r.At.After(rec.At))) {
			rec = &recs[i]
		}
	}
	if rec != nil && !fromEmbedded(rec.Source) {
		if pack.IsURL(rec.Source) {
			if _, err := fs.ReadFile(pack.URLFS(rec.Source), pack.File); err != nil {
				return nil, fmt.Errorf("pack %s was imported into depot %s from %s, which is gone (%v)", arg, rec.Depot, rec.Source, err)
			}
			p, err := pack.Load(pack.URLFS(rec.Source), rec.Source)
			if err == nil && p.Name != arg {
				return nil, fmt.Errorf("pack %s was imported into depot %s from %s, which now holds pack %s", arg, rec.Depot, rec.Source, p.Name)
			}
			return p, err
		}
		if _, err := os.Stat(filepath.Join(rec.Source, pack.File)); err != nil {
			return nil, fmt.Errorf("pack %s was imported into depot %s from %s, which is gone (%v): give the directory the pack is in now", arg, rec.Depot, rec.Source, err)
		}
		p, err := pack.LoadDir(rec.Source)
		if err == nil && p.Name != arg {
			return nil, fmt.Errorf("pack %s was imported into depot %s from %s, which now holds pack %s: give the directory pack %s is in now", arg, rec.Depot, rec.Source, p.Name, arg)
		}
		return p, err
	}
	if _, err := fs.Stat(packs.FS, arg+"/"+pack.File); err == nil {
		sub, err := fs.Sub(packs.FS, arg)
		if err != nil {
			return nil, err
		}
		return pack.Load(sub, packs.Embedded+" in yardr "+version.Describe())
	}
	var imported, embedded []string
	for _, r := range recs {
		if !slices.Contains(imported, r.Pack) {
			imported = append(imported, r.Pack)
		}
	}
	if entries, err := fs.ReadDir(packs.FS, "."); err == nil {
		for _, e := range entries {
			embedded = append(embedded, e.Name())
		}
	}
	slices.Sort(imported)
	hint := "a pack's directory is written with a path separator"
	if info, err := os.Stat(arg); err == nil && info.IsDir() {
		hint = fmt.Sprintf("for the directory %s here, write ./%s", arg, arg)
	}
	return nil, fmt.Errorf("no pack named %q: this yard has imported %s, yardr embeds %s; %s", arg, or(strings.Join(imported, ", "), "none"), or(strings.Join(embedded, ", "), "none"), hint)
}

// packDir reports whether arg, a command's pack, is a directory and no
// name: it has a path separator in it, or is . or .., which have none and
// can be no pack's name.
func packDir(arg string) bool {
	return strings.ContainsRune(arg, filepath.Separator) || strings.Contains(arg, "/") || arg == "." || arg == ".."
}

// openPack completes f's options for this yard (packOptions) and reads the
// pack arg names. A directory is read first, before the store is opened: a
// pack that is not valid is told its mistakes wherever it is checked, with
// no yard there to ask. A name is the yard's to resolve. yard, when given,
// runs before the yard is first reached for: pack check, which the first-run
// check lets through for the directory's sake, makes that check there.
func (a *app) openPack(ctx context.Context, arg string, f *packFlags, yard func() error) (store.API, *config.Config, *pack.Pack, error) {
	var p *pack.Pack
	if packDir(arg) {
		var err error
		if p, err = a.loadPack(ctx, nil, arg, f.opts.Depot); err != nil {
			return nil, nil, nil, err
		}
	}
	if yard != nil {
		if err := yard(); err != nil {
			return nil, nil, nil, err
		}
	}
	st, cfg, err := a.packOptions(ctx, f)
	if err != nil {
		return nil, nil, nil, err
	}
	if p == nil {
		if p, err = a.loadPack(ctx, st, arg, f.opts.Depot); err != nil {
			return nil, nil, nil, err
		}
	}
	return st, cfg, p, nil
}

// fromEmbedded reports whether source, as an import recorded it, is a pack
// yardr embeds and no directory.
func fromEmbedded(source string) bool {
	return source == packs.Embedded || strings.HasPrefix(source, packs.Embedded+" ")
}

// packOptions completes f's options for this yard and returns its store
// and configuration.
func (a *app) packOptions(ctx context.Context, f *packFlags) (store.API, *config.Config, error) {
	if f.opts.PackRoles {
		// To stderr: --json's output stays the plan alone.
		fmt.Fprintln(os.Stderr, "--pack-roles: the pack's roles are the default now; the flag is only needed to undo an earlier --depot-roles")
	}
	f.opts.Crew = map[string]string{}
	for _, kv := range f.crew {
		from, to, ok := strings.Cut(kv, "=")
		if !ok || from == "" || to == "" {
			return nil, nil, fmt.Errorf("--crew %q: want <pack's name>=<yard's crew member>", kv)
		}
		f.opts.Crew[from] = to
	}
	f.opts.Groups = map[string]string{}
	for _, kv := range f.groups {
		from, to, ok := strings.Cut(kv, "=")
		if !ok || from == "" || to == "" {
			return nil, nil, fmt.Errorf("--group %q: want <pack's group>=<yard's group>", kv)
		}
		f.opts.Groups[from] = to
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
	// Whom yardr init names in the yardmaster's role it installs.
	f.opts.Operator = yardOperator(ctx, st)
	return st, cfg, nil
}

func (a *app) packCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pack", Short: "A yard's way of working as a directory: flows, groups, roles, crew members"}

	// What check and apply take for the pack, said once in each one's help.
	const naming = `
<pack> is the pack's directory when it has a path separator in it
(packs/starter, ./mine, ~/packs/team), or a URL: the pack's directory on a
web server (https://yardr.dev/packs/starter/), fetched when the command
runs. A bare word is a name: of a pack this yard has imported (yardr pack
list), read again from where it was imported from, else of a pack yardr
carries in the binary (starter). A name is never
tried as a directory: write ./starter for a directory of that name.
`

	var cf packFlags
	check := &cobra.Command{
		Use:   "check <pack> --depot <depot>",
		Short: "Read a pack and show what importing it into a depot would change",
		Long: `Reads the pack (pack.yaml, roles/, scripts/), checks it, and prints
what importing it into the depot would do to this yard, object by object:
create, change (field: old -> new), unchanged, not in the pack (the yard
has it, the pack does not, and an import leaves it alone), or no longer in
the pack (an earlier import brought it, the pack has dropped it since, and
pack apply --prune removes it). Then what an import would ask, which flow
would govern the depot, what each source a crew member subscribes to still
lacks, and which sources the pack asks that a crew member may open threads in
(open_threads): an import asks the operator, and only about a source the
yard has a hook and a credential for. For each crew member the pack asks to
have exposed to the yard's peers (expose_to_peers), the plan shows which
peers it is exposed to, which an import asks the operator about, and which
an earlier import was told no for. For each source the pack carries a
stage map for (sources.<source>: as, hears), the plan shows the map, the
yard's crew member it would be acted on as, whether the yard has a hook of
that source and whether that crew member may set a status there (set_state),
with the command for whatever is missing. For each source the pack declares
(sources.<name>: declaration), the plan shows where its calls go, the calls,
what a declaration the yard has under that name would change to, and what
the yard must still be told: the file its credential is read from, and for a
source that can tell a user, which user names it; and, where the pack's file
replaces the yard's and adds or changes a read call, who gets that call
through a grant of read they hold. For each group and crew member the pack
asks calls of a declared source for (calls), the plan shows which it has,
which an import asks the operator about, which an earlier import was told
no for, and which lack something, with the command. Nothing is written.

The pack's role is set on every agent group and crew member it names, also
where the depot has its own .yardr/roles/<name>.md: the plan names that file
as shadowed. --depot-roles keeps the depot's own role instead.

--group <pack's group>=<yard's group> has a group the yard has take a group
of the pack, instead of a <depot>-<group> of the depot's own: the plan says
"takes", the pack's routes lead to it, and nothing of it is changed. Where
its role or limit is not the pack's, the plan says so in a line.

A role or stage file is installed with {{operator}} filled with the yard's
operator and {{<crew member>}} with the yard's crew member that takes that
crew member of the pack; any other {{...}} in one makes the pack invalid.

It exits 1 on a pack that is not valid, naming every mistake by file, line
and key, and on a plan an import would refuse (open beads in a stage the
pack's flow lacks). A pack given as a directory is read and checked before
the yard is asked anything, so its mistakes are named on a home with no yard
too; a valid one stops there for the yard its plan is made against. --json prints the plan as data. See docs/packs.md.
` + naming,
		Example: `  yardr pack check starter --depot yardr
  yardr pack check ~/packs/starter --depot yardr --crew yardmaster=boss
  yardr pack check contributors --depot signalbox --group builders=yardr-builders`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			// The first-run check, which pack check is let through
			// (noOnboarding), once a directory pack has been read.
			st, _, p, err := a.openPack(c.Context(), args[0], &cf, func() error { return a.onboard(c) })
			if err != nil {
				return err
			}
			plan, err := pack.Build(c.Context(), st, p, cf.opts)
			if err != nil {
				return err
			}
			if err := a.out(plan, func() { printPlan(plan, nil, nil, nil, nil) }); err != nil {
				return err
			}
			if len(plan.Refused) > 0 {
				return errCheckFailed
			}
			return nil
		},
	}
	cf.register(check, "would be")

	var af importFlags
	apply := &cobra.Command{
		Use:   "apply <pack> --depot <depot>",
		Short: "Import a pack into a depot: set the yard to what the pack says",
		Long: `Does what pack check shows: prints the plan, with what the flags answer
filled in, asks for what a pack cannot say, then sets the yard to the pack's
state. Everything is matched by name,
so an object the yard has is overwritten in what the pack carries, never
added a second time, and keeps what the pack does not carry (a group's
agent, model, arguments and members; a crew member's identity and directory).
Applying the same pack again changes nothing and logs one pack_unchanged
event.

The pack's roles win: a group or crew member gets the pack's copied role also
where the depot has its own .yardr/roles/<name>.md, which the plan names as
shadowed. --depot-roles keeps the depot's own role instead; the import records
the choice and the next one repeats it. A copy under <home>/packs/<pack>/
that was edited by hand is overwritten only with --yes: the edit belongs in
the pack's source.

What it asks, and the flag that answers instead:
  whether a group the yard has takes a group of   --group <pack's>=<yard's>
  the pack the yard lacks, as it is
  the agent and model of a group the yard lacks   --bind <group>=<kind>[:<model>][,<preset>]
  the members of a people group the yard lacks    --members <group>=a,b   (empty: anyone)
  which crew member takes one of the pack         --crew <pack's>=<yard's>
  the agent of a crew member the yard lacks       --bind crew:<pack's>=<kind>[:<model>][,<preset>]
  which flow, when the pack has several           --flow <name>
  whether a crew member may open threads in the   --open-threads <pack's>=linear,github
  sources the pack asks for (open_threads)        (=none for no)
  whether a group's sessions or a crew member     --calls <group>=<source>:read,<call>
  may make the calls of a declared source the     --calls crew:<pack's>=<source>:<call>
  pack asks for (calls)                           (<source>:none for no)
<group> is the pack's name for it (builders). Without a terminal nothing is
asked: a missing answer is an error, never a default, and --yes is needed
to apply. The one exception is --group: nothing but the flag, or the last
import, has a group of the yard take the pack's, and without either the
group is the depot's own, <depot>-<group>.

A group taken with --group stays the yard's: its agent, model, arguments,
limit, members and role are not changed, the plan names in a line what of the
pack's it does not get, and no --prune removes it. In a terminal the import
asks for a group it would make whether one of the yard's of its kind takes
it instead (an agent group for an agent group, people for people), unless
--yes is given or --bind or --members names that group.

A pack may ask that its crew member may open threads in a source. That is the
operator's to grant, so the import asks, last and with no as the default,
and only about a source this yard has a hook and a credential for; the plan
says what any other source lacks. Without a terminal, with --json or with
--yes, nothing is granted unless --open-threads says so, and the output
names the flag. Run by anyone but the operator the import does the rest and
grants nothing. An answer is recorded, so it is asked once.

A pack may ask that the yards this one is linked with see its crew member
and may write to it (crew.<name>.expose_to_peers: true). That is the
operator's to grant too, per link, so the import asks "Expose <name> to
<peer>?" once per peer the yard has, last and with no as the default. A pack
names no peer and no person, and sets nothing itself. No flag answers:
without a terminal, with --json or with --yes, nobody is exposed, and yardr
peer allow <peer> --expose <name> is the command that does. Run by anyone
but the operator the import does the rest and exposes nobody. An answer is
recorded by the peer, so it is asked once, and a peer linked later is asked
about by the next import.

A pack may ask that a group's sessions or a crew member may make calls of a
declared source (groups.<group>.calls, crew.<name>.calls: { crm: [read,
note] }; read is every read call, an update is asked for by its name). That
is the operator's to grant too (yardr source grant), and is asked the same
way: last, once per group or crew member and source, with no as the
default, and only about a source whose env_file is set, so that an import
which adds the source asks nothing and the one after yardr source set asks.
Without a terminal, with --json or with --yes, nothing is granted unless
--calls says so. Run by anyone but the operator the import does the rest
and grants nothing. An answer is recorded, so it is asked once; a grant is
taken back with yardr source revoke.

A pack may carry a stage map for a source (sources.<source>: as, hears):
which status an issue goes to when the bead tracking it enters a stage. The
import sets it, and the crew member it is acted on as, on the yard's hook of
that source; a hook that has another map gets the pack's, and the plan shows
both. It never makes a hook: without one the import goes through, sets
nothing for that source and names the command, and the next apply sets it.
It never lets the crew member set a status either: until the operator has
(yardr crew set <crew member> set_state=<source>), the map moves nothing, and
the plan and the output say so. As with yardr hook add --as, only the
operator or that crew member's own session may have a hook act as a crew member:
run by anyone else the import does the rest and sets no map.

A pack may also carry what a source's events say of a bead (sources.<source>:
says): lines of a filter over the event and the outcome it reports for the
train it is about, which the train's flow routes (yardr hook add --help). The
import sets the table on the same hook, and needs the hook alone: no crew
member acts, so nothing is asked and anyone who may apply the pack sets it.

A pack may declare a source (sources.<name>: declaration: <file>): a
service outside the yard that yardr has no Go for, as yardr source add
takes it (yardr source --help). The import adds it under the pack's name
for it, and replaces a declaration the yard has under that name, with every
changed call and a changed address in the plan. A pack carries no secret
and names no file of the yard, so afterwards the source's credential is
still to be set (yardr source set <name> env_file=<file>), and no user is
told through it until one names it (yardr user set <user> notify=<name>
env_file=<file>): the plan and the output give both commands. A declaration
in a pack has the keys ` + strings.Join(pack.DeclarationKeys, ", ") + `
and no other. As with yardr source add, adding a source is the operator's:
run by anyone else the import does the rest and adds none.

The one answer with a default is what a new group's or crew member's agent may
do without asking. <preset> names one of the agent's permission presets by
the label the group form shows (claude: auto, accept-edits, bypass,
read-only; none for no arguments). Left out, a terminal asks with the same
list; without a terminal, or with --yes, the agent gets the arguments yardr
init gives it:
` + agents.UnattendedHelp() + `
The plan prints the arguments each new group and crew member gets. Those of a
group or crew member the yard already has are never touched.

Refused, changing nothing, while open beads sit in a stage the pack's flow
lacks; the message names them. Objects an earlier import brought and the
pack has dropped since are listed, and removed only with --prune. A running
crew member whose role or subscriptions change is not restarted; the output
says so. The import is recorded (yardr pack list). See docs/packs.md.
` + naming,
		Example: `  yardr pack apply starter --depot web
  yardr pack apply packs/starter --depot web --yes --crew yardmaster=boss \
    --bind builders=claude:opus --bind reviewers=codex,approve-for-me --members backlog= --members decisions=mckean`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			imp, err := a.planImport(c.Context(), "pack apply", args[0], &af, "", nil)
			if imp == nil || err != nil {
				return err
			}
			return imp.apply(c.Context(), a, nil, nil)
		},
	}
	af.register(apply, "is")
	af.registerAnswers(apply)
	apply.Flags().BoolVar(&af.prune, "prune", false, "remove what an earlier import of this pack brought and the pack has dropped since")

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
					fmt.Println("no pack is imported (yardr pack apply <dir> --depot <depot>)")
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

// plannedImport is an import settled up to the writing: the plan, every
// answer, and the operator's yes.
type plannedImport struct {
	st      store.API
	plan    *pack.Plan
	answers pack.Answers
	open    *openAnswers
	calls   *callAnswers
	expose  *exposeAnswers
	refused map[string]string
	// sources says, by declared source, why this run does not add it.
	sources map[string]string
	prune   bool
	yes     bool
}

// planImport is an import up to the writing, as pack apply and depot init
// both run it: it reads the pack arg names, prints the plan with
// the flags' answers filled in, asks for what is open and for the
// confirmation. cmd is the command's name in what it says. also names what
// cmd writes besides the import once it is confirmed (depot init's guess at the depot's test command),
// "" for nothing: the confirmation then covers it, and is asked for it
// alone when the import itself changes nothing. A nil import with no error
// means the operator left a form or said no, and nothing is to be written.
// fits, when cmd has one, refuses a pack before anything of its plan is
// shown: depot init's, for a pack written for the other kind of depot.
func (a *app) planImport(ctx context.Context, cmd, arg string, f *importFlags, also string, fits func(*pack.Pack) error) (*plannedImport, error) {
	st, cfg, p, err := a.openPack(ctx, arg, &f.packFlags, nil)
	if err != nil {
		return nil, err
	}
	if fits != nil {
		if err := fits(p); err != nil {
			return nil, err
		}
	}
	asking := interactive() && !a.json
	plan, err := pack.Build(ctx, st, p, f.opts)
	if err != nil {
		return nil, err
	}
	// Which flow, which crew members and which groups of the yard shape the
	// plan itself, so they are settled first and the plan made again.
	// Whether a group of the yard takes a new one has an answer nobody need
	// give, a group of the depot's own as before there was the question: it
	// is asked in a terminal alone, not with --yes, and not of a group a
	// flag binds.
	shape := slices.DeleteFunc(shaping(plan), func(q pack.Question) bool {
		return q.Field == "group" && (!asking || f.yes || bound(plan, q, f.binds, f.members))
	})
	if len(shape) > 0 {
		if !asking {
			return nil, fmt.Errorf("%s cannot ask without a terminal: %s", cmd, strings.Join(flagsFor(plan, shape), "; "))
		}
		if err := a.askShape(ctx, st, p, plan, shape, &f.opts); err != nil {
			return nil, cancelled(err)
		}
		if plan, err = pack.Build(ctx, st, p, f.opts); err != nil {
			return nil, err
		}
	}
	if len(plan.Refused) > 0 {
		_ = a.out(plan, func() { printPlan(plan, nil, nil, nil, nil) })
		return nil, errCheckFailed
	}
	// The flags' answers go into the printed plan, so it shows what
	// each new group and crew member gets and lists only what is still
	// open. Where nothing can be asked, or --yes says not to, a new
	// agent gets its kind's unattended arguments.
	answers, err := packAnswers(plan, f.binds, f.members, cfg.Home, !asking || f.yes)
	if err != nil {
		return nil, err
	}
	// Whether a crew member may open threads is asked last, and only
	// where a no can be said: --yes says not to ask.
	open, err := a.openThreads(ctx, st, plan, f.opens, asking && !f.yes)
	if err != nil {
		return nil, err
	}
	// So is which calls of a declared source a group's sessions or a crew
	// member may make.
	calls, err := a.packCalls(ctx, st, plan, f.calls, asking && !f.yes)
	if err != nil {
		return nil, err
	}
	// And to which peers a crew member is exposed.
	expose, err := a.packExpose(ctx, st, plan, asking && !f.yes)
	if err != nil {
		return nil, err
	}
	// A hook that acts as a crew member speaks as it: the store refuses
	// it (store.PutHook); said here, the rest of the import is done.
	refused := map[string]string{}
	for _, o := range plan.Objects {
		if o.Kind != "map" || o.Map == nil || (o.Action != pack.Create && o.Action != pack.Change) {
			continue
		}
		if o.Map.As == "" {
			continue // the table alone: the hook acts as no one
		}
		if err := a.maySpeakAs(ctx, st, cmd+": the stage map of "+o.Map.Source, o.Map.As); err != nil {
			refused[o.Map.Source] = err.Error()
		}
	}
	// A source is where a credential of the yard is sent: the line yardr
	// source add draws. The store holds it for good; said here, the rest
	// of the import is done.
	sources := map[string]string{}
	for _, o := range plan.Objects {
		if o.Kind != "source" || o.Source == nil || (o.Action != pack.Create && o.Action != pack.Change) {
			continue
		}
		// As the store reads it (maySource): a yard that names no operator
		// has nobody to hold this to.
		settings, err := st.Settings(ctx)
		if err != nil {
			return nil, err
		}
		if operator := settings["operator"]; operator != "" && a.who() != operator {
			sources[o.Source.Name] = fmt.Sprintf("adding a source is the operator's (%s), and this is %s", operator, a.who())
		}
	}
	if !a.json {
		printPlan(plan, answers, open.Answers, calls.Answers, notDone(refused, sources))
	}
	// A copy someone edited is not overwritten on a confirmation
	// alone: the edit is lost for good, and its place is the pack.
	if edited := plan.EditedFiles(); len(edited) > 0 && !f.yes {
		return nil, fmt.Errorf("nothing was changed: %s; --yes proceeds", strings.Join(edited, "; "))
	}
	if missing := plan.Missing(answers); len(missing) > 0 {
		if !asking {
			return nil, fmt.Errorf("%s cannot ask without a terminal: %s", cmd, strings.Join(flagsFor(plan, missing), "; "))
		}
		fmt.Println()
		if err := a.askAnswers(ctx, st, plan, answers); err != nil {
			return nil, cancelled(err)
		}
	}
	todo := 0
	for _, o := range plan.Objects {
		if o.Kind == "map" && o.Map != nil && refused[o.Map.Source] != "" {
			continue
		}
		if o.Kind == "source" && sources[o.Name] != "" {
			continue
		}
		if o.Action == pack.Create || o.Action == pack.Change || (f.prune && o.Action == pack.Dropped && o.Held == "") {
			todo++
		}
	}
	if !asking && !f.yes && (todo+len(open.Answers)+len(calls.Answers) > 0 || also != "") {
		return nil, fmt.Errorf("%s changes the yard: give --yes, or run it in a terminal to be asked", cmd)
	}
	if (todo > 0 || also != "") && !f.yes {
		ok := true
		title := fmt.Sprintf("Apply pack %s to depot %s?", plan.Pack, plan.Depot)
		desc := fmt.Sprintf("%d objects to write or remove.", todo)
		if also != "" {
			desc += " Also written: " + also + "."
		}
		if err := ask(huh.NewConfirm().Title(title).Description(desc).Value(&ok)); err != nil {
			return nil, cancelled(err)
		}
		if !ok {
			fmt.Println("not applied")
			return nil, nil
		}
	}
	if err := open.ask(); err != nil {
		return nil, cancelled(err)
	}
	if err := calls.ask(); err != nil {
		return nil, cancelled(err)
	}
	if err := expose.ask(); err != nil {
		return nil, cancelled(err)
	}
	return &plannedImport{st: st, plan: plan, answers: answers, open: open, calls: calls, expose: expose, refused: refused, sources: sources, prune: f.prune, yes: f.yes}, nil
}

// apply writes a planned import and says what it did. extra is what the
// command wrote besides, added to --json's output; after prints it, below
// the import's result.
func (i *plannedImport) apply(ctx context.Context, a *app, extra map[string]any, after func()) error {
	res, err := pack.Apply(ctx, i.st, i.plan, pack.ApplyOptions{Answers: i.answers, OpenThreads: i.open.Answers, Calls: i.calls.Answers, Expose: i.expose.Answers, MapRefused: i.refused, SourceRefused: i.sources, Prune: i.prune, Overwrite: i.yes, Actor: a.who()})
	if res != nil {
		v := map[string]any{"plan": i.plan, "answers": i.answers, "open_threads": i.open, "calls": i.calls, "expose": i.expose, "maps_refused": i.refused, "sources_refused": i.sources, "result": res}
		maps.Copy(v, extra)
		if oerr := a.out(v, func() {
			printResult(i.plan, res, i.prune, i.answers, i.open, i.calls, i.expose, notDone(i.refused, i.sources))
			if after != nil {
				after()
			}
		}); oerr != nil {
			return oerr
		}
	}
	return err
}

// notDone is why this run leaves out a stage map or a declared source, by
// source: the two have no name in common, since a declared source never has
// the name of one yardr has built in.
func notDone(stageMaps, sources map[string]string) map[string]string {
	out := maps.Clone(stageMaps)
	maps.Copy(out, sources)
	return out
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
// which flow the depot follows, which crew member takes a pack's, and
// whether a group of the yard takes one the import would make.
func shaping(p *pack.Plan) []pack.Question {
	var out []pack.Question
	for _, q := range p.Questions {
		if q.ID == pack.FlowQuestion || q.Field == "crew" || q.Field == "group" {
			out = append(out, q)
		}
	}
	return out
}

// bound reports whether a --bind or --members flag names the new group q
// asks about: whoever says what a new group runs, or who is in it, has said
// that it is new.
func bound(p *pack.Plan, q pack.Question, binds, members []string) bool {
	_, yard, _ := strings.Cut(q.Object, " ")
	return slices.ContainsFunc(slices.Concat(binds, members), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return name == packName(p.Groups, yard)
	})
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
		case q.Field == "crew":
			flag = fmt.Sprintf("--crew %s=<crew member> (%s)", packName(p.Crew, name), q.Ask)
		case kind == "group" && q.Field == "members":
			flag = fmt.Sprintf("--members %s=<a,b> for the new group %s (empty: anyone)", packName(p.Groups, name), name)
		case kind == "group":
			flag = fmt.Sprintf("--bind %s=<kind>[:<model>] for the new group %s", packName(p.Groups, name), name)
		case kind == "crew":
			flag = fmt.Sprintf("--bind crew:%s=<kind>[:<model>] for the new crew member %s", packName(p.Crew, name), name)
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
// questions, and answers for a group still asked about that it is new. A
// flag must name a group or crew member of the pack; one for an
// object the yard already has answers nothing, since the yard's binding is
// kept, and is not an error, so the same command can be run again. A new
// crew member works in the yard's home, as one made by crew add does.
// With unattended, a new agent whose kind is answered and whose --bind names
// no preset gets its kind's unattended arguments, as yardr init gives them.
func packAnswers(p *pack.Plan, binds, members []string, home string, unattended bool) (pack.Answers, error) {
	answers := pack.Answers{}
	asked := map[string]bool{}
	for _, q := range p.Questions {
		asked[q.ID] = true
		if strings.HasPrefix(q.ID, "crew.") && q.Field == "dir" {
			answers[q.ID] = home
		}
		// Still in the plan, so no group of the yard was named to take it
		// (planImport): the group is new, under the name the plan gives it.
		if _, name, _ := strings.Cut(q.Object, " "); q.Field == "group" {
			answers[q.ID] = name
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
		rest, is := strings.CutPrefix(name, "crew:")
		if is {
			obj, names, name = "crew", p.Crew, rest
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
// its crew may open threads in.
type openAnswers struct {
	// Answers are the operator's, by the yard's crew member and source: what
	// pack.Apply grants and records.
	Answers map[string]map[string]bool `json:"answers"`
	// Notes say what was not granted and why, each naming who or which flag
	// would grant it.
	Notes []string `json:"notes"`
	// open are the crew still to ask about, in a terminal.
	open []pack.OpenThreads
}

// openThreads settles what the plan's OpenThreads come to: --open-threads
// answers for the pack's crew member it names, a terminal is asked for the
// rest (mayAsk), and without either nothing is granted and a note names the
// flag. The safe answer exists here, and it is no, so a missing flag is no
// error as it is for the import's other questions. Anyone but the operator
// answers nothing, by flag or by form: the grant is the store's to refuse
// (store.AllowOpening), and said here, the import does the rest.
func (a *app) openThreads(ctx context.Context, st store.API, p *pack.Plan, flags []string, mayAsk bool) (*openAnswers, error) {
	out := &openAnswers{Answers: map[string]map[string]bool{}, Notes: []string{}}
	named := map[string][]string{}
	for _, kv := range flags {
		name, v, ok := strings.Cut(kv, "=")
		if !ok || name == "" || v == "" {
			return nil, fmt.Errorf("--open-threads %q: want <pack's crew member>=<source>[,<source>], or =none", kv)
		}
		if _, ok := p.Crew[name]; !ok {
			return nil, fmt.Errorf("--open-threads %q: the pack has no crew member %s (it has: %s)", kv, name, strings.Join(sortedKeys(p.Crew), ", "))
		}
		i := slices.IndexFunc(p.OpenThreads, func(ot pack.OpenThreads) bool { return ot.Pack == name })
		if i < 0 {
			return nil, fmt.Errorf("--open-threads %q: the pack asks for no source its crew member %s may open threads in (open_threads)", kv, name)
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
			out.Notes = append(out.Notes, fmt.Sprintf("crew member %s was not let open threads in %s: only the operator (%s) may grant that, and this is %s. The rest of the import does not depend on it; %s applies the pack again to be asked",
				ot.CrewMember, strings.Join(about, ", "), operator, a.who(), operator))
		case flagged:
			out.Answers[ot.CrewMember] = answers
		case mayAsk:
			out.open = append(out.open, ot)
		default:
			out.Notes = append(out.Notes, fmt.Sprintf("crew member %s was not let open threads in %s: nothing is asked here, and the answer without a flag is no. --open-threads %s=%s grants it, --open-threads %s=none says no for good",
				ot.CrewMember, strings.Join(about, ", "), ot.Pack, strings.Join(about, ","), ot.Pack))
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

// ask asks the operator, once per crew member, whether it may open threads in
// the sources still open, and takes the one answer for all of them. No is
// the default: leaving the form at its first key grants nothing.
func (o *openAnswers) ask() error {
	for _, ot := range o.open {
		yes := false
		if err := ask(huh.NewConfirm().Title(fmt.Sprintf("Let %s open threads in %s?", ot.CrewMember, strings.Join(ot.Asks, ", "))).
			Description("The pack asks for it. Yes lets the crew member start a thread there unasked (yardr thread open); no leaves its setting as it is. Either answer is recorded and not asked again.").
			Value(&yes)); err != nil {
			return err
		}
		answers := map[string]bool{}
		for _, src := range ot.Asks {
			answers[src] = yes
		}
		o.Answers[ot.CrewMember] = answers
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
	for _, perm := range preset.Flags.Permissions {
		if presetLabel(perm.Label) == want {
			return perm.Args, nil
		}
		have = append(have, presetLabel(perm.Label))
	}
	return nil, fmt.Errorf("%s has no permission preset %q (it has: %s)", kind, label, strings.Join(append(have, "none"), ", "))
}

// askShape asks open, of the plan's shaping questions: which flow the depot
// follows, which crew member takes each of the pack's and whether a group of
// the yard takes one of its groups. It puts the answers into o.
func (a *app) askShape(ctx context.Context, st store.API, p *pack.Pack, plan *pack.Plan, open []pack.Question, o *pack.Options) error {
	for _, q := range open {
		if q.ID == pack.FlowQuestion {
			var opts []huh.Option[string]
			for _, f := range p.Flows {
				if f.Type == "" {
					opts = append(opts, huh.NewOption(f.Name, f.Name))
				}
			}
			o.Flow = q.Default
			if err := ask(huh.NewSelect[string]().Title(fmt.Sprintf("Which of the pack's flows does depot %s follow?", plan.Depot)).Options(opts...).Value(&o.Flow)); err != nil {
				return err
			}
			continue
		}
		_, yard, _ := strings.Cut(q.Object, " ")
		if q.Field == "group" {
			name := packName(plan.Groups, yard)
			have, err := st.Groups(ctx)
			if err != nil {
				return err
			}
			// The first, and the default: the name the plan gives the new
			// group, which as --group's value says that none takes it.
			pick := yard
			opts := []huh.Option[string]{huh.NewOption("A new group called "+yard, yard)}
			i := slices.IndexFunc(p.Groups, func(g pack.Group) bool { return g.Name == name })
			for _, h := range have {
				if i >= 0 && pack.Fits(p.Groups[i], h) {
					opts = append(opts, huh.NewOption(h.Name+"  ("+groupAgent(h)+")", h.Name))
				}
			}
			if err := ask(huh.NewSelect[string]().Title("Which group takes the pack's " + name + "?").
				Description("A group the yard has takes it as it is: the pack's routes lead to it, and its agent, model, arguments, limit, members and role stay.").
				Options(opts...).Value(&pick)); err != nil {
				return err
			}
			o.Groups[name] = pick
			continue
		}
		name := packName(plan.Crew, yard)
		have, err := st.Crew(ctx)
		if err != nil {
			return err
		}
		pick := name
		opts := []huh.Option[string]{huh.NewOption("A new crew member called "+name, name)}
		for _, r := range have {
			opts = append(opts, huh.NewOption(r.Name+"  ("+crewAgent(r)+")", r.Name))
		}
		if err := ask(huh.NewSelect[string]().Title("Which crew member takes the pack's " + name + "?").
			Description("It gets the pack's role and what the pack says it hears; its identity, directory, agent and model stay.").
			Options(opts...).Value(&pick)); err != nil {
			return err
		}
		o.Crew[name] = pick
	}
	return nil
}

// groupAgent is what a group of the yard runs, in a list of those that may
// take a pack's: its agent and model, its command, or its members, and its
// limit.
func groupAgent(g store.Group) string {
	var parts []string
	for _, key := range []string{"kind", "model", "cmd"} {
		if v, _ := g.Config[key].(string); v != "" {
			parts = append(parts, v)
		}
	}
	if g.Runner == "manual" {
		parts = append(parts, or(strings.Join(g.Members, ", "), "anyone"))
	}
	return strings.Join(append(parts, fmt.Sprintf("limit %d", g.Limit)), " · ")
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
			ans := groupAnswers{Kind: or(defaultKind(ctx, st), agents.DefaultKind), For: " for " + kindWord(o.Kind) + " " + o.Name}
			if err := a.agentKindModel(ctx, &ans); err != nil {
				return err
			}
			answers[id("kind")], answers[id("model")] = ans.Kind, ans.Model
		}
		if open("args") {
			ans := groupAnswers{Kind: answers[id("kind")], For: " for " + kindWord(o.Kind) + " " + o.Name}
			ans.PermArgs = agents.UnattendedArgs(ans.Kind)
			if err := a.agentPermissions(&ans); err != nil {
				return err
			}
			answers[id("args")] = strings.Join(ans.PermArgs, " ")
		}
		if open("members") {
			var v string
			if err := ask(huh.NewInput().Title("Members of group " + o.Name).Placeholder("mckean, yardmaster").
				Description("Optional, comma-separated actors (YARDR_ACTOR). If set, only they may advance beads out of this group's stages.").
				Value(&v)); err != nil {
				return err
			}
			answers[id("members")] = v
		}
	}
	return nil
}

func printResult(plan *pack.Plan, res *pack.Result, pruning bool, answers pack.Answers, open *openAnswers, calls *callAnswers, expose *exposeAnswers, refused map[string]string) {
	fmt.Println()
	if res.Wrote() {
		fmt.Printf("applied pack %s to depot %s: %d created, %d changed, %d unchanged", res.Pack, res.Depot, res.Created, res.Changed, res.Unchanged)
		if len(res.Pruned) > 0 {
			fmt.Printf(", %d pruned", len(res.Pruned))
		}
		fmt.Println()
	} else {
		fmt.Printf("pack %s is already applied to depot %s: nothing changed\n", res.Pack, res.Depot)
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
		fmt.Printf("%s %s is new and has no agent arguments: an unattended agent stops at every permission prompt until it is given them (yardr %s add, the form)\n", kindWord(o.Kind), o.Name, o.Kind)
	}
	for _, g := range res.Granted {
		fmt.Printf("crew member %s may now open threads in %s\n", g.CrewMember, strings.Join(g.Sources, ", "))
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
			fmt.Printf("crew member %s was not let open threads in %s: the no is recorded and not asked again (--open-threads %s=%s grants it after all)\n",
				name, strings.Join(no, ", "), packName(plan.Crew, name), strings.Join(no, ","))
		}
	}
	for _, n := range open.Notes {
		fmt.Println(n)
	}
	printExposed(res, expose)
	for _, m := range res.Mapped {
		if m.As != "" {
			fmt.Printf("hook %s now moves tracked %s issues as %s: %s\n", m.Hook, m.Source, m.As, strings.Join(pack.MapLines(m.Map), " "))
		}
		if len(m.Says) > 0 {
			fmt.Printf("hook %s's events now say of a bead:\n", m.Hook)
			for _, l := range store.SayLines(m.Says) {
				fmt.Printf("  %s\n", l)
			}
		}
		if len(m.Makes) > 0 {
			fmt.Printf("hook %s's events now make a bead for a thread none tracks:\n", m.Hook)
			for _, l := range store.MakeLines(m.Makes) {
				fmt.Printf("  %s\n", l)
			}
		}
	}
	// What the import did not do for a stage map, each with who does it and
	// how: it makes no hook, gives no identity and grants no set_state.
	for _, m := range plan.Maps {
		subject := "the stage map of " + m.Source + " was"
		if m.Pack == "" {
			subject = "what the events of " + m.Source + " say was"
		}
		if why := refused[m.Source]; why != "" {
			fmt.Printf("%s not set on hook %s: %s. The rest of the import does not depend on it\n", subject, m.Hook, why)
		}
		for _, what := range []string{"hook", "identity"} {
			if detail := m.Missing(what); detail != "" {
				fmt.Printf("%s not set: %s\n", subject, detail)
			}
		}
		if detail := m.Missing(store.SetState); detail != "" {
			fmt.Println("note: " + detail)
		}
	}
	// A declared source: what was added, and what the import leaves to the
	// operator, each with its command.
	for _, d := range plan.Declared {
		switch {
		case refused[d.Source] != "":
			fmt.Printf("the source %s was not added: %s. The rest of the import does not depend on it\n", d.Source, refused[d.Source])
			continue
		case slices.Contains(res.Declared, d.Source):
			fmt.Printf("source %s is declared: %s under %s (yardr source show %s)\n", d.Source, plural(len(d.Calls), "call"), d.Base, d.Source)
		}
		for _, n := range d.Needs {
			if n.Status != "ok" {
				fmt.Printf("next, for source %s: %s\n", d.Source, n.Detail)
			}
		}
	}
	// After the sources: a call is granted of one the yard has.
	printCallResult(plan, res, calls)
	for _, r := range res.Restart {
		fmt.Printf("crew member %s is running and was not restarted: its role or subscriptions changed, so restart it (yardr crew restart %s)\n", r, r)
	}
}

func printRecord(r store.PackImport) {
	fmt.Printf("%s in depot %s  %s by %s\n", r.Pack, r.Depot, r.At.Local().Format("2006-01-02 15:04"), r.By)
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
	if r.DepotRoles {
		roles = "the depot's own where it has them (--depot-roles)"
	}
	fmt.Printf("  roles      %s\n", roles)
	fmt.Printf("  groups     %s\n", pairs(r.Groups))
	fmt.Printf("  crew       %s\n", pairs(r.Crew))
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
	for _, who := range sortedKeys(r.Calls) {
		var said []string
		for _, call := range sortedKeys(r.Calls[who]) {
			said = append(said, call+" "+map[bool]string{true: "yes", false: "no"}[r.Calls[who][call]])
		}
		fmt.Printf("  may call   %s: %s\n", who, strings.Join(said, ", "))
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

// kindWord is a plan object's kind as a sentence names one of it: a crew
// member, where the kind is the command's word.
func kindWord(kind string) string {
	if kind == "crew" {
		return "crew member"
	}
	return kind
}

// planKinds are the kinds of object in a plan, in its order, with the
// heading each is printed under.
var planKinds = [][2]string{{"file", "files"}, {"group", "groups"}, {"flow", "flows"}, {"pointer", "flow pointers"},
	{"route", "routes"}, {"crew", "crew"}, {"subscription", "subscriptions"}, {"map", "stage maps"}, {"source", "declared sources"}}

// printPlan prints the plan. answers are those already given: an answered
// question is printed as what its object gets, and only the open ones are
// listed as what an import would ask. open are the answers to whether a
// crew member may open threads, by crew member and source, and calls those
// to which calls of a declared source a group's sessions or a crew member
// may make, by grantee and call. refused says, by source, why a stage map
// the plan would set or a declared source it would add is left out by this
// run.
func printPlan(p *pack.Plan, answers pack.Answers, open, calls map[string]map[string]bool, refused map[string]string) {
	kind := "of no kind"
	if p.Kind != "" {
		kind = "kind " + p.Kind
	}
	fmt.Printf("pack %s (%s), %s, into depot %s\n", p.Pack, p.Dir, kind, p.Depot)
	fmt.Printf("its roles and scripts would be copied to %s\n", p.Install)
	if slices.ContainsFunc(p.Objects, func(o pack.Object) bool { return o.File != nil && o.File.Stage != "" }) {
		fmt.Printf("its stage files would be copied to %s\n", filepath.Join(p.StageFiles, p.Pack+".<flow>"))
	}
	if slices.ContainsFunc(p.Objects, func(o pack.Object) bool { return o.File != nil && o.File.Fill }) {
		fmt.Printf("in its roles and stage files %s\n", fills(p.Fill))
	}
	count := map[string]int{}
	label := map[string]string{pack.Create: "create", pack.Change: "change", pack.Unchanged: "unchanged", pack.NotInPack: "not in the pack", pack.Dropped: "no longer in it", pack.Takes: "takes"}
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
			case o.Action == pack.Takes:
				line += " (the yard's, for the pack's " + packName(p.Groups, o.Name) + ": nothing of it would be changed)"
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
					if line := answered(id, v); line != "" {
						fmt.Printf("  %-15s    %s\n", "", line)
					}
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
		if _, done := open[ot.CrewMember]; !done && len(ot.Asks) > 0 {
			asks = append(asks, fmt.Sprintf("  the operator: let %s open threads in %s (--open-threads %s=%s, or =none)? [no]",
				ot.CrewMember, strings.Join(ot.Asks, ", "), ot.Pack, strings.Join(ot.Asks, ",")))
		}
	}
	asks = append(asks, planAsksCalls(p, calls)...)
	asks = append(asks, planAsksExpose(p)...)
	if len(asks) > 0 {
		fmt.Println("\nan import would ask")
		for _, line := range asks {
			fmt.Println(line)
		}
	}
	if len(p.Governs) > 0 {
		fmt.Printf("\nflows governing depot %s afterwards\n", p.Depot)
		for _, g := range p.Governs {
			who := "the depot's beads"
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
		fmt.Printf("\nsource %s, for crew member %s\n", s.Source, s.CrewMember)
		for _, n := range s.Needs {
			fmt.Printf("  %-8s %-12s %s\n", n.Status, strings.ReplaceAll(n.What, "_", " "), n.Detail)
		}
	}
	for _, ot := range p.OpenThreads {
		fmt.Printf("\nopen threads, for crew member %s\n", ot.CrewMember)
		fmt.Printf("  the pack asks for %s\n", strings.Join(ot.Wants, ", "))
		row := func(what string, sources []string, detail string) {
			if len(sources) > 0 {
				fmt.Printf("  %-9s %s%s\n", what, strings.Join(sources, ", "), detail)
			}
		}
		row("has", ot.Has, ": it may already, and nothing is asked")
		var yes, no, asked []string
		for _, src := range ot.Wants {
			switch said, done := open[ot.CrewMember][src]; {
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
		if m.Pack != "" {
			fmt.Printf("\nstage map of source %s, as crew member %s\n", m.Source, m.As)
			fmt.Printf("  the pack says %s\n", strings.Join(pack.MapLines(m.Map), " "))
		} else {
			fmt.Printf("\nwhat the events of source %s say of a bead, and make\n", m.Source)
		}
		for _, l := range store.SayLines(m.Says) {
			fmt.Printf("  the pack says %s\n", l)
		}
		for _, l := range store.MakeLines(m.Makes) {
			fmt.Printf("  the pack makes %s\n", l)
		}
		switch {
		case refused[m.Source] != "":
			fmt.Printf("  not set on hook %s: %s\n", m.Hook, refused[m.Source])
		case m.Action == pack.Create:
			fmt.Printf("  would be set on hook %s, which has none\n", m.Hook)
		case m.Action == pack.Change:
			var was []string
			if m.Pack != "" {
				was = append(was, "as "+or(m.Was.As, "(none)"), or(strings.Join(pack.MapLines(m.Was.Map), " "), "(no map)"))
			}
			if len(m.Says) > 0 {
				was = append(was, or(strings.Join(store.SayLines(m.Was.Says), "; "), "(no table)"))
			}
			if len(m.Makes) > 0 {
				was = append(was, or(strings.Join(store.MakeLines(m.Was.Makes), "; "), "(makes nothing)"))
			}
			fmt.Printf("  would replace what hook %s has: %s\n", m.Hook, strings.Join(was, ", "))
		case m.Action == pack.Unchanged:
			fmt.Printf("  hook %s has it\n", m.Hook)
		default:
			fmt.Println("  nothing would be set: the import goes through without it, and the next one sets it once the yard has what is missing")
		}
		for _, n := range m.Needs {
			fmt.Printf("  %-8s %-12s %s\n", n.Status, strings.ReplaceAll(n.What, "_", " "), n.Detail)
		}
	}
	for _, d := range p.Declared {
		fmt.Printf("\ndeclared source %s, from %s\n", d.Source, d.File)
		fmt.Printf("  its calls go to %s: %s\n", d.Base, strings.Join(d.Calls, ", "))
		switch {
		case refused[d.Source] != "":
			fmt.Printf("  not added: %s\n", refused[d.Source])
		case d.Action == pack.Create:
			fmt.Println("  would be added to the yard's sources")
		case d.Action == pack.Change:
			fmt.Println("  would replace the declaration the yard has under that name, which keeps its env_file (what changes is under declared sources, above)")
		default:
			fmt.Println("  the yard has it")
		}
		if r := d.Reaches; r != nil {
			who := make([]string, len(r.Who))
			for i, g := range r.Who {
				who[i] = g.String()
			}
			fmt.Printf("  through their grant of %s, %s would then call: %s (nobody is asked: %s covers the read calls a later file adds; yardr source revoke %s %s takes it back)\n",
				store.GrantRead, strings.Join(who, ", "), strings.Join(r.Calls, ", "), store.GrantRead, d.Source, store.GrantRead)
		}
		for _, n := range d.Needs {
			fmt.Printf("  %-8s %-12s %s\n", n.Status, strings.ReplaceAll(n.What, "_", " "), n.Detail)
		}
	}
	printPlanCalls(p, calls)
	printPlanExpose(p)
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
	if n := count[pack.Takes]; n > 0 {
		fmt.Printf(", %d of the yard's taken", n)
	}
	fmt.Println()
	if len(p.Refused) > 0 {
		fmt.Println("\nan import would be refused")
		for _, r := range p.Refused {
			fmt.Println("  " + r)
		}
	}
}

// answered prints the answer v to question id as what its object gets, ""
// for an answer that gives it nothing.
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
	case "group":
		// That no group of the yard takes it is what "create" says.
		return ""
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
