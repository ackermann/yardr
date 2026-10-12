package store

import (
	"fmt"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// A link can trust a name of the peer's yard as a user of this one
// (docs/peers.md, "Trusting a name as a person of this yard"): the operator
// says that mail from <name>@<peer> is that user speaking (Peer.Trust, yardr
// peer allow --trust). It is a grant on the link and about whose word what
// comes is, mail and answers both. The two are not one identity, the name
// gets none of the operator's powers, and nothing on the socket asks it:
// mail runs no command, and an answer is taken trusted or not. What it
// changes is one field of the mail that comes, as, which those who read
// the mail are shown ("as <user>") and told the meaning of (TrustedMail),
// and the mark an answer from that name carries in numbered text.
//
// A grant lasts while both its ends do. The user removed takes it along
// (untrust), and so does the peer once the roster it says has the name no
// more (distrust): whoever has either name next is trusted as nobody.

// A grant that ends is kept as what it was (Peer.Spoke): the name was how
// the user spoke until then, so the mail of that time is the user's still,
// in their conversations (history). Mail that came says whom it spoke as
// itself (as); mail this yard wrote to the name says nothing of the kind,
// and is the user's by the grant, the one that stands or one that ended
// after it was written.

// Reach is a second grant on the same link, beside trust and never without
// it (Peer.Reach, yardr peer allow --reach): the operator says that what is
// asked of the user is also sent to that name of the peer's yard, and that
// the name's answer to it is taken. Trust is about what comes in; reach
// adds what goes out, the text of every ask for the user with what the
// message carries of its bead, and widens what the name may answer. It is
// given only for a name the link trusts as that user, since an answer from
// any other would be nobody's word, and only for one the roster says is a
// person, since a crew member has no decisions queue. It goes with the
// trust it stands on, and when the roster has no person of the name
// (unreach).

// PeerTrust is one grant of a link, as it is listed: of trust, or of
// reach.
type PeerTrust struct {
	// Name is the name on the peer's roster, Peer this yard's name for the
	// link, and As the user of this yard.
	Name string `json:"name"`
	Peer string `json:"peer"`
	As   string `json:"as"`
}

// PeerSpoke is a grant of trust that ended: the link trusted Name as the
// user As through the event Until, the last logged before it ended.
type PeerSpoke struct {
	Name  string `json:"name"`
	As    string `json:"as"`
	Until uint64 `json:"until"`
}

// spoke keeps the grant for name, which ends, as one that was: until is
// the last event logged, 0 where the caller has no log at hand and
// distrusted numbers it.
func (p *Peer) spoke(name string, until uint64) {
	if as := p.Trust[name]; as != "" {
		p.Spoke = append(p.Spoke, PeerSpoke{Name: name, As: as, Until: until})
	}
}

// spokeAs is the user of this yard that name on the link was trusted as
// when the event seq was logged, as far as the link knows: by the grant
// that ended first after it, else by the one that stands. A grant says
// nothing of when it began, so mail written to a name before it was
// trusted counts with the rest.
func (p *Peer) spokeAs(name string, seq uint64) string {
	for _, s := range p.Spoke {
		if s.Name == name && seq <= s.Until {
			return s.As
		}
	}
	return p.Trust[name]
}

// Address is whose mail the grant is about: <name>@<peer>.
func (t PeerTrust) Address() string { return t.Name + "@" + t.Peer }

func (t PeerTrust) String() string { return t.Address() + " -> " + t.As }

// TrustedAs is whom the links trust as a user of this yard, by the
// address mail and answers name them with: <name>@<peer> to the user.
// Whoever reads such words from another yard marks them with it.
func TrustedAs(peers []Peer) map[string]string {
	out := map[string]string{}
	for _, p := range peers {
		for _, t := range p.Trusted() {
			out[t.Address()] = t.As
		}
	}
	return out
}

// Trusted are the link's grants, by name; none for a link that trusts
// nobody.
func (p Peer) Trusted() []PeerTrust {
	out := []PeerTrust{}
	for name, as := range p.Trust {
		out = append(out, PeerTrust{Name: name, Peer: p.Name, As: as})
	}
	slices.SortFunc(out, func(a, b PeerTrust) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Reaching are the link's grants of reach, by name; none for a link that
// sends nobody's asks on.
func (p Peer) Reaching() []PeerTrust {
	out := []PeerTrust{}
	for name, as := range p.Reach {
		out = append(out, PeerTrust{Name: name, Peer: p.Name, As: as})
	}
	slices.SortFunc(out, func(a, b PeerTrust) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// AskReach are the names an ask for user, written by asker, is also sent
// to: each name a link gives reach for the user, over a link that carries
// ask out, by link and name. None when the asker's name is none another
// yard is told (peerMailName): the ask stands in this yard alone. AddAsk
// queues by it, and the command says by it where the ask went.
func AskReach(peers []Peer, user, asker string) []PeerTrust {
	var out []PeerTrust
	if !peerMailName.MatchString(asker) {
		return nil
	}
	for _, p := range peers {
		if !p.carries(PeerAsk, false) {
			continue
		}
		for _, t := range p.Reaching() {
			if t.As == user && p.reaches(t.Name) == user {
				out = append(out, t)
			}
		}
	}
	slices.SortFunc(out, func(a, b PeerTrust) int { return strings.Compare(a.Address(), b.Address()) })
	return out
}

// person is whether the roster the peer last said has name as a person.
func (p *Peer) person(name string) bool {
	return p.Said != nil && slices.ContainsFunc(p.Said.Roster, func(e PeerRosterEntry) bool { return e.Name == name && e.Kind == RosterUser })
}

// reaches is the user of this yard whose asks are sent to name on the
// link, and whose answer name's is taken as: "" unless the link gives the
// name reach, trusts it as the same user and has it on the roster as a
// person, all three as they stand now.
func (p *Peer) reaches(name string) string {
	as := p.Reach[name]
	if as == "" || p.Trust[name] != as || !p.person(name) {
		return ""
	}
	return as
}

// reach changes the link's grants of reach: add is name -> user, drop the
// names reached no more. A name is given reach only as the user the link
// already trusts it as, and only when the roster says it is a person. A
// name to drop may be any.
func (p *Peer) reach(add map[string]string, drop []string) error {
	for _, name := range drop {
		delete(p.Reach, name)
	}
	for name, as := range add {
		switch {
		case p.Trust[name] != as:
			return fmt.Errorf("peer %s: the link does not trust %s@%s as %s, and reach is given only to a name whose word is that user's already (yardr peer allow %s --trust %s@%s=%s first)",
				p.Name, name, p.Name, as, p.Name, name, p.Name, as)
		case !p.person(name):
			return fmt.Errorf("peer %s: %s is no person on the roster it last said, and only a person has decisions to be asked (yardr peer show %s lists whom it exposes)", p.Name, name, p.Name)
		}
		if p.Reach == nil {
			p.Reach = map[string]string{}
		}
		p.Reach[name] = as
	}
	if len(p.Reach) == 0 {
		p.Reach = nil
	}
	return nil
}

// unreach drops the grants of reach that stand on nothing any more, and
// answers their names: the link trusts the name as that user no longer,
// or the roster the peer last said has no person of the name.
func (p *Peer) unreach() []string {
	var gone []string
	for name := range p.Reach {
		if p.reaches(name) == "" {
			delete(p.Reach, name)
			gone = append(gone, name)
		}
	}
	if len(p.Reach) == 0 {
		p.Reach = nil
	}
	slices.Sort(gone)
	return gone
}

// exposes is whether the roster the peer last said has name.
func (p *Peer) exposes(name string) bool {
	return p.Said != nil && slices.ContainsFunc(p.Said.Roster, func(e PeerRosterEntry) bool { return e.Name == name })
}

// trust changes the link's grants: add is name -> user, drop the names
// trusted no more. A name to trust must be on the roster the peer last said,
// which is all this yard knows of who is there, and the user one of this
// yard: a crew member is no person to speak as. A name to drop may be any.
func (p *Peer) trust(tx *bolt.Tx, add map[string]string, drop []string) error {
	until := tx.Bucket(bEvents).Sequence()
	for _, name := range drop {
		p.spoke(name, until)
		delete(p.Trust, name)
	}
	for name, as := range add {
		switch {
		case !p.exposes(name):
			return fmt.Errorf("peer %s: it exposes nobody called %q to this yard, and only a name on its roster is trusted (yardr peer show %s lists whom it exposes)", p.Name, name, p.Name)
		case tx.Bucket(bUsers).Get([]byte(as)) == nil:
			return fmt.Errorf("peer %s: %q is no user of this yard, and %s@%s is trusted as one of them (yardr user list)", p.Name, as, name, p.Name)
		}
		if p.Trust == nil {
			p.Trust = map[string]string{}
		}
		if p.Trust[name] != as {
			p.spoke(name, until)
		}
		p.Trust[name] = as
	}
	if len(p.Trust) == 0 {
		p.Trust = nil
	}
	// Reach stands on trust: what is trusted no more, or as another user,
	// is reached no more.
	p.unreach()
	return nil
}

// speaksAs is the user of this yard that mail from name on the link speaks
// as: "" unless the link trusts the name, as a user there still is.
func (p *Peer) speaksAs(tx *bolt.Tx, name string) string {
	as := p.Trust[name]
	if as == "" || tx.Bucket(bUsers).Get([]byte(as)) == nil {
		return ""
	}
	return as
}

// distrust drops the grants whose name the roster the peer last said has
// no more, and with them or without the grants of reach whose name it has
// as a person no more, and keeps the names for the write that saves p
// (distrusted).
func (p *Peer) distrust() {
	for name := range p.Trust {
		if !p.exposes(name) {
			p.spoke(name, 0)
			delete(p.Trust, name)
			p.untrusted = append(p.untrusted, name)
		}
	}
	if len(p.Trust) == 0 {
		p.Trust = nil
	}
	slices.Sort(p.untrusted)
	p.unreached = append(p.unreached, p.unreach()...)
}

// distrusted logs the grants that what the peer said took off p, as
// peer_trust_dropped and peer_reach_dropped: the operator gave them, and
// is to see that they went. The grants of trust among them are kept as
// what they were (spoke), through the last event logged before this.
func (w *wtx) distrusted(p *Peer) error {
	for i := range p.Spoke {
		if p.Spoke[i].Until == 0 {
			p.Spoke[i].Until = w.Bucket(bEvents).Sequence()
		}
	}
	for kind, names := range map[string]*[]string{"peer_trust_dropped": &p.untrusted, "peer_reach_dropped": &p.unreached} {
		if len(*names) == 0 {
			continue
		}
		gone := *names
		*names = nil
		if err := w.event("", kind, "peer:"+p.Name, map[string]any{"peer": p.Name, "names": gone, "reason": "unexposed"}); err != nil {
			return err
		}
	}
	return nil
}

// untrust takes the grants that name the user, who is removed, off every
// link, those of reach with those of trust: whoever has the name next is
// someone no peer speaks as, and whose asks go nowhere. data, the event of
// the removal, names the addresses that were trusted as them, and those
// their asks were sent to.
func untrust(tx *bolt.Tx, user string, data map[string]any) error {
	var had []Peer
	err := each(tx, bPeers, "", func(_ []byte, p *Peer) error {
		for _, as := range p.Trust {
			if as == user {
				had = append(had, *p)
				break
			}
		}
		return nil
	})
	if err != nil || len(had) == 0 {
		return err
	}
	var gone, unreached []string
	for _, p := range had {
		for _, t := range p.Trusted() {
			if t.As == user {
				delete(p.Trust, t.Name)
				gone = append(gone, t.Address())
			}
		}
		if len(p.Trust) == 0 {
			p.Trust = nil
		}
		for _, name := range p.unreach() {
			unreached = append(unreached, name+"@"+p.Name)
		}
		if err := put(tx, bPeers, p.Name, p); err != nil {
			return err
		}
	}
	data["untrusted"] = gone
	if len(unreached) > 0 {
		data["unreached"] = unreached
	}
	return nil
}

// SpeaksAs is the user of this yard the mail e speaks as: the one its
// sender's name was trusted as on the link it came over, when it came. ""
// for any other mail, and for mail of a yard that is linked no more, whose
// grants went with the link.
func SpeaksAs(e Event) string {
	if !FromOutside(e) || unlinked(e) {
		return ""
	}
	as, _ := e.Data["as"].(string)
	return as
}

// MailFrom is the sender of the mail e as an inbox line names it:
// MailSender, with "(as <user>)" behind a name that speaks as a user of
// this yard and "(via <device>)" behind a user who wrote through a device.
func MailFrom(e Event) string {
	from := MailSender(e)
	if as := SpeaksAs(e); as != "" {
		from += " (as " + as + ")"
	}
	return ViaMark(from, Via(e))
}

// MarkedAuthor is who the author reads as in numbered text: a name of
// this yard unchanged, a name of another yard as mail is marked. Where
// the operator trusts the address as a user of this yard (as, from
// TrustedAs) it carries "(as <user>)"; otherwise it carries the foreign
// line. Answers are taken trusted or not; the mark says whose word one
// is.
func MarkedAuthor(author string, as map[string]string) string {
	if _, _, foreign := strings.Cut(author, "@"); !foreign {
		return author
	}
	if user := as[author]; user != "" {
		return author + " (as " + user + ")"
	}
	return author + " (" + OutsideMail + ")"
}
