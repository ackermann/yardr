package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

var (
	bHooks        = []byte("hooks")
	bHookPayloads = []byte("hook_payloads") // <event seq>: the raw body as received
	bHookSeen     = []byte("hook_seen")     // <dedupe key>: seq of the event that recorded it
	bHookMentions = []byte("hook_mentions") // <source> 0 <entity>: who its text mentioned last
)

func init() { allBuckets = append(allBuckets, bHooks, bHookPayloads, bHookSeen, bHookMentions) }

// MaxHookPayload bounds a stored delivery; the receiver rejects larger
// bodies before they reach the store.
const MaxHookPayload = 1 << 20

// hookName is what may appear in /hooks/<name>.
var hookName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Hook is a named webhook endpoint. The secret stays in SecretFile, read
// when a delivery is verified, so it never enters the store, the event log
// or a command line.
type Hook struct {
	Name       string `json:"name"`
	Source     string `json:"source"` // adapter: linear, token
	SecretFile string `json:"secret_file"`
	// Blocked is why the receiver refuses this hook's deliveries (its
	// secret file is missing or open to others), empty while it takes them.
	// Only the receiver sets it, through BlockHook.
	Blocked string `json:"blocked,omitempty"`
	// Map says which status an issue of the source goes to when the bead
	// tracking it enters a stage: "<bead type>:<stage>" -> started, review
	// or done (see IssueMove). As is the resident the change is made as:
	// with its credential, under its identity in the source, and only where
	// its definition allows it (SetState).
	As  string            `json:"as,omitempty"`
	Map map[string]string `json:"map,omitempty"`
}

// ThreadStates are the words a thread's status is asked for by, in the
// order work passes through them (hooks.States, which a test holds equal).
var ThreadStates = []string{"started", "review", "done"}

// MapKey is the key of a Hook's Map for beads of type typ entering stage.
func MapKey(typ, stage string) string { return typ + ":" + stage }

// checkMap refuses a hook whose map could never be acted on: one without
// the resident to act as, a line that names no bead type and stage, or a
// word that is no state.
func (h Hook) checkMap() error {
	if len(h.Map) > 0 && h.As == "" {
		return fmt.Errorf("hook %s: a map needs the resident it acts as (--as): the issue is moved with that resident's credential, under its identity", h.Name)
	}
	for k, word := range h.Map {
		typ, stage, ok := strings.Cut(k, ":")
		if !ok {
			return fmt.Errorf("hook %s: map %q: want <bead type>:<stage>=<state>", h.Name, k+"="+word)
		}
		if err := checkName("hook "+h.Name+": map "+k+": bead type", typ); err != nil {
			return err
		}
		if err := checkName("hook "+h.Name+": map "+k+": stage", stage); err != nil {
			return err
		}
		if !slices.Contains(ThreadStates, word) {
			return fmt.Errorf("hook %s: map %s=%s: %q is no state: want %s", h.Name, k, word, word, strings.Join(ThreadStates, ", "))
		}
	}
	return nil
}

