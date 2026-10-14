package web

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Line is the right column, the same on every page: the beads that wait for
// a person, and under them the line in numbers.
type Line struct {
	// As is the user the view acts as, "" when it does not act: an asked
	// row then answers with buttons, and no other row takes a write.
	As string `json:"as,omitempty"`
	// Waits are the open beads something stops: held, blocked on a prompt
	// or stranded. The one that began to wait last comes first.
	Waits []Waiting `json:"waits"`
	// Parked are the open beads nothing stops but that no agent will move:
	// their only reason is a stage of a manual group. In the same order.
	Parked []Waiting `json:"parked"`
	Open   int       `json:"open"`
	// Working counts the beads an agent's or a command's session has
	// claimed; a manual group's claim is not work in progress.
	Working int `json:"working"`
	Held    int `json:"held"`
	// Sessions counts the sessions running: a manual group's claim is no
	// process and is not among them.
	Sessions int `json:"sessions"`
	// Stages are the stages that hold an open bead, summed over the depots, in
	// the order of their flows.
	Stages []StageCount `json:"stages"`
	Depots []LineDepot  `json:"depots"`
	// Home is the home mode (?home=1): the line panel alone, without the
	// waits home lists at the top of its centre already. HTML-only: the
	// JSON carries the waits either way.
	Home bool `json:"-"`
}

type LineDepot struct {
	Name string `json:"name"`
	Open int    `json:"open"`
}

// OpenDepots are the depots that hold an open bead: what the column shows,
// which prints no zero.
func (l Line) OpenDepots() []LineDepot {
	var out []LineDepot
	for _, d := range l.Depots {
		if d.Open > 0 {
			out = append(out, d)
		}
	}
	return out
}

// Waiting is one bead that waits for a person, with every reason it does.
type Waiting struct {
	Bead  string `json:"bead"`
	Title string `json:"title"`
	Depot string `json:"depot,omitempty"`
	Stage string `json:"stage"`
	// Since is when the bead last changed: about when it began to wait.
	Since time.Time `json:"since"`
	Why   []Reason  `json:"why"`
	// Topic is the bead's first topic label without its prefix ("web-view"
	// for "topic:web-view"), "" when it has none. The home row shows it
	// small beside an ask; HTML-only, as the panel's marks are.
	Topic string `json:"-"`
	// Revision is the bead's revision when the row was read, for the
	// forms that answer its asks: a write from another revision is
	// refused. HTML-only: the JSON carries why, and the socket's Bead
	// carries the revision.
	Revision string `json:"-"`
	// As is the user the view acts as, "" when it does not act: the row's
	// asks then answer with buttons. HTML-only, as the revision is.
	As string `json:"-"`
}

// Reason is one cause a bead waits for a person.
type Reason struct {
	// Kind is held, blocked, stranded, manual or ask. A bead whose reasons
	// are all manual is parked; an ask waits in the same list, one row per
	// bead whatever the reasons.
	Kind string `json:"kind"`
	// Text says it in the yard's words: "waits for decisions in decide",
	// or the ask's question.
	Text string `json:"text"`
	// Note is what was said with it: the reason a hold was given, or an
	// ask's context lines.
	Note string `json:"note,omitempty"`
	// GaveUp is set on a held reason when the bead's events hold a gave_up
	// for that hold: a builder that gave up after repeated failures. The
	// text then names the give-up, and the home row shortens it further.
	// HTML-only, as the panel's marks are: the JSON carries the text.
	GaveUp bool `json:"-"`
	// Recommend is an ask's recommendation in one line ("1. Move them
	// now", or "words wanted" when the ask has no options), "" when the
	// ask recommends nothing. The home row shows it faint; HTML-only: the
	// JSON carries the question and the context.
	Recommend string `json:"-"`
	// RecommendWhy is why the ask recommends it, folded to one line, ""
	// when it says nothing or recommends nothing: what the ask's detail
	// names after the option. HTML-only, as the recommendation is.
	RecommendWhy string `json:"-"`
	// Context is an ask's context lines, each folded to one line, whole:
	// what the ask's detail shows, where Note joins and shortens them
	// for the JSON. Nil on any other reason. HTML-only, as the
	// recommendation is.
	Context []string `json:"-"`
	// Ask is the ask's seq on its bead, 0 when the reason is no ask: what
	// an answer names. Options are the ask's options in order, none for
	// an ask that wants words, a secret or a command's approval.
	// SecretLine is what a secret ask asks for, in the yard's words, ""
	// otherwise; RunCommand the command a run ask proposes, whole, ""
	// otherwise. HTML-only, as the recommendation is: the row's buttons
	// answer from them.
	Ask        uint64            `json:"-"`
	Options    []store.AskOption `json:"-"`
	SecretLine string            `json:"-"`
	RunCommand string            `json:"-"`
}

