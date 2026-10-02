package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

func (a *app) flowCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "flow", Short: "Bead lifecycles: named flows of allowed stage transitions and terminal stages"}
	var file string
	set := &cobra.Command{
		Use:   "set <name> -f <file>",
		Short: "Set (replace) a named flow; rigs and types point at it with 'yardr rig flow'",
		Example: `  yardr flow set code -f flows/code.flow
  yardr rig flow yard code                   # rig yard's beads follow it
  yardr rig flow webapp code                 # and webapp's, from the same flow

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
			f.Name = args[0]
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			saved, err := st.PutFlow(c.Context(), *f, a.who())
			if err != nil {
				return err
			}
			return a.out(saved, func() { fmt.Printf("flow %s: %d stages\n", saved.Name, len(saved.Stages)) })
		},
	}
	set.Flags().StringVarP(&file, "file", "f", "", "flow file (- reads stdin)")

	show := &cobra.Command{
		Use:   "show <rig|flow>",
		Short: "Show the flows governing a rig (each stage's group, members, open beads, next), or one named flow",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if _, err := st.GetRig(c.Context(), args[0]); errors.Is(err, store.ErrNotFound) {
				return a.showNamedFlow(c.Context(), st, args[0])
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
					fmt.Printf("flow %s (%s)\n", v.Flow.Name, v.Source)
					rows := [][]string{}
					for _, s := range v.Stages {
						// Edges names the outcome that takes each move, if any.
						next := strings.Join(v.Flow.Edges(s.Stage), " | ")
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

	list := &cobra.Command{
		Use:   "list",
		Short: "List named flows in the store and flows in rigs' repositories, with what each governs",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			rows, err := flowList(c.Context(), st)
			if err != nil {
				return err
			}
			return a.out(rows, func() {
				var out [][]string
				for _, r := range rows {
					out = append(out, []string{r.Name, r.Source, strconv.Itoa(r.Stages), or(strings.Join(r.Governs, ", "), "-")})
				}
				table("NAME\tSOURCE\tSTAGES\tGOVERNS", out)
			})
		},
	}

	rm := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a named flow (refused while a rig, type or open bead uses it)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			return st.DeleteFlow(c.Context(), args[0], a.who())
		},
	}
	cmd.AddCommand(set, show, list, rm)
	return cmd
}

// showNamedFlow prints a named flow from the store and the rigs and types
// pointing at it.
func (a *app) showNamedFlow(ctx context.Context, st store.API, name string) error {
	f, err := st.GetFlow(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%s is neither a rig nor a named flow: %w", name, store.ErrNotFound)
	} else if err != nil {
		return err
	}
	ptrs, err := st.FlowPointers(ctx)
	if err != nil {
		return err
	}
	users := []string{}
	for _, p := range ptrs {
		if p.Flow == name {
			users = append(users, p.String())
		}
	}
	v := map[string]any{"flow": f, "pointed_at_by": users}
	return a.out(v, func() {
		fmt.Printf("flow %s: %d stages, pointed at by %s\n\n%s", f.Name, len(f.Stages), or(strings.Join(users, ", "), "nothing"), f)
	})
}

type flowRow struct {
	Name    string   `json:"name"`
	Source  string   `json:"source"` // store, or repo <rig>@<sha> <path>
	Stages  int      `json:"stages"`
	Error   string   `json:"error,omitempty"`
	Governs []string `json:"governs"` // rigs and types it governs (pins aside)
}

// flowList is every named flow in the store, then every flow file in each
// rig's repository, with what each one governs now.
func flowList(ctx context.Context, st store.API) ([]flowRow, error) {
	flows, err := st.Flows(ctx)
	if err != nil {
		return nil, err
	}
	rigs, err := st.Rigs(ctx)
	if err != nil {
		return nil, err
	}
	governs := map[string][]string{} // store:<name> or repo:<rig>:<path>
	var rows []flowRow
	var repoRows []flowRow
	for _, r := range rigs {
		rfs, err := st.RigFlows(ctx, r.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: rig %s: %v\n", r.Name, err)
		}
		for _, rf := range rfs {
			k := "store:" + rf.Flow.Name
			if rf.Source.Path != "" {
				k = "repo:" + r.Name + ":" + rf.Source.Path
			}
			governs[k] = append(governs[k], store.FlowPointer{Rig: r.Name, Type: rf.Type}.String())
		}
		repo, err := st.RepoFlows(ctx, r.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: rig %s: %v\n", r.Name, err)
			continue
		}
		for _, f := range repo.Flows {
			row := flowRow{Name: strings.TrimSuffix(strings.TrimPrefix(f.Path, store.RepoFlowDir+"/"), ".flow"),
				Source: fmt.Sprintf("repo %s@%.7s", r.Name, repo.Commit), Error: f.Error,
				Governs: governs["repo:"+r.Name+":"+f.Path]}
			if f.Flow != nil {
				row.Stages = len(f.Flow.Stages)
			}
			repoRows = append(repoRows, row)
		}
	}
	for _, f := range flows {
		rows = append(rows, flowRow{Name: f.Name, Source: "store", Stages: len(f.Stages), Governs: governs["store:"+f.Name]})
	}
	return append(rows, repoRows...), nil
}

type flowView struct {
	Flow     store.Flow       `json:"flow"`
	Type     string           `json:"type,omitempty"` // the bead type it governs; "" for the rig's own
	Source   store.FlowSource `json:"source"`
	Stages   []stageView      `json:"stages"`
	Warnings []string         `json:"warnings,omitempty"`
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

// flowViews describes the flows governing rig's beads, the rig's own first.
func flowViews(ctx context.Context, st store.API, rig string) ([]flowView, error) {
	if _, err := st.GetRig(ctx, rig); err != nil {
		return nil, err
	}
	all, err := st.RigFlows(ctx, rig)
	if err != nil {
		return nil, err
	}
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
	for _, rf := range all {
		typed[rf.Type] = rf.Type != ""
	}
	views := []flowView{}
	for _, rf := range all {
		f := rf.Flow
		v := flowView{Flow: *f, Type: rf.Type, Source: rf.Source}
		for _, s := range f.Stages {
			sv := stageView{Stage: s, Next: []string{}, Terminal: f.IsTerminal(s)}
			if next := f.Transitions[s]; len(next) > 0 {
				sv.Next = next
			}
			// Routes may match on type; a rig flow is shown for the default type.
			probe := store.Bead{Stage: s, Type: or(rf.Type, "task"), Rig: &rig}
			if r := dispatch.Match(routes, probe); r != nil {
				sv.Group = r.Group
				if i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == r.Group }); i >= 0 && groups[i].Runner == "manual" {
					sv.Human, sv.Members = true, groups[i].Members
				}
			} else if !sv.Terminal {
				v.Warnings = append(v.Warnings, fmt.Sprintf("stage %s has no route: beads there wait for a human", s))
			}
			for _, b := range beads {
				// A bead that pins its flow follows that one instead.
				governed := store.PinnedFlow(&b) == "" && (b.Type == rf.Type || (rf.Type == "" && !typed[b.Type]))
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
