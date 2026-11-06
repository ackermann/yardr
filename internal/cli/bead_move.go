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
			note, err := moveNote(c, advNote, "yardr bead advance <id> --to <stage> --note -")
			if err != nil {
				return err
			}
			b, err := st.AdvanceNote(c.Context(), args[0], to, note, a.expect(*rev), a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), b, func(w io.Writer) { fmt.Fprintf(w, "%s → %s\n", b.ID, b.Stage) })
		},
	}
	advance.Flags().StringVar(&to, "to", "", "next stage")
	advance.Flags().StringVar(&advNote, "note", "", "attach a note with the move (- reads stdin; use a quoted heredoc so the shell does not expand backticks or $( ) in double-quoted words; one with no words is refused)")
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
refusal shows each unread note and how to ack it. It is refused while the
session's worktree has uncommitted changes; the refusal names each
changed file, and committing them, removing them, or attaching them to a
note and removing them clears it. --outcome done-safe is
refused while the bead's diff touches a path its depot lists in
.yardr/security_paths; the refusal names the files, and done goes to the
review that reads them.

--note <text> attaches a note to the move, and --note - reads it from
stdin: write one that quotes code or file names as a heredoc with a quoted
delimiter, since inside a double-quoted argument the shell runs every
backtick span and $( ) and puts what it prints in the note. A --note given
with no words, empty or only white space, is refused.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			note, err := moveNote(c, doneNote, "yardr bead done <id> --note -")
			if err != nil {
				return err
			}
			b, err := st.Done(c.Context(), args[0], outcome, note, a.expect(*rev), a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), b, func(w io.Writer) { fmt.Fprintf(w, "%s → %s\n", b.ID, b.Stage) })
		},
	}
	done.Flags().StringVar(&outcome, "outcome", store.DefaultOutcome, "how the work ended, one of the stage's outcomes")
	done.Flags().StringVar(&doneNote, "note", "", "attach a note with the move (- reads stdin; use a quoted heredoc so the shell does not expand backticks or $( ) in double-quoted words; one with no words is refused)")
	return done
}

// moveNote is the note a move carries from its --note flag, "" for a move
// with none: "-" reads it from stdin, trailing newlines trimmed. A note
// that was given and has no words is refused instead of dropped, as the
// store refuses one written on the bead: most often a shell emptied it
// (a backtick span or $( ) in double quotes that printed nothing), and the
// bead would move on without the words its writer meant to leave. fix is
// the command written so that the shell expands nothing.
func moveNote(c *cobra.Command, note, fix string) (string, error) {
	if !c.Flags().Changed("note") {
		return "", nil
	}
	if note == "-" {
		b, err := io.ReadAll(c.InOrStdin())
		if err != nil {
			return "", err
		}
		note = strings.TrimRight(string(b), "\n")
	}
	if strings.TrimSpace(note) == "" {
		return "", fmt.Errorf("--note has no words, and a shell may have emptied it, since it runs the backticks and $( ) in a double-quoted argument and puts their output in the text; give the words on stdin from a quoted heredoc (%s <<'NOTE', the words, then NOTE on a line of its own), or leave --note out to move the bead with no note", fix)
	}
	return note, nil
}

func (a *app) newBeadCloseCmd(rev *string) *cobra.Command {
	var reason string
	closeCmd := &cobra.Command{
		Use:   "close <id>",
		Short: "Close a bead",
		Long: `Closes a bead, with --reason <text> as the reason.

--reason - reads the reason from stdin: write one that quotes code or file
names as a heredoc with a quoted delimiter, since inside a double-quoted
argument the shell runs every backtick span and $( ) and puts what it prints
in the reason. A --reason - with no words on stdin is refused, and the bead
stays open.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			// Read first, so that a reason with no words closes nothing.
			if reason == "-" {
				b, err := io.ReadAll(c.InOrStdin())
				if err != nil {
					return err
				}
				if reason = strings.TrimRight(string(b), "\n"); strings.TrimSpace(reason) == "" {
					return fmt.Errorf("nothing to write: stdin gave no reason, so bead %s is still open (leave --reason out to close it with none)", args[0])
				}
			}
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
	closeCmd.Flags().StringVar(&reason, "reason", "", "why it is closed (- reads stdin; use a quoted heredoc so the shell does not expand backticks or $( ) in double-quoted words)")
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
