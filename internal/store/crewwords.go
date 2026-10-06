package store

import (
	"cmp"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/config"
)

// Until schema 11 the crew were the residents (migrateCrew). The old word
// is still read for this release wherever it may be given or was logged: a
// kind in a filter or an --on list (EventKind), a label or a memory's scope
// (crewLabel), an event's field (EventCrew), the refusal a peer sends
// (RefusedNoCrew, which is still sent under the old word).
const (
	oldCrewWord   = "resident"
	oldCrewBucket = "residents"
	oldCrewDir    = "residents"
	// CrewDir is the directory of a yard's home where the operator keeps
	// what a crew member runs with (an env_file, a key): a convention of
	// the docs, which yardr makes nowhere, and <home>/residents until
	// schema 11 (moveCrewDir).
	CrewDir = "crew"
)

// oldCrewKinds are the event kinds that said resident until schema 11, each
// with the kind new events carry. Events already in the log keep the old
// kind; a filter that names one means the new.
var oldCrewKinds = map[string]string{
	"resident_defined": "crew_defined", "resident_deleted": "crew_deleted",
	"resident_enabled": "crew_enabled", "resident_disabled": "crew_disabled",
	"resident_started": "crew_started", "resident_status": "crew_status", "resident_stopped": "crew_stopped",
	"resident_start_failed": "crew_start_failed", "resident_gave_up": "crew_gave_up",
	"resident_restart_requested": "crew_restart_requested", "resident_rotation_asked": "crew_rotation_asked",
	"resident_blocked": "crew_blocked",
}

// EventKind is the event kind the word kind names: kind itself, or the one
// that replaced it when kind is one an earlier release logged
// (OldTrainKinds, and the kinds that said resident). What reads the log by
// kind goes by it, so an event from before the upgrade is read as one from
// after.
func EventKind(kind string) string {
	if k := oldCrewKinds[kind]; k != "" {
		return k
	}
	if k := OldTrainKinds[kind]; k != "" {
		return k
	}
	return kind
}

// kindSpellings are the kinds an event of kind is in the log under: kind
// as new events carry it, and what an earlier release called it.
func kindSpellings(kind string) []string {
	out := []string{kind}
	for _, olds := range []map[string]string{oldCrewKinds, OldTrainKinds} {
		for old, k := range olds {
			if k == kind {
				out = append(out, old)
			}
		}
	}
	return out
}

// eventKinds is a list of kinds (a crew member's --on) with those an
// earlier release used replaced, and whether any was.
func eventKinds(kinds []string) ([]string, bool) {
	changed := false
	for i, k := range kinds {
		if EventKind(k) == k {
			continue
		}
		if !changed {
			kinds, changed = slices.Clone(kinds), true
		}
		kinds[i] = EventKind(k)
	}
	if changed {
		// One given under both words is one.
		seen := map[string]bool{}
		kinds = slices.DeleteFunc(kinds, func(k string) bool {
			dup := seen[k]
			seen[k] = true
			return dup
		})
	}
	return kinds, changed
}

// EventCrew is the crew member an event's data names: its field crew, or
// resident in an event logged before schema 11. "" when it names none.
func EventCrew(data map[string]any) string {
	if name, ok := data["crew"].(string); ok {
		return name
	}
	name, _ := data[oldCrewWord].(string)
	return name
}

// CrewLabel is label with the word an earlier release used replaced:
// resident:<name>, the label of a crew member's journal and the scope of
// its memories, is crew:<name>, also behind scope: (a memory's label).
func CrewLabel(label string) string {
	head := ""
	if rest, ok := strings.CutPrefix(label, "scope:"); ok {
		head, label = "scope:", rest
	}
	if name, ok := strings.CutPrefix(label, oldCrewWord+":"); ok {
		label = "crew:" + name
	}
	return head + label
}

// crewLabels is CrewLabel of each label, and whether any changed.
func crewLabels(labels []string) ([]string, bool) {
	changed := false
	for i, l := range labels {
		if CrewLabel(l) == l {
			continue
		}
		if !changed {
			labels, changed = slices.Clone(labels), true
		}
		labels[i] = CrewLabel(l)
	}
	if changed {
		labels = dedupe(labels)
	}
	return labels, changed
}

// crewWhere is a subscription's filter with the words the crew had until
// schema 11 replaced, and whether any was: a kind that said resident names
// the kind new events carry, and a label that said it the label a crew
// member's journal has now (CrewLabel).
func crewWhere(where map[string]string) (map[string]string, bool) {
	out, changed := where, false
	for k, v := range where {
		to := v
		switch k {
		case "kind":
			to = cmp.Or(oldCrewKinds[v], v)
		case "label":
			to = CrewLabel(v)
		}
		if to == v {
			continue
		}
		if !changed {
			out, changed = maps.Clone(where), true
		}
		out[k] = to
	}
	return out, changed
}

