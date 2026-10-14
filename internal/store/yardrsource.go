package store

import (
	"fmt"
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
// reason, such as the assembly's "merged <sha>" after a terminal stage, or
// a close after a reopen, is an ordinary closed event).
const StateDone = "done"

// YardrWhereKeys are what --where may filter a yardr subscription on:
//
//	bead   the bead (handle)          depot, type   the bead's
//	label  any of the bead's labels   kind        the event (created, advanced, noted, closed, ...),
//	                                              or a class of them (KindAttention)
//	stage  the stage entered (created, advanced)  from  the stage left (advanced)
//	group  the group owning the bead, or whose session the event ended
//	actor  who acted                  state       done (see StateDone)
//	train   the bead, or a bead it blocks or is the parent of (direct edges)
var YardrWhereKeys = []string{"bead", "depot", "type", "label", "stage", "from", "kind", "group", "actor", "train", "state"}

// deliver decides, as bead event e is logged, which crew members it reaches
// through yardr subscriptions and why, and records that on the event as
// "deliveries". The users it reaches the same way are recorded as
// "notified", and each gets a notification (see Notification): a user is
// matched as a crew member is, but has no journal to be told about. A closed
// event then ends the subscriptions watching that bead, after they heard
// of the close, and lists whose in "watches_ended".
//
// An answer to an ask reaches its asker without subscribing, when the asker
// is a crew member or a user of this yard and is not the one answering, and
// reads in words wherever it lands (see answerSentence). A mention is "@name"
// in this event alone (see eventText), never in what the bead said before,
// so a journal that once said "@B" does not reach B with every later entry.
// Journal and memory beads are left out of the source unless a subscription
// names them (bead= or type=); a mention in one still reaches whoever it
// names.
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
	if err != nil {
		return w.endWatchesOn(e, row, err)
	}
	out, told := map[string]any{}, map[string]any{}
	if len(cands) > 0 {
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
				if len(sub.Where) > 0 && fields == nil {
					fields = beadFields(w.Tx, e, row, need)
				}
				// What a bead event leaves of match's questions: nobody is
				// assigned a bead here, taking part counts for scope involved
				// alone (scope all says all of a bead its holder worked on,
				// and does not look), and a done subscription, which hears of
				// a bead once, takes any reason for it as done.
				done := sub.Where["state"] == StateDone
				reason := match(sub.Subscription, fields,
					func() bool { return mentioned || (done && onBead()) },
					given(false),
					func() bool { return sub.Scope == ScopeInvolved && participant() })
				if reason != "" && done {
					reason = ReasonDone
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
	}
	// An answer reads in words wherever it lands, and reaches its asker
	// without subscribing. The sentence is stored on the event, so the inbox
	// and the notification read it without another lookup; the text for
	// mentions was already read, so it cannot match a name in it.
	if ans := answerOf(w.Tx, row, e); ans != nil {
		e.Data["answer"] = ans.line
		if ans.asker != e.Actor {
			if w.Bucket(bCrew).Get([]byte(ans.asker)) != nil {
				prev, _ := out[ans.asker].(string)
				out[ans.asker] = best(prev, ReasonAnswered)
			} else if w.Bucket(bUsers).Get([]byte(ans.asker)) != nil {
				prev, _ := told[ans.asker].(string)
				told[ans.asker] = best(prev, ReasonAnswered)
			}
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

// deliverBeadless decides, as a bead-less yard event e is logged (the
// upgrade's failure kinds, a hook gone quiet: the only bead-less kinds
// matched against subscriptions), which crew members it reaches through
// yardr subscriptions and why, and records that on the event as
// "deliveries"; the users it reaches the same way are recorded as
// "notified", and each gets a notification (see Notification). It is
// matched on kind alone: kind and actor are the event's own, and a
// subscription that filters on any other key needs a bead, which e has
// none of (beadlessWhere). Only scope all reaches without one (match,
// with no mention in, assignment of, or part in nothing).
func (w *wtx) deliverBeadless(e *Event) error {
	type cand struct {
		name string
		user bool
		subs []boundSub
	}
	var cands []cand
	yardr := func(subs []boundSub) bool {
		return slices.ContainsFunc(subs, func(s boundSub) bool { return s.Source == SourceYardr })
	}
	err := subscribers(w.Tx, func(r *CrewMember, subs []boundSub) error {
		if e.Actor != r.Name && yardr(subs) {
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
	if err != nil {
		return err
	}
	out, told := map[string]any{}, map[string]any{}
	fields := map[string][]string{"kind": kindWords(e.Kind), "actor": {e.Actor}}
	for _, c := range cands {
		got := ""
		for _, sub := range c.subs {
			if sub.Source != SourceYardr || !beadlessWhere(sub.Where) {
				continue
			}
			if reason := match(sub.Subscription, fields, given(false), given(false), given(false)); reason != "" {
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
		if err := w.notify(yardNotification(e), told); err != nil {
			return err
		}
	}
	return nil
}

// answeredAsk is an answer event read whole: who asked, and the line it
// reads as in an inbox.
type answeredAsk struct {
	asker string
	line  string
}

// answerOf reads the answer the noted event e added, with its ask and the
// pick it replaced, if any. It is nil when the event is no answer, or when
// either note does not parse: corrupt notes are yardr's own surfaces to
// show, and the event reads as a note did.
func answerOf(tx *bolt.Tx, row *beadRow, e *Event) *answeredAsk {
	if e.Kind != "noted" {
		return nil
	}
	var seq uint64
	switch n := e.Data["note"].(type) {
	case uint64:
		seq = n
	case float64:
		seq = uint64(n)
	default:
		return nil
	}
	var n Note
	if ok, err := get(tx, bNotes, join(row.UUID, seqKey(seq)), &n); err != nil || !ok || n.Kind != NoteAnswer {
		return nil
	}
	ans, err := ParseAnswer(n)
	if err != nil {
		return nil
	}
	var askNote Note
	if ok, err := get(tx, bNotes, join(row.UUID, seqKey(ans.Ask)), &askNote); err != nil || !ok || askNote.Kind != NoteAsk {
		return nil
	}
	ask, err := ParseAsk(askNote)
	if err != nil {
		return nil
	}
	prev := 0
	if ans.Option != 0 {
		notes, err := beadNotes(tx, row.UUID)
		if err != nil {
			return nil
		}
		for _, r := range ResolveAsks(notes, n.At) {
			if r.Note.Seq != ans.Ask {
				continue
			}
			for _, a := range r.Answers {
				if a.Note.Seq == n.Seq {
					break
				}
				// Only the immediately current answer is replaced. A later
				// answer puts the ask aside, so a pick after it is not a
				// change to the pick that preceded it.
				prev = a.Answer.Option
			}
		}
	}
	return &answeredAsk{asker: askNote.Author, line: answerSentence(e.Actor, row.ID, askNote.Author, ans.Ask, ask, ans, n, prev)}
}

// answerSentence is an answer as one line of an inbox: who, which bead and
// ask, the question, and the pick with its label and words; or put aside
// until when; or declined with the words; or left to the asker. A pick that
// replaces an earlier one reads as a change and names the earlier one. A
// secret that was given reads as who gave which names for which source, or
// as whose own address in it, and never holds a value: the note it is made
// from has none. An approved command reads as who approved which ask, and
// the command the yard ran, whole.
func answerSentence(actor, bead, asker string, askSeq uint64, ask Ask, ans Answer, n Note, prev int) string {
	question := fmt.Sprintf("%q", oneLine(ask.Question))
	label := func(n int) string {
		if n >= 1 && n <= len(ask.Options) {
			return ask.Options[n-1].Label
		}
		return ""
	}
	words := ""
	if w := oneLine(ans.Words); w != "" {
		words = ": " + w
	}
	switch {
	case ans.Ran != nil:
		return oneLine(fmt.Sprintf("%s approved ask %d on %s, and the yard ran: %s", actor, askSeq, bead, ans.Ran.Command()))
	case len(ans.Given) > 0 && ask.Secret != nil && ask.Secret.User != "":
		return oneLine(fmt.Sprintf("%s gave %s, %s's own address in the source %s (ask %d on %s)", actor, strings.Join(ans.Given, " and "), ask.Secret.User, ask.Secret.Source, askSeq, bead))
	case len(ans.Given) > 0 && ask.Secret != nil:
		return oneLine(fmt.Sprintf("%s gave %s for the source %s (ask %d on %s)", actor, strings.Join(ans.Given, " and "), ask.Secret.Source, askSeq, bead))
	case ans.Later:
		until := n.At.Add(LaterFor).Local().Format("2006-01-02 15:04")
		return oneLine(fmt.Sprintf("%s put aside %s ask %d %s until %s", actor, bead, askSeq, question, until))
	case ans.TakeBack:
		return oneLine(fmt.Sprintf("%s took back %s ask %d %s%s", actor, bead, askSeq, question, words))
	case ans.Decline:
		return oneLine(fmt.Sprintf("%s declined %s ask %d %s%s", actor, bead, askSeq, question, words))
	case ans.Option == 0:
		return oneLine(fmt.Sprintf("%s answered %s ask %d %s%s", actor, bead, askSeq, question, words))
	}
	pick := fmt.Sprintf("%d (%s)", ans.Option, label(ans.Option))
	if prev != 0 && label(ans.Option) == YouDecide {
		was := fmt.Sprintf("%d (%s)", prev, label(prev))
		return oneLine(fmt.Sprintf("%s changed the pick on %s ask %d %s: was %s, now left to %s%s", actor, bead, askSeq, question, was, asker, words))
	}
	if label(ans.Option) == YouDecide {
		return oneLine(fmt.Sprintf("%s left %s ask %d %s to %s%s", actor, bead, askSeq, question, asker, words))
	}
	if prev != 0 {
		was := fmt.Sprintf("%d (%s)", prev, label(prev))
		return oneLine(fmt.Sprintf("%s changed the pick on %s ask %d %s: was %s, now %s%s", actor, bead, askSeq, question, was, pick, words))
	}
	return oneLine(fmt.Sprintf("%s picked %s on %s ask %d %s%s", actor, pick, bead, askSeq, question, words))
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
	as, _ := e.Data["crew"].(string)
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
// a set any of whose values may match: an event's kind is also the class
// it is in (kindWords), as a bead has each of its labels. Those that read more of the store
// (state, train) are worked out only when need has them.
func beadFields(tx *bolt.Tx, e *Event, row *beadRow, need map[string]bool) map[string][]string {
	str := func(k string) string { s, _ := e.Data[k].(string); return s }
	f := map[string][]string{
		"bead": {row.ID}, "type": {row.Type}, "label": row.Labels, "kind": kindWords(e.Kind), "actor": {e.Actor},
	}
	if row.Depot != nil {
		f["depot"] = []string{*row.Depot}
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
