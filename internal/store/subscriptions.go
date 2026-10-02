package store

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Identity is who a resident is in a source, so a delivery can tell when it
// is mentioned, assigned, or the one acting. A source may name people two
// ways: Linear's markdown mentions carry a profile key ("mayor"), its
// rich-text and assignee fields a user id; either matches a mention.
type Identity struct {
	ID  string `json:"id,omitempty"`
	Key string `json:"key,omitempty"`
}

func (i Identity) zero() bool { return i.ID == "" && i.Key == "" }

// is reports whether v names this identity.
func (i Identity) is(v string) bool { return v != "" && (v == i.ID || v == i.Key) }

// Subscription scopes, from widest to narrowest.
const (
	ScopeAll      = "all"      // every delivery from the source
	ScopeMentions = "mentions" // deliveries that mention the resident
	ScopeInvolved = "involved" // mentioned, assigned, or in a thread it participates in
)

// Delivery reasons, recorded per resident on the event delivered. A
// delivery through a group's subscription reads "<reason> via <group>".
const (
	ReasonAll         = "all"
	ReasonMentioned   = "mentioned"
	ReasonAssigned    = "assigned"
	ReasonParticipant = "participant"
	ReasonDone        = "done" // a yardr state=done subscription: the bead is done
)

// WhereKeys are what --where may filter on for a hook source: envelope
// fields, then source fields an adapter may expose. Exact matches only,
// ANDed. The yardr source has its own (YardrWhereKeys).
var WhereKeys = []string{"type", "action", "team", "project", "repo", "label"}

// whereKeys are the keys a subscription to source may filter on.
func whereKeys(source string) []string {
	if source == SourceYardr {
		return YardrWhereKeys
	}
	return WhereKeys
}

// Subscription delivers a source's events to a resident's inbox: hook
// events from a hook source, or bead events from the yardr source. A manual
// group's subscriptions deliver to each of its members that is a resident.
type Subscription struct {
	Source string            `json:"source"`
	Scope  string            `json:"scope"`
	Where  map[string]string `json:"where,omitempty"`
}

func (s Subscription) String() string {
	out := s.Source + " " + s.Scope
	keys := slices.Sorted(maps.Keys(s.Where))
	for _, k := range keys {
		out += " " + k + "=" + s.Where[k]
	}
	return out
}

func (s Subscription) same(o Subscription) bool {
	return s.Source == o.Source && s.Scope == o.Scope && maps.Equal(s.Where, o.Where)
}

// SetIdentity records who the resident is in source; a zero identity
// removes it.
func (s *Store) SetIdentity(ctx context.Context, name, source string, id Identity, actor string) error {
	if source == "" {
		return fmt.Errorf("identity: source is required")
	}
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		if id.zero() {
			if _, ok := r.Identities[source]; !ok {
				return fmt.Errorf("resident %s has no %s identity: %w", name, source, ErrNotFound)
			}
			delete(r.Identities, source)
			return w.event(r.Journal, "identity_removed", actor, map[string]any{"resident": name, "source": source})
		}
		if r.Identities == nil {
			r.Identities = map[string]Identity{}
		}
		r.Identities[source] = id
		return w.event(r.Journal, "identity_set", actor,
			map[string]any{"resident": name, "source": source, "id": id.ID, "key": id.Key})
	})
}

// AddSubscription subscribes the resident, or else the manual group,
// called name; an identical subscription is kept once. A yardr
// subscription's bead and epic are stored as handles.
func (s *Store) AddSubscription(ctx context.Context, name string, sub Subscription, actor string) error {
	if sub.Source == "" {
		return fmt.Errorf("subscribe: source is required")
	}
	if !slices.Contains([]string{ScopeAll, ScopeMentions, ScopeInvolved}, sub.Scope) {
		return fmt.Errorf("subscribe: scope %q: use all, mentions or involved", sub.Scope)
	}
	keys := whereKeys(sub.Source)
	for k, v := range sub.Where {
		if !slices.Contains(keys, k) {
			return fmt.Errorf("subscribe: cannot filter %s on %q: use %s", sub.Source, k, strings.Join(keys, ", "))
		}
		if v == "" {
			return fmt.Errorf("subscribe: empty value for %s", k)
		}
	}
	if st, ok := sub.Where["state"]; ok && st != StateDone {
		return fmt.Errorf("subscribe: state %q: only %s", st, StateDone)
	}
	if len(sub.Where) == 0 {
		sub.Where = nil
	}
	return s.write(func(w *wtx) error {
		if sub.Source == SourceYardr {
			for _, k := range []string{"bead", "epic"} {
				if ref, ok := sub.Where[k]; ok {
					row, err := w.loadBead(ref)
					if err != nil {
						return fmt.Errorf("subscribe: %s: %w", k, err)
					}
					sub.Where[k] = row.ID
				}
			}
		}
		return w.changeSubscriptions(name, actor, func(subs []Subscription) ([]Subscription, string, map[string]any, error) {
			if slices.ContainsFunc(subs, sub.same) {
				return subs, "", nil, nil
			}
			return append(subs, sub), "subscribed", map[string]any{"subscription": sub.String()}, nil
		})
	})
}

