package store

import (
	"context"
	"fmt"
	"regexp"

	bolt "go.etcd.io/bbolt"
)

var (
	bHooks        = []byte("hooks")
	bHookPayloads = []byte("hook_payloads") // <event seq>: the raw body as received
)

func init() { allBuckets = append(allBuckets, bHooks, bHookPayloads) }

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
}

// PutHook creates or replaces a hook.
func (s *Store) PutHook(ctx context.Context, h Hook, actor string) (*Hook, error) {
	if !hookName.MatchString(h.Name) {
		return nil, fmt.Errorf("hook name %q: use lowercase letters, digits, - and _", h.Name)
	}
	if h.Source == "" || h.SecretFile == "" {
		return nil, fmt.Errorf("hook %s: source and secret file are required", h.Name)
	}
	err := s.write(func(w *wtx) error {
		if err := put(w.Tx, bHooks, h.Name, h); err != nil {
			return err
		}
		return w.event("", "hook_added", actor, map[string]any{"hook": h.Name, "source": h.Source})
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

// DeleteHook removes the hook; deliveries it already accepted stay.
func (s *Store) DeleteHook(ctx context.Context, name, actor string) error {
	return s.write(func(w *wtx) error {
		if w.Bucket(bHooks).Get([]byte(name)) == nil {
			return fmt.Errorf("hook %s: %w", name, ErrNotFound)
		}
		if err := w.Bucket(bHooks).Delete([]byte(name)); err != nil {
			return err
		}
		return w.event("", "hook_removed", actor, map[string]any{"hook": name})
	})
}

// RecordHook logs an accepted delivery as one `hook` event carrying the
// normalized envelope, and keeps the raw payload beside it under the
// event's seq. The envelope is what routing reads; the payload is there
// for `yard hook show` when the envelope is not enough. Which residents it
// reaches, and why, is decided here against their subscriptions as they
// are now, and kept on the event as "deliveries". A resident that is the
// delivery's actor becomes a participant in its thread from then on.
func (s *Store) RecordHook(ctx context.Context, name string, envelope map[string]any, payload []byte) (uint64, error) {
	if len(payload) > MaxHookPayload {
		return 0, fmt.Errorf("hook %s: payload of %d bytes exceeds %d", name, len(payload), MaxHookPayload)
	}
	var seq uint64
	err := s.write(func(w *wtx) error {
		data := map[string]any{"hook": name}
		for k, v := range envelope {
			data[k] = v
		}
		deliveries, err := hookDeliveries(w.Tx, data)
		if err != nil {
			return err
		}
		data["deliveries"] = deliveries
		if err := recordParticipation(w.Tx, data); err != nil {
			return err
		}
		if err := w.event("", "hook", "hook:"+name, data); err != nil {
			return err
		}
		seq = w.Bucket(bEvents).Sequence()
		return w.Bucket(bHookPayloads).Put([]byte(seqKey(seq)), payload)
	})
	return seq, err
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
