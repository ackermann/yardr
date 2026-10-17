package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Event is one entry of the append-only log. Seq orders all events and is
// the cursor subscribers resume from.
type Event struct {
	Seq   uint64         `json:"seq"`
	ID    string         `json:"id"`
	Bead  *string        `json:"bead,omitempty"` // stored as the UUID, returned as the handle
	Kind  string         `json:"kind"`
	Actor string         `json:"actor"`
	Data  map[string]any `json:"data,omitempty"`
	At    time.Time      `json:"at"`
}

// Events returns the most recent events, oldest first; bead filters if set.
func (s *Store) Events(ctx context.Context, bead string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []Event
	err := s.read(func(tx *bolt.Tx) error {
		if bead != "" {
			u, err := resolve(tx, bead)
			if err != nil {
				return err
			}
			bead = u
		}
		evs := tx.Bucket(bEvents)
		if bead == "" {
			c := evs.Cursor()
			for k, _ := c.Last(); k != nil && len(out) < limit; k, _ = c.Prev() {
				var e Event
				if ok, err := get(tx, bEvents, string(k), &e); err != nil || !ok {
					return err
				}
				out = append(out, e)
			}
		} else {
			ks := keys(tx, bEventsBy, bead+"\x00")
			for i := len(ks) - 1; i >= 0 && len(out) < limit; i-- {
				var e Event
				if _, err := get(tx, bEvents, ks[i][len(bead)+1:], &e); err != nil {
					return err
				}
				out = append(out, e)
			}
		}
		named(tx, out)
		return nil
	})
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, err
}

// EventsOfKind returns the most recent events of one kind, oldest first, at
// most limit of them: read through the index by kind, so what it costs is
// what it returns and not the length of the log.
func (s *Store) EventsOfKind(ctx context.Context, kind string, limit int) ([]Event, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []Event
	err := s.read(func(tx *bolt.Tx) error {
		prefix := []byte(kind + "\x00")
		c := tx.Bucket(bEventsKd).Cursor()
		// The key after the kind's last: the one before it is that last.
		k, _ := c.Seek([]byte(kind + "\x01"))
		if k == nil {
			k, _ = c.Last()
		} else {
			k, _ = c.Prev()
		}
		for ; k != nil && bytes.HasPrefix(k, prefix) && len(out) < limit; k, _ = c.Prev() {
			var e Event
			if ok, err := get(tx, bEvents, string(k[len(prefix):]), &e); err != nil {
				return err
			} else if ok {
				out = append(out, e)
			}
		}
		named(tx, out)
		return nil
	})
	slices.Reverse(out)
	return out, err
}

// EventsSince returns up to limit events with Seq > after, oldest first.
func (s *Store) EventsSince(ctx context.Context, after uint64, limit int) ([]Event, error) {
	var out []Event
	err := s.read(func(tx *bolt.Tx) error {
		c := tx.Bucket(bEvents).Cursor()
		for k, _ := c.Seek([]byte(seqKey(after + 1))); k != nil && (limit <= 0 || len(out) < limit); k, _ = c.Next() {
			var e Event
			if _, err := get(tx, bEvents, string(k), &e); err != nil {
				return err
			}
			out = append(out, e)
		}
		named(tx, out)
		return nil
	})
	return out, err
}

// named replaces the bead UUIDs events are stored with by handles.
func named(tx *bolt.Tx, evs []Event) {
	h := handlesIn(tx)
	for i := range evs {
		if evs[i].Bead != nil {
			v := h.of(*evs[i].Bead)
			evs[i].Bead = &v
		}
	}
}

// Head is the sequence of the newest event (0 when there are none).
func (s *Store) Head(ctx context.Context) (uint64, error) {
	var head uint64
	err := s.read(func(tx *bolt.Tx) error {
		if k, _ := tx.Bucket(bEvents).Cursor().Last(); k != nil {
			head = binary.BigEndian.Uint64(k)
		}
		return nil
	})
	return head, err
}

// LogEvent records an event that has no accompanying state change, of any
// kind. It is not on the API, so only the process that owns the store can
// write arbitrary kinds (a `hook` among them); clients use LogDispatch.
func (s *Store) LogEvent(ctx context.Context, bead, kind, actor string, data map[string]any) error {
	return s.write(func(w *wtx) error { return w.event(bead, kind, actor, data) })
}

// InstallFailed and RolledBack are the automatic install's failure kinds:
// an install of a new yardr failed, and a failed install put the previous
// build back. Both are yard events, about no bead, told by kind
// subscription (deliverBeadless); the operator holds one by default
// (upgradeFailureSub).
const (
	InstallFailed = "install_failed"
	RolledBack    = "rolled_back"
)

