package store

import (
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	bolt "go.etcd.io/bbolt"
)

// SourceYardr is yardr's own event log as a subscription source: bead
// events, matched against subscriptions when they are logged, like a hook
// delivery when it arrives.
const SourceYardr = "yardr"

// StateDone is the one state a yardr subscription filters on: the bead is
// done. A bead is done when it closes, which entering a terminal stage of
// its flow does too, in the same transaction. Only its first close counts,
// so a done subscription hears of a bead once (a later close with another
// reason, such as the refinery's "merged <sha>" after a terminal stage, or
// a close after a reopen, is an ordinary closed event).
const StateDone = "done"

// YardrWhereKeys are what --where may filter a yardr subscription on:
//
//	bead   the bead (handle)          rig, type   the bead's
//	label  any of the bead's labels   kind        the event (created, advanced, noted, closed, ...)
//	stage  the stage entered (created, advanced)  from  the stage left (advanced)
//	group  the group owning the bead, or whose session the event ended
//	actor  who acted                  state       done (see StateDone)
//	train   the bead, or a bead it blocks or is the parent of (direct edges)
var YardrWhereKeys = []string{"bead", "rig", "type", "label", "stage", "from", "kind", "group", "actor", "train", "state"}

// deliver decides, as bead event e is logged, which crew members it reaches
// through yardr subscriptions and why, and records that on the event as
// "deliveries". The users it reaches the same way are recorded as
// "notified", and each gets a notification (see Notification): a user is
// matched as a crew member is, but has no journal to be told about. A closed
// event then ends the subscriptions watching that bead, after they heard
// of the close, and lists whose in "watches_ended".
//
// A mention is "@name" in this event alone (see eventText), never in what
// the bead said before, so a journal that once said "@B" does not reach B
// with every later entry. Journal and memory beads are left out of the
// source unless a subscription names them (bead= or type=); a mention in
// one still reaches whoever it names.
func (w *wtx) deliver(e *Event) error {
	row := new(beadRow)
	if ok, err := get(w.Tx, bBeads, *e.Bead, row); err != nil || !ok {
		return err
	}
	// Collected first: whether the bead is a crew member's journal is only
	// known once every crew member has been read.
	type cand struct {
		name string
		user bool
		subs []boundSub
	}
	var cands []cand
	private := NotWork[row.Type]
	yardr := func(subs []boundSub) bool {
		return slices.ContainsFunc(subs, func(s boundSub) bool { return s.Source == SourceYardr })
	}
	err := subscribers(w.Tx, func(r *CrewMember, subs []boundSub) error {
		private = private || r.Journal == row.ID
		if e.Actor != r.Name && !aboutItself(r, row.ID, e.Kind) && !doneAs(r, e) && yardr(subs) {
			cands = append(cands, cand{name: r.Name, subs: subs})
		}
		return nil
	})
	if err == nil {
		// Nor is a person told of what they did themselves.
		err = userSubscribers(w.Tx, func(u *User, subs []boundSub) error {
			if e.Actor != u.Name && yardr(subs) {
				cands = append(cands, cand{name: u.Name, user: true, subs: subs})
			}
			return nil
		})
	}
	if err != nil || len(cands) == 0 {
		return w.endWatchesOn(e, row, err)
	}
	text := eventText(w.Tx, e, row)
	need := map[string]bool{} // the where keys any candidate filters on
	for _, c := range cands {
		for _, sub := range c.subs {
			if sub.Source == SourceYardr {
				for k := range sub.Where {
					need[k] = true
				}
			}
		}
	}
	var fields map[string][]string // worked out for the first subscription that filters
	out, told := map[string]any{}, map[string]any{}
	for _, c := range cands {
		mentioned := text != "" && w.mentions.pattern(c.name).MatchString(text)
		participant := lazy(func() bool { return participated(w.Tx, row, c.name) })
		// A done subscription hears of a bead once, so it may look at the
		// whole bead: mentioned anywhere on it counts.
		onBead := lazy(func() bool { return mentionedOnBead(w.Tx, row, w.mentions.pattern(c.name)) })
		got := ""
		for _, sub := range c.subs {
			if sub.Source != SourceYardr {
				continue
			}
			named := sub.Where["bead"] != "" || sub.Where["type"] != ""
			if private && !named && !mentioned {
				continue
			}
			if len(sub.Where) > 0 {
				if fields == nil {
					fields = beadFields(w.Tx, e, row, need)
				}
				if !fieldsMatch(sub.Where, fields) {
					continue
				}
			}
			reason := ""
			switch {
			case sub.Where["state"] == StateDone:
				if sub.Scope == ScopeAll || mentioned || onBead() || (sub.Scope == ScopeInvolved && participant()) {
					reason = ReasonDone
				}
			case mentioned: // every scope covers a mention
				reason = ReasonMentioned
			case sub.Scope == ScopeInvolved && participant():
				reason = ReasonParticipant
			case sub.Scope == ScopeAll:
				reason = ReasonAll
			}
			if reason != "" {
				got = best(got, sub.reason(reason))
			}
		}
		switch {
		case got == "":
		case c.user:
			told[c.name] = got
		default:
			out[c.name] = got
		}
	}
	if len(out) > 0 {
		e.Data["deliveries"] = out
	}
	if len(told) > 0 {
		e.Data["notified"] = told
		if err := w.notify(beadNotification(e, row), told); err != nil {
			return err
		}
	}
	return w.endWatchesOn(e, row, nil)
}

