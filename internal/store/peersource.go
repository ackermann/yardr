package store

import (
	"fmt"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// SourcePeer is the yards this one is linked with as a subscription source:
// what they say of the beads this yard has away at them (docs/peers.md,
// "The source peer"). It is yardr's own, as SourceYardr is: no file
// declares it, no hook receives it and it has no tables. A bead event this
// yard takes (saidOfBead) is one delivery of it, verified by the link's
// signature where a hook's is verified by its secret, and the pair of beads
// is a row among a bead's links (Bead.PeerLinks).
const SourcePeer = "peer"

// PeerWhereKeys are what --where may filter a peer subscription on:
//
//	peer   the yard, as this one calls it   bead    this yard's bead (handle)
//	depot  that bead's depot                label   any of that bead's labels
//	stage  the stage the other yard's bead is in, as that yard said
//	action the event: arrived, advanced, held, unheld, gave_up (PeerEvents)
var PeerWhereKeys = []string{"peer", "bead", "depot", "label", "stage", "action"}

// peerDelivered records the bead event e that p sent about the bead of
// rec's stay as one delivery of SourcePeer, through the routing every
// delivery goes through (delivered). The envelope is made here, from the
// fields the event was held to and from this yard's own bead: no text and
// no name crossed, so it has no text, actor, mentions or assignee. Its
// thread is this yard's bead, not the message, so a bead sent again is the
// same thread and whoever follows it still does.
func (w *wtx) peerDelivered(p *Peer, rec *peerSent, e PeerEventMessage) error {
	var row beadRow
	if ok, err := get(w.Tx, bBeads, rec.Bead, &row); err != nil || !ok {
		return err
	}
	fields := map[string][]string{"peer": {p.Name}, "bead": {row.ID}, "label": slices.Clone(row.Labels), "stage": {e.Stage}}
	if row.Depot != nil {
		fields["depot"] = []string{*row.Depot}
	}
	data := map[string]any{
		"source": SourcePeer,
		"type":   "bead",
		"action": e.Event,
		"thread": map[string]any{"id": row.ID, "title": row.Title, "url": peerIssue(e.Bead, p.Name)},
		"fields": fields,
		// For the line an inbox shows: the peer's handle for its bead, and
		// whether that bead is held.
		"there": e.Bead,
		"held":  e.Held,
	}
	_, err := w.delivered("peer:"+p.Name, data)
	return err
}

// PeerDeliveryLine is what an inbox says of a delivery of SourcePeer, from
// the event's data: this yard's own sentence around the fields the peer's
// event was held to (one of five words, a stage and a handle, each of plain
// characters). "" for data that is no such delivery.
func PeerDeliveryLine(data map[string]any) string {
	if s, _ := data["source"].(string); s != SourcePeer {
		return ""
	}
	fields := hookFields(data)
	first := func(k string) string {
		if len(fields[k]) == 0 {
			return ""
		}
		return fields[k][0]
	}
	bead, stage := first("bead"), first("stage")
	held, _ := data["held"].(bool)
	var what string
	switch action := first("action"); action {
	case PeerEventArrived:
		what = fmt.Sprintf("%s arrived there, in the stage %s", bead, stage)
	case PeerEventAdvanced:
		what = fmt.Sprintf("%s moved on there, to the stage %s", bead, stage)
	case PeerEventHeld:
		what, held = fmt.Sprintf("%s is held there, in the stage %s", bead, stage), false
	case PeerEventUnheld:
		what = fmt.Sprintf("%s is no longer held there, in the stage %s", bead, stage)
	case PeerEventGaveUp:
		what = fmt.Sprintf("%s was given up on there, in the stage %s", bead, stage)
	default:
		what = fmt.Sprintf("%s: %s there, in the stage %s", bead, action, stage)
	}
	if held {
		what += ", held"
	}
	if there, _ := data["there"].(string); there != "" {
		what += fmt.Sprintf(" (there %s)", there)
	}
	return fmt.Sprintf("peer %s: %s", first("peer"), what)
}

// renamePeerFilters puts the peer's new name in place of the old in every
// subscription that filters on it, a crew member's, a user's or a group's,
// and returns whose had it: the filter named the yard, and the yard is the
// same one. That is a subscription to SourcePeer with the yard in its peer=
// filter, and any subscription with one of the yard's crew in its speaker=
// filter, <crew>@<old>: the speaker names the peer too
// (PeerIdentity.Speaker), whatever source the subscription hears.
func renamePeerFilters(tx *bolt.Tx, name, to string) ([]string, error) {
	renamed := func(subs []Subscription) bool {
		changed := false
		for i, sub := range subs {
			peer, speaker := "", ""
			if sub.Source == SourcePeer {
				if vals := WhereValues(sub.Where["peer"]); slices.Contains(vals, name) {
					vals[slices.Index(vals, name)] = to
					peer = JoinWhere(vals)
				}
			}
			if vals, ok := renamedSpeakers(WhereValues(sub.Where[SpeakerKey]), name, to); ok {
				speaker = JoinWhere(vals)
			}
			if peer == "" && speaker == "" {
				continue
			}
			// Not the stored map itself, which another subscription may share.
			where := map[string]string{}
			for k, v := range sub.Where {
				where[k] = v
			}
			if peer != "" {
				where["peer"] = peer
			}
			if speaker != "" {
				where[SpeakerKey] = speaker
			}
			subs[i].Where, changed = where, true
		}
		return changed
	}
	var crew []CrewMember
	var users []User
	var groups []Group
	err := each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		if renamed(r.Subscriptions) {
			crew = append(crew, *r)
		}
		return nil
	})
	if err == nil {
		err = each(tx, bUsers, "", func(_ []byte, u *User) error {
			if renamed(u.Subscriptions) {
				users = append(users, *u)
			}
			return nil
		})
	}
	if err == nil {
		err = each(tx, bGroups, "", func(_ []byte, g *Group) error {
			if renamed(g.Subscriptions) {
				groups = append(groups, *g)
			}
			return nil
		})
	}
	var names []string
	for _, r := range crew {
		if err != nil {
			break
		}
		r.Revision = newRevision()
		names = append(names, r.Name)
		err = put(tx, bCrew, r.Name, r)
	}
	for _, u := range users {
		if err != nil {
			break
		}
		names = append(names, u.Name)
		err = put(tx, bUsers, u.Name, u)
	}
	for _, g := range groups {
		if err != nil {
			break
		}
		names = append(names, g.Name)
		err = put(tx, bGroups, g.Name, g)
	}
	return names, err
}

