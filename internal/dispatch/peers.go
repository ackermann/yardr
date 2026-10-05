package dispatch

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
	"yardr.dev/yardr/internal/yards"
)

// Messages between this yard and its peers (docs/peers.md) wait in the
// store, in an outbox for each peer, and the daemon is what carries them,
// as it makes the issue moves (issues.go) and for the same reasons: a call
// out of the yard is made outside the store's write, and every pass reads
// the outbox again, so a daemon restarted carries on with no state of its
// own.
//
// Only a peer with a URL is called. To it the daemon posts what waits, in
// order, and of it the daemon asks what waits there for this yard: a yard
// that cannot be called (a laptop behind a router) gets its messages by
// asking. A yard that can be called asks all the same, and is handed
// nothing: whether the other side has its URL is the other side's to know,
// and may change there.
//
// A message may be posted twice (the daemon died between the peer's answer
// and the store's write): the peer takes an id once.

const (
	// peerTimeout bounds one request to a peer from a pass.
	peerTimeout = 30 * time.Second
	// peerBurst bounds the messages posted to one peer in a pass.
	peerBurst = 16
)

// peers posts and asks every peer that can be called.
func (d *Dispatcher) peers(ctx context.Context) error {
	ps, err := d.Store.Peers(ctx)
	if err != nil {
		return err
	}
	d.peerCalls = false
	for _, p := range ps {
		d.peerCalls = d.peerCalls || p.URL != ""
	}
	if !d.peerCalls {
		return nil
	}
	if d.peerWait == nil {
		d.peerWait = map[string]time.Time{}
	}
	// Read every pass, as a hook's secret is on every delivery: the file
	// may have been made, or loosened, since.
	key, err := yardkey.Load(d.PeerKey)
	if err != nil {
		if msg := err.Error(); msg != d.peerKeyErr {
			d.peerKeyErr = msg
			if d.PeerKey == "" || errors.Is(err, fs.ErrNotExist) {
				d.Log.Warn("this yard has peers and no key, so it sends and asks for nothing; yardr peer key makes one", "file", d.PeerKey)
			} else {
				d.Log.Warn("this yard's key cannot be read, so it sends its peers nothing and asks for nothing", "err", err)
			}
		}
		return nil
	}
	d.peerKeyErr = ""
	c := &yards.Caller{Key: key, HTTP: d.PeerHTTP, Now: d.now}
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	for _, p := range ps {
		// A peer that just failed is not called again in the same breath.
		if until, ok := d.peerWait[p.Name]; p.URL == "" || ok && d.now().Before(until) {
			continue
		}
		// Posted first: a peer that is down then costs its oldest message a
		// try, which is what gives the message up in the end. What the
		// asking brings may be answered at once (a ping), so the answer is
		// posted in this pass too.
		if d.push(ctx, c, p) && d.pull(ctx, c, p) > 0 {
			d.push(ctx, c, p)
		}
	}
	return nil
}

// pull asks p for what waits there and hands the answer to the store,
// which checks whose it is. It returns how many messages that brought.
func (d *Dispatcher) pull(ctx context.Context, c *yards.Caller, p store.Peer) int {
	cctx, cancel := context.WithTimeout(ctx, peerTimeout)
	batch, err := c.Pull(cctx, p, p.Pulled)
	cancel()
	if err != nil {
		d.peerFailed(p, "asking", err)
		return 0
	}
	res, err := d.Store.PeerPulled(ctx, p.Name, batch)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) { // removed meanwhile
			d.peerFailed(p, "taking what it handed over", err)
		}
		return 0
	}
	delete(d.peerWait, p.Name)
	if len(res.Taken) > 0 {
		d.Log.Info("messages pulled from peer", "peer", p.Name, "messages", len(res.Taken))
	}
	return len(res.Taken)
}

// push posts what waits for p, oldest first, and stops at the first that
// is not due or fails: a message never overtakes one before it. false when
// p did not answer as itself, so there is no use in asking it either.
func (d *Dispatcher) push(ctx context.Context, c *yards.Caller, p store.Peer) bool {
	msgs, err := d.Store.PeerOutbox(ctx, p.Name, peerBurst)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			d.Log.Warn("peer outbox", "peer", p.Name, "err", err)
		}
		return false
	}
	for _, m := range msgs {
		if m.RetryAt != nil && d.now().Before(*m.RetryAt) {
			return true
		}
		cctx, cancel := context.WithTimeout(ctx, peerTimeout)
		err := c.Push(cctx, p, m.PeerMessage)
		cancel()
		switch {
		case err == nil:
			delete(d.peerWait, p.Name)
			d.Log.Info("message sent to peer", "peer", p.Name, "kind", m.Kind, "id", m.ID)
			if err := d.Store.PeerDelivered(ctx, p.Name, m.Seq); err != nil && !errors.Is(err, store.ErrNotFound) {
				// It stays and is posted again; the peer takes an id once.
				d.Log.Warn("peer_message_sent event", "peer", p.Name, "id", m.ID, "err", err)
				return true
			}
		case errors.Is(err, yards.ErrRefused):
			// The peer's own word, signed: no try will change it.
			d.Log.Warn("message refused by peer", "peer", p.Name, "kind", m.Kind, "id", m.ID, "err", err)
			d.peerGaveUp(ctx, m, err.Error(), d.now(), true)
		default:
			tries := m.Attempts + 1
			final := tries >= d.PeerTries
			if m.Attempts == 0 || final {
				d.Log.Warn("message not sent to peer", "peer", p.Name, "kind", m.Kind, "id", m.ID, "tries", tries, "given_up", final, "err", err)
			}
			d.peerGaveUp(ctx, m, err.Error(), d.now().Add(min(d.Backoff<<min(tries-1, 30), d.BackoffMax)), final)
			d.peerWait[p.Name] = d.now().Add(d.Backoff)
			return false
		}
	}
	return true
}

// peerGaveUp records a post that failed: to be tried again at retry, or
// with final given up.
func (d *Dispatcher) peerGaveUp(ctx context.Context, m store.PeerOut, reason string, retry time.Time, final bool) {
	if err := d.Store.PeerSendFailed(ctx, m.Peer, m.Seq, reason, retry, final); err != nil && !errors.Is(err, store.ErrNotFound) {
		d.Log.Warn("peer message: recording the failure", "peer", m.Peer, "id", m.ID, "err", err)
	}
}

// peerFailed leaves p alone for a while after a request that failed, and
// says so once per error rather than on every pass.
func (d *Dispatcher) peerFailed(p store.Peer, doing string, err error) {
	if d.peerErr == nil {
		d.peerErr = map[string]string{}
	}
	if msg := err.Error(); d.peerErr[p.Name] != msg {
		d.peerErr[p.Name] = msg
		d.Log.Warn("peer: "+doing+" failed; trying again", "peer", p.Name, "err", err)
	}
	d.peerWait[p.Name] = d.now().Add(d.Backoff)
}
