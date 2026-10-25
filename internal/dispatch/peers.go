package dispatch

import (
	"context"
	"errors"
	"fmt"
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
// The posting is a pass's. The asking is not, under Run: the peer holds a
// question open until it has something to hand over, for up to PeerHold, so
// each peer is asked from a goroutine of its own (pulling), which asks
// again as soon as it is answered and wakes the loop for what came. A pass
// run alone (Reconcile) asks once itself, and is answered at once.
//
// A message may be posted twice (the daemon died between the peer's answer
// and the store's write): the peer takes an id once.

const (
	// peerTimeout bounds one request to a peer from a pass.
	peerTimeout = 30 * time.Second
	// peerBurst bounds the messages posted to one peer in a pass.
	peerBurst = 16
	// peerAskBackoffMax is the longest a peer is left alone after
	// questions that failed: the wait doubles from Backoff up to it. Short
	// beside a post's, since nothing is given up by it: a laptop back from
	// sleep is to have what queued meanwhile within minutes.
	peerAskBackoffMax = 2 * time.Minute
)

// puller is the goroutine that asks one peer, as of the URL it was started
// for.
type puller struct {
	url  string
	stop context.CancelFunc
}

// peers posts and asks every peer that can be called.
func (d *Dispatcher) peers(ctx context.Context) error {
	// What a bead's stay at a peer came to and the store's own write could
	// not apply (its depot's repository did not read) is applied now.
	if err := d.Store.SettlePeerBeads(ctx); err != nil {
		d.Log.Warn("peers: moving on a bead whose stay at a peer ended", "err", err)
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
	d.pullers(ps)
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
		// Under Run the asking is the peer's puller's, and what it brings
		// wakes a pass to post the answer.
		if d.push(ctx, c, p) && d.pulling == nil && d.pull(ctx, c, p) > 0 {
			d.push(ctx, c, p)
		}
	}
	return nil
}

// pull asks p for what waits there and hands the answer to the store,
// which checks whose it is. It returns how many messages that brought.
func (d *Dispatcher) pull(ctx context.Context, c *yards.Caller, p store.Peer) int {
	n, err := d.ask(ctx, c, p, 0)
	if err != nil {
		d.peerFailed(p, "asking", err)
		return 0
	}
	delete(d.peerWait, p.Key)
	return n
}

// ask is one question to p, held there for up to hold, and its answer
// handed to the store. It returns how many messages that brought; none,
// and no error, for a peer removed meanwhile.
func (d *Dispatcher) ask(ctx context.Context, c *yards.Caller, p store.Peer, hold time.Duration) (int, error) {
	cctx, cancel := context.WithTimeout(ctx, hold+peerTimeout)
	batch, err := c.Pull(cctx, p, p.Pulled, hold)
	cancel()
	if err != nil {
		return 0, err
	}
	res, err := d.Store.PeerPulled(ctx, p.Name, batch)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	} else if err != nil {
		return 0, fmt.Errorf("taking what it handed over: %w", err)
	}
	if len(res.Taken) > 0 {
		d.Log.Info("messages pulled from peer", "peer", p.Name, "messages", len(res.Taken))
	}
	return len(res.Taken), nil
}

// pullers starts a puller for every peer of ps that can be called and has
// none, and stops the one of a peer that is gone or is called elsewhere
// now: its question may be held at the old URL for as long as a hold is.
// Only under Run, which is what sets pulling.
func (d *Dispatcher) pullers(ps []store.Peer) {
	if d.pulling == nil {
		return
	}
	urls := make(map[string]string, len(ps))
	for _, p := range ps {
		if p.URL != "" {
			urls[p.Key] = p.URL
		}
	}
	for key, pl := range d.pullersOf {
		if urls[key] != pl.url {
			pl.stop()
			delete(d.pullersOf, key)
		}
	}
	for key, url := range urls {
		if _, ok := d.pullersOf[key]; ok {
			continue
		}
		ctx, stop := context.WithCancel(d.pulling)
		if d.pullersOf == nil {
			d.pullersOf = map[string]puller{}
		}
		d.pullersOf[key] = puller{url: url, stop: stop}
		go d.pullFrom(ctx, key, url)
	}
}

