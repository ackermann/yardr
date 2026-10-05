package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
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
	Rig         *string        `json:"rig,omitempty"`
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
	// Epic is the handle of the epic the bead is a child of (see Family),
	// set on the beads a listing returns (ListBeads, Ready, Dispatchable)
	// so a table of them can show it without a lookup per row. It is
	// derived from the parent edges, never stored; "" for no epic, and on a
	// bead returned by anything other than a listing.
	Epic string `json:"epic,omitempty"`
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
	Rig      string
	Labels   []string
	Prefix   string // handle prefix; defaults to the rig name, else "yd"
	ID       string // explicit handle (imports, journals); must be unused
	Metadata map[string]any
	// Links are made with the bead, in the same write: a link that is
	// refused (the issue has a tracker) creates no bead.
	Links []NewLink
	// DiscoveredFrom names the bead this one was found while working on
	// (a handle or UUID, open or closed, in any rig). The discovered-from
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

// validName is the shape of rig names, bead types and dep kinds. They end up
// in paths (rig clones, flow files), BDP type URLs and route matches, so no
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

func (w *wtx) mintBead(row *beadRow, actor string) error {
	row.Revision = newRevision()
	row.UpdatedAt = now()
	row.LastActor = actor
	row.Epic = "" // derived on listing, never stored
	return put(w.Tx, bBeads, row.UUID, row)
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
		prefix = nb.Rig
	}
	if prefix == "" {
		prefix = "yd"
	}
	typ := or(nb.Type, "task")
	if err := checkName("bead type", typ); err != nil {
		return nil, err
	}
	var rig *string
	if nb.Rig != "" {
		rig = &nb.Rig
	}
	for _, nl := range nb.Links {
		if err := checkLink(nl); err != nil {
			return nil, err
		}
	}
	pin := PinnedFlow(&Bead{Metadata: nb.Metadata})
	repo, err := s.repoFor(ctx, rig, typ, pin)
	if err != nil {
		return nil, err
	}
	var created *Bead
	err = s.write(func(w *wtx) error {
		if nb.Rig != "" && w.Bucket(bRigs).Get([]byte(nb.Rig)) == nil {
			return fmt.Errorf("rig %s: %w", nb.Rig, ErrNotFound)
		}
		f, _, err := resolveFlow(w.Tx, rig, typ, pin, repo)
		if err != nil {
			return changedMeanwhile(err)
		}
		// Without a stage the bead starts where its flow starts (backlog
		// in the starter flow, where the mayor shapes it), so create works
		// for any flow, an epic's too; "new" is only for beads no flow
		// governs.
		stage := or(nb.Stage, firstStage(f))
		if err := checkCreateStage(f, stage); err != nil {
			return err
		}
		row := &beadRow{Bead: Bead{
			Title: nb.Title, Body: nb.Body, Type: typ, Stage: stage,
			Status: "open", Priority: nb.Priority, Labels: dedupe(nb.Labels), CreatedAt: now(), CreatedBy: &actor,
			Metadata: nb.Metadata,
		}}
		row.Rig = rig
		if err := w.insertBead(row, prefix, nb.ID, actor); err != nil {
			return err
		}
		created = &row.Bead
		if err := w.event(row.UUID, "created", actor, map[string]any{"title": nb.Title}); err != nil {
			return err
		}
		seq := w.Bucket(bEvents).Sequence()
		if nb.DiscoveredFrom != "" {
			origin, err := w.loadBead(nb.DiscoveredFrom)
			if err != nil {
				return fmt.Errorf("discovered from: %w", err)
			}
			if err := w.addDep(origin, row, DepDiscoveredFrom, actor); err != nil {
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
				if err := w.addDep(blocker, row, DepBlocks, actor); err != nil {
					return err
				}
			}
			if _, err := get(w.Tx, bBeads, row.UUID, row); err != nil {
				return err
			}
		}
		for _, nl := range nb.Links {
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
		if err == nil {
			b = &row.Bead
		}
		return err
	})
	return b, err
}

