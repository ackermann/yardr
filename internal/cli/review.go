package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// reviewItem is a bead waiting on a human: open, in a stage a manual group
// works, and movable on by the actor.
type reviewItem struct {
	Bead    store.Bead     `json:"bead"`
	Group   string         `json:"group"`
	Members []string       `json:"members,omitempty"` // none = anyone
	Actions []reviewAction `json:"actions"`
	// FlowError says why the bead's flow does not resolve; every move is
	// refused until it is fixed, so only diff and skip are offered.
	FlowError string `json:"flow_error,omitempty"`
}

// Action kinds. advance moves the bead to Stage; done reports Outcome,
// which the flow sends to Stage; diff and skip leave it.
const (
	actAdvance = "advance"
	actDone    = "done"
	actDiff    = "diff"
	actSkip    = "skip"
)

type reviewAction struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Stage string `json:"stage,omitempty"`
	// Outcome is the outcome a done action reports.
	Outcome string `json:"outcome,omitempty"`
	// NoteRequired: sending a bead back needs a note saying what to change,
	// or whoever picks it up next has nothing to go on.
	NoteRequired bool `json:"note_required,omitempty"`
	Terminal     bool `json:"terminal,omitempty"` // entering Stage closes the bead
}

// reviewQueue lists the open beads waiting on actor in human stages, the
// longest untouched first. A bead's group is the one working it, else the
// one its routes send it to; a group with members only shows its beads to
// them, mirroring Advance's check. only, if set, keeps those groups'
// beads (placeholder stages such as backlog are manual too).
func reviewQueue(ctx context.Context, st store.API, actor string, only []string) ([]reviewItem, error) {
	beads, err := st.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		return nil, err
	}
	routes, err := st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	store.SortRoutes(routes)
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	operator := settings[store.OperatorSetting]
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]store.Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	items := []reviewItem{}
	for _, b := range beads {
		name := deref(b.Group)
		if b.Group == nil {
			r := store.MatchRoute(routes, b, operator)
			if r == nil {
				continue
			}
			name = r.Worker()
		}
		g, ok := byName[name]
		if !ok || g.Runner != "manual" || (len(only) > 0 && !slices.Contains(only, g.Name)) {
			continue
		}
		if len(g.Members) > 0 && !slices.Contains(g.Members, actor) {
			continue
		}
		it := reviewItem{Bead: b, Group: g.Name, Members: g.Members}
		if next, err := st.NextStages(ctx, b.ID); err != nil {
			it.FlowError, it.Actions = err.Error(), lookActions()
		} else {
			it.Actions = reviewActions(b, next.Flow, routes)
		}
		items = append(items, it)
	}
	slices.SortStableFunc(items, func(x, y reviewItem) int {
		if c := x.Bead.UpdatedAt.Compare(y.Bead.UpdatedAt); c != 0 {
			return c
		}
		return strings.Compare(x.Bead.ID, y.Bead.ID)
	})
	return items, nil
}

// reviewActions is what a reviewer may do with b. Under a flow that names
// outcomes for b's stage: those outcomes. Under a flow without: each stage
// it allows next. Either way a stage earlier in the flow sends the bead
// back and needs a note. Without a flow: any stage a route uses. Then
// always open diff and skip.
func reviewActions(b store.Bead, f *store.Flow, routes []store.Route) []reviewAction {
	var out []reviewAction
	if f != nil {
		here := slices.Index(f.Stages, b.Stage)
		// move describes going to s; its label starts lower-case to follow an outcome.
		move := func(s string) reviewAction {
			a := reviewAction{Kind: actAdvance, Stage: s, Terminal: f.IsTerminal(s)}
			switch {
			case a.Terminal:
				a.Label = "finish as " + s + " (closes the bead)"
			case here >= 0 && slices.Index(f.Stages, s) < here:
				a.Label, a.NoteRequired = "send back to "+s+" (note required)", true
			default:
				a.Label = "move to " + s
			}
			return a
		}
		if outcomes := f.OutcomesFrom(b.Stage); len(outcomes) > 0 {
			for _, o := range outcomes {
				a := move(o.To)
				a.Kind, a.Outcome, a.Label = actDone, o.Name, o.Name+": "+a.Label
				out = append(out, a)
			}
		} else {
			for _, s := range f.Next(b.Stage) {
				a := move(s)
				a.Label = strings.ToUpper(a.Label[:1]) + a.Label[1:]
				out = append(out, a)
			}
		}
	} else {
		var stages []string
		for _, r := range routes {
			if r.Stage != b.Stage && !slices.Contains(stages, r.Stage) {
				stages = append(stages, r.Stage)
			}
		}
		slices.Sort(stages)
		for _, s := range stages {
			out = append(out, reviewAction{Kind: actAdvance, Stage: s, Label: "Move to " + s})
		}
	}
	return append(out, lookActions()...)
}

// lookActions are offered for every bead: they leave it where it is.
func lookActions() []reviewAction {
	return []reviewAction{
		{Kind: actDiff, Label: "Open diff"},
		{Kind: actSkip, Label: "Skip"},
	}
}

