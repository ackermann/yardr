package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"io"
	"yardr.dev/yardr/internal/diskkeep"
)

func (a *app) diskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "disk", Short: "The machine's disk: what the yard keeps free"}
	cmd.AddCommand(&cobra.Command{
		Use:   "keep",
		Short: "Keep free space for landings' builds: rotate GOCACHE and clear stale go-tmp when under disk_keep_gb",
		Long: `Keep free space for landings' builds. Under disk_keep_gb free on
GOCACHE's volume (30 GB while unset, 0 turns the keeping off) it rotates
GOCACHE at a fresh sibling directory, so builders that start afterwards
use it, and records the old directory with the time; removes a recorded
directory once it has stood aside for 30 minutes, never the live one; and
clears go-build directories in GOTMPDIR older than 3 hours. It logs what
it did as a disk_kept event, and says it gave up when free space is still
under 10 GB afterwards. Over the threshold it does nothing and says
nothing.

The assembly runs it before every landing's tests run
(contrib/assembly/merge-local), where a failure is in the log and the
landing goes on to the tests. Run by hand it reports the same lines, and
exits 1 when anything past the rotation failed.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			settings, err := st.Settings(c.Context())
			if err != nil {
				return err
			}
			rep, err := diskkeep.Keep(c.Context(), st, settings)
			if err != nil {
				return err
			}
			if err := a.out(c.OutOrStdout(), rep, func(w io.Writer) {
				for _, line := range rep.Lines() {
					fmt.Fprintln(w, line)
				}
			}); err != nil {
				return err
			}
			if len(rep.Errors) > 0 {
				return fmt.Errorf("disk keep: %s", strings.Join(rep.Errors, "; "))
			}
			return nil
		},
	})
	return cmd
}
