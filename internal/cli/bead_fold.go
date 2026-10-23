package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
)

func (a *app) newBeadFoldCmd() *cobra.Command {
	var to, title string
	fold := &cobra.Command{
		Use:   "fold <id> <id>... [--to <stage>] [--title <title>]",
		Short: "Fold beads into one bead that carries them all",
		Long: `Folds the beads into one new bead, the carrier, and closes each of them
"folded into <carrier>". The carrier's body holds each bead's body as a part,
in the order given, so one session builds and one review reads them all. Its
priority is the highest of theirs, its originator the oldest bead's, and its
labels are theirs but for size: and difficulty:, which it gets of its own
(difficulty: the highest of theirs, size: from what they weigh: small 1,
medium 2, large 4, none 2). What each bead tracked, blocked or was watched
for moves to the carrier; its notes and files stay on it, and a note on the
carrier says where.

The carrier starts in --to. Without it, in the stage the beads share, or,
when they wait in a siding (a stage worked by a manual group that sets fold,
fold_after or fold_max), in the stage the siding leads to first; beads in
different stages need --to. --title replaces the title the carrier gets,
"<depot> bundle: <title>; <title>".

The beads must be open, not worked by a session, and of one depot and one
flow, and none may have come from another yard, be away at one, or be blocked
by an open bead that is not folded with it: the carrier is made without its
members' blockers, so it would be ready while they are open. A manual group's
claim is a person's turn, not a session at work, so a bead it holds is folded
and the fold ends the claim. A fold is all or nothing: a bead it cannot take
leaves everything as it was. --json prints the carrier, with the beads it was
made of as members and what they weigh as points.`,
		Args: func(c *cobra.Command, args []string) error {
			if len(args) < 2 {
				return fmt.Errorf("a fold takes two beads or more: a carrier of one would be that bead with a worse title")
			}
			return nil
		},
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			// Read first, for the handles to print whatever form of id was
			// typed, and for the stage the carrier starts in.
			members := make([]store.Bead, len(args))
			ids := make([]string, len(args))
			for i, id := range args {
				b, err := st.GetBead(c.Context(), id)
				if err != nil {
					return err
				}
				members[i], ids[i] = *b, b.ID
			}
			stage := to
			if stage == "" {
				if stage, err = foldStage(c.Context(), st, members); err != nil {
					return err
				}
			}
			carrier, err := st.FoldBeads(c.Context(), ids, store.FoldOptions{To: stage, Title: title}, a.who())
			if err != nil {
				return err
			}
			// The fold went through; what follows only decides the line.
			points, err := foldedPoints(c.Context(), st, carrier.ID)
			if err != nil {
				return fmt.Errorf("%s folded from %s, but its points could not be read: %w", carrier.ID, strings.Join(ids, ", "), err)
			}
			return a.out(c.OutOrStdout(), struct {
				*store.Bead
				Members []string `json:"members"`
				Points  int      `json:"points"`
			}{carrier, ids, points}, func(w io.Writer) {
				fmt.Fprintln(w, foldLine(carrier, ids, points))
			})
		},
	}
	fold.Flags().StringVar(&to, "to", "", "the stage the carrier starts in (default: the stage the beads share, or a siding's next)")
	fold.Flags().StringVar(&title, "title", "", "the carrier's title (default: <depot> bundle: <title>; <title>)")
	return fold
}

// foldLine is what a fold prints: the carrier, the beads it was made of, what
// they weigh, the estimate the carrier got and where it starts.
func foldLine(carrier *store.Bead, from []string, points int) string {
	var estimate []string
	for _, prefix := range []string{"size:", "difficulty:"} {
		for _, l := range carrier.Labels {
			if strings.HasPrefix(l, prefix) {
				estimate = append(estimate, l)
			}
		}
	}
	weighs := fmt.Sprintf("%d points", points)
	if len(estimate) > 0 {
		weighs += ", " + strings.Join(estimate, " ")
	}
	return fmt.Sprintf("%s folded from %s (%s), in %s", carrier.ID, strings.Join(from, ", "), weighs, carrier.Stage)
}

// foldedPoints is what the fold that made carrier weighed, as the store
// counted it and wrote it on the carrier's folded event, so that the line
// says what the store did and not what a second count of the labels would.
func foldedPoints(ctx context.Context, st store.API, carrier string) (int, error) {
	// The event is the last of the fold's write, so it is among the newest.
	evs, err := st.Events(ctx, carrier, 20)
	if err != nil {
		return 0, err
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind != "folded" {
			continue
		}
		raw, err := json.Marshal(evs[i].Data)
		if err != nil {
			return 0, err
		}
		var data struct {
			Points int `json:"points"`
		}
		if err := json.Unmarshal(raw, &data); err != nil {
			return 0, err
		}
		return data.Points, nil
	}
	return 0, fmt.Errorf("the carrier has no folded event")
}