// endWatchesOn ends, when e is a close, the watches on its bead (see
// endWatches), passing err through.
func (w *wtx) endWatchesOn(e *Event, row *beadRow, err error) error {
	if err != nil || e.Kind != "closed" {
		return err
	}
	ended, err := endWatches(w.Tx, row.ID)
	if err != nil {
		return err
	}
	if len(ended) > 0 {
		e.Data["watches_ended"] = ended
	}
	return nil
}

// aboutItself reports whether an event of kind on the bead with handle
// bead is yardr telling the crew member about itself, which never reaches it
// whoever the actor (the daemon logs starts and stops as "yard"): anything
// on its own journal (its session starting and stopping, its journal
// entries and handoffs, its subscriptions changing) but a plain note
// someone left there.
func aboutItself(r *CrewMember, bead, kind string) bool {
	return bead == r.Journal && kind != "noted"
}

// doneAs reports whether e is an issue's status set as the crew member by a
// hook (IssueMoved). Like what it does in a source itself, it never reaches
// its own inbox.
func doneAs(r *CrewMember, e *Event) bool {
	as := EventCrew(e.Data)
	return e.Kind == "thread_state" && as == r.Name
}

// eventText is what the event itself says, where a mention counts: the
// note it added, the body of a bead it created, and its own string fields
// (a close or hold reason, a title).
func eventText(tx *bolt.Tx, e *Event, row *beadRow) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(e.Data)) {
		if s, ok := e.Data[k].(string); ok {
			b.WriteString(s + "\n")
		}
	}
	if seq, ok := e.Data["note"].(uint64); ok {
		var n Note
		if ok, _ := get(tx, bNotes, join(row.UUID, seqKey(seq)), &n); ok {
			b.WriteString(n.Text + "\n")
		}
	}
	if e.Kind == "created" {
		b.WriteString(row.Body)
	}
	return b.String()
}

// beadFields are the values a yardr subscription's where keys match, each
// a set any of whose values may match. Those that read more of the store
// (state, train) are worked out only when need has them.
func beadFields(tx *bolt.Tx, e *Event, row *beadRow, need map[string]bool) map[string][]string {
	str := func(k string) string { s, _ := e.Data[k].(string); return s }
	f := map[string][]string{
		"bead": {row.ID}, "type": {row.Type}, "label": row.Labels, "kind": {e.Kind}, "actor": {e.Actor},
	}
	if row.Rig != nil {
		f["rig"] = []string{*row.Rig}
	}
	switch e.Kind {
	case "created":
		f["stage"] = []string{row.Stage}
	case "advanced":
		f["stage"], f["from"] = []string{str("to")}, []string{str("from")}
	case "closed":
		if need["state"] && firstClose(tx, row.UUID) {
			f["state"] = []string{StateDone}
		}
	}
	// Advancing and closing end the session and free the bead; the event
	// names the group that had it.
	if g := str("group"); g != "" {
		f["group"] = []string{g}
	} else if row.Group != nil {
		f["group"] = []string{*row.Group}
	}
	if !need["train"] {
		return f
	}
	f["train"] = []string{row.ID}
	for _, k := range keys(tx, bDepTo, row.UUID+"\x00") {
		var d depRow
		if ok, _ := get(tx, bDeps, k[len(row.UUID)+1:], &d); !ok || (d.Kind != "blocks" && d.Kind != "parent") {
			continue
		}
		var from beadRow
		if ok, _ := get(tx, bBeads, d.From, &from); ok {
			f["train"] = append(f["train"], from.ID)
		}
	}
	return f
}

