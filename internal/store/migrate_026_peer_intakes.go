package store

import (
	"encoding/json"

	bolt "go.etcd.io/bbolt"
)

// migratePeerIntakes turns the one intake a peer's record holds (schema 25)
// into a list of one, marked as the default: a link holds an intake per
// depot now, and a bead that names no remote goes to the default one. A
// peer with no intake keeps none.
func migratePeerIntakes(tx *bolt.Tx, from uint64) error {
	if from >= 26 {
		return nil
	}
	b := tx.Bucket(bPeers)
	if b == nil {
		return nil
	}
	changes := map[string][]byte{}
	err := b.ForEach(func(k, raw []byte) error {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		old, ok := m["intake"]
		if !ok || m["intakes"] != nil {
			return nil
		}
		delete(m, "intake")
		var check struct {
			Depot string `json:"depot"`
			Flow  string `json:"flow"`
		}
		var in map[string]json.RawMessage
		if string(old) == "null" || json.Unmarshal(old, &check) != nil || check.Depot == "" || check.Flow == "" ||
			json.Unmarshal(old, &in) != nil {
			out, err := json.Marshal(m)
			if err != nil {
				return err
			}
			changes[string(k)] = out
			return nil
		}
		// By the JSON as stored, not by this build's type: what the
		// intake holds besides is kept as it is.
		in["default"] = json.RawMessage("true")
		one, err := json.Marshal(in)
		if err != nil {
			return err
		}
		m["intakes"] = json.RawMessage("[" + string(one) + "]")
		out, err := json.Marshal(m)
		if err != nil {
			return err
		}
		changes[string(k)] = out
		return nil
	})
	if err != nil {
		return err
	}
	for k, v := range changes {
		if err := b.Put([]byte(k), v); err != nil {
			return err
		}
	}
	return nil
}
