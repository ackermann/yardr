package store

import (
	"bytes"
	"cmp"
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

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
)

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
const schemaVersion = 19

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

// oldCheckFields are the fields of a depot's record that said, until schema
// 19, what yardr ran before a bead of the depot landed, and
// oldCheckSlotsSetting the setting that said how many of those runs the
// machine took at once.
var oldCheckFields = []string{"check", "check_light", "light_slot", "allow_no_check"}

const oldCheckSlotsSetting = "gate_slots"

// migrateChecks drops what a store held, until schema 19, of the checks
// yardr ran itself: oldCheckFields from each depot's record and the setting
// oldCheckSlotsSetting. A depot whose check was a command in the store and
// that has no test command gets that command as its test command: it is
// what tested the depot, and whoever lands its beads now is given it as
// YARDR_TEST. One event says what each depot lost and which got a test
// command; a store that held none of it is written nothing.
func migrateChecks(tx *bolt.Tx, from uint64) error {
	if from >= 19 {
		return nil
	}
	dropped, tests := map[string]any{}, map[string]any{}
	records := map[string]map[string]json.RawMessage{}
	err := tx.Bucket(bDepots).ForEach(func(k, raw []byte) error {
		rec := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return fmt.Errorf("depot %s: %w", k, err)
		}
		was := map[string]any{}
		for _, f := range oldCheckFields {
			if v, ok := rec[f]; ok {
				var held any
				if err := json.Unmarshal(v, &held); err != nil {
					return fmt.Errorf("depot %s: %s: %w", k, f, err)
				}
				was[f] = held
				delete(rec, f)
			}
		}
		if len(was) == 0 {
			return nil
		}
		check, _ := was["check"].(string)
		var test string
		if v, ok := rec["test"]; ok {
			if err := json.Unmarshal(v, &test); err != nil {
				return fmt.Errorf("depot %s: test: %w", k, err)
			}
		}
		if check != "" && test == "" {
			rec["test"], _ = json.Marshal(check)
			tests[string(k)] = check
		}
		dropped[string(k)], records[string(k)] = was, rec
		return nil
	})
	if err != nil {
		return err
	}
	for k, rec := range records {
		if err := put(tx, bDepots, k, rec); err != nil {
			return err
		}
	}
	data := map[string]any{}
	if len(dropped) > 0 {
		data["dropped"] = dropped
	}
	if len(tests) > 0 {
		data["test"] = tests
	}
	var slots string
	if had, err := get(tx, bSettings, oldCheckSlotsSetting, &slots); err != nil {
		return err
	} else if had {
		if err := tx.Bucket(bSettings).Delete([]byte(oldCheckSlotsSetting)); err != nil {
			return err
		}
		data[oldCheckSlotsSetting] = slots
	}
	if len(data) == 0 {
		return nil
	}
	data["from"] = "checks"
	return (&wtx{Tx: tx}).event("", "depots_migrated", "yardr", data)
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
		kind, from = agents.DefaultKind, "default"
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
// operator named stays: a flow called web.epic keeps its name, as does a
// pointer at it or a bead's pin that names it while the store has that flow
// (migrateFlowPointers). Events already logged are not rewritten.
func migrateTrains(tx *bolt.Tx, from uint64) error {
	if from >= 10 {
		return nil
	}
	return renameTypes(tx, "types "+oldTypeTrain+" and "+oldTypeWagon, oldBeadType, trainWhere)
}

// trainFlowName is the name a flow of trains or wagons has now, for one
// named in the words of before schema 10, and whether name was: <name>.epic
// is <name>.train and <name>.part is <name>.wagon, and the type's own file
// of a repository, type/epic or type/part, is type/train or type/wagon.
func trainFlowName(name string) (string, bool) {
	for old, typ := range map[string]string{oldTypeTrain: TypeTrain, oldTypeWagon: TypeWagon} {
		if head, ok := strings.CutSuffix(name, "."+old); ok && head != "" {
			return head + "." + typ, true
		}
		if name == "type/"+old {
			return "type/" + typ, true
		}
	}
	return name, false
}

