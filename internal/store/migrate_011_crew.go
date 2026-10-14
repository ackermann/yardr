package store

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// oldCrewWord, oldCrewBucket and oldCrewDir are the word, the bucket and
// the home directory the crew had until schema 11.
const (
	oldCrewWord   = "resident"
	oldCrewBucket = "residents"
	oldCrewDir    = "residents"
)

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
// pack, and, when the directory was moved (moveDir), an env_file under
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
			if p, ok := movedPath(path, home, oldCrewDir, CrewDir); ok {
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
		if scope, _ := r.Metadata["scope"].(string); crewLabel(scope) != scope {
			r.Metadata["scope"], changed = crewLabel(scope), true
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

// crewLabel is label with the word an earlier release used replaced:
// resident:<name>, the label of a crew member's journal and the scope of
// its memories, is crew:<name>, also behind scope: (a memory's label).
func crewLabel(label string) string {
	head := ""
	if rest, ok := strings.CutPrefix(label, "scope:"); ok {
		head, label = "scope:", rest
	}
	if name, ok := strings.CutPrefix(label, oldCrewWord+":"); ok {
		label = "crew:" + name
	}
	return head + label
}

// crewLabels is crewLabel of each label, and whether any changed.
func crewLabels(labels []string) ([]string, bool) {
	changed := false
	for i, l := range labels {
		if crewLabel(l) == l {
			continue
		}
		if !changed {
			labels, changed = slices.Clone(labels), true
		}
		labels[i] = crewLabel(l)
	}
	if changed {
		labels = dedupe(labels)
	}
	return labels, changed
}

// crewWhere is a subscription's filter with the words the crew had until
// schema 11 replaced, and whether any was: a kind that said resident names
// the kind new events carry, and a label that said it the label a crew
// member's journal has now (crewLabel).
func crewWhere(where map[string]string) (map[string]string, bool) {
	out, changed := crewWhereOne(where)
	for k, v := range out {
		if !strings.Contains(v, whereSep) {
			continue
		}
		vals := WhereValues(v)
		for i, one := range vals {
			if w, ok := crewWhereOne(map[string]string{k: one}); ok {
				vals[i] = w[k]
			}
		}
		if to := JoinWhere(vals); to != v {
			if !changed {
				out, changed = maps.Clone(out), true
			}
			out[k] = to
		}
	}
	return out, changed
}

func crewWhereOne(where map[string]string) (map[string]string, bool) {
	out, changed := where, false
	for k, v := range where {
		to := v
		switch k {
		case "kind":
			to = cmp.Or(oldCrewKinds[v], v)
		case "label":
			to = crewLabel(v)
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
