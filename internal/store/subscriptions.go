package store

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Identity is who a crew member is in a source, so a delivery can tell when it
// is mentioned, assigned, or the one acting. A source may name people two
// ways: Linear's markdown mentions carry a profile key ("yardmaster"), its
// rich-text, assignee and delegate fields a user id; either matches a
// mention.
type Identity struct {
	ID  string `json:"id,omitempty"`
	Key string `json:"key,omitempty"`
}

func (i Identity) zero() bool { return i.ID == "" && i.Key == "" }

// is reports whether v names this identity. Keys are names people type
// (@Yardmaster is @yardmaster), so they match in any case; ids match exactly.
func (i Identity) is(v string) bool {
	return v != "" && (v == i.ID || strings.EqualFold(v, i.Key))
}

// IsIn reports whether id is who the crew member is in source: what tells
// that the actor of one of the source's events was the yard itself.
func (r CrewMember) IsIn(source, id string) bool {
	who, known := r.Identities[source]
	return known && who.is(id)
}

// botSuffix ends the login of an App's bot: the App yardr acts as
// yardr[bot].
const botSuffix = "[bot]"

// mentionedAs reports whether a mention of v is one of this identity. An
// App is mentioned by its slug: a source has no mention of an App, so
// people write @yardr, as they do for a person, and the App whose bot is
// yardr[bot] takes it as meant for it. So a key ending in [bot] is also
// mentioned by the name before it. Only a mention is read so: the actor or
// assignee yardr is the account of that name, another party than the bot.
func (i Identity) mentionedAs(v string) bool {
	if i.is(v) {
		return true
	}
	slug, bot := strings.CutSuffix(strings.ToLower(i.Key), botSuffix)
	return bot && slug != "" && strings.EqualFold(v, slug)
}

// Subscription scopes, from widest to narrowest.
const (
	ScopeAll      = "all"      // every delivery from the source
	ScopeMentions = "mentions" // deliveries that mention the crew member
	ScopeInvolved = "involved" // mentioned, assigned (or delegated), or in a thread it participates in
)

// Delivery reasons, recorded per crew member on the event delivered. A
// delivery through a group's subscription reads "<reason> via <group>".
const (
	ReasonAll         = "all"
	ReasonMentioned   = "mentioned"
	ReasonAssigned    = "assigned"
	ReasonParticipant = "participant"
	ReasonDone        = "done"     // a yardr state=done subscription: the bead is done
	ReasonAnswered    = "answered" // the ask's asker, told of its answer without subscribing
)

// WhereKeys are what --where may filter on for a hook source: envelope
// fields, then source fields its hook.keys may expose. Exact matches only,
// ANDed; a key with several values (WhereValues) matches any of them. The
// yardr source has its own (YardrWhereKeys), as the peer source has
// (PeerWhereKeys).
var WhereKeys = []string{"type", "action", "team", "project", "repo", "label"}

// extraWhereKeys are the keys a hook source adds to WhereKeys: the fields
// its adapter exposes, registered by internal/hooks at init. The store
// cannot ask the adapters itself: they import it (as with IssueForm).
var extraWhereKeys = map[string][]string{}

// AddWhereKeys registers the keys a subscription to source may filter on
// besides WhereKeys; internal/hooks calls it with each adapter's Fielder.
func AddWhereKeys(source string, keys []string) { extraWhereKeys[source] = keys }

// whereKeys are the keys a subscription to source may filter on.
func whereKeys(source string) []string {
	if source == SourceYardr {
		return YardrWhereKeys
	}
	if source == SourcePeer {
		return PeerWhereKeys
	}
	if extra, ok := extraWhereKeys[source]; ok {
		return append(slices.Clone(WhereKeys), extra...)
	}
	return WhereKeys
}

