package store

import (
	"fmt"
	"slices"
	"strings"
)

// OriginatorAny is the originator a route names to take every originator's
// beads. It is a keyword, not a user: no user is named it, and no bead is
// for it.
const OriginatorAny = "any"

// DepotTemplate is the placeholder a yard default may use in a group name
// for the depot whose beads it serves: "{depot}-assembly" reads as
// "yardr-assembly" for depot yardr. An own route names no template, since
// it knows its depot.
const DepotTemplate = "{depot}"

// IsTemplatedGroup reports whether name uses the depot template.
func IsTemplatedGroup(name string) bool { return strings.Contains(name, DepotTemplate) }

// ResolveGroup is name with the depot template replaced by depot.
func ResolveGroup(name, depot string) string {
	return strings.ReplaceAll(name, DepotTemplate, depot)
}

// A RouteMatch is the match fields of a route: what matching reads. Route
// and eta.Route each adapt to it at the boundary, since the two types
// differ and the one implementation of matching reads this shape alone.
type RouteMatch struct {
	Stage        string
	Type         *string
	Depot        *string
	Labels       []string
	BeadPriority *int
	// Originator matches beads for this user alone; OriginatorAny
	// matches every originator's, and nil the operator's alone.
	Originator *string
}

// A BeadMatch is the bead fields matching reads.
type BeadMatch struct {
	Stage      string
	Type       string
	Depot      *string
	Labels     []string
	Priority   int
	Originator string
}

// MatchesOriginator reports whether a route naming routeOriginator takes
// beads for originator, whose subscription they build on. Any takes every
// originator's; a route without one takes only the operator's, however it
// ranks, so no general route swallows another person's work. A bead without one reads as the operator's.
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
func Matches(r RouteMatch, b BeadMatch, operator string) bool {
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
// itself, the group chain and the note are left out: those are what an
// override overrides.
type Key struct {
	stage           string
	typ             string
	hasType         bool
	labels          string // the labels sorted, joined; "" for none
	beadPriority    int
	hasBeadPriority bool
	originator      string
}

// ResolvedOriginator is whose beads a route naming routeOriginator takes
// as one word: the operator where it names nobody, any where it names
// any, else the user it names.
func ResolvedOriginator(routeOriginator *string, operator string) string {
	if routeOriginator == nil {
		return operator
	}
	return *routeOriginator
}

// KeyOf is r's match key for operator.
func KeyOf(r RouteMatch, operator string) Key {
	k := Key{stage: r.Stage, originator: ResolvedOriginator(r.Originator, operator)}
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

// EffectiveRoutesByKey are the routes of the depot a bead names, decided by
// match key alone: its own routes plus every yard default whose key no own
// route states. An own route therefore sets a default aside only for the
// beads it states, and only in its depot; a depot that states no route for
// a key keeps the default, and a bead with no depot (nil) has the defaults
// alone. A route naming another depot is never in the set. The group
// template stays unresolved: the store's EffectiveRoutes and eta resolve it
// after. view adapts each route to what matching reads; the result keeps
// the order of routes.
func EffectiveRoutesByKey[R any](routes []R, view func(R) RouteMatch, depot *string, operator string) []R {
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
func Match[R any](routes []R, view func(R) RouteMatch, b BeadMatch, operator string) *R {
	for i := range routes {
		if Matches(view(routes[i]), b, operator) {
			return &routes[i]
		}
	}
	return nil
}
