// Package store keeps the yard in a single bbolt file. One process owns it
// (bbolt locks the file); everything else reaches the yard through that
// process.
//
// Every write runs in one transaction that also appends its events, so the
// event log explains every step. Events carry a sequence number assigned in
// that transaction; subscribers resume from a sequence and are notified only
// after the commit succeeds.
//
// bbolt rules followed throughout: read transactions stay short (they pin
// pages), values are decoded (copied) before a transaction ends, and writes
// go through db.Batch so a burst shares one fsync. Batch may re-run a write
// function, so a function's only effects are inside its transaction;
// notification happens after the commit.
//
// The file is kept bounded. Past states of beads and deps are not retained:
// the event log is their history, and a "versions" bucket that once kept a
// full snapshot per change (read by nothing but a migration) is dropped on
// open. The log and the ended sessions are pruned by age (see Prune), the
// log never past what a running resident has yet to read (within a bound)
// nor past the oldest live session, and a pruned file shrinks only when
// compacted (Compact) while no server holds it.
//
// Rigs stored before rig kinds existed (git or dir) are stamped "git" on
// open (migrateRigKinds): every rig until then was a repository. The
// stamp needs no schema bump, an older yardr reads past the field.
//
// Reads that serve one resident or follow the log page through indexes
// written at ingest (inbox, events_by_kind) rather than scanning the log
// in one read transaction, and what needs the live sessions (a claim, the
// dispatcher's pass) reads them through an index of their own rather than
// every session ever stored.
package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/config"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrTaken means another session already owns the bead.
	ErrTaken = errors.New("bead already claimed")
	// ErrAtLimit means the group already runs as many sessions as its limit.
	ErrAtLimit = errors.New("group at its session limit")
	// ErrConflict means the record changed since the caller read it (see
	// Expect); it errs as "stale".
	ErrConflict = errors.New("stale")
	// ErrLocked means another process owns the store file.
	ErrLocked = errors.New("store is in use by another process")
)

var (
	bBeads    = []byte("beads")
	bDeps     = []byte("deps")
	bDepFrom  = []byte("dep_from") // <from>\x00<dep id>
	bDepTo    = []byte("dep_to")   // <to>\x00<dep id>
	bNotes    = []byte("notes")    // <bead>\x00<seq>
	bEvents   = []byte("events")   // <seq>
	bEventsBy = []byte("events_by_bead")
	bInbox    = []byte("inbox")          // <resident>\x00<seq>: mail to it, or delivered to it by a subscription
	bEventsKd = []byte("events_by_kind") // <kind>\x00<seq>
	bSessions = []byte("sessions")
	bSessLive = []byte("sessions_live") // <group>\x00<id>: the sessions that are starting or running
	bGroups   = []byte("groups")
	bRoutes   = []byte("routes")
	bRigs     = []byte("rigs")
	bFlows    = []byte("named_flows")   // <name>
	bFlowPtrs = []byte("flow_pointers") // <rig>\x00<type>
	bMeta     = []byte("meta")          // schema version
)

// bVersions kept a snapshot of every bead and dep state; retired (see the
// package comment) and deleted on open.
var bVersions = []byte("versions")

var allBuckets = [][]byte{bBeads, bHandles, bDeps, bDepFrom, bDepTo, bNotes, bEvents, bEventsBy, bInbox, bEventsKd,
	bSessions, bSessLive, bGroups, bRoutes, bRigs, bFlows, bFlowPtrs, bMeta}

type Store struct {
	db    *bolt.DB
	hub   hub
	repos repoCache
	// mentions holds each resident's compiled @mention pattern, so matching
	// an event under the write lock compiles a pattern once per resident.
	mentions mentionCache
	// inboxReads counts the events Inbox decoded, so a test can tell it
	// read only the resident's own.
	inboxReads atomic.Int64
	// sessionReads counts the sessions read through the live index, so a
	// test can tell Claim never looked at an ended one.
	sessionReads atomic.Int64
	// pair is the pairing this yard has open with another (pairing.go):
	// in memory only, so a code is never in the file.
	pair pairings

	// pump publishes committed events in seq order; writers only nudge it.
	nudge  chan struct{}
	done   chan struct{}
	pumped chan struct{} // closed when the pump exits
}

// NoSyncEnv names the variable that, set to 1, opens the store without
// syncing each commit to the disk. It is for tests: a commit's sync is some
// 30ms on macOS and a test suite makes thousands, which was two thirds of
// the gate's time (yardr-a9qf). A store opened so is whole after its process
// dies, and can be lost when the machine does, so nothing but a test's
// throwaway yard may run with it.
const NoSyncEnv = "STORE_NOSYNC"