// PutHook creates or replaces a hook. A replaced hook starts unblocked: the
// receiver finds out again on the next delivery. A hook that acts as a
// resident (As) is refused when there is no such resident, or when it has
// no identity in the hook's source: the source's delivery of a change made
// as the resident is kept from the resident's inbox by that identity alone.
func (s *Store) PutHook(ctx context.Context, h Hook, actor string) (*Hook, error) {
	h.Blocked = ""
	if !hookName.MatchString(h.Name) {
		return nil, fmt.Errorf("hook name %q: use lowercase letters, digits, - and _", h.Name)
	}
	if h.Source == "" || h.SecretFile == "" {
		return nil, fmt.Errorf("hook %s: source and secret file are required", h.Name)
	}
	if err := h.checkMap(); err != nil {
		return nil, err
	}
	err := s.write(func(w *wtx) error {
		data := map[string]any{"hook": h.Name, "source": h.Source}
		if h.As != "" {
			var r Resident
			if ok, err := get(w.Tx, bResidents, h.As, &r); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("hook %s: --as %s: resident %s: %w", h.Name, h.As, h.As, ErrNotFound)
			}
			if r.Identities[h.Source].zero() {
				return fmt.Errorf("hook %s: --as %s: resident %s has no identity in %s, so the delivery of a change made as it would reach its own inbox: yardr resident identity %s --source %s --id <its id there>",
					h.Name, h.As, h.As, h.Source, h.As, h.Source)
			}
			data["as"] = h.As
		}
		if len(h.Map) > 0 {
			data["map"] = h.Map
		}
		if err := put(w.Tx, bHooks, h.Name, h); err != nil {
			return err
		}
		return w.event("", "hook_added", actor, data)
	})
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func (s *Store) Hooks(ctx context.Context) ([]Hook, error) {
	out := []Hook{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bHooks, "", func(_ []byte, h *Hook) error {
			out = append(out, *h)
			return nil
		})
	})
	return out, err
}

