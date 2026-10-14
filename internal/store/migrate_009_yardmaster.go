package store

import (
	bolt "go.etcd.io/bbolt"
)

// oldYardmasterSetting is the setting that named the yard's coordinating
// crew member until schema 9.
const oldYardmasterSetting = "mayor"

// migrateYardmaster renames the setting that names the yard's coordinating
// crew member, mayor until schema 9, to yardmaster. The crew member keeps its name,
// which is the operator's to give: a yard from before the setting whose
// crew member is called mayor gets the setting, so what defaulted to that name
// still finds it now that the default is yardmaster.
func migrateYardmaster(tx *bolt.Tx, from uint64) error {
	if from >= 9 {
		return nil
	}
	var name string
	had, err := get(tx, bSettings, oldYardmasterSetting, &name)
	if err != nil {
		return err
	}
	source := "setting " + oldYardmasterSetting
	if had {
		if err := tx.Bucket(bSettings).Delete([]byte(oldYardmasterSetting)); err != nil {
			return err
		}
	} else if tx.Bucket(bCrew).Get([]byte(oldYardmasterSetting)) != nil {
		name, source = oldYardmasterSetting, "crew member "+oldYardmasterSetting
	}
	if name == "" || tx.Bucket(bSettings).Get([]byte(YardmasterSetting)) != nil {
		return nil
	}
	if err := put(tx, bSettings, YardmasterSetting, name); err != nil {
		return err
	}
	return (&wtx{Tx: tx}).event("", "settings_migrated", "yardr",
		map[string]any{YardmasterSetting: name, "from": source})
}
