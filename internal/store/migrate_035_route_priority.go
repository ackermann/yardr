package store

import (
	"encoding/json"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/shared"
)

// migrateRoutePriority drops the priority every route carried until schema
// 35: routes are ranked by a named order of what they ask of a bead
// (SortRoutes), and the number is gone. Each route is rewritten without it,
// keeping its id, and no event is logged for that: what each route takes
// is what it was. Two routes of one scope that state one match key could
// stand while their priorities differed, and only the higher ever took a
// bead, since the two match the same beads; ranked by id they might trade
// places, so the one that never won is removed and told as route_removed
// from yard, with the route kept, at the reason. A store from after the
// add refused such a pair holds none.
func migrateRoutePriority(tx *bolt.Tx, from uint64) error {
	if from >= 35 {
		return nil
	}
	operator, err := operatorIn(tx)
	if err != nil {
		return err
	}
	type stored struct {
		Route
		Priority int `json:"priority"`
	}
	type scopeKey struct {
		own   bool
		depot string
		key   shared.Key
	}
	scopeOf := func(r Route) scopeKey {
		k := scopeKey{key: r.matchKey(operator)}
		if r.Depot != nil {
			k.own, k.depot = true, *r.Depot
		}
		return k
	}
	var all []stored
	winner := map[scopeKey]stored{}
	if err := tx.Bucket(bRoutes).ForEach(func(_, raw []byte) error {
		var r stored
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		all = append(all, r)
		sk := scopeOf(r.Route)
		// As the ranking before the change read two routes of one key:
		// the higher priority, then the id.
		if w, ok := winner[sk]; !ok || r.Priority > w.Priority || (r.Priority == w.Priority && r.ID < w.ID) {
			winner[sk] = r
		}
		return nil
	}); err != nil {
		return err
	}
	w := &wtx{Tx: tx}
	for _, r := range all {
		if win := winner[scopeOf(r.Route)]; win.ID != r.ID {
			if err := tx.Bucket(bRoutes).Delete([]byte(r.ID)); err != nil {
				return err
			}
			data := routeEvent(r.Route)
			data["reason"] = "route " + win.ID + " takes the same beads in the same scope, and ranked above it by priority, which routes no longer carry"
			if err := w.event("", "route_removed", "yard", data); err != nil {
				return err
			}
			continue
		}
		if err := put(tx, bRoutes, r.ID, r.Route); err != nil {
			return err
		}
	}
	return nil
}
