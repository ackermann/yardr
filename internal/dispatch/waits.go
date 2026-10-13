package dispatch

import (
	"context"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Waited is the event raised on a bead that has been in a stage of a manual
// group for longer than the group's `wait`. Nothing runs in a manual group,
// so no session of it goes idle or ages: without this a bead nobody was
// told of waits unseen. It is of the class attention, and it is its own
// memory: one in the bead's log since the bead entered its stage means the
// stay was told of, so a daemon that replaces itself does not tell again,
// and a bead that leaves the stage and comes back is told of anew.
const Waited = "waited"

// stay is what the dispatcher remembers of a bead that waits in a manual
// group with a wait: when it entered its stage and whether that was told
// of, as its log had it at rev. The log is read again only when the bead
// has changed, not on every pass.
type stay struct {
	rev   string
	since time.Time
	told  bool
}

// waits raises Waited, once per stay, on each open bead that has been
// longer than its manual group's wait in the stage it is in, by the
// dispatcher's clock. The group is the one that holds the bead, else the
// one its stage is routed to: a manual group at its limit leaves beads
// waiting unclaimed. A group that sets no wait raises nothing.
func (d *Dispatcher) waits(ctx context.Context, groups map[string]store.Group) {
	limits := map[string]time.Duration{}
	for _, g := range groups {
		if g.Runner != "manual" {
			continue
		}
		// The store took the value as ParseSpan reads it; one from before
		// config was checked that does not parse is no wait.
		if s, _ := g.Config["wait"].(string); s != "" {
			if wait, err := store.ParseSpan(s); err == nil {
				limits[g.Name] = wait
			}
		}
	}
	if len(limits) == 0 {
		d.stays = nil
		return
	}
	beads, err := d.Store.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		// The next pass tries again: a bead is told of late rather than not.
		d.Log.Warn("waiting beads", "err", err)
		return
	}
	var routes []store.Route
	routed := false
	now := d.now()
	stays := map[string]stay{}
	for _, b := range beads {
		group := ""
		if b.Group != nil {
			group = *b.Group
		} else {
			if !routed {
				if routes, err = d.Store.Routes(ctx); err != nil {
					d.Log.Warn("waiting beads", "err", err)
					return
				}
				SortRoutes(routes)
				routed = true
			}
			if r := Match(routes, b); r != nil {
				group = r.Worker()
			}
		}
		// A bead younger than the wait has not been anywhere that long, and
		// its log is not read to find out.
		wait, ok := limits[group]
		if !ok || now.Sub(b.CreatedAt) < wait {
			continue
		}
		st, known := d.stays[b.ID]
		if !known || st.rev != b.Revision {
			evs, err := d.Store.Events(ctx, b.ID, 1000)
			if err != nil {
				d.Log.Warn("waiting bead", "bead", b.ID, "err", err)
				continue
			}
			st = stayOf(evs, b)
		}
		if waited := now.Sub(st.since); !st.told && waited >= wait {
			d.Log.Warn("bead waited", "bead", b.ID, "group", group, "stage", b.Stage, "waited", round(waited))
			err := d.Store.LogDispatch(ctx, b.ID, Waited, actor, map[string]any{"group": group, "stage": b.Stage,
				"since": st.since.UTC().Format(time.RFC3339), "waited": round(waited).String()})
			if err != nil {
				d.Log.Warn(Waited+" event", "bead", b.ID, "err", err)
			}
			// One that could not be logged is tried again on the next pass.
			st.told = err == nil
		}
		stays[b.ID] = st
	}
	d.stays = stays
}

// stayOf reads b's stay in its stage from its log, evs, oldest first: when
// the bead entered the stage, and whether a Waited was raised since. A bead
// whose log no longer reaches back to that (pruned, or longer than was
// read) has been there since it was made, as far as anyone can tell.
func stayOf(evs []store.Event, b store.Bead) stay {
	st := stay{rev: b.Revision, since: b.CreatedAt}
	for i := len(evs) - 1; i >= 0; i-- {
		switch e := evs[i]; e.Kind {
		case Waited:
			st.told = true
		case "advanced", "reopened", "created":
			st.since = e.At
			return st
		}
	}
	return st
}