// renamedSpeakers is vals with the peer's new name in place of the old in
// every speaker of its crew, <crew>@<old>. A speaker with no "@" is a user
// of this yard or outside, and one at another yard names another peer, so
// both stand as they are. Neither a crew member's name nor a peer's holds
// an "@", so the part past the first is the yard it names, whole.
func renamedSpeakers(vals []string, name, to string) ([]string, bool) {
	out, changed := vals, false
	for i, v := range vals {
		crew, peer, ok := strings.Cut(v, "@")
		if !ok || peer != name {
			continue
		}
		if !changed {
			out = slices.Clone(vals)
		}
		out[i], changed = crew+"@"+to, true
	}
	return out, changed
}

// peerThread is the thread a crew member follows in source as its
// deliveries name it: of SourcePeer a bead of this yard, by its handle
// however it was given. A thread of any other source is the source's own
// id, which the yard cannot check.
func peerThread(tx *bolt.Tx, source, thread string) (string, error) {
	if source != SourcePeer {
		return thread, nil
	}
	row, err := loadBead(tx, thread)
	if err != nil {
		return "", fmt.Errorf("a thread of the source %s is a bead of this yard: %w", SourcePeer, err)
	}
	return row.ID, nil
}

// The kinds of the rows a bead has for its counterpart in another yard
// (Bead.PeerLinks), in the words yardr bead show says them: the bead is at that
// yard, was at it, or came from it. They are no kinds of a link anyone
// makes: such a row is neither tracks nor relates, since no stage of this
// yard moves a bead of another.
const (
	LinkAt    = "at"
	LinkWasAt = "was at"
	LinkFrom  = "from"
)

// peerIssue is the counterpart of a bead as a row names it: the other
// yard's handle for its bead, at the yard as this one calls it.
func peerIssue(handle, peer string) string { return handle + "@" + peer }

// PeerIssue reports whether issue is in the form a row for a bead of
// another yard has, <handle>@<peer>, which no URL of an issue is.
func PeerIssue(issue string) bool {
	handle, peer, ok := strings.Cut(issue, "@")
	return ok && validID.MatchString(handle) && validName.MatchString(peer)
}

