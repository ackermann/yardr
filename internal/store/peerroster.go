package store

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	bolt "go.etcd.io/bbolt"
)

// A link's roster is who of this yard the peer may see and write to
// (docs/peers.md): the crew members and users the operator named for that
// peer (Peer.Expose, yardr peer allow --expose), nobody until then. It is
// the one list: mail from the peer reaches a name on it and no other
// (checkMail), and the peer is told it with everything this yard says of
// itself (PeerSays.Roster), so its operator and crew see whom they can
// write to. Whether the link carries mail at all is the other grant
// (--receive mail), which each entry repeats.

// What a roster entry is, as it says itself.
const (
	RosterCrew = "crew"
	RosterUser = "user"
)

const (
	// MaxPeerExpose bounds the names one link exposes, and so the roster
	// kept of what a peer said: a yard that pulls says its roster in the
	// address of every question, which has to stay one a proxy passes on.
	MaxPeerExpose = 32
	// maxRosterRole bounds an entry's role line, in characters.
	maxRosterRole = 80
)

// PeerRosterEntry is one of a yard as its peer is told of them: the name
// mail is addressed to, whether it is a crew member or a user, one line of
// what they do, and whether the link takes mail for them. Nothing else of
// a crew member crosses: no agent, model, file or path.
type PeerRosterEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Role is the first line of a crew member's role file, and for a user
	// the word for what they are in the yard: operator or user.
	Role string `json:"role,omitempty"`
	// Mail is whether the link takes mail from the peer (receive mail), on
	// the side that says the roster.
	Mail bool `json:"mail"`
	// file is the crew member's role file, which is read once the
	// transaction that found it is over (readRoles).
	file string
}

// PeerParty is one of a linked yard as this yard writes to them: an entry
// of the roster its peer last said (PeerSaid.Roster), under this yard's
// name for the link.
type PeerParty struct {
	PeerRosterEntry
	Peer string `json:"peer"`
	// Sends is whether this yard's side of the link carries mail out
	// (send mail), the other grant mail to them needs.
	Sends bool `json:"sends"`
}

// Address is what mail to them is addressed to: <name>@<peer>.
func (e PeerParty) Address() string { return e.Name + "@" + e.Peer }

// PeerParties are those the peers ps expose to this yard, in the order of
// ps and by name within one. A peer that exposes nobody, or has not said,
// adds none.
func PeerParties(ps []Peer) []PeerParty {
	var out []PeerParty
	for _, p := range ps {
		if p.Said == nil {
			continue
		}
		for _, e := range p.Said.Roster {
			out = append(out, PeerParty{PeerRosterEntry: e, Peer: p.Name, Sends: slices.Contains(p.Send, PeerMail)})
		}
	}
	return out
}

// exposable refuses a name that is neither a crew member's nor a user's:
// only they have an inbox mail from a peer can go to.
func exposable(tx *bolt.Tx, name string) error {
	if tx.Bucket(bCrew).Get([]byte(name)) != nil || tx.Bucket(bUsers).Get([]byte(name)) != nil {
		return nil
	}
	return fmt.Errorf("%q is neither a crew member nor a user of this yard, and only they are exposed to a peer (a group never is)", name)
}

// rosterOf fills in what this yard says to p of those it exposes to it. A
// crew member's role line is still to be read (readRoles). One exposed and
// gone is left out.
func rosterOf(tx *bolt.Tx, p *Peer) error {
	p.Roster = nil
	if len(p.Expose) == 0 {
		return nil
	}
	mail := p.carries(PeerMail, true)
	for _, name := range p.Expose {
		var r CrewMember
		if ok, err := get(tx, bCrew, name, &r); err != nil {
			return err
		} else if ok {
			e := PeerRosterEntry{Name: name, Kind: RosterCrew, Mail: mail}
			if e.file, _ = r.Config["prompt"].(string); e.file != "" && !filepath.IsAbs(e.file) && r.Dir != "" {
				e.file = filepath.Join(r.Dir, e.file)
			}
			p.Roster = append(p.Roster, e)
			continue
		}
		var u User
		if ok, err := get(tx, bUsers, name, &u); err != nil {
			return err
		} else if ok {
			e := PeerRosterEntry{Name: name, Kind: RosterUser, Role: "user", Mail: mail}
			if u.IsOperator() {
				e.Role = RoleOperator
			}
			p.Roster = append(p.Roster, e)
		}
	}
	return nil
}

// readRoles gives each crew member of the roster its role line: the first
// line of its role file that says something, without a heading's marks. A
// file that cannot be read leaves the entry without one: the roster is
// said many times a minute, and a role is no reason to fail it.
func readRoles(roster []PeerRosterEntry) {
	for i := range roster {
		e := &roster[i]
		if e.file == "" {
			continue
		}
		e.Role, e.file = roleLine(e.file), ""
	}
}

func roleLine(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	// The line is near the top or there is none worth saying.
	sc := bufio.NewScanner(io.LimitReader(f, 4<<10))
	for sc.Scan() {
		if line := cleanRole(strings.TrimLeft(sc.Text(), "# \t")); line != "" {
			return line
		}
	}
	return ""
}

// cleanRole holds a role to one line of maxRosterRole characters that can
// be printed.
func cleanRole(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s))
	if r := []rune(s); len(r) > maxRosterRole {
		s = strings.TrimSpace(string(r[:maxRosterRole-1])) + "…"
	}
	return s
}

// cleanRoster is what is kept of the roster a peer said, which is the
// peer's text: up to MaxPeerExpose entries whose name is one mail can be
// addressed to, each name once, of a kind there is, with a role of one
// line; sorted by name. nil for none.
func cleanRoster(in []PeerRosterEntry) []PeerRosterEntry {
	var out []PeerRosterEntry
	for _, e := range in {
		if len(out) == MaxPeerExpose {
			break
		}
		if !peerMailName.MatchString(e.Name) || (e.Kind != RosterCrew && e.Kind != RosterUser) ||
			slices.ContainsFunc(out, func(o PeerRosterEntry) bool { return o.Name == e.Name }) {
			continue
		}
		out = append(out, PeerRosterEntry{Name: e.Name, Kind: e.Kind, Role: cleanRole(e.Role), Mail: e.Mail})
	}
	slices.SortFunc(out, func(a, b PeerRosterEntry) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// unexpose takes name, a crew member or user that is removed, off every
// link that exposes it: whoever has the name next is someone the operator
// has exposed to nobody. data, the event of the removal, names the peers
// it was exposed to.
func unexpose(tx *bolt.Tx, name string, data map[string]any) error {
	var had []Peer
	err := each(tx, bPeers, "", func(_ []byte, p *Peer) error {
		if slices.Contains(p.Expose, name) {
			had = append(had, *p)
		}
		return nil
	})
	if err != nil || len(had) == 0 {
		return err
	}
	var peers []string
	for _, p := range had {
		if p.Expose = slices.DeleteFunc(p.Expose, func(n string) bool { return n == name }); len(p.Expose) == 0 {
			p.Expose = nil
		}
		if err := put(tx, bPeers, p.Name, p); err != nil {
			return err
		}
		peers = append(peers, p.Name)
	}
	data["unexposed"] = peers
	return nil
}
