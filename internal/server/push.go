package server

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"yardr.dev/yardr/internal/edgelink"
	"yardr.dev/yardr/internal/store"
)

// pushTick is how often the footplate sender sweeps the queue and sends
// what is due; tests shorten it.
var pushTick = time.Second

// pushSender reports the footplate queue to the relay: the intents and
// removals the store queued, as link frames up the yard role's link. Rows
// are sent oldest first while the link is up and wait out its absence
// with their event ids, which the relay deduplicates; past their age they
// drop untold. The sweep runs with no edge set too, so a direct yard's
// rows age out instead of piling.
type pushSender struct {
	st   *store.Store
	log  *slog.Logger
	link func() *edgelink.Link
	// ups is the link's Ups as last seen, so a new link sends again
	// what waits for an ack. Only the run loop touches it.
	ups int
}

// startPushSender runs the sender until the function it returns is called.
func startPushSender(ctx context.Context, s *pushSender) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.run(ctx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *pushSender) run(ctx context.Context) {
	t := time.NewTicker(pushTick)
	defer t.Stop()
	for {
		s.once(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *pushSender) once(ctx context.Context) {
	if expired, stale, err := s.st.FootSweep(ctx); err != nil {
		if ctx.Err() == nil {
			s.log.Warn("push: the footplate queue is not swept", "err", err)
		}
	} else if expired+stale > 0 {
		s.log.Info("push: footplate rows past telling dropped", "expired", expired, "stale", stale)
	}
	l := s.link()
	if l == nil {
		return // the rows wait for the link's return, with their ids
	}
	state := l.State()
	if !state.Up {
		return
	}
	if state.Ups != s.ups {
		// A new link, which never saw what waits for an ack: what waits
		// for one goes again with the same ids.
		s.ups = state.Ups
		if err := s.st.FootLinkReturned(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("push: the footplate queue is not readied for the link", "err", err)
		}
	}
	due, err := s.st.FootDue(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("push: the footplate queue is not read", "err", err)
		}
		return
	}
	for _, f := range due {
		if err := l.SendPush(ctx, footFrame(f)); err != nil {
			// The link went down mid-pass: the rows keep, and the next
			// pass sends them again with the same ids.
			return
		}
		if _, err := s.st.FootSent(ctx, f.EventID); err != nil && ctx.Err() == nil {
			s.log.Warn("push: a footplate send is not recorded", "event", f.EventID, "err", err)
		}
	}
}

// footFrame is the row as the link carries it: an intent names the user,
// the device, the kind and the event id, and a removal the user and the
// device with the id the relay's ack matches. Never a platform or a push
// token: the yard holds none.
func footFrame(f store.FootItem) edgelink.PushFrame {
	if f.Kind == store.FootRemove {
		return edgelink.PushFrame{Type: edgelink.PushRemove, EventID: f.EventID, User: f.User, Device: f.Device}
	}
	return edgelink.PushFrame{Type: edgelink.PushIntent, EventID: f.EventID, User: f.User, Device: f.Device, Kind: f.Kind}
}

// ack is the relay's answer to an intent or removal, from the link's read
// loop: recorded without blocking it.
func (s *pushSender) ack(a edgelink.PushAck) {
	go s.handleAck(a)
}

func (s *pushSender) handleAck(a edgelink.PushAck) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Refusal text and the ack id both came from the edge. Use the refusal
	// only to decide whether to retry: neither belongs in the store or logs.
	switch {
	case a.Refused == "":
		if _, err := s.st.FootLanded(ctx, a.EventID); err != nil {
			s.log.Warn("push: an acked footplate row is not dropped", "err", err)
		}
	case pushRetryable(a.Refused):
		kept, err := s.st.FootRetry(ctx, a.EventID)
		switch {
		case err != nil:
			s.log.Warn("push: a footplate retry is not recorded", "err", err)
		case !kept:
			s.log.Warn("push: a footplate row is given up")
		}
	default:
		// The relay will never take it: no registered device, past its
		// cap, or not an intent it reads. Dropped, not retried.
		if _, err := s.st.FootLanded(ctx, a.EventID); err != nil {
			s.log.Warn("push: a refused footplate row is not dropped", "err", err)
		} else {
			s.log.Info("push: the relay refused a footplate row; dropped")
		}
	}
}

// pushRetryable reports whether the relay's refused word asks for the row
// again: its two transient words end "send it again" (handlePushFrame in
// the router), and every other refusal drops the row.
func pushRetryable(refused string) bool {
	return strings.HasSuffix(refused, "send it again")
}
