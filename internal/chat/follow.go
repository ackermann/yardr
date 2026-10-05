package chat

import (
	"context"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Subscriber is the part of the store Follow reads.
type Subscriber interface {
	Subscribe(ctx context.Context, after uint64) (<-chan store.Event, error)
}

// Follow hands every event after seq to event, in order and once, until
// ctx ends. When the stream ends (the yardr server went away, or dropped
// the subscription as too slow) it says so with up(false), waits (wait), and
// subscribes again after the last event it handed on, so nothing between
// is lost and nothing comes twice; up(true) says the stream is back. It
// never reads the store otherwise: what happens reaches the view pushed.
func Follow(ctx context.Context, st Subscriber, after uint64, wait time.Duration, event func(store.Event), up func(bool)) {
	last, down := after, false
	for ctx.Err() == nil {
		ch, err := st.Subscribe(ctx, last)
		if err == nil {
			if down {
				down = false
				up(true)
			}
			for e := range ch {
				last = e.Seq
				event(e)
			}
		}
		if ctx.Err() != nil {
			return
		}
		if !down {
			down = true
			up(false)
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
}
