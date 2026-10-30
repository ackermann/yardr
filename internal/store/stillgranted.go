package store

import (
	"fmt"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/source"
)

// StillGranted is a line to each call now changes of the declaration it
// replaces that somebody holds by its name. A grant by name was given for
// the call as it was, and an update that now writes something else is
// still its holder's to make, which the diff of the calls does not say. A
// grant of read holds no call by its name: Widened says who it reaches.
//
// The terminal's source add, the replacement cards and the pack plan all
// say it in these words; a card ends each line with a period, as its
// other lines do.
func StillGranted(was, now *source.Declaration, grants []SourceGrant) []string {
	if was == nil {
		return nil
	}
	var out []string
	for _, c := range now.Calls {
		old, had := was.Call(c.Name)
		if !had || SameCall(*old, c) {
			continue
		}
		var who []string
		for _, g := range grants {
			if slices.Contains(g.Calls, c.Name) {
				who = append(who, g.Holder())
			}
		}
		if len(who) > 0 {
			out = append(out, fmt.Sprintf("the changed call %s (%s) is still granted by its name to %s (yardr source revoke %s %s takes it back)",
				c.Name, c.Kind, strings.Join(who, ", "), now.Name, c.Name))
		}
	}
	return out
}
