package store

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// schemaVersion 9 calls the resident that coordinates a yard its yardmaster:
// the setting that names it is yardmaster, where it was mayor
// (migrateYardmaster). A build before it reads the old key only, so it would
// not find the name and would write a second one beside it.
// Version 8 has an end to a held bead's wait: a peer's intake says
// how long a bead it sent waits to be released (PeerIntake.HoldFor), which
// a build before it would drop from a record it rewrites, as it does with
// every message it takes from the peer. An intake set to hold for ever
// would then hold for the default, and its beads go back refused. Nothing
// is migrated: an intake without it holds for PeerHoldFor.
// Version 7 has beads that are sent on from yard to yard: the record
// of a bead that came from a peer holds the yards it had passed through
// (peerBead.Path), which a build before it would drop from a record it
// rewrites, when the bead is released, say. Sent on after that, the bead
// would no longer say where it began, and the yard it began in would take
// it again: the loop the path is there to stop. Nothing is migrated: a
// record without a path is one whose bead came from its peer alone, and
// the beads sent out are in a bucket of their own (peer_beads_out).
// Version 6 has peers that send beads: a peer's record holds the
// intake its operator set for them (PeerIntake), which a build before it
// would drop from a record it rewrites, as it does with every message it
// takes from the peer. The beads would then be refused until the operator
// noticed. Nothing is migrated: the records of the beads themselves are in
// a bucket of their own (peer_beads), which an older build never opens.
// Version 5 has peers that can be renamed: a peer's record holds the
// names it had and the event it was added after (migratePeerSince), which
// a build before it would drop from a record it rewrites, and with them
// the mail from before a rename. Version 4 has users that subscribe: a
// user's record holds its subscriptions and a second cursor, for the
// notifications they make (migrateUserCursors), which a build before it
// would drop from a record it rewrites. Version 3 has the inbox and events_by_kind indexes, written
// with every event (migrateIndexes). Version 2 keys beads by UUID with handles
// (yard-29). Version 1 is every store before it: beads keyed by counter ids
// (yard-1) minted from the counters bucket.
//
// Bump this whenever a build starts writing something every write must
// keep up (an index, a field a read relies on): the stamp is what stops an
// older build from appending to the store without it.
const schemaVersion = 9

var (
	kSchema   = []byte("schema")
	bCounters = []byte("counters") // retired with version 1
)

// ErrNewerSchema means the store was written by a newer yardr: its
// migrations only run forwards, so an older build must not touch it.
var ErrNewerSchema = errors.New("store schema is newer than this yardr knows")

// checkSchema refuses a store stamped with a schema this build does not
// know, before any migration or bucket creation writes to it.
func checkSchema(db *bolt.DB) error {
	return db.View(func(tx *bolt.Tx) error {
		m := tx.Bucket(bMeta)
		if m == nil {
			return nil
		}
		v := m.Get(kSchema)
		if v == nil {
			return nil
		}
		if len(v) != 8 {
			return fmt.Errorf("schema stamp %x is not a version", v)
		}
		if n := binary.BigEndian.Uint64(v); n > schemaVersion {
			return fmt.Errorf("%w: schema %d, this build knows up to %d; run the yardr that wrote it (or newer)",
				ErrNewerSchema, n, schemaVersion)
		}
		return nil
	})
}

// stampedSchema is the schema the store was last opened by: 0 before any
// build stamped it.
func stampedSchema(tx *bolt.Tx) uint64 {
	m := tx.Bucket(bMeta)
	if m == nil {
		return 0
	}
	v := m.Get(kSchema)
	if len(v) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(v)
}

// setSchema stamps a store that needs no migration (a new one, or one just
// migrated) with this build's schema and drops what version 1 left behind.
func setSchema(tx *bolt.Tx) error {
	if tx.Bucket(bCounters) != nil {
		if err := tx.DeleteBucket(bCounters); err != nil {
			return err
		}
	}
	stamp := binary.BigEndian.AppendUint64(nil, schemaVersion)
	if bytes.Equal(tx.Bucket(bMeta).Get(kSchema), stamp) {
		return nil
	}
	return tx.Bucket(bMeta).Put(kSchema, stamp)
}