// Reasons are why the bead waits, in one line.
func (w Waiting) Reasons() string {
	var out []string
	for _, r := range w.Why {
		out = append(out, r.Text)
	}
	return strings.Join(out, " · ")
}

// Lead is the row's first words on the home page: what happened for a bead
// something stops, where the person is needed for a parked one. A give-up
// names itself; a manual wait names its stage. An ask is never the lead:
// the row shows its question where the title stands, after the fault when
// there is one.
func (w Waiting) Lead() string {
	for _, r := range w.Why {
		if r.Kind == "manual" || r.Kind == "ask" {
			continue
		}
		if r.Kind == "held" && r.GaveUp {
			return "gave up"
		}
		return r.Kind
	}
	return w.Stage
}

// HasAsk reports whether the bead carries an open ask to the operator.
func (w Waiting) HasAsk() bool {
	return slices.ContainsFunc(w.Why, func(r Reason) bool { return r.Kind == "ask" })
}

// AskQuestion is the oldest open ask's question: what the home row shows
// where the title stands. "" when the bead carries no ask.
func (w Waiting) AskQuestion() string {
	for _, r := range w.Why {
		if r.Kind == "ask" {
			return r.Text
		}
	}
	return ""
}

// AskRecommend is the oldest open ask's recommendation in one line, "" when
// it recommends nothing or the bead carries no ask.
func (w Waiting) AskRecommend() string {
	for _, r := range w.Why {
		if r.Kind == "ask" {
			return r.Recommend
		}
	}
	return ""
}

// FirstAsk is the oldest open ask's reason, for the home row's buttons:
// the question the row shows is the one they answer. Nil when the bead
// carries no ask.
func (w Waiting) FirstAsk() *Reason {
	for i := range w.Why {
		if w.Why[i].Kind == "ask" {
			return &w.Why[i]
		}
	}
	return nil
}

// Fault reports whether the bead waits on something gone wrong (a hold, a
// prompt nobody answers) and not a stage that is a person's by design.
func (w Waiting) Fault() bool {
	return slices.ContainsFunc(w.Why, func(r Reason) bool { return r.Alarm() })
}

// ShortID is the bead's short id for the row's end: what stands after the
// last dash (yardr-evd6 reads as evd6), the whole id when there is none.
func (w Waiting) ShortID() string {
	if i := strings.LastIndex(w.Bead, "-"); i >= 0 {
		return w.Bead[i+1:]
	}
	return w.Bead
}

// Age is how long the bead has waited, about: since its last change.
func (w Waiting) Age() string { return age(time.Since(w.Since)) }

// Alarm reports whether the reason is something gone wrong (a hold, a
// prompt nobody answers) and not a stage that is a person's by design.
func (r Reason) Alarm() bool { return r.Kind == "held" || r.Kind == "blocked" }

