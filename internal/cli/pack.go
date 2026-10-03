package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
)

func (a *app) packCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "pack", Short: "A yard's way of working as a directory: flows, groups, roles, residents"}

	var (
		opts      pack.Options
		residents []string
	)
	check := &cobra.Command{
		Use:   "check <dir> --rig <rig>",
		Short: "Read a pack and show what importing it into a rig would change",
		Long: `Reads the pack in <dir> (pack.yaml, roles/, scripts/), checks it, and prints
what importing it into the rig would do to this yard, object by object:
create, change (field: old -> new), unchanged, or not in the pack (the yard
has it, the pack does not, and an import would leave it alone). Then what an
import would ask, which flow would govern the rig, and what each source a
resident subscribes to still lacks. Nothing is written.

It exits 1 on a pack that is not valid, naming every mistake by file, line
and key, and on a plan an import would refuse (open beads in a stage the
pack's flow lacks). --json prints the plan as data. See docs/packs.md.`,
		Example: `  yardr pack check packs/starter --rig yardr
  yardr pack check ~/packs/starter --rig yardr --resident mayor=yardmaster`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := pack.Load(args[0])
			if err != nil {
				var errs pack.Errors
				if a.json && errors.As(err, &errs) {
					// The mistakes as data, then the exit code; Execute adds
					// nothing to what a red check has printed.
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					_ = enc.Encode(map[string]any{"error": "the pack is not valid", "errors": errs})
					return errCheckFailed
				}
				return err
			}
			opts.Residents = map[string]string{}
			for _, kv := range residents {
				from, to, ok := strings.Cut(kv, "=")
				if !ok || from == "" || to == "" {
					return fmt.Errorf("--resident %q: want <pack's name>=<yard's resident>", kv)
				}
				opts.Residents[from] = to
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			cfg := a.cfg
			if cfg == nil {
				if cfg, err = config.Load(); err != nil {
					return err
				}
			}
			opts.Home, opts.HookAddr = cfg.Home, cfg.HookAddr
			plan, err := pack.Build(c.Context(), st, p, opts)
			if err != nil {
				return err
			}
			if err := a.out(plan, func() { printPlan(plan) }); err != nil {
				return err
			}
			if len(plan.Refused) > 0 {
				return errCheckFailed
			}
			return nil
		},
	}
	check.Flags().StringVar(&opts.Rig, "rig", "", "the rig the pack would be imported into")
	check.Flags().StringVar(&opts.Flow, "flow", "", "the pack's flow the rig would follow (default: its first flow without a type)")
	check.Flags().StringArrayVar(&residents, "resident", nil, "<pack's name>=<yard's resident>: which resident takes a resident of the pack (repeatable)")
	_ = check.MarkFlagRequired("rig")

	cmd.AddCommand(check)
	return cmd
}

// planKinds are the kinds of object in a plan, in its order, with the
// heading each is printed under.
var planKinds = [][2]string{{"file", "files"}, {"group", "groups"}, {"flow", "flows"}, {"pointer", "flow pointers"},
	{"route", "routes"}, {"resident", "residents"}, {"subscription", "subscriptions"}}

func printPlan(p *pack.Plan) {
	fmt.Printf("pack %s (%s) into rig %s\n", p.Pack, p.Dir, p.Rig)
	fmt.Printf("its roles and scripts would be copied to %s\n", p.Install)
	count := map[string]int{}
	label := map[string]string{pack.Create: "create", pack.Change: "change", pack.Unchanged: "unchanged", pack.NotInPack: "not in the pack"}
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
			if o.Action == pack.NotInPack {
				line += " (would be left alone)"
			}
			fmt.Println(line)
			for _, d := range o.Changes {
				fmt.Printf("  %-15s    %s: %s -> %s\n", "", d.Field, planValue(d.Old), planValue(d.New))
			}
			for _, id := range o.Asks {
				fmt.Printf("  %-15s    asks: %s\n", "", id)
			}
		}
	}
	if len(p.Questions) > 0 {
		fmt.Println("\nan import would ask")
		for _, q := range p.Questions {
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
	fmt.Printf("\n%d to create, %d to change, %d unchanged, %d not in the pack\n",
		count[pack.Create], count[pack.Change], count[pack.Unchanged], count[pack.NotInPack])
	if len(p.Refused) > 0 {
		fmt.Println("\nan import would be refused")
		for _, r := range p.Refused {
			fmt.Println("  " + r)
		}
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
