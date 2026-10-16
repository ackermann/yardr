package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// dropping is a stream that ends after each batch: the yardr server going
// away between them.
type dropping struct {
	mu      sync.Mutex
	batches [][]store.Event
	asked   []uint64 // the seq each subscription was asked to start after
	refuse  int      // subscriptions to refuse first, as a server that is away does
}

func (d *dropping) Subscribe(_ context.Context, after uint64) (<-chan store.Event, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asked = append(d.asked, after)
	if len(d.asked) > 1 && d.refuse > 0 {
		d.refuse--
		return nil, errors.New("no server")
	}
	ch := make(chan store.Event, 8)
	if len(d.batches) > 0 {
		for _, e := range d.batches[0] {
			ch <- e
		}
		d.batches = d.batches[1:]
	}
	close(ch)
	return ch, nil
}

func (d *dropping) askedAfter() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fmt.Sprint(d.asked)
}

// When the stream drops, Follow says so, subscribes again after the last
// event it handed on, and says when it is back: every event once, in
// order.
func TestFollowResumesAfterTheLastEvent(t *testing.T) {
	t.Parallel()
	ev := func(seq uint64) store.Event { return store.Event{Seq: seq, Kind: "mail"} }
	d := &dropping{batches: [][]store.Event{{ev(8), ev(9)}, {ev(10)}, {}, {ev(11)}}, refuse: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.Follow(ctx, d, 7, time.Millisecond, func(e store.Event) {
			got = append(got, fmt.Sprint(e.Seq))
			if e.Seq == 11 {
				cancel()
			}
		}, func(up bool) { got = append(got, map[bool]string{true: "up", false: "down"}[up]) })
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Follow did not get to the last event")
	}
	if want := "8 9 down up 10 down up down up 11"; strings.Join(got, " ") != want {
		t.Fatalf("followed %q, want %q", strings.Join(got, " "), want)
	}
	// After 7 as asked, then after the last event each time: twice more
	// after 9 while the server was away, and after 10 for a stream that
	// brought nothing.
	if want := "[7 9 9 9 10 10]"; d.askedAfter() != want {
		t.Fatalf("subscribed after %v, want %s", d.askedAfter(), want)
	}
}

// Follow against the store itself: what was committed before it
// subscribed and what comes after reach the follower in order.
func TestFollowTheStore(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	after, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	create := func(title string) string {
		b, err := st.CreateBead(ctx, store.NewBead{Title: title}, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	want := []string{create("before")}
	got := make(chan store.Event, 16)
	go store.Follow(ctx, st, after, time.Millisecond, func(e store.Event) { got <- e }, nil)
	want = append(want, create("after"))
	for _, id := range want {
		select {
		case e := <-got:
			if e.Kind != "created" || e.Bead == nil || *e.Bead != id {
				t.Fatalf("followed %+v, want the creation of %s", e, id)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no event for %s", id)
		}
	}
}

// Following's channel stays open across streams that end and a server
// that is away, hands on every event once, and closes when ctx ends.
func TestFollowingOutlivesTheStream(t *testing.T) {
	t.Parallel()
	ev := func(seq uint64) store.Event { return store.Event{Seq: seq, Kind: "mail"} }
	d := &dropping{batches: [][]store.Event{{ev(8), ev(9)}, {ev(10)}, {}, {ev(11)}}, refuse: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := store.Following(ctx, d, 7, time.Millisecond)
	for want := uint64(8); want <= 11; want++ {
		select {
		case e, ok := <-ch:
			if !ok || e.Seq != want {
				t.Fatalf("got %+v (open %v), want seq %d", e, ok, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no event %d", want)
		}
	}
	cancel()
	select {
	case e, ok := <-ch:
		if ok {
			t.Fatalf("after ctx ended the channel brought %+v", e)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the channel stayed open after ctx ended")
	}
}
