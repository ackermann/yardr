// Package shared holds what eta, the store and limits share but cannot
// pass between them: route matching and duration spans. The store reads
// eta for its estimate shapes and limits reads the store, so neither the
// store's matcher nor limits' span can be imported where they are also
// needed; the one implementation of each lives here, over minimal shapes
// every side adapts at the boundary, and every caller delegates to it.
package shared

import (
	"fmt"
	"slices"
	"strings"
)

// OriginatorAny is the originator a route names to take every originator's
// beads. It is a keyword, not a user: no user is named it, and no bead is
// for it.
const OriginatorAny = "any"

// A Route is the match fields of a route: what matching reads. Both
// store.Route and eta.Route adapt to it at the boundary; neither moves
// here, the store's being persisted.
type Route struct {
	Stage        string
	Type         *string
	Depot        *string
	Labels       []string
	BeadPriority *int
	// Originator matches beads for this user alone; OriginatorAny
	// matches every originator's, and nil the operator's alone.
	Originator *string
}

// A Bead is the bead fields matching reads.
type Bead struct {
	Stage      string
	Type       string
	Depot      *string
	Labels     []string
	Priority   int
	Originator string
}

// MatchesOriginator reports whether a route naming routeOriginator takes
// beads for originator, whose subscription they build on. Any takes every
// originator's; a route without one takes only the operator's, however
// high its priority or specificity, so no general route swallows another
// person's work. A bead without one reads as the operator's.
func MatchesOriginator(routeOriginator *string, originator, operator string) bool {
	if originator == "" {
		originator = operator
	}
	switch {
	case routeOriginator == nil:
		return originator == operator
	case *routeOriginator == OriginatorAny:
		return true
	default:
		return originator == *routeOriginator
	}
}

// Matches reports whether r applies to b: its stage, and every filter it
// sets — the type, the depot, each label, the bead priority and the
// originator.
func Matches(r Route, b Bead, operator string) bool {
	if r.Stage != b.Stage {
		return false
	}
	if r.Type != nil && *r.Type != b.Type {
		return false
	}
	if r.Depot != nil && (b.Depot == nil || *r.Depot != *b.Depot) {
		return false
	}
	if slices.ContainsFunc(r.Labels, func(l string) bool { return !slices.Contains(b.Labels, l) }) {
		return false
	}
	if r.BeadPriority != nil && b.Priority > *r.BeadPriority {
		return false
	}
	return MatchesOriginator(r.Originator, b.Originator, operator)
}

// A Key is which beads a route takes beside its scope: its stage, its
// type (unset is any type), its set of labels (order does not matter), its
// bead priority (unset is any) and its originator as MatchesOriginator
// resolves it (unset is the operator, any is every originator, a name is
// that user; a route that names the operator and one that names nobody
// therefore state the same key while that user is operator). The depot
// itself, the priority, the group chain and the note are left out: those
// are what an override overrides.
type Key struct {
	stage           string
	typ             string
	hasType         bool
	labels          string // the labels sorted, joined; "" for none
	beadPriority    int
	hasBeadPriority bool
	originator      string
}

// KeyOf is r's match key for operator.
func KeyOf(r Route, operator string) Key {
	originator := operator
	if r.Originator != nil {
		originator = *r.Originator
	}
	k := Key{stage: r.Stage, originator: originator}
	if r.Type != nil {
		k.typ, k.hasType = *r.Type, true
	}
	if len(r.Labels) > 0 {
		sorted := slices.Clone(r.Labels)
		slices.Sort(sorted)
		var labels strings.Builder
		for _, label := range slices.Compact(sorted) {
			fmt.Fprintf(&labels, "%d:", len(label))
			labels.WriteString(label)
		}
		k.labels = labels.String()
	}
	if r.BeadPriority != nil {
		k.beadPriority, k.hasBeadPriority = *r.BeadPriority, true
	}
	return k
}

// EffectiveRoutes are the routes of the depot a bead names: its own routes
// plus every yard default whose match key no own route states. An own
// route therefore sets a default aside only for the beads it states, and
// only in its depot; a depot that states no route for a key keeps the
// default, and a bead with no depot (nil) has the defaults alone. A route
// naming another depot is never in the set. view adapts each route to
// what matching reads; the result keeps the order of routes.
func EffectiveRoutes[R any](routes []R, view func(R) Route, depot *string, operator string) []R {
	if depot == nil {
		var out []R
		for _, r := range routes {
			if view(r).Depot == nil {
				out = append(out, r)
			}
		}
		return out
	}
	own := map[Key]bool{}
	for _, r := range routes {
		vr := view(r)
		if vr.Depot != nil && *vr.Depot == *depot {
			own[KeyOf(vr, operator)] = true
		}
	}
	var out []R
	for _, r := range routes {
		vr := view(r)
		switch {
		case vr.Depot != nil && *vr.Depot == *depot:
			out = append(out, r)
		case vr.Depot == nil && !own[KeyOf(vr, operator)]:
			out = append(out, r)
		}
	}
	return out
}

// Match is the first route in match order that applies to b: routes in the
// order they are tried, already effective for b's depot. nil when none
// does. view adapts each route to what matching reads.
func Match[R any](routes []R, view func(R) Route, b Bead, operator string) *R {
	for i := range routes {
		if Matches(view(routes[i]), b, operator) {
			return &routes[i]
		}
	}
	return nil
}