// line reads the right column. ov is the overview when the handler has read
// it already, nil otherwise.
func (s *Server) line(ctx context.Context, ov *Home) (*Line, error) {
	if ov == nil {
		var err error
		if ov, err = s.overview(ctx, nil); err != nil {
			return nil, err
		}
	}
	l := &Line{As: s.acting(ctx), Stages: []StageCount{}, Depots: []LineDepot{}}
	add := func(counts []StageCount) {
		for _, c := range counts {
			if c.Open == 0 {
				continue
			}
			if i := slices.IndexFunc(l.Stages, func(sc StageCount) bool { return sc.Stage == c.Stage }); i >= 0 {
				l.Stages[i].Open += c.Open
			} else {
				l.Stages = append(l.Stages, c)
			}
		}
	}
	for _, r := range ov.Depots {
		l.Open += r.Open
		l.Working += r.Working
		l.Held += r.Held
		l.Depots = append(l.Depots, LineDepot{Name: r.Name, Open: r.Open})
		for _, f := range r.Flows {
			add(f.Stages)
		}
		add(r.Elsewhere)
	}
	for _, sess := range ov.yard.sessions {
		if !isManual(sess) {
			l.Sessions++
		}
	}
	var err error
	if l.Waits, l.Parked, err = s.waits(ctx, ov.yard); err != nil {
		return nil, err
	}
	return l, nil
}

// waits lists the open beads that wait for a person: held, in a session
// that is blocked on a prompt, stranded in a stage their flow lacks, or in
// a stage routed to a manual group. Those with no reason but the last are
// parked, and listed apart: nothing went wrong with them.
func (s *Server) waits(ctx context.Context, y *yard) (waits, parked []Waiting, err error) {
	groups, err := s.st.Groups(ctx)
	if err != nil {
		return nil, nil, err
	}
	manual := map[string]store.Group{}
	for _, g := range groups {
		if g.Runner == "manual" {
			manual[g.Name] = g
		}
	}
	routes, err := s.st.Routes(ctx)
	if err != nil {
		return nil, nil, err
	}
	store.SortRoutes(routes)
	// A depot whose repository cannot be read must not take the column, and
	// with it every page, down: its beads are then not known to be stranded.
	stranded, err := s.st.Stranded(ctx, "")
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, err
		}
		s.log.Error("web: stranded beads", "err", err)
	}
	lacking := map[string]string{}
	for _, sb := range stranded {
		lacking[sb.Bead] = sb.Flow
	}
	// The sessions that can wait on a prompt, by bead. A manual group's
	// claim is none of them: nothing runs in it, so nothing asks, and a
	// yard's parked beads are each claimed by one. Asking a bead's events
	// for each would cost a read of the store per parked bead.
	agents := map[string][]store.Session{}
	for _, sess := range y.sessions {
		if !isManual(sess) {
			agents[sess.Bead] = append(agents[sess.Bead], sess)
		}
	}
	asks, err := s.openAsks(ctx, y)
	if err != nil {
		return nil, nil, err
	}
	rd := s.newReader(nil)
	waits, parked = []Waiting{}, []Waiting{}
	for _, b := range y.beads {
		var why []Reason
		if b.Hold {
			why = append(why, rd.held(ctx, b.ID))
		}
		for _, sess := range agents[b.ID] {
			if rd.blocked(ctx, sess) {
				why = append(why, Reason{Kind: "blocked", Text: "its session waits on a prompt: " + sessionName(sess)})
			}
		}
		if flow, ok := lacking[b.ID]; ok {
			why = append(why, Reason{Kind: "stranded",
				Text: fmt.Sprintf("stranded in %s, a stage flow %s lacks", b.Stage, flow)})
		}
		// The group working the bead, else the one its stage is routed to: a
		// manual group at its limit leaves beads waiting unclaimed.
		group := ""
		if b.Group != nil {
			group = *b.Group
		} else if r := store.MatchRoute(routes, b); r != nil {
			group = r.Worker()
		}
		if g, ok := manual[group]; ok {
			text := "waits for " + g.Name
			if len(g.Members) > 0 {
				text += " (" + strings.Join(g.Members, ", ") + ")"
			}
			why = append(why, Reason{Kind: "manual", Text: text + " in " + b.Stage})
		}
		// An open ask to the acting user waits with the rest, fault first:
		// a bead that both waits and is asked stands once.
		why = append(why, asks[b.ID]...)
		if len(why) == 0 {
			continue
		}
		w := Waiting{Bead: b.ID, Title: b.Title, Stage: b.Stage, Since: b.UpdatedAt, Why: why, Revision: b.Revision, As: s.acting(ctx)}
		if b.Depot != nil {
			w.Depot = *b.Depot
		}
		for _, l := range b.Labels {
			if topic, ok := strings.CutPrefix(l, "topic:"); ok {
				w.Topic = topic
				break
			}
		}
		if !slices.ContainsFunc(why, func(r Reason) bool { return r.Kind != "manual" }) {
			parked = append(parked, w)
			continue
		}
		waits = append(waits, w)
	}
	latest := func(a, b Waiting) int { return b.Since.Compare(a.Since) }
	slices.SortStableFunc(waits, latest)
	slices.SortStableFunc(parked, latest)
	return waits, parked, nil
}