func (s *Store) GetHook(ctx context.Context, name string) (*Hook, error) {
	h := new(Hook)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bHooks, name, h)
		if err == nil && !ok {
			err = fmt.Errorf("hook %s: %w", name, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

// DeleteHook removes the hook; deliveries it already accepted stay. The
// issue moves it had still to make (IssueMoves) are dropped with it, and
// the hook_removed event says how many.
func (s *Store) DeleteHook(ctx context.Context, name, actor string) error {
	return s.write(func(w *wtx) error {
		if w.Bucket(bHooks).Get([]byte(name)) == nil {
			return fmt.Errorf("hook %s: %w", name, ErrNotFound)
		}
		if err := w.Bucket(bHooks).Delete([]byte(name)); err != nil {
			return err
		}
		data := map[string]any{"hook": name}
		// What the hook had still to do is not done behind the back of
		// whoever removed it.
		dropped, err := dropIssueMoves(w.Tx, name)
		if err != nil {
			return err
		}
		if dropped > 0 {
			data["issue_moves_dropped"] = dropped
		}
		return w.event("", "hook_removed", actor, data)
	})
}

// BlockHook records that the receiver refuses the hook's deliveries, and
// why, or with an empty reason that it takes them again. One `hook_blocked`
// event is logged when the state begins, not for every delivery refused
// while it lasts; it carries the hook, the path and the reason, never what
// the file holds.
//
// Like RecordHook it is not on the API: only the receiver sees the file as
// the deliveries do.
func (s *Store) BlockHook(ctx context.Context, name, reason string) error {
	return s.write(func(w *wtx) error {
		var h Hook
		if ok, err := get(w.Tx, bHooks, name, &h); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("hook %s: %w", name, ErrNotFound)
		}
		if h.Blocked == reason {
			return nil
		}
		began := h.Blocked == ""
		h.Blocked = reason
		if err := put(w.Tx, bHooks, name, h); err != nil {
			return err
		}
		if !began {
			return nil
		}
		return w.event("", "hook_blocked", "hook:"+name, map[string]any{"hook": name, "path": h.SecretFile, "reason": reason})
	})
}

// RecordHook logs an accepted delivery as one `hook` event carrying the
// normalized envelope, and keeps the raw payload beside it under the
// event's seq. The envelope is what routing reads; the payload is there
// for `yardr hook show` when the envelope is not enough. Which residents it
// reaches, and why, is decided here against their subscriptions as they
// are now, and kept on the event as "deliveries"; the users it reaches are
// kept as "notified" and each gets a notification. A resident that is the
// delivery's actor becomes a participant in its thread from then on.
//
// dedupe are the keys that identify this delivery (see hooks.DedupeKeys).
// When any of them was recorded before, nothing is written and RecordHook
// returns that delivery's seq with duplicate set. The check and the write
// share one transaction, so of two concurrent redeliveries exactly one is
// recorded.
//
// When the envelope names an entity (an issue whose description persists
// across deliveries), only the mentions that entity's text did not already
// carry count as mentions for routing; the full list stays on the event.
//
// RecordHook is not on the API: only the webhook receiver, in the process
// that owns the store, records deliveries, so no client can forge one.
func (s *Store) RecordHook(ctx context.Context, name string, envelope map[string]any, payload []byte, dedupe []string) (seq uint64, duplicate bool, err error) {
	if len(payload) > MaxHookPayload {
		return 0, false, fmt.Errorf("hook %s: payload of %d bytes exceeds %d", name, len(payload), MaxHookPayload)
	}
	err = s.write(func(w *wtx) error {
		seq, duplicate = 0, false // a batched write may run this twice
		seen := w.Bucket(bHookSeen)
		for _, k := range dedupe {
			if v := seen.Get([]byte(k)); v != nil {
				seq, duplicate = binary.BigEndian.Uint64(v), true
				return nil
			}
		}
		data := map[string]any{"hook": name}
		for k, v := range envelope {
			data[k] = v
		}
		if err := freshMentions(w.Tx, data); err != nil {
			return err
		}
		deliveries, err := hookDeliveries(w.Tx, data)
		if err != nil {
			return err
		}
		data["deliveries"] = deliveries
		told, err := hookNotified(w.Tx, data)
		if err != nil {
			return err
		}
		if len(told) > 0 {
			data["notified"] = told
		}
		if err := recordParticipation(w.Tx, data); err != nil {
			return err
		}
		if err := w.event("", "hook", "hook:"+name, data); err != nil {
			return err
		}
		seq = w.Bucket(bEvents).Sequence()
		if len(told) > 0 {
			var e Event
			if _, err := get(w.Tx, bEvents, seqKey(seq), &e); err != nil {
				return err
			}
			if err := w.notify(hookNotification(seq, e.At, data), told); err != nil {
				return err
			}
		}
		for _, k := range dedupe {
			if err := seen.Put([]byte(k), []byte(seqKey(seq))); err != nil {
				return err
			}
		}
		return w.Bucket(bHookPayloads).Put([]byte(seqKey(seq)), payload)
	})
	return seq, duplicate, err
}

// freshMentions diffs the mentions of a delivery that names an entity
// against those the entity's text carried last time, and keeps the new set.
// Routing then reads "new_mentions", so a resident mentioned in an issue's
// description, or in a comment, is told once, not again on every status
// or label change nor on every edit of the comment.
func freshMentions(tx *bolt.Tx, data map[string]any) error {
	entity, _ := data["entity"].(string)
	source, _ := data["source"].(string)
	if entity == "" {
		return nil
	}
	key := []byte(join(source, entity))
	b := tx.Bucket(bHookMentions)
	var prev []string
	if raw := b.Get(key); raw != nil {
		if err := json.Unmarshal(raw, &prev); err != nil {
			return err
		}
	}
	now := strs(data["mentions"])
	fresh := []string{}
	for _, m := range now {
		if !slices.Contains(prev, m) {
			fresh = append(fresh, m)
		}
	}
	data["new_mentions"] = fresh
	// The entity is gone (Linear says remove, GitHub deleted): forget what
	// it mentioned, so nothing is held against an id that comes back.
	if action, _ := data["action"].(string); action == "remove" || action == "deleted" {
		return b.Delete(key)
	}
	raw, err := json.Marshal(now)
	if err != nil {
		return err
	}
	return b.Put(key, raw)
}

// HookPayload returns the raw body of the delivery logged as event seq.
func (s *Store) HookPayload(ctx context.Context, seq uint64) ([]byte, error) {
	var out []byte
	err := s.read(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bHookPayloads).Get([]byte(seqKey(seq)))
		if raw == nil {
			return fmt.Errorf("hook payload for event %d: %w", seq, ErrNotFound)
		}
		out = append([]byte{}, raw...)
		return nil
	})
	return out, err
}
