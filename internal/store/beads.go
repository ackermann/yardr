package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

type Bead struct {
	// ID is the bead's handle (yard-k3f9): what people, branches and every
	// other record returned by the store call it. It never changes.
	ID string `json:"id"`
	// UUID is the bead's identity, a UUIDv7 the store is keyed by.
	UUID        string         `json:"uuid"`
	Title       string         `json:"title"`
	Body        string         `json:"body,omitempty"`
	Type        string         `json:"type"`
	Stage       string         `json:"stage"`
	Status      string         `json:"status"`
	Priority    int            `json:"priority"`
	Depot       *string        `json:"depot,omitempty"`
	Labels      []string       `json:"labels"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Hold        bool           `json:"hold,omitempty"`
	Group       *string        `json:"group,omitempty"`
	Session     *string        `json:"session,omitempty"`
	CloseReason *string        `json:"close_reason,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	ClosedAt    *time.Time     `json:"closed_at,omitempty"`
	CreatedBy   *string        `json:"created_by,omitempty"`
	// Revision is an opaque token that changes on every real state change.
	Revision string `json:"revision"`
	// Train is the handle of the train the bead is a child of (see Family),
	// set on the beads a listing returns (ListBeads, Ready)
	// so a table of them can show it without a lookup per row. It is
	// derived from the parent edges, never stored; "" for no train, and on a
	// bead returned by anything other than a listing.
	Train string `json:"train,omitempty"`
	// Peer is the bead's counterpart in a linked yard, for a bead that came
	// from one (peerbeads.go) or was sent to one (peersend.go): read from
	// its record where a bead is read (GetBead and the listings), never
	// stored on the bead, and nil on a bead returned by a write. From and
	// Away say which it is.
	Peer *BeadPeer `json:"peer,omitempty"`
}

// beadRow is what the beads bucket holds: the bead plus bookkeeping that is
// part of its served representation.
type beadRow struct {
	Bead
	LastActor string `json:"last_actor,omitempty"`
	Notes     int    `json:"notes,omitempty"`
	// Failures counts the sessions that died or failed in the bead's
	// current stage since it entered the stage or was last un-held (see
	// FailureCount). On the bead, so it survives the log and the ended
	// sessions being pruned, and un-holding resets it rather than being
	// read back from an event.
	Failures int `json:"failures,omitempty"`
}

type NewBead struct {
	Title    string
	Body     string
	Type     string
	Stage    string
	Priority int
	Depot    string
	Labels   []string
	Prefix   string // handle prefix; defaults to the depot name, else "yd"
	ID       string // explicit handle (imports, journals); must be unused
	Metadata map[string]any
	// Links are made with the bead, in the same write: a link that is
	// refused (the issue has a tracker) creates no bead.
	Links []NewLink
	// Untracked makes the bead only for a thread no open bead tracks, which
	// is how an event makes one (Hook's Makes): where an open bead tracks
	// an issue Links would have this one track, no bead is created and the
	// error is ErrConflict, since whoever asked had read that none did. A
	// closed bead that tracks it hands the link on and keeps a relates
	// link, as after MoveLink. The check is in the write that makes the
	// bead, so two events about one thread make one bead.
	Untracked bool
	// DiscoveredFrom names the bead this one was found while working on
	// (a handle or UUID, open or closed, in any depot). The discovered-from
	// edge is made with the bead, in the same write: an origin that does
	// not exist creates no bead. Nothing of the origin is inherited.
	DiscoveredFrom string
	// BlockedBy names the beads that must close before this one is ready
	// (handles or UUIDs). The blocks edges are made with the bead, in the
	// same write: a blocker that does not exist, or one named twice,
	// creates no bead, so the bead is never there unblocked for a route to
	// hand out.
	BlockedBy []string
}

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// validName is the shape of depot names, bead types and dep kinds. They end up
// in paths (depot clones, flow files), BDP type URLs and route matches, so no
// slash or whitespace, and one case so "Bug" and "bug" are not two types.
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func checkName(what, name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("%s %q: use lowercase letters, digits, '.', '_' or '-' (starting with a letter or digit)", what, name)
	}
	return nil
}

// loadBead reads the bead ref names (see resolve).
func (w *wtx) loadBead(ref string) (*beadRow, error) {
	return loadBead(w.Tx, ref)
}

func loadBead(tx *bolt.Tx, ref string) (*beadRow, error) {
	u, err := resolve(tx, ref)
	if err != nil {
		return nil, err
	}
	row := new(beadRow)
	ok, err := get(tx, bBeads, u, row)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("bead %s: %w", ref, ErrNotFound)
	}
	return row, nil
}

// stateJSON is the bead's state for change detection: everything but the
// fields a mint itself sets.
func stateJSON(r *beadRow) []byte {
	c := *r
	c.Revision, c.UpdatedAt, c.LastActor = "", time.Time{}, ""
	b, _ := json.Marshal(c)
	return b
}

// saveBead writes row if it differs from old (nil when creating), minting
// a fresh revision and retaining the new state. It reports whether it
// wrote, so callers log events only for real changes.
func (w *wtx) saveBead(old, row *beadRow, actor string) (bool, error) {
	if old != nil && bytes.Equal(stateJSON(old), stateJSON(row)) {
		return false, nil
	}
	return true, w.mintBead(row, actor)
}

