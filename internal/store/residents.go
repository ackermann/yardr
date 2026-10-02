package store

import (
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bResidents = []byte("residents")

func init() { allBuckets = append(allBuckets, bResidents) }

// Bounds that keep a fresh session's starting context small.
const (
	MaxHandoffBytes = 16 << 10
	MaxJournalBytes = 4 << 10
	MaxMailBytes    = 4 << 10
	PrimeEntries    = 20 // journal entries since the latest handoff
	PrimeInbox      = 20
	PrimeFallback   = 15 // recent events when the last session left no handoff
	MaxStartFails   = 5  // consecutive failed starts before a resident is parked
)

// Resident is an always-on session not tied to a bead, such as a mayor.
// The daemon keeps it running, delivers its inbox when it is idle, and
// rotates it through handoffs written to its journal bead.
type Resident struct {
	Name         string         `json:"name"`
	Runner       string         `json:"runner"`
	Config       map[string]any `json:"config"`
	Dir          string         `json:"dir"`
	On           []string       `json:"on"`            // event kinds delivered to the inbox (mail always is)
	MaxAge       string         `json:"max_age"`       // e.g. "6h"; "" never rotates
	HandoffGrace string         `json:"handoff_grace"` // how long a requested handoff may take
	Enabled      bool           `json:"enabled"`
	Journal      string         `json:"journal"` // journal bead id

	// Who it is in each source (by source name), and which hook events it
	// hears about. Kept when the definition is replaced.
	Identities    map[string]Identity `json:"identities,omitempty"`
	Subscriptions []Subscription      `json:"subscriptions,omitempty"`

	// Runtime, maintained by the daemon.
	State              string         `json:"state"` // stopped running failed
	Handle             map[string]any `json:"handle,omitempty"`
	StartedAt          *time.Time     `json:"started_at,omitempty"`
	PrevStartedAt      *time.Time     `json:"prev_started_at,omitempty"`
	Failures           int            `json:"failures,omitempty"`
	RetryAfter         *time.Time     `json:"retry_after,omitempty"`
	RestartRequested   string         `json:"restart_requested,omitempty"`
	RestartRequestedAt *time.Time     `json:"restart_requested_at,omitempty"`
	RotationAskedAt    *time.Time     `json:"rotation_asked_at,omitempty"`
	LastHandoffAt      *time.Time     `json:"last_handoff_at,omitempty"`
	Cursor             uint64         `json:"cursor"`   // inbox acknowledged through this event seq
	Notified           uint64         `json:"notified"` // nudged about events through this seq
}

func (r Resident) Live() bool { return r.State == "running" }

func journalID(name string) string {
	return strings.NewReplacer("/", "-", " ", "-").Replace(name) + "-journal"
}

// PutResident creates a resident (with its journal bead) or replaces its
// definition, keeping its runtime state, journal and inbox position.
func (s *Store) PutResident(ctx context.Context, def Resident, actor string) (*Resident, error) {
	var out *Resident
	err := s.write(func(w *wtx) error {
		var r Resident
		ok, err := get(w.Tx, bResidents, def.Name, &r)
		if err != nil {
			return err
		}
		if !ok {
			// A new resident starts reading the inbox from now, not history.
			head := uint64(0)
			if k, _ := w.Bucket(bEvents).Cursor().Last(); k != nil {
				head = binary.BigEndian.Uint64(k)
			}
			r = Resident{Name: def.Name, State: "stopped", Enabled: true, Cursor: head, Notified: head,
				Journal: journalID(def.Name)}
			if w.Bucket(bBeads).Get([]byte(r.Journal)) == nil {
				t := now()
				jr := &beadRow{Bead: Bead{ID: r.Journal, Title: "Journal of " + def.Name, Type: TypeJournal,
					Stage: "journal", Status: "open", Labels: []string{"resident:" + def.Name},
					CreatedAt: t, CreatedBy: &actor}}
				if err := w.mintBead(jr, actor); err != nil {
					return err
				}
				if err := w.event(r.Journal, "created", actor, map[string]any{"title": jr.Title}); err != nil {
					return err
				}
			}
		}
		r.Runner, r.Config, r.Dir, r.On, r.MaxAge, r.HandoffGrace =
			def.Runner, def.Config, def.Dir, def.On, def.MaxAge, def.HandoffGrace
		if r.Config == nil {
			r.Config = map[string]any{}
		}
		for _, d := range []string{r.MaxAge, r.HandoffGrace} {
			if d != "" {
				if _, err := time.ParseDuration(d); err != nil {
					return fmt.Errorf("duration %q: %w", d, err)
				}
			}
		}
		if err := put(w.Tx, bResidents, r.Name, r); err != nil {
			return err
		}
		out = &r
		return w.event(r.Journal, "resident_defined", actor, map[string]any{"resident": r.Name, "runner": r.Runner})
	})
	return out, err
}

func (s *Store) Residents(ctx context.Context) ([]Resident, error) {
	out := []Resident{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bResidents, "", func(_ []byte, r *Resident) error {
			out = append(out, *r)
			return nil
		})
	})
	return out, err
}

