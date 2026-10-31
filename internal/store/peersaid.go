package store

import (
	"slices"
	"time"
)

// PeerSays is what a yard states of its build with every signed request
// and every answer to one: the kinds it understands (PeerKinds, there) and
// its yardr. Two yards are not upgraded on the same day, and a yard is
// upgraded long after it was linked, so it is said every time and not at
// pairing. A build from before it was said ignores the field and says
// nothing.
//
// Roster is who of the yard the peer it speaks to may see and write to
// (Peer.Expose, peerroster.go): said with the rest, so the other yard has
// it as it is now, and one left out of it is gone there with the next
// exchange.
type PeerSays struct {
	Understands []string          `json:"understands"`
	Yardr       string            `json:"yardr,omitempty"`
	Roster      []PeerRosterEntry `json:"roster,omitempty"`
}

// Says is what this build states of itself to a peer; yardr names the
// build, "" when it has no name (a test binary).
func Says(yardr string) *PeerSays {
	return &PeerSays{Understands: slices.Clone(PeerKinds), Yardr: yardr}
}

// PeerSaid is what a peer last said, and when this yard heard it.
type PeerSaid struct {
	PeerSays
	At time.Time `json:"at"`
}

// Knows reports whether the peer said its build understands kind; a nil
// PeerSaid, a peer that has not said, knows of none.
func (s *PeerSaid) Knows(kind string) bool {
	return s != nil && slices.Contains(s.Understands, kind)
}

const (
	// maxPeerSaidKinds bounds the kinds kept of what a peer said.
	maxPeerSaidKinds = 32
	// peerSaidEvery is how old the time kept with what a peer said may
	// grow while it says the same: a peer says it with every question, and a
	// write for each would wake the daemon each time.
	peerSaidEvery = time.Hour
	// peerHeardEvery is how old the time a peer was last heard from may
	// grow while it keeps being heard, for the same reason.
	peerHeardEvery = time.Minute
)

// clean is what is kept of what a peer said, which is the peer's text: the
// kinds that are a kind's few plain characters, each once and sorted, and a
// build of a version's characters, and the roster held to what one is
// (cleanRoster). nil when it named no kinds at all, as a yardr from before
// this was said does not.
func (s *PeerSays) clean() *PeerSays {
	if s == nil || s.Understands == nil {
		return nil
	}
	out := &PeerSays{Understands: []string{}}
	for _, k := range s.Understands {
		if len(out.Understands) < maxPeerSaidKinds && peerKind.MatchString(k) && !slices.Contains(out.Understands, k) {
			out.Understands = append(out.Understands, k)
		}
	}
	slices.Sort(out.Understands)
	if peerBuild.MatchString(s.Yardr) {
		out.Yardr = s.Yardr
	}
	out.Roster = cleanRoster(s.Roster)
	return out
}

// heard keeps on p that its yard was heard from, with a request or an
// answer signed with its key, and what it said then (said). It reports
// whether p changed and is to be saved: the time it was heard at is kept
// to peerHeardEvery.
func (p *Peer) heard(says *PeerSays) bool {
	changed := p.said(says)
	if p.HeardAt == nil || !now().Before(p.HeardAt.Add(peerHeardEvery)) {
		at := now()
		p.HeardAt, changed = &at, true
	}
	return changed
}

// said keeps on p what its yard said of its build, and reports whether p
// changed. One that
// said nothing is an older yardr (again, when it had said before): nothing
// is kept of it. What it says again unchanged is not saved again until the
// time kept with it is peerSaidEvery old.
func (p *Peer) said(says *PeerSays) bool {
	says = says.clean()
	switch {
	case says == nil:
		if p.Said == nil {
			return false
		}
		p.Said = nil
		p.distrust()
		return true
	case p.Said != nil && p.Said.Yardr == says.Yardr && slices.Equal(p.Said.Understands, says.Understands) &&
		slices.Equal(p.Said.Roster, says.Roster) && now().Before(p.Said.At.Add(peerSaidEvery)):
		return false
	}
	p.Said = &PeerSaid{PeerSays: *says, At: now()}
	p.distrust()
	return true
}