// migrateFlowPointers points a depot, or a depot's type, at the flow in
// today's words where its pointer names one in those of before schema 10
// (trainFlowName): train -> yardr.epic, from when the repository's file was
// yardr.epic.flow, becomes train -> yardr.train. The pin of a bead that is
// not closed is a pointer of the same kind (resolveFlow looks both up the
// same way) and is rewritten by the same rule. Until v0.7.0 such a name
// was looked up under both words, and that release, which stopped, rewrote
// none: the depot's flows, and the pinned bead's, were not found. A pointer
// or a pin at a flow the store has under the old name is left, since it
// names that flow and resolves (migrateFlows made <depot>.epic, which
// migrateTrains kept). The repository cannot be read here (git does not run
// inside a transaction), so a file still called <name>.epic.flow is not
// looked for: a pointer at it stops resolving until the file has the new
// name. One event says what each pointer and each pin was. It looks on
// every open, not by the store's stamp: a store v0.7.0 opened is stamped as
// this build stamps it, and a second run finds no such pointer or pin.
func migrateFlowPointers(tx *bolt.Tx) error {
	// renamed is the name in today's words for a pointer or pin to rewrite.
	renamed := func(flow string) (string, bool) {
		name, old := trainFlowName(flow)
		return name, old && tx.Bucket(bFlows).Get([]byte(flow)) == nil
	}
	var ptrs []FlowPointer
	was := map[string]any{}
	err := each(tx, bFlowPtrs, "", func(_ []byte, p *FlowPointer) error {
		name, ok := renamed(p.Flow)
		if !ok {
			return nil
		}
		was[p.String()] = map[string]any{"was": p.Flow, "flow": name}
		p.Flow = name
		ptrs = append(ptrs, *p)
		return nil
	})
	if err != nil {
		return err
	}
	pins := map[string]beadRow{}
	pinned := map[string]any{}
	err = each(tx, bBeads, "", func(k []byte, r *beadRow) error {
		pin := PinnedFlow(&r.Bead)
		name, ok := renamed(pin)
		if !ok || r.Status == "closed" {
			return nil
		}
		pinned[r.ID] = map[string]any{"was": pin, "flow": name}
		r.Metadata["flow"] = name
		pins[string(k)] = *r
		return nil
	})
	if err != nil || len(ptrs)+len(pins) == 0 {
		return err
	}
	for _, p := range ptrs {
		if err := put(tx, bFlowPtrs, join(p.Depot, p.Type), p); err != nil {
			return err
		}
	}
	for k, r := range pins {
		if err := put(tx, bBeads, k, r); err != nil {
			return err
		}
	}
	data := map[string]any{"from": "types " + oldTypeTrain + " and " + oldTypeWagon}
	if len(was) > 0 {
		data["pointers"] = was
	}
	if len(pinned) > 0 {
		data["beads"] = pinned
	}
	return (&wtx{Tx: tx}).event("", "flows_migrated", "yardr", data)
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

// The words schemas 9 to 12 replaced, as a store from before them holds
// them. Only the migrations below read them: what is given to this build
// (a type, a filter, a flag, a setting's key) is in today's words. The
// kinds of events already logged are the exception (oldKinds, in crew.go).
const (
	oldYardmasterSetting = "mayor"
	oldTypeTrain         = "epic"
	oldTypeWagon         = "part"
	oldCrewWord          = "resident"
	oldCrewBucket        = "residents"
	oldCrewDir           = "residents"
	oldDepotWord         = "rig"
	oldDepotBucket       = "rigs"
	oldDepotsDir         = "rigs"
)

// oldBeadType is the bead type t names in a store from before schema 10: t
// itself, or the type that replaced it.
func oldBeadType(t string) string {
	switch t {
	case oldTypeTrain:
		return TypeTrain
	case oldTypeWagon:
		return TypeWagon
	}
	return t
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

// trainWhere is a subscription's filter with the words trains had until
// schema 10 replaced, and whether any was: the key epic is train, type=epic
// and type=part name the types as they are now, and a kind that said epic
// or part names the kind new events carry (oldTrainKinds).
func trainWhere(where map[string]string) (map[string]string, bool) {
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
		case k == oldTypeTrain:
			if _, has := where[TypeTrain]; has {
				v = where[TypeTrain]
			}
			set(k, TypeTrain, v)
		case k == "type" && oldBeadType(v) != v:
			set(k, k, oldBeadType(v))
		case k == "kind" && oldTrainKinds[v] != "":
			set(k, k, oldTrainKinds[v])
		}
	}
	return out, changed
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

// depotWhere is a subscription's filter with the words a depot had until
// schema 12 replaced, and whether any was: the key rig is depot, and a kind
// that said rig names the kind new events carry (oldDepotKinds).
func depotWhere(where map[string]string) (map[string]string, bool) {
	out, changed := depotWhereOne(where)
	for k, v := range out {
		if !strings.Contains(v, whereSep) {
			continue
		}
		vals := WhereValues(v)
		for i, one := range vals {
			if w, ok := depotWhereOne(map[string]string{k: one}); ok {
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

func depotWhereOne(where map[string]string) (map[string]string, bool) {
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