// mintBead writes row under a fresh revision and keeps it in, or out of,
// the open index. Every write of a bead by a running store goes through
// here, so the index is exact; what writes the beads bucket without it (the
// migrations on open) changes no bead's status or key, but for the move to
// UUIDs, which fills the index itself.
func (w *wtx) mintBead(row *beadRow, actor string) error {
	row.Revision = newRevision()
	row.UpdatedAt = now()
	row.LastActor = actor
	row.Train, row.Peer = "", nil // derived on reading, never stored
	if err := put(w.Tx, bBeads, row.UUID, row); err != nil {
		return err
	}
	// Most mints leave the status as it was: the index is written only
	// when it changes, so they dirty no page of it.
	idx, key := w.Bucket(bBeadOpen), []byte(row.UUID)
	switch in := idx.Get(key) != nil; {
	case row.open() && !in:
		return idx.Put(key, []byte{})
	case !row.open() && in:
		return idx.Delete(key)
	}
	return nil
}

func (b Bead) open() bool { return b.Status == "open" }

// indexOpenBeads builds the open index, once, for a store from before it.
func indexOpenBeads(tx *bolt.Tx) error {
	if tx.Bucket(bBeadOpen) != nil || tx.Bucket(bBeads) == nil {
		return nil
	}
	if _, err := tx.CreateBucket(bBeadOpen); err != nil {
		return err
	}
	return fillOpenBeads(tx)
}

// fillOpenBeads puts every open bead in the open index.
func fillOpenBeads(tx *bolt.Tx) error {
	return each(tx, bBeads, "", func(k []byte, r *beadRow) error {
		if !r.open() {
			return nil
		}
		return tx.Bucket(bBeadOpen).Put(k, []byte{})
	})
}

// mintTouched re-mints a bead whose representation changed through another
// record (a note, an edge).
func (w *wtx) mintTouched(id, actor string) error {
	row, err := w.loadBead(id)
	if err != nil {
		return err
	}
	return w.mintBead(row, actor)
}

// createdData is the data of b's created event: its title, and where it
// starts (the stage, the type, and the depot when it has one). An advanced
// event names its stages; without the stage here, a reader that rebuilds a
// bead from the log could only guess that it started where its flow starts.
func createdData(b *Bead) map[string]any {
	data := map[string]any{"title": b.Title, "stage": b.Stage, "type": b.Type}
	if b.Depot != nil {
		data["depot"] = *b.Depot
	}
	return data
}

// CreateBead mints a UUID and a handle (<prefix>-<random>), or uses nb.ID
// as the handle, and creates the bead.
func (s *Store) CreateBead(ctx context.Context, nb NewBead, actor string) (*Bead, error) {
	if nb.ID != "" && !validID.MatchString(nb.ID) {
		return nil, fmt.Errorf("bead id %q: use letters, digits, '.', '_' or '-'", nb.ID)
	}
	if isUUID(nb.ID) {
		return nil, fmt.Errorf("bead id %q: a handle cannot look like a UUID", nb.ID)
	}
	prefix := nb.Prefix
	if prefix == "" {
		prefix = nb.Depot
	}
	if prefix == "" {
		prefix = "yd"
	}
	typ := or(nb.Type, "task")
	if err := checkName("bead type", typ); err != nil {
		return nil, err
	}
	var depot *string
	if nb.Depot != "" {
		depot = &nb.Depot
	}
	// The links are fixed in a copy: the slice is the caller's.
	nb.Links = slices.Clone(nb.Links)
	for i := range nb.Links {
		if err := checkLink(&nb.Links[i]); err != nil {
			return nil, err
		}
	}
	pin := PinnedFlow(&Bead{Metadata: nb.Metadata})
	repo, err := s.repoFor(ctx, depot, typ, pin)
	if err != nil {
		return nil, err
	}
	var created *Bead
	err = s.write(func(w *wtx) error {
		if nb.Depot != "" && w.Bucket(bDepots).Get([]byte(nb.Depot)) == nil {
			return fmt.Errorf("depot %s: %w", nb.Depot, ErrNotFound)
		}
		f, _, err := resolveFlow(w.Tx, depot, typ, pin, repo)
		if err != nil {
			return changedMeanwhile(err)
		}
		// Without a stage the bead starts where its flow starts (backlog
		// in the starter flow, where the yardmaster shapes it), so create works
		// for any flow, a train's too; "new" is only for beads no flow
		// governs.
		stage := or(nb.Stage, firstStage(f))
		if err := checkCreateStage(f, stage); err != nil {
			return err
		}
		// A wagon is created before it can be a train's child.
		if err := checkWagon(w.Tx, f, &beadRow{Bead: Bead{Type: typ}}, stage); err != nil {
			return err
		}
		row := &beadRow{Bead: Bead{
			Title: nb.Title, Body: nb.Body, Type: typ, Stage: stage,
			Status: "open", Priority: nb.Priority, Labels: dedupe(nb.Labels), CreatedAt: now(), CreatedBy: &actor,
			Metadata: nb.Metadata,
		}}
		row.Depot = depot
		if err := w.insertBead(row, prefix, nb.ID, actor); err != nil {
			return err
		}
		created = &row.Bead
		if err := w.event(row.UUID, "created", actor, createdData(&row.Bead)); err != nil {
			return err
		}
		seq := w.Bucket(bEvents).Sequence()
		if nb.DiscoveredFrom != "" {
			origin, err := w.loadBead(nb.DiscoveredFrom)
			if err != nil {
				return fmt.Errorf("discovered from: %w", err)
			}
			if err := w.addDep(origin, row, DepDiscoveredFrom, "", "", actor); err != nil {
				return err
			}
			// The edge re-minted the bead: go on with it as stored.
			if _, err := get(w.Tx, bBeads, row.UUID, row); err != nil {
				return err
			}
		}
		if len(nb.BlockedBy) > 0 {
			blockers, err := newBlockers(w.Tx, nb.BlockedBy)
			if err != nil {
				return err
			}
			for _, blocker := range blockers {
				// A bead that blocks nothing yet closes no cycle; the
				// check is AddDep's, should that ever change.
				if err := checkCycle(w.Tx, blocker, row, ""); err != nil {
					return err
				}
				if err := w.addDep(blocker, row, DepBlocks, "", "", actor); err != nil {
					return err
				}
			}
			if _, err := get(w.Tx, bBeads, row.UUID, row); err != nil {
				return err
			}
		}
		for _, nl := range nb.Links {
			if nb.Untracked && nl.Kind == LinkTracks {
				if err := w.handOn(nl.URL, row, actor); err != nil {
					return err
				}
			}
			if _, _, err := w.addLink(row, nl, actor, nil); err != nil {
				return err
			}
		}
		// A bead enters its first stage as it is created, and tracks what
		// it was created with.
		return w.queueIssueMoves(row, seq, row.Stage)
	})
	return created, err
}

