package store

import bolt "go.etcd.io/bbolt"

// migrateOperatorRole gives the user the operator setting names the
// operator role (schema 30): before roles the setting was the one
// operator, and every other user reads as a member, which a record with
// no role is. A setting naming a crew member, or nobody, makes nobody an
// operator, as it held nobody to the rule before.
func migrateOperatorRole(tx *bolt.Tx, from uint64) error {
	if from >= 30 {
		return nil
	}
	operator, err := operatorIn(tx)
	if err != nil || operator == "" {
		return err
	}
	var u User
	if ok, err := get(tx, bUsers, operator, &u); err != nil || !ok {
		return err
	}
	u.Role = RoleOperator
	return put(tx, bUsers, operator, &u)
}
