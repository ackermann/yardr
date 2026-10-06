package dispatch

import (
	"context"
	"errors"
	"time"

	"yardr.dev/yardr/internal/store"
)

// A bead that another yard worked may come back with a branch: the return
// names it, its commit and the remote it was pushed to (docs/peers.md,
// "Code that comes back"). The store keeps the outcome unapplied until the
// branch is this yard's, and taking it is the daemon's, here, as the issue
// moves are (issues.go) and for the same reasons: git runs outside the
// store's write, the daemon is what waits out an outage, and every pass
// reads what is owed from the store, so a daemon restarted carries on.

// branchTimeout bounds the taking of one branch in a pass, which holds up
// what comes after it.
const branchTimeout = 60 * time.Second

// fetches takes the branches that came back from peers and are due: each
// is fetched, checked and made its bead's own (store.TakePeerBranch), and
// the store then applies the outcome. One that fails is tried again after
// a wait that doubles from Backoff up to BackoffMax, as an issue move is,
// and after the tries of its bead's group the bead is held.
func (d *Dispatcher) fetches(ctx context.Context) {
	fs, err := d.Store.PeerFetches(ctx)
	if err != nil {
		// Once per error, not on every pass: a server of a build before
		// this one does not know the question until it is replaced.
		if msg := err.Error(); ctx.Err() == nil && msg != d.fetchErr {
			d.fetchErr = msg
			d.Log.Warn("peers: reading the branches that came back", "err", err)
		}
		return
	}
	d.fetchErr = ""
	for _, f := range fs {
		if f.RetryAt != nil && d.now().Before(*f.RetryAt) {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, branchTimeout)
		why, err := store.TakePeerBranch(cctx, f)
		cancel()
		if err == nil {
			if why == "" {
				d.Log.Info("branch of a bead taken from its peer", "bead", f.Bead, "peer", f.Peer, "branch", f.Git.Branch, "commit", f.Git.Commit, "into", f.Into)
			} else {
				d.Log.Info("branch of a bead not taken from its peer", "bead", f.Bead, "peer", f.Peer, "branch", f.Git.Branch, "reason", why)
			}
			if err := d.Store.PeerFetched(ctx, f.Bead, f.Message, why); err != nil && !errors.Is(err, store.ErrNotFound) {
				d.Log.Warn("peer_bead_fetched event", "bead", f.Bead, "err", err)
			}
			continue
		}
		tries := f.Attempts + 1
		final := store.FinalFetch(err) || tries >= f.Tries
		if f.Attempts == 0 || final {
			d.Log.Warn("branch of a bead not fetched from its peer's remote", "bead", f.Bead, "peer", f.Peer, "branch", f.Git.Branch, "remote", f.Remote, "tries", tries, "given_up", final, "err", err)
		}
		retry := d.now().Add(min(d.Backoff<<min(tries-1, 30), d.BackoffMax))
		if err := d.Store.PeerFetchFailed(ctx, f.Bead, f.Message, err.Error(), retry, final); err != nil && !errors.Is(err, store.ErrNotFound) {
			d.Log.Warn("peer fetch: recording the failure", "bead", f.Bead, "err", err)
		}
	}
}
