package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"io"
	"yardr.dev/yardr/internal/store"
)

func (a *app) newBeadAdvanceCmd(rev *string) *cobra.Command {
	var to, advNote string
	advance := &cobra.Command{
		Use:   "advance <id> --to <stage>",
		Short: "Finish the current stage and hand the bead to the next",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if to == "" {
				return fmt.Errorf("--to is required")
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if advNote == "-" {
				if advNote, err = readAllStdin(); err != nil {
					return err
				}
			}
			b, err := st.AdvanceNote(c.Context(), args[0], to, advNote, a.expect(*rev), a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), b, func(w io.Writer) { fmt.Fprintf(w, "%s → %s\n", b.ID, b.Stage) })
		},
	}
	advance.Flags().StringVar(&to, "to", "", "next stage")
	advance.Flags().StringVar(&advNote, "note", "", "attach a note with the move, e.g. the changes asked for (- reads stdin)")
	return advance
}

func (a *app) newBeadDoneCmd(rev *string) *cobra.Command {
	var outcome, doneNote string
	done := &cobra.Command{
		Use:   "done <id> [--outcome <outcome>]",
		Short: "Report how the work in the bead's stage ended; its flow picks the next stage",
		Long: `Reports an outcome for the bead's current stage (done unless --outcome
says otherwise) and moves the bead to the stage its flow names for it, as
advance would. A stage with a single way out offers done for it.
yardr prime --bead lists the stage's outcomes. The session's own done is
refused while a note on the bead names that session and is not acked; the
refusal shows each unread note and how to ack it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if doneNote == "-" {
				if doneNote, err = readAllStdin(); err != nil {
					return err
				}
			}
			b, err := st.Done(c.Context(), args[0], outcome, doneNote, a.expect(*rev), a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), b, func(w io.Writer) { fmt.Fprintf(w, "%s → %s\n", b.ID, b.Stage) })
		},
	}
	done.Flags().StringVar(&outcome, "outcome", store.DefaultOutcome, "how the work ended, one of the stage's outcomes")
	done.Flags().StringVar(&doneNote, "note", "", "attach a note with the move (- reads stdin)")
	return done
}

func (a *app) newBeadCloseCmd(rev *string) *cobra.Command {
	var reason string
	closeCmd := &cobra.Command{
		Use:   "close <id>",
		Short: "Close a bead",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			// Who this close could release: the beads this bead blocks.
			// Read before the write, with each target's holding blockers,
			// so the line names only the gates this close opens: a target
			// another blocker still holds stays held, and one this bead
			// never held was already open.
			blocker, err := st.GetBead(c.Context(), args[0])
			if err != nil {
				return err
			}
			deps, err := st.Deps(c.Context(), blocker.ID)
			if err != nil {
				return err
			}
			var targets []string
			seen := map[string]bool{}
			for _, d := range deps {
				if d.Kind != store.DepBlocks || d.From != blocker.ID || seen[d.To] {
					continue
				}
				seen[d.To] = true
				targets = append(targets, d.To)
			}
			before := make(map[string][]string, len(targets))
			for _, t := range targets {
				bs, err := st.OpenBlockers(c.Context(), t)
				if err != nil {
					return err
				}
				before[t] = bs
			}
			b, err := st.CloseBead(c.Context(), args[0], reason, a.expect(*rev), a.who())
			if err != nil {
				return err
			}
			// The close went through; what follows only decides the line.
			// A target read half-way through someone else's write is left
			// out rather than fail a close that happened.
			var releases []string
			for _, t := range targets {
				if !slices.Contains(before[t], blocker.ID) {
					continue
				}
				tb, err := st.GetBead(c.Context(), t)
				if err != nil {
					return fmt.Errorf("%s closed, but could not determine released targets: %w", b.ID, err)
				}
				if tb.Status == "closed" {
					continue
				}
				after, err := st.OpenBlockers(c.Context(), t)
				if err != nil {
					return fmt.Errorf("%s closed, but could not determine released targets: %w", b.ID, err)
				}
				if len(after) != 0 {
					continue
				}
				releases = append(releases, t)
			}
			slices.Sort(releases)
			return a.out(c.OutOrStdout(), struct {
				*store.Bead
				Releases []string `json:"releases,omitempty"`
			}{b, releases}, func(w io.Writer) {
				fmt.Fprintf(w, "%s closed\n", b.ID)
				if len(releases) > 0 {
					fmt.Fprintf(w, "releases %s\n", strings.Join(releases, ", "))
				}
			})
		},
	}
	closeCmd.Flags().StringVar(&reason, "reason", "", "why it is closed")
	return closeCmd
}

// beadSimpleCmd is a stage move that takes only a bead: it runs fn and
// prints "<id> <use>".
func (a *app) beadSimpleCmd(use, short string, fn func(c *cobra.Command, st store.API, id string) (*store.Bead, error)) *cobra.Command {
	return &cobra.Command{
		Use: use + " <id>", Short: short, Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			b, err := fn(c, st, args[0])
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), b, func(w io.Writer) { fmt.Fprintf(w, "%s %s\n", b.ID, use) })
		},
	}
}

func (a *app) newBeadReopenCmd(rev *string) *cobra.Command {
	reopen := a.beadSimpleCmd("reopen", "Reopen a closed bead", func(c *cobra.Command, st store.API, id string) (*store.Bead, error) {
		return st.Reopen(c.Context(), id, a.expect(*rev), a.who())
	})
	return reopen
}

func (a *app) newBeadHoldCmd(rev *string) *cobra.Command {
	hold := a.beadSimpleCmd("hold", "Stop routing a bead and end its session, freeing the slot", func(c *cobra.Command, st store.API, id string) (*store.Bead, error) {
		return st.SetHold(c.Context(), id, true, a.expect(*rev), a.who())
	})
	return hold
}

func (a *app) newBeadUnholdCmd(rev *string) *cobra.Command {
	unhold := a.beadSimpleCmd("unhold", "Resume routing a held bead", func(c *cobra.Command, st store.API, id string) (*store.Bead, error) {
		return st.SetHold(c.Context(), id, false, a.expect(*rev), a.who())
	})
	return unhold
}

func (a *app) newBeadReleaseCmd(rev *string) *cobra.Command {
	var relReason string
	release := a.beadSimpleCmd("release", "Take a bead from its group so it is routed again", func(c *cobra.Command, st store.API, id string) (*store.Bead, error) {
		return st.Release(c.Context(), id, relReason, a.expect(*rev), a.who())
	})
	release.Flags().StringVar(&relReason, "reason", "", "why")
	return release
}
