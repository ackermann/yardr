package web

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"slices"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Card is one bead as a list shows it: in a stage of /flows, in /beads, and
// as the train, a child or a blocker on a bead's own page.
type Card struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
	Stage string `json:"stage"`
	// Status is open or closed: a list of open beads holds only open ones,
	// a train's children may be either.
	Status string `json:"status"`
	Depot  string `json:"depot,omitempty"`
	// Held is a bead on hold; Working one an agent's or a command's session
	// has claimed. A manual group's claim is a person's turn and no work in
	// progress, as on Home and the line.
	Held    bool   `json:"held,omitempty"`
	Working bool   `json:"working"`
	Group   string `json:"group,omitempty"`
	// Train is the train the bead is a child of, on the cards of a listing.
	Train     string    `json:"train,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// Live is a card on /flows, which the stream takes out when its bead
	// moves or closes; Placed one under its stage's heading there, which
	// says its depot and stage already.
	Live   bool `json:"-"`
	Placed bool `json:"-"`
	// Arrived is a card the stream brings because its bead entered a
	// stage: the page shows it arriving.
	Arrived bool `json:"-"`
}

// DOM is the id of the card's element, and the name of the stream's event
// that takes the card out of the section it is in.
func (c Card) DOM() string { return domID("bead", c.ID) }

// Clock is when the bead last changed, as the feed writes a time.
func (c Card) Clock() string { return clock(c.UpdatedAt) }

// manualClaims are the ids of the sessions among sessions that are a manual
// group's claim.
func manualClaims(sessions []store.Session) map[string]bool {
	manual := map[string]bool{}
	for _, sess := range sessions {
		if isManual(sess) {
			manual[sess.ID] = true
		}
	}
	return manual
}

// working reports whether b is being worked: a session has claimed it, and
// not one of manual, the claims of manual groups.
func working(b *store.Bead, manual map[string]bool) bool {
	return b.Session != nil && !manual[*b.Session]
}

// card is b as a list shows it; manual are the manual groups' claims, which
// the bead alone does not tell from an agent's session.
func card(b *store.Bead, manual map[string]bool) Card {
	c := Card{ID: b.ID, Title: b.Title, Type: b.Type, Stage: b.Stage, Status: b.Status, Held: b.Hold,
		Working: working(b, manual), Train: b.Train, UpdatedAt: b.UpdatedAt}
	if b.Depot != nil {
		c.Depot = *b.Depot
	}
	if b.Group != nil {
		c.Group = *b.Group
	}
	return c
}

// newestFirst orders cards by their last change, the newest first: the
// order the stream keeps by putting a changed bead's card on top.
func newestFirst(cs []Card) {
	slices.SortStableFunc(cs, func(a, b Card) int {
		if c := b.UpdatedAt.Compare(a.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// domID is an element id made of parts that are names from the yard (a
// depot, a bead type, a stage, a bead). Each part is escaped, so no name can
// run into the next, and the id has no space, comma or newline: it is also
// the name of an event of the stream, in a list the page's markup gives.
func domID(kind string, parts ...string) string {
	id := kind
	for _, p := range parts {
		id += "/" + url.PathEscape(p)
	}
	return id
}

// Flows is the /flows page: per depot, the flows that govern it, each with
// its stages in order and the open beads in every stage.
type Flows struct {
	Depots []FlowsDepot `json:"depots"`
	// Head is the yard's newest seq when the page was read, before its
	// beads: the stream after it (/events?after=) brings every later move.
	Head uint64 `json:"head"`
	// rfs and groups are what the panel draws from: the flows as the store
	// reads them, beside Depots, and the yard's groups for the buildings.
	// HTML-only: the JSON never carries them.
	rfs    [][]store.DepotFlow
	groups []store.Group
	// marks and lamps are the panel's blocked vocabulary, read when the
	// panel is asked for: per bead what colours its block, per stage
	// whether a wait lamp burns. HTML-only like the panel itself.
	marks map[string]panelMark
	lamps map[string]flowLamp
}

// panelMark is what colours one bead's block on the yard panel: gave up,
// stalled, blocked on a prompt, waiting on a dependency, back on failed or
// changes. Held and working are the card's own.
type panelMark struct {
	gaveUp     bool
	stalled    bool
	blocked    bool
	dep        string // the first open blocker's full id, "" when none waits
	failedBack bool
}

// flowLamp is a stage's wait lamp on the yard panel.
type flowLamp string

const (
	lampFault flowLamp = "fault"
	lampTurn  flowLamp = "turn"
)

type FlowsDepot struct {
	Name  string       `json:"name"`
	Kind  string       `json:"kind"`
	Flows []FlowColumn `json:"flows"`
	// Elsewhere are the depot's open beads in no stage of the flows above: a
	// bead that pins a flow of its own, one a changed flow left behind,
	// and every bead of a depot with no flow.
	Elsewhere []Card `json:"elsewhere"`
	// Error is why the depot's flows could not be read; its beads are then
	// all in Elsewhere.
	Error string `json:"error,omitempty"`
}

// Section is the id of the element that holds the depot's Elsewhere cards.
func (r FlowsDepot) Section() string { return domID("off", r.Name) }

// FlowColumn is one flow of a depot: the depot's own (Type "") or a bead type's.
type FlowColumn struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	// Source is where the flow comes from, as `yardr flow show` says it:
	// "store", "repo@0f415ee".
	Source string      `json:"source"`
	Stages []FlowStage `json:"stages"`
}

// FlowStage is one stage of a flow and the open beads in it.
type FlowStage struct {
	Stage string `json:"stage"`
	// Group is the group a bead of the flow's type would be routed to in
	// this stage, "" when no route matches. Human says it is a manual
	// group, whose Members may move a bead on (none: anyone).
	Group   string   `json:"group,omitempty"`
	Human   bool     `json:"human,omitempty"`
	Members []string `json:"members,omitempty"`
	// Else are the further groups of the route, in order: which of them
	// takes a bead is chosen when it is claimed, by the quota then.
	Else []string `json:"else,omitempty"`
	// StageFile is the path of the stage's file when it has one: in the
	// depot's repository, or in the yard's home for a flow in the store.
	StageFile string   `json:"stage_file,omitempty"`
	Next      []string `json:"next"`
	// Terminal is a stage that closes the bead entering it.
	Terminal bool `json:"terminal,omitempty"`
	// Beads are the open beads in the stage, the newest change first.
	Beads []Card `json:"beads"`
	// Section is the id of the element that holds the stage's cards, and
	// the name of the stream's event that puts a card in it.
	Section string `json:"-"`
}

// placer says where on /flows a bead of one depot stands. The page and the
// stream both ask it, so a card the stream moves lands where a reload
// would show it.
type placer struct {
	depot string
	flows []store.DepotFlow
}

// section is the id of the element that holds b's card: the stage of the
// flow that governs b, or the depot's Elsewhere. A bead that pins its flow
// follows that one, not the depot's (as `yardr flow show` counts).
func (p placer) section(b *store.Bead) string {
	if store.PinnedFlow(b) == "" {
		own, typed := -1, -1
		for i, rf := range p.flows {
			switch rf.Type {
			case "":
				own = i
			case b.Type:
				typed = i
			}
		}
		if typed < 0 {
			typed = own
		}
		if typed >= 0 && slices.Contains(p.flows[typed].Flow.Stages, b.Stage) {
			return domID("at", p.depot, p.flows[typed].Type, b.Stage)
		}
	}
	return domID("off", p.depot)
}

// flows reads the yard for /flows.
func (s *Server) flows(ctx context.Context) (*Flows, error) {
	// The head first: a bead that moves while the page is read is then
	// moved again by the stream, never missed.
	head, err := s.st.Head(ctx)
	if err != nil {
		return nil, err
	}
	depots, err := s.st.Depots(ctx)
	if err != nil {
		return nil, err
	}
	routes, err := s.st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	store.SortRoutes(routes)
	groups, err := s.st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	sessions, err := s.st.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	manual := manualClaims(sessions)
	out := &Flows{Depots: []FlowsDepot{}, Head: head, rfs: [][]store.DepotFlow{}, groups: groups}
	for _, depot := range depots {
		beads, err := s.st.ListBeads(ctx, store.BeadFilter{Depot: depot.Name})
		if err != nil {
			return nil, err
		}
		fr := FlowsDepot{Name: depot.Name, Kind: depot.Kind, Flows: []FlowColumn{}, Elsewhere: []Card{}}
		// As on Home: a depot whose repository cannot be read still shows
		// its beads, and the other depots their flows.
		rfs, err := s.st.DepotFlows(ctx, depot.Name)
		if err != nil {
			fr.Error = err.Error()
		}
		out.rfs = append(out.rfs, rfs)
		for _, rf := range rfs {
			col := FlowColumn{Name: rf.Flow.Name, Type: rf.Type, Source: rf.Source.String(), Stages: []FlowStage{}}
			files := rf.StageFiles
			if rf.Source.InStore() {
				// A store flow's stage files are in the yard's home. One
				// that cannot be read is no reason to lose the page.
				files, _, _ = store.HomeStageFiles(s.home, rf.Flow)
			}
			for _, st := range rf.Flow.Stages {
				fs := FlowStage{Stage: st, StageFile: files[st], Next: []string{}, Terminal: rf.Flow.IsTerminal(st),
					Beads: []Card{}, Section: domID("at", depot.Name, rf.Type, st)}
				if next := rf.Flow.Transitions[st]; len(next) > 0 {
					fs.Next = next
				}
				// Routes may match on type; the depot's own flow is shown
				// for the default type, as `yardr flow show` does.
				probe := store.Bead{Stage: st, Type: cmp.Or(rf.Type, "task"), Depot: &depot.Name, Priority: store.DefaultPriority}
				if r := store.MatchRoute(routes, probe); r != nil {
					fs.Group, fs.Else = r.Worker(), r.Further()
					if i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == fs.Group }); i >= 0 && groups[i].Runner == "manual" {
						fs.Human, fs.Members = true, groups[i].Members
					}
				}
				col.Stages = append(col.Stages, fs)
			}
			fr.Flows = append(fr.Flows, col)
		}
		sections := map[string]*[]Card{}
		for i := range fr.Flows {
			for j := range fr.Flows[i].Stages {
				sections[fr.Flows[i].Stages[j].Section] = &fr.Flows[i].Stages[j].Beads
			}
		}
		p := placer{depot: depot.Name, flows: rfs}
		for _, b := range beads {
			c := card(&b, manual)
			dst, ok := sections[p.section(&b)]
			if c.Live, c.Placed = true, ok; !ok {
				dst = &fr.Elsewhere
			}
			*dst = append(*dst, c)
		}
		for _, cards := range sections {
			newestFirst(*cards)
		}
		newestFirst(fr.Elsewhere)
		out.Depots = append(out.Depots, fr)
	}
	return out, nil
}

func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	d, err := s.flows(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := Page{Template: "flows", Title: "Flows", Menu: "flows", Data: d, head: d.Head, stream: "card"}
	if panel(r) {
		// The yard panel: the same flows drawn as track, which reads
		// itself again on every event of the stream. The JSON is the
		// page's either way: the parameter shapes the HTML alone.
		if !wantsJSON(r.Header.Values("Accept")) {
			if err := s.panelMarks(r.Context(), d); err != nil {
				s.fail(w, r, err)
				return
			}
		}
		p.Template, p.Title, p.Menu, p.stream, p.Wide = "panel", "Yard", "yard", "seq", true
	}
	s.respond(w, r, p)
}

// placement is where the stream puts a bead's card on /flows: the card as
// the bead is now, and the section it stands in, "" for a bead the page no
// longer shows.
type placement struct {
	Card    Card
	Section string
}

// arrives are the events that put a bead into a stage: its card is shown
// arriving. Any other event changes the card where it is.
var arrives = []string{"created", "advanced", "reopened"}

// place says where e leaves its bead's card on /flows; ok is false for an
// event that changes no card. The card says how the bead is now, not how e
// left it, so replaying events is harmless: the last one of a bead leaves
// its card where a reload would.
func (s *Server) place(ctx context.Context, e store.Event) (p placement, ok bool) {
	// A note changes nothing a card shows.
	if e.Bead == nil || e.Kind == "noted" || !inFeed(e) {
		return p, false
	}
	b, err := s.st.GetBead(ctx, *e.Bead)
	if err != nil {
		return p, false
	}
	// The bead's own session says whether its claim is a manual group's;
	// one that cannot be read is taken for an agent's.
	manual := map[string]bool{}
	if b.Session != nil {
		if sess, err := s.st.GetSession(ctx, *b.Session); err == nil && isManual(*sess) {
			manual[sess.ID] = true
		}
	}
	p.Card = card(b, manual)
	if b.Status == "open" && b.Depot != nil && !store.NotWork[b.Type] {
		// A depot whose flows cannot be read has every bead in Elsewhere.
		rfs, _ := s.st.DepotFlows(ctx, *b.Depot)
		p.Section = placer{depot: *b.Depot, flows: rfs}.section(b)
		p.Card.Live, p.Card.Placed = true, p.Section != domID("off", *b.Depot)
		p.Card.Arrived = slices.Contains(arrives, e.Kind)
	}
	return p, true
}

// placed writes p to b as the messages that move the card on an open
// /flows. Each is an event named after an element of the page, which
// listens for it (templates/card.html, flows.html): first one named after
// the card, at which the card the page has takes itself out, then one
// named after the section, which puts the new card on top. A page that has
// no such card, or no such section, hears nothing.
//
// Neither carries the seq: the caller's message after them does. A client
// cut off between them then asks for the event again, and gets them all.
func (s *Server) placed(b *bytes.Buffer, seq uint64, p placement) {
	var html bytes.Buffer
	if p.Section != "" {
		if err := s.partials.ExecuteTemplate(&html, "card", p.Card); err != nil {
			s.log.Error("web: /events", "seq", seq, "err", err)
			return
		}
	}
	message(b, 0, p.Card.DOM(), []byte("left"))
	if p.Section != "" {
		message(b, 0, p.Section, html.Bytes())
	}
}

// Beads is the /beads page: the open beads, the newest change first.
type Beads struct {
	// Depot and Stage are the filters of the query string, "" for none.
	Depot string `json:"depot,omitempty"`
	Stage string `json:"stage,omitempty"`
	// All says the closed beads are listed too (?all=1), every one, as
	// `yardr bead list --all` lists them.
	All   bool      `json:"all,omitempty"`
	Beads []BeadRow `json:"beads"`
	// Depots and Stages are what the filters can be set to: the yard's depots,
	// and the stages that hold a listed bead (of Depot, when it is set).
	Depots []string `json:"depots"`
	Stages []string `json:"stages"`
	Head   uint64   `json:"head"`
}

// BeadRow is one bead of /beads: the bead as `yardr bead list --json`
// prints it, and beside its fields the two a card has that the bead alone
// does not say.
type BeadRow struct {
	store.Bead
	// Held is the bead's hold, by the name the cards of every other page
	// have it under.
	Held bool `json:"held,omitempty"`
	// Working is a bead an agent's or a command's session has claimed.
	Working bool `json:"working"`
}

// Card is the bead as the page shows it.
func (r BeadRow) Card() Card {
	c := card(&r.Bead, nil)
	c.Working = r.Working
	return c
}

// Link is the page with its filters set to depot and stage.
func (b Beads) Link(depot, stage string) string {
	q := url.Values{}
	if b.All {
		q.Set("all", "1")
	}
	if depot != "" {
		q.Set("depot", depot)
	}
	if stage != "" {
		q.Set("stage", stage)
	}
	if len(q) == 0 {
		return "/beads"
	}
	return "/beads?" + q.Encode()
}

// Self is the page as it was asked for, which it asks for again when
// something happens in the yard.
func (b Beads) Self() string { return b.Link(b.Depot, b.Stage) }

func (s *Server) handleBeads(w http.ResponseWriter, r *http.Request) {
	ctx, q := r.Context(), r.URL.Query()
	d := Beads{Depot: q.Get("depot"), Stage: q.Get("stage"), All: all(r), Beads: []BeadRow{}, Depots: []string{}, Stages: []string{}}
	var err error
	if d.Head, err = s.st.Head(ctx); err != nil {
		s.fail(w, r, err)
		return
	}
	// One read for the list and for the shell's line beside it.
	y, err := s.readYard(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, depot := range y.depots {
		d.Depots = append(d.Depots, depot.Name)
	}
	beads := y.beads
	if d.All {
		// The line beside the list still counts the open ones.
		if beads, err = s.st.ListBeads(ctx, store.BeadFilter{All: true}); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	manual := manualClaims(y.sessions)
	for _, b := range beads {
		if d.Depot != "" && !in(&b, d.Depot) {
			continue
		}
		d.Stages = append(d.Stages, b.Stage)
		if d.Stage == "" || b.Stage == d.Stage {
			d.Beads = append(d.Beads, BeadRow{Bead: b, Held: b.Hold, Working: working(&b, manual)})
		}
	}
	slices.Sort(d.Stages)
	d.Stages = slices.Compact(d.Stages)
	// The newest change first, as the cards of newestFirst.
	slices.SortStableFunc(d.Beads, func(a, b BeadRow) int {
		return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), cmp.Compare(a.ID, b.ID))
	})
	s.respond(w, r, Page{Template: "beads", Title: "Beads", Menu: "beads", Data: d, yard: y, head: d.Head, stream: "seq"})
}

// BeadPage is /beads/<id>: what `yardr prime --bead <id>` knows, for a
// person.
type BeadPage struct {
	// As is the user the view acts as, "" when it does not act: its asks
	// then answer with buttons, and its moves send the bead on.
	As   string      `json:"as,omitempty"`
	Bead *store.Bead `json:"bead"`
	// Working says an agent's or a command's session has claimed the bead:
	// a manual group's claim is a person's turn.
	Working bool `json:"working"`
	// Flow is the flow that governs the bead, absent when none does: the
	// bead may then move to any stage.
	Flow *BeadFlow `json:"flow,omitempty"`
	// Base is what the bead's branch starts from and lands on: its train's
	// branch, else the depot's base.
	Base string `json:"base,omitempty"`
	// Train is the train the bead is a child of; Children are a train's own.
	Train    *Card  `json:"train,omitempty"`
	Children []Card `json:"children,omitempty"`
	// Blockers are the open beads that must close before this one is
	// worked.
	Blockers []Card `json:"blockers,omitempty"`
	// DiscoveredFrom is the bead this one was found while working on;
	// LedTo are the beads found from this one, open and closed.
	DiscoveredFrom []Card `json:"discovered_from,omitempty"`
	LedTo          []Card `json:"led_to,omitempty"`
	// Deps are the bead's edges, both ways.
	Deps []store.Dep `json:"deps,omitempty"`
	// StageFile is what the flow says about the bead's stage.
	StageFile *store.StageFile `json:"stage_file,omitempty"`
	// Outcomes are how work in the stage can end; Next the stages the bead
	// may be moved to, and Terminal those of them that close it.
	Outcomes []BeadOutcome `json:"outcomes,omitempty"`
	Next     []string      `json:"next,omitempty"`
	Terminal []string      `json:"terminal,omitempty"`
	// Asks are the bead's open asks to the user the view acts as, oldest
	// first, as the socket's Bead reads them: the page answers them with
	// buttons. Empty without --as.
	Asks []store.BeadAsk `json:"asks,omitempty"`
	// Moves are what the user the view acts as may do with the bead, as
	// `yardr review` offers them: the outcomes its flow names for the
	// stage, else the stages it allows next. Empty without --as, and
	// when the bead waits for no manual group the user belongs to.
	Moves []MoveAction `json:"moves,omitempty"`
	// Error is why a POST did not write, back on the redrawn page.
	Error string `json:"error,omitempty"`
	// Notes are the bead's notes, the oldest first.
	Notes []store.Note `json:"notes"`
	// Missing are the hashes of the notes' images and pages the yard no
	// longer holds: the page draws those as a line saying so, never a
	// broken icon or link. HTML-only: the JSON names the files as the
	// notes have them.
	Missing map[string]bool `json:"-"`
	// Board is the bead's note pages in note order, each with the note
	// that carries it: the board above the notes. Mail pages stand on no
	// board. HTML-only like Missing: the JSON names the files as the
	// notes have them.
	Board []BoardEntry `json:"-"`
	// RunBase is the sandbox origin the page's run links open on, as the
	// reader reaches it, without a scheme. HTML-only, as Missing is.
	RunBase string `json:"-"`
	// Sessions are the sessions that worked the bead, the oldest first.
	Sessions []store.Session `json:"sessions"`
	// History is every event of the bead the log still has, the oldest
	// first, as `yardr bead history --json` prints it.
	History []Change `json:"history"`
	// Warnings say what could not be read: a flow that is broken, a train
	// that is ambiguous. The rest of the page still stands.
	Warnings []string `json:"warnings,omitempty"`
	Head     uint64   `json:"head"`
}

// BoardEntry is one of the bead's note pages with the note that carries
// it: the board is an arrangement over existing items, in note order,
// with each page's note beside it.
type BoardEntry struct {
	File store.BlobRef `json:"file"`
	Note store.Note    `json:"note"`
}

// Self is the page's own path, which it asks for again at every event of
// the yard: a blocker that closes or a child that moves changes the page
// as the bead's own events do.
func (p BeadPage) Self() string { return "/beads/" + url.PathEscape(p.Bead.ID) }

// BeadFlow is the flow governing a bead and where it comes from.
type BeadFlow struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// BeadOutcome is one way work in the bead's stage can end, with the
// command that reports it.
type BeadOutcome struct {
	Name    string `json:"name"`
	To      string `json:"to"`
	Command string `json:"command"`
}

// MoveAction is one way the view's user may send the bead on, as `yardr
// review` offers it: an outcome the flow names (done), or a stage it
// allows next (advance).
type MoveAction struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Outcome is the outcome a done action reports; To the stage either
	// enters.
	Outcome string `json:"outcome,omitempty"`
	To      string `json:"to"`
	// NoteRequired: sending a bead back needs a note saying what to
	// change, or whoever picks it up next has nothing to go on.
	NoteRequired bool `json:"note_required,omitempty"`
	// Terminal is a stage that closes the bead entering it.
	Terminal bool `json:"terminal,omitempty"`
}

