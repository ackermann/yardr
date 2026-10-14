package dispatch

import (
	"context"
	"time"

	"yardr.dev/yardr/internal/store"
)

// HookQuietEvery is how often the daemon judges its hooks quiet: the usual
// gap floors at an hour, so looking more often finds nothing sooner.
const HookQuietEvery = time.Hour

// quietHooks logs hook_quiet for each hook that used to deliver and has
// delivered nothing for longer than its usual gap (store.QuietGaps): the
// webhook is off at the source, or cannot reach this yard. It is its own
// memory, as a waited bead's stay is: a hook_quiet logged since the hook
// last accepted a delivery means the spell was told of, so a daemon that
// replaces itself does not tell again, and a hook that delivers and goes
// quiet again is told of anew. Tells are read per hook, so one hook's many
// tells never crowd another's out.
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
	gaps, err := store.QuietGaps(ctx, d.Store, hooks, now)
	if err != nil {
		d.Log.Warn("quiet hooks", "err", err)
		return
	}
	if len(gaps) == 0 {
		d.quietChecked = now
		return
	}
	byName := map[string]store.Hook{}
	for _, h := range hooks {
		byName[h.Name] = h
	}
	failed := false
	for name, gap := range gaps {
		h := byName[name]
		told, err := d.Store.EventsOfHook(ctx, store.HookQuiet, name, 1)
		if err != nil {
			d.Log.Warn("quiet hooks", "hook", name, "err", err)
			failed = true
			continue
		}
		if !told.Complete {
			// The walk did not reach the hook's tells: unknown whether
			// the spell was told of. Skip this pass and retry the next,
			// rather than tell twice.
			failed = true
			continue
		}
		if len(told.Events) == 1 && !told.Events[0].At.Before(*h.Accepted) {
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
