package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		Short: "Set (replace) a named flow; depots and types point at it with 'yardr depot flow'",
		Example: `  yardr flow set code -f flows/code.flow
  yardr depot flow yard code                   # depot yard's beads follow it
  yardr depot flow webapp code                 # and webapp's, from the same flow

A flow file has one rule per line (# starts a comment):

  backlog -> new
  new -> review | backlog
  review -> approved | new
  approved -> merged | merge | new
  merge -> approved
  decide -> backlog
  merged terminal

Stages are ordered as the rules first name them; the first stage is where a
bead in a stage the flow does not know rejoins it. A JSON flow lists them
under "stages", or is refused when two source stages have no order.

Replacing a flow is refused, naming the beads, while an open bead that
follows it (through a depot's or type's pointer, or its own pin) sits in a
stage the new version lacks. Advance them to a stage it has, or pin them to
another flow (yardr bead update <id> --meta flow=<name>), then set it again.

The flow's stage files, markdown saying what each stage is for, are plain
files in the yard's home: <home>/flows/<name>/<stage>.md. Whoever works a
bead in the stage is shown it, in the brief and in yardr prime, in every depot
that follows the flow; yardr flow show <name> says which stages have one. (A
flow in a depot's repository has them beside it: ` + store.RepoFlowDir + `/<name>/<stage>.md.)`,
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
		Use:   "show <depot|flow>",
		Short: "Show the flows governing a depot (each stage's group, members, open beads, stage file, next), or one named flow",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if _, err := st.GetDepot(c.Context(), args[0]); errors.Is(err, store.ErrNotFound) {
				return a.showNamedFlow(c.Context(), st, args[0])
			}
			views, err := flowViews(c.Context(), st, args[0], a.home())
			if err != nil {
				return err
			}
			if len(views) == 0 {
				return a.out(views, func() { fmt.Printf("depot %s has no flow: beads may move to any stage\n", args[0]) })
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
						} else if s.Yard != "" {
							// Who works it is another yard, not anyone here.
							members = "yard " + s.Yard
						}
						file := "-"
						if s.StageFile != "" {
							file = "yes"
						}
						rows = append(rows, []string{s.Stage, or(s.Group, "-"), members, strconv.Itoa(s.Open), file, next})
					}
					table("STAGE\tGROUP\tMEMBERS\tOPEN\tFILE\tNEXT", rows)
					for _, w := range v.Warnings {
						fmt.Printf("warning: %s\n", w)
					}
				}
			})
		},
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List named flows in the store and flows in depots' repositories, with what each governs",
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
		Short: "Remove a named flow (refused while a depot, type or open bead uses it)",
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