// foldStage is the stage a carrier starts in when the person names none, from
// where its members wait: the stage they share, or, when that stage is a
// siding, the stage the flow leads to first from it, since a siding is
// where beads wait to be folded and not where the fold waits. Members in
// different stages have no stage of the carrier's to take, and the person is
// asked for one.
func foldStage(ctx context.Context, st store.API, members []store.Bead) (string, error) {
	stage := members[0].Stage
	if slices.ContainsFunc(members, func(m store.Bead) bool { return m.Stage != stage }) {
		var stages []string
		at := map[string][]string{}
		for _, m := range members {
			if _, ok := at[m.Stage]; !ok {
				stages = append(stages, m.Stage)
			}
			at[m.Stage] = append(at[m.Stage], m.ID)
		}
		for i, s := range stages {
			stages[i] = s + ": " + strings.Join(at[s], ", ")
		}
		return "", fmt.Errorf("these beads wait in different stages (%s): say where the carrier starts with --to <stage>", strings.Join(stages, "; "))
	}
	siding, err := inSiding(ctx, st, members)
	if err != nil || !siding {
		return stage, err
	}
	next, err := st.NextStages(ctx, members[0].ID)
	if err != nil {
		return "", err
	}
	// A flow always leads on from a stage but its first, so this is a depot
	// that follows none, or a siding that is the flow's first stage.
	if len(next.Stages) == 0 {
		return "", fmt.Errorf("stage %s is a siding, but no stage follows it: say where the carrier starts with --to <stage>", stage)
	}
	return next.Stages[0], nil
}

// inSiding says whether each of the members waits in a siding: a stage whose
// route, for that bead, reaches a group that is one (store.SidingOf, which
// flow show asks as well). That is what makes a stage one, so that neither
// its name nor the flow says anything of it.
func inSiding(ctx context.Context, st store.API, members []store.Bead) (bool, error) {
	routes, err := st.Routes(ctx)
	if err != nil {
		return false, err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return false, err
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		return false, err
	}
	for _, m := range members {
		r := store.MatchRoute(routes, m, settings[store.OperatorSetting])
		if r == nil {
			return false, nil
		}
		i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == r.Worker() })
		if i < 0 {
			return false, nil
		}
		if _, ok := store.SidingOf(groups[i]); !ok {
			return false, nil
		}
	}
	return true, nil
}

// beadFolds is where a bead stands in a fold: the carriers it was folded into
// and the beads folded into it, each list oldest edge first. A bead is folded
// into more than one carrier only when it was reopened and folded again, and
// show names each rather than choose.
type beadFolds struct {
	Into []store.BeadRef
	From []store.BeadRef
}

// foldsOf reads the folded edges of b. A fold makes one from the carrier to
// each bead it folds, so b is the carrier of the edges that leave it and a
// member of the edges that end in it.
func foldsOf(ctx context.Context, st store.API, b *store.Bead) (*beadFolds, error) {
	deps, err := st.Deps(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	f := &beadFolds{Into: []store.BeadRef{}, From: []store.BeadRef{}}
	for _, d := range deps {
		if d.Kind != store.DepFolded {
			continue
		}
		other, refs := d.From, &f.Into
		if d.From == b.ID {
			other, refs = d.To, &f.From
		}
		o, err := st.GetBead(ctx, other)
		if err != nil {
			return nil, err
		}
		*refs = append(*refs, store.BeadRef{ID: o.ID, Title: o.Title, Status: o.Status})
	}
	return f, nil
}

// printFolds adds where a bead stands in a fold to bead show's text, as
// printDiscovery adds where it was found from: the carrier it was folded
// into, and the beads folded into it. Their titles are their writers' words,
// one line each, as letters only.
func printFolds(w io.Writer, f *beadFolds) {
	if f == nil {
		return
	}
	for _, o := range f.Into {
		fmt.Fprintf(w, "  folded into %s %s%s\n", o.ID, store.OneLine(o.Title), closedMark(o))
	}
	for _, o := range f.From {
		fmt.Fprintf(w, "  folded from %s %s%s\n", o.ID, store.OneLine(o.Title), closedMark(o))
	}
}

// closedMark is " (closed)" after a bead that is, as the lists of beads
// bead show prints mark them.
func closedMark(r store.BeadRef) string {
	if r.Status == "closed" {
		return " (closed)"
	}
	return ""
}
