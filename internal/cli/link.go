package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"io"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

// hookedSources reports whether the yard has a hook of each source, by
// name: what links default to when two sources share a host.
func hookedSources(ctx context.Context, st store.API) map[string]bool {
	hs, err := st.Hooks(ctx)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, h := range hs {
		out[h.Source] = true
	}
	return out
}

// issueLink is the issue arg names, as a link to it of kind: arg is the
// issue's URL, or the seq of a hook delivery from the issue's thread, as
// yardr reply takes it. Either way the URL is put in the source's fixed
// form, so the same issue is the same link however it was named. Only a
// delivery gives the thread's id. No call is made to the source.
//
// source names the link's source when two claim the URL (a builtin and a
// declared source on one host): the URL is fixed as an issue of that
// source. Without it the yard's hooks decide: a declared source that has a
// hook in the yard before a builtin one that has none, else a builtin
// before a declared one. A delivery names its own source, so source takes a
// URL only.
func issueLink(ctx context.Context, st store.API, kind, arg, source string) (store.NewLink, error) {
	nl := store.NewLink{Kind: kind}
	// A bead of another yard is no issue: its row is the yard's record.
	if store.PeerIssue(arg) {
		return nl, store.PeerRowRefusal(arg)
	}
	decls := allDeclarations(ctx, st)
	if _, err := strconv.ParseUint(arg, 10, 64); err != nil {
		if source != "" {
			url, err := hooks.IssueIn(source, arg, decls...)
			nl.Source, nl.URL = source, url
			return nl, err
		}
		nl.Source, nl.URL, err = hooks.IssueWithHooks(arg, decls, hookedSources(ctx, st))
		return nl, err
	}
	if source != "" {
		return nl, fmt.Errorf("--source takes a URL, not a delivery seq: event %s names its own source", arg)
	}
	seq, env, err := hookEnvelope(ctx, st, arg)
	if err != nil {
		return nl, err
	}
	if env.Source == store.SourcePeer {
		return nl, store.PeerRowRefusal(env.Thread.URL)
	}
	if env.Thread.URL == "" {
		return nl, fmt.Errorf("event %d names no issue: its delivery has no thread URL; give the issue's URL", seq)
	}
	url, err := hooks.IssueIn(env.Source, env.Thread.URL, decls...)
	if err != nil {
		return nl, fmt.Errorf("event %d: %w", seq, err)
	}
	nl.Source, nl.URL, nl.Thread = env.Source, url, env.Thread.ID
	return nl, nil
}

