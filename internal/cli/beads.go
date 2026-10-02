package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mckean/yard/internal/store"
)

func beadRows(bs []store.Bead) [][]string {
	rows := make([][]string, len(bs))
	for i, b := range bs {
		flags := b.Status
		if b.Hold {
			flags += ",hold"
		}
		rows[i] = []string{b.ID, "P" + strconv.Itoa(b.Priority), b.Type, b.Stage, flags,
			deref(b.Group), deref(b.Rig), b.Title}
	}
	return rows
}

const beadHeader = "ID\tPRI\tTYPE\tSTAGE\tSTATUS\tGROUP\tRIG\tTITLE"

func (a *app) printBead(b *store.Bead) error {
	return a.out(b, func() {
		fmt.Printf("%s  %s\n", b.ID, b.Title)
		fmt.Printf("  type %s · stage %s · %s · P%d", b.Type, b.Stage, b.Status, b.Priority)
		if b.Hold {
			fmt.Print(" · HELD")
		}
		fmt.Println()
		if b.Rig != nil {
			fmt.Printf("  rig %s\n", *b.Rig)
		}
		if b.Group != nil {
			fmt.Printf("  owned by %s (session %s)\n", *b.Group, deref(b.Session))
		}
		if len(b.Labels) > 0 {
			fmt.Printf("  labels %s\n", strings.Join(b.Labels, ", "))
		}
		if b.CloseReason != nil {
			fmt.Printf("  closed: %s\n", *b.CloseReason)
		}
		if b.Body != "" {
			fmt.Printf("\n%s\n", b.Body)
		}
	})
}