// diffRange is the bead branch against the depot's base, as git diff takes
// it, and the depot checkout to run it in. A dir depot has no branches: its
// beads' outputs are files in the directory, named in their notes.
func diffRange(ctx context.Context, st store.API, b store.Bead) (dir, rng string, err error) {
	if b.Depot == nil {
		return "", "", fmt.Errorf("bead %s has no depot", b.ID)
	}
	depot, err := st.GetDepot(ctx, *b.Depot)
	if err != nil {
		return "", "", err
	}
	if depot.IsDir() {
		return "", "", fmt.Errorf("no diff in dir depot %s: the outputs are in %s, as the notes name them", depot.Name, depot.Path)
	}
	branch := runner.Branch(b)
	if err := exec.CommandContext(ctx, "git", "-C", depot.Path, "rev-parse", "--verify", "--quiet", branch+"^{commit}").Run(); err != nil {
		return "", "", fmt.Errorf("no branch %s in %s", branch, depot.Path)
	}
	return depot.Path, or(depot.Base, "origin/main") + "..." + branch, nil
}

// diffStat summarises the bead's branch, or says why there is nothing to show.
func diffStat(ctx context.Context, st store.API, b store.Bead) string {
	dir, rng, err := diffRange(ctx, st, b)
	if err != nil {
		return err.Error()
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--stat", rng).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("git diff --stat %s: %v: %s", rng, err, strings.TrimSpace(string(out)))
	}
	if s := strings.TrimRight(string(out), "\n"); s != "" {
		return s
	}
	return "no changes against " + rng
}

// pageDiff shows the full diff through $PAGER (less -R if unset).
func pageDiff(ctx context.Context, st store.API, b store.Bead) error {
	dir, rng, err := diffRange(ctx, st, b)
	if err != nil {
		return err
	}
	git := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--color=always", rng)
	pager := exec.CommandContext(ctx, "sh", "-c", or(os.Getenv("PAGER"), "less -R"))
	if pager.Stdin, err = git.StdoutPipe(); err != nil {
		return err
	}
	git.Stderr = os.Stderr
	pager.Stdout, pager.Stderr = os.Stdout, os.Stderr
	if err := git.Start(); err != nil {
		return err
	}
	perr := pager.Run()
	// A pager quit early closes the pipe; git's broken-pipe exit is not news.
	_ = git.Wait()
	return perr
}

// lastNotes formats the bead's newest n notes, oldest first: an ask as
// numbered text with its state, its answers folded into it. An answer
// from another yard says whose word it is, as mail does; nil as reads
// unmarked. A note with files names them under its text, on a marked
// line of their own, each image by its path in the yard at home.
func lastNotes(notes []store.Note, n int, as map[string]string, home string) string {
	blocks := store.NoteBlocks(notes, time.Now(), home)
	if as != nil {
		blocks = store.NoteBlocksMarked(notes, time.Now(), as, home)
	}
	if len(blocks) > n {
		blocks = blocks[len(blocks)-n:]
	}
	return strings.Join(blocks, "\n\n")
}

func (a *app) reviewCmd() *cobra.Command {
	var only []string
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Work through beads waiting on you in human stages, one form per bead",
		Long: `Walks the open beads in stages routed to manual groups (only those whose
group has you as a member, if it lists members), longest untouched first.
Each shows its notes and the diff of its branch against the depot's base (for
a dir depot's bead, where its outputs are); pick where it goes next, open the
full diff, or skip it.

For scripts: yardr review list, and yardr bead done <id> --outcome <outcome>
--note <text> (or yardr bead advance <id> --to <stage> where the stage
names no outcomes).`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			items, err := reviewQueue(c.Context(), st, a.who(), only)
			if err != nil {
				return err
			}
			if a.json {
				return a.out(items, nil)
			}
			// The form needs a terminal; piped or scripted, list instead of
			// failing on a prompt nobody can answer.
			if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
				printReviewList(items)
				fmt.Fprintln(os.Stderr, "not a terminal: listed instead of the form; move beads with yardr bead done or yardr bead advance")
				return nil
			}
			return a.reviewForm(c.Context(), st, items)
		},
	}
	list := &cobra.Command{
		Use: "list", Short: "List the beads yardr review would show, with the moves each allows", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			items, err := reviewQueue(c.Context(), st, a.who(), only)
			if err != nil {
				return err
			}
			return a.out(items, func() { printReviewList(items) })
		},
	}
	cmd.PersistentFlags().StringSliceVarP(&only, "group", "g", nil, "only these manual groups' beads")
	cmd.AddCommand(list)
	return cmd
}

// printReviewList prints the review queue as a table, one bead per row with
// the moves it allows. The title is its writer's words, one line, as
// letters only, so it cannot start a row of its own.
func printReviewList(items []reviewItem) {
	rows := [][]string{}
	for _, it := range items {
		var next []string
		for _, ac := range it.Actions {
			switch ac.Kind {
			case actAdvance:
				next = append(next, ac.Stage)
			case actDone:
				next = append(next, ac.Outcome+":"+ac.Stage)
			}
		}
		nextCol := or(strings.Join(next, " | "), "-")
		if it.FlowError != "" {
			nextCol = "flow error: " + it.FlowError
		}
		rows = append(rows, []string{it.Bead.ID, it.Bead.Stage, it.Group, age(it.Bead.UpdatedAt),
			nextCol, store.OneLine(it.Bead.Title)})
	}
	table("ID\tSTAGE\tGROUP\tWAITING\tNEXT\tTITLE", rows)
}

