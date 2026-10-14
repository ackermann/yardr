package store

import (
	bolt "go.etcd.io/bbolt"
)

// bLegacyFlows held flows before they had names (yard-9cza): one per depot
// and one per type in a depot, keyed <depot>\x00<type>.
var bLegacyFlows = []byte("flows")

// migrateFlows turns each depot's flow into the named flow <depot> and each
// type flow into <depot>.<type>, with the depot and type pointing at them, so
// every bead keeps the lifecycle it had. It runs once: the old bucket goes.
func migrateFlows(tx *bolt.Tx) error {
	if tx.Bucket(bLegacyFlows) == nil {
		return nil
	}
	type legacy struct {
		Flow
		Depot string `json:"rig"` // the word then
		Type  string `json:"type"`
	}
	var rows []*legacy
	if err := each(tx, bLegacyFlows, "", func(_ []byte, l *legacy) error {
		rows = append(rows, l)
		return nil
	}); err != nil {
		return err
	}
	var names []string
	for _, l := range rows {
		f := l.Flow
		f.Name = l.Depot
		if l.Type != "" {
			f.Name += "." + l.Type
		}
		if err := put(tx, bFlows, f.Name, f); err != nil {
			return err
		}
		if err := put(tx, bFlowPtrs, join(l.Depot, l.Type), FlowPointer{Depot: l.Depot, Type: l.Type, Flow: f.Name}); err != nil {
			return err
		}
		names = append(names, f.Name)
	}
	if err := tx.DeleteBucket(bLegacyFlows); err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	return (&wtx{Tx: tx}).event("", "flows_migrated", "yardr", map[string]any{"flows": names})
}
