package store

import (
	"context"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// bSettings holds the yard's own defaults, key -> value (a JSON string),
// such as default_kind, the agent kind a herdr group or crew member gets when
// its config names none. Which keys exist and what they accept is the
// CLI's business; the store keeps strings.
var bSettings = []byte("settings")

func init() { allBuckets = append(allBuckets, bSettings) }

// YardmasterSetting names the crew member that coordinates the yard, whom the
// role prompts tell agents to mail. Until schema 9 the key was mayor
// (migrateYardmaster), and SettingKey still reads that for this release.
const (
	YardmasterSetting    = "yardmaster"
	oldYardmasterSetting = "mayor"
)

// SettingKey is the key a setting is stored under: key itself, or the key
// that replaced it when it is one an earlier release used.
func SettingKey(key string) string {
	if key == oldYardmasterSetting {
		return YardmasterSetting
	}
	return key
}

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

// SetSetting sets key to value; an empty value removes it. A key an earlier
// release used sets the one that replaced it (SettingKey). The operator is
// a user (see User), so naming one adds the user when there is none. A
// crew member's name is refused: mail to a name reaches one party. A yard that
// has a crew member named as its operator already keeps its setting as it is.
func (s *Store) SetSetting(ctx context.Context, key, value, actor string) error {
	key = SettingKey(key)
	if err := checkName("setting", key); err != nil {
		return err
	}
	if key == OperatorSetting && value != "" {
		if err := checkUserName(value); err != nil {
			return fmt.Errorf("operator: %w", err)
		}
	}
	return s.write(func(w *wtx) error {
		if key == OperatorSetting && value != "" && w.Bucket(bCrew).Get([]byte(value)) != nil {
			if was, err := operatorIn(w.Tx); err != nil {
				return err
			} else if was != value {
				return fmt.Errorf("%s %q: that is a crew member's name, and mail to a name reaches one party: "+
					"choose another name for the operator, or remove the crew member first (yardr crew rm %s)", key, value, value)
			}
		}
		var err error
		if value == "" {
			err = w.Bucket(bSettings).Delete([]byte(key))
		} else {
			err = put(w.Tx, bSettings, key, value)
		}
		if err != nil {
			return err
		}
		if key == OperatorSetting {
			if err := w.ensureOperator(actor); err != nil {
				return err
			}
		}
		return w.event("", "setting_changed", actor, map[string]any{"key": key, "value": value})
	})
}
