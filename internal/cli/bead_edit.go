package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
)

func (a *app) newBeadCreateCmd() *cobra.Command {
	var nb store.NewBead
	var createMeta, tracks, relates []string
	create := &cobra.Command{
		Use:   "create <title>",
		Short: "Create a bead",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			nb.Title = args[0]
			if len(createMeta) > 0 {
				if nb.Metadata, err = parseSets(createMeta); err != nil {
					return err
				}
			}
			if nb.Body == "-" {
				b, err := readAllStdin()
				if err != nil {
					return err
				}
				nb.Body = b
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
			b, err := st.CreateBead(c.Context(), nb, a.who())
			if err != nil {
				return err
			}
			return a.out(b, func() { fmt.Println(b.ID) })
		},
	}
	f := create.Flags()
	f.StringVarP(&nb.Body, "body", "b", "", "description (- reads stdin)")
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
	f.StringArrayVar(&tracks, "tracks", nil, "outside issue the bead tracks: its URL, or the seq of a hook delivery from it (one per source; see yardr link)")
	f.StringArrayVar(&relates, "relates", nil, "outside issue the bead relates to, never moves (repeatable)")
	return create
}

func (a *app) newBeadUpdateCmd(rev *string) *cobra.Command {
	var up struct {
		title, body, typ, depot string
		pri                     int
		add, rm, meta           []string
	}
	update := &cobra.Command{
		Use:   "update <id>",
		Short: "Change a bead's fields (use advance to change stage)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			var p store.BeadPatch
			fl := c.Flags()
			if fl.Changed("title") {
				p.Title = &up.title
			}
			if fl.Changed("body") {
				if up.body == "-" {
					if up.body, err = readAllStdin(); err != nil {
						return err
					}
				}
				p.Body = &up.body
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
			p.AddLabels, p.RemoveLabel = up.add, up.rm
			if len(up.meta) > 0 {
				if p.Metadata, err = parseSets(up.meta); err != nil {
					return err
				}
			}
			b, err := st.UpdateBead(c.Context(), args[0], p, store.Expect{Revision: *rev}, a.who())
			if err != nil {
				return err
			}
			return a.printBead(b)
		},
	}
	uf := update.Flags()
	uf.StringVar(&up.title, "title", "", "new title")
	uf.StringVarP(&up.body, "body", "b", "", "new description (- reads stdin)")
	uf.StringVarP(&up.typ, "type", "t", "", "new type")
	uf.StringVarP(&up.depot, "depot", "d", "", "new depot")
	uf.IntVarP(&up.pri, "priority", "p", 0, "new priority")
	uf.StringSliceVar(&up.add, "add-label", nil, "labels to add")
	uf.StringSliceVar(&up.rm, "remove-label", nil, "labels to remove")
	uf.StringArrayVar(&up.meta, "meta", nil, "metadata key=value to merge")
	return update
}