func (s *Server) handleBead(w http.ResponseWriter, r *http.Request) {
	d, err := s.beadPage(r.Context(), r.PathValue("id"), r.Host)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respond(w, r, Page{Template: "bead", Title: d.Bead.ID, Menu: "beads", Data: d, head: d.Head, stream: "seq"})
}

// beadPage reads the bead's page: what `yardr prime --bead <id>` knows,
// for a person. The POST that wrote nothing reads it again with the
// reason, so the two never show different beads. hostport is the Host
// header the reader asked with, which the page's run links are built
// from.
func (s *Server) beadPage(ctx context.Context, id, hostport string) (*BeadPage, error) {
	d := &BeadPage{As: s.as, Notes: []store.Note{}, Sessions: []store.Session{}, RunBase: s.runBase(hostport)}
	var err error
	if d.Head, err = s.st.Head(ctx); err != nil {
		return nil, err
	}
	b, err := s.st.GetBead(ctx, id)
	if err != nil {
		return nil, err
	}
	d.Bead = b
	// The live sessions: the manual groups' claims among them say which
	// bead is not being worked. The bead's own, ended ones too, are read
	// by themselves below, so the page costs nothing for the sessions of
	// every other bead the yard keeps.
	sessions, err := s.st.Sessions(ctx, false)
	if err != nil {
		return nil, err
	}
	manual := manualClaims(sessions)
	d.Working = working(b, manual)
	// A card for each bead this one names; one that is gone is left out.
	cards := func(ids []string) []Card {
		var out []Card
		for _, id := range ids {
			if o, err := s.st.GetBead(ctx, id); err == nil {
				out = append(out, card(o, manual))
			}
		}
		return out
	}
	// A bead whose train is ambiguous or whose flow is broken is still
	// shown, with why (as `yardr prime` does).
	if fam, err := s.st.Family(ctx, b.ID); err != nil {
		d.Warnings = append(d.Warnings, err.Error())
	} else {
		d.Base, d.Children = fam.Base, cards(fam.Children)
		if train := cards([]string{fam.Train}); fam.Train != "" && len(train) == 1 {
			d.Train = &train[0]
		}
	}
	next, nerr := s.st.NextStages(ctx, b.ID)
	if nerr != nil {
		d.Warnings = append(d.Warnings, "no move is allowed until the flow is fixed: "+nerr.Error())
	} else if next.Flow != nil {
		d.Flow = &BeadFlow{Name: next.Flow.Name, Source: next.Source.String()}
		d.Next, d.Terminal, d.StageFile = next.Stages, next.Terminal, next.StageFile
		// A store flow's stage file is a file in the yard's home, which
		// the store does not read.
		if d.StageFile == nil && next.Source.InStore() {
			if d.StageFile, err = store.HomeStageFile(s.home, next.Flow.Name, b.Stage); err != nil {
				d.Warnings = append(d.Warnings, err.Error())
			}
		}
		for _, o := range next.Outcomes {
			cmd := "yardr bead done " + b.ID
			if o.Name != store.DefaultOutcome {
				cmd += " --outcome " + o.Name
			}
			d.Outcomes = append(d.Outcomes, BeadOutcome{Name: o.Name, To: o.To, Command: cmd})
		}
	}
	if s.as != "" {
		// What the view's user may do with the bead, as `yardr review`
		// offers it; none when it waits for no manual group they belong
		// to, or its flow names no move.
		if nerr == nil {
			if moves, err := s.moveActions(ctx, b, next); err != nil {
				return nil, err
			} else {
				d.Moves = moves
			}
		}
		if asks, err := s.openBeadAsks(ctx, b.ID); err != nil {
			return nil, err
		} else {
			d.Asks = asks
		}
	}
	blockers, err := s.st.OpenBlockers(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	d.Blockers = cards(blockers)
	if d.Deps, err = s.st.Deps(ctx, b.ID); err != nil {
		return nil, err
	}
	for _, e := range d.Deps {
		if e.Kind != store.DepDiscoveredFrom {
			continue
		}
		if e.To == b.ID {
			d.DiscoveredFrom = append(d.DiscoveredFrom, cards([]string{e.From})...)
		} else {
			d.LedTo = append(d.LedTo, cards([]string{e.To})...)
		}
	}
	notes, err := s.st.Notes(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(notes, func(a, b store.Note) int { return cmp.Compare(a.Seq, b.Seq) })
	d.Notes = append(d.Notes, notes...)
	// The page draws each note's images and pages below its words; one
	// the yard no longer holds is a line saying so, never a broken icon
	// or link. The board above the notes is the note pages alone, in the
	// same note order, each with its note beside it; mail is read
	// nowhere here, so mail pages stand on no board.
	for _, n := range d.Notes {
		for _, f := range n.Files {
			if f.IsPage() {
				d.Board = append(d.Board, BoardEntry{File: f, Note: n})
			}
			if _, err := s.st.NoteBlob(ctx, f.Hash); err != nil {
				if !errors.Is(err, store.ErrNotFound) {
					return nil, err
				}
				if d.Missing == nil {
					d.Missing = map[string]bool{}
				}
				d.Missing[f.Hash] = true
			}
		}
	}
	own, err := s.st.ListSessions(ctx, store.SessionFilter{All: true, Bead: b.ID})
	if err != nil {
		return nil, err
	}
	d.Sessions = append(d.Sessions, own...)
	if d.History, err = History(ctx, s.st, b.ID); err != nil {
		return nil, err
	}
	return d, nil
}

// pagePolicy is the policy a note's page opens under: the page is bytes an
// agent wrote, shown top-level and inert. The sandbox kills scripts, forms
// and plugins, and the fetch allowlist kills what the sandbox alone does
// not — the network and frames: inline styles and data: images are all
// that render beside the markup, so a self-contained page shows whole.
// The sandbox's opaque origin withholds the yard's API and cookies from
// the page, and no frame embeds it; what survives is the reader following
// a link out by clicking it. (docs/design/2026-10-14-yardr-x4t5-board.md,
// section 3.3.)
const pagePolicy = "sandbox allow-top-navigation-by-user-activation; default-src 'none'; script-src 'none'; connect-src 'none'; frame-src 'none'; child-src 'none'; object-src 'none'; font-src 'none'; media-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// handleBlob serves a note's image or page: the bytes of a blob a note on
// some bead names, with their type. Anything else — a mail's file, one the
// yard never held, a name that is no hash — answers as a missing blob:
// the view has no login, so it serves nothing but what its bead pages
// show. The check stands before the file: only a hash the yard holds
// reaches the directory. A page opens under the page policy, inert, which
// replaces the yard page's policy ServeHTTP set before the mux served; an
// image keeps that policy.
func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	ref, err := s.st.NoteBlob(r.Context(), hash)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if ref.IsPage() {
		w.Header().Set("Content-Security-Policy", pagePolicy)
	}
	serveBlob(w, r, s.home, hash, ref)
}

// runPolicy is the policy of the sandbox origin: every answer carries
// it, the run wrapper and the plain 404s alike. It runs a page's
// inline scripts and nothing else: no fetch, frames, workers or peer
// connections, and no way out anywhere — the wrapper's frame-src
// stops every navigation away, and every same-origin answer refuses
// framing. It carries no sandbox directive, so the wrapper keeps its
// origin for frame-src to name; the page inside is confined by the
// frame's own sandbox attribute and inherits this policy.
// (docs/design/2026-10-14-yardr-x4t5-board.md, section 3.5, as the
// approval on yardr-x4t5 decided it.)
const runPolicy = "default-src 'none'; script-src 'unsafe-inline'; connect-src 'none'; frame-src 'self'; child-src 'none'; worker-src 'none'; webrtc 'block'; object-src 'none'; font-src 'none'; media-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// runShim is the first script of every run page: it takes the WebRTC
// constructor, the one network API the policy cannot take from a page
// in a browser that does not enforce the webrtc directive (as of
// October 2026 none does). It stands before the page's own bytes, so
// it runs before any of their scripts, and nothing later can bring
// the constructor back: the page loads no frame, worker or popup of
// its own. It carries no "</script" but its own end, so the page's
// bytes cannot break out of it; parsing only moves forward.
const runShim = `<script>try{delete window.RTCPeerConnection;delete window.webkitRTCPeerConnection;}catch(e){}window.RTCPeerConnection=undefined;window.webkitRTCPeerConnection=undefined;</script>`

// runPage wraps one blob for running: the blob's bytes in a sandboxed
// srcdoc frame, under the shim, filling the window. The wrapper
// itself has no script and no link: there is nothing in it to run or
// to follow. Srcdoc is the page's bytes with the shim before them,
// escaped for the attribute by the template.
var runPage = template.Must(template.New("run").Parse(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>run {{.Short}}</title>
<style>html,body{margin:0;height:100%}iframe{border:0;width:100%;height:100%}</style>
</head>
<body>
<iframe srcdoc="{{.Srcdoc}}" sandbox="allow-scripts"></iframe>
</body>
</html>
`))

// handleRun serves a note's page for running, on the sandbox origin:
// the run wrapper around the same bytes the view serves. The hash is
// checked as the view's blob route checks it, and anything else answers
// plainly, as the origin answers no yard route.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if _, err := s.st.NoteBlob(r.Context(), hash); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "no such blob", http.StatusNotFound)
			return
		}
		if r.Context().Err() == nil {
			s.log.Error("pages: "+r.URL.Path, "err", err)
		}
		http.Error(w, "could not read the blob", http.StatusInternalServerError)
		return
	}
	body, err := os.ReadFile(store.BlobPath(s.home, hash))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "no such blob", http.StatusNotFound)
			return
		}
		if r.Context().Err() == nil {
			s.log.Error("pages: "+r.URL.Path, "err", err)
		}
		http.Error(w, "could not read the blob", http.StatusInternalServerError)
		return
	}
	// The shim runs first, before the page's own bytes; the doctype
	// before it keeps the page in standards mode, and a second doctype
	// of the page's own is ignored.
	srcdoc := "<!DOCTYPE html>" + runShim + string(body)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The wrapper is the binary's, not the blob's: a stored copy would
	// outlive the shim that fenced it.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_ = runPage.Execute(w, map[string]string{"Short": hash[:8], "Srcdoc": srcdoc})
}

// serveBlob serves a note's blob ref, held under hash: its bytes with
// their type. As the socket serves a blob: the bytes never change, so
// the name is all a cache needs to know.
func serveBlob(w http.ResponseWriter, r *http.Request, home, hash string, ref *store.BlobRef) {
	w.Header().Set("Content-Type", ref.Type)
	w.Header().Set("ETag", `"`+ref.Hash+`"`)
	http.ServeFile(w, r, store.BlobPath(home, hash))
}

// inc is one more than i: an ask's options are numbered from one, and the
// template ranges them from zero.
func inc(i int) int { return i + 1 }

// clock is a time as the pages show it: the time alone today, with the day
// before that. It is this machine's time, which is the reader's: the view
// is served on loopback.
func clock(t time.Time) string {
	at, now := t.Local(), time.Now()
	if at.Year() == now.Year() && at.YearDay() == now.YearDay() {
		return at.Format("15:04:05")
	}
	return at.Format("Jan 2 15:04")
}