func (s *Store) GetResident(ctx context.Context, name string) (*Resident, error) {
	r := new(Resident)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bResidents, name, r)
		if err == nil && !ok {
			err = fmt.Errorf("resident %s: %w", name, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// DeleteResident removes the resident; its journal bead is kept as history.
func (s *Store) DeleteResident(ctx context.Context, name, actor string) error {
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		if err := w.Bucket(bResidents).Delete([]byte(name)); err != nil {
			return err
		}
		return w.event(r.Journal, "resident_deleted", actor, map[string]any{"resident": name})
	}, true)
}

// changeResident applies fn to the stored resident and saves it, unless
// deleted is set (fn removed it).
func (s *Store) changeResident(name string, fn func(*wtx, *Resident) error, deleted ...bool) error {
	return s.write(func(w *wtx) error {
		var r Resident
		ok, err := get(w.Tx, bResidents, name, &r)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("resident %s: %w", name, ErrNotFound)
		}
		if err := fn(w, &r); err != nil {
			return err
		}
		if len(deleted) > 0 && deleted[0] {
			return nil
		}
		return put(w.Tx, bResidents, name, r)
	})
}

// SetResidentEnabled starts (true) or stops (false) keeping it alive. Enabling
// also clears a failure streak.
func (s *Store) SetResidentEnabled(ctx context.Context, name string, enabled bool, actor string) error {
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		r.Enabled = enabled
		if enabled {
			r.Failures, r.RetryAfter = 0, nil
			if r.State == "failed" {
				r.State = "stopped"
			}
		}
		kind := map[bool]string{true: "resident_enabled", false: "resident_disabled"}[enabled]
		return w.event(r.Journal, kind, actor, map[string]any{"resident": name})
	})
}

// ResidentStarted records a fresh session.
func (s *Store) ResidentStarted(ctx context.Context, name string, handle map[string]any) error {
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		t := now()
		r.PrevStartedAt, r.StartedAt = r.StartedAt, &t
		r.State, r.Handle, r.Failures, r.RetryAfter = "running", handle, 0, nil
		r.RestartRequested, r.RestartRequestedAt, r.RotationAskedAt = "", nil, nil
		// The new session sees its unread inbox in its prime; only events
		// after this point warrant a nudge.
		if k, _ := w.Bucket(bEvents).Cursor().Last(); k != nil {
			r.Notified = max(r.Notified, binary.BigEndian.Uint64(k))
		}
		return w.event(r.Journal, "resident_started", "yard", map[string]any{"resident": name, "handle": handle})
	})
}

// SetResidentHandle replaces the running session's handle (e.g. once its
// pending first prompt was delivered).
func (s *Store) SetResidentHandle(ctx context.Context, name string, handle map[string]any) error {
	return s.changeResident(name, func(_ *wtx, r *Resident) error {
		r.Handle = handle
		return nil
	})
}

// ResidentStopped records that the session ended (stopped by yard, or died).
func (s *Store) ResidentStopped(ctx context.Context, name, reason string) error {
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		if r.State != "running" {
			return nil
		}
		r.State, r.Handle = "stopped", nil
		return w.event(r.Journal, "resident_stopped", "yard", map[string]any{"resident": name, "reason": reason})
	})
}

// ResidentStartFailed counts a failed start; after MaxStartFails in a row
// the resident is parked (state failed) until someone starts it again.
func (s *Store) ResidentStartFailed(ctx context.Context, name, reason string, backoff time.Duration) error {
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		r.Failures++
		retry := now().Add(backoff)
		r.RetryAfter, r.State = &retry, "stopped"
		kind := "resident_start_failed"
		if r.Failures >= MaxStartFails {
			r.State, kind = "failed", "resident_gave_up"
		}
		return w.event(r.Journal, kind, "yard", map[string]any{"resident": name, "reason": reason, "failures": r.Failures})
	})
}

// RequestRestart asks the daemon for a fresh session once the current one
// is idle (or the handoff grace has passed).
func (s *Store) RequestRestart(ctx context.Context, name, reason, actor string) error {
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		t := now()
		r.RestartRequested, r.RestartRequestedAt = reason, &t
		return w.event(r.Journal, "resident_restart_requested", actor, map[string]any{"resident": name, "reason": reason})
	})
}

