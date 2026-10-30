package cli

import (
	"fmt"
	"io"
	"strings"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// printPlanDeclared prints, for each source the pack declares, where its
// calls go and what an import would do about it: added, replaced with
// what the replacement widens or still grants to somebody already, or
// already had. refused says, by source, why a declared source the plan
// would add is left out by this run.
func printPlanDeclared(w io.Writer, p *pack.Plan, refused map[string]string) {
	for _, d := range p.Declared {
		fmt.Fprintf(w, "\ndeclared source %s, from %s\n", d.Source, d.File)
		fmt.Fprintf(w, "  its calls go to %s: %s\n", d.Base, strings.Join(d.Calls, ", "))
		for _, c := range d.Commands {
			if c.SHA256 == "" {
				fmt.Fprintf(w, "  command %s: %s (the program is the operator's own, not copied)\n", c.Key, c.Program)
			} else {
				fmt.Fprintf(w, "  command %s: %s, sha256 %s\n", c.Key, c.Program, c.SHA256)
			}
		}
		switch {
		case refused[d.Source] != "":
			fmt.Fprintf(w, "  not added: %s\n", refused[d.Source])
		case d.Action == pack.Create:
			fmt.Fprintln(w, "  would be added to the yard's sources")
		case d.Action == pack.Change:
			fmt.Fprintln(w, "  would replace the declaration the yard has under that name, which keeps its env_file (what changes is under declared sources, above)")
		default:
			fmt.Fprintln(w, "  the yard has it")
		}
		if r := d.Reaches; r != nil {
			who := make([]string, len(r.Who))
			for i, holder := range r.Who {
				who[i] = holder.Holder()
			}
			fmt.Fprintf(w, "  through their grant of %s, %s would then call: %s (nobody is asked: %s covers the read calls a later file adds; yardr source revoke %s %s takes it back)\n",
				store.GrantRead, strings.Join(who, ", "), strings.Join(r.Calls, ", "), store.GrantRead, d.Source, store.GrantRead)
		}
		for _, l := range d.StillGranted {
			fmt.Fprintf(w, "  %s\n", l)
		}
		for _, n := range d.Needs {
			fmt.Fprintf(w, "  %-8s %-12s %s\n", n.Status, strings.ReplaceAll(n.What, "_", " "), n.Detail)
		}
	}
}
