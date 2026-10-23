package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/dispatch"
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

// groupWait is why a ready bead no group owns waits for its group: the
// route that matched it, what of the bead matched, and the group that
// would take it with its live count and limit. The dispatcher logs one
// group_wait event for such a bead, once until it starts; this is the
// same wait computed from the routes, groups and live sessions, the
// dispatcher's way but read-only: nothing is asked of a provider and no
// event is written. Bead show says it of one bead, ready --waiting of
// every such bead, in the same words.
type groupWait struct {
	// Group would take the bead; Active of its Limit slots are live.
	Group  string
	Active int
	Limit  int
	// Route matched the bead; Match is what of it matched, Priority the
	// route's.
	Route    string
	Match    string
	Priority int
	// Quota is the stored quota_wait's why, where one of this route and
	// stage still holds the bead; "" otherwise.
	Quota string
}

// line is the wait for a person: "waits for builders (2 of 2) by route
// k2abc (label difficulty:1, priority 1)", or "(paused)" where the group's
// limit is 0.
func (w *groupWait) line() string {
	if w == nil {
		return ""
	}
	by := fmt.Sprintf("priority %d", w.Priority)
	if w.Match != "" {
		by = w.Match + ", " + by
	}
	slots := fmt.Sprintf("%d of %d", w.Active, w.Limit)
	if w.Limit == 0 {
		slots = "paused"
	}
	s := fmt.Sprintf("waits for %s (%s) by route %s (%s)",
		w.Group, slots, w.Route, by)
	if w.Quota != "" {
		s += "; quota: " + w.Quota
	}
	return s
}

// routeMatch is what of a bead r matched, as the group wait names it:
// the filters the route sets, in route list's column order. A route that
// sets none matches any bead of its stage, and names only its priority.
func routeMatch(r store.Route) string {
	var parts []string
	if r.Type != nil {
		parts = append(parts, "type "+*r.Type)
	}
	if r.Depot != nil {
		parts = append(parts, "depot "+*r.Depot)
	}
	if labels := r.Labels(); len(labels) == 1 {
		parts = append(parts, "label "+labels[0])
	} else if len(labels) > 1 {
		parts = append(parts, "labels "+strings.Join(labels, ", "))
	}
	if r.BeadPriority != nil {
		parts = append(parts, "beads "+r.BeadPriorities())
	}
	if r.Originator != nil && *r.Originator != store.OriginatorAny {
		parts = append(parts, "originator "+*r.Originator)
	}
	return strings.Join(parts, ", ")
}

// waitView is what the group wait is computed from: the routes in match
// order, the groups by name, the live sessions per group, and the
// operator. One read of each serves every bead of a listing.
type waitView struct {
	routes   []store.Route
	groups   map[string]store.Group
	active   map[string]int
	operator string
}

// waitViewOf reads the yard the group wait is computed from. The counts
// are the live sessions per group, as the dispatcher counts them, except
// the dispatcher reaps first: a session its runner reports gone is ended
// there and counted here, until the next pass.
func waitViewOf(ctx context.Context, st store.API) (*waitView, error) {
	routes, err := st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	dispatch.SortRoutes(routes)
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	sessions, err := st.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	y := &waitView{routes: routes, groups: map[string]store.Group{}, active: map[string]int{},
		operator: settings[store.OperatorSetting]}
	for _, g := range groups {
		y.groups[g.Name] = g
	}
	for _, s := range sessions {
		y.active[s.Group]++
	}
	return y, nil
}

