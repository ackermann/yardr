package web

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"yardr.dev/yardr/internal/store"
)

// feedPage is how many entries one page of the feed holds.
const feedPage = 50

// Entry is one event of the yard as the feed shows it.
type Entry struct {
	// Seq is the event's seq: the entry's place in the feed and its id in
	// the stream.
	Seq   uint64    `json:"seq"`
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"`
	Actor string    `json:"actor"`
	Bead  string    `json:"bead,omitempty"`
	Title string    `json:"title,omitempty"`
	// Text is what happened, in the yard's words: "moved from new to
	// review (done)".
	Text string `json:"text"`
	// Note is what was said with it: the note itself, the reason of a hold,
	// the note a move came with. A long one is cut short.
	Note string `json:"note,omitempty"`
	// Commit is the commit a bead landed at, when its landing's note names one.
	Commit string `json:"commit,omitempty"`
	// Tone is "held" or "landed" for an entry that reads in that colour.
	Tone string `json:"tone,omitempty"`
}

// Clock is the entry's time as the page shows it (see clock).
func (e Entry) Clock() string { return clock(e.At) }

// inFeed reports whether an event has an entry in the feed. A crew member's
// journal entries and handoffs are its own notes to itself, and there are
// many: they are left out. So are a crew member's turns beginning and ending
// (crew_status, but for one that stops at a prompt or leaves it) and
// mail being read (mail_read), which a view of the crew follows and
// nobody reads as news. Of a timer only its firing is news: not that it
// was set, dropped or removed, nor the mail a firing is to a user, which
// the timer's own entry tells.
func inFeed(e store.Event) bool {
	// EventKind: an event from before the crew had the name is in the log
	// under its old kind.
	switch store.EventKind(e.Kind) {
	case store.NoteJournal, store.NoteHandoff, "mail_read", "timer_set", "timer_dropped", "timer_removed":
		return false
	case "mail":
		_, timer := e.Data["timer"]
		return !timer
	case "crew_status":
		return e.Data["status"] == "blocked" || e.Data["was"] == "blocked"
	}
	return true
}

// inHomeFeed reports whether e stands in home's feed: what a person wants
// on a phone (docs/web.md, "The feed"), the rest one tap away behind "All
// activity". HTML-only, beside inFeed: the feed, the JSON and /?all=1 keep
// every event inFeed keeps. A landing's move to the terminal stage is
// hidden beside it, so a landing stands once, and so is a note by the
// bead's own agent or a script group.
func inHomeFeed(ctx context.Context, r *reader, e store.Event) bool {
	switch kind := store.EventKind(e.Kind); {
	case kind == "held" || kind == "unheld":
		return true
	case kind == "session_blocked" || kind == "session_unblocked":
		return true
	case kind == "gave_up" || kind == "crew_gave_up" || kind == "session_harness_error":
		return true
	case kind == "closed" || kind == "released" || kind == "reopened" || kind == "timer":
		return true
	case kind == "noted":
		return keepNote(ctx, r, e)
	case kind == "advanced":
		return keepMove(ctx, r, e)
	case sessionEnded(kind):
		return true
	}
	return false
}

// keepMove reports whether e, an advanced event, stands in home's feed: a
// move back on failed or changes, or a move into or out of a manual stage.
// Every other move is agents doing their jobs.
func keepMove(ctx context.Context, r *reader, e store.Event) bool {
	str := func(k string) string { s, _ := e.Data[k].(string); return s }
	if o := str("outcome"); o == "failed" || o == "changes" {
		return true
	}
	if e.Bead == nil || landingMove(ctx, r, e) {
		return false
	}
	return r.manualStage(ctx, *e.Bead, str("from")) || r.manualStage(ctx, *e.Bead, str("to"))
}