// moveCrewDir renames <home>/residents, where the docs had the operator
// keep a crew member's env_file, to <home>/crew, and leaves a link of the
// old name to it: an env_file names other files by path (a GitHub App's
// key), and so may a script of the operator's. It reports whether the
// directory is at the new name, having moved it now or on an earlier open
// that did not get to stamp the store. A home with both directories is the
// operator's to sort out, and one that cannot be renamed is left: nothing
// is moved, and the paths recorded stay as they are, which still work.
func moveCrewDir(home string) bool {
	old, dir := filepath.Join(home, oldCrewDir), filepath.Join(home, CrewDir)
	st, err := os.Lstat(old)
	if err != nil {
		return false
	}
	if st.Mode()&os.ModeSymlink != 0 {
		to, err := os.Readlink(old)
		return err == nil && (to == CrewDir || to == dir)
	}
	if !st.IsDir() {
		return false
	}
	if _, err := os.Lstat(dir); err == nil || os.Rename(old, dir) != nil {
		return false
	}
	// Relative, so the home can be moved with it.
	_ = os.Symlink(CrewDir, old)
	return true
}

// crewPath is an env_file's path with <home>/residents replaced by
// <home>/crew, spelled as it was (a leading ~ stays), and whether it was
// under that directory.
func crewPath(path, home string) (string, bool) {
	full, err := config.ExpandPath(path)
	if err != nil {
		return path, false
	}
	rest, ok := strings.CutPrefix(full, filepath.Join(home, oldCrewDir)+string(filepath.Separator))
	if !ok {
		return path, false
	}
	rest = string(filepath.Separator) + rest
	if head, ok := strings.CutSuffix(path, oldCrewDir+rest); ok {
		return head + CrewDir + rest, true
	}
	return filepath.Join(home, CrewDir) + rest, true
}

// migrateCrewBucket moves the crew's records from the bucket they were in
// until schema 11 to bCrew. It runs before every other migration, which
// read the crew where it is now, and once: the old bucket goes.
func migrateCrewBucket(tx *bolt.Tx) error {
	old := tx.Bucket([]byte(oldCrewBucket))
	if old == nil {
		return nil
	}
	b, err := tx.CreateBucketIfNotExists(bCrew)
	if err != nil {
		return err
	}
	err = old.ForEach(func(k, v []byte) error {
		if b.Get(k) != nil {
			return nil
		}
		return b.Put(k, v)
	})
	if err != nil {
		return err
	}
	return tx.DeleteBucket([]byte(oldCrewBucket))
}

// migrateCrew renames what the store holds under the word resident, as it
// was until schema 11, to crew: the labels of the crew's journals and the
// scope of their memories (resident:<name>), the kinds in a crew member's
// --on and in the filters of subscriptions, the field that names the crew
// member in a thread it takes part in and in the record of an imported
// pack, and, when the directory was moved (moveCrewDir), an env_file under
// <home>/residents. The records themselves are moved by migrateCrewBucket.
// Events already logged are not rewritten.
func migrateCrew(tx *bolt.Tx, from uint64, home string, moved bool) error {
	if from >= 11 {
		return nil
	}
	counts := map[string]any{}
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
			if w, ok := crewWhere(list[i].Where); ok {
				list[i].Where, changed = w, true
			}
		}
		return changed
	}
	err := rewrite("crew", bCrew, func(raw []byte) (any, error) {
		var r CrewMember
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		changed := subs(r.Subscriptions)
		if on, ok := eventKinds(r.On); ok {
			r.On, changed = on, true
		}
		if path, _ := r.Config["env_file"].(string); moved && path != "" {
			if p, ok := crewPath(path, home); ok {
				r.Config, changed = maps.Clone(r.Config), true
				r.Config["env_file"] = p
			}
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
	err = rewrite("beads", bBeads, func(raw []byte) (any, error) {
		var r beadRow
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		labels, changed := crewLabels(r.Labels)
		r.Labels = labels
		if scope, _ := r.Metadata["scope"].(string); CrewLabel(scope) != scope {
			r.Metadata["scope"], changed = CrewLabel(scope), true
		}
		if !changed {
			return nil, nil
		}
		return r, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("threads", bThreads, func(raw []byte) (any, error) {
		var p struct {
			Participation
			Old string `json:"resident"`
		}
		if err := json.Unmarshal(raw, &p); err != nil || p.Old == "" {
			return nil, err
		}
		if p.CrewMember == "" {
			p.CrewMember = p.Old
		}
		return p.Participation, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("packs", bPacks, func(raw []byte) (any, error) {
		type object struct {
			PackObject
			Old string `json:"resident"`
		}
		var old struct {
			PackImport
			Old     map[string]string `json:"residents"`
			Objects []object          `json:"objects"`
		}
		if err := json.Unmarshal(raw, &old); err != nil {
			return nil, err
		}
		rec, changed := old.PackImport, false
		if old.Old != nil && rec.Crew == nil {
			rec.Crew, changed = old.Old, true
		}
		rec.Objects = make([]PackObject, 0, len(old.Objects))
		for _, o := range old.Objects {
			if o.Old != "" && o.CrewMember == "" {
				o.CrewMember, changed = o.Old, true
			}
			if o.Kind == oldCrewWord {
				o.Kind, changed = "crew", true
			}
			if o.Subscription != nil {
				if w, ok := crewWhere(o.Subscription.Where); ok {
					o.Subscription.Where, changed = w, true
				}
			}
			rec.Objects = append(rec.Objects, o.PackObject)
		}
		if !changed {
			return nil, nil
		}
		return rec, nil
	})
	if err != nil || len(counts) == 0 {
		return err
	}
	counts["from"] = oldCrewBucket
	return (&wtx{Tx: tx}).event("", "crew_migrated", "yardr", counts)
}
