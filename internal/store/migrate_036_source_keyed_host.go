package store

import (
	bolt "go.etcd.io/bbolt"
)

// migrateSourceKeyedHost pairs, once, each source whose credential is
// already stored with the host its calls go to (Source.KeyedHost, from
// schema 36). Until then no record said where a credential was given for, so
// holding every keyed source until its operator typed each secret again
// would stop the whole yard at the upgrade. A source with an env_file is
// paired as KeySource and the operator's own settings pair one: with the host
// of its declaration under the base setting it has. A source with no
// env_file holds no credential and stays unpaired, and so does one whose
// declaration this build cannot read: no call of it is made either. The
// pairing is a record of the declaration's own public address, so no event
// is logged for it.
func migrateSourceKeyedHost(tx *bolt.Tx, from uint64) error {
	if from >= 36 {
		return nil
	}
	var paired []*Source
	if err := each(tx, bSources, "", func(_ []byte, src *Source) error {
		d, err := src.Declaration()
		if err != nil {
			return nil
		}
		src.pairCredential(d)
		if src.KeyedHost != "" {
			paired = append(paired, src)
		}
		return nil
	}); err != nil {
		return err
	}
	// Written after the walk: a bucket is not changed while it is read.
	for _, src := range paired {
		if err := put(tx, bSources, src.Name, src); err != nil {
			return err
		}
	}
	return nil
}
