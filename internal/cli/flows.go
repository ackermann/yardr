package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mckean/yard/internal/dispatch"
	"github.com/mckean/yard/internal/store"
)

func (a *app) flowCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "flow", Short: "A rig's bead lifecycle: allowed stage transitions and terminal stages"}
	var typ, file string
	set := &cobra.Command{
		Use:   "set <rig> -f <file>",
		Short: "Set (replace) a rig's flow, or the flow for one bead type in the rig",
		Example: `  yard flow set yard -f flows/yard.flow
  yard flow set yard --type decide -f flows/decide.flow

A flow file has one rule per line (# starts a comment):

  backlog -> new
  new -> review | backlog
  review -> approved | new
  approved -> merged | merge | new
  merge -> approved
  decide -> backlog
  merged terminal`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if file == "" {
				return fmt.Errorf("-f is required (- reads stdin)")
			}
			var text string
			if file == "-" {
				var err error
				if text, err = readAllStdin(); err != nil {
					return err
				}
			} else {
				raw, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				text = string(raw)
			}
			f, err := store.ParseFlow(text)
			if err != nil {
				return err
			}
			f.Rig, f.Type = args[0], typ
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			saved, err := st.PutFlow(c.Context(), *f, a.who())
			if err != nil {
				return err
			}
			return a.out(saved, func() { fmt.Printf("flow %s: %d stages\n", flowName(*saved), len(saved.Stages)) })
		},
	}
	set.Flags().StringVarP(&typ, "type", "t", "", "only beads of this type (wins over the rig's flow)")
	set.Flags().StringVarP(&file, "file", "f", "", "flow file (- reads stdin)")

	show := &cobra.Command{
		Use:   "show <rig>",
		Short: "Show a rig's flows: each stage's group (and its members), allowed next stages and open beads",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			views, err := flowViews(c.Context(), st, args[0])
			if err != nil {
				return err
			}
			if len(views) == 0 {
				return a.out(views, func() { fmt.Printf("rig %s has no flow: beads may move to any stage\n", args[0]) })
			}
			return a.out(views, func() {
				for i, v := range views {
					if i > 0 {
						fmt.Println()
					}
					fmt.Printf("flow %s\n", flowName(v.Flow))
					rows := [][]string{}
					for _, s := range v.Stages {
						next := strings.Join(s.Next, " | ")
						switch {
						case s.Terminal:
							next = "(terminal: closes the bead)"
						case len(s.Next) == 0:
							next = "(no transitions: any stage)"
						}
						members := "-"
						if s.Human {
							members = or(strings.Join(s.Members, ","), "anyone")
						}
						rows = append(rows, []string{s.Stage, or(s.Group, "-"), members, strconv.Itoa(s.Open), next})
					}
					table("STAGE\tGROUP\tMEMBERS\tOPEN\tNEXT", rows)
					for _, w := range v.Warnings {
						fmt.Printf("warning: %s\n", w)
					}
				}
			})
		},
	}

	rm := &cobra.Command{
		Use:   "rm <rig>",
		Short: "Remove a rig's flow (or, with --type, the flow for one bead type)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			return st.DeleteFlow(c.Context(), args[0], typ, a.who())
		},
	}
	rm.Flags().StringVarP(&typ, "type", "t", "", "the flow for this bead type")
	cmd.AddCommand(set, show, rm)
	return cmd
}

type flowView struct {
	Flow     store.Flow  `json:"flow"`
	Stages   []stageView `json:"stages"`
	Warnings []string    `json:"warnings,omitempty"`
}

type stageView struct {
	Stage    string   `json:"stage"`
	Group    string   `json:"group,omitempty"`   // the route a bead of the flow's type in the rig would take
	Human    bool     `json:"human,omitempty"`   // Group is a manual group
	Members  []string `json:"members,omitempty"` // who may move a bead on from a human stage; none = anyone
	Next     []string `json:"next"`
	Terminal bool     `json:"terminal,omitempty"`
	Open     int      `json:"open"` // open beads in the stage that this flow governs
}

// flowViews describes rig's flows, the rig's own first.
func flowViews(ctx context.Context, st store.API, rig string) ([]flowView, error) {
	if _, err := st.GetRig(ctx, rig); err != nil {
		return nil, err
	}
	all, err := st.Flows(ctx)
	if err != nil {
		return nil, err
	}
	all = slices.DeleteFunc(all, func(f store.Flow) bool { return f.Rig != rig })
	slices.SortFunc(all, func(x, y store.Flow) int { return strings.Compare(x.Type, y.Type) })
	routes, err := st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	dispatch.SortRoutes(routes)
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	beads, err := st.ListBeads(ctx, store.BeadFilter{Rig: rig})
	if err != nil {
		return nil, err
	}
	typed := map[string]bool{}
	for _, f := range all {
		typed[f.Type] = f.Type != ""
	}
	views := []flowView{}
	for _, f := range all {
		v := flowView{Flow: f}
		for _, s := range f.Stages {
			sv := stageView{Stage: s, Next: []string{}, Terminal: f.IsTerminal(s)}
			if next := f.Transitions[s]; len(next) > 0 {
				sv.Next = next
			}
			// Routes may match on type; a rig flow is shown for the default type.
			probe := store.Bead{Stage: s, Type: or(f.Type, "task"), Rig: &rig}
			if r := dispatch.Match(routes, probe); r != nil {
				sv.Group = r.Group
				if i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == r.Group }); i >= 0 && groups[i].Runner == "manual" {
					sv.Human, sv.Members = true, groups[i].Members
				}
			} else if !sv.Terminal {
				v.Warnings = append(v.Warnings, fmt.Sprintf("stage %s has no route: beads there wait for a human", s))
			}
			for _, b := range beads {
				governed := b.Type == f.Type || (f.Type == "" && !typed[b.Type])
				if governed && b.Stage == s {
					sv.Open++
				}
			}
			v.Stages = append(v.Stages, sv)
		}
		views = append(views, v)
	}
	return views, nil
}

func flowName(f store.Flow) string {
	if f.Type != "" {
		return f.Rig + " (type " + f.Type + ")"
	}
	return f.Rig
}