type BeadFilter struct {
	All   bool // include closed
	Stage string
	Type  string
	Rig   string
	Label string
	Group string
	// Epic keeps the epic (a handle or UUID) and its children, the epic
	// first.
	Epic string
	// DiscoveredFrom keeps the beads found from this one (a handle or
	// UUID): those with a discovered-from edge from it.
	DiscoveredFrom string
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
	epic := ""
	if f.Epic != "" {
		err := s.read(func(tx *bolt.Tx) error {
			row, err := loadBead(tx, f.Epic)
			if err != nil {
				return err
			}
			if row.Type != TypeEpic {
				return fmt.Errorf("%s is a %s, not an epic", row.ID, row.Type)
			}
			epic = row.ID
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	bs, err := s.beadsWhere(func(tx *bolt.Tx, b *Bead) bool {
		return (f.All || b.Status == "open") &&
			(epic == "" || b.ID == epic || b.Epic == epic) &&
			(found == nil || found[b.UUID]) &&
			(f.Stage == "" || b.Stage == f.Stage) &&
			// Journals and memories are listed only when asked for by type.
			((f.Type == "" && !NotWork[b.Type]) || b.Type == f.Type) &&
			(f.Rig == "" || (b.Rig != nil && *b.Rig == f.Rig)) &&
			(f.Label == "" || slices.Contains(b.Labels, f.Label)) &&
			(f.Group == "" || (b.Group != nil && *b.Group == f.Group))
	})
	if epic != "" {
		// The epic heads its children, whatever its priority.
		i := slices.IndexFunc(bs, func(b Bead) bool { return b.ID == epic })
		if i > 0 {
			e := bs[i]
			copy(bs[1:i+1], bs[:i])
			bs[0] = e
		}
	}
	return bs, err
}

// Ready lists open, unheld beads whose blockers are all closed, assigned or not.
func (s *Store) Ready(ctx context.Context) ([]Bead, error) {
	return s.beadsWhere(ready)
}

// Dispatchable lists ready beads that no group owns yet.
func (s *Store) Dispatchable(ctx context.Context) ([]Bead, error) {
	return s.beadsWhere(func(tx *bolt.Tx, b *Bead) bool { return b.Group == nil && ready(tx, b) })
}

// NotWork lists bead types that carry knowledge, not work: they are never
// ready and never routed.
var NotWork = map[string]bool{TypeJournal: true, TypeMemory: true}

const (
	TypeJournal = "journal"
	TypeMemory  = "memory"
)

func ready(tx *bolt.Tx, b *Bead) bool {
	return !NotWork[b.Type] && b.Status == "open" && !b.Hold && len(openBlockers(tx, b.UUID)) == 0
}

// openBlockers lists the UUIDs of beads that block the bead with UUID u
// (kind blocks) and are not closed.
func openBlockers(tx *bolt.Tx, u string) []string {
	var out []string
	for _, k := range keys(tx, bDepTo, u+"\x00") {
		var d depRow
		if ok, _ := get(tx, bDeps, k[len(u)+1:], &d); !ok || d.Kind != "blocks" {
			continue
		}
		var blocker beadRow
		if ok, _ := get(tx, bBeads, d.From, &blocker); ok && blocker.Status != "closed" {
			out = append(out, d.From)
		}
	}
	return out
}

// beadsWhere returns matching beads by priority, then creation time: the
// order of the beads bucket, whose UUIDv7 keys start with it. match sees
// each bead with its Epic set.
func (s *Store) beadsWhere(match func(*bolt.Tx, *Bead) bool) ([]Bead, error) {
	out := []Bead{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
			r.Epic = epicOf(tx, r.UUID)
			if match(tx, &r.Bead) {
				out = append(out, r.Bead)
			}
			return nil
		})
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out, err
}

type BeadPatch struct {
	Title       *string
	Body        *string
	Type        *string
	Priority    *int
	Rig         *string
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
		rig, typ := pre.Rig, pre.Type
		if p.Rig != nil {
			rig = p.Rig
		}
		if p.Type != nil {
			typ = *p.Type
		}
		if repo, err = s.repoFor(ctx, rig, typ, pin); err != nil {
			return nil, err
		}
	}
	return s.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
		if err := w.check(ctx, exp, b); err != nil {
			return "", nil, err
		}
		changed := map[string]any{}
		if p.Title != nil {
			b.Title, changed["title"] = *p.Title, *p.Title
		}
		if p.Body != nil {
			b.Body, changed["body"] = *p.Body, true
		}
		if p.Type != nil {
			b.Type, changed["type"] = *p.Type, *p.Type
		}
		if p.Priority != nil {
			b.Priority, changed["priority"] = *p.Priority, *p.Priority
		}
		if p.Rig != nil {
			if w.Bucket(bRigs).Get([]byte(*p.Rig)) == nil {
				return "", nil, fmt.Errorf("rig %s: %w", *p.Rig, ErrNotFound)
			}
			b.Rig, changed["rig"] = p.Rig, *p.Rig
		}
		if len(p.AddLabels) > 0 || len(p.RemoveLabel) > 0 {
			labels := dedupe(append(slices.Clone(b.Labels), p.AddLabels...))
			labels = slices.DeleteFunc(labels, func(l string) bool { return slices.Contains(p.RemoveLabel, l) })
			b.Labels = labels
			changed["labels_added"], changed["labels_removed"] = p.AddLabels, p.RemoveLabel
		}
		if len(p.Metadata) > 0 {
			if b.Metadata == nil {
				b.Metadata = map[string]any{}
			}
			for k, v := range p.Metadata {
				b.Metadata[k] = v
			}
			changed["metadata"] = p.Metadata
		}
		if pin != "" {
			if _, _, err := resolveFlow(w.Tx, b.Rig, b.Type, pin, repo); err != nil {
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
	return s.advance(ctx, id, note, exp, actor, func(f *Flow, b *beadRow, data map[string]any) (string, error) {
		return stage, checkAdvance(f, b.ID, b.Stage, stage)
	})
}

// Done reports how the work in the bead's stage ended and moves it where
// its flow sends that outcome ("" is done), with an optional note, in one
// write like AdvanceNote. The outcome is recorded on the advanced event.
// A bead no flow governs has no outcomes: it is moved with Advance.
func (s *Store) Done(ctx context.Context, id, outcome, note string, exp Expect, actor string) (*Bead, error) {
	outcome = or(strings.TrimSpace(outcome), DefaultOutcome)
	return s.advance(ctx, id, note, exp, actor, func(f *Flow, b *beadRow, data map[string]any) (string, error) {
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
}

// advance is the move AdvanceNote and Done share: pick names the stage
// under the bead's flow (nil without one), may add to the event's data,
// and refuses by returning an error.
func (s *Store) advance(ctx context.Context, id, note string, exp Expect, actor string, pick func(f *Flow, b *beadRow, data map[string]any) (string, error)) (*Bead, error) {
	pre, err := s.GetBead(ctx, id)
	if err != nil {
		return nil, err
	}
	repo, err := s.repoFor(ctx, pre.Rig, pre.Type, PinnedFlow(pre))
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
		var stage string
		var terminal bool
		b, err := w.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
			f, _, err := resolveFlow(w.Tx, b.Rig, b.Type, PinnedFlow(&b.Bead), repo)
			if err != nil {
				return "", nil, changedMeanwhile(err)
			}
			data := withGroup(b, map[string]any{"from": b.Stage})
			if stage, err = pick(f, b, data); err != nil {
				return "", nil, err
			}
			data["to"] = stage
			g, err := stageGroup(w.Tx, b)
			if err != nil {
				return "", nil, err
			}
			if err := checkMembers(g, b.ID, b.Stage, actor); err != nil {
				return "", nil, err
			}
			terminal = f != nil && f.IsTerminal(stage)
			if err := w.endSession(b, "done"); err != nil {
				return "", nil, err
			}
			b.Stage = stage
			return "advanced", data, nil
		})
		result = b
		if err != nil || !terminal {
			return err
		}
		result, err = w.changeBead(b.UUID, actor, closeWith(stage))
		return err
	})
	return result, err
}

// CloseBead closes the bead, ending its session. A stale exp refuses it
// (see Expect).
func (s *Store) CloseBead(ctx context.Context, id, reason string, exp Expect, actor string) (*Bead, error) {
	close := closeWith(reason)
	return s.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
		if err := w.check(ctx, exp, b); err != nil {
			return "", nil, err
		}
		return close(w, b)
	})
}