// landingMove reports whether e, an advanced event, is the move a landing
// stands beside: the event right after it closes the same bead with e's
// stage as the reason, as entry reads the pair from the closed side. The
// move is hidden on home so the landing stands once.
func landingMove(ctx context.Context, r *reader, e store.Event) bool {
	to, _ := e.Data["to"].(string)
	if e.Bead == nil || to == "" {
		return false
	}
	n := r.after(ctx, e)
	return n != nil && n.Kind == "closed" && n.Bead != nil && *n.Bead == *e.Bead && n.Data["reason"] == to
}

// keepNote reports whether e, a noted event, stands in home's feed: a note
// by a user or a crew member does. A note by the agent of a session on the
// bead, which acts as <bead>-<stage>, or by a script group, whose sessions
// act as the group (an exec session's actor is its group, and the landing
// is one), is agents doing their jobs.
func keepNote(ctx context.Context, r *reader, e store.Event) bool {
	if e.Bead != nil && strings.HasPrefix(e.Actor, *e.Bead+"-") {
		return false
	}
	return !r.scriptGroup(ctx, e.Actor)
}

// scriptGroup reports whether actor is a group whose sessions act as the
// group itself: the exec runner's (runner.Env). A herdr session acts as its
// bead's agent instead, and a manual group's claim runs nothing.
func (r *reader) scriptGroup(ctx context.Context, actor string) bool {
	if actor == "" {
		return false
	}
	r.loadRoutes(ctx)
	return r.runner[actor] == "exec"
}

// maxNote bounds the note an entry carries: a builder's closing note runs
// to pages, and the feed shows what happened, not all that was said.
const maxNote = 280

// reader reads what an entry needs beyond its event, once per bead, for
// one page of the feed or one event of the stream.
type reader struct {
	st     store.API
	events map[uint64]store.Event
	titles map[string]string
	notes  map[string][]store.Note
	// byBead are a bead's latest events, oldest first.
	byBead map[string][]store.Event
	// beads are the beads looked up, by id, nil for one that is gone.
	beads map[string]*store.Bead
	// routes are the yard's routes, manual the groups among them a
	// person works (Runner manual) and runner every group's runner: what
	// manualStage matches a move's stages against and scriptGroup a note's
	// actor against. Read once, when a move or a note is first filtered.
	routes []store.Route
	manual map[string]bool
	runner map[string]string
	routed bool
}

func (s *Server) newReader(evs []store.Event) *reader { return readerOf(s.st, evs) }

func readerOf(st store.API, evs []store.Event) *reader {
	r := &reader{st: st, events: map[uint64]store.Event{}, titles: map[string]string{}, notes: map[string][]store.Note{},
		byBead: map[string][]store.Event{}, beads: map[string]*store.Bead{}}
	for _, e := range evs {
		r.events[e.Seq] = e
	}
	return r
}

// title is the bead's title, "" for a bead that is gone.
func (r *reader) title(ctx context.Context, bead string) string {
	t, ok := r.titles[bead]
	if !ok {
		if b, err := r.st.GetBead(ctx, bead); err == nil {
			t = b.Title
		}
		r.titles[bead] = t
	}
	return t
}

func (r *reader) beadNotes(ctx context.Context, bead string) []store.Note {
	ns, ok := r.notes[bead]
	if !ok {
		ns, _ = r.st.Notes(ctx, bead)
		r.notes[bead] = ns
	}
	return ns
}

// noteOf is the note a `noted` event added.
func (r *reader) noteOf(ctx context.Context, e store.Event) string {
	seq, ok := number(e.Data["note"])
	if !ok || e.Bead == nil {
		return ""
	}
	for _, n := range r.beadNotes(ctx, *e.Bead) {
		if n.Seq == seq {
			return n.Text
		}
	}
	return ""
}

// beadEvents bounds the events of one bead the reader looks through.
const beadEvents = 200

