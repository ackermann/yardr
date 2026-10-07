package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

// schemaVersion 17 has sources that take updates: a source's record holds
// how many of a caller's calls in an hour may be updates
// (Source.UpdatesPerHour), and the key an update is sent with is kept until
// the service said it took it (the bucket source_keys, see updateKey). A
// build before it would drop the limit from a record it rewrites (source
// set, source add), so a source the operator had bound to fewer updates
// would be back at the default with nothing to say so, and its source rm
// would leave the keys behind. Nothing is migrated: a source without the
// setting has the default, and one without keys has no update under way.
// Version 16 has sources that sessions and crew members call: who the
// operator granted a source's calls is in a bucket of its own
// (source_grants, see GrantSource), and a source's record holds how many
// calls an hour one caller may make (Source.CallsPerHour). A build before
// it would drop the limit from a record it rewrites (source set, source
// add), so the source would be back at the default with nothing to say so,
// and its source rm would leave the grants behind: a source added again
// under the name would be open to whoever the old one was, with the yard's
// credential behind the call. Nothing is migrated: a source without grants
// is called by the operator alone, as before.
// Version 15 has what lands in a user's inbox told outside the yard:
// with a mail to a user and with a notification, a push is queued for the
// daemon to send (the bucket pushes, see Push), and the user's record holds
// how the last send ended and when the last ones went (User.LastPush,
// User.Pushed). A build before it would fill the inbox and queue nothing,
// so the person would not be told and nothing would say so, and it would
// drop the two fields from a record it rewrites, and with them the count
// that bounds an hour's messages. Nothing is migrated: what was in an inbox
// before is not told afterwards.
// Version 14 has users that are told outside the yard: a user's
// record holds the source that tells them and the path of the file their
// address is read from (User.Notify, User.EnvFile), which a build before it
// would drop from a record it rewrites, as it does when the user
// acknowledges their inbox. The person would then stop being told, with
// nothing to say so. Nothing is migrated: a user without them is told
// nowhere, as before.
// Version 13 calls a piece of a train a wagon. Version 10 to 12 of
// main, in no release, called it a car, and a yard that ran one has that
// type in beads and in all that names a type (migrateWagons). A build before
// it knows no wagon, so it would take one for a bead like any other: under
// the depot's default flow, and landing on the base by itself.
// Version 12 calls the repository or directory beads are worked in a
// depot: its records are in the bucket depots, where they were in rigs, and
// the field that names it in a bead, a route, a flow pointer, an imported
// pack and a peer's intake is depot, where it was rig
// (migrateDepotRecords), as is the word in what else the store held it in
// (migrateDepots). A build before it reads the old bucket and field only,
// so it would find a yard with no depots and beads that are in none.
// Version 11 calls the sessions that are always in a yard its crew:
// their records are in the bucket crew, where they were in residents
// (migrateCrewBucket), and the word is crew where the store held resident
// (migrateCrew). A build before it reads the old bucket only, so it would
// find a yard with nobody in it and make a second yardmaster.
// Version 10 calls work that lands as one a train and a piece of it a
// wagon: the bead types epic and part are train and wagon, in beads and in all
// that names a type (migrateTrains). A build before it knows only the old
// types, so it would take a train for a bead like any other: its children
// would branch from the depot's base and land there one by one.
// Version 9 calls the crew member that coordinates a yard its yardmaster:
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
const schemaVersion = 17

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

// firstStamped is the schema of the first build that stamped a store: one
// migrated from counter ids and nothing more.
const firstStamped = 2

// setSchema stamps a store that needs no migration (a new one, or one just
// migrated) with this build's schema and drops what version 1 left behind.
func setSchema(tx *bolt.Tx) error {
	if tx.Bucket(bCounters) != nil {
		if err := tx.DeleteBucket(bCounters); err != nil {
			return err
		}
	}
	return stampSchema(tx, schemaVersion)
}

// stampSchema writes version as the schema the store is at.
func stampSchema(tx *bolt.Tx, version uint64) error {
	stamp := binary.BigEndian.AppendUint64(nil, version)
	if bytes.Equal(tx.Bucket(bMeta).Get(kSchema), stamp) {
		return nil
	}
	return tx.Bucket(bMeta).Put(kSchema, stamp)
}

