package store

import (
	"slices"

	bolt "go.etcd.io/bbolt"
)

// migrateUpgradeTell subscribes the operator's user to the automatic
// install's failure kinds, for yards from before the operator carried it
// (ensureOperator adds it with a user it makes). Yards with no operator
// user are left alone: one made later carries it from its creation.
func migrateUpgradeTell(tx *bolt.Tx, from uint64) error {
	if from >= 20 {
		return nil
	}
	operator, err := operatorIn(tx)
	if err != nil || operator == "" {
		return err
	}
	var u User
	ok, err := get(tx, bUsers, operator, &u)
	if err != nil || !ok {
		return err
	}
	want := upgradeFailureSub()
	if slices.ContainsFunc(u.Subscriptions, want.same) {
		return nil
	}
	u.Subscriptions = append(u.Subscriptions, want)
	return put(tx, bUsers, operator, &u)
}