// waitFor is why b waits for its group, nil when it waits for none: a
// bead that is not open, is held or owned, or is of no working type never
// routes; one no route matches, whose group cannot be read without a
// provider, whose group is unknown, or whose group has a free slot waits
// for no group. Readiness is the caller's: bead show reads HoldingDeps,
// ready lists Ready beads. routed is the bead's stored routing, for the
// quota a stored wait says holds it.
func (y *waitView) waitFor(b *store.Bead, routed *beadRouting) *groupWait {
	if b.Status != "open" || b.Hold || b.Group != nil ||
		store.NotWork[b.Type] || b.Type == store.TypeQuestion {
		return nil
	}
	r := dispatch.Match(y.routes, *b, y.operator)
	if r == nil {
		return nil
	}
	g := readOnlyChoice(*r)
	if g == "" {
		return nil
	}
	grp, ok := y.groups[g]
	if !ok {
		return nil
	}
	if y.active[g] < grp.Limit {
		return nil
	}
	w := &groupWait{Group: g, Active: y.active[g], Limit: grp.Limit,
		Route: r.ID, Match: routeMatch(*r), Priority: r.Priority}
	// A quota the stored wait says holds it, from the stored event, never
	// by asking the provider: only where it is of this route, in this
	// stage as routingOf already holds.
	if routed != nil && routed.Group == "" && routed.Route == r.ID {
		if why := (store.RouteChoice{Passed: routed.Passed}).Why(); why != "" {
			w.Quota = why
		}
	}
	return w
}

// readOnlyChoice is the group of r that would take a bead, without asking
// any provider: the first group where no condition gates it, else the
// route's only group where it names one. Whether a window of that group's
// provider keeps it from starting the bead is the daemon's to read, and the
// quota_wait event says it. "" where the choice needs the windows, a
// condition on the first of several groups: only the daemon runs the limits
// command.
func readOnlyChoice(r store.Route) string {
	if r.When == "" {
		return r.Group
	}
	var only string
	n := 0
	for _, g := range r.Groups() {
		if g.Name == store.RouteWait {
			continue
		}
		n++
		only = g.Name
	}
	if n == 1 {
		return only
	}
	return ""
}

// groupProvider is the provider the agents of g burn and whether a source
// answers for it, as the dispatcher resolves it for a route's condition
// (routeQuota): the group's kind, for a herdr group that names none the
// yard's default_kind (def), and provider setting. name is "" when nothing
// says which provider it is.
func groupProvider(as []limits.Answerer, g store.Group, def string) (name, kind string, ok bool) {
	kind = g.Kind(def)
	if kind == "" {
		kind = agents.DefaultKind
	}
	provider, _ := g.Config["provider"].(string)
	_, name, ok = limits.Resolve(as, kind, provider)
	return name, kind, ok
}

// groupEnvFile is the env_file setting of g, "" where it names none. Two
// groups naming one file read one login's windows; two naming different
// files read each their own.
func groupEnvFile(g store.Group) string {
	file, _ := g.Config["env_file"].(string)
	return file
}

// sameProvider are the warnings for a route that falls through to a group
// as used up as the one it passed over: a group with no condition that
// burns the provider of a conditioned group before it, on the same login. It
// is judged by none of the windows being used up, so it takes the beads the
// conditioned group was passed over for until they are. Two groups on one
// login's windows read one answer; two on different logins read each their
// own, so a personal group failing over to the operator's warns of nothing.
func sameProvider(as []limits.Answerer, groups map[string]store.Group, r store.Route, def string) []string {
	var out []string
	type providerLogin struct{ provider, envFile string }
	gated := map[providerLogin]string{} // provider and env_file -> first conditioned group
	for _, rg := range r.Groups() {
		if rg.Name == store.RouteWait {
			break // it ends the route, and burns nothing
		}
		name, _, _ := groupProvider(as, groups[rg.Name], def)
		if rg.When != "" {
			comparesProvider := false
			if limits, err := store.ParseQuota(rg.When); err == nil {
				for _, l := range limits {
					if l.Source == "" {
						comparesProvider = true
						break
					}
				}
			}
			if comparesProvider {
				key := providerLogin{name, groupEnvFile(groups[rg.Name])}
				if _, seen := gated[key]; !seen && name != "" {
					gated[key] = rg.Name
				}
			}
			continue
		}
		key := providerLogin{name, groupEnvFile(groups[rg.Name])}
		if first, same := gated[key]; same {
			out = append(out, fmt.Sprintf("%s burns provider %s, as %s does, and has no condition: a bead that passes over %s goes on to %s "+
				"until the same windows are used up. Give it a condition of its own (%s@<window><<percent>) or name a group of another provider",
				rg.Name, name, first, first, rg.Name, rg.Name))
		}
		// A group with no condition holds until a window is used up, and then
		// a group after it on the same login fails the same way: none after it
		// is warned of.
		break
	}
	return out
}