// Expect is what a caller believes about a bead it changes: the session it
// acts for, the stage the bead is in, and its revision. A write made on a
// stale view is refused with ErrConflict and changes nothing, so an agent
// kept or orphaned after its bead moved on cannot end the session that owns
// the bead now, a review form opened before someone else decided cannot
// apply its decision on top, and two writers that read the same revision
// cannot both apply what they read it for (compare-and-swap). An empty
// field expects nothing: a person acting outside any session (the mayor,
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
// (see Expect).
func (s *Store) SetHold(ctx context.Context, id string, hold bool, exp Expect, actor string) (*Bead, error) {
	return s.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
		if err := w.check(ctx, exp, b); err != nil {
			return "", nil, err
		}
		b.Hold = hold
		if hold {
			var data map[string]any
			if b.Session != nil {
				data = map[string]any{"session": *b.Session}
			}
			return "held", data, w.endSession(b, "held")
		}
		b.Failures = 0
		return "unheld", nil, nil
	})
}

// Release takes a bead away from its group so it is routed again, ending
// the current session as 'released'. A stale exp refuses it (see Expect):
// an agent orphaned by a restart cannot release the bead from under the
// session that owns it now.
func (s *Store) Release(ctx context.Context, id, reason string, exp Expect, actor string) (*Bead, error) {
	return s.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
		if err := w.check(ctx, exp, b); err != nil {
			return "", nil, err
		}
		if err := w.endSession(b, "released"); err != nil {
			return "", nil, err
		}
		return "released", map[string]any{"reason": reason}, nil
	})
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
