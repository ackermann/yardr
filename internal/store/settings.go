package store

import (
	"context"

	bolt "go.etcd.io/bbolt"
)

// bSettings holds the yard's own defaults, key -> value (a JSON string),
// such as default_kind, the agent kind a herdr group or resident gets when
// its config names none. Which keys exist and what they accept is the
// CLI's business; the store keeps strings.
var bSettings = []byte("settings")

func init() { allBuckets = append(allBuckets, bSettings) }

// Settings returns every setting.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bSettings, "", func(k []byte, v *string) error {
			out[string(k)] = *v
			return nil
		})
	})
	return out, err
}

// SetSetting sets key to value; an empty value removes it.
func (s *Store) SetSetting(ctx context.Context, key, value, actor string) error {
	if err := checkName("setting", key); err != nil {
		return err
	}
	return s.write(func(w *wtx) error {
		var err error
		if value == "" {
			err = w.Bucket(bSettings).Delete([]byte(key))
		} else {
			err = put(w.Tx, bSettings, key, value)
		}
		if err != nil {
			return err
		}
		return w.event("", "setting_changed", actor, map[string]any{"key": key, "value": value})
	})
}
