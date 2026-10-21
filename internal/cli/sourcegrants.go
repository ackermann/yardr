package cli

import (
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// grantsCmd is yardr source grants: every grant of every source in one
// list, a row per source × holder × call, each with its own grant (design
// yardr-ekmx 3.4). A row's GRANTED and BY are the call's, never the
// record's: the record's At and By are those of its last call, so a call
// with no Given entry says it was not kept, and its record's At and By
// are printed below the summary as what they are.
func (a *app) grantsCmd() *cobra.Command {
	var src, as string
	var groups, crew []string
	cmd := &cobra.Command{
		Use:   "grants [--source <name>] [--group <group>] [--crew <crew member>] [--as <user>]",
		Short: "List every grant of every source: who may call what, given when, by whom and on which card",
		Long: `Prints a row for each call a holder is granted of a source: read, which
is every read call of the source and is one row, or a call by its name. A
holder's plain grant and its grant as a user are rows apart. GRANTED and BY
are that call's own: when it was granted, by whom, and where it was
approved on a card the bead, the ask and who asked it. A call granted
before the yard kept that, whose history is gone, reads not kept; below
the summary its record's line says when it last gained a call and by whom,
for no call in particular.

--source, --group, --crew and --as narrow the list; --group and --crew
repeat, and a row of any one of them is listed.`,
		Example: `  yardr source grants
  yardr source grants --source google-mail
  yardr source grants --crew yardmaster --as ada`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			all, err := st.SourceGrants(c.Context(), src)
			if err != nil {
				return err
			}
			grants := []store.SourceGrant{}
			for _, g := range all {
				held := len(groups) == 0 && len(crew) == 0 ||
					g.Group != "" && slices.Contains(groups, g.Group) ||
					g.Crew != "" && slices.Contains(crew, g.Crew)
				if held && (as == "" || g.As == as) {
					grants = append(grants, g)
				}
			}
			decls := map[string]*source.Declaration{}
			if srcs, err := st.Sources(c.Context()); err == nil {
				for _, s := range srcs {
					// One this build does not read has no call it could
					// count or mark: its rows say the words alone.
					if d, err := s.Declaration(); err == nil {
						decls[d.Name] = d
					}
				}
			}
			return a.out(c.OutOrStdout(), grants, func(w io.Writer) {
				printGrants(w, grants, decls, time.Local)
			})
		},
	}
	cmd.Flags().StringVar(&src, "source", "", "the grants of this source alone")
	cmd.Flags().StringArrayVar(&groups, "group", nil, "the grants of this group (repeatable)")
	cmd.Flags().StringArrayVar(&crew, "crew", nil, "the grants of this crew member (repeatable)")
	cmd.Flags().StringVar(&as, "as", "", "the grants made as this user alone")
	return cmd
}

// grantTime is how yardr source grants says a time: "17 Oct 19:02".
const grantTime = "2 Jan 15:04"

// printGrants writes the grants' table, its summary, a line for each
// record holding a call whose grant was not kept, and how to take one
// back. decls are the sources' declarations by name, for the read calls
// read covers and which calls are updates; loc is the zone times are said
// in.
func printGrants(w io.Writer, grants []store.SourceGrant, decls map[string]*source.Declaration, loc *time.Location) {
	if len(grants) == 0 {
		fmt.Fprintln(w, "no call of a source is granted")
		return
	}
	var rows [][]string
	var unkept []string
	sources, holders := map[string]bool{}, map[store.Grantee]bool{}
	updates, asUser := 0, 0
	for _, g := range grants {
		sources[g.Source], holders[g.Grantee] = true, true
		d := decls[g.Source]
		kept := true
		for _, call := range g.Calls {
			words := call
			if d != nil && call == store.GrantRead {
				words += " (" + countPhrase(readCalls(d), "call", "calls") + ")"
			} else if d != nil && isUpdate(d, call) {
				words += " (" + source.Update + ")"
				updates++
			}
			if g.As != "" {
				asUser++
			}
			granted, by := "not kept", "-"
			if given, ok := g.Given[call]; ok {
				granted, by = given.At.In(loc).Format(grantTime), given.By
				if given.Bead != "" {
					by += fmt.Sprintf(", on %s ask %d asked by %s", given.Bead, given.Ask, or(given.Asker, "unknown"))
				}
			} else {
				kept = false
			}
			rows = append(rows, []string{g.Source, g.Holder(), words, granted, by})
		}
		if !kept {
			unkept = append(unkept, fmt.Sprintf("%s to %s: granted before its history was kept; the record last gained a call %s by %s.",
				g.Source, g.Holder(), g.At.In(loc).Format(grantTime), g.By))
		}
	}
	table(w, "SOURCE\tHOLDER\tCALLS\tGRANTED\tBY", rows)
	verb := "call"
	if asUser == 1 {
		verb = "calls"
	}
	fmt.Fprintf(w, "%s granted of %s to %s: %d may change something outside the yard, %d %s as a user.\n",
		countPhrase(len(rows), "call", "calls"), countPhrase(len(sources), "source", "sources"),
		countPhrase(len(holders), "holder", "holders"), updates, asUser, verb)
	for _, line := range unkept {
		fmt.Fprintln(w, line)
	}
	fmt.Fprintln(w, "take one back: yardr source revoke <source> <call> --crew|--group <holder> [--as <user>]")
}

// readCalls is how many read calls the declaration d has: what a grant of
// read covers today.
func readCalls(d *source.Declaration) int {
	n := 0
	for _, c := range d.Calls {
		if c.Kind == source.Read {
			n++
		}
	}
	return n
}

// isUpdate reports whether the declaration d has the call name as an
// update call.
func isUpdate(d *source.Declaration, name string) bool {
	c, ok := d.Call(name)
	return ok && c.Kind == source.Update
}
