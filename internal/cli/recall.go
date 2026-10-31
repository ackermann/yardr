package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
)

func (a *app) recallCmd() *cobra.Command {
	var scope string
	cmd := &cobra.Command{
		Use:   "recall [words...]",
		Short: "List live memories (titles), or find them and journal entries by words",
		Long: `Lists live memories by title; yardr bead show <id> reads one.

With words, an entry that holds any one of them is found, in any case and
inside a longer word, and the matching entries of crew journals follow the
table, in full: the journal read back. An entry that holds more of the words
comes first, and of two that hold the same ones, the one that holds them side
by side as typed. A word under 3 characters is left out unless no word is
longer. Memories otherwise keep their order and journal entries come newest
first. At most 20 journal entries are printed, then a line counting the rest:
narrow the query to see them.`,
		Example: `  yardr recall worktree cleanup timeout`,
		Args:    cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if scope == "" {
				if r := a.yardrenv("CREW"); r != "" {
					scope = "crew:" + r
				}
			}
			q := strings.TrimSpace(strings.Join(args, " "))
			ms, err := st.Recall(c.Context(), scope, q)
			if err != nil {
				return err
			}
			hits := &store.JournalRecall{Hits: []store.JournalHit{}}
			if q != "" {
				if hits, err = st.RecallJournal(c.Context(), scope, q); err != nil {
					return err
				}
			}
			v := map[string]any{"memories": ms, "journal": hits.Hits, "journal_omitted": hits.Omitted}
			return a.out(c.OutOrStdout(), v, func(w io.Writer) {
				rows := [][]string{}
				for _, m := range ms {
					exp := "-"
					if m.Expires != nil {
						exp = m.Expires.Local().Format("2006-01-02 15:04")
					}
					rows = append(rows, []string{m.ID, m.Scope, exp, m.Title})
				}
				table(w, "ID\tSCOPE\tEXPIRES\tTITLE", rows)
				if len(hits.Hits) > 0 {
					fmt.Fprintf(w, "\nJournal entries matching %q:\n", q)
					for _, h := range hits.Hits {
						fmt.Fprintf(w, "- [%s %s %s %s] %s\n", h.Crew, h.Note.At.Local().Format("01-02 15:04"),
							or(h.Note.Kind, "note"), h.Note.Author, strings.TrimSpace(h.Note.Text))
					}
					if hits.Omitted > 0 {
						fmt.Fprintf(w, "... and %d more: narrow the query\n", hits.Omitted)
					}
				}
			})
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "", "this scope's memories, and with a query its journal (crew:<name> for one, all for every; default: your crew member, else all)")
	return cmd
}
