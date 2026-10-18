package store

import (
	bolt "go.etcd.io/bbolt"
)

// migrateHearer names the yard's hearer (schema 27): the crew member an
// approved claim subscribes to the claimed address beside writing it. A
// yard from before the setting keeps hearing through its yardmaster: the
// stored yardmaster setting where one is set, else the crew member
// yardmaster where one is there, else unset, and an approval then writes
// the address alone, as before.
func migrateHearer(tx *bolt.Tx, from uint64) error {
	if from >= 27 {
		return nil
	}
	if tx.Bucket(bSettings).Get([]byte(HearerSetting)) != nil {
		return nil
	}
	var name string
	had, err := get(tx, bSettings, YardmasterSetting, &name)
	if err != nil {
		return err
	}
	source := "setting " + YardmasterSetting
	if !had || name == "" {
		name, source = "yardmaster", "crew member yardmaster"
		if tx.Bucket(bCrew).Get([]byte(name)) == nil {
			return nil
		}
	}
	if err := put(tx, bSettings, HearerSetting, name); err != nil {
		return err
	}
	return (&wtx{Tx: tx}).event("", "settings_migrated", "yardr",
		map[string]any{HearerSetting: name, "from": source})
}