// Open opens (creating if needed) the store file. It fails with ErrLocked
// if another process holds it, and with ErrNewerSchema if a newer yardr
// wrote it.
func Open(path string) (*Store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second, NoSync: config.Getenv(NoSyncEnv) == "1"})
	if errors.Is(err, bolt.ErrTimeout) {
		return nil, fmt.Errorf("%s: %w", path, ErrLocked)
	} else if err != nil {
		return nil, err
	}
	if err := checkSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := upgrade(db, path); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: upgrade: %w", path, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(bVersions) != nil {
			if err := tx.DeleteBucket(bVersions); err != nil {
				return err
			}
		}
		if err := migrateIndexes(tx, stampedSchema(tx)); err != nil {
			return err
		}
		if err := indexSessions(tx); err != nil {
			return err
		}
		for _, b := range allBuckets {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		if err := migrateFlows(tx); err != nil {
			return err
		}
		if err := migrateRigKinds(tx); err != nil {
			return err
		}
		if err := migrateSettings(tx); err != nil {
			return err
		}
		if err := migrateOperator(tx); err != nil {
			return err
		}
		return setSchema(tx)
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	// Batch holds a write until MaxBatchDelay to coalesce fsyncs. Measured on
	// NVMe (fsync ~3.2ms): the 10ms default makes sequential writes 13.5ms;
	// 1ms costs ~1ms sequentially and is 6x faster than Update at 16 writers.
	db.MaxBatchDelay = time.Millisecond
	s := &Store{db: db, nudge: make(chan struct{}, 1), done: make(chan struct{}), pumped: make(chan struct{})}
	s.hub.init()
	head, err := s.Head(context.Background())
	if err != nil {
		db.Close()
		return nil, err
	}
	go s.pump(head)
	return s, nil
}

func (s *Store) Close(ctx context.Context) error {
	close(s.done)
	<-s.pumped
	s.hub.close()
	return s.db.Close()
}

// pump is the only publisher. Batched writers commit together and return
// in any order, so publishing from each writer could deliver seq 19 before
// 16; reading committed events back from the store in seq order cannot.
func (s *Store) pump(published uint64) {
	defer close(s.pumped)
	for {
		select {
		case <-s.done:
			return
		case <-s.nudge:
		}
		evs, err := s.EventsSince(context.Background(), published, 0)
		if err != nil || len(evs) == 0 {
			continue
		}
		s.hub.publish(evs)
		published = evs[len(evs)-1].Seq
	}
}

// wtx is one write transaction.
type wtx struct {
	*bolt.Tx
	mentions *mentionCache // nil outside Store.write: compiled per use
}

// write runs fn in a batched write transaction, then nudges the pump to
// publish what was committed and wakes wakers.
func (s *Store) write(fn func(*wtx) error) error {
	err := s.db.Batch(func(tx *bolt.Tx) error { return fn(&wtx{Tx: tx, mentions: &s.mentions}) })
	if err != nil {
		return err
	}
	select {
	case s.nudge <- struct{}{}:
	default: // a nudge is already pending; the pump reads everything new
	}
	s.hub.ping()
	return nil
}

func (s *Store) read(fn func(*bolt.Tx) error) error { return s.db.View(fn) }

// get decodes key from bucket into v; false if absent.
func get(tx *bolt.Tx, bucket []byte, key string, v any) (bool, error) {
	raw := tx.Bucket(bucket).Get([]byte(key))
	if raw == nil {
		return false, nil
	}
	return true, json.Unmarshal(raw, v)
}

func put(tx *bolt.Tx, bucket []byte, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return tx.Bucket(bucket).Put([]byte(key), raw)
}

