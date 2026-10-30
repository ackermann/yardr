package store

// The store's migrations, one file per migration.
//
// migrate_NNN_<name>.go holds the migration that brings schema NNN, where
// NNN is the version a store must be below for it to run: migrateChecks,
// which runs below 19, is migrate_019_checks.go. A migration whose bucket
// move runs before every other migration keeps both phases in its one file
// (migrate_011_crew.go, migrate_012_depots.go). migrate_<name>.go holds a
// one-time migration with no schema version, named after what it does.
// Three migrations live beside the area they migrate instead
// (migrateUserCursors, migratePeerSince, migrateOperator), and the index
// builds beside their indexes; all join the list where they run.
// openMigrations below is the whole upgrade in the order Open runs it.
// Only the migrations read the old words; what is given to this build is
// in today's words. The helpers two migrations share stay in this file.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// schemaVersion 35 has routes without a priority: they are ranked by a named
// order of what they ask of a bead (migrateRoutePriority), and of two of one
// scope and key from before the add refused them, the one that never won is
// removed. A build before it never touches the rewritten routes:
// checkSchema refuses a store newer than the build knows before any
// migration or bucket write.
// schemaVersion 34 has every notification of a user who is not the operator
// remade from what its person is told of the event again
// (migrateNotificationsHeld): rows written before the hold of old approval
// sentences kept the sentence whole, naming the typed value or path. A build
// before it never touches the remade rows: checkSchema refuses a store newer
// than the build knows before any migration or bucket write.
// schemaVersion 33 has every notification of a user who is not the operator
// remade from what its person is told of the event again
// (migrateNotificationsTakebackHeld): rows written before the hold of old
// login-takeback sentences kept the sentence whole, carrying the tail of the
// login command. A build before it never touches the remade rows: checkSchema
// refuses a store newer than the build knows before any migration or bucket
// write.
// schemaVersion 31 has every notification of a user who is not the operator
// remade from what its person is told of the event (migrateNotificationsTold):
// rows written before the hold kept the whole event, and a row whose event
// was pruned is bare, from its Kind, Bead, Reason and Title alone. A build
// before it never touches the remade rows: checkSchema refuses a store newer
// than the build knows before any migration or bucket write.
// schemaVersion 30 has a role on each user: operator or member, and every
// operator gate reads it instead of comparing the actor with the operator
// setting (migrateOperatorRole makes the setting's user the first
// operator). A build before it drops the role when it rewrites a user, so
// an operator other than the setting's is a member again there, and the
// setting's is still its one operator.
// schemaVersion 29 has each call of a source grant carry its own grant,
// Given: when, by whom and on which card (migrateGrantGiven, from the
// source_granted events still kept). A build before it drops Given when it
// rewrites a grant; the calls themselves are kept.
// schemaVersion 28 has the forced rotation at a day: a crew member whose
// stored max_age is 6h is moved to 24h (migrateCrewRotation), and the
// rotation itself is by context use past 60% of the window. A build
// before it rotates at 24h as written and knows no context use; it drops
// no field that is set.
// schemaVersion 27 has a hearer: the crew member an approved claim
// subscribes to the claimed address (migrateHearer). A build before it
// writes the address alone, and an approval between its card and this
// build's reads otherwise on the card, so it is stale, not misheard.
// schemaVersion 26 has a peer link hold an intake per depot: the one intake
// of a peer's record is a list of one, marked as the default
// (migratePeerIntakes). A build before it would drop the list from a record
// it rewrites, as it does with every message it takes from the peer, and
// the beads would then be refused until the operator noticed.
// schemaVersion 25 has an upgrade_from setting that always names a depot of
// the yard: setting it to one the yard has not got, and removing the depot
// it names, are refused, and a store from before either refusal has a
// dangling value cleared once on open (migrateUpgradeFromDangling), told as
// the setting's own change. A build before it would keep the dangling value
// and only fail the next upgrade on it ("not in the yard").
// schemaVersion 24 has routes that name whose beads they take: the
// originator (migrateOriginatorRoutes), where a route without one takes the
// operator's alone and one to a group that starts no agents takes every
// originator's. A build before it would drop the field from a record it
// rewrites, as it does when the route is unchanged, and read every route
// as taking every bead.
// schemaVersion 23 has beads that name the person they are for: the
// originator (migrateOriginator), which a build before it would drop from
// a record it rewrites, as it does when the bead is updated. The bead
// would then read as the operator's and build on their subscription.
// schemaVersion 22 has the stays' clocks and the builds per group, label
// and stage (eta.go): the stay fields on the bead row, eta_cells seeded
// from the retained log (migrateETA), and failed session markers. A build
// before it would drop the stay fields from a record it rewrites and would
// write no cells, so estimates would go sparse where it wrote.
// schemaVersion 20 has the operator told of a failed upgrade: their user
// subscribes to install_failed and rolled_back (migrateUpgradeTell), which
// a build before it would drop from the record it rewrites, as it does when
// the user acknowledges their inbox. Nothing is migrated for yards with no
// operator user: one made later carries the subscription from its creation
// (ensureOperator).
// schemaVersion 19 has depots with a test command and nothing else that
// says how they are checked: the fields of a depot's record that named the
// command yardr itself ran before a bead landed (check, check_light,
// light_slot, allow_no_check) and the setting that bounded those runs
// (gate_slots) are gone (migrateChecks). A build before it runs yardr depot
// check, which would find a depot whose check was in the store with none.
// Version 18 has hooks that say what their source's events say of a
// bead (Hook.Says): until then the dispatcher's rules for its sources were
// Go. A build before it would drop the table from a hook it rewrites (hook
// add, a
// pack's import), and with it every move the events made. Nothing is
// migrated: a hook without a table moves no bead, and says so on each event
// about one (yardr hook add --says, or a pack's sources.<source>.says).
// Version 17 has sources that take updates: a source's record holds
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
// Version 8 had an end to a held bead's wait: a peer's intake said how
// long a bead it sent waited to be released (hold_for), which a build
// before it would drop from a record it rewrites, as it does with every
// message it takes from the peer. An intake set to hold for ever would
// then have held for the default, and its beads gone back refused. Nothing
// was migrated, and nothing is now that a bead arrives unheld and the
// intake has no such time (docs/peers.md, "The intake").
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
// (yard-1) minted from the counters bucket, which no build after v0.4.0
// migrates (ErrCounterIDs).
//
// Bump this whenever a build starts writing something every write must
// keep up (an index, a field a read relies on): the stamp is what stops an
// older build from appending to the store without it.
const schemaVersion = 35

