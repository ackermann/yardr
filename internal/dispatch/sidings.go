package dispatch

import (
	"context"

	"yardr.dev/yardr/internal/store"
)

// sidings folds the sidings that are ready by the dispatcher's clock
// (store.FoldSidings), and notes when the next is due so Run wakes for it.
// A siding that is not ready costs the pass its share of the one listing;
// one that is ready folds on the write that made it so, or at its due.
func (d *Dispatcher) sidings(ctx context.Context, groups map[string]store.Group, beads []store.Bead, routes []store.Route) {
	siding := false
	for _, g := range groups {
		if _, ok := store.SidingOf(g); ok {
			siding = true
			break
		}
	}
	if !siding {
		d.sidingDue = nil
		return
	}
	pass, err := d.Store.FoldSidings(ctx, d.now(), beads, routes)
	if err != nil {
		// The next pass tries again: a siding folds late rather than not.
		d.Log.Warn("sidings", "err", err)
		return
	}
	d.sidingDue = pass.Next
	if len(pass.Folded) > 0 {
		advanced := 0
		for _, f := range pass.Folded {
			if f.Advanced {
				advanced++
			}
		}
		d.Log.Info("sidings", "folded", len(pass.Folded)-advanced, "advanced", advanced)
	}
}

// passLists lists the open beads and the yard's routes once for the pass,
// for waits and sidings to share, with the operator their routes match
// against. With neither a wait nor a siding configured it lists nothing.
// ok is false when a listing failed: the pass skips both steps, and the
// next pass tries again, so a bead is told of and a siding folds late
// rather than not.
func (d *Dispatcher) passLists(ctx context.Context, groups map[string]store.Group) (beads []store.Bead, routes []store.Route, operator string, ok bool) {
	waits, sidings := false, false
	for _, g := range groups {
		if g.Runner != "manual" {
			continue
		}
		if s, _ := g.Config["wait"].(string); s != "" {
			waits = true
		}
		if _, ok := store.SidingOf(g); ok {
			sidings = true
		}
	}
	if !waits && !sidings {
		return nil, nil, "", true
	}
	var err error
	if beads, err = d.Store.ListBeads(ctx, store.BeadFilter{}); err != nil {
		d.Log.Warn("waiting beads", "err", err)
		return nil, nil, "", false
	}
	if !sidings {
		claimed := true
		for _, b := range beads {
			if b.Group == nil {
				claimed = false
				break
			}
		}
		if claimed {
			return beads, nil, "", true
		}
	}
	if routes, err = d.Store.Routes(ctx); err != nil {
		d.Log.Warn("waiting beads", "err", err)
		return nil, nil, "", false
	}
	settings, err := d.Store.Settings(ctx)
	if err != nil {
		d.Log.Warn("waiting beads", "err", err)
		return nil, nil, "", false
	}
	return beads, routes, settings[store.OperatorSetting], true
}