func (a *app) beadCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "bead", Short: "Create, inspect and move beads"}

	var nb store.NewBead
	var blockedBy, createMeta []string
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
			b, err := st.CreateBead(c.Context(), nb, a.who())
			if err != nil {
				return err
			}
			for _, blocker := range blockedBy {
				if err := st.AddDep(c.Context(), blocker, b.ID, "blocks", a.who()); err != nil {
					return fmt.Errorf("created %s but blocking on %s failed: %w", b.ID, blocker, err)
				}
			}
			return a.out(b, func() { fmt.Println(b.ID) })
		},
	}
	f := create.Flags()
	f.StringVarP(&nb.Body, "body", "b", "", "description (- reads stdin)")
	f.StringVarP(&nb.Type, "type", "t", "", "bead type (default task)")
	f.StringVarP(&nb.Stage, "stage", "s", "", "initial stage (default new)")
	f.IntVarP(&nb.Priority, "priority", "p", 2, "priority, 0 is highest")
	f.StringVarP(&nb.Rig, "rig", "r", "", "rig the work happens in")
	f.StringSliceVarP(&nb.Labels, "label", "l", nil, "labels")
	f.StringVar(&nb.Prefix, "prefix", "", "id prefix (default rig name, else yd)")
	f.StringVar(&nb.ID, "id", "", "explicit handle instead of a minted one (imports; must be unused)")
	f.StringArrayVar(&createMeta, "meta", nil, "metadata key=value (repeatable), e.g. model=opus")
	f.StringSliceVar(&blockedBy, "blocked-by", nil, "beads that must close first")

	show := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a bead",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			b, err := st.GetBead(c.Context(), args[0])
			if err != nil {
				return err
			}
			return a.printBead(b)
		},
	}

	var lf store.BeadFilter
	list := &cobra.Command{
		Use:   "list",
		Short: "List open beads",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			bs, err := st.ListBeads(c.Context(), lf)
			if err != nil {
				return err
			}
			return a.out(bs, func() { table(beadHeader, beadRows(bs)) })
		},
	}
	lf2 := list.Flags()
	lf2.BoolVarP(&lf.All, "all", "a", false, "include closed beads")
	lf2.StringVarP(&lf.Stage, "stage", "s", "", "filter by stage")
	lf2.StringVarP(&lf.Type, "type", "t", "", "filter by type")
	lf2.StringVarP(&lf.Rig, "rig", "r", "", "filter by rig")
	lf2.StringVarP(&lf.Label, "label", "l", "", "filter by label")
	lf2.StringVarP(&lf.Group, "group", "g", "", "filter by owning group")

	var up struct {
		title, body, typ, rig string
		pri                   int
		add, rm, meta         []string
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
			if fl.Changed("rig") {
				p.Rig = &up.rig
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
			b, err := st.UpdateBead(c.Context(), args[0], p, a.who())
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
	uf.StringVarP(&up.rig, "rig", "r", "", "new rig")
	uf.IntVarP(&up.pri, "priority", "p", 0, "new priority")
	uf.StringSliceVar(&up.add, "add-label", nil, "labels to add")
	uf.StringSliceVar(&up.rm, "remove-label", nil, "labels to remove")
	uf.StringArrayVar(&up.meta, "meta", nil, "metadata key=value to merge")

	note := &cobra.Command{
		Use:   "note <id> <text>",
		Short: "Attach a note (evidence) to a bead",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			text := args[1]
			if text == "-" {
				if text, err = readAllStdin(); err != nil {
					return err
				}
			}
			b, err := st.GetBead(c.Context(), args[0])
			if err != nil {
				return err
			}
			if err := st.AddNote(c.Context(), b.ID, text, a.who()); err != nil {
				return err
			}
			return a.out(map[string]string{"bead": b.ID, "noted": "ok"}, func() {})
		},
	}

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
			b, err := st.AdvanceNote(c.Context(), args[0], to, advNote, a.who())
			if err != nil {
				return err
			}
			return a.out(b, func() { fmt.Printf("%s → %s\n", b.ID, b.Stage) })
		},
	}
	advance.Flags().StringVar(&to, "to", "", "next stage")
	advance.Flags().StringVar(&advNote, "note", "", "attach a note with the move, e.g. the changes asked for (- reads stdin)")

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
			b, err := st.CloseBead(c.Context(), args[0], reason, a.who())
			if err != nil {
				return err
			}
			return a.out(b, func() { fmt.Printf("%s closed\n", b.ID) })
		},
	}
	closeCmd.Flags().StringVar(&reason, "reason", "", "why it is closed")

	simple := func(use, short string, fn func(c *cobra.Command, st store.API, id string) (*store.Bead, error)) *cobra.Command {
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
				return a.out(b, func() { fmt.Printf("%s %s\n", b.ID, use) })
			},
		}
	}
	reopen := simple("reopen", "Reopen a closed bead", func(c *cobra.Command, st store.API, id string) (*store.Bead, error) {
		return st.Reopen(c.Context(), id, a.who())
	})
	hold := simple("hold", "Stop routing a bead (a running session continues)", func(c *cobra.Command, st store.API, id string) (*store.Bead, error) {
		return st.SetHold(c.Context(), id, true, a.who())
	})
	unhold := simple("unhold", "Resume routing a held bead", func(c *cobra.Command, st store.API, id string) (*store.Bead, error) {
		return st.SetHold(c.Context(), id, false, a.who())
	})
	var relReason string
	release := simple("release", "Take a bead from its group so it is routed again", func(c *cobra.Command, st store.API, id string) (*store.Bead, error) {
		return st.Release(c.Context(), id, relReason, a.who())
	})
	release.Flags().StringVar(&relReason, "reason", "", "why")

	cmd.AddCommand(create, show, list, update, note, advance, closeCmd, reopen, hold, unhold, release)
	return cmd
}

func (a *app) readyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ready",
		Short: "List open, unheld beads whose blockers are closed",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			bs, err := st.Ready(c.Context())
			if err != nil {
				return err
			}
			return a.out(bs, func() { table(beadHeader, beadRows(bs)) })
		},
	}
}

