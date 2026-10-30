package dispatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
)

// A route may name several groups and put a condition on the windows of a
// group's provider (store.Route.Choose): "yardr-builders while Claude's
// Weekly window is under 80% used, else muse-builders". A group that starts
// agents and names no condition is read the same way, as "none of its
// provider's windows is used up" (a window the source says is for one model
// alone aside, unless it is the bead's model in that group,
// store.QuotaWindow), so a plain route is gated too. The store holds the
// route and makes the comparison; what the windows are is learned here, from
// the yard's limits source, because only the daemon may run its command
// (harness_limits.go says why it is not leaned on).

// routeLimitsMemory is how old an answer of a provider may be for a route's
// condition: the command runs at most twelve times an hour for a provider,
// however many beads are ready. A group may so start a session or two
// after its window crossed the threshold, which is within the margin a
// threshold below 100 gives. An answer that says how long it holds
// (Report.HoldsFor, as the built-in machine's does for one pass) is asked
// again once that has passed, within the five minutes.
const routeLimitsMemory = 5 * time.Minute

// chooseGroup is the group of r that takes b: the first whose quota
// condition holds among the groups that could start it now, with the groups
// passed over. A group its model's hold keeps from starting (waitReason) is
// passed over for the route's next group, not chosen and then waited for:
// the choice is made among the rest, in route order, with their quota
// conditions judged as ever, and a group passed over is not judged, so its
// windows are not read. A group at its limit, with its runtime down, or
// with a start in flight is waited for instead, as before. ok is false when
// the bead does not start in this pass.
//
// The limits command is run only for a gated route (store.Route.Gated): one
// with a condition, or with a group that starts agents. A route of groups
// that start none, a script's or a person's, asks nothing. It is run only
// when one of the route's groups could start a bead now: a bead that would
// wait anyway (every group at its limit, its runtime down, held back for its
// model) asks nothing, and says so in one group_wait event until it starts.
// When no group's condition holds, or a window of each group's provider is
// used up, the bead waits, and says so in one quota_wait event until it
// starts.
//
// def is the yard's default_kind setting, for a group that names no kind.
func (d *Dispatcher) chooseGroup(ctx context.Context, b store.Bead, r *store.Route, groups map[string]store.Group, active map[string]int, def string) (c store.RouteChoice, ok bool) {
	if !r.Gated(groups, def) {
		return r.Choose(nil, nil), true
	}
	free := false
	var waits []waitedGroup
	var aside []waitedGroup
	for _, rg := range r.Groups() {
		g, known := groups[rg.Name]
		if !known {
			continue
		}
		if w, ok := d.waitReason(g, b, active); ok {
			waits = append(waits, w)
			if w.hold {
				aside = append(aside, w)
			}
		} else {
			free = true
		}
	}
	if !free {
		// A route that names no known group stays silent, as before:
		// there is nothing to name.
		if len(waits) > 0 {
			d.reportGroupWait(ctx, b, r, waits)
		}
		return c, false
	}
	choice := r
	if len(aside) > 0 {
		rest := withoutGroups(r, aside)
		choice = &rest
	}
	// The pass's own clock decides whether a window resets within a
	// comparison's duration, as it decides every other wait of the pass.
	// Each group is judged for the model the bead would start with in it,
	// so a per-model window used up holds the group that would stop on it.
	c = choice.ChooseAt(d.now(), func(group, source string) store.Quota {
		if source != "" {
			return d.sourceQuota(ctx, source)
		}
		return d.routeQuota(ctx, groups[group], def)
	}, func(group string) string {
		return modelFor(groups[group], b)
	})
	// The groups passed over for their hold head the choice's, in route
	// order: the claimed event, and a quota_wait, say why each was.
	if len(aside) > 0 {
		passed := make([]store.PassedGroup, 0, len(aside)+len(c.Passed))
		for _, w := range aside {
			passed = append(passed, store.PassedGroup{Group: w.Group, Reason: w.Reason})
		}
		c.Passed = append(passed, c.Passed...)
	}
	if c.Group != "" {
		return c, true
	}
	if ctx.Err() != nil {
		return c, false // the daemon is stopping: the provider was not heard out
	}
	// Once for a route, not for each percentage a window passes through.
	if d.noted(b.ID, "quota_wait", b.Stage+"|"+r.ID) {
		return c, false
	}
	data := map[string]any{"stage": b.Stage, "route": r.ID, "passed": c.Passed, "reason": c.Why()}
	hint := "the bead waits until a group's condition holds"
	if at := c.Resets(); at != nil {
		data["resets_at"] = at.UTC().Format(time.RFC3339)
		hint += fmt.Sprintf("; the first of those windows resets at %s", at.UTC().Format(time.RFC3339))
	}
	data["hint"] = hint + " (yardr limits; yardr route list)"
	d.Log.Warn("quota_wait", "bead", b.ID, "route", r.ID, "reason", c.Why())
	_ = d.Store.LogDispatch(ctx, b.ID, "quota_wait", actor, data)
	return c, false
}