// upgrade migrates a version 1 store that holds beads to version 2, once,
// after copying the file to <path>.pre-uuid-<time> so the old state can be
// restored.
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
		// First, as on every open: what follows reads beads by the field
		// that names their depot now.
		if _, err := migrateDepotRecords(tx); err != nil {
			return err
		}
		for _, b := range allBuckets {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		if err := migrateToUUIDs(tx); err != nil {
			return err
		}
		// The loop above made the open index, empty, so Open finds one and
		// builds none: it is filled here, by the keys the beads have now.
		if err := fillOpenBeads(tx); err != nil {
			return err
		}
		// The same for the sessions' live, pane and pending indexes.
		if err := fillLiveSessions(tx); err != nil {
			return err
		}
		if err := fillPanesAndPending(tx); err != nil {
			return err
		}
		// The store is where the first build that stamped one left it, and
		// says so: the migrations that go by the stamp are Open's, which
		// runs them next as for any store of that age, and stamps it with
		// this build's schema when they are done.
		return stampSchema(tx, firstStamped)
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
	// handles now, and they never change. The inbox and kind indexes are
	// not written here: Open builds them for a store stamped as this one
	// is (migrateIndexes).
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

// bLegacyFlows held flows before they had names (yard-9cza): one per depot
// and one per type in a depot, keyed <depot>\x00<type>.
var bLegacyFlows = []byte("flows")

// migrateFlows turns each depot's flow into the named flow <depot> and each
// type flow into <depot>.<type>, with the depot and type pointing at them, so
// every bead keeps the lifecycle it had. It runs once: the old bucket goes.
func migrateFlows(tx *bolt.Tx) error {
	if tx.Bucket(bLegacyFlows) == nil {
		return nil
	}
	type legacy struct {
		Flow
		Depot string `json:"rig"` // the word then
		Type  string `json:"type"`
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
		f.Name = l.Depot
		if l.Type != "" {
			f.Name += "." + l.Type
		}
		if err := put(tx, bFlows, f.Name, f); err != nil {
			return err
		}
		if err := put(tx, bFlowPtrs, join(l.Depot, l.Type), FlowPointer{Depot: l.Depot, Type: l.Type, Flow: f.Name}); err != nil {
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

// migrateDepotKinds stamps every depot stored without a kind as a git depot,
// which every depot was before dir depots. It writes nothing once each has one.
func migrateDepotKinds(tx *bolt.Tx) error {
	var old []*Depot
	if err := each(tx, bDepots, "", func(_ []byte, r *Depot) error {
		if r.Kind == "" {
			old = append(old, r)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, r := range old {
		r.Kind = DepotGit
		if err := put(tx, bDepots, r.Name, r); err != nil {
			return err
		}
	}
	return nil
}

// migrateSettings gives a yard from before the onboarding (yard-p01r) the
// default_kind setting that marks a home set up: a store with depots, groups
// or crew members but no default_kind is in use, and without it every command
// would onboard it again (yard-w6np). The kind is the first herdr group's,
// else the first herdr crew member's, else claude. The operator setting stays
// unset: role prompts already installed keep the name they have.
func migrateSettings(tx *bolt.Tx) error {
	if tx.Bucket(bSettings).Get([]byte("default_kind")) != nil {
		return nil
	}
	inUse := false
	for _, b := range [][]byte{bDepots, bGroups, bCrew} {
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
	if err := each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		if k, ok := r.Config["kind"].(string); ok && k != "" && r.Runner == "herdr" && kind == "" {
			kind, from = k, "crew member "+r.Name
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
// crew member, mayor until schema 9, to yardmaster. The crew member keeps its name,
// which is the operator's to give: a yard from before the setting whose
// crew member is called mayor gets the setting, so what defaulted to that name
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
	} else if tx.Bucket(bCrew).Get([]byte(oldYardmasterSetting)) != nil {
		name, source = oldYardmasterSetting, "crew member "+oldYardmasterSetting
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

// migrateTrains renames the bead types epic and part, as they were until
// schema 10, to train and wagon wherever the store holds one: beads, routes,
// flow pointers, hooks' stage maps, the filters of subscriptions (where
// the key epic becomes train too, and a kind that said epic or part the
// kind new events carry) and the records of imported packs. What the
// operator named stays: a flow called web.epic keeps its name, and a
// pointer or a pin that names it still finds it (flowNames). Events already
// logged are not rewritten.
func migrateTrains(tx *bolt.Tx, from uint64) error {
	if from >= 10 {
		return nil
	}
	return renameTypes(tx, "types "+oldTypeTrain+" and "+oldTypeWagon, BeadType, trainWhere)
}

// migrateWagons renames the bead type car to wagon in a store of schema 10
// to 12: main called a wagon a car between two releases, and a yard that
// ran it then stored the word in every place migrateTrains writes a type,
// and the kind car_without_train in a subscription's filter. No release had
// the word, so nothing reads it afterwards but flowNames.
func migrateWagons(tx *bolt.Tx, from uint64) error {
	if from < 10 || from >= 13 {
		return nil
	}
	typ := func(t string) string {
		if t == typeCar {
			return TypeWagon
		}
		return t
	}
	where := func(where map[string]string) (map[string]string, bool) {
		v, changed := where["type"], false
		out := where
		if v == typeCar {
			out, changed = maps.Clone(where), true
			out["type"] = TypeWagon
		}
		if where["kind"] == typeCar+"_without_train" {
			if !changed {
				out, changed = maps.Clone(where), true
			}
			out["kind"] = TypeWagon + "_without_train"
		}
		return out, changed
	}
	return renameTypes(tx, "type "+typeCar, typ, where)
}

// renameTypes rewrites every record that names a bead type typ replaces, or
// holds a subscription's filter where does: beads, routes, flow pointers
// (their key too), hooks' stage maps, the subscriptions of the crew, of
// users and of groups, and the records of imported packs. It logs one
// types_migrated event with the counts, saying they were renamed from what
// from names, and none when nothing was.
func renameTypes(tx *bolt.Tx, from string, typ func(string) string, where func(map[string]string) (map[string]string, bool)) error {
	counts := map[string]any{}
	// rewrite puts back each record of bucket that fix changed, under the
	// key fix gives it, and counts them as what.
	rewrite := func(what string, bucket []byte, fix func(key string, raw []byte) (newKey string, v any, err error)) error {
		type change struct {
			old, key string
			v        any
		}
		var changes []change
		err := tx.Bucket(bucket).ForEach(func(k, raw []byte) error {
			key, v, err := fix(string(k), raw)
			if err != nil || v == nil {
				return err
			}
			changes = append(changes, change{string(k), key, v})
			return nil
		})
		if err != nil {
			return err
		}
		for _, c := range changes {
			if c.key != c.old {
				if err := tx.Bucket(bucket).Delete([]byte(c.old)); err != nil {
					return err
				}
				// A record under the new word already is the newer one.
				if tx.Bucket(bucket).Get([]byte(c.key)) != nil {
					continue
				}
			}
			if err := put(tx, bucket, c.key, c.v); err != nil {
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
			if w, ok := where(list[i].Where); ok {
				list[i].Where, changed = w, true
			}
		}
		return changed
	}
	err := rewrite("beads", bBeads, func(k string, raw []byte) (string, any, error) {
		var r beadRow
		if err := json.Unmarshal(raw, &r); err != nil || typ(r.Type) == r.Type {
			return k, nil, err
		}
		r.Type = typ(r.Type)
		return k, r, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("routes", bRoutes, func(k string, raw []byte) (string, any, error) {
		var r Route
		if err := json.Unmarshal(raw, &r); err != nil || r.Type == nil || typ(*r.Type) == *r.Type {
			return k, nil, err
		}
		typ := typ(*r.Type)
		r.Type = &typ
		return k, r, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("flow_pointers", bFlowPtrs, func(k string, raw []byte) (string, any, error) {
		var p FlowPointer
		if err := json.Unmarshal(raw, &p); err != nil || typ(p.Type) == p.Type {
			return k, nil, err
		}
		p.Type = typ(p.Type)
		return join(p.Depot, p.Type), p, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("hooks", bHooks, func(k string, raw []byte) (string, any, error) {
		var h Hook
		if err := json.Unmarshal(raw, &h); err != nil {
			return k, nil, err
		}
		m, changed := mapTypes(h.Map, typ)
		if !changed {
			return k, nil, nil
		}
		h.Map = m
		return k, h, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("subscriptions", bCrew, func(k string, raw []byte) (string, any, error) {
		var r CrewMember
		if err := json.Unmarshal(raw, &r); err != nil || !subs(r.Subscriptions) {
			return k, nil, err
		}
		return k, r, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("user_subscriptions", bUsers, func(k string, raw []byte) (string, any, error) {
		var u User
		if err := json.Unmarshal(raw, &u); err != nil || !subs(u.Subscriptions) {
			return k, nil, err
		}
		return k, u, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("group_subscriptions", bGroups, func(k string, raw []byte) (string, any, error) {
		var g Group
		if err := json.Unmarshal(raw, &g); err != nil || !subs(g.Subscriptions) {
			return k, nil, err
		}
		return k, g, nil
	})
	if err != nil {
		return err
	}
	err = rewrite("packs", bPacks, func(k string, raw []byte) (string, any, error) {
		var rec PackImport
		if err := json.Unmarshal(raw, &rec); err != nil {
			return k, nil, err
		}
		changed := false
		for i := range rec.Objects {
			o := &rec.Objects[i]
			if typ(o.Type) != o.Type {
				o.Type, changed = typ(o.Type), true
			}
			if m, ok := mapTypes(o.Map, typ); ok {
				o.Map, changed = m, true
			}
			if o.Subscription != nil {
				if w, ok := where(o.Subscription.Where); ok {
					o.Subscription.Where, changed = w, true
				}
			}
		}
		if !changed {
			return k, nil, nil
		}
		return k, rec, nil
	})
	if err != nil || len(counts) == 0 {
		return err
	}
	counts["from"] = from
	return (&wtx{Tx: tx}).event("", "types_migrated", "yardr", counts)
}