func (a *app) depCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "dep", Short: "Edges between beads"}
	var kind string
	add := &cobra.Command{
		Use:   "add <from> <to>",
		Short: "Relate two beads; with kind blocks (default), <from> must close before <to> is ready",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			// Echo handles, whatever form of id was typed.
			from, err := st.GetBead(c.Context(), args[0])
			if err != nil {
				return err
			}
			to, err := st.GetBead(c.Context(), args[1])
			if err != nil {
				return err
			}
			if err := st.AddDep(c.Context(), from.ID, to.ID, kind, a.who()); err != nil {
				return err
			}
			return a.out(store.Dep{From: from.ID, To: to.ID, Kind: kind}, func() {
				fmt.Printf("%s -%s-> %s\n", from.ID, kind, to.ID)
			})
		},
	}
	add.Flags().StringVarP(&kind, "kind", "k", "blocks", "edge kind (blocks, related, parent, ...)")
	var rmKind string
	rm := &cobra.Command{
		Use:   "rm <from> <to>",
		Short: "Remove an edge",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := st.RemoveDep(c.Context(), args[0], args[1], rmKind, a.who()); err != nil {
				return err
			}
			return a.out(map[string]string{"removed": "ok"}, func() {})
		},
	}
	rm.Flags().StringVarP(&rmKind, "kind", "k", "blocks", "edge kind")
	list := &cobra.Command{
		Use:   "list <id>",
		Short: "Edges touching a bead",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			ds, err := st.Deps(c.Context(), args[0])
			if err != nil {
				return err
			}
			return a.out(ds, func() {
				for _, d := range ds {
					fmt.Printf("%s -%s-> %s\n", d.From, d.Kind, d.To)
				}
			})
		},
	}
	cmd.AddCommand(add, rm, list)
	return cmd
}

func (a *app) primeCmd() *cobra.Command {
	var id, resident string
	cmd := &cobra.Command{
		Use:   "prime",
		Short: "Print everything an agent needs to work a bead, or to run as a resident",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if resident == "" && id == "" {
				resident = os.Getenv("YARD_RESIDENT")
			}
			if resident != "" {
				st, err := a.store(c.Context())
				if err != nil {
					return err
				}
				return a.primeResident(c.Context(), st, resident)
			}
			if id == "" {
				id = os.Getenv("YARD_BEAD")
			}
			if id == "" {
				return fmt.Errorf("--bead or --resident is required (or set YARD_BEAD / YARD_RESIDENT)")
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			b, err := st.GetBead(c.Context(), id)
			if err != nil {
				return err
			}
			notes, err := st.Notes(c.Context(), id)
			if err != nil {
				return err
			}
			deps, err := st.Deps(c.Context(), id)
			if err != nil {
				return err
			}
			blockers, err := st.OpenBlockers(c.Context(), id)
			if err != nil {
				return err
			}
			next, err := st.NextStages(c.Context(), id)
			if err != nil {
				return err
			}
			v := map[string]any{"bead": b, "notes": notes, "deps": deps, "open_blockers": blockers, "next": next}
			return a.out(v, func() {
				_ = a.printBead(b)
				if next.Flow != nil {
					line := strings.Join(next.Stages, ", ")
					if len(next.Terminal) > 0 {
						line += " (" + strings.Join(next.Terminal, ", ") + " closes the bead)"
					}
					fmt.Printf("\nNEXT STAGES: %s\n", line)
				}
				if len(blockers) > 0 {
					fmt.Printf("\nBLOCKED BY (open): %s\n", strings.Join(blockers, ", "))
				}
				if len(deps) > 0 {
					fmt.Println("\nEdges:")
					for _, d := range deps {
						fmt.Printf("  %s -%s-> %s\n", d.From, d.Kind, d.To)
					}
				}
				if len(notes) > 0 {
					fmt.Println("\nNotes:")
					for _, n := range notes {
						fmt.Printf("  [%s %s]\n  %s\n\n", n.At.Local().Format("2006-01-02 15:04"), n.Author,
							strings.ReplaceAll(strings.TrimSpace(n.Text), "\n", "\n  "))
					}
				}
			})
		},
	}
	cmd.Flags().StringVar(&id, "bead", "", "bead id (default $YARD_BEAD)")
	cmd.Flags().StringVar(&resident, "resident", "", "resident name (default $YARD_RESIDENT)")
	return cmd
}

func readAllStdin() (string, error) {
	b, err := os.ReadFile(filepath.Clean("/dev/stdin"))
	return strings.TrimRight(string(b), "\n"), err
}
