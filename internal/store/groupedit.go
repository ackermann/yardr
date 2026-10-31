// Changing a group one setting at a time (SetGroup): what an edit names
// changes, and the rest of the group is kept. PutGroup, which replaces a
// group whole, is in routing.go; the settings either write holds to the
// operator are in define.go.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// GroupEdit is what SetGroup changes of a group: the config keys of Set
// written, those of Unset removed, and Limit and Members where they are
// given. What it does not name is kept.
type GroupEdit struct {
	Set     map[string]any `json:"set,omitempty"`
	Unset   []string       `json:"unset,omitempty"`
	Limit   *int           `json:"limit,omitempty"`
	Members *[]string      `json:"members,omitempty"`
}

// GroupChange is a group as SetGroup found it and as it left it.
type GroupChange struct {
	Was Group `json:"was"`
	Is  Group `json:"is"`
}

// SetGroup changes what e names of the group name and keeps the rest, in
// one transaction: PutGroup writes the whole group, so a caller that read
// it, changed a key and put it back would undo what was written in
// between. The result is held to what PutGroup holds a group to. A group
// that is so already is not written and logs nothing; one that changes
// logs group_set, as PutGroup does. From a session's or a crew member's
// pane the write is recorded as that session's actor or that crew member,
// whatever actor says; from anywhere else, as given (paneAuthor).
func (s *Store) SetGroup(ctx context.Context, name string, e GroupEdit, actor string) (*GroupChange, error) {
	if e.Limit != nil && *e.Limit < 0 {
		return nil, fmt.Errorf("group %s: a limit is 0 or more", name)
	}
	var ch *GroupChange
	err := s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		ch = new(GroupChange)
		if ok, err := get(w.Tx, bGroups, name, &ch.Was); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("group %s: %w", name, ErrNotFound)
		}
		// Read again, not copied: the two share no map or slice.
		if _, err := get(w.Tx, bGroups, name, &ch.Is); err != nil {
			return err
		}
		g := &ch.Is
		var cfgErr error
		if g.Config, cfgErr = MergeConfig(name, g.Config, e.Set, e.Unset); cfgErr != nil {
			return cfgErr
		}
		if e.Limit != nil {
			g.Limit = *e.Limit
		}
		if e.Members != nil {
			g.Members = dedupeOrdered(*e.Members)
		}
		// Compared as stored: a list is the same list whatever type it was
		// given as.
		was, err := json.Marshal(ch.Was)
		if err != nil {
			return err
		}
		if is, err := json.Marshal(g); err != nil {
			return err
		} else if bytes.Equal(was, is) {
			ch.Is = ch.Was
			return nil
		}
		return putGroup(ctx, w, g, actor, e.Limit != nil)
	})
	if err != nil {
		return nil, err
	}
	return ch, nil
}

// ParseSets reads key=value words into a config: a value that is valid
// JSON is kept as what it is, anything else as the word. The commands
// that take --set read their values through it, and crew set reads its
// words through it after checking their shape itself.
func ParseSets(sets []string) (map[string]any, error) {
	m := map[string]any{}
	for _, kv := range sets {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--set %q: want key=value", kv)
		}
		var js any
		if err := json.Unmarshal([]byte(v), &js); err == nil {
			m[k] = js
		} else {
			m[k] = v
		}
	}
	return m, nil
}

// MergeConfig is a runner config with the keys of sets written over it and
// those of unset removed; config itself is left as it was. A key to unset
// that is not there is refused, naming whose config it is: a misspelt key
// would otherwise read as removed.
func MergeConfig(whose string, config, sets map[string]any, unset []string) (map[string]any, error) {
	out := make(map[string]any, len(config)+len(sets))
	for k, v := range config {
		out[k] = v
	}
	for _, k := range unset {
		if _, is := out[k]; !is {
			has := strings.Join(slices.Sorted(maps.Keys(config)), ", ")
			if has == "" {
				has = "none"
			}
			return nil, fmt.Errorf("--unset %s: %s has no such setting (it has: %s)", k, whose, has)
		}
		delete(out, k)
	}
	for k, v := range sets {
		out[k] = v
	}
	return out, nil
}

func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	out := []Group{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bGroups, "", func(_ []byte, g *Group) error {
			out = append(out, *g)
			return nil
		})
	})
	return out, err
}
