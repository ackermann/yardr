package dispatch

import (
	"context"
	"time"

	"yardr.dev/yardr/internal/store"
)

// HookQuietEvery is how often the daemon judges its hooks quiet: the usual
// gap floors at an hour, so looking more often finds nothing sooner.
const HookQuietEvery = time.Hour

// quietTold caps the hook_quiet events one look reads to tell whether the
// spell was told of: one is logged per spell, so a hundred spells back is
// far enough.
const quietTold = 100

// quietHooks logs hook_quiet for each hook that used to deliver and has
// delivered nothing for longer than its usual gap (store.Quiet): the
// webhook is off at the source, or cannot reach this yard. It is its own
// memory, as a waited bead's stay is: a hook_quiet logged since the hook
// last accepted a delivery means the spell was told of, so a daemon that
// replaces itself does not tell again, and a hook that delivers and goes
// quiet again is told of anew.
func (d *Dispatcher) quietHooks(ctx context.Context) {
	now := d.now()
	if !d.quietChecked.IsZero() && now.Sub(d.quietChecked) < HookQuietEvery {
		return
	}
	hooks, err := d.Store.Hooks(ctx)
	if err != nil {
		d.Log.Warn("quiet hooks", "err", err)
		return
	}
	var journal []store.Hook
	for _, h := range hooks {
		if h.Accepted != nil && h.Blocked == "" && !h.LastRefused() {
			journal = append(journal, h)
		}
	}
	if len(journal) == 0 {
		d.quietChecked = now
		return
	}
	delivered, err := d.Store.EventsOfKind(ctx, "hook", store.QuietRead)
	if err != nil {
		d.Log.Warn("quiet hooks", "err", err)
		return
	}
	at := map[string][]time.Time{}
	for _, e := range delivered {
		if name, _ := e.Data["hook"].(string); name != "" {
			at[name] = append(at[name], e.At)
		}
	}
	told, err := d.Store.EventsOfKind(ctx, store.HookQuiet, quietTold)
	if err != nil {
		d.Log.Warn("quiet hooks", "err", err)
		return
	}
	heard := map[string]time.Time{}
	for _, e := range told {
		if name, _ := e.Data["hook"].(string); name != "" {
			if was, ok := heard[name]; !ok || e.At.After(was) {
				heard[name] = e.At
			}
		}
	}
	failed := false
	for _, h := range journal {
		is, gap := store.Quiet(h.Accepted, at[h.Name], now)
		if !is {
			continue
		}
		if was, ok := heard[h.Name]; ok && !was.Before(*h.Accepted) {
			continue
		}
		d.Log.Warn("hook quiet", "hook", h.Name, "source", h.Source,
			"last", h.Accepted.UTC().Format(time.RFC3339), "gap", round(gap).String())
		if err := d.Store.LogDispatch(ctx, "", store.HookQuiet, actor, map[string]any{
			"hook": h.Name, "source": h.Source,
			"last": h.Accepted.UTC().Format(time.RFC3339), "gap": round(gap).String()}); err != nil {
			d.Log.Warn(store.HookQuiet+" event", "hook", h.Name, "err", err)
			failed = true
		}
	}
	// A transient read or write failure must be retried on the next pass,
	// rather than waiting another hour for the next quiet-hook check.
	if !failed {
		d.quietChecked = now
	}
}
