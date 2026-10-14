package store

import (
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// oldCheckFields are the fields of a depot's record that said, until schema
// 19, what yardr ran before a bead of the depot landed, and
// oldCheckSlotsSetting the setting that said how many of those runs the
// machine took at once.
var oldCheckFields = []string{"check", "check_light", "light_slot", "allow_no_check"}

const oldCheckSlotsSetting = "gate_slots"

// migrateChecks drops what a store held, until schema 19, of the checks
// yardr ran itself: oldCheckFields from each depot's record and the setting
// oldCheckSlotsSetting. A depot whose check was a command in the store and
// that has no test command gets that command as its test command: it is
// what tested the depot, and whoever lands its beads now is given it as
// YARDR_TEST. One event says what each depot lost and which got a test
// command; a store that held none of it is written nothing.
func migrateChecks(tx *bolt.Tx, from uint64) error {
	if from >= 19 {
		return nil
	}
	dropped, tests := map[string]any{}, map[string]any{}
	records := map[string]map[string]json.RawMessage{}
	err := tx.Bucket(bDepots).ForEach(func(k, raw []byte) error {
		rec := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("depot %s: %w", k, err)
		}
		was := map[string]any{}
		for _, f := range oldCheckFields {
			if v, ok := rec[f]; ok {
				var held any
				if err := json.Unmarshal(v, &held); err != nil {
					return fmt.Errorf("depot %s: %s: %w", k, f, err)
				}
				was[f] = held
				delete(rec, f)
			}
		}
		if len(was) == 0 {
			return nil
		}
		check, _ := was["check"].(string)
		var test string
		if v, ok := rec["test"]; ok {
			if err := json.Unmarshal(v, &test); err != nil {
				return fmt.Errorf("depot %s: test: %w", k, err)
			}
		}
		if check != "" && test == "" {
			rec["test"], _ = json.Marshal(check)
			tests[string(k)] = check
		}
		dropped[string(k)], records[string(k)] = was, rec
		return nil
	})
	if err != nil {
		return err
	}
	for k, rec := range records {
		if err := put(tx, bDepots, k, rec); err != nil {
			return err
		}
	}
	data := map[string]any{}
	if len(dropped) > 0 {
		data["dropped"] = dropped
	}
	if len(tests) > 0 {
		data["test"] = tests
	}
	var slots string
	if had, err := get(tx, bSettings, oldCheckSlotsSetting, &slots); err != nil {
		return err
	} else if had {
		if err := tx.Bucket(bSettings).Delete([]byte(oldCheckSlotsSetting)); err != nil {
			return err
		}
		data[oldCheckSlotsSetting] = slots
	}
	if len(data) == 0 {
		return nil
	}
	data["from"] = "checks"
	return (&wtx{Tx: tx}).event("", "depots_migrated", "yardr", data)
}