// RemoveSubscriptions drops the subscriptions of the resident (or manual
// group) name to source, only those of scope and with exactly where when
// given, and reports how many.
func (s *Store) RemoveSubscriptions(ctx context.Context, name, source, scope string, where map[string]string, actor string) (int, error) {
	return s.dropSubscriptions(name, source, actor, func(sub Subscription) bool {
		return (scope == "" || sub.Scope == scope) && (len(where) == 0 || maps.Equal(sub.Where, where))
	})
}

// Unwatch drops the yardr subscriptions of the resident (or manual group)
// name that are for bead, and reports how many.
func (s *Store) Unwatch(ctx context.Context, name, bead, actor string) (int, error) {
	b, err := s.GetBead(ctx, bead)
	if err != nil {
		return 0, err
	}
	return s.dropSubscriptions(name, SourceYardr, actor, func(sub Subscription) bool { return sub.Where["bead"] == b.ID })
}

func (s *Store) dropSubscriptions(name, source, actor string, match func(Subscription) bool) (int, error) {
	n := 0
	err := s.write(func(w *wtx) error {
		return w.changeSubscriptions(name, actor, func(subs []Subscription) ([]Subscription, string, map[string]any, error) {
			var keep []Subscription
			var gone []string
			for _, sub := range subs {
				if sub.Source == source && match(sub) {
					gone = append(gone, sub.String())
					continue
				}
				keep = append(keep, sub)
			}
			if len(gone) == 0 {
				return nil, "", nil, fmt.Errorf("%s has no matching %s subscription: %w", name, source, ErrNotFound)
			}
			n = len(gone)
			return keep, "unsubscribed", map[string]any{"subscriptions": gone}, nil
		})
	})
	return n, err
}

// changeSubscriptions replaces the subscriptions of the resident called
// name, or else of the manual group, with what fn returns, and logs fn's
// event (none when kind is ""): on the resident's journal, or unattached
// for a group.
func (w *wtx) changeSubscriptions(name, actor string, fn func([]Subscription) ([]Subscription, string, map[string]any, error)) error {
	var r Resident
	if ok, err := get(w.Tx, bResidents, name, &r); err != nil {
		return err
	} else if ok {
		subs, kind, data, err := fn(r.Subscriptions)
		if err != nil || kind == "" {
			return err
		}
		r.Subscriptions = subs
		if err := put(w.Tx, bResidents, name, r); err != nil {
			return err
		}
		data["resident"] = name
		return w.event(r.Journal, kind, actor, data)
	}
	var g Group
	if ok, err := get(w.Tx, bGroups, name, &g); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("no resident or group %s: %w", name, ErrNotFound)
	}
	if g.Runner != "manual" {
		return fmt.Errorf("group %s is %s: only manual groups subscribe, for their members", name, g.Runner)
	}
	subs, kind, data, err := fn(g.Subscriptions)
	if err != nil || kind == "" {
		return err
	}
	g.Subscriptions = subs
	if err := put(w.Tx, bGroups, name, g); err != nil {
		return err
	}
	data["group"] = name
	return w.event("", kind, actor, data)
}

// boundSub is a subscription as it applies to one resident: its own, or
// one of a manual group it is a member of (via).
type boundSub struct {
	Subscription
	via string
}

// reason is why a delivery through this subscription happened.
func (b boundSub) reason(r string) string {
	if b.via == "" {
		return r
	}
	return r + " via " + b.via
}

