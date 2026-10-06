package store

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/config"
)

// Until schema 12 a depot was a rig (migrateDepotRecords, migrateDepots).
// The old word is still read for this release wherever it may be given or
// was logged: a kind in a filter or an --on list (EventKind), the key of a
// filter (depotWhere), an event's field (EventDepot).
const (
	oldDepotWord   = "rig"
	oldDepotBucket = "rigs"
	oldDepotsDir   = "rigs"
	// DepotsDir is the directory of a yard's home that `yardr depot add
	// <name> <url>` clones into, <home>/rigs until schema 12 (moveDir).
	DepotsDir = "depots"
)

// oldDepotKinds are the event kinds that said rig until schema 12, each
// with the kind new events carry. Events already in the log keep the old
// kind; a filter that names one means the new.
var oldDepotKinds = map[string]string{
	"rig_updated": "depot_updated", "rig_removed": "depot_removed", "bad_rig": "bad_depot",
}

// EventDepot is the depot an event's data names: its field depot, or rig
// in an event logged before schema 12. "" when it names none.
func EventDepot(data map[string]any) string {
	if name, ok := data["depot"].(string); ok {
		return name
	}
	name, _ := data[oldDepotWord].(string)
	return name
}

// depotWhere is a subscription's filter with the words a depot had until
// schema 12 replaced, and whether any was: the key rig is depot, and a kind
// that said rig names the kind new events carry (oldDepotKinds).
func depotWhere(where map[string]string) (map[string]string, bool) {
	out, changed := where, false
	set := func(old, k, v string) {
		if !changed {
			out, changed = maps.Clone(where), true
		}
		delete(out, old)
		out[k] = v
	}
	for k, v := range where {
		switch {
		case k == oldDepotWord:
			// The new word stands when both are given.
			if _, has := where["depot"]; has {
				v = where["depot"]
			}
			set(k, "depot", v)
		case k == "kind" && oldDepotKinds[v] != "":
			set(k, k, oldDepotKinds[v])
		}
	}
	return out, changed
}

// moveDir renames <home>/<old>, a directory of the home that had another
// name until this release, to <home>/<name>, and leaves a link of the old
// name to it: files name others by path (an env_file a GitHub App's key, a
// worktree its repository), and so may a script of the operator's. It
// reports whether the directory is at the new name, having moved it now or
// on an earlier open that did not get to stamp the store. A home with both
// directories is the operator's to sort out, and one that cannot be renamed
// is left: nothing is moved, and the paths recorded stay as they are, which
// still work.
func moveDir(home, old, name string) bool {
	from, dir := filepath.Join(home, old), filepath.Join(home, name)
	st, err := os.Lstat(from)
	if err != nil {
		return false
	}
	if st.Mode()&os.ModeSymlink != 0 {
		to, err := os.Readlink(from)
		return err == nil && (to == name || to == dir)
	}
	if !st.IsDir() {
		return false
	}
	if _, err := os.Lstat(dir); err == nil || os.Rename(from, dir) != nil {
		return false
	}
	// Relative, so the home can be moved with it.
	_ = os.Symlink(name, from)
	return true
}

// movedPath is a path with <home>/<old> replaced by <home>/<name>, spelled
// as it was (a leading ~ stays), and whether it was that directory or under
// it.
func movedPath(path, home, old, name string) (string, bool) {
	full, err := config.ExpandPath(path)
	if err != nil {
		return path, false
	}
	sep := string(filepath.Separator)
	rest, ok := strings.CutPrefix(full+sep, filepath.Join(home, old)+sep)
	if !ok {
		return path, false
	}
	rest = strings.TrimSuffix(sep+rest, sep)
	if head, ok := strings.CutSuffix(path, old+rest); ok {
		return head + name + rest, true
	}
	return filepath.Join(home, name) + rest, true
}

// rekey renames the keys of the JSON object raw as keys says (old to new),
// and reports whether it had any of the old. A key under the new word
// stands when both are there. Every other field stays byte for byte, the
// ones this build does not know among them.
func rekey(raw []byte, keys map[string]string) ([]byte, bool, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		// Not an object: nothing of it is named by a key.
		return raw, false, nil
	}
	changed := false
	for old, name := range keys {
		v, ok := m[old]
		if !ok {
			continue
		}
		delete(m, old)
		if _, has := m[name]; !has {
			m[name] = v
		}
		changed = true
	}
	if !changed {
		return raw, false, nil
	}
	out, err := json.Marshal(m)
	return out, true, err
}

