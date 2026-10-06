package cli

import (
	"context"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

// issueLink is the issue arg names, as a link to it of kind: arg is the
// issue's URL, or the seq of a hook delivery from the issue's thread, as
// yardr reply takes it. Either way the URL is put in the source's fixed
// form (hooks.Issue), so the same issue is the same link however it was
// named. Only a delivery gives the thread's id. No call is made to the
// source.
func issueLink(ctx context.Context, st store.API, kind, arg string) (store.NewLink, error) {
	nl := store.NewLink{Kind: kind}
	if _, err := strconv.ParseUint(arg, 10, 64); err != nil {
		nl.Source, nl.URL, err = hooks.Issue(arg)
		return nl, err
	}
	seq, env, err := hookEnvelope(ctx, st, arg)
	if err != nil {
		return nl, err
	}
	if env.Thread.URL == "" {
		return nl, fmt.Errorf("event %d names no issue: its delivery has no thread URL; give the issue's URL", seq)
	}
	url, err := hooks.IssueIn(env.Source, env.Thread.URL)
	if err != nil {
		return nl, fmt.Errorf("event %d: %w", seq, err)
	}
	nl.Source, nl.URL, nl.Thread = env.Source, url, env.Thread.ID
	return nl, nil
}

// linkLine is a link as link list and bead show print it.
func linkLine(l store.Link) string {
	return fmt.Sprintf("%-7s %s (%s)", l.Kind, l.URL, l.Source)
}

func (a *app) linkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Link beads to the outside issues they belong to",
		Long: `A link records the issue outside the yard (a Linear issue, a GitHub issue)
that a bead belongs to. A bead that tracks an issue is the one whose stage
moves it; a bead that relates to an issue only shows it. A bead tracks at
most one issue per source, and an issue has at most one tracking bead.

<issue> is the issue's URL, or the seq of a hook delivery from its thread
(the number an inbox line starts with). The URL is kept in one form per
issue: a Linear URL loses its title slug, whatever follows the issue is
dropped. Linking calls neither Linear nor GitHub.`,
	}

	var relates bool
	add := &cobra.Command{
		Use:   "add <bead> <issue>",
		Short: "Link a bead to an issue: it tracks the issue, or with --relates only relates to it",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			kind := store.LinkTracks
			if relates {
				kind = store.LinkRelates
			}
			nl, err := issueLink(c.Context(), st, kind, args[1])
			if err != nil {
				return err
			}
			ch, err := st.AddLink(c.Context(), args[0], nl, a.who())
			if err != nil {
				return err
			}
			return a.out(ch, func() {
				fmt.Printf("%s -%s-> %s (%s)", ch.Link.Bead, ch.Link.Kind, ch.Link.URL, ch.Link.Source)
				// The bead had the other kind of link to this issue, and has
				// one link per issue.
				if ch.Replaced != "" {
					fmt.Printf(", replacing its %s link", ch.Replaced)
				}
				fmt.Println()
			})
		},
	}
	add.Flags().BoolVar(&relates, "relates", false, "relate to the issue instead of tracking it: shown, never moves it")

	rm := &cobra.Command{
		Use:   "rm <bead> <issue>",
		Short: "Remove a bead's link to an issue",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			nl, err := issueLink(c.Context(), st, "", args[1])
			if err != nil {
				return err
			}
			if err := st.RemoveLink(c.Context(), args[0], nl.URL, a.who()); err != nil {
				return err
			}
			return a.out(map[string]string{"removed": "ok", "url": nl.URL}, func() {})
		},
	}

	var ofIssue string
	list := &cobra.Command{
		Use:   "list <bead> | --issue <issue>",
		Short: "A bead's links, or with --issue the beads linked to an issue",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if (len(args) == 1) == (ofIssue != "") {
				return fmt.Errorf("give a bead, or --issue <issue>")
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if ofIssue == "" {
				ls, err := st.Links(c.Context(), args[0])
				if err != nil {
					return err
				}
				return a.out(ls, func() {
					for _, l := range ls {
						fmt.Println(linkLine(l))
					}
				})
			}
			nl, err := issueLink(c.Context(), st, "", ofIssue)
			if err != nil {
				return err
			}
			ls, err := st.IssueLinks(c.Context(), nl.URL)
			if err != nil {
				return err
			}
			return a.out(ls, func() {
				for _, l := range ls {
					fmt.Printf("%s  %s\n", l.Bead, linkLine(l))
				}
			})
		},
	}
	list.Flags().StringVar(&ofIssue, "issue", "", "list the beads linked to this issue, its tracking bead first")

	var to string
	move := &cobra.Command{
		Use:   "move <issue> --to <bead>",
		Short: "Make another bead the issue's tracker; the earlier one keeps a relates link",
		Long: `Moves the tracks link of an issue to <bead> in one write, so the issue is
never without a tracking bead nor with two: for work that grew into a train,
which then tracks the issue in place of the bead that started it. The bead
that tracked it keeps a relates link.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if to == "" {
				return fmt.Errorf("--to is required")
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			nl, err := issueLink(c.Context(), st, store.LinkTracks, args[0])
			if err != nil {
				return err
			}
			ch, err := st.MoveLink(c.Context(), nl.URL, to, a.who())
			if err != nil {
				return err
			}
			return a.out(ch, func() {
				fmt.Printf("%s -%s-> %s (%s), moved from %s, which keeps a %s link\n",
					ch.Link.Bead, ch.Link.Kind, ch.Link.URL, ch.Link.Source, ch.From, store.LinkRelates)
			})
		},
	}
	move.Flags().StringVar(&to, "to", "", "the bead that tracks the issue from now on")

	cmd.AddCommand(add, rm, list, move)
	return cmd
}
