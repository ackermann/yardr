package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"yardr.dev/yardr/internal/eta"
	"yardr.dev/yardr/internal/store"
)

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
	// ETA and Late are the bead's estimate, absent when there is nothing
	// to say; the same shapes `bead show --json` carries. The page shows
	// the ETA sentence and the late mark under the header.
	ETA  *eta.ETA  `json:"eta,omitempty"`
	Late *eta.Late `json:"late,omitempty"`
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
	// RoundsLines say how far into a round limit the bead is, one for each
	// limited outcome of its stage whose stage has rounds counted, as `yardr
	// bead show` prints them: "new 2 of 3 (the next changes from review goes
	// to new)".
	RoundsLines []string `json:"rounds_lines,omitempty"`
	// Asks are the bead's open asks to the user the view acts as, oldest
	// first, as the socket's Bead reads them: the page answers them with
	// buttons. Empty without --as.
	Asks []store.BeadAsk `json:"asks,omitempty"`
	// HeldPayloads are identity card bodies visible only to the addressed person.
	HeldPayloads map[uint64][]string `json:"-"`
	// site is where the page's cards stand, for the ask partial: the yard
	// and who the viewing user may write to. Set where the page carries
	// an ask.
	site cardSite
	// Moves are what the user the view acts as may do with the bead, as
	// `yardr review` offers them: the outcomes its flow names for the
	// stage, else the stages it allows next. Empty without --as, and
	// when the bead waits for no manual group the user belongs to.
	Moves []MoveAction `json:"moves,omitempty"`
	// Error is why a POST did not write, back on the redrawn page.
	Error string `json:"error,omitempty"`
	// Notes are the bead's notes, the oldest first.
	Notes []store.Note `json:"notes"`
	// Marks is whom the links trust as a user of this yard, by the
	// address notes name them with, for the mark the page carries on a
	// note's author: (as <user>) where trusted, the foreign line
	// otherwise. Nil when the trusts cannot be read, which reads
	// unmarked. HTML-only: the JSON names the author as the note has
	// it.
	Marks map[string]string `json:"-"`
	// Missing are the hashes of the notes' files the yard no longer
	// holds: the page draws those as a line saying so, never a broken
	// icon or link. HTML-only: the JSON names the files as the notes
	// have them.
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
	Name string `json:"name"`
	// To is where reporting the outcome takes this bead now: the stage a
	// round limit sends it to once its rounds are used, not the flow's own
	// edge.
	To      string `json:"to"`
	Command string `json:"command"`
	// Note says which round the report would be ("round 3 of 3") or that the
	// rounds are used, where a round limit is in play; absent otherwise.
	Note string `json:"note,omitempty"`
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
	as := s.acting(ctx)
	d := &BeadPage{As: as, Notes: []store.Note{}, Sessions: []store.Session{}, RunBase: s.runBase(hostport)}
	var err error
	if d.Head, err = s.st.Head(ctx); err != nil {
		return nil, err
	}
	// A bead the acting user may not see reads as missing, as the
	// socket's Bead reads it. The core lets everyone see every bead,
	// so today every page reads as before.
	if err := s.st.May(ctx, as, store.ActSee, store.Object{Kind: store.KindBead, Name: id}); err != nil {
		return nil, fmt.Errorf("bead %s: %w", id, store.ErrNotFound)
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
	// Their estimates are attached after, over one shared snapshot.
	cards := func(ids []string) []Card {
		var out []Card
		for _, id := range ids {
			if o, err := s.st.GetBead(ctx, id); err == nil {
				out = append(out, card(o, manual, nil))
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
			cmd := store.DoneCommand(b.ID, o.Name)
			d.Outcomes = append(d.Outcomes, BeadOutcome{Name: o.Name, To: o.Goes, Command: cmd, Note: o.RoundNote("")})
		}
		d.RoundsLines = next.RoundsLines()
	}
	if as != "" {
		// What the request's user may do with the bead, as `yardr review`
		// offers it; none when it waits for no manual group they belong
		// to, or its flow names no move.
		if nerr == nil {
			if moves, err := s.moveActions(ctx, b, next); err != nil {
				return nil, err
			} else {
				d.Moves = moves
			}
		}
		if err := s.loadBeadPageAsks(ctx, d, b.ID, as); err != nil {
			return nil, err
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
	// Whose word a note by a name of another yard is, as mail is
	// marked; with no grants when the trusts cannot be read, so every
	// name of another yard carries the foreign line.
	if peers, err := s.st.Peers(ctx); err != nil {
		s.log.Warn("web: bead page without trust grants", "bead", b.ID, "err", err)
		d.Marks = map[string]string{}
	} else {
		d.Marks = store.TrustedAs(peers)
	}
	// The page draws each note's files below its words: an image, a page's
	// links, a file's download link; one the yard no longer holds is a
	// line saying so, never a broken icon or link. The board above the
	// notes is the note pages alone, in the
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
	if d.History, err = History(ctx, s.st, b.ID, s.acting(ctx)); err != nil {
		return nil, err
	}
	// The bead's estimate and its cards', over one shared queue snapshot.
	if err := s.estimateBeadPage(ctx, d); err != nil {
		return nil, err
	}
	return d, nil
}

// estimateBeadPage carries the page's bead and the cards it names their
// estimates: one shared estimate over the page, so the queue behind them is
// read once.
func (s *Server) estimateBeadPage(ctx context.Context, d *BeadPage) error {
	ids := []string{d.Bead.ID}
	var cards []*Card
	if d.Train != nil {
		cards = append(cards, d.Train)
	}
	for _, list := range [][]Card{d.Children, d.Blockers, d.DiscoveredFrom, d.LedTo} {
		for i := range list {
			cards = append(cards, &list[i])
		}
	}
	for _, c := range cards {
		ids = append(ids, c.ID)
	}
	ests, err := s.estimates(ctx, s.st, ids, time.Now())
	if err != nil {
		return err
	}
	if est := ests[d.Bead.ID]; est != nil {
		d.ETA, d.Late = est.ETA, est.Late
	}
	for _, c := range cards {
		if est := ests[c.ID]; est != nil {
			c.ETA, c.Late = est.ETA, est.Late
		}
	}
	return nil
}