// said is the note the event's actor left on the bead just before it: the
// note a move came with (`bead advance --note`), the reason before a hold
// (`bead note ... && bead hold`). Only the bead's event right before e
// counts, and only when it is the actor's note: an older note, or someone
// else's, is no reason for this event.
func (r *reader) said(ctx context.Context, e store.Event) string {
	if e.Bead == nil {
		return ""
	}
	evs := r.eventsOf(ctx, *e.Bead)
	for i := len(evs) - 1; i > 0; i-- {
		if evs[i].Seq == e.Seq {
			if p := evs[i-1]; p.Kind == "noted" && p.Actor == e.Actor {
				return r.noteOf(ctx, p)
			}
			break
		}
	}
	return ""
}

// eventsOf are a bead's latest events, oldest first.
func (r *reader) eventsOf(ctx context.Context, bead string) []store.Event {
	evs, ok := r.byBead[bead]
	if !ok {
		evs, _ = r.st.Events(ctx, bead, beadEvents)
		r.byBead[bead] = evs
	}
	return evs
}

// before is the event with the seq before e's, nil when it is not retained.
func (r *reader) before(ctx context.Context, e store.Event) *store.Event {
	if e.Seq < 2 {
		return nil
	}
	if p, ok := r.events[e.Seq-1]; ok {
		return &p
	}
	evs, err := r.st.EventsSince(ctx, e.Seq-2, 1)
	if err != nil || len(evs) == 0 || evs[0].Seq != e.Seq-1 {
		return nil
	}
	return &evs[0]
}

// after is the event with the seq after e's, nil when it is not retained
// or not committed yet.
func (r *reader) after(ctx context.Context, e store.Event) *store.Event {
	if p, ok := r.events[e.Seq+1]; ok {
		return &p
	}
	evs, err := r.st.EventsSince(ctx, e.Seq, 1)
	if err != nil || len(evs) == 0 || evs[0].Seq != e.Seq+1 {
		return nil
	}
	return &evs[0]
}

// bead is the bead id names, nil for one that is gone.
func (r *reader) bead(ctx context.Context, id string) *store.Bead {
	b, ok := r.beads[id]
	if !ok {
		b, _ = r.st.GetBead(ctx, id)
		r.beads[id] = b
	}
	return b
}

// manualStage reports whether stage is a manual group's: the routes send a
// bead of bead's kind there to a group a person works. The bead's own group
// is now's, not the move's, so only the routes say.
func (r *reader) manualStage(ctx context.Context, bead, stage string) bool {
	b := r.bead(ctx, bead)
	if b == nil {
		return false
	}
	r.loadRoutes(ctx)
	probe := *b
	probe.Stage = stage
	rt := store.MatchRoute(r.routes, probe)
	return rt != nil && r.manual[rt.Group]
}

// loadRoutes reads the yard's routes and groups once. Without a store (the
// kinds test) there is nothing to read.
func (r *reader) loadRoutes(ctx context.Context) {
	if r.routed {
		return
	}
	r.routed = true
	r.manual = map[string]bool{}
	r.runner = map[string]string{}
	if r.st == nil {
		return
	}
	if routes, err := r.st.Routes(ctx); err == nil {
		store.SortRoutes(routes)
		r.routes = routes
	}
	if groups, err := r.st.Groups(ctx); err == nil {
		for _, g := range groups {
			r.manual[g.Name] = g.Runner == "manual"
			r.runner[g.Name] = g.Runner
		}
	}
}

// A commit as a note names it: git's short or full hash, on its own.
var commitRE = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)

// sessionEnded reports whether kind is a session ending in a state
// (session_<state>, from EndSession). The dispatcher's session_ events say
// how a running one is doing instead.
func sessionEnded(kind string) bool {
	return strings.HasPrefix(kind, "session_") && !slices.Contains(store.DispatchKinds, kind)
}