// DispatchKinds are the events LogDispatch accepts: what the daemon and its
// runners report about the work they supervise, none of which changes state.
// move_refused is the store's own (see Store.refused) and is here as what
// it is: a report on a session that changes nothing.
var DispatchKinds = []string{
	"daemon_replaced", "daemon_started", "replace_skipped",
	"session_prompted", "session_harness_starting", "session_prompt_lost", "session_prompt_typed", "session_prompt_gave_up", "session_blocked", "session_unblocked", "session_idle", "session_idle_nudge", "session_aged", "session_stalled", "session_harness_error", "session_harness_dialog", "session_harness_retry", "session_model_mismatch",
	"move_refused",
	"runtime_down", "runtime_up",
	"backup_failed", "install_failed", "rolled_back",
	"workspace_kept", "workspace_removed",
	"gave_up", "backoff", "capped", "quota_wait", "unrouted", "no_runner", "bad_depot", "bad_flow", "stranded",
	"crew_blocked", "nudge_held", "human_notified", "waited",
	"train_branched", "bad_train", "wagon_without_train", "hook_rule",
	HookQuiet, QuietStarted, QuietPassed, QuietFailed,
}

// LogDispatch records a dispatcher or runner decision. It accepts only
// DispatchKinds, so a client on the socket cannot pass off an event of
// another kind (a webhook delivery, a state change) as the yard's own. A
// failed-session event also marks its live claim for ETA pairing without
// changing the bead's revision. From a session's or a crew member's pane
// the report is recorded as that session's actor or that crew member,
// whatever actor says; from anywhere else, as given (paneAuthor).
func (s *Store) LogDispatch(ctx context.Context, bead, kind, actor string, data map[string]any) error {
	if !slices.Contains(DispatchKinds, kind) {
		return fmt.Errorf("event kind %q is not a dispatch event", kind)
	}
	return s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		if etaFailedKinds[kind] {
			if session, _ := data["session"].(string); session != "" {
				var sess Session
				if ok, err := get(w.Tx, bSessions, session, &sess); err != nil {
					return err
				} else if ok && sess.live() && bead != "" && !strings.Contains(bead, "@") {
					uuid, err := resolve(w.Tx, bead)
					if err != nil {
						return err
					}
					if sess.Bead == uuid {
						if err := put(w.Tx, bETAFailed, session, true); err != nil {
							return err
						}
					}
				}
			}
		}
		return w.event(bead, kind, actor, data)
	})
}

// backlogPage bounds the events one read transaction of a Subscribe
// backlog decodes, so a subscriber far behind does not pin the file's pages
// for the whole replay.
const backlogPage = 1000

// Subscribe streams every event with Seq > after, in order, with no gaps or
// duplicates: first the retained backlog, then live events as they commit.
// The channel closes when ctx ends, the store closes, or the subscriber
// falls too far behind; resubscribe from the last Seq seen to continue.
func (s *Store) Subscribe(ctx context.Context, after uint64) (<-chan Event, error) {
	// Register before reading the backlog, so nothing committed in between
	// is missed; duplicates are dropped by Seq below.
	sub := s.hub.add(4096)
	// The first page is read here so a failing store errs to the caller;
	// the rest is read page by page as the subscriber takes it.
	page, err := s.EventsSince(ctx, after, backlogPage)
	if err != nil {
		s.hub.remove(sub)
		return nil, err
	}
	out := make(chan Event)
	go func() {
		defer close(out)
		defer s.hub.remove(sub)
		last := after
		send := func(e Event) bool {
			if e.Seq <= last {
				return true
			}
			select {
			case out <- e:
				last = e.Seq
				return true
			case <-ctx.Done():
				return false
			}
		}
		for len(page) > 0 {
			for _, e := range page {
				if !send(e) {
					return
				}
			}
			if len(page) < backlogPage {
				break
			}
			if page, err = s.EventsSince(ctx, last, backlogPage); err != nil {
				return // the subscriber resubscribes from last
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-sub.ch:
				if !ok || !send(e) {
					return
				}
			}
		}
	}()
	return out, nil
}

// TailEvents streams events committed from now on.
func (s *Store) TailEvents(ctx context.Context) (<-chan Event, error) {
	head, err := s.Head(ctx)
	if err != nil {
		return nil, err
	}
	return s.Subscribe(ctx, head)
}

// Wake returns a channel that receives (coalesced) after every committed
// write. A wake means only "state may have changed": re-read it.
func (s *Store) Wake(ctx context.Context) (<-chan struct{}, error) {
	ch := s.hub.addWaker()
	go func() {
		<-ctx.Done()
		s.hub.removeWaker(ch)
	}()
	return ch, nil
}

// PeerWake is Wake for one peer, by its key: its channel receives after a
// committed write that put a message in that peer's outbox, and after no
// other. It is what a pull the peer holds open waits on (yards.Receiver),
// so it is not on the API.
func (s *Store) PeerWake(ctx context.Context, key string) (<-chan struct{}, error) {
	ch := s.hub.addPeerWaker(key)
	go func() {
		<-ctx.Done()
		s.hub.removePeerWaker(key, ch)
	}()
	return ch, nil
}
