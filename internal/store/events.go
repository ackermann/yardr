package store

import (
	"context"
	"encoding/binary"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Event is one entry of the append-only log. Seq orders all events and is
// the cursor subscribers resume from.
type Event struct {
	Seq   uint64         `json:"seq"`
	ID    string         `json:"id"`
	Bead  *string        `json:"bead,omitempty"`
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
		return nil
	})
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
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
		return nil
	})
	return out, err
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

// LogEvent records an event that has no accompanying state change, such as
// a dispatcher decision.
func (s *Store) LogEvent(ctx context.Context, bead, kind, actor string, data map[string]any) error {
	return s.write(func(w *wtx) error { return w.event(bead, kind, actor, data) })
}

// Subscribe streams every event with Seq > after, in order, with no gaps or
// duplicates: first the retained backlog, then live events as they commit.
// The channel closes when ctx ends, the store closes, or the subscriber
// falls too far behind; resubscribe from the last Seq seen to continue.
func (s *Store) Subscribe(ctx context.Context, after uint64) (<-chan Event, error) {
	// Register before reading the backlog, so nothing committed in between
	// is missed; duplicates are dropped by Seq below.
	sub := s.hub.add(4096)
	backlog, err := s.EventsSince(ctx, after, 0)
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
		for _, e := range backlog {
			if !send(e) {
				return
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