// routeWarnings is sameProvider for a route just added, from the yard's
// sources and groups, and the personal-login warning for a route taking
// operator beads to a personal group. A yard that cannot be read warns of
// nothing: the route is added either way.
func routeWarnings(ctx context.Context, st store.API, r store.Route) []string {
	all, err := st.Groups(ctx)
	if err != nil {
		return nil
	}
	groups := map[string]store.Group{}
	for _, g := range all {
		groups[g.Name] = g
	}
	var out []string
	settings, settingsErr := st.Settings(ctx)
	if settingsErr == nil && takesOperator(r, settings[store.OperatorSetting]) {
		if g, ok := groups[r.Worker()]; ok {
			if who := g.LoginFor(); who != "" {
				operator := settings[store.OperatorSetting]
				if who != operator {
					out = append(out, fmt.Sprintf("first group %s declares login_for=%s, and the route takes the operator's beads: it burns %s's login for the operator's work", g.Name, who, who))
				}
			}
		}
	}
	if !r.Quotaed() {
		return out
	}
	as, err := limits.Answerers(ctx, st, "")
	if err != nil {
		return out
	}
	return append(out, sameProvider(as, groups, r, defaultKind(ctx, st))...)
}

// takesOperator reports whether the route takes operator beads: one without
// an originator, with any, or naming the operator.
func takesOperator(r store.Route, operator string) bool {
	return r.MatchesOriginator(operator, operator)
}

// routeNotes are doctor's notes on routes with a comparison that can never
// be evaluated: it is on a provider no source answers for, or it names a
// source that says no limits of itself, so it holds whatever is used and
// gates nothing.
func routeNotes(as []limits.Answerer, routes []store.Route, groups []store.Group, def string) []string {
	byName := map[string]store.Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	self := map[string]bool{}
	for _, a := range as {
		if a.Limits.Self() {
			self[a.Source] = true
		}
	}
	var out []string
	for _, r := range routes {
		for _, rg := range r.Groups() {
			if rg.When == "" {
				continue
			}
			// AddRoute stored only what parses; a record that does not is
			// said nowhere.
			limits, err := store.ParseQuota(rg.When)
			if err != nil {
				continue
			}
			var provider, sourced bool
			for _, l := range limits {
				if l.Source == "" {
					provider = true
				} else {
					sourced = true
				}
			}
			if provider {
				name, kind, ok := groupProvider(as, byName[rg.Name], def)
				if !ok {
					whose := "its kind " + kind
					if name != "" {
						whose = "its provider " + name
					}
					holds := "the condition holds whatever is used"
					if sourced {
						holds = "the comparison on the provider's windows holds whatever is used"
					}
					out = append(out, fmt.Sprintf("route %s (stage %s) puts the condition %s on group %s, and no source answers for %s: %s (limits.kinds)",
						r.ID, r.Stage, rg.When, rg.Name, whose, holds))
				}
			}
			seen := map[string]bool{}
			for _, l := range limits {
				if l.Source == "" || seen[l.Source] || self[l.Source] {
					continue
				}
				seen[l.Source] = true
				out = append(out, fmt.Sprintf("route %s (stage %s) puts the condition %s on group %s, and no source named %s says limits of itself: "+
					"the comparison holds whatever is used (a limits key with no kinds)", r.ID, r.Stage, rg.When, rg.Name, l.Source))
			}
		}
	}
	return out
}
