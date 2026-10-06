package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// PeerWait is how long a bead is at another yard before the mayor hears of
// it (session_aged), when its group sets no max_age: a laptop that sleeps
// for a night is no news.
const PeerWait = 24 * time.Hour

// Peer hands a bead to a linked yard (docs/peers.md, "Beads, the sending
// side"): the group names the yard (peer=<name>), Start has the store
// queue the bead for it, and the session then waits for that yard's
// return, which is what ends it. Nothing runs here, so there is nothing to
// find dead and nothing to stop: a yard that does not answer leaves the
// bead waiting, and taking it back is a person's move.
type Peer struct {
	Store store.API
	// Home is the yard's home, where its key is.
	Home string
}

func (p *Peer) Start(ctx context.Context, job dispatch.Job) (map[string]any, error) {
	if p.Store == nil {
		return nil, fmt.Errorf("the peer runner has no store to queue bead %s in", job.Bead.ID)
	}
	// Without a key nothing this yard sends is signed, and no bead of the
	// group gets further than this one: the group's, not the bead's.
	if _, err := yardkey.Load((&config.Config{Home: p.Home}).PeerKey()); p.Home != "" && errors.Is(err, fs.ErrNotExist) {
		return nil, dispatch.Blocked(fmt.Errorf("this yard has no key, so it sends its peers nothing: yardr peer key makes one (%s)", filepath.Join(p.Home, "peer.key")))
	}
	sent, err := p.Store.PeerBeadSend(ctx, job.Session.ID)
	if err != nil {
		return nil, err
	}
	h := map[string]any{"peer": job.Group.AtPeer(), "bead": job.Bead.ID}
	if sent != nil {
		h["peer"], h["message"] = sent.Yard(), sent.Message
	}
	return h, nil
}

// Alive is always true: the work is another yard's, and only its return,
// or this yard giving the message up, says that it is over.
func (*Peer) Alive(context.Context, map[string]any) (bool, error) { return true, nil }

func (*Peer) Stop(context.Context, map[string]any) error { return nil }

// WaitLimit makes the session one that waits (dispatch.Waiter).
func (*Peer) WaitLimit() time.Duration { return PeerWait }
