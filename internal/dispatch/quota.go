package dispatch

import (
	"context"
	"fmt"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
)

// A route may name several groups and put a condition on the windows of a
// group's provider (store.Route.Choose): "yardr-builders while Claude's
// Weekly window is under 80% used, else muse-builders". The store holds
// the route and makes the comparison; what the windows are is learned
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
// The limits command is run only for a route with a condition, and only
// when one of the route's groups could start a bead now: a bead that would
// wait anyway (every group at its limit, its runtime down) asks nothing.
// When no group's condition holds the bead waits, and says so in one
// quota_wait event until it starts.
//
// def is the yard's default_kind setting, for a group that names no kind.
func (d *Dispatcher) chooseGroup(ctx context.Context, b store.Bead, r *store.Route, groups map[string]store.Group, active map[string]int, def string) (c store.RouteChoice, ok bool) {
	if !r.Quotaed() {
		return r.Choose(nil), true
	}
	free := false
	for _, rg := range r.Groups() {
		g, known := groups[rg.Name]
		o := d.down[rg.Name]
		if known && active[g.Name] < g.Limit && !(o != nil && time.Now().Before(o.until)) {
			free = true
		}
	}
	if !free {
		return c, false
	}
	c = r.Choose(func(group string) store.Quota { return d.routeQuota(ctx, groups[group], def) })
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

// routeQuota is what the yard's limits source says of the provider the
// agents of g burn, as a route's condition reads it: the group's kind and
// provider setting resolve it, as they do for a harness that stopped. The
// kind is the one Claim gives the group's sessions (store.Group.Kind, def
// being the yard's default_kind), so the windows read are those the session
// burns; a group that starts no agents is asked for as before.
func (d *Dispatcher) routeQuota(ctx context.Context, g store.Group, def string) store.Quota {
	kind := g.Kind(def)
	if kind == "" {
		kind = agents.DefaultKind
	}
	provider, _ := g.Config["provider"].(string)
	r, err := d.askLimits(ctx, kind, provider, routeLimitsMemory)
	if err != nil {
		return store.Quota{Unknown: err.Error()}
	}
	q := store.Quota{Provider: r.Provider}
	for _, w := range r.Windows {
		qw := store.QuotaWindow{Label: w.Label, UsedPercent: w.UsedPercent, ResetsAt: w.ResetsAt}
		// A provider that counts instead says the same in two numbers.
		if qw.UsedPercent == nil && w.Used != nil && w.Limit != nil && *w.Limit > 0 {
			p := float64(*w.Used) * 100 / float64(*w.Limit)
			qw.UsedPercent = &p
		}
		q.Windows = append(q.Windows, qw)
	}
	return q
}