// entry says e in the yard's words.
func (r *reader) entry(ctx context.Context, e store.Event) Entry {
	out := Entry{Seq: e.Seq, At: e.At, Kind: e.Kind, Actor: e.Actor}
	if e.Bead != nil {
		out.Bead = *e.Bead
		out.Title = r.title(ctx, out.Bead)
	}
	str := func(k string) string { s, _ := e.Data[k].(string); return s }
	switch {
	case e.Kind == "created":
		out.Text = "created"
		if out.Title == "" {
			out.Title = str("title")
		}
	case e.Kind == "advanced":
		out.Text = fmt.Sprintf("moved from %s to %s", str("from"), str("to"))
		if o := str("outcome"); o != "" {
			out.Text += " (" + o + ")"
		}
		out.Note = r.said(ctx, e)
	case e.Kind == "closed":
		// A bead that enters a terminal stage is closed in the same write,
		// with the stage as the reason: it landed. The move's note says
		// where (the assembly's "merged <commit>").
		if p := r.before(ctx, e); p != nil && p.Kind == "advanced" && p.Bead != nil && *p.Bead == out.Bead &&
			p.Data["to"] == str("reason") && str("reason") != "" {
			out.Text, out.Tone = "landed in "+str("reason"), "landed"
			out.Note = r.said(ctx, *p)
			if out.Commit = commitRE.FindString(out.Note); out.Commit != "" {
				out.Text += " at " + out.Commit
			}
			break
		}
		out.Text = "closed"
		if reason := str("reason"); reason != "" {
			out.Text += ": " + reason
		}
	case e.Kind == "reopened":
		out.Text = "reopened"
	case e.Kind == "updated":
		out.Text, out.Note = "updated", updated(e.Data)
	case e.Kind == "held":
		out.Text, out.Tone = "held", "held"
		out.Note = r.said(ctx, e)
	case e.Kind == "unheld":
		out.Text = "no longer held"
	case e.Kind == "released":
		out.Text, out.Note = "released, to be routed again", str("reason")
	case e.Kind == "noted":
		out.Text, out.Note = "noted", r.noteOf(ctx, e)
	case e.Kind == "claimed":
		out.Text = fmt.Sprintf("claimed by %s for %s", str("group"), str("stage"))
	case e.Kind == "started":
		out.Text = "session started"
		if h, ok := e.Data["handle"].(map[string]any); ok {
			if agent, _ := h["agent"].(string); agent != "" {
				out.Text += ": " + agent
			}
		}
	case sessionEnded(e.Kind):
		// A session that ends well ends with its bead's move, which has an
		// entry of its own: one that ends here did not.
		out.Text, out.Note, out.Tone = "session ended: "+strings.TrimPrefix(e.Kind, "session_"), str("reason"), "held"
	case e.Kind == "session_harness_error":
		// The reason and the harness's own line, not everything the event
		// carries: its hint and the pane's end would be all an entry shows.
		out.Text, out.Note, out.Tone = "harness stopped on an error: "+strings.ReplaceAll(str("reason"), "_", " "), str("line"), "held"
	case e.Kind == "session_blocked":
		// What the agent asks stands at the end of its pane, so the note is
		// cut from the front, where every other is cut at the end.
		out.Text, out.Note = "session blocked", ending(str("tail"))
	case e.Kind == store.MoveRefused:
		// What the agent ran and the yard's answer, not the hint.
		out.Text, out.Note, out.Tone = "move refused ("+strings.ReplaceAll(str("reason"), "_", " ")+"): "+str("command"), str("error"), "held"
	case e.Kind == "timer":
		out.Text, out.Note = fmt.Sprintf("timer of %s for %s", str("by"), str("to")), strings.TrimPrefix(str("text"), "timer: ")
		if str("by") == str("to") {
			out.Text = "timer of " + str("by")
		}
	default:
		// Every other event in its own name, with what it carries.
		out.Text, out.Note = strings.ReplaceAll(e.Kind, "_", " "), carried(e.Data)
	}
	out.Note = short(out.Note)
	return out
}