// openAsks reads the open asks on the yard's open beads, oldest first per
// bead: the queue UserAsks keeps, joined to the rows the waits join. A
// request that acts reads the user it acts as — in JWT mode the request's
// verified user, so each visitor sees only their own asks; one that does
// not reads the operator. A question from another yard (bead@peer) stands
// in no row: it has no bead here and no stage for a lamp. A yard that
// names no one to read, or whose queue cannot be read, waits without asks,
// as a depot whose beads cannot be told stranded does.
func (s *Server) openAsks(ctx context.Context, y *yard) (map[string][]Reason, error) {
	out := map[string][]Reason{}
	op := s.acting(ctx)
	if op == "" {
		all, err := s.st.Settings(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			s.log.Error("web: settings for asks", "err", err)
			return out, nil
		}
		op = all[store.OperatorSetting]
		if op == "" {
			return out, nil
		}
	}
	q, err := s.st.UserAsks(ctx, op)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		s.log.Error("web: user asks", "err", err)
		return out, nil
	}
	// The queue holds closed beads too; only the open ones wait.
	open := map[string]bool{}
	for _, b := range y.beads {
		open[b.ID] = true
	}
	byBead := map[string][]store.OpenAsk{}
	for _, o := range q.Open {
		if !open[o.Bead] || strings.Contains(o.Bead, "@") {
			continue
		}
		byBead[o.Bead] = append(byBead[o.Bead], o)
	}
	now := time.Now()
	for id, opens := range byBead {
		notes, err := s.st.Notes(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			s.log.Error("web: bead notes for asks", "bead", id, "err", err)
			continue
		}
		resolved := map[uint64]store.ResolvedAsk{}
		for _, r := range store.ResolveAsks(notes, now) {
			resolved[r.Note.Seq] = r
		}
		for _, o := range opens {
			r, ok := resolved[o.Ask]
			if !ok || r.AskErr != nil || r.Ask.To != op || r.State != store.AskOpen {
				continue
			}
			out[id] = append(out[id], askReason(r))
		}
	}
	return out, nil
}

// askReason is one open ask as a wait: the question in one line, its
// context lines as the note and whole, and its recommendation in one line
// for the home row: the recommended option's number with its label, so
// the row names what to pick where writing lives. The row keeps one line,
// so the question folds to it, as letters only, and a context line folds
// to the line it is; the note's breaks are then only between genuine
// lines. The ask's detail shows the lines whole, with the options and the
// recommendation labelled. An ask with no options wants words, a secret
// ask says what it asks for, and a run ask shows the command its approval
// runs, whole.
func askReason(r store.ResolvedAsk) Reason {
	context := make([]string, len(r.Ask.Context))
	for i, line := range r.Ask.Context {
		context[i] = store.OneLine(line)
	}
	out := Reason{Kind: "ask", Text: store.OneLine(r.Ask.Question), Note: short(strings.Join(context, "\n")),
		Context: context, Ask: r.Note.Seq, Options: r.Ask.Options}
	switch {
	case r.Ask.Run != nil:
		// The yard's own, and never shortened: the command as it would run.
		out.Recommend = "to approve: " + r.Ask.Run.Command()
		out.RunCommand = r.Ask.Run.Command()
	case r.Ask.Secret != nil:
		// The yard's line, never the asker's words: what is asked for.
		out.Recommend = r.Ask.Secret.Line()
		out.SecretLine = r.Ask.Secret.Line()
	case len(r.Ask.Options) == 0:
		out.Recommend = "words wanted"
	case r.Ask.Recommend >= 1 && r.Ask.Recommend <= len(r.Ask.Options):
		out.Recommend = fmt.Sprintf("%d. %s", r.Ask.Recommend, store.OneLine(r.Ask.Options[r.Ask.Recommend-1].Label))
		out.RecommendWhy = store.OneLine(r.Ask.Why)
	}
	return out
}