// whereKeysTx are the keys a subscription to source may filter on: whereKeys,
// with the keys of a declared source's hook beside them, those it names
// twice said once.
func whereKeysTx(tx *bolt.Tx, source string) []string {
	keys := whereKeys(source)
	var src Source
	if ok, err := get(tx, bSources, source, &src); err != nil || !ok {
		return keys
	}
	d, err := src.Declaration()
	if err != nil || d.Hook == nil {
		return keys
	}
	out := slices.Clone(keys)
	for _, k := range d.Hook.Keys {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// Subscription delivers a source's events to a crew member's inbox: hook
// events from a hook source, or bead events from the yardr source. A manual
// group's subscriptions deliver to each of its members that is a crew member.
// A user's make the events notifications in the user's inbox (see
// Notification).
//
// Where holds a key's values in one string (WhereValues), so a filter of
// one value per key, all there was until a key could take several, is
// stored as it always was.
type Subscription struct {
	Source string            `json:"source"`
	Scope  string            `json:"scope"`
	Where  map[string]string `json:"where,omitempty"`
}

// whereSep is between the values of one key of a filter. NUL cannot occur
// in an argument passed to a command, so every value the CLI could store
// before several values were supported remains one value, including line
// breaks. JSON encodes it as an escaped character in the existing string.
const whereSep = "\x00"

// WhereValues are the values a key of a filter holds as v, any of which
// matches.
func WhereValues(v string) []string { return strings.Split(v, whereSep) }

// JoinWhere is vals as one key of a filter holds them: each once, sorted,
// so the same values in another order are the same filter.
func JoinWhere(vals []string) string {
	vals = slices.Clone(vals)
	slices.Sort(vals)
	return strings.Join(slices.Compact(vals), whereSep)
}

// String is the subscription as it is typed: a key with several values is
// there once for each, as --where takes them.
func (s Subscription) String() string {
	out := s.Source + " " + s.Scope
	if len(s.Where) > 0 {
		out += " " + whereString(s.Where)
	}
	return out
}

// whereString is a filter as it is typed, the keys sorted: a key with
// several values is there once for each.
func whereString(where map[string]string) string {
	var words []string
	for _, k := range slices.Sorted(maps.Keys(where)) {
		for _, v := range WhereValues(where[k]) {
			v = strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(v)
			words = append(words, k+"="+v)
		}
	}
	return strings.Join(words, " ")
}

// ParseWhere reads a filter typed as words of key=value: a key given
// several times holds each value, any of which matches.
func ParseWhere(s string) (map[string]string, error) {
	vals := map[string][]string{}
	for _, kv := range strings.Fields(s) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("%q: want key=value", kv)
		}
		vals[k] = append(vals[k], v)
	}
	out := map[string]string{}
	for k, vs := range vals {
		out[k] = JoinWhere(vs)
	}
	return out, nil
}

func (s Subscription) same(o Subscription) bool {
	return s.Source == o.Source && s.Scope == o.Scope && maps.Equal(s.Where, o.Where)
}

// SetIdentity records who the crew member, or else the user, called name
// is in source; a zero identity removes it. The two share one space of
// names, so a name is one of them. A user's is theirs and the operator's
// to say (mayIdentify).
func (s *Store) SetIdentity(ctx context.Context, name, source string, id Identity, actor string) error {
	if source == "" {
		return fmt.Errorf("identity: source is required")
	}
	// set changes the identities of a party of that kind, and says what
	// the event is.
	set := func(ids *map[string]Identity, kind, party string) (string, map[string]any, error) {
		if id.zero() {
			if _, ok := (*ids)[source]; !ok {
				return "", nil, fmt.Errorf("%s %s has no %s identity: %w", kind, name, source, ErrNotFound)
			}
			delete(*ids, source)
			return "identity_removed", map[string]any{party: name, "source": source}, nil
		}
		if *ids == nil {
			*ids = map[string]Identity{}
		}
		(*ids)[source] = id
		return "identity_set", map[string]any{party: name, "source": source, "id": id.ID, "key": id.Key}, nil
	}
	var user bool
	if err := s.read(func(tx *bolt.Tx) error {
		user = tx.Bucket(bCrew).Get([]byte(name)) == nil && tx.Bucket(bUsers).Get([]byte(name)) != nil
		return nil
	}); err != nil {
		return err
	}
	if user {
		return s.write(func(w *wtx) error {
			u, err := getUser(w.Tx, name)
			if err != nil {
				return err
			}
			if err := w.mayIdentify(ctx, name, actor); err != nil {
				return err
			}
			kind, data, err := set(&u.Identities, "user", "user")
			if err != nil {
				return err
			}
			if err := put(w.Tx, bUsers, name, u); err != nil {
				return err
			}
			return w.event("", kind, actor, data)
		})
	}
	return s.changeCrew(name, func(w *wtx, r *CrewMember) error {
		kind, data, err := set(&r.Identities, "crew member", "crew")
		if err != nil {
			return err
		}
		return w.event(r.Journal, kind, actor, data)
	})
}

