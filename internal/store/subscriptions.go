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
	ScopeInvolved = "involved" // mentioned or assigned
)

// Delivery reasons, recorded per resident on a hook event.
const (
	ReasonAll       = "all"
	ReasonMentioned = "mentioned"
	ReasonAssigned  = "assigned"
)

// WhereKeys are what --where may filter on: envelope fields, then source
// fields an adapter may expose. Exact matches only, ANDed.
var WhereKeys = []string{"type", "action", "team", "project", "repo", "label"}

// Subscription delivers a source's hook events to a resident's inbox.
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

// AddSubscription subscribes the resident; an identical subscription is
// kept once.
func (s *Store) AddSubscription(ctx context.Context, name string, sub Subscription, actor string) error {
	if sub.Source == "" {
		return fmt.Errorf("subscribe: source is required")
	}
	if !slices.Contains([]string{ScopeAll, ScopeMentions, ScopeInvolved}, sub.Scope) {
		return fmt.Errorf("subscribe: scope %q: use all, mentions or involved", sub.Scope)
	}
	for k, v := range sub.Where {
		if !slices.Contains(WhereKeys, k) {
			return fmt.Errorf("subscribe: cannot filter on %q: use %s", k, strings.Join(WhereKeys, ", "))
		}
		if v == "" {
			return fmt.Errorf("subscribe: empty value for %s", k)
		}
	}
	if len(sub.Where) == 0 {
		sub.Where = nil
	}
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		if slices.ContainsFunc(r.Subscriptions, sub.same) {
			return nil
		}
		r.Subscriptions = append(r.Subscriptions, sub)
		return w.event(r.Journal, "subscribed", actor, map[string]any{"resident": name, "subscription": sub.String()})
	})
}

// RemoveSubscriptions drops the resident's subscriptions to source, only
// those of scope and with exactly where when given, and reports how many.
func (s *Store) RemoveSubscriptions(ctx context.Context, name, source, scope string, where map[string]string, actor string) (int, error) {
	n := 0
	err := s.changeResident(name, func(w *wtx, r *Resident) error {
		keep := r.Subscriptions[:0]
		var gone []string
		for _, sub := range r.Subscriptions {
			if sub.Source == source && (scope == "" || sub.Scope == scope) &&
				(len(where) == 0 || maps.Equal(sub.Where, where)) {
				gone = append(gone, sub.String())
				continue
			}
			keep = append(keep, sub)
		}
		if len(gone) == 0 {
			return fmt.Errorf("resident %s has no matching %s subscription: %w", name, source, ErrNotFound)
		}
		n, r.Subscriptions = len(gone), keep
		return w.event(r.Journal, "unsubscribed", actor, map[string]any{"resident": name, "subscriptions": gone})
	})
	return n, err
}

// hookDeliveries decides, at ingest, which residents a hook event reaches
// and why: resident name -> reason. A resident never hears of its own
// actions in the source, and without an identity there it can only match
// a scope=all subscription.
func hookDeliveries(tx *bolt.Tx, data map[string]any) (map[string]any, error) {
	source, _ := data["source"].(string)
	actor, _ := data["actor"].(map[string]any)
	actorID, _ := actor["id"].(string)
	assignee, _ := data["assignee"].(string)
	mentions := strs(data["mentions"])
	out := map[string]any{}
	err := each(tx, bResidents, "", func(_ []byte, r *Resident) error {
		id, known := r.Identities[source]
		if known && id.is(actorID) {
			return nil
		}
		mentioned := known && slices.ContainsFunc(mentions, id.is)
		assigned := known && id.is(assignee)
		best := ""
		for _, sub := range r.Subscriptions {
			if sub.Source != source || !whereMatches(sub.Where, data) {
				continue
			}
			reason := ""
			switch {
			case mentioned: // every scope covers a mention
				reason = ReasonMentioned
			case assigned && sub.Scope != ScopeMentions:
				reason = ReasonAssigned
			case sub.Scope == ScopeAll:
				reason = ReasonAll
			}
			if rank(reason) > rank(best) {
				best = reason
			}
		}
		if best != "" {
			out[r.Name] = best
		}
		return nil
	})
	return out, err
}

// rank orders reasons so the most specific one is recorded when several
// subscriptions match: being mentioned says more than being assigned,
// which says more than hearing everything.
func rank(reason string) int {
	return map[string]int{ReasonAll: 1, ReasonAssigned: 2, ReasonMentioned: 3}[reason]
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

// HookReason is why a hook event reached the resident ("" if it did not).
func HookReason(e Event, resident string) string {
	d, _ := e.Data["deliveries"].(map[string]any)
	reason, _ := d[resident].(string)
	return reason
}
