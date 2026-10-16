package store

import (
	bolt "go.etcd.io/bbolt"
)

// migrateUpgradeFromDangling clears an upgrade_from setting that names no
// depot of the yard: setting it to such a name, and removing the depot it
// names, are now refused, so a yard from before either refusal can hold a
// pointer the next upgrade would only fail on ("not in the yard"). The
// clearing is told as the setting's own change, from yard, naming the depot
// that was not there: clearing it stops the automatic install, which never
// happens untold. A yard whose setting names a depot keeps it.
func migrateUpgradeFromDangling(tx *bolt.Tx, from uint64) error {
	if from >= 25 {
		return nil
	}
	var name string
	ok, err := get(tx, bSettings, UpgradeFromSetting, &name)
	if err != nil || !ok {
		return err
	}
	if tx.Bucket(bDepots).Get([]byte(name)) != nil {
		return nil
	}
	if err := tx.Bucket(bSettings).Delete([]byte(UpgradeFromSetting)); err != nil {
		return err
	}
	return (&wtx{Tx: tx}).event("", "setting_changed", "yard",
		map[string]any{"key": UpgradeFromSetting, "value": "",
			"reason": "cleared: named depot " + name + ", which the yard does not have"})
}
