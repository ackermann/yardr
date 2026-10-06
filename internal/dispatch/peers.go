package dispatch

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"net/http"
	"slices"
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
	// What a bead's stay at a peer came to and the store's own write could
	// not apply (its depot's repository did not read) is applied now.
	if err := d.Store.SettlePeerBeads(ctx); err != nil {
		d.Log.Warn("peers: moving on a bead whose stay at a peer ended", "err", err)
	}
	// A bead a peer sent that nobody here released in the time its intake
	// gives goes back as refused, before the outbox is posted: the return
	// leaves in this pass.
	if err := d.Store.ExpirePeerBeads(ctx, d.now()); err != nil {
		d.Log.Warn("peers: returning a bead nobody released", "err", err)
	}
	ps, err := d.Store.Peers(ctx)
	if err != nil {
		return err
	}
	d.peerCalls = false
	for _, p := range ps {
		d.peerCalls = d.peerCalls || p.URL != ""
	}
	// What the daemon keeps of a peer it keeps by the peer's key, not its
	// name: a rename changes nothing of it, and a yard added under a name
	// that a removal or a rename freed is another yard, not left alone for
	// the failures of the one before. What is no peer's any more goes.
	gone := func(key string) bool {
		return !slices.ContainsFunc(ps, func(p store.Peer) bool { return p.Key == key })
	}
	maps.DeleteFunc(d.peerWait, func(key string, _ time.Time) bool { return gone(key) })
	maps.DeleteFunc(d.peerErr, func(key, _ string) bool { return gone(key) })
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
	c := &yards.Caller{
		Key: key, HTTP: d.PeerHTTP, Now: d.now,
		Heard: func(ctx context.Context, p store.Peer, answer store.PeerAnswer) {
			if err := d.Store.PeerPostAnswered(ctx, p.Name, answer); err != nil && !errors.Is(err, store.ErrNotFound) {
				d.Log.Warn("peer: keeping what it says it understands", "peer", p.Name, "err", err)
			}
		},
	}
	if c.HTTP == nil {
		c.HTTP = http.DefaultClient
	}
	for _, p := range ps {
		// A peer that just failed is not called again in the same breath.
		if until, ok := d.peerWait[p.Key]; p.URL == "" || ok && d.now().Before(until) {
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
	delete(d.peerWait, p.Key)
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
	for i, m := range msgs {
		if m.RetryAt != nil && d.now().Before(*m.RetryAt) {
			return true
		}
		cctx, cancel := context.WithTimeout(ctx, peerTimeout)
		err := c.Push(cctx, p, m.PeerMessage)
		cancel()
		switch {
		case err == nil:
			delete(d.peerWait, p.Key)
			d.Log.Info("message sent to peer", "peer", p.Name, "kind", m.Kind, "id", m.ID)
			if err := d.Store.PeerDelivered(ctx, p.Name, m.Seq); err != nil && !errors.Is(err, store.ErrNotFound) {
				// It stays and is posted again; the peer takes an id once.
				d.Log.Warn("peer_message_sent event", "peer", p.Name, "id", m.ID, "err", err)
				return true
			}
		case errors.Is(err, yards.ErrRefused):
			// The peer's own word, signed: no try will change it.
			d.Log.Warn("message refused by peer", "peer", p.Name, "kind", m.Kind, "id", m.ID, "err", err)
			if err := d.Store.PeerRefused(ctx, m.Peer, m.Seq, err.Error()); err != nil && !errors.Is(err, store.ErrNotFound) {
				d.Log.Warn("peer message: recording the refusal", "peer", m.Peer, "id", m.ID, "err", err)
			}
		default:
			tries := m.Attempts + 1
			// A bead is given up sooner than mail: its group says after how
			// many tries the yardmaster is to hear that the yard is not
			// reached.
			final := tries >= d.PeerTries || m.Tries > 0 && tries >= m.Tries
			if m.Attempts == 0 || final {
				d.Log.Warn("message not sent to peer", "peer", p.Name, "kind", m.Kind, "id", m.ID, "tries", tries, "given_up", final, "err", err)
			}
			retry := d.now().Add(min(d.Backoff<<min(tries-1, 30), d.BackoffMax))
			d.peerGaveUp(ctx, m, err.Error(), retry, final)
			// The yard was not reached, which is what a bead's tries count:
			// one that waits behind this message has had this try too, or
			// mail the yard never takes would keep a bead waiting for most
			// of a day before anyone heard.
			for _, o := range msgs[i+1:] {
				if o.Tries > 0 {
					d.peerGaveUp(ctx, o, err.Error(), retry, o.Attempts+1 >= o.Tries)
				}
			}
			d.peerWait[p.Key] = d.now().Add(d.Backoff)
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
	if msg := err.Error(); d.peerErr[p.Key] != msg {
		d.peerErr[p.Key] = msg
		d.Log.Warn("peer: "+doing+" failed; trying again", "peer", p.Name, "err", err)
	}
	d.peerWait[p.Key] = d.now().Add(d.Backoff)
}
