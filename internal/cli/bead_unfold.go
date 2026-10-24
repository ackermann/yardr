package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
)

func (a *app) newBeadUnfoldCmd() *cobra.Command {
	var to, reason string
	unfold := &cobra.Command{
		Use:   "unfold <carrier> <member> --reason <reason> [--to <stage>]",
		Short: "Take a bead out of the carrier it was folded into",
		Long: `Takes the member out of the carrier, restored whole: it reopens in --to
(the flow's first stage without it) with the reason as a note, and what the
fold moved from it comes back: its tracking links, the beads it blocked
(with the stages the edges named) and whoever watched it. A target two
parts moved stays on the carrier until the last of them is out, so each
part restores exactly what it moved. The folded edge is gone, the carrier's
body gains the taken-out line, and an unfolded event is logged on it.

Each part of a carrier is its own commit, starting with the part's bead id,
so taking the part out of the bead leaves taking it out of the branch: revert
its commit on the carrier's branch and go on reviewing. A bead that is not a
member of the carrier is refused, as are an open member, a closed carrier and
a --to outside the member's flow. --json prints the member, with the carrier
it came out of.`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			if strings.TrimSpace(reason) == "" {
				return fmt.Errorf("say why the part comes out with --reason <reason>")
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			// Read first, for the handles to print whatever form of id was
			// typed.
			carrier, err := st.GetBead(c.Context(), args[0])
			if err != nil {
				return err
			}
			member, err := st.GetBead(c.Context(), args[1])
			if err != nil {
				return err
			}
			out, err := st.Unfold(c.Context(), carrier.ID, member.ID, to, reason, a.who())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), struct {
				*store.Bead
				Carrier string `json:"carrier"`
			}{out, carrier.ID}, func(w io.Writer) {
				fmt.Fprintf(w, "%s reopened in %s, out of %s; revert its commit on %s\n",
					out.ID, out.Stage, carrier.ID, store.Branch(carrier.ID))
			})
		},
	}
	unfold.Flags().StringVar(&to, "to", "", "the stage the part reopens in (default: the flow's first stage)")
	unfold.Flags().StringVar(&reason, "reason", "", "why the part comes out")
	return unfold
}