// insertBead gives a new bead its UUID, from its creation time and after
// every key already stored, and its handle: handle when given, else one
// minted under prefix. Then it mints the bead.
func (w *wtx) insertBead(row *beadRow, prefix, handle, actor string) error {
	last, _ := w.Bucket(bBeads).Cursor().Last()
	u := after(string(last), uuidAt(row.CreatedAt))
	if handle == "" {
		h, err := mintHandle(w.Tx, prefix, u)
		if err != nil {
			return err
		}
		handle = h
	} else if w.Bucket(bHandles).Get([]byte(handle)) != nil {
		return fmt.Errorf("bead %s already exists", handle)
	}
	row.ID, row.UUID = handle, formatUUID(u)
	if err := w.Bucket(bHandles).Put([]byte(row.ID), []byte(row.UUID)); err != nil {
		return err
	}
	return w.mintBead(row, actor)
}

// GetBead returns the bead ref names: its UUID, its handle, or a unique
// handle prefix.
func (s *Store) GetBead(ctx context.Context, ref string) (*Bead, error) {
	var b *Bead
	err := s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, ref)
		if err != nil {
			return err
		}
		b = &row.Bead
		return withPeer(tx, b)
	})
	return b, err
}

type BeadFilter struct {
	All   bool // include closed
	Stage string
	Type  string
	Depot string
	Label string
	Group string
	// Train keeps the train (a handle or UUID) and its children, the train
	// first.
	Train string
	// DiscoveredFrom keeps the beads found from this one (a handle or
	// UUID): those with a discovered-from edge from it.
	DiscoveredFrom string
	// Since keeps the beads created at it or later.
	Since time.Time
	// Limit keeps the newest that many, by creation, of what the rest
	// kept; 0 is all.
	Limit int
}