// waitedGroup is a group that cannot start a bead now, with why in words.
// hold says the wait is the group's model hold (holdBack): the claim passes
// the group over for the route's next group instead of waiting for it (see
// chooseGroup). Every other wait is waited for.
type waitedGroup struct {
	Group  string
	Reason string
	hold   bool
}

// withoutGroups is r without the groups named in aside: choosing among the
// rest judges only them, in route order, with their conditions as they are.
// The wait marker stays where it was, and a group the yard no longer knows
// is judged as ever. aside never names every group: some group is free, and
// a group that is free is kept.
func withoutGroups(r *store.Route, aside []waitedGroup) store.Route {
	skip := make(map[string]bool, len(aside))
	for _, w := range aside {
		skip[w.Group] = true
	}
	var kept []store.RouteGroup
	for _, rg := range r.Groups() {
		if !skip[rg.Name] {
			kept = append(kept, rg)
		}
	}
	out := *r
	out.Group, out.When = kept[0].Name, kept[0].When
	out.Else = nil
	out.Else = append(out.Else, kept[1:]...)
	return out
}

// waitReason is why g cannot start b now: its limit, its runtime, or its
// model's hold, in the order the dispatcher checks them. It reports false
// when the group could start the bead.
func (d *Dispatcher) waitReason(g store.Group, b store.Bead, active map[string]int) (waitedGroup, bool) {
	if active[g.Name] >= g.Limit {
		if g.Limit == 0 {
			return waitedGroup{Group: g.Name, Reason: fmt.Sprintf("group %s is paused (limit 0); the bead starts when the limit is raised (yardr group set %s --limit <n>)", g.Name, g.Name)}, true
		}
		return waitedGroup{Group: g.Name, Reason: fmt.Sprintf("group %s runs as many sessions as its limit (%d) allows; the bead starts when one ends, or the limit is raised (yardr group set %s --limit <n>)", g.Name, g.Limit, g.Name)}, true
	}
	if o := d.down[g.Name]; o != nil && time.Now().Before(o.until) {
		return waitedGroup{Group: g.Name, Reason: fmt.Sprintf("group %s's runtime is down (see the group's runtime_down event); the bead starts when it is tried again", g.Name)}, true
	}
	// One start in flight per group, and no queue (starts.go): while its
	// worker is starting a session the group starts no other, and this
	// bead stays unclaimed until a later pass has drained that start's
	// completion and applied any outage it recorded.
	if d.startInFlight[g.Name] != "" {
		return waitedGroup{Group: g.Name, Reason: fmt.Sprintf("group %s is starting a session; the bead starts when that start finishes (yardr session list)", g.Name)}, true
	}
	// A session of the group sits stalled with this model, or waits
	// with it for a usage limit's window to reset: another would end
	// the same way (see holdBack).
	model := modelFor(g, b)
	if sid, held := d.paused[g.Name][model]; held {
		with := "the agent's default model"
		if model != "" {
			with = "model " + model
		}
		return waitedGroup{Group: g.Name, Reason: fmt.Sprintf("group %s starts no session with %s while session %s holds it back (stalled, or waiting for a usage window); the bead starts when that session ends, or with another model", g.Name, with, sid), hold: true}, true
	}
	return waitedGroup{}, false
}

// reportGroupWait logs one group_wait event for a bead its route's groups
// cannot start now: each group with its reason in words. Once per stage
// and route until the bead starts, as quota_wait is.
func (d *Dispatcher) reportGroupWait(ctx context.Context, b store.Bead, r *store.Route, waits []waitedGroup) {
	if d.noted(b.ID, "group_wait", b.Stage+"|"+r.ID) {
		return
	}
	groups := make([]map[string]any, 0, len(waits))
	reasons := make([]string, 0, len(waits))
	for _, w := range waits {
		groups = append(groups, map[string]any{"group": w.Group, "reason": w.Reason})
		reasons = append(reasons, w.Reason)
	}
	reason := strings.Join(reasons, "; ")
	d.Log.Warn("group_wait", "bead", b.ID, "route", r.ID, "reason", reason)
	_ = d.Store.LogDispatch(ctx, b.ID, "group_wait", actor, map[string]any{
		"stage": b.Stage, "route": r.ID, "groups": groups, "reason": reason,
		"hint": "the bead waits until a group of its route can start it (yardr session list; yardr group list)"})
}