// mayIdentify refuses a change of who the user name is in a source by
// anyone but that user and the operator. A thread opened assigned to them
// goes to whoever the identity names, so whoever sets it sends what is
// meant for a person to another. Who asks is told as for mayTell: by the
// store's own records of the pane before the caller's word.
func (w *wtx) mayIdentify(ctx context.Context, name, actor string) error {
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return err
	}
	why := "who " + name + " is in a source is said by " + name
	if operator != "" && operator != name {
		why += ", or by the operator (" + operator + ")"
	}
	why += ": a thread assigned to them goes there"
	if who, err := agentPane(ctx, w.Tx); err != nil {
		return err
	} else if who != "" {
		return fmt.Errorf("%s; this is %s", why, who)
	}
	if w.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s; %s is a crew member", why, actor)
	}
	if actor != name && (operator == "" || actor != operator) {
		return fmt.Errorf("%s; this is %s", why, or(actor, "nobody"))
	}
	return nil
}

// AddSubscription subscribes the crew member, or else the user, or else the
// manual group, called name; an identical subscription is kept once. Only
// the user themselves and the operator subscribe a user (maySubscribe), or
// remove what the user subscribes to. A yardr
// subscription's bead and train are stored as handles; one for a closed
// bead is refused, as its watch would already have ended.
func (s *Store) AddSubscription(ctx context.Context, name string, sub Subscription, actor string) error {
	if err := sub.check(); err != nil {
		return err
	}
	return s.write(func(w *wtx) error {
		// Batch reruns this closure, so it binds a copy.
		sub := sub
		sub.Where = maps.Clone(sub.Where)
		return w.addSubscription(name, sub, actor, nil)
	})
}

// check refuses a subscription that no record of the yard is needed to
// refuse: one without a source or a scope, an empty value, several beads,
// and a yardr state that is not done.
func (sub Subscription) check() error {
	if sub.Source == "" {
		return fmt.Errorf("subscribe: source is required")
	}
	if !slices.Contains([]string{ScopeAll, ScopeMentions, ScopeInvolved}, sub.Scope) {
		return fmt.Errorf("subscribe: scope %q: use all, mentions or involved", sub.Scope)
	}
	for k, v := range sub.Where {
		if slices.Contains(WhereValues(v), "") {
			return fmt.Errorf("subscribe: empty value for %s", k)
		}
	}
	// A watch ends when its bead closes, and is found by its bead to be
	// ended or dropped (endWatches, Unwatch): it is of one bead.
	if n := len(WhereValues(sub.Where["bead"])); n > 1 {
		return fmt.Errorf("subscribe: %d beads in one subscription: a subscription to a bead ends when the bead closes, so give each its own", n)
	}
	// A yardr bead is only ever filtered on becoming done; a hook source's
	// state is the pull request's or review's own (open, approved, ...).
	if st, ok := sub.Where["state"]; ok && sub.Source == SourceYardr && st != StateDone {
		return fmt.Errorf("subscribe: state %q: only %s", st, StateDone)
	}
	return nil
}

// bindSubscription holds the subscription sub of name, which check has
// passed, to the yard's records, and writes into it what is stored: the keys
// it filters on are the source's, and a bead and a train are kept as
// handles. It writes nothing to the store.
func (w *wtx) bindSubscription(name string, sub *Subscription) error {
	if len(sub.Where) == 0 {
		sub.Where = nil
	}
	keys := whereKeysTx(w.Tx, sub.Source)
	for k := range sub.Where {
		if !slices.Contains(keys, k) {
			return fmt.Errorf("subscribe: cannot filter %s on %q: use %s", sub.Source, k, strings.Join(keys, ", "))
		}
	}
	var user User
	if ok, err := get(w.Tx, bUsers, name, &user); err != nil {
		return err
	} else if ok && sub.Source != SourceYardr && sub.Scope != ScopeAll {
		return fmt.Errorf("subscribe: user %s has no identity or thread participation in %s; use --scope all", name, sub.Source)
	}
	// The peer source's bead is this yard's, kept by its handle as its
	// deliveries name it.
	if ref, ok := sub.Where["bead"]; ok && sub.Source == SourcePeer {
		row, err := w.loadBead(ref)
		if err != nil {
			return fmt.Errorf("subscribe: bead: %w", err)
		}
		sub.Where["bead"] = row.ID
	}
	if sub.Source == SourceYardr {
		for _, k := range []string{"bead", "train"} {
			if refs, ok := sub.Where[k]; ok {
				ids := WhereValues(refs)
				for i, ref := range ids {
					row, err := w.loadBead(ref)
					if err != nil {
						return fmt.Errorf("subscribe: %s: %w", k, err)
					}
					// A watch ends when its bead closes; on a closed bead
					// it would end before it began.
					if k == "bead" && row.Status == "closed" {
						return fmt.Errorf("subscribe: bead %s is closed: nothing to watch", row.ID)
					}
					ids[i] = row.ID
				}
				sub.Where[k] = JoinWhere(ids)
			}
		}
	}
	return nil
}

