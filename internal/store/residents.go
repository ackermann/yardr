package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
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

// MinSessionLife is how long a resident's session has to live for its start
// to count as one that worked. A session gone sooner (a harness that exits
// on a bad flag or an expired login) is a failed start, or the daemon would
// start the next one on every pass and never give up.
const MinSessionLife = 60 * time.Second

// Resident is an always-on session not tied to a bead, such as a mayor.
// The daemon keeps it running, delivers its inbox when it is idle, and
// rotates it through handoffs written to its journal bead.
type Resident struct {
	Name         string         `json:"name"`
	Runner       string         `json:"runner"`
	Config       map[string]any `json:"config"`
	Dir          string         `json:"dir"`
	On           []string       `json:"on"`            // event kinds delivered to the inbox (mail always is), as if subscribed to yardr all kind=<k>
	MaxAge       string         `json:"max_age"`       // e.g. "6h"; "" never rotates
	HandoffGrace string         `json:"handoff_grace"` // how long a requested handoff may take
	Enabled      bool           `json:"enabled"`
	Journal      string         `json:"journal"` // journal bead handle
	// Revision changes on every write to the resident. Given to PutResident
	// (or as rev to the writes that take one) it is the revision the caller
	// read, and the write is refused with ErrConflict if it moved since.
	Revision string `json:"revision,omitempty"`

	// Who it is in each source (by source name), and which hook events it
	// hears about. Kept when the definition is replaced.
	Identities    map[string]Identity `json:"identities,omitempty"`
	Subscriptions []Subscription      `json:"subscriptions,omitempty"`

	// Runtime, maintained by the daemon.
	State string `json:"state"` // stopped running failed
	// Status is what the running session is doing, as the daemon last saw
	// it (ResidentStatus): working, idle, blocked (it waits at a prompt
	// only a person in its pane can answer) or unknown; "" when none runs
	// or the daemon has not looked yet.
	Status             string         `json:"status,omitempty"`
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

	// Left are the handles of sessions that were gone when the daemon
	// looked (ResidentGone), oldest first: what they ran in is still open
	// in the runtime, and the handle is how the daemon knows it as its own
	// when it closes all but the latest (ResidentLeft).
	Left []map[string]any `json:"left,omitempty"`
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
		if err := checkResidentRev(def.Name, r.Revision, def.Revision); err != nil {
			return err
		}
		if !ok {
			// One name is one party: mail to it must reach one of the two.
			if w.Bucket(bUsers).Get([]byte(def.Name)) != nil {
				return fmt.Errorf("resident %s: that is a user's name, and mail to a name reaches one party: choose another", def.Name)
			}
			// A new resident starts reading the inbox from now, not history.
			head := uint64(0)
			if k, _ := w.Bucket(bEvents).Cursor().Last(); k != nil {
				head = binary.BigEndian.Uint64(k)
			}
			r = Resident{Name: def.Name, State: "stopped", Enabled: true, Cursor: head, Notified: head,
				Journal: journalID(def.Name)}
			if w.Bucket(bHandles).Get([]byte(r.Journal)) == nil {
				jr := &beadRow{Bead: Bead{Title: "Journal of " + def.Name, Type: TypeJournal,
					Stage: "journal", Status: "open", Labels: []string{"resident:" + def.Name},
					CreatedAt: now(), CreatedBy: &actor}}
				if err := w.insertBead(jr, "", r.Journal, actor); err != nil {
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
		r.Revision = newRevision()
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
// A rev other than "" must be the resident's current revision.
func (s *Store) DeleteResident(ctx context.Context, name, rev, actor string) error {
	return s.changeResidentAt(name, rev, func(w *wtx, r *Resident) error {
		if err := w.Bucket(bResidents).Delete([]byte(name)); err != nil {
			return err
		}
		if err := dropThreads(w.Tx, name); err != nil {
			return err
		}
		return w.event(r.Journal, "resident_deleted", actor, map[string]any{"resident": name})
	}, true)
}

// changeResident applies fn to the stored resident and saves it under a
// fresh revision, unless deleted is set (fn removed it). The daemon's own
// bookkeeping writes through it unchecked.
func (s *Store) changeResident(name string, fn func(*wtx, *Resident) error, deleted ...bool) error {
	return s.changeResidentAt(name, "", fn, deleted...)
}

// changeResidentAt is changeResident refusing with ErrConflict unless the
// resident is at rev ("" expects nothing).
func (s *Store) changeResidentAt(name, rev string, fn func(*wtx, *Resident) error, deleted ...bool) error {
	return s.write(func(w *wtx) error {
		var r Resident
		ok, err := get(w.Tx, bResidents, name, &r)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("resident %s: %w", name, ErrNotFound)
		}
		if err := checkResidentRev(name, r.Revision, rev); err != nil {
			return err
		}
		if err := fn(w, &r); err != nil {
			return err
		}
		if len(deleted) > 0 && deleted[0] {
			return nil
		}
		r.Revision = newRevision()
		return put(w.Tx, bResidents, name, r)
	})
}

// checkResidentRev refuses a write that expected revision want of a
// resident now at have. A resident stored before revisions existed has
// none, so any expectation of it is stale: the caller read something else.
func checkResidentRev(name, have, want string) error {
	if want == "" || want == have {
		return nil
	}
	if have == "" {
		have = "none"
	}
	return fmt.Errorf("%w: resident %s is at revision %s, not %s; read it again", ErrConflict, name, have, want)
}

// SetResidentEnabled starts (true) or stops (false) keeping it alive. Enabling
// also clears a failure streak. A rev other than "" must be the resident's
// current revision.
func (s *Store) SetResidentEnabled(ctx context.Context, name string, enabled bool, rev, actor string) error {
	return s.changeResidentAt(name, rev, func(w *wtx, r *Resident) error {
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

// ResidentStarted records a fresh session. The failure streak stays: only a
// session that lives (ResidentSettled) ends it.
func (s *Store) ResidentStarted(ctx context.Context, name string, handle map[string]any) error {
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		t := now()
		r.PrevStartedAt, r.StartedAt = r.StartedAt, &t
		r.State, r.Status, r.Handle, r.RetryAfter = "running", "", handle, nil
		r.RestartRequested, r.RestartRequestedAt, r.RotationAskedAt = "", nil, nil
		// The new session sees its unread inbox in its prime; only events
		// after this point warrant a nudge.
		if k, _ := w.Bucket(bEvents).Cursor().Last(); k != nil {
			r.Notified = max(r.Notified, binary.BigEndian.Uint64(k))
		}
		return w.event(r.Journal, "resident_started", "yard", map[string]any{"resident": name, "handle": handle})
	})
}

// ResidentSettled ends the failure streak: the session has lived
// MinSessionLife, so its start worked.
func (s *Store) ResidentSettled(ctx context.Context, name string) error {
	return s.changeResident(name, func(_ *wtx, r *Resident) error {
		r.Failures = 0
		return nil
	})
}

// ResidentStatuses are what ResidentStatus takes: a runtime's word for
// what a session is doing, "done" being a turn that ended, which is idle.
var ResidentStatuses = []string{"working", "idle", "blocked", "unknown"}

// ResidentStatus records what the resident's running session is doing, as
// the daemon saw it on its pass. A change is an event, resident_status
// with the status and what it was, so a view of the residents follows it
// from the stream; the same status again writes nothing. The event is on
// no bead: a session's turns are not its journal's history.
func (s *Store) ResidentStatus(ctx context.Context, name, status string) error {
	if !slices.Contains(ResidentStatuses, status) {
		return fmt.Errorf("resident status %q: it is one of %s", status, strings.Join(ResidentStatuses, ", "))
	}
	var r Resident
	if err := s.read(func(tx *bolt.Tx) error {
		_, err := get(tx, bResidents, name, &r)
		return err
	}); err != nil {
		return err
	}
	if r.Status == status {
		return nil // no write, and no new revision, for what did not change
	}
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		if r.State != "running" || r.Status == status {
			return nil
		}
		was := r.Status
		r.Status = status
		return w.event("", "resident_status", "yard", map[string]any{"resident": name, "status": status, "was": was})
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
		r.State, r.Status, r.Handle = "stopped", "", nil
		return w.event(r.Journal, "resident_stopped", "yard", map[string]any{"resident": name, "reason": reason})
	})
}

// ResidentGone is ResidentStopped for a session that ended by itself in a
// runtime that keeps what it ran in open: its handle goes to the end of
// Left, in the same write, so no session's leavings are lost track of.
func (s *Store) ResidentGone(ctx context.Context, name, reason string) error {
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		if r.State != "running" {
			return nil
		}
		if len(r.Handle) > 0 {
			r.Left = append(r.Left, r.Handle)
		}
		r.State, r.Status, r.Handle = "stopped", "", nil
		return w.event(r.Journal, "resident_stopped", "yard", map[string]any{"resident": name, "reason": reason})
	})
}