// each decodes every value under prefix in bucket into a fresh T.
func each[T any](tx *bolt.Tx, bucket []byte, prefix string, fn func(key []byte, v *T) error) error {
	c := tx.Bucket(bucket).Cursor()
	p := []byte(prefix)
	for k, raw := c.Seek(p); k != nil && bytes.HasPrefix(k, p); k, raw = c.Next() {
		v := new(T)
		if err := json.Unmarshal(raw, v); err != nil {
			return fmt.Errorf("%s/%q: %w", bucket, k, err)
		}
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

// keys lists the keys under prefix, copied out of the transaction.
func keys(tx *bolt.Tx, bucket []byte, prefix string) []string {
	var out []string
	c := tx.Bucket(bucket).Cursor()
	p := []byte(prefix)
	for k, _ := c.Seek(p); k != nil && bytes.HasPrefix(k, p); k, _ = c.Next() {
		out = append(out, string(k))
	}
	return out
}

func seqKey(n uint64) string {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, n)
	return string(b)
}

// join builds index keys; ids never contain NUL.
func join(parts ...string) string {
	var b bytes.Buffer
	for i, p := range parts {
		if i > 0 {
			b.WriteByte(0)
		}
		b.WriteString(p)
	}
	return b.String()
}

// event appends an event in this transaction. bead is any reference
// resolve accepts; the event records the bead's UUID, and who its yardr
// subscriptions deliver it to.
func (w *wtx) event(bead, kind, actor string, data map[string]any) error {
	if bead != "" {
		u, err := resolve(w.Tx, bead)
		if err != nil {
			return err
		}
		bead = u
	}
	seq, err := w.Bucket(bEvents).NextSequence()
	if err != nil {
		return err
	}
	if data == nil {
		data = map[string]any{}
	}
	e := Event{Seq: seq, ID: fmt.Sprint(seq), Kind: kind, Actor: actor, Data: data, At: now()}
	if bead != "" {
		e.Bead = &bead
		// Before the event is indexed under its bead, so it can tell a
		// first close from a later one.
		if err := w.deliver(&e); err != nil {
			return err
		}
		if err := w.hearStage(&e); err != nil {
			return err
		}
		if err := w.Bucket(bEventsBy).Put([]byte(join(bead, seqKey(seq))), []byte{}); err != nil {
			return err
		}
	}
	if err := index(w.Tx, e); err != nil {
		return err
	}
	return put(w.Tx, bEvents, seqKey(seq), e)
}

// inboxOf lists whom e is addressed to: whom mail is for (a resident, or a
// user), and the residents a subscription delivered it to. What reaches an inbox through a
// resident's --on kinds is found through events_by_kind instead, since
// those are checked when the inbox is read.
func inboxOf(e Event) []string {
	var out []string
	if to := mailReceiver(e); to != "" {
		out = append(out, to)
	}
	d, _ := e.Data["deliveries"].(map[string]any)
	for name := range d {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// index writes e's inbox and kind index entries.
func index(tx *bolt.Tx, e Event) error {
	k := seqKey(e.Seq)
	for _, name := range inboxOf(e) {
		if err := tx.Bucket(bInbox).Put([]byte(join(name, k)), []byte{}); err != nil {
			return err
		}
	}
	return tx.Bucket(bEventsKd).Put([]byte(join(e.Kind, k)), []byte{})
}

// now is UTC truncated to microseconds, so values survive a JSON round trip
// unchanged and compare equal.
func now() time.Time { return clock().UTC().Truncate(time.Microsecond) }

// clock is the time now and newRevision read; a test sets it to put writes
// at chosen moments.
var clock = time.Now

// newRevision returns a ULID (48-bit milliseconds, 80 random bits) in
// Crockford base32. Opaque to clients and unique per minted state.
func newRevision() string {
	var b [16]byte
	ms := uint64(clock().UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	if _, err := rand.Read(b[6:]); err != nil {
		panic(err)
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	// 128 bits as 26 digits: the first carries the top 3 bits.
	out := []byte{alphabet[b[0]>>5]}
	acc, bits := uint64(b[0]&0x1f), 5
	for _, c := range b[1:] {
		acc, bits = acc<<8|uint64(c), bits+8
		for bits >= 5 {
			bits -= 5
			out = append(out, alphabet[(acc>>uint(bits))&0x1f])
		}
	}
	return string(out)
}

// hub fans committed events out to subscribers and pings wakers after
// every committed write, with or without events.
type hub struct {
	mu     sync.Mutex
	subs   map[*subscriber]struct{}
	wakers map[chan struct{}]struct{}
	closed bool
}

type subscriber struct{ ch chan Event }

func (h *hub) init() {
	h.subs = map[*subscriber]struct{}{}
	h.wakers = map[chan struct{}]struct{}{}
}

func (h *hub) addWaker() chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan struct{}, 1)
	if h.closed {
		close(ch)
		return ch
	}
	h.wakers[ch] = struct{}{}
	return ch
}

func (h *hub) removeWaker(ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.wakers[ch]; ok {
		delete(h.wakers, ch)
		close(ch)
	}
}

// ping coalesces: a waker that has not consumed the last ping gets nothing
// new, which is enough because a wake only means "re-read state".
func (h *hub) ping() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.wakers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *hub) add(buffer int) *subscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	sub := &subscriber{ch: make(chan Event, buffer)}
	if h.closed {
		close(sub.ch)
		return sub
	}
	h.subs[sub] = struct{}{}
	return sub
}

func (h *hub) remove(sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[sub]; ok {
		delete(h.subs, sub)
		close(sub.ch)
	}
}

// publish never blocks a writer: a subscriber that falls a whole buffer
// behind is dropped (its channel closes) and resubscribes from its cursor.
func (h *hub) publish(events []Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		for _, e := range events {
			select {
			case sub.ch <- e:
				continue
			default:
			}
			delete(h.subs, sub)
			close(sub.ch)
			break
		}
	}
}

func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for sub := range h.subs {
		delete(h.subs, sub)
		close(sub.ch)
	}
	for ch := range h.wakers {
		delete(h.wakers, ch)
		close(ch)
	}
}
