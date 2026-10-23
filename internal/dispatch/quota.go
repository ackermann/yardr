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
// alone aside, store.QuotaWindow), so a plain route is gated too. The store
// holds the route and makes the comparison; what the windows are is learned
// here, from the yard's limits source, because only the daemon may run its
// command (harness_limits.go says why it is not leaned on).

// routeLimitsMemory is how old an answer of a provider may be for a route's
// condition: the command runs at most twelve times an hour for a provider,
// however many beads are ready. A group may so start a session or two
// after its window crossed the threshold, which is within the margin a
// threshold below 100 gives.
const routeLimitsMemory = 5 * time.Minute

// chooseGroup is the group of r that takes b: the first whose quota
// condition holds, with the groups passed over. ok is false when the bead
// does not start in this pass.
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
		return r.Choose(nil), true
	}
	free := false
	var waits []waitedGroup
	for _, rg := range r.Groups() {
		g, known := groups[rg.Name]
		if !known {
			continue
		}
		if w, ok := d.waitReason(g, b, active); ok {
			waits = append(waits, w)
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
	c = r.Choose(func(group, source string) store.Quota {
		if source != "" {
			return d.sourceQuota(ctx, source)
		}
		return d.routeQuota(ctx, groups[group], def)
	})
	if c.Group != "" {
		return c, true
	}
	if ctx.Err() != nil {
		return c, false // the daemon is stopping: the provider was not heard out
	}
	// Once for a route, not for each percentage a window passes through.
	key := "quota_wait|" + b.Stage + "|" + r.ID
	if d.reported[b.ID] == key {
		return c, false
	}
	d.reported[b.ID] = key
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
type waitedGroup struct {
	Group  string
	Reason string
}

// waitReason is why g cannot start b now: its limit, its runtime, or its
// model's hold, in the order the dispatcher checks them. It reports false
// when the group could start the bead.
func (d *Dispatcher) waitReason(g store.Group, b store.Bead, active map[string]int) (waitedGroup, bool) {
	if active[g.Name] >= g.Limit {
		if g.Limit == 0 {
			return waitedGroup{g.Name, fmt.Sprintf("group %s is paused (limit 0); the bead starts when the limit is raised (yardr group set %s --limit <n>)", g.Name, g.Name)}, true
		}
		return waitedGroup{g.Name, fmt.Sprintf("group %s runs as many sessions as its limit (%d) allows; the bead starts when one ends, or the limit is raised (yardr group set %s --limit <n>)", g.Name, g.Limit, g.Name)}, true
	}
	if o := d.down[g.Name]; o != nil && time.Now().Before(o.until) {
		return waitedGroup{g.Name, fmt.Sprintf("group %s's runtime is down (see the group's runtime_down event); the bead starts when it is tried again", g.Name)}, true
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
		return waitedGroup{g.Name, fmt.Sprintf("group %s starts no session with %s while session %s holds it back (stalled, or waiting for a usage window); the bead starts when that session ends, or with another model", g.Name, with, sid)}, true
	}
	return waitedGroup{}, false
}

// reportGroupWait logs one group_wait event for a bead its route's groups
// cannot start now: each group with its reason in words. Once per stage
// and route until the bead starts, as quota_wait is.
func (d *Dispatcher) reportGroupWait(ctx context.Context, b store.Bead, r *store.Route, waits []waitedGroup) {
	key := "group_wait|" + b.Stage + "|" + r.ID
	if d.reported[b.ID] == key {
		return
	}
	d.reported[b.ID] = key
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
	r, err := d.askLimits(ctx, kind, provider, routeLimitsMemory, env)
	if err != nil {
		return store.Quota{Unknown: err.Error()}
	}
	return quotaOf(r)
}

// sourceQuota is what the source named source says of itself, as a route's
// condition reads it: the comparisons that name a source read these windows
// whatever the group's provider is, so a group with no kind is gated the
// same way.
func (d *Dispatcher) sourceQuota(ctx context.Context, source string) store.Quota {
	r, err := d.askSource(ctx, source, routeLimitsMemory)
	if err != nil {
		return store.Quota{Unknown: err.Error()}
	}
	return quotaOf(r)
}

// quotaOf is a report as a route's condition reads it.
func quotaOf(r *limits.Report) store.Quota {
	q := store.Quota{Provider: r.Provider}
	for _, w := range r.Windows {
		qw := store.QuotaWindow{Label: w.Label, Model: w.Model, UsedPercent: w.UsedPercent, TargetPercent: w.TargetPercent, ResetsAt: w.ResetsAt}
		// A provider that counts instead says the same in two numbers.
		if qw.UsedPercent == nil && w.Used != nil && w.Limit != nil && *w.Limit > 0 {
			p := float64(*w.Used) * 100 / float64(*w.Limit)
			qw.UsedPercent = &p
		}
		q.Windows = append(q.Windows, qw)
	}
	return q
}
