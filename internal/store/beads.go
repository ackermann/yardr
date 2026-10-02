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
}

// beadRow is what the beads bucket holds: the bead plus bookkeeping that is
// part of its served representation.
type beadRow struct {
	Bead
	LastActor string `json:"last_actor,omitempty"`
	Notes     int    `json:"notes,omitempty"`
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
}

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

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
	if err := put(w.Tx, bBeads, row.UUID, row); err != nil {
		return err
	}
	return w.version("bead", row.UUID, row.Revision, actor, row, false)
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
	typ, stage := or(nb.Type, "task"), or(nb.Stage, "new")
	var rig *string
	if nb.Rig != "" {
		rig = &nb.Rig
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
		return w.event(row.UUID, "created", actor, map[string]any{"title": nb.Title})
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
}

func (s *Store) ListBeads(ctx context.Context, f BeadFilter) ([]Bead, error) {
	return s.beadsWhere(func(tx *bolt.Tx, b *Bead) bool {
		return (f.All || b.Status == "open") &&
			(f.Stage == "" || b.Stage == f.Stage) &&
			// Journals and memories are listed only when asked for by type.
			((f.Type == "" && !NotWork[b.Type]) || b.Type == f.Type) &&
			(f.Rig == "" || (b.Rig != nil && *b.Rig == f.Rig)) &&
			(f.Label == "" || slices.Contains(b.Labels, f.Label)) &&
			(f.Group == "" || (b.Group != nil && *b.Group == f.Group))
	})
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
// order of the beads bucket, whose UUIDv7 keys start with it.
func (s *Store) beadsWhere(match func(*bolt.Tx, *Bead) bool) ([]Bead, error) {
	out := []Bead{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
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

func (s *Store) UpdateBead(ctx context.Context, id string, p BeadPatch, actor string) (*Bead, error) {
	return s.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
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
// ownership. Shared by Advance, Close and Release.
func (w *wtx) endSession(b *beadRow, state string) error {
	if b.Session != nil {
		var sess Session
		ok, err := get(w.Tx, bSessions, *b.Session, &sess)
		if err != nil {
			return err
		}
		if ok && (sess.State == "starting" || sess.State == "running") {
			t := now()
			sess.State, sess.EndedAt = state, &t
			if err := put(w.Tx, bSessions, sess.ID, sess); err != nil {
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
// only they may move it.
func (s *Store) Advance(ctx context.Context, id, stage, actor string) (*Bead, error) {
	return s.AdvanceNote(ctx, id, stage, "", actor)
}

// AdvanceNote is Advance with a note attached in the same transaction, so
// a refused move leaves no note behind and whoever picks the bead up in the
// next stage already sees it (a reviewer's requested changes). An empty
// note adds none.
func (s *Store) AdvanceNote(ctx context.Context, id, stage, note, actor string) (*Bead, error) {
	return s.advance(ctx, id, note, actor, func(f *Flow, b *beadRow, data map[string]any) (string, error) {
		return stage, checkAdvance(f, b.ID, b.Stage, stage)
	})
}

// Done reports how the work in the bead's stage ended and moves it where
// its flow sends that outcome ("" is done), with an optional note, in one
// write like AdvanceNote. The outcome is recorded on the advanced event.
// A bead no flow governs has no outcomes: it is moved with Advance.
func (s *Store) Done(ctx context.Context, id, outcome, note, actor string) (*Bead, error) {
	outcome = or(strings.TrimSpace(outcome), DefaultOutcome)
	return s.advance(ctx, id, note, actor, func(f *Flow, b *beadRow, data map[string]any) (string, error) {
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
func (s *Store) advance(ctx context.Context, id, note, actor string, pick func(f *Flow, b *beadRow, data map[string]any) (string, error)) (*Bead, error) {
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

func (s *Store) CloseBead(ctx context.Context, id, reason, actor string) (*Bead, error) {
	return s.changeBead(id, actor, closeWith(reason))
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

func (s *Store) Reopen(ctx context.Context, id, actor string) (*Bead, error) {
	return s.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
		b.Status, b.CloseReason, b.ClosedAt = "open", nil, nil
		return "reopened", nil, nil
	})
}

// SetHold pauses (or resumes) routing for a bead. Holding does not stop a
// session already working it.
func (s *Store) SetHold(ctx context.Context, id string, hold bool, actor string) (*Bead, error) {
	return s.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
		b.Hold = hold
		if hold {
			return "held", nil, nil
		}
		return "unheld", nil, nil
	})
}

// Release takes a bead away from its group so it is routed again, ending
// the current session as 'released'.
func (s *Store) Release(ctx context.Context, id, reason, actor string) (*Bead, error) {
	return s.changeBead(id, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
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