// MarkRotationAsked records that the resident was asked to hand off.
func (s *Store) MarkRotationAsked(ctx context.Context, name string) error {
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		t := now()
		r.RotationAskedAt = &t
		return w.event(r.Journal, "resident_rotation_asked", "yard", map[string]any{"resident": name})
	})
}

// MarkNotified records that the resident was nudged about events through seq.
func (s *Store) MarkNotified(ctx context.Context, name string, seq uint64) error {
	return s.changeResident(name, func(_ *wtx, r *Resident) error {
		r.Notified = max(r.Notified, seq)
		return nil
	})
}

// Journal writes an entry on the resident's journal bead. A handoff also
// requests a fresh session.
func (s *Store) Journal(ctx context.Context, name, kind, text, actor string) error {
	limit := map[string]int{NoteJournal: MaxJournalBytes, NoteHandoff: MaxHandoffBytes}[kind]
	if limit == 0 {
		return fmt.Errorf("journal kind %q: use journal or handoff", kind)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty %s entry", kind)
	}
	if len(text) > limit {
		return fmt.Errorf("%s entry is %d bytes, over the %d-byte limit: summarize it", kind, len(text), limit)
	}
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		if err := w.addNote(r.Journal, kind, text, actor); err != nil {
			return err
		}
		if kind == NoteHandoff {
			t := now()
			r.LastHandoffAt = &t
			r.RestartRequested, r.RestartRequestedAt = "handoff", &t
		}
		return nil
	})
}

// Mail sends a message to a resident's inbox.
func (s *Store) Mail(ctx context.Context, to, text, actor string) error {
	if strings.TrimSpace(text) == "" || len(text) > MaxMailBytes {
		return fmt.Errorf("mail must be 1..%d bytes", MaxMailBytes)
	}
	return s.write(func(w *wtx) error {
		if w.Bucket(bResidents).Get([]byte(to)) == nil {
			return fmt.Errorf("resident %s: %w", to, ErrNotFound)
		}
		return w.event("", "mail", actor, map[string]any{"to": to, "text": text})
	})
}

// forResident reports whether e belongs in r's inbox: mail to it, a hook
// delivery matched to it at ingest, or a kind it subscribes to, never its
// own actions.
func forResident(r *Resident, e Event) bool {
	if e.Actor == r.Name {
		return false
	}
	switch e.Kind {
	case "mail":
		return e.Data["to"] == r.Name
	case "hook":
		return HookReason(e, r.Name) != ""
	}
	return slices.Contains(r.On, e.Kind)
}

// InboxPage is the start of a resident's unacknowledged events.
type InboxPage struct {
	Events []Event `json:"events"` // oldest first, at most the limit
	Total  int     `json:"total"`  // all unacknowledged events
	Last   uint64  `json:"last"`   // seq of the newest unacknowledged event
}

// Inbox returns up to limit unacknowledged events for the resident.
func (s *Store) Inbox(ctx context.Context, name string, limit int) (*InboxPage, error) {
	r, err := s.GetResident(ctx, name)
	if err != nil {
		return nil, err
	}
	all, err := s.EventsSince(ctx, r.Cursor, 0)
	if err != nil {
		return nil, err
	}
	page := &InboxPage{Events: []Event{}}
	for _, e := range all {
		if forResident(r, e) {
			page.Total++
			page.Last = e.Seq
			if limit <= 0 || len(page.Events) < limit {
				page.Events = append(page.Events, e)
			}
		}
	}
	return page, nil
}

// AckInbox marks the inbox read through seq.
func (s *Store) AckInbox(ctx context.Context, name string, through uint64) error {
	return s.changeResident(name, func(_ *wtx, r *Resident) error {
		r.Cursor = max(r.Cursor, through)
		r.Notified = max(r.Notified, r.Cursor)
		return nil
	})
}

// Prime is what a fresh resident session reads first, bounded in size.
type Prime struct {
	Resident    Resident       `json:"resident"`
	Handoff     *Note          `json:"handoff,omitempty"`
	Since       []Note         `json:"since"`   // journal entries after the handoff, newest last
	Omitted     int            `json:"omitted"` // entries after the handoff not shown
	Inbox       []Event        `json:"inbox"`
	InboxTotal  int            `json:"inbox_total"`
	Memories    []MemoryInfo   `json:"memories"` // titles only
	NoHandoff   bool           `json:"no_handoff"`
	EventCounts map[string]int `json:"event_counts,omitempty"` // since the last handoff, when there was none at the end
	Recent      []Event        `json:"recent,omitempty"`
}