// linkLine is a link as link list and bead show print it.
func linkLine(l store.Link) string {
	if l.Source == store.SourcePeer {
		return l.PeerRow()
	}
	return fmt.Sprintf("%-7s %s (%s)", l.Kind, printable(l.URL), l.Source)
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
dropped. Linking calls neither Linear nor GitHub.

The link's source is told by the URL's host. When a builtin and a declared
source share a host, link add takes --source to say which the link is of;
without it a declared source that has a hook in the yard wins over a
builtin one that has none, else the builtin wins.

A bead that is at a linked yard, was at one or came from one has a row for
its counterpart there, which link list and bead show print first: at, was at
or from, then <the other yard's bead>@<peer> (peer). It is read from this
yard's record of the pair each time and is no link anyone made: link add,
link rm and link move refuse it. link list --issue <bead>@<peer> finds this
yard's bead.`,
	}

	var relates bool
	var addSource string
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
			nl, err := issueLink(c.Context(), st, kind, args[1], addSource)
			if err != nil {
				return err
			}
			ch, err := st.AddLink(c.Context(), args[0], nl, a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), ch, func(w io.Writer) {
				fmt.Fprintf(w, "%s -%s-> %s (%s)", ch.Link.Bead, ch.Link.Kind, printable(ch.Link.URL), ch.Link.Source)
				// The bead had the other kind of link to this issue, and has
				// one link per issue.
				if ch.Replaced != "" {
					fmt.Fprintf(w, ", replacing its %s link", ch.Replaced)
				}
				fmt.Fprintln(w)
			})
		},
	}
	add.Flags().BoolVar(&relates, "relates", false, "relate to the issue instead of tracking it: shown, never moves it")
	add.Flags().StringVar(&addSource, "source", "", "the link's source, when a builtin and a declared source share the URL's host")

	rm := &cobra.Command{
		Use:   "rm <bead> <issue>",
		Short: "Remove a bead's link to an issue",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			nl, err := issueLink(c.Context(), st, "", args[1], "")
			if err != nil {
				return err
			}
			if err := st.RemoveLink(c.Context(), args[0], nl.URL, a.who()); err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), map[string]string{"removed": "ok", "url": nl.URL}, func(w io.Writer) {})
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
				// Before them, the bead's counterpart in another yard.
				b, err := st.GetBead(c.Context(), args[0])
				if err != nil {
					return err
				}
				if rows := b.PeerLinks(); len(rows) > 0 {
					ls = append(rows, ls...)
				}
				return a.out(c.OutOrStdout(), ls, func(w io.Writer) {
					for _, l := range ls {
						fmt.Fprintln(w, linkLine(l))
					}
				})
			}
			// A bead of another yard is looked up as it is named.
			nl := store.NewLink{URL: ofIssue}
			if !store.PeerIssue(ofIssue) {
				if nl, err = issueLink(c.Context(), st, "", ofIssue, ""); err != nil {
					return err
				}
			}
			ls, err := st.IssueLinks(c.Context(), nl.URL)
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), ls, func(w io.Writer) {
				for _, l := range ls {
					fmt.Fprintf(w, "%s  %s\n", l.Bead, linkLine(l))
				}
			})
		},
	}
	list.Flags().StringVar(&ofIssue, "issue", "", "list the beads linked to this issue, its tracking bead first")

	var to, fromSource, toSource string
	move := &cobra.Command{
		Use:   "move <issue> --to <bead> | --from-source <source> --to-source <source>",
		Short: "Make another bead the issue's tracker; the earlier one keeps a relates link",
		Long: `Moves the tracks link of an issue to <bead> in one write, so the issue is
never without a tracking bead nor with two: for work that grew into a train,
which then tracks the issue in place of the bead that started it. The bead
that tracked it keeps a relates link.

With --from-source and --to-source and no issue, it moves the yard's links
from one source to the other in one write instead: for a yard whose source
moved, a builtin one to a declared one on the same host. The two must share
the host, and a bead that would then track two issues of the new source
refuses the whole move. Undo it by moving back.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if fromSource != "" || toSource != "" {
				if len(args) != 0 || to != "" {
					return fmt.Errorf("--from-source and --to-source take no <issue> and no --to: they move the yard's links from one source to the other")
				}
				if fromSource == "" || toSource == "" {
					return fmt.Errorf("give both --from-source and --to-source")
				}
				if err := checkLinkSources(c.Context(), st, fromSource, toSource); err != nil {
					return err
				}
				n, err := st.MoveLinkSource(c.Context(), fromSource, toSource, a.who())
				if err != nil {
					return err
				}
				return a.out(c.OutOrStdout(), map[string]any{"from_source": fromSource, "to_source": toSource, "moved": n}, func(w io.Writer) {
					fmt.Fprintf(w, "moved %d links from %s to %s\n", n, fromSource, toSource)
				})
			}
			if len(args) != 1 {
				return fmt.Errorf("give an <issue> --to <bead>, or --from-source <source> --to-source <source>")
			}
			if to == "" {
				return fmt.Errorf("--to is required")
			}
			nl, err := issueLink(c.Context(), st, store.LinkTracks, args[0], "")
			if err != nil {
				return err
			}
			ch, err := st.MoveLink(c.Context(), nl.URL, to, a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), ch, func(w io.Writer) {
				fmt.Fprintf(w, "%s -%s-> %s (%s), moved from %s, which keeps a %s link\n",
					ch.Link.Bead, ch.Link.Kind, ch.Link.URL, ch.Link.Source, ch.From, store.LinkRelates)
			})
		},
	}
	move.Flags().StringVar(&to, "to", "", "the bead that tracks the issue from now on")
	move.Flags().StringVar(&fromSource, "from-source", "", "move the yard's links of this source")
	move.Flags().StringVar(&toSource, "to-source", "", "to this source (on the same host)")

	cmd.AddCommand(add, rm, list, move)
	return cmd
}

// checkLinkSources refuses a move of links between sources that are no
// link sources, or not on one host: the URLs stay as they are, so the new
// source must read them as the old one did.
func checkLinkSources(ctx context.Context, st store.API, from, to string) error {
	decls := allDeclarations(ctx, st)
	var fromHost, toHost string
	for _, name := range []string{from, to} {
		var host string
		if b, ok := hooks.Adapters[name].(hooks.Linker); ok {
			host = b.IssueHost()
		} else {
			found := false
			for _, d := range decls {
				if d != nil && d.Name == name && d.Link != nil {
					host, found = d.Link.Host, true
					break
				}
			}
			if !found {
				return fmt.Errorf("%s is no link source (links go to %s)%s", name, hooks.NamesOrNone(hooks.LinkSources(decls...), ", "), packHint(ctx, st, name))
			}
		}
		if name == from {
			fromHost = host
		} else {
			toHost = host
		}
	}
	if !strings.EqualFold(fromHost, toHost) {
		return fmt.Errorf("%s is on %s and %s on %s: a move keeps the URLs, so both sources must share the host", from, fromHost, to, toHost)
	}
	return nil
}