// pullFrom asks the peer with key, at url, until ctx ends: again at once
// after an answer that was held or brought something, after PeerPoll after
// one that came at once and empty, and after a failure after a wait that
// doubles from Backoff up to peerAskBackoffMax. It keeps nothing of the
// peer between questions but that wait: each reads the peer, and the
// yard's key, again, so a question says what the yard has as of now.
func (d *Dispatcher) pullFrom(ctx context.Context, key, url string) {
	var backoff time.Duration
	said := ""
	for ctx.Err() == nil {
		name, pause, err := d.pullOnce(ctx, key, url)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			backoff = min(max(2*backoff, d.Backoff), max(peerAskBackoffMax, d.Backoff))
			pause = backoff
			if msg := err.Error(); msg != said {
				said = msg
				d.Log.Warn("peer: asking failed; trying again", "peer", name, "in", pause, "err", err)
			}
		default:
			backoff, said = 0, ""
		}
		if pause <= 0 {
			continue
		}
		t := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// pullOnce is one question of pullFrom's, and says what the peer is called
// now and how long to leave it alone after the question.
func (d *Dispatcher) pullOnce(ctx context.Context, key, url string) (name string, pause time.Duration, err error) {
	ps, err := d.Store.Peers(ctx)
	if err != nil {
		return "", 0, err
	}
	i := slices.IndexFunc(ps, func(p store.Peer) bool { return p.Key == key && p.URL == url })
	if i < 0 {
		// Gone, or called elsewhere: the next pass stops this puller.
		return "", d.PeerPoll, nil
	}
	name = ps[i].Name
	// The pass says what is wrong with the key, once (peers).
	own, err := yardkey.Load(d.PeerKey)
	if err != nil {
		return name, d.PeerPoll, nil
	}
	c := &yards.Caller{Key: own, HTTP: d.PeerHTTP, Now: d.now}
	// By the clock on the wall, whatever a test's says: it is the peer's
	// hold that is timed.
	asked := time.Now()
	n, err := d.ask(ctx, c, ps[i], d.PeerHold)
	switch {
	case errors.Is(err, yards.ErrHoldCapped):
		// The edge ended the hold itself: nothing was lost, so the peer is
		// asked again after the poll, as after an answer the yard did not
		// hold, with no failure said and no wait that doubles.
		return name, d.PeerPoll, nil
	case err != nil:
		return name, 0, err
	case n > 0:
		// What came may want an answer posted (a ping), and the store's
		// own wake for it is the server's to pass on: said here too.
		d.poke()
		return name, 0, nil
	case time.Since(asked) < d.PeerHold:
		// Empty, and sooner than the hold asked for: a yardr that holds no
		// question, one that holds its fill, or a yard that posts to this
		// one. Asked at the rhythm of before there were holds, and held
		// from the first question it does hold.
		return name, d.PeerPoll, nil
	}
	return name, 0, nil
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
		if !p.CarriesOut(m.Kind) {
			// The link stopped carrying the kind while the message
			// waited: it is dropped, not posted, and never tried
			// again. Gone with it, so what waits behind it is posted.
			dropped, err := d.Store.PeerSendDisallowed(ctx, m.Peer, m.Seq)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				d.Log.Warn("peer message: dropping one its link no longer carries", "peer", m.Peer, "id", m.ID, "err", err)
				// It is still at the head of the outbox. Keep what follows
				// behind it until a later pass can decide what to do.
				return true
			}
			if dropped {
				d.Log.Info("message dropped: the link no longer carries its kind out", "peer", m.Peer, "kind", m.Kind, "id", m.ID)
				continue
			}
			// The link may have started carrying the kind again since the
			// peer snapshot above. It remains waiting; do not let a later
			// message overtake it in this pass.
			return true
		}
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
			var over *yards.Refused
			if errors.As(err, &over) {
				err = d.Store.PeerRefusedRate(ctx, m.Peer, m.Seq, over.Limit, over.Until)
			} else {
				err = d.Store.PeerRefused(ctx, m.Peer, m.Seq, err.Error())
			}
			if err != nil && !errors.Is(err, store.ErrNotFound) {
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