// ResidentLeft replaces Left, once the daemon has closed some of what it
// lists.
func (s *Store) ResidentLeft(ctx context.Context, name string, left []map[string]any) error {
	return s.changeResident(name, func(_ *wtx, r *Resident) error {
		r.Left = left
		return nil
	})
}

// ResidentStartFailed counts a failed start (one that did not happen, or a
// session gone within MinSessionLife); after MaxStartFails in a row the
// resident is parked (state failed) until someone starts it again.
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
// is idle (or the handoff grace has passed). A rev other than "" must be
// the resident's current revision.
func (s *Store) RequestRestart(ctx context.Context, name, reason, rev, actor string) error {
	return s.changeResidentAt(name, rev, func(w *wtx, r *Resident) error {
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

// Mail sends a message to the inbox of a resident or of a user.
func (s *Store) Mail(ctx context.Context, to, text, actor string) error {
	if strings.TrimSpace(text) == "" || len(text) > MaxMailBytes {
		return fmt.Errorf("mail must be 1..%d bytes", MaxMailBytes)
	}
	return s.write(func(w *wtx) error {
		data := map[string]any{"to": to, "text": text}
		switch {
		case w.Bucket(bResidents).Get([]byte(to)) != nil:
		case w.Bucket(bUsers).Get([]byte(to)) != nil:
			data["to_user"] = true
		default:
			return fmt.Errorf("resident %s: %w (nor is there a user of that name)", to, ErrNotFound)
		}
		return w.event("", "mail", actor, data)
	})
}

// forResident reports whether e belongs in r's inbox: mail to it, a hook
// or bead event its subscriptions delivered to it when logged, or a kind
// it hears of through --on, never its own actions nor yardr telling of
// itself (aboutItself). --on works as implicit
// "yardr all kind=<k>" subscriptions, checked when the inbox is read, and
// also covers events not about a bead.
func forResident(r *Resident, e Event) bool {
	if e.Actor == r.Name || (e.Bead != nil && aboutItself(r, *e.Bead, e.Kind)) {
		return false
	}
	switch e.Kind {
	case "mail":
		return mailTo(e) == r.Name
	case "hook":
		return Reason(e, r.Name) != ""
	}
	return Reason(e, r.Name) != "" || slices.Contains(r.On, e.Kind)
}

// InboxPage is the start of a resident's unacknowledged events.
type InboxPage struct {
	Events []Event `json:"events"` // oldest first, at most the limit
	Total  int     `json:"total"`  // all unacknowledged events
	Last   uint64  `json:"last"`   // seq of the newest unacknowledged event
}

// Inbox returns up to limit unacknowledged events for the resident. It
// reads only the resident's own index entries and those of its --on kinds
// after its cursor, never the rest of the log.
func (s *Store) Inbox(ctx context.Context, name string, limit int) (*InboxPage, error) {
	page := &InboxPage{Events: []Event{}}
	err := s.read(func(tx *bolt.Tx) error {
		r := new(Resident)
		if ok, err := get(tx, bResidents, name, r); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("resident %s: %w", name, ErrNotFound)
		}
		var evs []Event
		for _, k := range inboxKeys(tx, r) {
			var e Event
			if ok, err := get(tx, bEvents, k, &e); err != nil {
				return err
			} else if ok {
				evs = append(evs, e)
			}
		}
		s.inboxReads.Add(int64(len(evs)))
		named(tx, evs) // forResident compares handles
		for _, e := range evs {
			if forResident(r, e) {
				page.Total++
				page.Last = e.Seq
				if limit <= 0 || len(page.Events) < limit {
					page.Events = append(page.Events, e)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

// inboxKeys are the event keys after r's cursor that may be in its inbox,
// in seq order: its inbox index entries and those of the kinds it hears of
// through --on.
func inboxKeys(tx *bolt.Tx, r *Resident) []string {
	from := seqKey(r.Cursor + 1)
	seen := map[string]bool{}
	var out []string
	scan := func(b []byte, prefix string) {
		c := tx.Bucket(b).Cursor()
		p := []byte(prefix + "\x00")
		for k, _ := c.Seek([]byte(prefix + "\x00" + from)); k != nil && bytes.HasPrefix(k, p); k, _ = c.Next() {
			if sk := string(k[len(p):]); !seen[sk] {
				seen[sk] = true
				out = append(out, sk)
			}
		}
	}
	scan(bInbox, r.Name)
	for _, kind := range r.On {
		scan(bEventsKd, kind)
	}
	slices.Sort(out) // big-endian seqs sort as numbers
	return out
}

// AckInbox marks the inbox read through seq.
func (s *Store) AckInbox(ctx context.Context, name string, through uint64) error {
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		was := r.Cursor
		r.Cursor = max(r.Cursor, through)
		r.Notified = max(r.Notified, r.Cursor)
		return w.mailRead(name, was, r.Cursor)
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
	Timers      []Timer        `json:"timers"`   // the live ones it set or is told by
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
	if p.Timers, err = s.Timers(ctx, name); err != nil {
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
		p.NoHandoff, p.EventCounts = true, map[string]int{}
		// Newest first, back to where that session began: the log before
		// it is never read.
		err := s.read(func(tx *bolt.Tx) error {
			c := tx.Bucket(bEvents).Cursor()
			for k, raw := c.Last(); k != nil; k, raw = c.Prev() {
				var e Event
				if err := json.Unmarshal(raw, &e); err != nil {
					return err
				}
				if !e.At.After(from) {
					break
				}
				p.EventCounts[e.Kind]++
				if len(p.Recent) < PrimeFallback {
					p.Recent = append(p.Recent, e)
				}
			}
			slices.Reverse(p.Recent)
			named(tx, p.Recent)
			return nil
		})
		if err != nil {
			return nil, err
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
			if _, err := s.CloseBead(ctx, m.ID, "expired", Expect{}, "yard"); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}