// PeerRowRefusal is why nothing adds, removes or moves the row for the bead
// of another yard that issue names: the record is the yard's own, and
// nothing anyone can edit of a bead changes whose it is (bPeerBeads).
func PeerRowRefusal(issue string) error {
	return fmt.Errorf("%s is a bead of another yard: its row is read from this yard's record of the pair and is no link anyone made, so nothing adds, removes or moves it (yardr link list shows it)", issue)
}

// PeerLinks are the rows a bead has for its counterpart in another yard:
// one for a bead that came from a peer, one for a bead that is or was at
// one, both for a bead sent on. They are read off the bead's records of the
// pair (BeadPeer) each time and never stored, so they name the peer as it
// is called today, Links has none of them and neither has BDP, which
// exports stored links.
func (b *Bead) PeerLinks() []Link {
	var out []Link
	for p := b.Peer; p != nil; p = p.Onward {
		l := Link{Bead: b.ID, Source: SourcePeer, Peer: or(p.Peer, p.Was), CreatedAt: p.At}
		handle := p.Bead
		switch {
		case p.Unsent && p.Stayed == nil:
			// Not sent, and never there before: no counterpart to name.
			continue
		case p.Unsent:
			// Not sent this time: the row is of the stay it last had.
			l.Kind, l.CreatedAt, l.Until, handle = LinkWasAt, p.Stayed.At, &p.Stayed.Until, p.Stayed.Bead
		case p.Direction != "out":
			l.Kind, l.Stage = LinkFrom, p.Stage
		case p.ReturnedAt != nil:
			l.Kind, l.Until = LinkWasAt, p.ReturnedAt
		default:
			l.Kind = LinkAt
		}
		// Until a bead event or the return has named the other yard's
		// handle, the row names the yard alone.
		if handle != "" {
			l.URL = peerIssue(handle, l.Peer)
		}
		out = append(out, l)
	}
	return out
}

// peerIssueLinks are the rows of this yard's beads whose counterpart in
// another yard is issue (PeerIssue), the bead that is there now first.
func peerIssueLinks(tx *bolt.Tx, issue string) ([]Link, error) {
	handle, _, _ := strings.Cut(issue, "@")
	var beads []string
	err := each(tx, bPeerSent, "", func(_ []byte, rec *peerSent) error {
		if rec.Theirs == handle || rec.Stayed != nil && rec.Stayed.Bead == handle {
			beads = append(beads, rec.Bead)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = each(tx, bPeerBeads, "", func(_ []byte, rec *peerBead) error {
		if rec.Theirs == handle && !slices.Contains(beads, rec.Bead) {
			beads = append(beads, rec.Bead)
		}
		return nil
	})
	out := []Link{}
	for _, uuid := range beads {
		if err != nil {
			break
		}
		var row beadRow
		if ok, gerr := get(tx, bBeads, uuid, &row); gerr != nil || !ok {
			err = gerr
			continue
		}
		if err = withPeer(tx, &row.Bead); err != nil {
			break
		}
		for _, l := range row.PeerLinks() {
			if l.URL == issue {
				out = append(out, l)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b Link) int {
		if x, y := a.Kind == LinkAt, b.Kind == LinkAt; x != y {
			if x {
				return -1
			}
			return 1
		}
		return a.CreatedAt.Compare(b.CreatedAt)
	})
	return out, err
}

// PeerRow is a row for a bead's counterpart in another yard as link list
// and bead show print it: where a link has its issue, the other yard's bead
// at that yard, and since when, or for a bead that came, which stage of
// that yard it is worked for.
func (l Link) PeerRow() string {
	what := fmt.Sprintf("%s (%s)", l.URL, l.Source)
	if l.URL == "" {
		what = l.Peer + " (its bead not said yet)"
	}
	var when string
	switch {
	case l.Kind == LinkFrom:
		when = "for its stage " + l.Stage
	case l.Until != nil:
		when = fmt.Sprintf("since %s, until %s", stamp(l.CreatedAt), stamp(*l.Until))
	default:
		when = "since " + stamp(l.CreatedAt)
	}
	return fmt.Sprintf("%-7s %s   %s", l.Kind, what, when)
}
