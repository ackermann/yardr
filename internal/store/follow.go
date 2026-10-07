package store

import (
	"context"
	"time"
)

// Subscriber is the part of the store Follow reads.
type Subscriber interface {
	Subscribe(ctx context.Context, after uint64) (<-chan Event, error)
}

// Follow hands every event after seq to event, in order and once, until
// ctx ends. When the stream ends (the yardr server went away, or dropped
// the subscription as too slow) it says so with up(false), waits (wait), and
// subscribes again after the last event it handed on, so nothing between
// is lost and nothing comes twice; up(true) says the stream is back. A
// follower that shows neither passes a nil up. It never starts the yardr
// server: any yard command does, so a deliberate stop stays stopped.
func Follow(ctx context.Context, st Subscriber, after uint64, wait time.Duration, event func(Event), up func(bool)) {
	if up == nil {
		up = func(bool) {}
	}
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

// Following is Follow as a channel, for a follower that waits on something
// else too (a heartbeat, a timer): unlike a subscription's, it stays open
// while the yardr server is away and closes only when ctx ends. An event is
// handed on when the one before it was taken, so a reader that stops and
// ends ctx has lost nothing it did not take.
func Following(ctx context.Context, st Subscriber, after uint64, wait time.Duration) <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		Follow(ctx, st, after, wait, func(e Event) {
			select {
			case out <- e:
			case <-ctx.Done():
			}
		}, nil)
	}()
	return out
}