// fieldsMatch ANDs exact matches of where against fields.
func fieldsMatch(where map[string]string, fields map[string][]string) bool {
	for k, want := range where {
		if !slices.Contains(fields[k], want) {
			return false
		}
	}
	return true
}

// firstClose reports whether the closed event being logged for the bead
// with UUID u is its first: it is not in the log yet.
func firstClose(tx *bolt.Tx, u string) bool {
	for _, k := range keys(tx, bEventsBy, u+"\x00") {
		var e Event
		if ok, _ := get(tx, bEvents, k[len(u)+1:], &e); ok && e.Kind == "closed" {
			return false
		}
	}
	return true
}

// lazy returns f's result, worked out once, when first asked: only
// subscriptions whose filters pass need it.
func lazy(f func() bool) func() bool {
	done, v := false, false
	return func() bool {
		if !done {
			done, v = true, f()
		}
		return v
	}
}

// mentionedOnBead reports whether mention is in the bead's body or a note.
func mentionedOnBead(tx *bolt.Tx, row *beadRow, mention *regexp.Regexp) bool {
	if mention.MatchString(row.Body) {
		return true
	}
	mentioned := false
	_ = each(tx, bNotes, row.UUID+"\x00", func(_ []byte, n *Note) error {
		mentioned = mentioned || mention.MatchString(n.Text)
		return nil
	})
	return mentioned
}

// participated reports whether the crew member created, advanced or noted the
// bead.
func participated(tx *bolt.Tx, row *beadRow, name string) bool {
	if row.CreatedBy != nil && *row.CreatedBy == name {
		return true
	}
	for _, k := range keys(tx, bEventsBy, row.UUID+"\x00") {
		var e Event
		if ok, _ := get(tx, bEvents, k[len(row.UUID)+1:], &e); ok && e.Actor == name &&
			(e.Kind == "created" || e.Kind == "advanced" || e.Kind == "noted") {
			return true
		}
	}
	return false
}

// mentionCache holds a compiled "@name" pattern per crew member name. Names
// are few and patterns small, so entries are never dropped.
type mentionCache struct {
	mu       sync.Mutex
	byName   map[string]*regexp.Regexp
	compiled atomic.Int64 // patterns compiled, for tests
}

// pattern matches "@name" as a whole word. A nil cache compiles each time.
func (c *mentionCache) pattern(name string) *regexp.Regexp {
	compile := func() *regexp.Regexp {
		return regexp.MustCompile(`(^|[^A-Za-z0-9_.-])@` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_-])`)
	}
	if c == nil {
		return compile()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	re, ok := c.byName[name]
	if !ok {
		if c.byName == nil {
			c.byName = map[string]*regexp.Regexp{}
		}
		re = compile()
		c.byName[name] = re
		c.compiled.Add(1)
	}
	return re
}

// endWatches drops every subscription watching the bead with handle h
// (where bead=h), from crew members, users and groups, and names whose it
// dropped.
func endWatches(tx *bolt.Tx, h string) ([]string, error) {
	watching := func(sub Subscription) bool { return sub.Source == SourceYardr && sub.Where["bead"] == h }
	// Collected first: a bucket must not change under its own cursor.
	var rs []*CrewMember
	var us []*User
	var gs []*Group
	err := each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		if slices.ContainsFunc(r.Subscriptions, watching) {
			rs = append(rs, r)
		}
		return nil
	})
	if err == nil {
		err = each(tx, bUsers, "", func(_ []byte, u *User) error {
			if slices.ContainsFunc(u.Subscriptions, watching) {
				us = append(us, u)
			}
			return nil
		})
	}
	if err == nil {
		err = each(tx, bGroups, "", func(_ []byte, g *Group) error {
			if slices.ContainsFunc(g.Subscriptions, watching) {
				gs = append(gs, g)
			}
			return nil
		})
	}
	if err != nil {
		return nil, err
	}
	var ended []string
	for _, r := range rs {
		r.Subscriptions = slices.DeleteFunc(r.Subscriptions, watching)
		if err := put(tx, bCrew, r.Name, r); err != nil {
			return nil, err
		}
		ended = append(ended, r.Name)
	}
	for _, u := range us {
		u.Subscriptions = slices.DeleteFunc(u.Subscriptions, watching)
		if err := put(tx, bUsers, u.Name, u); err != nil {
			return nil, err
		}
		ended = append(ended, u.Name)
	}
	for _, g := range gs {
		g.Subscriptions = slices.DeleteFunc(g.Subscriptions, watching)
		if err := put(tx, bGroups, g.Name, g); err != nil {
			return nil, err
		}
		ended = append(ended, g.Name)
	}
	return ended, nil
}
