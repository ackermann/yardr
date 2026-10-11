package store

import (
	"context"
	"fmt"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// SourceMove counts what MoveSourceRecords folded from one source into
// another: hooks, identities, crew grants, subscriptions and thread
// participation. Links move by MoveLinkSource, which the caller runs
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
// to, in one write: hooks, crew identities, crew grants (open_threads,
// set_state), subscriptions (crew, users, groups) and thread
// participation. Links are MoveLinkSource's, which the caller runs
// beside this; hook events already logged stay as they are.
//
// Where both names hold the same thing it is kept once: an identity the
// two share must agree, grants merge, identical subscriptions collapse,
// and a thread followed under both keeps the participation it has under
// to. Anything else under from moves to to. With nothing under from,
// nothing is written. One source_migrated event logs what moved.
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
		*out = SourceMove{}
		var err error
		if out.Hooks, err = moveHookSource(w.Tx, from, to); err != nil {
			return err
		}
		if out.Identities, out.Grants, err = moveCrewIdentitiesAndGrants(w.Tx, from, to); err != nil {
			return err
		}
		if out.Subscriptions, err = moveSubscriptions(w.Tx, from, to); err != nil {
			return err
		}
		if out.Threads, err = moveParticipation(w.Tx, from, to); err != nil {
			return err
		}
		if !out.Moved() {
			return nil
		}
		return w.event("", "source_migrated", actor, map[string]any{
			"from": from, "to": to, "hooks": out.Hooks, "identities": out.Identities,
			"grants": out.Grants, "subscriptions": out.Subscriptions, "threads": out.Threads,
		})
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