// migrateDepotRecords moves the depots' records from the bucket they were
// in until schema 12 to bDepots, and renames the field that names a depot
// (rig) in the records that have one: beads, routes, flow pointers, the
// records of imported packs (rig_roles too) and a peer's intake. It goes
// by the JSON as it is stored, not by this build's types, and before every
// other migration: those read a record into a type that knows only the new
// field and write it back, which would drop the old one. It returns how
// many records of each bucket it renamed; a second run finds none, and a
// store stamped with schema 12 is not looked through again.
func migrateDepotRecords(tx *bolt.Tx) (map[string]any, error) {
	counts := map[string]any{}
	if old := tx.Bucket([]byte(oldDepotBucket)); old != nil {
		b, err := tx.CreateBucketIfNotExists(bDepots)
		if err != nil {
			return nil, err
		}
		n := 0
		err = old.ForEach(func(k, v []byte) error {
			n++
			if b.Get(k) != nil {
				return nil
			}
			return b.Put(k, v)
		})
		if err != nil {
			return nil, err
		}
		if err := tx.DeleteBucket([]byte(oldDepotBucket)); err != nil {
			return nil, err
		}
		if n > 0 {
			counts["depots"] = n
		}
	}
	word := map[string]string{oldDepotWord: "depot"}
	// rewrite puts back each record of bucket that fix changed and counts
	// them as what. bbolt cursors must not see their bucket change, so the
	// bucket is read whole first.
	rewrite := func(what string, bucket []byte, fix func(raw []byte) ([]byte, bool, error)) error {
		b := tx.Bucket(bucket)
		if b == nil {
			return nil
		}
		changes := map[string][]byte{}
		err := b.ForEach(func(k, raw []byte) error {
			out, changed, err := fix(raw)
			if err == nil && changed {
				changes[string(k)] = out
			}
			return err
		})
		if err != nil {
			return err
		}
		for k, v := range changes {
			if err := b.Put([]byte(k), v); err != nil {
				return err
			}
		}
		if len(changes) > 0 {
			counts[what] = len(changes)
		}
		return nil
	}
	top := func(keys map[string]string) func([]byte) ([]byte, bool, error) {
		return func(raw []byte) ([]byte, bool, error) { return rekey(raw, keys) }
	}
	if err := rewrite("beads", bBeads, top(word)); err != nil {
		return nil, err
	}
	if err := rewrite("routes", bRoutes, top(word)); err != nil {
		return nil, err
	}
	if err := rewrite("flow_pointers", bFlowPtrs, top(word)); err != nil {
		return nil, err
	}
	if err := rewrite("packs", bPacks, top(map[string]string{oldDepotWord: "depot", "rig_roles": "depot_roles"})); err != nil {
		return nil, err
	}
	err := rewrite("peers", bPeers, func(raw []byte) ([]byte, bool, error) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil || m["intake"] == nil {
			return raw, false, nil
		}
		intake, changed, err := rekey(m["intake"], word)
		if err != nil || !changed {
			return raw, false, err
		}
		m["intake"] = intake
		out, err := json.Marshal(m)
		return out, true, err
	})
	return counts, err
}

// migrateDepots renames what the store holds under the word rig, as it was
// until schema 12, to depot, beside the records migrateDepotRecords moved
// (renamed is its count of them): the key and the kinds in the filters of
// subscriptions and of the records of imported packs, the kinds in a crew
// member's --on, and, when the directory was moved (moveDir), the path of
// a depot cloned under <home>/rigs. Events already logged are not
// rewritten.
func migrateDepots(tx *bolt.Tx, from uint64, home string, moved bool, renamed map[string]any) error {
	if from >= 12 {
		return nil
	}
	counts := maps.Clone(renamed)
	if counts == nil {
		counts = map[string]any{}
	}
	// rewrite puts back each record of bucket that fix changed and counts
	// them as what.
	rewrite := func(what string, bucket []byte, fix func(raw []byte) (any, error)) error {
		changes := map[string]any{}
		err := tx.Bucket(bucket).ForEach(func(k, raw []byte) error {
			v, err := fix(raw)
			if err == nil && v != nil {
				changes[string(k)] = v
			}
			return err
		})
		if err != nil {
			return err
		}
		for k, v := range changes {
			if err := put(tx, bucket, k, v); err != nil {
				return err
			}
		}
		if len(changes) > 0 {
			counts[what] = len(changes)
		}
		return nil
	}
	subs := func(list []Subscription) bool {
		changed := false
		for i := range list {
			if w, ok := depotWhere(list[i].Where); ok {
				list[i].Where, changed = w, true
			}
		}
		return changed
	}
	err := rewrite("paths", bDepots, func(raw []byte) (any, error) {
		var r Depot
		if err := json.Unmarshal(raw, &r); err != nil || !moved {
			return nil, err
		}
		p, ok := movedPath(r.Path, home, oldDepotsDir, DepotsDir)
		if !ok {
			return nil, nil
		}
		r.Path = p
		return r, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("subscriptions", bCrew, func(raw []byte) (any, error) {
		var r CrewMember
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		changed := subs(r.Subscriptions)
		if on, ok := eventKinds(r.On); ok {
			r.On, changed = on, true
		}
		if !changed {
			return nil, nil
		}
		return r, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("user_subscriptions", bUsers, func(raw []byte) (any, error) {
		var u User
		if err := json.Unmarshal(raw, &u); err != nil || !subs(u.Subscriptions) {
			return nil, err
		}
		return u, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("group_subscriptions", bGroups, func(raw []byte) (any, error) {
		var g Group
		if err := json.Unmarshal(raw, &g); err != nil || !subs(g.Subscriptions) {
			return nil, err
		}
		return g, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("pack_subscriptions", bPacks, func(raw []byte) (any, error) {
		var rec PackImport
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, err
		}
		changed := false
		for i := range rec.Objects {
			if o := &rec.Objects[i]; o.Subscription != nil {
				if w, ok := depotWhere(o.Subscription.Where); ok {
					o.Subscription.Where, changed = w, true
				}
			}
		}
		if !changed {
			return nil, nil
		}
		return rec, nil
	})
	if err != nil || len(counts) == 0 {
		return err
	}
	counts["from"] = oldDepotBucket
	return (&wtx{Tx: tx}).event("", "depots_migrated", "yardr", counts)
}
