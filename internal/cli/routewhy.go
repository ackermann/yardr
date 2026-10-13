package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
)

// What a person is told of a route's quota conditions (store.Route.Choose)
// outside the dispatcher: why a bead went to its group or waits (bead
// show), and the two conditions that cannot work as they read (route add,
// doctor). None of it asks a provider: only the daemon runs the limits
// command for a route.

// beadRouting is what the dispatcher last said of the bead in its stage:
// the claim that gave it to its group, or the wait for a group whose
// condition holds.
type beadRouting struct {
	// Group took the bead; "" while it waits.
	Group string `json:"group,omitempty"`
	Route string `json:"route,omitempty"`
	// Passed are the groups the route passed over, each with why.
	Passed []store.PassedGroup `json:"passed,omitempty"`
	// Unknown says what of Group's condition could not be evaluated.
	Unknown string `json:"limits_unknown,omitempty"`
	// Was is the group that last held the bead in this stage, when it is
	// another than Group.
	Was string `json:"was,omitempty"`
	// ResetsAt is, for a bead that waits, the earliest reset among the
	// windows it waits for, where the provider said one.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// At is when the dispatcher said it.
	At time.Time `json:"at"`
}

// routingEvents is how far back bead show reads for the bead's claim: a
// session's own events (prompts, notes) come after it.
const routingEvents = 500

// routingOf reads the newest claimed or quota_wait event of the bead's
// stay in its stage; nil when the log has none, or when it no longer
// describes the bead: a claim whose group has let the bead go, a wait of a
// bead that has since been claimed or closed.
func routingOf(ctx context.Context, st store.API, b *store.Bead) (*beadRouting, error) {
	evs, err := st.Events(ctx, b.ID, routingEvents)
	if err != nil {
		return nil, err
	}
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		switch {
		case e.Kind == "advanced":
			// The bead came into its stage here: what is older is of an
			// earlier stay.
			return nil, nil
		case e.Kind != "claimed" && e.Kind != "quota_wait", e.Data["stage"] != b.Stage:
			continue
		}
		raw, err := json.Marshal(e.Data)
		if err != nil {
			return nil, err
		}
		r := beadRouting{At: e.At}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if e.Kind == "claimed" && (b.Group == nil || *b.Group != r.Group) {
			return nil, nil
		}
		if e.Kind == "quota_wait" && (b.Group != nil || b.Status != "open") {
			return nil, nil
		}
		return &r, nil
	}
	return nil, nil
}

// line is the routing for a person, "" when there is nothing to say: a
// bead that went to the one group of its route, as every bead did before
// a route could name several.
func (r *beadRouting) line(now time.Time) string {
	if r == nil {
		return ""
	}
	parts := []string{}
	if why := (store.RouteChoice{Passed: r.Passed}).Why(); why != "" {
		parts = append(parts, why)
	}
	if r.Group == "" {
		if r.ResetsAt != nil {
			parts = append(parts, "the first of those windows "+resetText(r.ResetsAt, now))
		}
		return fmt.Sprintf("waits since %s for a group of route %s whose condition holds: %s",
			limits.Clock(r.At, now), r.Route, strings.Join(parts, "; "))
	}
	if r.Unknown != "" {
		parts = append(parts, r.Unknown)
	}
	if r.Was != "" {
		parts = append(parts, r.Was+" held it in this stage before")
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("routed to %s by route %s: %s", r.Group, r.Route, strings.Join(parts, "; "))
}

// groupProvider is the provider the agents of g burn and whether a source
// answers for it, as the dispatcher resolves it for a route's condition
// (routeQuota): the group's kind and provider setting. name is "" when
// nothing says which provider it is.
func groupProvider(as []limits.Answerer, g store.Group) (name, kind string, ok bool) {
	kind, _ = g.Config["kind"].(string)
	if kind == "" {
		kind = agents.DefaultKind
	}
	provider, _ := g.Config["provider"].(string)
	_, name, ok = limits.Resolve(as, kind, provider)
	return name, kind, ok
}

// sameProvider are the warnings for a route that falls through to a group
// as used up as the one it passed over: a group with no condition that
// burns the provider of a conditioned group before it. The windows are the
// provider's and the dispatcher's memory is by provider, so the two read
// one answer.
func sameProvider(as []limits.Answerer, groups map[string]store.Group, r store.Route) []string {
	var out []string
	gated := map[string]string{} // provider -> the first conditioned group burning it
	for _, rg := range r.Groups() {
		name, _, _ := groupProvider(as, groups[rg.Name])
		if rg.When != "" {
			if _, seen := gated[name]; !seen && name != "" {
				gated[name] = rg.Name
			}
			continue
		}
		if first, same := gated[name]; same {
			out = append(out, fmt.Sprintf("%s burns provider %s, as %s does, and has no condition: a bead that passes over %s goes on to %s "+
				"however far the same windows are used. Give it a condition of its own (%s@<window><<percent>) or name a group of another provider",
				rg.Name, name, first, first, rg.Name, rg.Name))
		}
		// A group with no condition always holds: none after it is reached.
		break
	}
	return out
}

// routeWarnings is sameProvider for a route just added, from the yard's
// sources and groups. A yard that cannot be read warns of nothing: the
// route is added either way.
func routeWarnings(ctx context.Context, st store.API, r store.Route) []string {
	if !r.Quotaed() {
		return nil
	}
	as, err := limits.Answerers(ctx, st, "")
	if err != nil {
		return nil
	}
	all, err := st.Groups(ctx)
	if err != nil {
		return nil
	}
	groups := map[string]store.Group{}
	for _, g := range all {
		groups[g.Name] = g
	}
	return sameProvider(as, groups, r)
}

// routeNotes are doctor's notes on routes whose condition can never be
// evaluated: it is on a group no source answers for, so it holds whatever
// is used and gates nothing.
func routeNotes(as []limits.Answerer, routes []store.Route, groups []store.Group) []string {
	byName := map[string]store.Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	var out []string
	for _, r := range routes {
		for _, rg := range r.Groups() {
			if rg.When == "" {
				continue
			}
			name, kind, ok := groupProvider(as, byName[rg.Name])
			if ok {
				continue
			}
			whose := "its kind " + kind
			if name != "" {
				whose = "its provider " + name
			}
			out = append(out, fmt.Sprintf("route %s (stage %s) puts the condition %s on group %s, and no source answers for %s: "+
				"the condition holds whatever is used (limits.kinds)", r.ID, r.Stage, rg.When, rg.Name, whose))
		}
	}
	return out
}