// age is how long ago t was, coarsely.
func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// reviewForm asks, bead by bead, what to do with each item.
func (a *app) reviewForm(ctx context.Context, st store.API, items []reviewItem) error {
	if len(items) == 0 {
		fmt.Println("nothing waiting on you")
		return nil
	}
	as := trustMarks(ctx, st)
	for i, it := range items {
		b := it.Bead
		notes, err := st.Notes(ctx, b.ID)
		if err != nil {
			return err
		}
		desc := fmt.Sprintf("stage %s · group %s · %s · P%d · waiting %s", b.Stage, it.Group, b.Type, b.Priority, age(b.UpdatedAt))
		if b.Hold {
			desc += " · HELD"
		}
		if it.FlowError != "" {
			desc += "\n\nFlow error (moves are refused until it is fixed): " + it.FlowError
		}
		if block := cardBody(b.Body); block != "" {
			desc += "\n\n" + block
		}
		if n := lastNotes(notes, 3, as, a.mailHome()); n != "" {
			desc += "\n\nNotes:\n" + n
		}
		desc += "\n\nDiff:\n" + clip(diffStat(ctx, st, b), 15)

		for {
			pick := 0
			opts := make([]huh.Option[int], len(it.Actions))
			for j, ac := range it.Actions {
				opts[j] = huh.NewOption(ac.Label, j)
			}
			err := ask(
				huh.NewNote().Title(cardTitle(i, len(items), b)).Description(desc),
				huh.NewSelect[int]().Title("Action").Options(opts...).Value(&pick),
			)
			if errors.Is(err, huh.ErrUserAborted) {
				return nil
			}
			if err != nil {
				return err
			}
			ac := it.Actions[pick]
			if ac.Kind == actDiff {
				if err := pageDiff(ctx, st, b); err != nil {
					fmt.Fprintln(os.Stderr, "yardr:", err)
				}
				continue
			}
			if ac.Kind == actAdvance || ac.Kind == actDone {
				if err := a.reviewAdvance(ctx, st, b, ac); err != nil {
					if errors.Is(err, huh.ErrUserAborted) {
						continue
					}
					fmt.Fprintln(os.Stderr, "yardr:", err)
					continue
				}
			}
			break
		}
	}
	return nil
}

// reviewAdvance asks for the move's note and makes the move: reporting the
// outcome for a done action, advancing for the others.
func (a *app) reviewAdvance(ctx context.Context, st store.API, b store.Bead, ac reviewAction) error {
	var note string
	field := huh.NewText().Title("Note for " + b.ID).Value(&note)
	if ac.NoteRequired {
		field = field.Description("What should change? It is added to the bead before it moves to " + ac.Stage + ".").Validate(nonEmpty)
	} else {
		field = field.Description("Optional; added to the bead with the move to " + ac.Stage + ".")
	}
	if err := ask(field); err != nil {
		return err
	}
	// b is the form's snapshot from when it started: if the bead has moved
	// or changed since (an agent finished, someone else decided, a note
	// came in), the store refuses this decision instead of applying it to
	// a bead it was not made for.
	exp := store.Expect{Stage: b.Stage, Revision: b.Revision}
	var moved *store.Bead
	var err error
	if ac.Kind == actDone {
		moved, err = st.Done(ctx, b.ID, ac.Outcome, note, exp, a.who())
	} else {
		moved, err = st.AdvanceNote(ctx, b.ID, ac.Stage, note, exp, a.who())
	}
	if err != nil {
		return err
	}
	if moved.Status == "closed" {
		fmt.Printf("%s → %s (closed)\n", moved.ID, moved.Stage)
	} else {
		fmt.Printf("%s → %s\n", moved.ID, moved.Stage)
	}
	return nil
}

// cardBody is a bead's body as the review card shows it: the writer's
// words as letters only, standing four in, further in than any line the
// yard prints on the card (its notes stand beside two spaces), so none
// reads as a heading, a note or the diff. Clipped to twelve lines, the
// yard's own count standing at the margin. "" for a body with no words.
func cardBody(body string) string {
	words := strings.TrimSpace(store.NoteLetters(body))
	if words == "" {
		return ""
	}
	return clip("    "+strings.ReplaceAll(words, "\n", "\n    "), 12)
}

// cardTitle is the review card's title for the i'th of n beads: the yard's
// count and the bead, and the title as its writer's words, one line, as
// letters only, so it cannot continue the card's title onto lines of its
// own.
func cardTitle(i, n int, b store.Bead) string {
	return fmt.Sprintf("[%d/%d] %s  %s", i+1, n, b.ID, store.OneLine(b.Title))
}

// clip keeps the first n lines of s.
func clip(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n… %d more lines", len(lines)-n)
}