// addSubscription is AddSubscription inside the transaction, for a
// subscription check has passed. by is the ask it was approved on, nil for
// one typed at a terminal: the event then names it (ApproveAsk).
func (w *wtx) addSubscription(name string, sub Subscription, actor string, by *approvedOn) error {
	if err := w.bindSubscription(name, &sub); err != nil {
		return err
	}
	return w.changeSubscriptions(name, actor, func(subs []Subscription) ([]Subscription, string, map[string]any, error) {
		if slices.ContainsFunc(subs, sub.same) {
			return subs, "", nil, nil
		}
		data := map[string]any{"subscription": sub.String()}
		if by != nil {
			data["approved"] = by
		}
		return append(subs, sub), "subscribed", data, nil
	})
}

// RemoveSubscriptions drops the subscriptions of the crew member (or manual
// group) name to source, only those of scope and with exactly where when
// given, and reports how many.
func (s *Store) RemoveSubscriptions(ctx context.Context, name, source, scope string, where map[string]string, actor string) (int, error) {
	return s.dropSubscriptions(name, source, actor, func(sub Subscription) bool {
		return (scope == "" || sub.Scope == scope) && (len(where) == 0 || maps.Equal(sub.Where, where))
	})
}

// Unwatch drops the yardr subscriptions of the crew member (or manual group)
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

