package dispatch

import (
	"context"
	"errors"
	"slices"
	"strings"
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

// bases keeps, in every depot that is a repository, the refs for what the
// open beads from peers start from as the store says they are
// (store.KeepPeerBases): the ref of a bead that has closed is removed, and
// one that is missing is made. A depot is looked at when its beads have
// changed since it last was, and once after the daemon starts, which is
// when a ref may be left of a bead that closed while none ran.
func (d *Dispatcher) bases(ctx context.Context) {
	bases, err := d.Store.PeerBases(ctx)
	if err != nil {
		if msg := err.Error(); ctx.Err() == nil && msg != d.basesErr {
			d.basesErr = msg
			d.Log.Warn("peers: reading what the beads from peers start from", "err", err)
		}
		return
	}
	d.basesErr = ""
	depots, of := map[string]store.Depot{}, map[string][]store.PeerBase{}
	if d.basesKept == nil {
		all, err := d.Store.Depots(ctx)
		if err != nil {
			return
		}
		d.basesKept = map[string]keptBases{}
		for _, r := range all {
			depots[r.Name] = r
		}
	}
	for name, k := range d.basesKept {
		depots[name] = k.depot
	}
	for _, b := range bases {
		depots[b.Depot.Name], of[b.Depot.Name] = b.Depot, append(of[b.Depot.Name], b)
	}
	for name, depot := range depots {
		if depot.IsDir() || depot.Path == "" {
			continue
		}
		slices.SortFunc(of[name], func(a, b store.PeerBase) int { return strings.Compare(a.Bead, b.Bead) })
		var is strings.Builder
		for _, b := range of[name] {
			is.WriteString(b.Bead + "=" + b.Commit + " ")
		}
		if k, ok := d.basesKept[name]; ok && k.is == is.String() && k.depot.Path == depot.Path {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, branchTimeout)
		err := store.KeepPeerBases(cctx, depot, of[name])
		cancel()
		if err != nil {
			// Leave it uncached so the next pass retries. The depot may have
			// been unavailable temporarily, and its bases must stay rooted.
			d.Log.Warn("peers: keeping what the beads from peers start from", "depot", name, "err", err)
			continue
		}
		if len(of[name]) == 0 {
			// Nothing to keep there any more, and nothing to remember.
			delete(d.basesKept, name)
			continue
		}
		d.basesKept[name] = keptBases{depot: depot, is: is.String()}
	}
}

// keptBases is a depot whose refs for the bases of beads from peers were
// made what the store said: is, its beads and their commits then.
type keptBases struct {
	depot store.Depot
	is    string
}