// showNamedFlow prints a named flow from the store and the depots and types
// pointing at it.
func (a *app) showNamedFlow(ctx context.Context, st store.API, name string) error {
	f, err := st.GetFlow(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%s is neither a depot nor a named flow: %w", name, store.ErrNotFound)
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
	// The flow is in the store, so its stage files are in the yard's home.
	have, stray, err := store.HomeStageFiles(a.home(), f)
	if err != nil {
		return err
	}
	v := map[string]any{"flow": f, "pointed_at_by": users}
	if len(have) > 0 {
		v["stage_files"] = have
	}
	if len(stray) > 0 {
		v["stray_stage_files"] = stray
	}
	return a.out(v, func() {
		var with []string
		for _, s := range f.Stages {
			if have[s] != "" {
				with = append(with, s)
			}
		}
		fmt.Printf("flow %s: %d stages, pointed at by %s\n", f.Name, len(f.Stages), or(strings.Join(users, ", "), "nothing"))
		fmt.Printf("stage files: %s (%s)\n\n%s", or(strings.Join(with, ", "), "none"), filepath.Join(store.HomeStageDir(a.home(), f.Name), "<stage>.md"), f)
		for _, s := range stray {
			fmt.Printf("warning: stage file %s\n", s)
		}
	})
}

// home is the yard's home, where a store flow's stage files live; "" when
// this run has none.
func (a *app) home() string {
	if a.cfg == nil {
		return ""
	}
	return a.cfg.Home
}

type flowRow struct {
	Name    string   `json:"name"`
	Source  string   `json:"source"` // store, or repo <depot>@<sha> <path>
	Stages  int      `json:"stages"`
	Error   string   `json:"error,omitempty"`
	Governs []string `json:"governs"` // depots and types it governs (pins aside)
}

// flowList is every named flow in the store, then every flow file in each
// depot's repository, with what each one governs now.
func flowList(ctx context.Context, st store.API) ([]flowRow, error) {
	flows, err := st.Flows(ctx)
	if err != nil {
		return nil, err
	}
	depots, err := st.Depots(ctx)
	if err != nil {
		return nil, err
	}
	governs := map[string][]string{} // store:<name> or repo:<depot>:<path>
	var rows []flowRow
	var repoRows []flowRow
	for _, r := range depots {
		rfs, err := st.DepotFlows(ctx, r.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: depot %s: %v\n", r.Name, err)
		}
		for _, rf := range rfs {
			k := "store:" + rf.Flow.Name
			if rf.Source.Path != "" {
				k = "repo:" + r.Name + ":" + rf.Source.Path
			}
			governs[k] = append(governs[k], store.FlowPointer{Depot: r.Name, Type: rf.Type}.String())
		}
		repo, err := st.RepoFlows(ctx, r.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: depot %s: %v\n", r.Name, err)
			continue
		}
		src := fmt.Sprintf("repo %s@%.7s", r.Name, repo.Commit)
		if repo.Dir != "" {
			src = "dir " + r.Name
		}
		for _, f := range repo.Flows {
			row := flowRow{Name: strings.TrimSuffix(strings.TrimPrefix(f.Path, store.RepoFlowDir+"/"), ".flow"),
				Source: src, Error: f.Error,
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
	Type     string           `json:"type,omitempty"` // the bead type it governs; "" for the depot's own
	Source   store.FlowSource `json:"source"`
	Stages   []stageView      `json:"stages"`
	Warnings []string         `json:"warnings,omitempty"`
}

type stageView struct {
	Stage    string   `json:"stage"`
	Group    string   `json:"group,omitempty"`   // the route a bead of the flow's type in the depot would take
	Human    bool     `json:"human,omitempty"`   // Group is a manual group
	Members  []string `json:"members,omitempty"` // who may move a bead on from a human stage; none = anyone
	Yard     string   `json:"yard,omitempty"`    // the linked yard that works the stage: Group is a peer group
	Next     []string `json:"next"`
	Terminal bool     `json:"terminal,omitempty"`
	Open     int      `json:"open"` // open beads in the stage that this flow governs
	// StageFile is the stage's file, if it has one: in the depot's repository,
	// or in the yard's home for a store flow.
	StageFile string `json:"stage_file,omitempty"`
}

// flowViews describes the flows governing depot's beads, the depot's own first.
// home is the yard's home, where the stage files of a store flow are.
func flowViews(ctx context.Context, st store.API, depot, home string) ([]flowView, error) {
	if _, err := st.GetDepot(ctx, depot); err != nil {
		return nil, err
	}
	all, err := st.DepotFlows(ctx, depot)
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
	beads, err := st.ListBeads(ctx, store.BeadFilter{Depot: depot})
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
		files := rf.StageFiles
		if rf.Source.InStore() {
			var stray []string
			if files, stray, err = store.HomeStageFiles(home, f); err != nil {
				return nil, err
			}
			for _, s := range stray {
				v.Warnings = append(v.Warnings, "stage file "+s)
			}
		}
		for _, s := range f.Stages {
			sv := stageView{Stage: s, Next: []string{}, Terminal: f.IsTerminal(s), StageFile: files[s]}
			if next := f.Transitions[s]; len(next) > 0 {
				sv.Next = next
			}
			// Routes may match on type; a depot flow is shown for the default type.
			probe := store.Bead{Stage: s, Type: or(rf.Type, "task"), Depot: &depot}
			if r := dispatch.Match(routes, probe); r != nil {
				sv.Group = r.Group
				if i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == r.Group }); i >= 0 && groups[i].Runner == "manual" {
					sv.Human, sv.Members = true, groups[i].Members
				} else if i >= 0 {
					sv.Yard = groups[i].AtPeer()
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
	// Beads in a stage their flow lacks, under the flow that lacks it; a
	// bead pinning a flow the depot does not follow is told under the depot's.
	stranded, err := st.Stranded(ctx, depot)
	if err != nil {
		return nil, err
	}
	for _, sb := range stranded {
		i := slices.IndexFunc(views, func(v flowView) bool { return v.Flow.Name == sb.Flow && v.Source == sb.Source })
		if i < 0 {
			// A bead may pin a flow that does not otherwise govern this depot.
			// Keep its warning visible even when there are no normal views.
			views = append(views, flowView{Flow: store.Flow{Name: sb.Flow}, Source: sb.Source})
			i = len(views) - 1
		}
		views[i].Warnings = append(views[i].Warnings,
			fmt.Sprintf("bead %s: it is not routed and waits until someone advances it (yardr bead advance %s --to <stage>)", sb, sb.Bead))
	}
	return views, nil
}