// subscribers calls fn for every resident with the subscriptions that
// apply to it: its own, then those of the groups it is a member of.
func subscribers(tx *bolt.Tx, fn func(r *Resident, subs []boundSub) error) error {
	byMember := map[string][]boundSub{}
	err := each(tx, bGroups, "", func(_ []byte, g *Group) error {
		for _, m := range g.Members {
			for _, sub := range g.Subscriptions {
				byMember[m] = append(byMember[m], boundSub{sub, g.Name})
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return each(tx, bResidents, "", func(_ []byte, r *Resident) error {
		subs := make([]boundSub, 0, len(r.Subscriptions)+len(byMember[r.Name]))
		for _, sub := range r.Subscriptions {
			subs = append(subs, boundSub{Subscription: sub})
		}
		return fn(r, append(subs, byMember[r.Name]...))
	})
}

// best keeps the more specific of two recorded reasons (see rank).
func best(a, b string) string {
	base := func(r string) string { s, _, _ := strings.Cut(r, " via "); return s }
	if rank(base(b)) > rank(base(a)) {
		return b
	}
	return a
}

// hookDeliveries decides, at ingest, which residents a hook event reaches
// and why: resident name -> reason. A resident never hears of its own
// actions in the source. Without an identity there it is never mentioned
// or assigned, but it can still participate in threads it follows by hand.
func hookDeliveries(tx *bolt.Tx, data map[string]any) (map[string]any, error) {
	source, _ := data["source"].(string)
	actor, _ := data["actor"].(map[string]any)
	actorID, _ := actor["id"].(string)
	assignee, _ := data["assignee"].(string)
	mentions := strs(data["mentions"])
	thread, _ := data["thread"].(map[string]any)
	threadID, _ := thread["id"].(string)
	out := map[string]any{}
	err := subscribers(tx, func(r *Resident, subs []boundSub) error {
		id, known := r.Identities[source]
		if known && id.is(actorID) {
			return nil
		}
		mentioned := known && slices.ContainsFunc(mentions, id.is)
		assigned := known && id.is(assignee)
		participant := participates(tx, r.Name, source, threadID)
		got := ""
		for _, sub := range subs {
			if sub.Source != source || !whereMatches(sub.Where, data) {
				continue
			}
			reason := ""
			switch {
			case mentioned: // every scope covers a mention
				reason = ReasonMentioned
			case assigned && sub.Scope != ScopeMentions:
				reason = ReasonAssigned
			case participant && sub.Scope != ScopeMentions:
				reason = ReasonParticipant
			case sub.Scope == ScopeAll:
				reason = ReasonAll
			}
			if reason != "" {
				got = best(got, sub.reason(reason))
			}
		}
		if got != "" {
			out[r.Name] = got
		}
		return nil
	})
	return out, err
}

// rank orders reasons so the most specific one is recorded when several
// subscriptions match: a bead being done (what a done subscription waits
// for) says more than being mentioned, which says more than being
// assigned, which says more than taking part in the thread, which says
// more than hearing everything.
func rank(reason string) int {
	return map[string]int{ReasonAll: 1, ReasonParticipant: 2, ReasonAssigned: 3, ReasonMentioned: 4, ReasonDone: 5}[reason]
}

// whereMatches ANDs exact matches: type and action against the envelope,
// anything else against the source's fields, any of whose values may match.
func whereMatches(where map[string]string, data map[string]any) bool {
	fields := map[string]any{}
	switch f := data["fields"].(type) {
	case map[string]any:
		fields = f
	case map[string][]string: // an envelope built in process
		for k, v := range f {
			fields[k] = v
		}
	}
	for k, want := range where {
		switch k {
		case "type", "action":
			if got, _ := data[k].(string); got != want {
				return false
			}
		default:
			if !slices.Contains(strs(fields[k]), want) {
				return false
			}
		}
	}
	return true
}

// strs reads a list of strings from decoded JSON (or a []string set in
// process).
func strs(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, 0, len(l))
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Reason is why a hook or bead event reached the resident through its
// subscriptions ("" if none delivered it).
func Reason(e Event, resident string) string {
	d, _ := e.Data["deliveries"].(map[string]any)
	reason, _ := d[resident].(string)
	return reason
}
