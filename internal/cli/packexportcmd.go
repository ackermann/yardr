package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/pack"
)

// packExportCmd is yardr pack export: the one verb that writes a pack
// where the others read one.
func (a *app) packExportCmd() *cobra.Command {
	var o pack.ExportOptions
	c := &cobra.Command{
		Use:   "export --depot <depot> [--name <pack>] <dir>",
		Short: "Write a depot's way of working out as a pack directory",
		Long: `Writes the way of working of one depot to <dir> as a pack: a directory
another yard can import (yardr pack check, yardr pack apply), or that you
can share. <dir> must be new or empty: a pack is edited by hand once it is
written, and an export never merges with what is there.

What is written:

  pack.yaml       the pack's name (--name, else the depot's), the depot's
                  kind, and the flows that govern the depot: its own and
                  each bead type's, with their stages, outcomes and manual
                  moves, however the depot came by them (a pack, the
                  store, or .yardr/flows in its repository)
  flows/          the stage file of each stage, as a brief shows it
  groups          in pack.yaml, the groups that work those stages: an
                  agent group with its role and limit, a script group with
                  its script and limit, a people group; a group's calls of
                  a source are requests only, never permission grants
  roles/          the role of each agent group, as a brief reads it
  scripts/        the script of each script group, byte for byte

In the role and stage files the operator's name becomes {{operator}}
again, wherever it stands as a whole word; an import puts the importing
yard's operator there. Each such line is printed: a word that only equals
the name is rewritten too, so read them.

What is never written, whatever the depot holds: a bead, a note, a
message or an inbox; a setting; a file of the depot other than its flows
and roles under .yardr/; its test command; an agent product, a model, an
argument, an environment file or a secret; the members of a people group;
a user, a device or a linked yard. This version writes no crew member,
source definition or hook. A group's calls of a source are written only
as requests; no permission grant is exported.

What the depot has and a pack cannot say is not dropped silently: it is
printed under "left out", each with why. A group's agent, model and
arguments stay in the yard, and an import asks for them. A stage is given
its group only where one plain route in the depot's effective routes (its
own, plus every yard default no own route of the same key sets aside) is
what works it today, whether that route is the depot's own or a yard
default the pack follows; a stage worked through a route with a condition
or further groups, or a route that wins for only some beads because of a
label, bead priority or type is written with no group, and the routes are
listed. A crew member is not exported: where a role or stage
file names one of the yard's, the name stays as plain text and the line is
listed.

The export refuses, and writes nothing, where the pack would be wrong
without saying so: a group worked by another yard (runner peer) on one of
the depot's routes, two groups or two flows that would get one name in the
pack, a script group whose command names no script file by its full path,
an agent group with no prompt or one whose prompt cannot be read, and a
role or stage file that already holds a {{placeholder}}.

Nothing in the yard is changed, and nothing is asked. What was written is
read back as yardr pack check reads a pack, and the verdict is printed.`,
		Example: `  yardr pack export --depot essays ./essays-pack
  yardr pack export --depot web --name team ~/packs/team
  yardr pack check ./essays-pack --depot <a depot of another yard>`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			// The operator as an import of this yard is told it (packOptions).
			o.Home, o.Dir, o.Operator = a.cfg.Home, args[0], yardOperator(c.Context(), st)
			ex, err := pack.Export(c.Context(), st, o)
			if err != nil {
				return err
			}
			return a.out(ex, func() { printExported(ex) })
		},
	}
	c.Flags().StringVar(&o.Depot, "depot", "", "the depot whose way of working is written")
	c.Flags().StringVar(&o.Name, "name", "", "what the pack calls itself (default: the depot's name)")
	_ = c.MarkFlagRequired("depot")
	return c
}

func printExported(ex *pack.Exported) {
	kind := ""
	if ex.Kind != "" {
		kind = ", kind " + ex.Kind
	}
	fmt.Printf("pack %s%s, written to %s from depot %s\n", ex.Pack, kind, ex.Dir, ex.Depot)
	fmt.Println("flows")
	for _, f := range ex.Flows {
		typ := ""
		if f.Type != "" {
			typ = "type " + f.Type + ", "
		}
		fmt.Printf("  %-16s %s%d stages, from %s\n", f.Name, typ, f.Stages, f.From)
	}
	if len(ex.Groups) > 0 {
		fmt.Println("groups")
	}
	for _, g := range ex.Groups {
		fmt.Printf("  %-16s %-7s %s\n", g.Name, g.Kind, strings.TrimSpace(g.File+"  (the yard's "+g.Yard+")"))
	}
	fmt.Println("files")
	for _, f := range ex.Files {
		fmt.Println("  " + f)
	}
	if len(ex.Restored) > 0 {
		fmt.Printf("{{operator}} stands where the yard's files said %s; read each line, a word that only equals the name was rewritten too\n", ex.Operator)
		for _, p := range ex.Restored {
			fmt.Printf("  %s:%d\n", p.File, p.Line)
		}
	}
	if len(ex.Left) > 0 {
		fmt.Println("left out")
		for _, l := range ex.Left {
			fmt.Println("  " + l)
		}
	}
	fmt.Println("never written: beads, notes, mail, settings, the depot's own files beside its flows and roles, its test command, crew members, source definitions, hooks or permission grants (group calls are requests only)")
	fmt.Printf("checked: %s reads back as a pack (yardr pack check %s --depot <depot> shows what importing it would change)\n", ex.Dir, ex.Dir)
}
