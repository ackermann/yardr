package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"io"
	"yardr.dev/yardr/internal/store"
)

// What route explain says of a bead: where it would go and why. The
// computation is the dispatcher's — the bead's depot's effective routes
// in SortRoutes order, the first route that Matches the winner — so the
// explanation, the list and the claim cannot drift apart. The words for
// what matched are routeMatch's; the set-aside lines are route list
// --depot's. Nothing is written, nothing is logged and no provider is
// asked: the choice among a winning route's groups, or whether its group
// that starts agents may start a bead, is made at the claim, from the
// windows then.

func (a *app) routeExplainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "explain <bead>",
		Short: "Say where a bead would go and why",
		Long: `Say where a bead would go: the winner among its depot's effective
routes, every route that matches in order with the ranking leg that
placed it, the routes considered that do not match and why, the
defaults the depot sets aside for the bead's stage, and the winning
route's groups.

Each matching route after the first names the leg that placed it below
the one above, the first of these on which the two differ: urgency (a
bead-priority filter, the narrower threshold first), asked for by name
(more labels that are not estimates), estimate (more labels that begin
difficulty: or size:), the rest (the type, the depot and a named
originator count one each), own before default, or id.

A bead with no depot is explained against the yard defaults. A bead
that pins a flow is explained as its unpinned twin: routing reads the
stage, type, depot, labels, bead priority and originator, never the
flow, and the pin is named beside the answer it does not change.

The choice among a winning route's groups is made at the claim, from
the windows then, as is whether a group that starts agents and has no
condition may start the bead: it does not while a window of its provider
is used up, one that counts for a single model aside (yardr limits marks
it). What a claim chose stays where it is, in bead show and the
quota_wait event. The command writes nothing, logs nothing and asks no
provider.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			b, err := st.GetBead(c.Context(), args[0])
			if err != nil {
				return err
			}
			settings, err := st.Settings(c.Context())
			if err != nil {
				return err
			}
			operator := settings[store.OperatorSetting]
			rs, err := st.Routes(c.Context())
			if err != nil {
				return err
			}
			gs, err := st.Groups(c.Context())
			if err != nil {
				return err
			}
			groups := make(map[string]store.Group, len(gs))
			for _, g := range gs {
				groups[g.Name] = g
			}
			exp := explainBeadRouting(*b, rs, groups, settings[store.DefaultKindSetting], operator)
			return a.out(c.OutOrStdout(), exp, func(w io.Writer) { printRouteExplanation(w, exp, operator) })
		},
	}
	return cmd
}

// routeExplanation is what route explain says of a bead, as --json
// carries it: the bead's matching facts, every effective route that
// matches in order with the leg that placed it, the winner's id, the
// effective routes that do not match with why, the defaults the depot
// sets aside for the bead's stage, and the winning route's groups.
type routeExplanation struct {
	Bead        routeExplainBead   `json:"bead"`
	Matches     []routeExplainHit  `json:"matches"`
	Winner      *string            `json:"winner"`
	NonMatching []routeExplainMiss `json:"non_matching"`
	SetAside    []store.SetAside   `json:"set_aside"`
	Quota       *routeExplainQuota `json:"quota"`
}

// routeExplainBead is the bead's matching facts: what routing reads,
// and the flow pin it does not.
type routeExplainBead struct {
	ID         string   `json:"id"`
	Stage      string   `json:"stage"`
	Type       string   `json:"type"`
	Depot      *string  `json:"depot"`
	Labels     []string `json:"labels"`
	Priority   int      `json:"priority"`
	Originator string   `json:"originator"`
	Pin        string   `json:"pin,omitempty"`
}

// routeExplainHit is an effective route that matches, with whether it is
// the depot's own or a yard default, and the leg that placed it below
// the route above it (nil for the winner).
type routeExplainHit struct {
	store.Route
	Effective string           `json:"effective"`
	Leg       *routeExplainLeg `json:"leg"`
}

// routeExplainLeg is the ranking leg that placed a matching route below
// the one above it: Above names that route, and Over its count and this
// route's for the counted legs (asked for by name, estimate, the rest).
type routeExplainLeg struct {
	Above string `json:"above"`
	Leg   string `json:"leg"`
	Over  []int  `json:"over,omitempty"`
}

// routeExplainMiss is an effective route that does not match, with
// whether it is the depot's own or a yard default, and why.
type routeExplainMiss struct {
	store.Route
	Effective string `json:"effective"`
	Reason    string `json:"reason"`
}

// routeExplainQuota is the winning route's groups in order; ClaimTime
// says the choice among them, or whether a group that starts agents may
// start the bead, is made at the claim, from the windows then. Nil where no
// route matched.
type routeExplainQuota struct {
	Groups    []store.RouteGroup `json:"groups"`
	ClaimTime bool               `json:"claim_time"`
}

// explainBeadRouting explains b against the yard's routes: its depot's
// effective routes in match order, each kept as a match or a miss, the
// first match the winner, and the defaults its depot sets aside for its
// stage. groups and def, the default_kind setting, say which of the winner's
// groups start agents (store.Route.Gated).
func explainBeadRouting(b store.Bead, routes []store.Route, groups map[string]store.Group, def, operator string) routeExplanation {
	eff := store.EffectiveRoutes(routes, b.Depot, operator)
	store.SortRoutes(eff)
	exp := routeExplanation{
		Bead: routeExplainBead{
			ID: b.ID, Stage: b.Stage, Type: b.Type, Depot: b.Depot,
			Labels:     append([]string{}, b.Labels...),
			Priority:   b.Priority,
			Originator: store.BeadOriginator(b, operator),
			Pin:        store.PinnedFlow(&b),
		},
		Matches: []routeExplainHit{}, NonMatching: []routeExplainMiss{}, SetAside: []store.SetAside{},
	}
	for _, r := range eff {
		mark := "default"
		if r.Depot != nil {
			mark = "own"
		}
		if !r.Matches(b, operator) {
			exp.NonMatching = append(exp.NonMatching, routeExplainMiss{
				Route: r, Effective: mark, Reason: missReason(r, b, operator)})
			continue
		}
		hit := routeExplainHit{Route: r, Effective: mark}
		if n := len(exp.Matches); n > 0 {
			above := exp.Matches[n-1].Route
			leg := &routeExplainLeg{Above: above.ID, Leg: store.RankLeg(above, r)}
			switch leg.Leg {
			case store.LegByName:
				leg.Over = []int{store.RouteByName(above), store.RouteByName(r)}
			case store.LegEstimate:
				leg.Over = []int{store.RouteEstimates(above), store.RouteEstimates(r)}
			case store.LegRest:
				leg.Over = []int{store.RouteRest(above), store.RouteRest(r)}
			}
			hit.Leg = leg
		}
		exp.Matches = append(exp.Matches, hit)
	}
	if len(exp.Matches) > 0 {
		winner := exp.Matches[0].Route
		exp.Winner = &winner.ID
		exp.Quota = &routeExplainQuota{Groups: winner.Groups(), ClaimTime: winner.Gated(groups, def)}
	}
	if b.Depot != nil {
		for _, s := range store.SetAsideRoutes(routes, *b.Depot, operator) {
			if s.Route.Stage == b.Stage {
				exp.SetAside = append(exp.SetAside, s)
			}
		}
	}
	return exp
}

// effectiveScope is whose routes the bead is explained against: its
// depot's effective routes, or the yard defaults for a bead with no
// depot.
func effectiveScope(depot *string) string {
	if depot == nil {
		return "the yard defaults"
	}
	return *depot + "'s effective routes"
}

// printRouteExplanation prints the explanation: the bead's matching
// facts, the matches in order with the winner, the routes considered
// that do not match, the defaults set aside for the bead's stage, and
// the winning route's groups.
func printRouteExplanation(w io.Writer, exp routeExplanation, operator string) {
	fmt.Fprintln(w, explainBeadLine(exp.Bead, operator))
	scope := effectiveScope(exp.Bead.Depot)
	if len(exp.Matches) == 0 {
		fmt.Fprintf(w, "no route in %s matches: the bead is unrouted\n", scope)
	} else {
		fmt.Fprintf(w, "matches in %s, in order:\n", scope)
		for i, m := range exp.Matches {
			s := fmt.Sprintf("  %d. %s  %s  %s  -> %s", i+1, m.ID, m.Effective, explainRouteDesc(m.Route), m.GroupList())
			if i == 0 {
				fmt.Fprintln(w, s+"  WINNER")
				continue
			}
			fmt.Fprintf(w, "%s  (%s)\n", s, explainLegText(i, m.Leg))
		}
	}
	if len(exp.NonMatching) == 0 {
		fmt.Fprintln(w, "considered and not matching: none")
	} else {
		fmt.Fprintln(w, "considered and not matching:")
		for _, m := range exp.NonMatching {
			fmt.Fprintf(w, "  %s  %s  %s: %s\n", m.ID, m.Effective, explainRouteDesc(m.Route), m.Reason)
		}
	}
	printExplainSetAside(w, exp, operator)
	if exp.Quota == nil {
		return
	}
	if !exp.Quota.ClaimTime {
		fmt.Fprintln(w, "quota: the winning route names one group with no condition; nothing to ask a provider for")
		return
	}
	if gs := exp.Quota.Groups; len(gs) == 1 && gs[0].When == "" {
		fmt.Fprintf(w, "quota: the winning route names %s with no condition; it starts the bead at the claim unless a window of its provider is used up then, one for a single model aside\n",
			gs[0].Name)
		return
	}
	fmt.Fprintf(w, "quota: the winning route names %s; the choice among these is made at the claim, from the windows then\n",
		exp.Matches[0].GroupList())
}

// explainBeadLine is the bead's matching facts: the stage, type, depot,
// labels, bead priority and originator routing reads, and the flow pin
// it does not.
func explainBeadLine(b routeExplainBead, operator string) string {
	depot := "no depot"
	if b.Depot != nil {
		depot = "depot " + *b.Depot
	}
	labels := "no labels"
	if len(b.Labels) > 0 {
		labels = "labels " + strings.Join(b.Labels, ", ")
	}
	orig := b.Originator
	switch {
	case orig == "":
		orig = "none"
	case orig == operator:
		orig += " (the operator)"
	}
	s := fmt.Sprintf("bead %s: stage %s, type %s, %s, %s, priority P%d, originator %s",
		b.ID, b.Stage, b.Type, depot, labels, b.Priority, orig)
	if b.Pin != "" {
		s += ", pinned to flow " + b.Pin + " (routing reads no flow)"
	}
	return s
}

// explainRouteDesc is a route of the explanation as its line names it:
// the stage, and what of the bead matched in routeMatch's words.
func explainRouteDesc(r store.Route) string {
	desc := r.Stage
	if m := routeMatch(r); m != "" {
		desc += ", " + m
	}
	return desc
}

// explainLegText is the leg that placed a route below the one above it,
// in words: the neighbour's number and the leg's name. above is that
// neighbour's number in the matches.
func explainLegText(above int, leg *routeExplainLeg) string {
	return fmt.Sprintf("below %d: %s", above, leg.Leg)
}

// printExplainSetAside names the defaults the bead's depot sets aside
// for the bead's stage, each with the own route that states its key, in
// route list --depot's words.
func printExplainSetAside(w io.Writer, exp routeExplanation, operator string) {
	if exp.Bead.Depot == nil {
		fmt.Fprintln(w, "defaults set aside: none (the bead names no depot)")
		return
	}
	depot, aside := *exp.Bead.Depot, exp.SetAside
	switch len(aside) {
	case 0:
		fmt.Fprintf(w, "defaults set aside in %s for this stage: none\n", depot)
	case 1:
		fmt.Fprintf(w, "1 default set aside in %s for this stage: %s\n", depot, setAsideLine(aside[0], operator))
	default:
		fmt.Fprintf(w, "%d defaults set aside in %s for this stage:\n", len(aside), depot)
		for _, s := range aside {
			fmt.Fprintln(w, "  "+setAsideLine(s, operator))
		}
	}
}

// missReason is why the effective route r does not match the bead b, ""
// where it does: the first filter in MatchRoute's order that fails, in
// words. The route's own filters are on its line; the reason names the
// bead's fact that misses them.
func missReason(r store.Route, b store.Bead, operator string) string {
	if r.Stage != b.Stage {
		return "the bead is in stage " + b.Stage
	}
	if r.Type != nil && *r.Type != b.Type {
		return "the bead is of type " + b.Type
	}
	if r.Depot != nil && (b.Depot == nil || *r.Depot != *b.Depot) {
		if b.Depot == nil {
			return "the bead names no depot"
		}
		return "the bead is in depot " + *b.Depot
	}
	var missing []string
	for _, l := range r.Labels() {
		if !slices.Contains(b.Labels, l) {
			missing = append(missing, l)
		}
	}
	if len(missing) == 1 {
		return "the bead does not have label " + missing[0]
	}
	if len(missing) > 1 {
		return "the bead does not have labels " + strings.Join(missing, ", ")
	}
	if r.BeadPriority != nil && b.Priority > *r.BeadPriority {
		return fmt.Sprintf("the bead is P%d, less urgent than P%d", b.Priority, *r.BeadPriority)
	}
	if !r.MatchesOriginator(b.Originator, operator) {
		orig := store.BeadOriginator(b, operator)
		who := orig
		switch {
		case orig == "":
			who = "none"
		case orig == operator:
			who += " (the operator)"
		}
		takes := "the operator"
		if r.Originator != nil && *r.Originator != operator {
			takes = *r.Originator
		}
		return fmt.Sprintf("the bead is for %s, not %s", who, takes)
	}
	return ""
}