// ResidentPrime gathers what the next session needs, and only that.
func (s *Store) ResidentPrime(ctx context.Context, name string) (*Prime, error) {
	r, err := s.GetResident(ctx, name)
	if err != nil {
		return nil, err
	}
	p := &Prime{Resident: *r, Since: []Note{}}
	notes, err := s.Notes(ctx, r.Journal)
	if err != nil {
		return nil, err
	}
	var after []Note
	for i := len(notes) - 1; i >= 0; i-- {
		if notes[i].Kind == NoteHandoff {
			h := notes[i]
			p.Handoff, after = &h, notes[i+1:]
			break
		}
	}
	if p.Handoff == nil {
		after = notes
	}
	if len(after) > PrimeEntries {
		p.Omitted, after = len(after)-PrimeEntries, after[len(after)-PrimeEntries:]
	}
	p.Since = append(p.Since, after...)

	inbox, err := s.Inbox(ctx, name, PrimeInbox)
	if err != nil {
		return nil, err
	}
	p.Inbox, p.InboxTotal = inbox.Events, inbox.Total
	if p.Memories, err = s.Recall(ctx, "resident:"+name, ""); err != nil {
		return nil, err
	}

	// The previous session ended without writing a handoff (it crashed, or
	// was rotated by force): summarize what happened since the last handoff,
	// or since that session started, so the new one can reconstruct.
	if prev := r.PrevStartedAt; prev != nil && (r.LastHandoffAt == nil || r.LastHandoffAt.Before(*prev)) {
		from := *prev
		if r.LastHandoffAt != nil {
			from = *r.LastHandoffAt
		}
		evs, err := s.EventsSince(ctx, 0, 0)
		if err != nil {
			return nil, err
		}
		p.NoHandoff, p.EventCounts = true, map[string]int{}
		for _, e := range evs {
			if e.At.After(from) {
				p.EventCounts[e.Kind]++
				p.Recent = append(p.Recent, e)
			}
		}
		if len(p.Recent) > PrimeFallback {
			p.Recent = p.Recent[len(p.Recent)-PrimeFallback:]
		}
	}
	return p, nil
}

// MemoryInfo is a memory as listed: enough to decide whether to read it.
type MemoryInfo struct {
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	Scope   string     `json:"scope"`
	Expires *time.Time `json:"expires,omitempty"`
}

type NewMemory struct {
	Title   string
	Body    string
	Scope   string // "all" or "resident:<name>" (or any label)
	Expires *time.Time
}

// Remember stores a piece of knowledge as a memory bead.
func (s *Store) Remember(ctx context.Context, m NewMemory, actor string) (*Bead, error) {
	if m.Scope == "" {
		m.Scope = "all"
	}
	meta := map[string]any{"scope": m.Scope}
	if m.Expires != nil {
		meta["expires_at"] = m.Expires.UTC().Format(time.RFC3339)
	}
	return s.CreateBead(ctx, NewBead{Title: m.Title, Body: m.Body, Type: TypeMemory, Stage: "memory",
		Prefix: "mem", Labels: []string{"scope:" + m.Scope}, Metadata: meta}, actor)
}

// Recall lists live memories visible in scope (its own and "all") whose
// title or body contains query (case-insensitive), titles only.
func (s *Store) Recall(ctx context.Context, scope, query string) ([]MemoryInfo, error) {
	q := strings.ToLower(query)
	beads, err := s.beadsWhere(func(_ *bolt.Tx, b *Bead) bool {
		if b.Type != TypeMemory || b.Status != "open" {
			return false
		}
		sc, _ := b.Metadata["scope"].(string)
		if scope != "" && sc != "all" && sc != scope {
			return false
		}
		return q == "" || strings.Contains(strings.ToLower(b.Title+"\n"+b.Body), q)
	})
	if err != nil {
		return nil, err
	}
	out := []MemoryInfo{}
	for _, b := range beads {
		mi := MemoryInfo{ID: b.ID, Title: b.Title}
		mi.Scope, _ = b.Metadata["scope"].(string)
		if e, ok := b.Metadata["expires_at"].(string); ok {
			if t, err := time.Parse(time.RFC3339, e); err == nil {
				mi.Expires = &t
			}
		}
		out = append(out, mi)
	}
	return out, nil
}

// ExpireMemories closes memories past their expiry as "expired" (retired,
// kept as history) and reports how many.
func (s *Store) ExpireMemories(ctx context.Context) (int, error) {
	mems, err := s.Recall(ctx, "", "")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range mems {
		if m.Expires != nil && time.Now().After(*m.Expires) {
			if _, err := s.CloseBead(ctx, m.ID, "expired", "yard"); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}
