package store

import (
	"regexp"
	"slices"

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
//	epic   the bead, or a bead it blocks or is the parent of (direct edges)
var YardrWhereKeys = []string{"bead", "rig", "type", "label", "stage", "from", "kind", "group", "actor", "epic", "state"}

// deliver decides, as bead event e is logged, which residents it reaches
// through yardr subscriptions and why, and records that on the event as
// "deliveries". A closed event then ends the subscriptions watching that
// bead, after they heard of the close, and lists whose in "watches_ended".
func (w *wtx) deliver(e *Event) error {
	row := new(beadRow)
	if ok, err := get(w.Tx, bBeads, *e.Bead, row); err != nil || !ok {
		return err
	}
	var fields map[string][]string // worked out for the first yardr subscription
	out := map[string]any{}
	err := subscribers(w.Tx, func(r *Resident, subs []boundSub) error {
		if e.Actor == r.Name { // never its own actions
			return nil
		}
		inv := lazyInvolvement(w.Tx, row, r.Name)
		got := ""
		for _, sub := range subs {
			if sub.Source != SourceYardr {
				continue
			}
			if fields == nil {
				fields = beadFields(w.Tx, e, row)
			}
			if !fieldsMatch(sub.Where, fields) {
				continue
			}
			reason := ""
			switch why := inv(); {
			case sub.Where["state"] == StateDone:
				if sub.Scope == ScopeAll || why == ReasonMentioned || (why != "" && sub.Scope == ScopeInvolved) {
					reason = ReasonDone
				}
			case why == ReasonMentioned: // every scope covers a mention
				reason = why
			case why == ReasonParticipant && sub.Scope != ScopeMentions:
				reason = why
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
	if err != nil {
		return err
	}
	if len(out) > 0 {
		e.Data["deliveries"] = out
	}
	if e.Kind == "closed" {
		ended, err := endWatches(w.Tx, row.ID)
		if err != nil {
			return err
		}
		if len(ended) > 0 {
			e.Data["watches_ended"] = ended
		}
	}
	return nil
}

// beadFields are the values a yardr subscription's where keys match, each
// a set any of whose values may match.
func beadFields(tx *bolt.Tx, e *Event, row *beadRow) map[string][]string {
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
		if firstClose(tx, row.UUID) {
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
	f["epic"] = []string{row.ID}
	for _, k := range keys(tx, bDepTo, row.UUID+"\x00") {
		var d depRow
		if ok, _ := get(tx, bDeps, k[len(row.UUID)+1:], &d); !ok || (d.Kind != "blocks" && d.Kind != "parent") {
			continue
		}
		var from beadRow
		if ok, _ := get(tx, bBeads, d.From, &from); ok {
			f["epic"] = append(f["epic"], from.ID)
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

// lazyInvolvement returns how the resident is involved in the bead
// (ReasonMentioned, ReasonParticipant or ""), worked out once, when first
// asked: only subscriptions whose filters pass need it.
func lazyInvolvement(tx *bolt.Tx, row *beadRow, name string) func() string {
	done, why := false, ""
	return func() string {
		if !done {
			done, why = true, involvement(tx, row, name)
		}
		return why
	}
}

// involvement: mentioned when "@name" is in the bead's body or a note;
// a participant when it created, advanced or noted the bead.
func involvement(tx *bolt.Tx, row *beadRow, name string) string {
	mention := regexp.MustCompile(`(^|[^A-Za-z0-9_.-])@` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_-])`)
	if mention.MatchString(row.Body) {
		return ReasonMentioned
	}
	mentioned := false
	_ = each(tx, bNotes, row.UUID+"\x00", func(_ []byte, n *Note) error {
		mentioned = mentioned || mention.MatchString(n.Text)
		return nil
	})
	if mentioned {
		return ReasonMentioned
	}
	if row.CreatedBy != nil && *row.CreatedBy == name {
		return ReasonParticipant
	}
	for _, k := range keys(tx, bEventsBy, row.UUID+"\x00") {
		var e Event
		if ok, _ := get(tx, bEvents, k[len(row.UUID)+1:], &e); ok && e.Actor == name &&
			(e.Kind == "created" || e.Kind == "advanced" || e.Kind == "noted") {
			return ReasonParticipant
		}
	}
	return ""
}

// endWatches drops every subscription watching the bead with handle h
// (where bead=h), from residents and groups, and names whose it dropped.
func endWatches(tx *bolt.Tx, h string) ([]string, error) {
	watching := func(sub Subscription) bool { return sub.Source == SourceYardr && sub.Where["bead"] == h }
	// Collected first: a bucket must not change under its own cursor.
	var rs []*Resident
	var gs []*Group
	err := each(tx, bResidents, "", func(_ []byte, r *Resident) error {
		if slices.ContainsFunc(r.Subscriptions, watching) {
			rs = append(rs, r)
		}
		return nil
	})
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
		if err := put(tx, bResidents, r.Name, r); err != nil {
			return nil, err
		}
		ended = append(ended, r.Name)
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