// held says why a bead is held: who held it, and the note they left right
// before (`yardr bead note ... && yardr bead hold`), as the feed shows it.
// When the bead's events hold a gave_up for that hold, the reason names the
// give-up instead: "held" alone hides the fault the person must fix.
func (r *reader) held(ctx context.Context, bead string) Reason {
	out := Reason{Kind: "held", Text: "held"}
	evs := r.eventsOf(ctx, bead)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == "held" {
			out.Text, out.Note = "held by "+evs[i].Actor, short(r.said(ctx, evs[i]))
			break
		}
	}
	if g := r.gaveUpAfter(ctx, bead); g != nil {
		out.GaveUp = true
		out.Text = gaveUpText(*g)
	}
	return out
}

// gaveUpAfter is the gave_up event after the bead's last held, if one stands
// there: the dispatcher holds first, then logs it. A hold lifted and given
// again is plain, whatever an earlier hold gave up.
func (r *reader) gaveUpAfter(ctx context.Context, bead string) *store.Event {
	evs := r.eventsOf(ctx, bead)
	last := -1
	for i, e := range evs {
		if e.Kind == "held" {
			last = i
		}
	}
	if last < 0 {
		return nil
	}
	for i := last + 1; i < len(evs); i++ {
		if evs[i].Kind == "gave_up" {
			return &evs[i]
		}
	}
	return nil
}

// gaveUpText says a gave_up event in the yard's words: "gave up after 3
// failures in new". A hand-logged event may lack the count or the stage,
// and is then said with what it has.
func gaveUpText(e store.Event) string {
	out := "gave up"
	if n, ok := number(e.Data["failures"]); ok {
		out += fmt.Sprintf(" after %d failure", n)
		if n != 1 {
			out += "s"
		}
	}
	if stage, _ := e.Data["stage"].(string); stage != "" {
		out += " in " + stage
	}
	return out
}

// blocked reports whether a session waits on a permission prompt or a
// question: the dispatcher's last word on it is session_blocked. The yard
// keeps that nowhere but in the bead's events.
func (r *reader) blocked(ctx context.Context, sess store.Session) bool {
	evs := r.eventsOf(ctx, sess.Bead)
	for i := len(evs) - 1; i >= 0; i-- {
		if k := evs[i].Kind; (k == "session_blocked" || k == "session_unblocked") && evs[i].Data["session"] == sess.ID {
			return k == "session_blocked"
		}
	}
	return false
}

// sessionName is what a person looks for to find a session: the agent's
// name in its runner when there is one, else the session's id.
func sessionName(sess store.Session) string {
	if agent, _ := sess.Handle["agent"].(string); agent != "" {
		return agent
	}
	return sess.ID
}

// homeLine reports whether the request asks for the home mode of /line
// (?home=1): the line panel alone, what home's aside shows and re-reads,
// following the boolean query-parameter convention of ?panel=1 and ?all=1.
// The parameter shapes the HTML alone.
func homeLine(r *http.Request) bool {
	switch r.URL.Query().Get("home") {
	case "", "0", "false":
		return false
	}
	return true
}

// handleLine is the right column as a page of its own, for a window with no
// room for the column.
func (s *Server) handleLine(w http.ResponseWriter, r *http.Request) {
	head, err := s.st.Head(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	l, err := s.line(r.Context(), nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	l.Home = homeLine(r)
	s.respond(w, r, Page{Template: "line", Title: "Line", Menu: "line", Data: l, line: l, head: head})
}
