package store

import (
	"context"
	"encoding/json"
	"slices"

	bolt "go.etcd.io/bbolt"
)

// lockPeerBatch keeps what a batch reads before its write in step with the
// transaction that takes it, so that what was read is still so when it is
// written. A batch with a file takes the file lock: filing writes blobs
// before the transaction that counts them, and concurrent batches could both
// keep files the write later refuses. A batch with a bead takes its link's
// lock: taking looks at the bases of the beads that the link's hour has room
// for before the write counts them, and batches of one link that overlapped
// would each find the room the others are using. That lock is the link's
// own, since looking at a base may take its time and no other link's beads
// wait for it, and it is taken first for the same reason: a batch queued
// behind it holds nothing the files of another link need. A batch with
// neither needs no lock.
func (s *Store) lockPeerBatch(name string, msgs []PeerMessage) func() {
	has := func(kind string) bool {
		return slices.ContainsFunc(msgs, func(m PeerMessage) bool { return m.Kind == kind })
	}
	unlock := func() {}
	if has(PeerBead) {
		unlock = s.peerBeadLocks.lock(name)
	}
	if has(PeerFile) {
		s.peerFileMu.Lock()
		beads := unlock
		unlock = func() {
			s.peerFileMu.Unlock()
			beads()
		}
	}
	return unlock
}

// beadsInHour says which of the bead messages among msgs the write will
// count within the peer's hour, by their place in msgs: the first that
// PeerBeadLimit leaves room for. A message that is no message, and one taken
// before or repeated in the batch, which the write drops by its id, is not
// counted there and is not here. Of the rest the write refuses every one
// past the limit before it reads the base, so none of those has its base
// looked at (preflight). p is this read's own: the hour begins anew in it as
// the write will begin it, and nothing of it is saved.
func beadsInHour(tx *bolt.Tx, p *Peer, msgs []PeerMessage) []bool {
	p.hour(now())
	seen := tx.Bucket(bPeerSeen)
	look := make([]bool, len(msgs))
	batch := map[string]bool{}
	for i, m := range msgs {
		if p.Arrivals >= PeerBeadLimit {
			break
		}
		if m.Kind != PeerBead || m.check() != nil || seen.Get([]byte(join(p.Name, m.ID))) != nil || batch[m.ID] {
			continue
		}
		p.Arrivals++
		batch[m.ID] = true
		look[i] = true
	}
	return look
}

// preflight asks the depot of the intake each bead message goes to whether
// the work can start where the message says (checkBase), once for each
// different base, for the messages that look (from beadsInHour) marks. The
// rest are refused or dropped in the write, and no depot is asked about them.
func (tk *taking) preflight(ctx context.Context, msgs []PeerMessage, look []bool) {
	type baseKey struct {
		depot string
		g     PeerGit
	}
	checked := map[baseKey]string{}
	for i, m := range msgs {
		var b struct {
			Git *PeerGit `json:"git"`
		}
		if !look[i] || json.Unmarshal(m.Body, &b) != nil || b.Git == nil {
			continue
		}
		remote := RemoteForm(b.Git.Remote)
		if remote == "" {
			continue
		}
		depot := ""
		for _, in := range tk.intakes {
			if form := tk.forms[in.Depot]; form != "" && form == remote {
				depot = in.Depot
				break
			}
		}
		if depot == "" {
			continue
		}
		at, ok := tk.depots[depot]
		if !ok || at == nil || at.IsDir() {
			continue
		}
		g := PeerGit{Base: b.Git.Base, BaseCommit: b.Git.BaseCommit, Remote: b.Git.Remote}
		why, ok := checked[baseKey{depot, g}]
		if !ok {
			why = checkBase(ctx, *at, g)
			checked[baseKey{depot, g}] = why
		}
		tk.at[m.ID], tk.bases[m.ID] = depot, why
	}
}