// routeQuota is what the yard's limits source says of the provider the
// agents of g burn, as a route's condition reads it: the group's kind and
// provider setting resolve it, as they do for a harness that stopped, and
// the command runs under the group's env, so the condition gates on that
// login's windows. The kind is the one Claim gives the group's sessions
// (store.Group.Kind, def being the yard's default_kind), so the windows
// read are those the session burns; a group that starts no agents is asked
// for as before.
func (d *Dispatcher) routeQuota(ctx context.Context, g store.Group, def string) store.Quota {
	kind := g.Kind(def)
	if kind == "" {
		kind = agents.DefaultKind
	}
	provider, _ := g.Config["provider"].(string)
	env, err := groupEnv(g)
	if err != nil {
		return store.Quota{Unknown: err.Error()}
	}
	rd := d.cachedLimits(ctx, kind, provider, routeLimitsMemory, env)
	if rd.report == nil {
		return d.unknownQuota(rd)
	}
	return quotaOf(rd.report)
}

// sourceQuota is what the source named source says of itself, as a route's
// condition reads it: the comparisons that name a source read these windows
// whatever the group's provider is, so a group with no kind is gated the
// same way.
func (d *Dispatcher) sourceQuota(ctx context.Context, source string) store.Quota {
	rd := d.cachedSource(ctx, source, routeLimitsMemory)
	if rd.report == nil {
		return d.unknownQuota(rd)
	}
	return quotaOf(rd.report)
}

// unknownQuotaMemory is how long a route goes by what a provider last said
// when no fresh answer is known: a report younger than this keeps failing
// the comparisons it answers, as last known, so one stale read does not
// open every gate on a pass. Past it only a window used up until a reset
// still ahead holds (usedUpAhead), so a source that died stops holding the
// line ten minutes after its last word.
const unknownQuotaMemory = 10 * time.Minute

// unknownQuota is what a read with no fresh report tells a route: no fresh
// answer (its refresh already running), a provider that gave none, or
// nothing that answers. Choose fails open on it, as it always has, but for
// what the provider last said: a report younger than unknownQuotaMemory is
// kept whole as LastKnown, so a load last seen past the threshold, or a
// window last seen past its target, still fails the comparison, with the
// reason saying it is the last known. Past the bound only the windows the
// provider last said were used up until a reset still ahead are kept
// (usedUpAhead): a session started into one stops on the limit at once, so
// they go on failing the comparisons they answer however old the report is,
// until a fresh one replaces them. A key never answered, and a provider that
// failed before it ever answered, have none.
func (d *Dispatcher) unknownQuota(rd limitRead) store.Quota {
	q := store.Quota{Unknown: rd.unknown()}
	if rd.last != nil {
		now := d.now()
		if now.Sub(rd.last.AskedAt) < unknownQuotaMemory {
			q.Provider, q.LastKnown = rd.last.Provider, quotaWindows(rd.last.Windows)
		} else if used := usedUpAhead(rd.last, now); len(used) > 0 {
			q.Provider, q.LastKnown = rd.last.Provider, quotaWindows(used)
		}
	}
	return q
}

// quotaOf is a report as a route's condition reads it.
func quotaOf(r *limits.Report) store.Quota {
	return store.Quota{Provider: r.Provider, Windows: quotaWindows(r.Windows)}
}

// quotaWindows is windows as a route's condition reads them.
func quotaWindows(windows []limits.Window) []store.QuotaWindow {
	var out []store.QuotaWindow
	for _, w := range windows {
		qw := store.QuotaWindow{Label: w.Label, Model: w.Model, UsedPercent: w.UsedPercent, TargetPercent: w.TargetPercent, ResetsAt: w.ResetsAt}
		// A provider that counts instead says the same in two numbers.
		if qw.UsedPercent == nil && w.Used != nil && w.Limit != nil && *w.Limit > 0 {
			p := float64(*w.Used) * 100 / float64(*w.Limit)
			qw.UsedPercent = &p
		}
		out = append(out, qw)
	}
	return out
}
