package store

import (
	"context"
	"fmt"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// SourceMove counts what MoveSourceRecords folded from one source into
// another: hooks, identities (of crew, of users, and of the crew members of
// linked yards that senders are mapped to), crew grants, subscriptions and
// thread participation. Links move by MoveLinkSource, which the caller runs
// beside this.
type SourceMove struct {
	Hooks         int `json:"hooks"`
	Identities    int `json:"identities"`
	Grants        int `json:"grants"`
	Subscriptions int `json:"subscriptions"`
	Threads       int `json:"threads"`
}

// Moved reports whether anything was folded.
func (m *SourceMove) Moved() bool {
	return m.Hooks+m.Identities+m.Grants+m.Subscriptions+m.Threads > 0
}

// MoveSourceRecords folds a yard's records under source from into source
// to, in one write: hooks, the identities of crew and users, crew grants
// (open_threads, set_state), subscriptions (crew, users, groups) and thread
// participation. Links are MoveLinkSource's, which the caller runs
// beside this; hook events already logged stay as they are.
//
// Where both names hold the same thing it is kept once: an identity the
// two share must agree, grants merge, identical subscriptions collapse,
// and a thread followed under both keeps the participation it has under
// to. Anything else under from moves to to. With nothing under from,
// nothing is written. Moving a source's records is the operator's to do
// (operatorOnly).
func (s *Store) MoveSourceRecords(ctx context.Context, from, to, actor string) (*SourceMove, error) {
	if from == "" || to == "" {
		return nil, fmt.Errorf("move records from a source to a source, not from %q to %q", from, to)
	}
	if from == to {
		return nil, fmt.Errorf("move records from %s to itself: give two sources", from)
	}
	if err := checkName("source", to); err != nil {
		return nil, err
	}
	out := &SourceMove{}
	err := s.write(func(w *wtx) error {
		paneActor, paneErr := paneAuthor(w.Tx, callerOf(ctx), actor)
		if paneErr != nil {
			return paneErr
		}
		actor = paneActor
		if err := w.operatorOnly(ctx, actor, "move a source's records", Alone); err != nil {
			return err
		}
		*out = SourceMove{}
		var err error
		if out.Hooks, err = moveHookSource(w.Tx, from, to); err != nil {
			return err
		}
		if out.Identities, out.Grants, err = moveCrewIdentitiesAndGrants(w.Tx, from, to); err != nil {
			return err
		}
		users, err := moveUserIdentities(w.Tx, from, to)
		if err != nil {
			return err
		}
		out.Identities += users
		peers, err := movePeerIdentities(w.Tx, from, to)
		if err != nil {
			return err
		}
		out.Identities += peers
		if out.Subscriptions, err = moveSubscriptions(w.Tx, from, to); err != nil {
			return err
		}
		if out.Threads, err = moveParticipation(w.Tx, from, to); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// moveHookSource points the hooks of from at to.
func moveHookSource(tx *bolt.Tx, from, to string) (int, error) {
	var all []Hook
	if err := each(tx, bHooks, "", func(_ []byte, h *Hook) error {
		all = append(all, *h)
		return nil
	}); err != nil {
		return 0, err
	}
	n := 0
	for i := range all {
		h := &all[i]
		if h.Source != from {
			continue
		}
		h.Source = to
		if err := put(tx, bHooks, h.Name, h); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// moveCrewIdentitiesAndGrants moves the identities of from to to and
// merges the grants. It returns the identities moved and the crew
// members whose grants changed. An identity under both names must
// agree, else the whole move is refused naming the crew member.
func moveCrewIdentitiesAndGrants(tx *bolt.Tx, from, to string) (identities, grants int, err error) {
	var all []CrewMember
	if err := each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		all = append(all, *r)
		return nil
	}); err != nil {
		return 0, 0, err
	}
	for i := range all {
		r := &all[i]
		changed := false
		if id, ok := r.Identities[from]; ok {
			if have, both := r.Identities[to]; both {
				if have != id {
					return 0, 0, fmt.Errorf("crew member %s has one %s identity and another %s identity: give it one (yardr crew identity %s --source %s ...), then move again", r.Name, from, to, r.Name, to)
				}
				delete(r.Identities, from)
			} else {
				if r.Identities == nil {
					r.Identities = map[string]Identity{}
				}
				r.Identities[to] = id
				delete(r.Identities, from)
			}
			identities++
			changed = true
		}
		grantsChanged := false
		for _, setting := range []string{OpenThreads, SetState} {
			may := r.sources(setting)
			if !slices.Contains(may, from) {
				continue
			}
			keep := slices.DeleteFunc(may, func(s string) bool { return s == from })
			if !slices.Contains(keep, to) {
				keep = append(keep, to)
			}
			slices.Sort(keep)
			if r.Config == nil {
				r.Config = map[string]any{}
			}
			r.Config[setting] = strings.Join(keep, ",")
			grantsChanged = true
		}
		if grantsChanged {
			grants++
			changed = true
		}
		if !changed {
			continue
		}
		r.Revision = newRevision()
		if err := put(tx, bCrew, r.Name, r); err != nil {
			return 0, 0, err
		}
	}
	return identities, grants, nil
}

// moveUserIdentities moves the users' identities of from to to, as
// moveCrewIdentitiesAndGrants moves the crew's, and returns how many.
func moveUserIdentities(tx *bolt.Tx, from, to string) (int, error) {
	var all []User
	if err := each(tx, bUsers, "", func(_ []byte, u *User) error {
		all = append(all, *u)
		return nil
	}); err != nil {
		return 0, err
	}
	n := 0
	for i := range all {
		u := &all[i]
		id, ok := u.Identities[from]
		if !ok {
			continue
		}
		if have, both := u.Identities[to]; both && have != id {
			return 0, fmt.Errorf("user %s has one %s identity and another %s identity: give them one (yardr user identity %s --source %s ...), then move again", u.Name, from, to, u.Name, to)
		}
		u.Identities[to] = id
		delete(u.Identities, from)
		if err := put(tx, bUsers, u.Name, u); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// movePeerIdentities moves the identities the links hold of from to to, as
// moveUserIdentities moves the users', and returns how many. A crew member
// of a peer's yard that has one id under from and another under to is
// refused, naming it; the same id under both is kept once. Left behind they
// would map a sender in a source that no delivery names any more.
func movePeerIdentities(tx *bolt.Tx, from, to string) (int, error) {
	var all []Peer
	if err := each(tx, bPeers, "", func(_ []byte, p *Peer) error {
		if slices.ContainsFunc(p.Identities, func(i PeerIdentity) bool { return i.Source == from }) {
			all = append(all, *p)
		}
		return nil
	}); err != nil {
		return 0, err
	}
	n := 0
	for i := range all {
		p := &all[i]
		for j, id := range p.Identities {
			if id.Source != from {
				continue
			}
			if at := slices.IndexFunc(p.Identities, func(have PeerIdentity) bool { return have.Source == to && have.Crew == id.Crew }); at >= 0 && p.Identities[at].ID != id.ID {
				return 0, fmt.Errorf("%s@%s has one %s identity and another %s identity: give it one (yardr peer identity %s %s --source %s ...), then move again", id.Crew, p.Name, from, to, p.Name, id.Crew, to)
			}
			// User identities have already moved, and earlier peer
			// identities in this fold have been written in this transaction.
			// Refuse a destination id that would now name another speaker,
			// just as either identity setter would.
			holder, err := speakerHolding(tx, to, id.ID, p.Name, id.Crew)
			if err != nil {
				return 0, err
			}
			if holder != "" {
				return 0, fmt.Errorf("%s@%s has identity %s in %s that would clash after moving %s to %s: %s in %s would also name %s; give one speaker another identity, then move again", id.Crew, p.Name, id.ID, from, from, to, id.ID, to, holder)
			}
			p.Identities[j].Source = to
			n++
			if err := put(tx, bPeers, p.Name, p); err != nil {
				return 0, err
			}
		}
		slices.SortFunc(p.Identities, comparePeerIdentity)
		p.Identities = slices.Compact(p.Identities)
		if err := put(tx, bPeers, p.Name, p); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// moveSubscriptions points the subscriptions of from at to, keeping one
// copy of what is then identical. It returns the subscriptions that
// pointed at from, whether kept or collapsed.
func moveSubscriptions(tx *bolt.Tx, from, to string) (int, error) {
	n := 0
	// moveOne returns the subscriptions with from pointed at to and
	// duplicates dropped, and whether anything changed.
	moveOne := func(subs []Subscription) ([]Subscription, bool) {
		changed := false
		var out []Subscription
		for _, sub := range subs {
			if sub.Source == from {
				sub.Source = to
				n++
				changed = true
			}
			if slices.ContainsFunc(out, sub.same) {
				changed = true
				continue
			}
			out = append(out, sub)
		}
		if len(out) == 0 && len(subs) > 0 {
			return nil, true
		}
		return out, changed
	}
	var crews []CrewMember
	if err := each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		crews = append(crews, *r)
		return nil
	}); err != nil {
		return 0, err
	}
	for i := range crews {
		r := &crews[i]
		subs, changed := moveOne(r.Subscriptions)
		if !changed {
			continue
		}
		r.Subscriptions = subs
		r.Revision = newRevision()
		if err := put(tx, bCrew, r.Name, r); err != nil {
			return 0, err
		}
	}
	var users []User
	if err := each(tx, bUsers, "", func(_ []byte, u *User) error {
		users = append(users, *u)
		return nil
	}); err != nil {
		return 0, err
	}
	for i := range users {
		u := &users[i]
		subs, changed := moveOne(u.Subscriptions)
		if !changed {
			continue
		}
		u.Subscriptions = subs
		if err := put(tx, bUsers, u.Name, u); err != nil {
			return 0, err
		}
	}
	var groups []Group
	if err := each(tx, bGroups, "", func(_ []byte, g *Group) error {
		groups = append(groups, *g)
		return nil
	}); err != nil {
		return 0, err
	}
	for i := range groups {
		g := &groups[i]
		subs, changed := moveOne(g.Subscriptions)
		if !changed {
			continue
		}
		g.Subscriptions = subs
		if err := put(tx, bGroups, g.Name, g); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// moveParticipation re-keys the participations of from under to. A
// thread followed under both keeps the one under to.
func moveParticipation(tx *bolt.Tx, from, to string) (int, error) {
	type entry struct {
		key string
		p   Participation
	}
	var all []entry
	if err := each(tx, bThreads, "", func(k []byte, p *Participation) error {
		all = append(all, entry{string(k), *p})
		return nil
	}); err != nil {
		return 0, err
	}
	n := 0
	b := tx.Bucket(bThreads)
	for _, e := range all {
		member, source, thread, ok := splitThreadKey(e.key)
		if !ok || source != from {
			continue
		}
		n++
		target := threadKey(member, to, thread)
		if b.Get([]byte(target)) != nil {
			if err := b.Delete([]byte(e.key)); err != nil {
				return 0, err
			}
			continue
		}
		e.p.Source = to
		if err := put(tx, bThreads, target, e.p); err != nil {
			return 0, err
		}
		if err := b.Delete([]byte(e.key)); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// splitThreadKey is the crew member, source and thread of a bThreads key.
func splitThreadKey(key string) (member, source, thread string, ok bool) {
	parts := strings.SplitN(key, "\x00", 3)
	if len(parts) != 3 {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