// upgrade migrates a version 1 store that holds beads, once, after copying
// the file to <path>.pre-uuid-<time> so the old state can be restored.
func upgrade(db *bolt.DB, path string) error {
	legacy := false
	err := db.View(func(tx *bolt.Tx) error {
		if m := tx.Bucket(bMeta); m != nil && m.Get(kSchema) != nil {
			return nil
		}
		if b := tx.Bucket(bBeads); b != nil {
			k, _ := b.Cursor().First()
			legacy = k != nil
		}
		return nil
	})
	if err != nil || !legacy {
		return err
	}
	backup := fmt.Sprintf("%s.pre-uuid-%s", path, time.Now().UTC().Format("20060102T150405Z"))
	if err := db.View(func(tx *bolt.Tx) error { return tx.CopyFile(backup, 0o600) }); err != nil {
		return fmt.Errorf("backup before migration: %w", err)
	}
	return db.Update(func(tx *bolt.Tx) error {
		for _, b := range allBuckets {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		if err := migrateToUUIDs(tx); err != nil {
			return err
		}
		return setSchema(tx)
	})
}

// migrateToUUIDs gives every bead a UUIDv7 built from its created_at, so
// old and new beads interleave in creation order, and keeps its counter id
// as its handle, so branches, worktrees and labels built from it stay
// valid. Every record that names a bead is rewritten to its UUID.
//
// bbolt cursors must not see their bucket change, so each bucket is read
// whole before it is rewritten.
func migrateToUUIDs(tx *bolt.Tx) error {
	var rows []*beadRow
	if err := each(tx, bBeads, "", func(k []byte, r *beadRow) error {
		r.ID = string(k)
		rows = append(rows, r)
		return nil
	}); err != nil {
		return err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	ids := map[string]string{} // counter id -> uuid
	prev := ""
	for _, r := range rows {
		r.UUID = formatUUID(after(prev, uuidAt(r.CreatedAt)))
		prev, ids[r.ID] = r.UUID, r.UUID
	}
	uuid := func(id string) string {
		if u, ok := ids[id]; ok {
			return u
		}
		return id // a dangling reference stays as it was
	}
	for _, r := range rows {
		if err := tx.Bucket(bBeads).Delete([]byte(r.ID)); err != nil {
			return err
		}
	}
	for _, r := range rows {
		if err := put(tx, bBeads, r.UUID, r); err != nil {
			return err
		}
		if err := tx.Bucket(bHandles).Put([]byte(r.ID), []byte(r.UUID)); err != nil {
			return err
		}
	}

	var notes []*Note
	if err := each(tx, bNotes, "", func(_ []byte, n *Note) error {
		notes = append(notes, n)
		return nil
	}); err != nil {
		return err
	}
	if err := recreate(tx, bNotes); err != nil {
		return err
	}
	for _, n := range notes {
		n.Bead = uuid(n.Bead)
		if err := put(tx, bNotes, join(n.Bead, seqKey(n.Seq)), n); err != nil {
			return err
		}
	}

	var deps []*depRow
	if err := each(tx, bDeps, "", func(_ []byte, d *depRow) error {
		deps = append(deps, d)
		return nil
	}); err != nil {
		return err
	}
	for _, b := range [][]byte{bDepFrom, bDepTo} {
		if err := recreate(tx, b); err != nil {
			return err
		}
	}
	for _, d := range deps {
		d.From, d.To = uuid(d.From), uuid(d.To)
		if err := put(tx, bDeps, d.ID, d); err != nil {
			return err
		}
		if err := tx.Bucket(bDepFrom).Put([]byte(join(d.From, d.ID)), []byte{}); err != nil {
			return err
		}
		if err := tx.Bucket(bDepTo).Put([]byte(join(d.To, d.ID)), []byte{}); err != nil {
			return err
		}
	}

	var sessions []*Session
	if err := each(tx, bSessions, "", func(_ []byte, s *Session) error {
		sessions = append(sessions, s)
		return nil
	}); err != nil {
		return err
	}
	for _, s := range sessions {
		s.Bead = uuid(s.Bead)
		if err := put(tx, bSessions, s.ID, s); err != nil {
			return err
		}
	}

	// Event payloads keep the ids they were written with: those are the
	// handles now, and they never change.
	var events []*Event
	if err := each(tx, bEvents, "", func(_ []byte, e *Event) error {
		events = append(events, e)
		return nil
	}); err != nil {
		return err
	}
	if err := recreate(tx, bEventsBy); err != nil {
		return err
	}
	for _, e := range events {
		if e.Bead != nil {
			u := uuid(*e.Bead)
			e.Bead = &u
		}
		if err := put(tx, bEvents, seqKey(e.Seq), e); err != nil {
			return err
		}
		if e.Bead != nil {
			if err := tx.Bucket(bEventsBy).Put([]byte(join(*e.Bead, seqKey(e.Seq))), []byte{}); err != nil {
				return err
			}
		}
		// The indexes exist (created above), so Open will not build them.
		if err := index(tx, *e); err != nil {
			return err
		}
	}

	return nil
}

// migrateIndexes builds the inbox and kind indexes (schema 3) for a store
// written by a build before them, going by the stamp rather than by whether
// the buckets exist: the indexes shipped before the schema was bumped, so a
// schema-2 build without them may have appended events to a store that has
// them, and only a full rebuild makes the indexes whole again. A rebuild
// writes the same keys an ingest would, so running it twice is harmless.
func migrateIndexes(tx *bolt.Tx, from uint64) error {
	if from >= 3 || tx.Bucket(bEvents) == nil {
		return nil
	}
	for _, b := range [][]byte{bInbox, bEventsKd} {
		if tx.Bucket(b) != nil {
			if err := tx.DeleteBucket(b); err != nil {
				return err
			}
		}
		if _, err := tx.CreateBucket(b); err != nil {
			return err
		}
	}
	return each(tx, bEvents, "", func(_ []byte, e *Event) error { return index(tx, *e) })
}

// recreate empties a bucket, keeping its sequence: note seqs come from it,
// and a reset would make the next note overwrite the first.
func recreate(tx *bolt.Tx, name []byte) error {
	seq := tx.Bucket(name).Sequence()
	if err := tx.DeleteBucket(name); err != nil {
		return err
	}
	b, err := tx.CreateBucket(name)
	if err != nil {
		return err
	}
	return b.SetSequence(seq)
}

// bLegacyFlows held flows before they had names (yard-9cza): one per rig
// and one per type in a rig, keyed <rig>\x00<type>.
var bLegacyFlows = []byte("flows")

// migrateFlows turns each rig's flow into the named flow <rig> and each
// type flow into <rig>.<type>, with the rig and type pointing at them, so
// every bead keeps the lifecycle it had. It runs once: the old bucket goes.
func migrateFlows(tx *bolt.Tx) error {
	if tx.Bucket(bLegacyFlows) == nil {
		return nil
	}
	type legacy struct {
		Flow
		Rig  string `json:"rig"`
		Type string `json:"type"`
	}
	var rows []*legacy
	if err := each(tx, bLegacyFlows, "", func(_ []byte, l *legacy) error {
		rows = append(rows, l)
		return nil
	}); err != nil {
		return err
	}
	var names []string
	for _, l := range rows {
		f := l.Flow
		f.Name = l.Rig
		if l.Type != "" {
			f.Name += "." + l.Type
		}
		if err := put(tx, bFlows, f.Name, f); err != nil {
			return err
		}
		if err := put(tx, bFlowPtrs, join(l.Rig, l.Type), FlowPointer{Rig: l.Rig, Type: l.Type, Flow: f.Name}); err != nil {
			return err
		}
		names = append(names, f.Name)
	}
	if err := tx.DeleteBucket(bLegacyFlows); err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	return (&wtx{Tx: tx}).event("", "flows_migrated", "yardr", map[string]any{"flows": names})
}

// migrateRigKinds stamps every rig stored without a kind as a git rig,
// which every rig was before dir rigs. It writes nothing once each has one.
func migrateRigKinds(tx *bolt.Tx) error {
	var old []*Rig
	if err := each(tx, bRigs, "", func(_ []byte, r *Rig) error {
		if r.Kind == "" {
			old = append(old, r)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, r := range old {
		r.Kind = RigGit
		if err := put(tx, bRigs, r.Name, r); err != nil {
			return err
		}
	}
	return nil
}

// migrateSettings gives a yard from before the onboarding (yard-p01r) the
// default_kind setting that marks a home set up: a store with rigs, groups
// or residents but no default_kind is in use, and without it every command
// would onboard it again (yard-w6np). The kind is the first herdr group's,
// else the first herdr resident's, else claude. The operator setting stays
// unset: role prompts already installed keep the name they have.
func migrateSettings(tx *bolt.Tx) error {
	if tx.Bucket(bSettings).Get([]byte("default_kind")) != nil {
		return nil
	}
	inUse := false
	for _, b := range [][]byte{bRigs, bGroups, bResidents} {
		if k, _ := tx.Bucket(b).Cursor().First(); k != nil {
			inUse = true
		}
	}
	if !inUse {
		return nil
	}
	kind, from := "", ""
	if err := each(tx, bGroups, "", func(_ []byte, g *Group) error {
		if k, ok := g.Config["kind"].(string); ok && k != "" && g.Runner == "herdr" && kind == "" {
			kind, from = k, "group "+g.Name
		}
		return nil
	}); err != nil {
		return err
	}
	if err := each(tx, bResidents, "", func(_ []byte, r *Resident) error {
		if k, ok := r.Config["kind"].(string); ok && k != "" && r.Runner == "herdr" && kind == "" {
			kind, from = k, "resident "+r.Name
		}
		return nil
	}); err != nil {
		return err
	}
	if kind == "" {
		kind, from = "claude", "default"
	}
	if err := put(tx, bSettings, "default_kind", kind); err != nil {
		return err
	}
	return (&wtx{Tx: tx}).event("", "settings_migrated", "yardr",
		map[string]any{"default_kind": kind, "from": from})
}

// migrateYardmaster renames the setting that names the yard's coordinating
// resident, mayor until schema 9, to yardmaster. The resident keeps its name,
// which is the operator's to give: a yard from before the setting whose
// resident is called mayor gets the setting, so what defaulted to that name
// still finds it now that the default is yardmaster.
func migrateYardmaster(tx *bolt.Tx, from uint64) error {
	if from >= 9 {
		return nil
	}
	var name string
	had, err := get(tx, bSettings, oldYardmasterSetting, &name)
	if err != nil {
		return err
	}
	source := "setting " + oldYardmasterSetting
	if had {
		if err := tx.Bucket(bSettings).Delete([]byte(oldYardmasterSetting)); err != nil {
			return err
		}
	} else if tx.Bucket(bResidents).Get([]byte(oldYardmasterSetting)) != nil {
		name, source = oldYardmasterSetting, "resident "+oldYardmasterSetting
	}
	if name == "" || tx.Bucket(bSettings).Get([]byte(YardmasterSetting)) != nil {
		return nil
	}
	if err := put(tx, bSettings, YardmasterSetting, name); err != nil {
		return err
	}
	return (&wtx{Tx: tx}).event("", "settings_migrated", "yardr",
		map[string]any{YardmasterSetting: name, "from": source})
}