var kSchema = []byte("schema")

// ErrNewerSchema means the store was written by a newer yardr: its
// migrations only run forwards, so an older build must not touch it.
var ErrNewerSchema = errors.New("store schema is newer than this yardr knows")

// ErrCounterIDs means the store is at version 1: it holds beads and no
// build stamped it. The migration from it went after v0.4.0, and the ones
// that are left would stamp it as it is, with no bead findable.
var ErrCounterIDs = errors.New("store is from before beads had UUIDs")

// checkSchema refuses a store stamped with a schema this build does not
// know, and one from before any build stamped it, before any migration or
// bucket creation writes to it.
func checkSchema(db *bolt.DB) error {
	return db.View(func(tx *bolt.Tx) error {
		var v []byte
		if m := tx.Bucket(bMeta); m != nil {
			v = m.Get(kSchema)
		}
		if v == nil {
			if b := tx.Bucket(bBeads); b != nil {
				if k, _ := b.Cursor().First(); k != nil {
					return fmt.Errorf("%w; open it once with yardr v0.4.0, which migrates it", ErrCounterIDs)
				}
			}
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

// stampSchema writes version as the schema the store is at.
func stampSchema(tx *bolt.Tx, version uint64) error {
	stamp := binary.BigEndian.AppendUint64(nil, version)
	if bytes.Equal(tx.Bucket(bMeta).Get(kSchema), stamp) {
		return nil
	}
	return tx.Bucket(bMeta).Put(kSchema, stamp)
}

// A migration is one step of the upgrade Open runs, in list order. version
// is the schema the step brings, or 0 when the step brings none: a
// one-time migration, an index build, or the bucket setup between them.
// Each step decides itself whether it runs; the version only says what it
// brings.
type migration struct {
	version uint64
	run     func(tx *bolt.Tx) error
}

// atVersion runs a migration that takes the schema the store was stamped with.
func atVersion(run func(tx *bolt.Tx, from uint64) error) func(tx *bolt.Tx) error {
	return func(tx *bolt.Tx) error { return run(tx, stampedSchema(tx)) }
}

// dropVersions deletes the retired versions bucket: past states of beads
// and deps, which the event log replaced (see the package comment).
func dropVersions(tx *bolt.Tx) error {
	if tx.Bucket(bVersions) == nil {
		return nil
	}
	return tx.DeleteBucket(bVersions)
}

// createBuckets creates every bucket the store reads, for a store from
// before some of them. The steps before it build what they need themselves,
// and the index builds tell a store from before them by the bucket's
// absence, so it runs where it does.
func createBuckets(tx *bolt.Tx) error {
	for _, b := range allBuckets {
		if _, err := tx.CreateBucketIfNotExists(b); err != nil {
			return err
		}
	}
	return nil
}

// openMigrations is the whole upgrade Open runs, in the order it runs it:
// each entry the step and the schema version it brings, 0 when it brings
// none. The order is load-bearing in places (the bucket moves and the index
// builds read buckets the setup below creates; the depot records move
// before every migration that reads one), so a new step goes where it runs,
// and a new versioned migration takes the next version number. home is the
// yard's home; moved and depotsMoved say whether Open moved the crew and
// depot directories to their new names.
func openMigrations(home string, moved, depotsMoved bool) []migration {
	var depots map[string]any
	return []migration{
		// Once: it reads every bead, and a store stamped 12 has none of
		// the old. Its count of renamed records goes to the second phase.
		{version: 12, run: func(tx *bolt.Tx) error {
			if stampedSchema(tx) >= 12 {
				return nil
			}
			var err error
			depots, err = migrateDepotRecords(tx)
			return err
		}},
		{version: 11, run: migrateCrewBucket},
		{run: dropVersions},
		{version: 3, run: atVersion(migrateIndexes)},
		{run: indexSessions},
		{run: indexPanesAndPending},
		{run: indexSessionOrder},
		{run: indexOpenBeads},
		{run: createBuckets},
		{run: migrateFlows},
		{run: migrateDepotKinds},
		{run: migrateSettings},
		{run: migrateOperator},
		{version: 4, run: atVersion(migrateUserCursors)},
		{version: 5, run: atVersion(migratePeerSince)},
		{version: 9, run: atVersion(migrateYardmaster)},
		{version: 10, run: atVersion(migrateTrains)},
		{version: 13, run: atVersion(migrateWagons)},
		{run: migrateFlowPointers},
		{version: 11, run: func(tx *bolt.Tx) error { return migrateCrew(tx, stampedSchema(tx), home, moved) }},
		{version: 12, run: func(tx *bolt.Tx) error { return migrateDepots(tx, stampedSchema(tx), home, depotsMoved, depots) }},
		{version: 19, run: atVersion(migrateChecks)},
		{version: 20, run: atVersion(migrateUpgradeTell)},
		{version: 21, run: atVersion(migrateETA)},
		{version: 22, run: atVersion(migrateETAFailed)},
		{version: 23, run: atVersion(migrateOriginator)},
		{version: 24, run: atVersion(migrateOriginatorRoutes)},
		{version: 25, run: atVersion(migrateUpgradeFromDangling)},
		{version: 26, run: atVersion(migratePeerIntakes)},
		{version: 27, run: atVersion(migrateHearer)},
		{version: 28, run: atVersion(migrateCrewRotation)},
		{version: 29, run: atVersion(migrateGrantGiven)},
		{version: 30, run: atVersion(migrateOperatorRole)},
		{version: 31, run: atVersion(migrateNotificationsTold)},
		{version: 33, run: atVersion(migrateNotificationsTakebackHeld)},
		{version: 34, run: atVersion(migrateNotificationsHeld)},
		{version: 35, run: atVersion(migrateRoutePriority)},
	}
}

// renameTypes rewrites every record that names a bead type typ replaces, or
// holds a subscription's filter where does: beads, routes, flow pointers
// (their key too), hooks' stage maps, the subscriptions of the crew, of
// users and of groups, and the records of imported packs.
func renameTypes(tx *bolt.Tx, typ func(string) string, where func(map[string]string) (map[string]string, bool)) error {
	// rewrite puts back each record of bucket that fix changed, under the
	// key fix gives it.
	rewrite := func(bucket []byte, fix func(key string, raw []byte) (newKey string, v any, err error)) error {
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
	err := rewrite(bBeads, func(k string, raw []byte) (string, any, error) {
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
	err = rewrite(bRoutes, func(k string, raw []byte) (string, any, error) {
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
	err = rewrite(bFlowPtrs, func(k string, raw []byte) (string, any, error) {
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
	err = rewrite(bHooks, func(k string, raw []byte) (string, any, error) {
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
	err = rewrite(bCrew, func(k string, raw []byte) (string, any, error) {
		var r CrewMember
		if err := json.Unmarshal(raw, &r); err != nil || !subs(r.Subscriptions) {
			return k, nil, err
		}
		return k, r, nil
	})
	if err != nil {
		return err
	}
	err = rewrite(bUsers, func(k string, raw []byte) (string, any, error) {
		var u User
		if err := json.Unmarshal(raw, &u); err != nil || !subs(u.Subscriptions) {
			return k, nil, err
		}
		return k, u, nil
	})
	if err != nil {
		return err
	}
	err = rewrite(bGroups, func(k string, raw []byte) (string, any, error) {
		var g Group
		if err := json.Unmarshal(raw, &g); err != nil || !subs(g.Subscriptions) {
			return k, nil, err
		}
		return k, g, nil
	})
	if err != nil {
		return err
	}
	err = rewrite(bPacks, func(k string, raw []byte) (string, any, error) {
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
	return err
}

// mapTypes is a hook's stage map with each bead type replaced by what typ
// gives for it, and whether any was.
func mapTypes(m map[string]string, typ func(string) string) (map[string]string, bool) {
	changed := false
	for k, state := range m {
		t, stage, ok := strings.Cut(k, ":")
		if !ok || typ(t) == t {
			continue
		}
		if !changed {
			m, changed = maps.Clone(m), true
		}
		delete(m, k)
		if _, has := m[MapKey(typ(t), stage)]; !has {
			m[MapKey(typ(t), stage)] = state
		}
	}
	return m, changed
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

// moveDir renames <home>/<old>, a directory of the home that had another
// name until schema 11 or 12, to <home>/<name>, and leaves a link of the old
// name to it: files name others by path (an env_file a credential's key, a
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
	full, err := ExpandPath(path)
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