func (s *Store) ListBeads(ctx context.Context, f BeadFilter) ([]Bead, error) {
	var found map[string]bool
	if f.DiscoveredFrom != "" {
		err := s.read(func(tx *bolt.Tx) error {
			origin, err := resolve(tx, f.DiscoveredFrom)
			if err != nil {
				return err
			}
			found = map[string]bool{}
			for _, d := range edges(tx, bDepFrom, origin, DepDiscoveredFrom) {
				found[d.To] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	train := ""
	if f.Train != "" {
		err := s.read(func(tx *bolt.Tx) error {
			row, err := loadBead(tx, f.Train)
			if err != nil {
				return err
			}
			if row.Type != TypeTrain {
				return fmt.Errorf("%s is a %s, not a train", row.ID, row.Type)
			}
			train = row.ID
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	bs, err := s.beadsWhere(!f.All, f.Since, f.Limit, func(tx *bolt.Tx, b *Bead) bool {
		return (f.All || b.open()) &&
			(train == "" || b.ID == train || b.Train == train) &&
			(found == nil || found[b.UUID]) &&
			(f.Stage == "" || b.Stage == f.Stage) &&
			// Journals and memories are listed only when asked for by type.
			((f.Type == "" && !NotWork[b.Type]) || b.Type == f.Type) &&
			(f.Depot == "" || (b.Depot != nil && *b.Depot == f.Depot)) &&
			(f.Label == "" || slices.Contains(b.Labels, f.Label)) &&
			(f.Group == "" || (b.Group != nil && *b.Group == f.Group))
	})
	if train != "" {
		// The train heads its children, whatever its priority.
		i := slices.IndexFunc(bs, func(b Bead) bool { return b.ID == train })
		if i > 0 {
			e := bs[i]
			copy(bs[1:i+1], bs[:i])
			bs[0] = e
		}
	}
	return bs, err
}

// Ready lists open, unheld beads whose blockers are all released, assigned or not.
func (s *Store) Ready(ctx context.Context) ([]Bead, error) {
	var out []Bead
	err := s.withUntilRepos(ctx, func(look *untilLooks) error {
		var err error
		out, err = s.beadsWhere(true, time.Time{}, 0, func(tx *bolt.Tx, b *Bead) bool {
			return ready(tx, b, look)
		})
		return err
	})
	return out, err
}

// ReadyBead is a ready bead no group owns yet, as much of it as says where
// it is routed and whether it can start now: the dispatcher asks for all of
// them on every pass and starts few, so it reads a bead (GetBead) when it
// starts it. The handle is under "bead", not "id", so a daemon and a server
// of different builds read no bead in each other's answer instead of
// starting one without its body.
type ReadyBead struct {
	Bead   string   `json:"bead"`
	Stage  string   `json:"stage"`
	Type   string   `json:"type"`
	Depot  *string  `json:"depot,omitempty"`
	Labels []string `json:"labels,omitempty"`
	// Model is the model the bead pins (metadata model=<name>), or "".
	Model string `json:"model,omitempty"`

	priority int
}

// Dispatchable lists ready beads that no group owns yet, by priority, then
// creation time.
func (s *Store) Dispatchable(ctx context.Context) ([]ReadyBead, error) {
	out := []ReadyBead{}
	err := s.withUntilRepos(ctx, func(look *untilLooks) error {
		out = []ReadyBead{}
		return s.read(func(tx *bolt.Tx) error {
			rows, err := s.openBeads(tx)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if r.Group != nil || !ready(tx, &r.Bead, look) {
					continue
				}
				model, _ := r.Metadata["model"].(string)
				out = append(out, ReadyBead{Bead: r.ID, Stage: r.Stage, Type: r.Type, Depot: copyOf(r.Depot),
					Labels: slices.Clone(r.Labels), Model: model, priority: r.Priority})
			}
			return nil
		})
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].priority < out[j].priority })
	return out, err
}

// NotWork lists bead types that carry knowledge, not work: they are never
// ready and never routed.
var NotWork = map[string]bool{TypeJournal: true, TypeMemory: true}

const (
	TypeJournal = "journal"
	TypeMemory  = "memory"
)

func ready(tx *bolt.Tx, b *Bead, look *untilLooks) bool {
	return !NotWork[b.Type] && b.open() && !b.Hold && len(openBlockers(tx, b, look)) == 0
}

// openBlockers lists the UUIDs of beads that hold b now: its blocks edges
// whose blocker is unreleased and that hold in its stage. An edge holds
// while its blocker is unreleased and (it names no stage or b is in that
// stage), so a bead held only at approved routes in new and waits in
// approved; a blocker an until edge names releases once it reaches that
// stage along done (released).
func openBlockers(tx *bolt.Tx, b *Bead, look *untilLooks) []string {
	var out []string
	for _, k := range keys(tx, bDepTo, b.UUID+"\x00") {
		var d depRow
		if ok, _ := get(tx, bDeps, k[len(b.UUID)+1:], &d); !ok || d.Kind != "blocks" {
			continue
		}
		if d.At != "" && d.At != b.Stage {
			continue
		}
		var blocker beadRow
		ok, _ := get(tx, bBeads, d.From, &blocker)
		if !ok || released(tx, &blocker, d.Until, look) {
			continue
		}
		out = append(out, d.From)
	}
	return out
}

// released reports whether blocker holds nothing through an edge with
// until: it is closed, or until names a stage it has reached. Reaching is
// being in the stage or past it along done; without a flow to walk, only
// the stage itself releases. Closing always releases, so won't-do releases
// exactly like merging.
func released(tx *bolt.Tx, blocker *beadRow, until string, look *untilLooks) bool {
	if blocker.Status == "closed" {
		return true
	}
	if until == "" {
		return false
	}
	if blocker.Stage == until {
		return true
	}
	f := look.flowOf(tx, blocker)
	return f != nil && f.doneReaches(until, blocker.Stage)
}

// beadsWhere returns matching beads by priority, then creation time: the
// order of the beads bucket, whose UUIDv7 keys start with it, and of the
// open index, which has the same keys. match sees each bead with its Train
// set, and leaves it as it is. With open, match keeps no closed bead, and
// only the open ones are read, through the index (openBeads): the store
// keeps every bead, and most are closed, so a listing of open beads should
// not scale with them.
//
// With a limit (0 is none) the newest that many matches are kept, and with
// since only beads created at it or later. Either is read from the newest
// bead back and ends where it is full or the keys are older than since, so
// a listing of every bead costs what it shows and not what the store keeps.
func (s *Store) beadsWhere(open bool, since time.Time, limit int, match func(*bolt.Tx, *Bead) bool) ([]Bead, error) {
	out := []Bead{}
	err := s.read(func(tx *bolt.Tx) error {
		// A key's time is its bead's creation or later (insertBead), so
		// below the first key since could have, every bead is older.
		floor := ""
		if !since.IsZero() {
			var u [16]byte
			binary.BigEndian.PutUint64(u[:8], uuidHigh(since)&^0xffff)
			floor = formatUUID(u)
		}
		full := func(k string) bool { return limit > 0 && len(out) >= limit || k < floor }
		keep := func(b *Bead) bool { return match(tx, b) && !b.CreatedAt.Before(since) }
		// From the newest back, whether or not there is a limit: the order
		// of the bucket is put back below.
		defer func() { slices.Reverse(out) }()
		if open {
			rows, err := s.openBeads(tx)
			if err != nil {
				return err
			}
			for i := len(rows) - 1; i >= 0 && !full(rows[i].UUID); i-- {
				if !keep(&rows[i].Bead) {
					continue
				}
				b := rows[i].Bead.clone()
				if err := withPeer(tx, &b); err != nil {
					return err
				}
				out = append(out, b)
			}
			return nil
		}
		n := 0
		defer func() { s.beadReads.Add(int64(n)) }()
		c := tx.Bucket(bBeads).Cursor()
		for k, raw := c.Last(); k != nil && !full(string(k)); k, raw = c.Prev() {
			r := new(beadRow)
			if err := json.Unmarshal(raw, r); err != nil {
				return fmt.Errorf("%s/%q: %w", bBeads, k, err)
			}
			n++
			r.Train = trainOf(tx, r.UUID)
			if err := withPeer(tx, &r.Bead); err != nil {
				return err
			}
			if keep(&r.Bead) {
				out = append(out, r.Bead)
			}
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out, err
}

// openSnap is the open beads as one read decoded them, kept for the reads
// that follow until the store is written to. The dispatcher's pass asks
// four questions of the open beads (ExpireMemories, the trains in in-pr,
// Dispatchable, Stranded) and a listing asks on every page, mostly of a
// store nothing has changed in since the last one asked: decoding them for
// each was most of what an idle server did at 20,000 beads.
type openSnap struct {
	mu   sync.Mutex
	ok   bool
	txid int
	rows []*beadRow
}

// openBeads is every open bead, in the order of the beads bucket, read
// through the open index, each with its Train set and no Peer. They are
// decoded once for a state of the store: a read transaction carries the id
// of the last write committed before it, so what one decoded is what any
// other with the same id would. The rows are therefore shared by every
// reader: they are read and never changed, and a bead handed out of the
// store is a copy (Bead.clone).
func (s *Store) openBeads(tx *bolt.Tx) ([]*beadRow, error) {
	decode := func() ([]*beadRow, error) {
		rows := []*beadRow{}
		for _, u := range keys(tx, bBeadOpen, "") {
			r := new(beadRow)
			if ok, err := get(tx, bBeads, u, r); err != nil {
				return nil, err
			} else if !ok {
				continue
			}
			r.Train = trainOf(tx, r.UUID)
			rows = append(rows, r)
		}
		s.beadReads.Add(int64(len(rows)))
		return rows, nil
	}
	// A write transaction sees its own changes, which its id does not name
	// until it commits.
	if tx.Writable() {
		return decode()
	}
	// Held while decoding, so readers that arrive together decode once.
	s.open.mu.Lock()
	defer s.open.mu.Unlock()
	if s.open.ok && s.open.txid == tx.ID() {
		return s.open.rows, nil
	}
	rows, err := decode()
	// A read that began before the write the kept rows are from does not
	// replace them with older ones.
	if err == nil && (!s.open.ok || tx.ID() > s.open.txid) {
		s.open.ok, s.open.txid, s.open.rows = true, tx.ID(), rows
	}
	return rows, err
}

// clone is b sharing nothing with it but its Peer, which a row of openBeads
// never has: a caller is free to change what a listing handed it.
func (b *Bead) clone() Bead {
	c := *b
	c.Depot, c.Group, c.Session = copyOf(b.Depot), copyOf(b.Group), copyOf(b.Session)
	c.CloseReason, c.CreatedBy, c.ClosedAt = copyOf(b.CloseReason), copyOf(b.CreatedBy), copyOf(b.ClosedAt)
	c.Labels = slices.Clone(b.Labels)
	if b.Metadata != nil {
		c.Metadata = cloneJSON(b.Metadata).(map[string]any)
	}
	return c
}

func copyOf[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// cloneJSON copies a value as encoding/json decodes one into an any: the
// maps and slices are new, and the rest cannot be changed in place.
func cloneJSON(v any) any {
	switch v := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(v))
		for k, e := range v {
			m[k] = cloneJSON(e)
		}
		return m
	case []any:
		l := make([]any, len(v))
		for i, e := range v {
			l[i] = cloneJSON(e)
		}
		return l
	}
	return v
}

type BeadPatch struct {
	Title       *string
	Body        *string
	Type        *string
	Priority    *int
	Depot       *string
	AddLabels   []string
	RemoveLabel []string
	Metadata    map[string]any // merged
}

// UpdateBead applies p. A stale exp refuses it (see Expect).
func (s *Store) UpdateBead(ctx context.Context, id string, p BeadPatch, exp Expect, actor string) (*Bead, error) {
	// A new pin must name a flow that exists: one naming nothing would
	// only fail later, when dispatch or a move resolves it.
	if p.Type != nil {
		if err := checkName("bead type", *p.Type); err != nil {
			return nil, err
		}
	}
	pin := PinnedFlow(&Bead{Metadata: p.Metadata})
	var repo *RepoFlows
	if pin != "" {
		pre, err := s.GetBead(ctx, id)
		if err != nil {
			return nil, err
		}
		depot, typ := pre.Depot, pre.Type
		if p.Depot != nil {
			depot = p.Depot
		}
		if p.Type != nil {
			typ = *p.Type
		}
		if repo, err = s.repoFor(ctx, depot, typ, pin); err != nil {
			return nil, err
		}
	}
	return s.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
		if err := w.check(ctx, exp, b); err != nil {
			return "", nil, err
		}
		// was holds what each field the patch names was before it: the
		// log is the bead's history (History), and the only place an old
		// title or body is kept. It is a map of its own, so nothing that
		// reads an event's plain values (a mention, a feed entry, a span)
		// takes an old body for something said now.
		changed, was := map[string]any{}, map[string]any{}
		if p.Title != nil {
			was["title"] = b.Title
			b.Title, changed["title"] = *p.Title, *p.Title
		}
		if p.Body != nil {
			was["body"] = b.Body
			b.Body, changed["body"] = *p.Body, true
		}
		if p.Type != nil {
			was["type"] = b.Type
			b.Type, changed["type"] = *p.Type, *p.Type
		}
		if p.Priority != nil {
			was["priority"] = b.Priority
			b.Priority, changed["priority"] = *p.Priority, *p.Priority
		}
		if p.Depot != nil {
			if w.Bucket(bDepots).Get([]byte(*p.Depot)) == nil {
				return "", nil, fmt.Errorf("depot %s: %w", *p.Depot, ErrNotFound)
			}
			was["depot"] = ""
			if b.Depot != nil {
				was["depot"] = *b.Depot
			}
			b.Depot, changed["depot"] = p.Depot, *p.Depot
		}
		if len(p.AddLabels) > 0 || len(p.RemoveLabel) > 0 {
			was["labels"] = slices.Clone(b.Labels)
			labels := dedupe(append(slices.Clone(b.Labels), p.AddLabels...))
			labels = slices.DeleteFunc(labels, func(l string) bool { return slices.Contains(p.RemoveLabel, l) })
			b.Labels = labels
			changed["labels_added"], changed["labels_removed"] = p.AddLabels, p.RemoveLabel
		}
		if len(p.Metadata) > 0 {
			if b.Metadata == nil {
				b.Metadata = map[string]any{}
			}
			// The keys the patch sets, and nil for one the bead did not have.
			before := map[string]any{}
			for k, v := range p.Metadata {
				before[k] = b.Metadata[k]
				b.Metadata[k] = v
			}
			was["metadata"] = before
			changed["metadata"] = p.Metadata
		}
		if len(was) > 0 {
			changed["was"] = was
		}
		if pin != "" {
			if _, _, err := resolveFlow(w.Tx, b.Depot, b.Type, pin, repo); err != nil {
				return "", nil, changedMeanwhile(err)
			}
		}
		return "updated", changed, nil
	})
}

// changeBead loads a bead, applies fn, and when the state really changed
// mints it and logs fn's event.
func (s *Store) changeBead(id, actor string, fn func(*wtx, *beadRow) (string, map[string]any, error)) (*Bead, error) {
	var result *Bead
	err := s.write(func(w *wtx) error {
		b, err := w.changeBead(id, actor, fn)
		result = b
		return err
	})
	return result, err
}

// changeBead is changeBead inside a caller's transaction.
func (w *wtx) changeBead(id, actor string, fn func(*wtx, *beadRow) (string, map[string]any, error)) (*Bead, error) {
	old, err := w.loadBead(id)
	if err != nil {
		return nil, err
	}
	row := cloneRow(old)
	kind, data, err := fn(w, row)
	if err != nil {
		return nil, err
	}
	if row.Stage != old.Stage {
		row.Failures = 0 // attempts are per stage
	}
	wrote, err := w.saveBead(old, row, actor)
	if err != nil {
		return nil, err
	}
	if !wrote {
		return &old.Bead, nil
	}
	return &row.Bead, w.event(row.UUID, kind, actor, data)
}

func cloneRow(r *beadRow) *beadRow {
	raw, _ := json.Marshal(r)
	c := new(beadRow)
	_ = json.Unmarshal(raw, c)
	return c
}

// endSession marks the bead's current session ended with state and clears
// ownership. Shared by Advance, Close, Release and SetHold.
func (w *wtx) endSession(b *beadRow, state string) error {
	if b.Session != nil {
		var sess Session
		ok, err := get(w.Tx, bSessions, *b.Session, &sess)
		if err != nil {
			return err
		}
		if ok && sess.live() {
			t := now()
			sess.State, sess.EndedAt = state, &t
			if err := putSession(w.Tx, &sess); err != nil {
				return err
			}
		}
	}
	b.Group, b.Session = nil, nil
	return nil
}

// Advance moves a bead to the next stage and frees it for routing. When a
// flow governs the bead (see resolveFlow), the move must be one the flow allows, and entering
// a terminal stage closes the bead, with the stage as reason, in the same
// transaction. When the group the bead answers to in its stage has members,
// only they may move it. A stale exp refuses the move (see Expect).
func (s *Store) Advance(ctx context.Context, id, stage string, exp Expect, actor string) (*Bead, error) {
	return s.AdvanceNote(ctx, id, stage, "", exp, actor)
}

// AdvanceNote is Advance with a note attached in the same transaction, so
// a refused move leaves no note behind and whoever picks the bead up in the
// next stage already sees it (a reviewer's requested changes). An empty
// note adds none. A stale exp refuses the move (see Expect).
func (s *Store) AdvanceNote(ctx context.Context, id, stage, note string, exp Expect, actor string) (*Bead, error) {
	b, err := s.advance(ctx, id, note, exp, actor, func(f *Flow, b *beadRow, data map[string]any) (string, error) {
		return stage, checkAdvance(f, b.ID, b.Stage, stage)
	})
	return b, s.refused(ctx, id, "yardr bead advance "+id+" --to "+stage, exp, actor, err)
}

// Done reports how the work in the bead's stage ended and moves it where
// its flow sends that outcome ("" is done), with an optional note, in one
// write like AdvanceNote. The outcome is recorded on the advanced event.
// A bead no flow governs has no outcomes: it is moved with Advance.
func (s *Store) Done(ctx context.Context, id, outcome, note string, exp Expect, actor string) (*Bead, error) {
	outcome = or(strings.TrimSpace(outcome), DefaultOutcome)
	command := "yardr bead done " + id
	if outcome != DefaultOutcome {
		command += " --outcome " + outcome
	}
	b, err := s.advance(ctx, id, note, exp, actor, func(f *Flow, b *beadRow, data map[string]any) (string, error) {
		if f == nil {
			return "", fmt.Errorf("bead %s: no flow governs it, so it has no outcomes; move it with yardr bead advance %s --to <stage>", b.ID, b.ID)
		}
		to, err := f.Resolve(b.Stage, outcome)
		if err != nil {
			return "", fmt.Errorf("bead %s: %w", b.ID, err)
		}
		data["outcome"] = outcome
		return to, nil
	})
	return b, s.refused(ctx, id, command, exp, actor, err)
}

// advance is the move AdvanceNote and Done share: pick names the stage
// under the bead's flow (nil without one), may add to the event's data,
// and refuses by returning an error. A move the flow or the stage's group
// refuses says which, for move_refused (see refused).
func (s *Store) advance(ctx context.Context, id, note string, exp Expect, actor string, pick func(f *Flow, b *beadRow, data map[string]any) (string, error)) (*Bead, error) {
	pre, err := s.GetBead(ctx, id)
	if err != nil {
		return nil, err
	}
	repo, err := s.repoFor(ctx, pre.Depot, pre.Type, PinnedFlow(pre))
	if err != nil {
		return nil, err
	}
	var result *Bead
	err = s.write(func(w *wtx) error {
		// Checked before the note, which re-mints the bead's revision.
		cur, err := w.loadBead(id)
		if err != nil {
			return err
		}
		if err := w.check(ctx, exp, cur); err != nil {
			return err
		}
		if strings.TrimSpace(note) != "" {
			if err := w.addNote(id, NoteNote, note, actor); err != nil {
				return err
			}
		}
		result, err = w.moveBead(id, actor, repo, pick)
		return err
	})
	return result, err
}

// moveBead is the move itself, inside a write: the bead goes to the stage
// pick names under its flow, which is read with repo, the depot's repository
// flows as read before the write. Entering a terminal stage closes the
// bead, and returns it to the yard it came from, if it came from one.
func (w *wtx) moveBead(id, actor string, repo *RepoFlows, pick func(f *Flow, b *beadRow, data map[string]any) (string, error)) (*Bead, error) {
	var stage, outcome string
	var terminal bool
	b, err := w.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
		f, _, err := resolveFlow(w.Tx, b.Depot, b.Type, PinnedFlow(&b.Bead), repo)
		if err != nil {
			return "", nil, changedMeanwhile(err)
		}
		data := withGroup(b, map[string]any{"from": b.Stage})
		if stage, err = pick(f, b, data); err != nil {
			return "", nil, refuse(RefusedNotAllowed, err)
		}
		data["to"] = stage
		outcome, _ = data["outcome"].(string)
		if err := checkWagon(w.Tx, f, b, stage); err != nil {
			return "", nil, refuse(RefusedNotAllowed, err)
		}
		g, err := stageGroup(w.Tx, b)
		if err != nil {
			return "", nil, err
		}
		if err := checkMembers(g, b.ID, b.Stage, actor); err != nil {
			return "", nil, refuse(RefusedNotMember, err)
		}
		terminal = f != nil && f.IsTerminal(stage)
		if err := w.endSession(b, "done"); err != nil {
			return "", nil, err
		}
		b.Stage = stage
		return "advanced", data, nil
	})
	if err != nil || !terminal {
		return b, err
	}
	result, err := w.changeBead(b.UUID, actor, closeWith(stage))
	if err != nil {
		return result, err
	}
	// A bead of another yard's ends here: what its flow came to goes
	// back in this write. A move that named no outcome is told by the
	// stage it ended in.
	return result, w.returnPeerBead(result, or(outcome, stage), nil, actor)
}

// CloseBead closes the bead, ending its session. A stale exp refuses it
// (see Expect). A bead that came from another yard is returned to it as
// refused, with the reason: it left its flow without an outcome of it.
func (s *Store) CloseBead(ctx context.Context, id, reason string, exp Expect, actor string) (*Bead, error) {
	close := closeWith(reason)
	var b *Bead
	err := s.write(func(w *wtx) (err error) {
		b, err = w.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
			if err := w.check(ctx, exp, b); err != nil {
				return "", nil, err
			}
			return close(w, b)
		})
		if err != nil || b.Status != "closed" {
			return err
		}
		return w.returnPeerBead(b, OutcomeRefused, &reason, actor)
	})
	return b, s.refused(ctx, id, "yardr bead close "+id, exp, actor, err)
}

// Expect is what a caller believes about a bead it changes: the session it
// acts for, the stage the bead is in, and its revision. A write made on a
// stale view is refused with ErrConflict and changes nothing, so an agent
// kept or orphaned after its bead moved on cannot end the session that owns
// the bead now, a review form opened before someone else decided cannot
// apply its decision on top, and two writers that read the same revision
// cannot both apply what they read it for (compare-and-swap). An empty
// field expects nothing: a person acting outside any session (the yardmaster,
// Christopher at a shell) is not checked.
//
// Session names the session the caller runs in ($YARDR_SESSION). It must be
// one the store started (an id it does not know is refused), and what it
// is expected to own comes from the store's own record of it, not from the
// caller: the bead the session was started for must still be owned by it;
// any other bead the same pane moves (a reviewer advancing a follow-up it
// filed) is checked as anyone's would be, except that one owned by a live
// session is refused rather than taken from under its agent. A call that
// comes from a session's pane (WithCaller: the process tree above the
// caller holds the pane's shell or the exec runner's script) is checked as
// that session's whatever Session says, so a pane that unsets
// $YARDR_SESSION is still checked, and one naming another session is
// refused.
type Expect struct {
	Session string `json:"session,omitempty"`
	Stage   string `json:"stage,omitempty"`
	// Revision is the bead's revision as the caller read it (Bead.Revision,
	// the BDP ETag). Any real change since mints another.
	Revision string `json:"revision,omitempty"`
}

// check refuses the write of b when e, resolved against the session table
// and the caller's pane, no longer holds (see Expect).
func (w *wtx) check(ctx context.Context, e Expect, b *beadRow) error {
	sess, err := w.callerSession(ctx, e, b)
	if err != nil {
		return err
	}
	owner := ""
	if b.Session != nil {
		owner = *b.Session
	}
	// The session's own bead must still be owned by it; any other bead it
	// moves must be owned by no session at all.
	other := sess != nil && sess.Bead != b.UUID
	sessionOK := sess == nil || sess.ID == owner
	if other {
		sessionOK = owner == ""
	}
	if sessionOK && (e.Stage == "" || e.Stage == b.Stage) {
		// Said last: a bead that moved on says so, not only that it
		// changed.
		if e.Revision != "" && e.Revision != b.Revision {
			return fmt.Errorf("%w: bead %s is at revision %s, not %s; read it again", ErrConflict, b.ID, b.Revision, e.Revision)
		}
		return nil
	}
	if owner == "" {
		owner = "no session"
	}
	state := ""
	if b.Status == "closed" {
		state = " (closed)"
	}
	return fmt.Errorf("%w: bead %s is now in %s%s owned by %s; expected %s", ErrConflict, b.ID, b.Stage, state, owner, w.want(e, sess, other))
}

// callerSession is the session the write of b is checked as: the one whose
// pane the call came from when the store knows it, else the one e names,
// which must exist. nil when neither names one.
func (w *wtx) callerSession(ctx context.Context, e Expect, b *beadRow) (*Session, error) {
	pane, err := paneSession(w.Tx, callerOf(ctx))
	if err != nil {
		return nil, err
	}
	switch {
	case pane != nil && e.Session != "" && e.Session != pane.ID:
		return nil, fmt.Errorf("%w: bead %s: this pane is session %s, not %s", ErrConflict, b.ID, pane.ID, e.Session)
	case pane != nil:
		return pane, nil
	case e.Session == "":
		return nil, nil
	}
	sess := new(Session)
	ok, err := get(w.Tx, bSessions, e.Session, sess)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("bead %s: session %s: %w; a move is checked as the session $YARDR_SESSION names, which must be one this yard started", b.ID, e.Session, ErrNotFound)
	}
	return sess, nil
}