// carried is an event's plain values, in the order of their names: what an
// entry with no wording of its own shows.
func carried(data map[string]any) string {
	var parts []string
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		switch v := data[k].(type) {
		case string:
			if v != "" {
				parts = append(parts, k+" "+v)
			}
		case bool, float64, int, int64, uint64:
			parts = append(parts, fmt.Sprintf("%s %v", k, v))
		}
	}
	return strings.Join(parts, " · ")
}

// short cuts a note to maxNote bytes, on a rune.
func short(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxNote {
		return s
	}
	cut := maxNote
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

// ending is the end of a pane's tail in maxNote bytes, from a rune on.
func ending(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxNote {
		return s
	}
	cut := len(s) - maxNote + len("…")
	for cut < len(s) && !utf8.RuneStart(s[cut]) {
		cut++
	}
	return "…" + strings.TrimSpace(s[cut:])
}

// number reads a seq from an event's data: a uint64 in an event straight
// from the store, a float64 in one that crossed JSON.
func number(v any) (uint64, bool) {
	switch n := v.(type) {
	case uint64:
		return n, true
	case float64:
		return uint64(n), n >= 0
	case int:
		return uint64(n), n >= 0
	}
	return 0, false
}

// Feed is one page of the feed, newest first.
type Feed struct {
	Events []Entry `json:"events"`
	// Head is the yard's newest seq when the page was read. The first page
	// holds everything up to it, so the stream after it (/events?after=)
	// continues the page with no gap and no duplicate.
	Head uint64 `json:"head"`
	// Before is the seq the page was asked to stay under, 0 for the first.
	Before uint64 `json:"before,omitempty"`
	// Older is the seq to ask for the next page with (?before=), 0 when
	// nothing older is retained.
	Older uint64 `json:"older,omitempty"`
	// evs are the events the entries were said from, one per entry, newest
	// first: what the home filter reads beside the entries. Not JSON.
	evs []store.Event
}

// keptHome filters f to what stands in home's feed, newest first. f keeps
// the full page: the JSON names every entry either way.
func (s *Server) keptHome(ctx context.Context, f *Feed) []Entry {
	rd := s.newReader(f.evs)
	out := []Entry{}
	for i, e := range f.evs {
		if inHomeFeed(ctx, rd, e) {
			out = append(out, f.Events[i])
		}
	}
	return out
}

// feed reads the newest feedPage entries with a seq under before (0: the
// newest of all).
func (s *Server) feed(ctx context.Context, before uint64) (*Feed, error) {
	head, err := s.st.Head(ctx)
	if err != nil {
		return nil, err
	}
	f := &Feed{Events: []Entry{}, Head: head, Before: before}
	// The oldest retained event: pruning takes the log's start, so the
	// seqs from there to the head have no hole worth walking.
	oldest, err := s.st.EventsSince(ctx, 0, 1)
	if err != nil {
		return nil, err
	}
	if len(oldest) == 0 {
		return f, nil
	}
	first := oldest[0].Seq
	// under is the seq every event still to read is below. The first page
	// stops at the head read above, not at whatever was committed since:
	// the stream brings that.
	under := head + 1
	if before != 0 && before < under {
		under = before
	}
	// Read backwards a window at a time: events left out of the feed mean
	// one window may not fill the page.
	const window = 4 * feedPage
	for under > first && len(f.Events) < feedPage {
		after := first - 1
		if under-1 > after+window {
			after = under - 1 - window
		}
		evs, err := s.st.EventsSince(ctx, after, int(under-1-after))
		if err != nil {
			return nil, err
		}
		rd := s.newReader(evs)
		for i := len(evs) - 1; i >= 0 && len(f.Events) < feedPage; i-- {
			if e := evs[i]; e.Seq < under {
				under = e.Seq
				if inFeed(e) {
					f.Events = append(f.Events, rd.entry(ctx, e))
					f.evs = append(f.evs, e)
				}
			}
		}
		if len(f.Events) < feedPage {
			under = after + 1
		}
	}
	if under > first {
		f.Older = under
	}
	return f, nil
}
