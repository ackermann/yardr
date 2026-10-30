package store

import (
	bolt "go.etcd.io/bbolt"
)

// migrateSettings gives a yard from before the onboarding (yard-p01r) the
// default_kind setting that marks a home set up: a store with depots, groups
// or crew members but no default_kind is in use, and without it every command
// would onboard it again (yard-w6np). The kind is the first herdr group's,
// else the first herdr crew member's, else claude. The operator setting stays
// unset: role prompts already installed keep the name they have.
func migrateSettings(tx *bolt.Tx) error {
	if tx.Bucket(bSettings).Get([]byte("default_kind")) != nil {
		return nil
	}
	inUse := false
	for _, b := range [][]byte{bDepots, bGroups, bCrew} {
		if k, _ := tx.Bucket(b).Cursor().First(); k != nil {
			inUse = true
		}
	}
	if !inUse {
		return nil
	}
	kind, from := "", ""
	if err := each(tx, bGroups, "", func(_ []byte, g *Group) error {
		if k, ok := g.Config["kind"].(string); ok && k != "" && g.Runner == "herdr" && kind == "" {
			kind, from = k, "group "+g.Name
		}
		return nil
	}); err != nil {
		return err
	}
	if err := each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		if k, ok := r.Config["kind"].(string); ok && k != "" && r.Runner == "herdr" && kind == "" {
			kind, from = k, "crew member "+r.Name
		}
		return nil
	}); err != nil {
		return err
	}
	if kind == "" {
		kind, from = "claude", "default"
	}
	if err := put(tx, bSettings, "default_kind", kind); err != nil {
		return err
	}
	return (&wtx{Tx: tx}).event("", "settings_migrated", "yardr",
		map[string]any{"default_kind": kind, "from": from})
}