// want names what e expected, for the stale error; other says the bead is
// not the session's own (see Expect).
func (w *wtx) want(e Expect, sess *Session, other bool) string {
	var parts []string
	if e.Stage != "" {
		parts = append(parts, "stage "+e.Stage)
	}
	switch {
	case other:
		parts = append(parts, "no session (session "+sess.ID+" owns "+handlesIn(w.Tx).of(sess.Bead)+", not this bead)")
	case sess != nil:
		parts = append(parts, "session "+sess.ID)
	}
	return strings.Join(parts, " and ")
}

func closeWith(reason string) func(*wtx, *beadRow) (string, map[string]any, error) {
	return func(w *wtx, b *beadRow) (string, map[string]any, error) {
		data := withGroup(b, map[string]any{"reason": reason})
		if err := w.endSession(b, "done"); err != nil {
			return "", nil, err
		}
		if b.Status != "closed" {
			t := now()
			b.ClosedAt = &t
		}
		b.Status, b.CloseReason = "closed", &reason
		return "closed", data, nil
	}
}

// withGroup names in data the group that owns b, before ending its
// session frees it, so the event says whose work ended.
func withGroup(b *beadRow, data map[string]any) map[string]any {
	if b.Group != nil {
		data["group"] = *b.Group
	}
	return data
}

