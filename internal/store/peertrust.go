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
// peer allow --trust). It is a grant on the link and about mail alone. The
// two are not one identity, the name gets none of the operator's powers,
// and nothing on the socket asks it: mail runs no command. What it changes
// is one field of the mail that comes, as, which those who read the mail
// are shown ("as <user>") and told the meaning of (TrustedMail).
//
// A grant lasts while both its ends do. The user removed takes it along
// (untrust), and so does the peer once the roster it says has the name no
// more (distrust): whoever has either name next is trusted as nobody.

// PeerTrust is one grant of a link, as it is listed.
type PeerTrust struct {
	// Name is the name on the peer's roster, Peer this yard's name for the
	// link, and As the user of this yard.
	Name string `json:"name"`
	Peer string `json:"peer"`
	As   string `json:"as"`
}

// Address is whose mail the grant is about: <name>@<peer>.
func (t PeerTrust) Address() string { return t.Name + "@" + t.Peer }

func (t PeerTrust) String() string { return t.Address() + " -> " + t.As }

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

// exposes is whether the roster the peer last said has name.
func (p *Peer) exposes(name string) bool {
	return p.Said != nil && slices.ContainsFunc(p.Said.Roster, func(e PeerRosterEntry) bool { return e.Name == name })
}

// trust changes the link's grants: add is name -> user, drop the names
// trusted no more. A name to trust must be on the roster the peer last said,
// which is all this yard knows of who is there, and the user one of this
// yard: a crew member is no person to speak as. A name to drop may be any.
func (p *Peer) trust(tx *bolt.Tx, add map[string]string, drop []string) error {
	for _, name := range drop {
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
		p.Trust[name] = as
	}
	if len(p.Trust) == 0 {
		p.Trust = nil
	}
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
// no more, and keeps the names for the write that saves p (distrusted).
func (p *Peer) distrust() {
	for name := range p.Trust {
		if !p.exposes(name) {
			delete(p.Trust, name)
			p.untrusted = append(p.untrusted, name)
		}
	}
	if len(p.Trust) == 0 {
		p.Trust = nil
	}
	slices.Sort(p.untrusted)
}

// distrusted logs the grants that what the peer said took off p, as
// peer_trust_dropped: the operator gave them, and is to see that they went.
func (w *wtx) distrusted(p *Peer) error {
	if len(p.untrusted) == 0 {
		return nil
	}
	names := p.untrusted
	p.untrusted = nil
	return w.event("", "peer_trust_dropped", "peer:"+p.Name, map[string]any{"peer": p.Name, "names": names, "reason": "unexposed"})
}

// untrust takes the grants that name the user, who is removed, off every
// link: whoever has the name next is someone no peer speaks as. data, the
// event of the removal, names the addresses that were trusted as them.
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
	var gone []string
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
		if err := put(tx, bPeers, p.Name, p); err != nil {
			return err
		}
	}
	data["untrusted"] = gone
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
// this yard.
func MailFrom(e Event) string {
	from := MailSender(e)
	if as := SpeaksAs(e); as != "" {
		from += " (as " + as + ")"
	}
	return from
}
