package store

import (
	"bytes"
	"encoding/json"
	"slices"

	bolt "go.etcd.io/bbolt"
)

// migrateGrantGiven fills each source grant's Given (schema 29) from the
// source_granted events still kept, and from nothing else. A call whose
// event was pruned gets no entry: the record's At and By are those of its
// last grant, but Calls is sorted on every grant and a revoke leaves At
// and By as they were, so nothing says which call they belong to, or
// whether it is still held, and giving them to one would invent who
// granted it. The events are read oldest first, so a call granted, taken
// back and granted again has its last grant, the one it is held by; a
// revoke writes no event, and the log is pruned oldest first, so a kept
// event is never older than a pruned one of the same call.
func migrateGrantGiven(tx *bolt.Tx, from uint64) error {
	if from >= 29 {
		return nil
	}
	type granted struct {
		Source   string      `json:"source"`
		What     string      `json:"what"`
		As       string      `json:"as"`
		Crew     string      `json:"crew"`
		Group    string      `json:"group"`
		Approved *approvedOn `json:"approved"`
	}
	grants := map[string]*SourceGrant{}
	prefix := []byte("source_granted\x00")
	c := tx.Bucket(bEventsKd).Cursor()
	for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
		var e Event
		if ok, err := get(tx, bEvents, string(k[len(prefix):]), &e); err != nil {
			return err
		} else if !ok {
			continue // an index entry its event was pruned from
		}
		raw, err := json.Marshal(e.Data)
		if err != nil {
			return err
		}
		var d granted
		if err := json.Unmarshal(raw, &d); err != nil {
			return err
		}
		key := grantKey(d.Source, Grantee{Group: d.Group, Crew: d.Crew}, d.As)
		g, ok := grants[key]
		if !ok {
			g = &SourceGrant{}
			if ok, err := get(tx, bSourceGrants, key, g); err != nil {
				return err
			} else if !ok {
				g = nil
			}
			grants[key] = g
		}
		if g == nil || !slices.Contains(g.Calls, d.What) {
			continue
		}
		given := GrantGiven{At: e.At, By: e.Actor}
		if d.Approved != nil {
			given.Bead, given.Ask = d.Approved.Bead, d.Approved.Ask
			// The event names the card, not who asked it: the ask's own
			// note does, where it is still there.
			if given.Asker, err = askerOf(tx, d.Approved.Bead, d.Approved.Ask); err != nil {
				return err
			}
		}
		if g.Given == nil {
			g.Given = map[string]GrantGiven{}
		}
		g.Given[d.What] = given
	}
	for key, g := range grants {
		if g == nil || g.Given == nil {
			continue
		}
		if err := put(tx, bSourceGrants, key, g); err != nil {
			return err
		}
	}
	return nil
}