// Reopen opens a closed bead again, in the stage it was closed in. A stale
// exp refuses it (see Expect).
func (s *Store) Reopen(ctx context.Context, id string, exp Expect, actor string) (*Bead, error) {
	return s.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
		if err := w.check(ctx, exp, b); err != nil {
			return "", nil, err
		}
		b.Status, b.CloseReason, b.ClosedAt = "open", nil, nil
		return "reopened", nil, nil
	})
}

// SetHold pauses (or resumes) routing for a bead. Holding ends the session
// working it (state 'held', which is no failure) and frees its group's slot:
// a held bead waits on a human, and a session kept for it would only hold a
// slot other beads could use. Unholding routes it again from its stage with
// fresh attempts (its failure count starts over). A stale exp refuses it
// (see Expect), and a bead that arrived held from another yard is released
// only by who may (releasePeerBead).
func (s *Store) SetHold(ctx context.Context, id string, hold bool, exp Expect, actor string) (*Bead, error) {
	b, err := s.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
		if err := w.check(ctx, exp, b); err != nil {
			return "", nil, err
		}
		if hold {
			b.Hold = true
			var data map[string]any
			if b.Session != nil {
				data = map[string]any{"session": *b.Session}
			}
			return "held", data, w.endSession(b, "held")
		}
		// A bead held as it arrived from another yard is not everyone's to
		// release.
		if b.Hold {
			if err := w.releasePeerBead(ctx, exp, b, actor); err != nil {
				return "", nil, err
			}
		}
		b.Hold = false
		b.Failures = 0
		return "unheld", nil, nil
	})
	if !hold {
		return b, err
	}
	return b, s.refused(ctx, id, "yardr bead hold "+id, exp, actor, err)
}

// Release takes a bead away from its group so it is routed again, ending
// the current session as 'released'. A stale exp refuses it (see Expect):
// an agent orphaned by a restart cannot release the bead from under the
// session that owns it now.
func (s *Store) Release(ctx context.Context, id, reason string, exp Expect, actor string) (*Bead, error) {
	b, err := s.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
		if err := w.check(ctx, exp, b); err != nil {
			return "", nil, err
		}
		if err := w.endSession(b, "released"); err != nil {
			return "", nil, err
		}
		return "released", map[string]any{"reason": reason}, nil
	})
	return b, s.refused(ctx, id, "yardr bead release "+id, exp, actor, err)
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// dedupe keeps the first occurrence of each label, sorted like a set.
func dedupe(in []string) []string {
	out := []string{}
	for _, l := range in {
		l = strings.TrimSpace(l)
		if l != "" && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}