// changeSubscriptions replaces the subscriptions of the crew member called
// name, or else of the user, or else of the manual group, with what fn
// returns, and logs fn's event (none when kind is ""): on the crew member's
// journal, or unattached for a user or a group. A user's are changed by
// the user and the operator only.
func (w *wtx) changeSubscriptions(name, actor string, fn func([]Subscription) ([]Subscription, string, map[string]any, error)) error {
	var r CrewMember
	if ok, err := get(w.Tx, bCrew, name, &r); err != nil {
		return err
	} else if ok {
		subs, kind, data, err := fn(r.Subscriptions)
		if err != nil || kind == "" {
			return err
		}
		r.Subscriptions = subs
		if err := put(w.Tx, bCrew, name, r); err != nil {
			return err
		}
		data["crew"] = name
		return w.event(r.Journal, kind, actor, data)
	}
	var u User
	if ok, err := get(w.Tx, bUsers, name, &u); err != nil {
		return err
	} else if ok {
		if err := w.maySubscribe(name, actor); err != nil {
			return err
		}
		subs, kind, data, err := fn(u.Subscriptions)
		if err != nil || kind == "" {
			return err
		}
		u.Subscriptions = subs
		if err := put(w.Tx, bUsers, name, u); err != nil {
			return err
		}
		data["user"] = name
		return w.event("", kind, actor, data)
	}
	var g Group
	if ok, err := get(w.Tx, bGroups, name, &g); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("no crew member, user or group %s: %w", name, ErrNotFound)
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

// boundSub is a subscription as it applies to one crew member: its own, or
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

// subscribers calls fn for every crew member with the subscriptions that
// apply to it: its own, then those of the groups it is a member of.
func subscribers(tx *bolt.Tx, fn func(r *CrewMember, subs []boundSub) error) error {
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
	return each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
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

// match is why an event reaches the holder of sub, "" when it does not: the
// one place a subscription is read against an event, whatever its source
// and whoever holds it. fields are the event's, flat, each a set any of
// whose values may match; sub's filter ANDs exact matches against them, and
// a key of it with several values matches when any of them does (wants).
// The other three say how the holder stands to the event, and are asked
// only when the filter passes, in that order, as far as the scope needs:
// what reads the store for an answer is then read for few subscriptions.
// The caller has checked that the event is of sub's source.
func match(sub Subscription, fields map[string][]string, mentioned, assigned, participant func() bool) string {
	for k, want := range sub.Where {
		if !wants(want, fields[k]) {
			return ""
		}
	}
	switch {
	case mentioned(): // every scope covers a mention
		return ReasonMentioned
	case sub.Scope == ScopeMentions:
		return ""
	case assigned():
		return ReasonAssigned
	case participant():
		return ReasonParticipant
	case sub.Scope == ScopeAll:
		return ReasonAll
	}
	return ""
}

// given is b as match asks for it, for what its caller already knows.
func given(b bool) func() bool { return func() bool { return b } }

// hookDeliveries decides, at ingest, whom a hook event reaches and why: the
// crew members (name -> reason) and, as deliver does for a bead event, the
// users, who are matched as a crew member is and get a notification. A crew
// member never hears of its own actions in the source. Without an identity
// there it is never mentioned or assigned, but it can still participate in
// threads it follows by hand. A user takes part in no thread there, and
// their identity in a source is not matched with its deliveries (it is
// whom a thread is assigned to, User.Identities), so only a subscription
// of scope all reaches them.
func hookDeliveries(tx *bolt.Tx, data map[string]any) (deliveries, notified map[string]any, err error) {
	source, _ := data["source"].(string)
	actor, _ := data["actor"].(map[string]any)
	actorID, _ := actor["id"].(string)
	assignee, _ := data["assignee"].(string)
	delegate, _ := data["delegate"].(string)
	mentions := strs(data["mentions"])
	if fresh, ok := data["new_mentions"]; ok {
		mentions = strs(fresh) // see freshMentions
	}
	thread, _ := data["thread"].(map[string]any)
	threadID, _ := thread["id"].(string)
	fields := hookFields(data)
	reached := func(subs []boundSub, mentioned, assigned, participant func() bool) string {
		got := ""
		for _, sub := range subs {
			if sub.Source != source {
				continue
			}
			if reason := match(sub.Subscription, fields, mentioned, assigned, participant); reason != "" {
				got = best(got, sub.reason(reason))
			}
		}
		return got
	}
	deliveries, notified = map[string]any{}, map[string]any{}
	err = subscribers(tx, func(r *CrewMember, subs []boundSub) error {
		id, known := r.Identities[source]
		if known && id.is(actorID) {
			return nil
		}
		mentioned := known && slices.ContainsFunc(mentions, id.mentionedAs)
		// An issue delegated to the crew member is its to work as much as one
		// assigned to it: in Linear an agent is the delegate, and the
		// assignee stays the person who handed it over.
		assigned := known && (id.is(assignee) || id.is(delegate))
		participant := lazy(func() bool { return participates(tx, r.Name, source, threadID) })
		if got := reached(subs, given(mentioned), given(assigned), participant); got != "" {
			deliveries[r.Name] = got
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	err = userSubscribers(tx, func(u *User, subs []boundSub) error {
		if got := reached(subs, given(false), given(false), given(false)); got != "" {
			notified[u.Name] = got
		}
		return nil
	})
	return deliveries, notified, err
}

// hookFields are a hook envelope as match reads it: the fields its
// source's adapter set, each a set of values, and the envelope's own type
// and action as two more, which a field of the same name does not replace.
func hookFields(data map[string]any) map[string][]string {
	out := map[string][]string{}
	switch f := data["fields"].(type) {
	case map[string]any:
		for k, v := range f {
			out[k] = strs(v)
		}
	case map[string][]string: // an envelope built in process
		maps.Copy(out, f)
	}
	for _, k := range []string{"type", "action"} {
		s, _ := data[k].(string)
		out[k] = []string{s}
	}
	return out
}

// rank orders reasons so the most specific one is recorded when several
// subscriptions match: being told of your own ask's answer says more than
// a bead being done (what a done subscription waits for), which says more
// than being mentioned, which says more than being assigned, which says
// more than taking part in the thread, which says more than hearing
// everything.
func rank(reason string) int {
	return map[string]int{ReasonAll: 1, ReasonParticipant: 2, ReasonAssigned: 3, ReasonMentioned: 4, ReasonDone: 5, ReasonAnswered: 6}[reason]
}

// wants reports whether a key of a filter, holding want (WhereValues), is
// met by an event with the values got for it: one of the one is among the
// other.
func wants(want string, got []string) bool {
	if !strings.Contains(want, whereSep) {
		return slices.Contains(got, want)
	}
	return slices.ContainsFunc(WhereValues(want), func(v string) bool { return slices.Contains(got, v) })
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

// Reason is why a hook or bead event reached the crew member through its
// subscriptions ("" if none delivered it).
func Reason(e Event, member string) string {
	d, _ := e.Data["deliveries"].(map[string]any)
	reason, _ := d[member].(string)
	return reason
}
