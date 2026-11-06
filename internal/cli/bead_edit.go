package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"io"
	"strings"
	"yardr.dev/yardr/internal/store"
)

func (a *app) newBeadCreateCmd() *cobra.Command {
	var nb store.NewBead
	var createMeta, tracks, relates []string
	create := &cobra.Command{
		Use:   "create <title|->",
		Short: "Create a bead",
		Long: `Creates a bead.

<title> may be - to read it from stdin, and -b - reads the body from stdin:
write a title or a body that quotes code or file names that way, since
inside a double-quoted argument the shell runs every backtick span and $( )
and puts what it prints in the text, where a heredoc with a quoted delimiter
expands nothing. Stdin is read once, so with both the first line is the title
and the lines after it the body:

  yardr bead create - -b - <<'BEAD'
  the title, as it is

  the body, as it is
  BEAD

A title given as - alone is one line. One with no words on stdin is refused.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if nb.Title, nb.Body, err = stdinTitleBody(c, args[0], nb.Body); err != nil {
				return err
			}
			if args[0] == "-" && strings.TrimSpace(nb.Title) == "" {
				return fmt.Errorf("nothing to create: stdin gave no title (with -b - as well, the title is its first line)")
			}
			if len(createMeta) > 0 {
				if nb.Metadata, err = store.ParseSets(createMeta); err != nil {
					return err
				}
			}
			// The links are made in the write that creates the bead, so a
			// link that is refused leaves no bead behind.
			nb.Links = nil
			for _, of := range []struct {
				kind   string
				issues []string
			}{{store.LinkTracks, tracks}, {store.LinkRelates, relates}} {
				for _, issue := range of.issues {
					nl, err := issueLink(c.Context(), st, of.kind, issue, "")
					if err != nil {
						return err
					}
					nb.Links = append(nb.Links, nl)
				}
			}
			// The store makes the edge in the write that creates the bead, and
			// refuses an unknown origin there. A server from before the flag
			// would drop the field and create the bead without its edge, so
			// ask it for the origin first: such a server does not know the
			// question, and nothing is created.
			if nb.DiscoveredFrom != "" {
				if _, err := st.Discovery(c.Context(), nb.DiscoveredFrom); err != nil {
					return fmt.Errorf("discovered from: %w", err)
				}
			}
			// The blocking edges are made in that write too, and the same
			// goes for them: an older server knows neither the field nor
			// this question, and a bead created without its blockers would
			// be ready for a route to hand out.
			if len(nb.BlockedBy) > 0 {
				if err := st.CheckBlockers(c.Context(), nb.BlockedBy); err != nil {
					return err
				}
			}
			// --for is the bead's originator, whose subscription builds
			// it: an older server would drop the field and bill the
			// operator's instead, so ask first, and create nothing when
			// it does not know the question.
			if nb.For != "" {
				if err := st.CheckFor(c.Context(), nb.For); err != nil {
					return err
				}
			}
			b, err := st.CreateBead(c.Context(), nb, a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), b, func(w io.Writer) { fmt.Fprintln(w, b.ID) })
		},
	}
	f := create.Flags()
	f.StringVarP(&nb.Body, "body", "b", "", "description (- reads stdin; with the title - too, the lines after the first)")
	f.StringVarP(&nb.Type, "type", "t", "", "bead type (default task)")
	f.StringVarP(&nb.Stage, "stage", "s", "", "initial stage (default: the flow's first stage, backlog in the starter flow; new without a flow)")
	f.IntVarP(&nb.Priority, "priority", "p", defaultPriority, "priority, 0 is highest")
	f.StringVarP(&nb.Depot, "depot", "d", "", "depot the work happens in")
	f.StringSliceVarP(&nb.Labels, "label", "l", nil, "labels")
	f.StringVar(&nb.Prefix, "prefix", "", "id prefix (default depot name, else yd)")
	f.StringVar(&nb.ID, "id", "", "explicit handle instead of a minted one (imports; must be unused)")
	f.StringArrayVar(&createMeta, "meta", nil, "metadata key=value (repeatable), e.g. model=opus")
	f.StringSliceVar(&nb.BlockedBy, "blocked-by", nil, "beads that must close first; made with the bead, so an unknown one creates nothing")
	f.StringVar(&nb.DiscoveredFrom, "discovered-from", "", "the bead this one was found while working on (open or closed, any depot); recorded as an edge, never blocks")
	f.StringVar(&nb.For, "for", "", "the user the bead is for, whose subscription builds it (must exist; refused from a bead agent's pane)")
	f.StringArrayVar(&tracks, "tracks", nil, "outside issue the bead tracks: its URL, or the seq of a hook delivery from it (one per source; see yardr link)")
	f.StringArrayVar(&relates, "relates", nil, "outside issue the bead relates to, never moves (repeatable)")
	return create
}

func (a *app) newBeadUpdateCmd(rev *string) *cobra.Command {
	var up struct {
		title, body, typ, depot, originator string
		pri                                 int
		add, rm, meta                       []string
	}
	update := &cobra.Command{
		Use:   "update <id>",
		Short: "Change a bead's fields (use advance to change stage)",
		Long: `Changes a bead's fields (use advance to change stage).

When the body or the title of a bead with a live session changes, and the
writer is not that session itself, the session is told of it with a note,
as one written with yardr bead note is: what changed (body, title, or
both), never the new text. Anything else a patch changes (labels,
priority, metadata, type, depot, originator) tells nothing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			var p store.BeadPatch
			fl := c.Flags()
			// From the command's stdin, as reply reads its own, so a test
			// feeds it with SetIn. An empty read is refused: it is a broken
			// pipe, not an intent, and writing it would clear the body (or
			// the title, which no bead may be without). Clearing the body on
			// purpose stays -b "".
			title, body, err := stdinTitleBody(c, up.title, up.body)
			if err != nil {
				return err
			}
			if fl.Changed("title") {
				if up.title == "-" && strings.TrimSpace(title) == "" {
					return fmt.Errorf("nothing to write: stdin gave no title, so bead %s's title is unchanged (with -b - as well, the title is its first line)", args[0])
				}
				p.Title = &title
			}
			if fl.Changed("body") {
				if up.body == "-" && strings.TrimSpace(body) == "" {
					return fmt.Errorf("nothing to write: stdin gave no body, so bead %s's body is unchanged (clear it on purpose with -b \"\")", args[0])
				}
				p.Body = &body
			}
			if fl.Changed("type") {
				p.Type = &up.typ
			}
			if fl.Changed("depot") {
				p.Depot = &up.depot
			}
			if fl.Changed("priority") {
				p.Priority = &up.pri
			}
			if fl.Changed("originator") {
				p.Originator = &up.originator
			}
			p.AddLabels, p.RemoveLabel = up.add, up.rm
			if len(up.meta) > 0 {
				if p.Metadata, err = store.ParseSets(up.meta); err != nil {
					return err
				}
			}
			b, err := st.UpdateBead(c.Context(), args[0], p, a.expect(*rev), a.who())
			if err != nil {
				return err
			}
			if err := a.printBead(c.OutOrStdout(), b); err != nil {
				return err
			}
			if !a.json && b.Notify != "" {
				fmt.Fprintf(c.OutOrStdout(), "(session %s will be told)\n", b.Notify)
			}
			return nil
		},
	}
	uf := update.Flags()
	uf.StringVar(&up.title, "title", "", "new title (- reads stdin; use a quoted heredoc so the shell does not expand backticks or $( ) in double-quoted words)")
	uf.StringVarP(&up.body, "body", "b", "", "new description (- reads stdin; with --title - too, the lines after the first)")
	uf.StringVarP(&up.typ, "type", "t", "", "new type")
	uf.StringVarP(&up.depot, "depot", "d", "", "new depot")
	uf.IntVarP(&up.pri, "priority", "p", 0, "new priority")
	uf.StringSliceVar(&up.add, "add-label", nil, "labels to add")
	uf.StringSliceVar(&up.rm, "remove-label", nil, "labels to remove")
	uf.StringArrayVar(&up.meta, "meta", nil, "metadata key=value to merge")
	uf.StringVar(&up.originator, "originator", "", "new originator, the user the bead is for (the operator's to change)")
	return update
}

// stdinTitleBody is a bead's title and body where either is "-", which reads
// the command's stdin: trailing newlines trimmed, as note and reply read
// theirs. Stdin can be read once, so when both are "-" its first line is the
// title and the lines after it the body, as a commit message divides; a
// title alone is all of stdin, and one line, since a second would be the
// body's. A "-" that stdin gave no words for comes back empty, for the
// command to refuse in its own words: create makes nothing, update changes
// nothing.
func stdinTitleBody(c *cobra.Command, title, body string) (string, string, error) {
	if title != "-" && body != "-" {
		return title, body, nil
	}
	b, err := io.ReadAll(c.InOrStdin())
	if err != nil {
		return "", "", err
	}
	text := strings.TrimRight(string(b), "\n")
	switch {
	case title != "-":
		return title, text, nil
	case body == "-":
		title, body, _ = strings.Cut(text, "\n")
		return title, strings.TrimLeft(body, "\n"), nil
	}
	title = text
	if strings.Contains(title, "\n") {
		return "", "", fmt.Errorf("a title is one line, and stdin gave %d: put the lines after the first in the body (-b -)", strings.Count(title, "\n")+1)
	}
	return title, body, nil
}
