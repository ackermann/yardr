package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"yardr.dev/yardr/internal/store"
)

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

// moveActions is what the view's user may do with b, as `yardr review`
// offers it: the outcomes its flow names for the stage, as they stand for
// the bead (a round limit can send the report somewhere the flow's edge
// does not name), else the stages it allows for this bead. Returning to a
// question's asker needs no new note because the answer is already on the
// bead. A bead that waits for no manual group the user belongs to, or a
// closed one, offers none. next is the bead's NextStages, read by the caller;
// a flow error leaves no move, as review's queue does.
func (s *Server) moveActions(ctx context.Context, b *store.Bead, next *store.NextStages) ([]MoveAction, error) {
	if b.Status != "open" {
		return nil, nil
	}
	routes, err := s.st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	store.SortRoutes(routes)
	settings, err := s.st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	operator := settings[store.OperatorSetting]
	groups, err := s.st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]store.Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
	}
	name := ""
	if b.Group != nil {
		name = *b.Group
	} else if r := store.MatchRoute(routes, *b, operator, byName); r != nil {
		name = r.Worker()
	}
	i := slices.IndexFunc(groups, func(g store.Group) bool { return g.Name == name })
	if i < 0 || groups[i].Runner != "manual" {
		return nil, nil
	}
	if members := groups[i].Members; len(members) > 0 && !slices.Contains(members, s.acting(ctx)) {
		return nil, nil
	}
	var out []MoveAction
	if f := next.Flow; f != nil {
		here := slices.Index(f.Stages, b.Stage)
		move := func(to string) MoveAction {
			a := MoveAction{Kind: "advance", To: to, Terminal: f.IsTerminal(to)}
			switch {
			case next != nil && to == next.Back:
				a.Label = "return to " + to
			case a.Terminal:
				a.Label = "finish as " + to + " (closes the bead)"
			case here >= 0 && slices.Index(f.Stages, to) < here:
				a.Label, a.NoteRequired = "send back to "+to+" (note required)", true
			default:
				a.Label = "move to " + to
			}
			return a
		}
		if outcomes := next.Outcomes; len(outcomes) > 0 {
			for _, o := range outcomes {
				a := move(o.Goes)
				a.Kind, a.Outcome, a.Label = "done", o.Name, o.Name+": "+a.Label
				if note := o.RoundNote(""); note != "" {
					a.Label += " (" + note + ")"
				}
				out = append(out, a)
			}
		} else {
			for _, to := range next.Stages {
				a := move(to)
				a.Label = strings.ToUpper(a.Label[:1]) + a.Label[1:]
				out = append(out, a)
			}
		}
		// A question can make a stage an additional move for this bead,
		// even when the stage's flow lists outcomes instead of bare moves.
		if next != nil && next.Back != "" && !slices.ContainsFunc(out, func(a MoveAction) bool {
			return a.To == next.Back
		}) {
			a := move(next.Back)
			a.Label = "Return to " + next.Back
			out = append(out, a)
		}
	} else {
		var stages []string
		for _, r := range routes {
			if r.Stage != b.Stage && !slices.Contains(stages, r.Stage) {
				stages = append(stages, r.Stage)
			}
		}
		slices.Sort(stages)
		for _, to := range stages {
			out = append(out, MoveAction{Kind: "advance", To: to, Label: "Move to " + to})
		}
	}
	return out, nil
}

// openBeadAsks are the bead's open asks to the user the request acts as,
// oldest first, as the socket's Bead reads them. The page shows the card's
// weight beside the question, so the yard's mark goes from the question
// where it carries it, as on the card: a line starting with the mark is
// the yard's.
func (s *Server) openBeadAsks(ctx context.Context, id string) ([]store.BeadAsk, error) {
	v, err := s.st.Bead(ctx, id, s.acting(ctx))
	if err != nil {
		return nil, err
	}
	var out []store.BeadAsk
	for _, a := range v.Asks {
		if a.State == "open" && a.To == s.acting(ctx) {
			a.Question = store.WithoutWeightMark(a.Question)
			out = append(out, a)
		}
	}
	return out, nil
}

// handleBeadPost takes the bead page's forms, and the needs rows' own:
// an ask's answer, or a move to the next stage. It is registered only on a
// view that acts (ActAs, or UseJWT for JWT mode): without either the mux
// answers 405 itself, as on every other route. A write answered, the page
// lands on the bead; a write refused re-reads the bead with the reason, as
// 200 so a form puts it in place.
func (s *Server) handleBeadPost(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); !ownOrigin(origin, r.Host) {
		http.Error(w, "yardr web takes answers and moves only from its own pages: the request's origin is "+quoted(origin)+", not this view's. A page on another site cannot answer or move as this view's user",
			http.StatusForbidden)
		return
	}
	ctx := r.Context()
	id := r.PathValue("id")
	b, err := s.st.GetBead(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The revision the page was drawn from: a bead that changed since
	// (another tab answered, a note came in) is read again instead of
	// written to.
	rev := r.PostFormValue("rev")
	if rev == "" || rev != b.Revision {
		s.beadAgain(w, r, b.ID, fmt.Sprintf("bead %s is at revision %s, not %s; read it again", b.ID, b.Revision, revOrNone(rev)))
		return
	}
	if outcome, to := r.PostFormValue("outcome"), r.PostFormValue("to"); outcome != "" || to != "" {
		s.beadMove(w, r, b, outcome, to, r.PostFormValue("note"), rev)
		return
	}
	s.beadAnswer(w, r, b, rev)
}

// beadMove reports the outcome, or advances to the stage, as `yardr review`
// does: with the note, from the bead's stage and revision, as the request's
// user. The move must be one the page offers: a bead that waits for
// another group, or an outcome its flow does not name, is refused.
func (s *Server) beadMove(w http.ResponseWriter, r *http.Request, b *store.Bead, outcome, to, note, rev string) {
	ctx := r.Context()
	next, err := s.st.NextStages(ctx, b.ID)
	if err != nil {
		s.beadAgain(w, r, b.ID, "no move is allowed until the flow is fixed: "+err.Error())
		return
	}
	moves, err := s.moveActions(ctx, b, next)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var want *MoveAction
	for i := range moves {
		m := &moves[i]
		if outcome != "" && m.Kind == "done" && m.Outcome == outcome {
			want = m
		}
		if outcome == "" && m.Kind == "advance" && m.To == to {
			want = m
		}
	}
	if want == nil {
		s.beadAgain(w, r, b.ID, "bead "+b.ID+" does not wait for "+s.acting(ctx)+" with that move; read it again")
		return
	}
	if want.NoteRequired && strings.TrimSpace(note) == "" {
		s.beadAgain(w, r, b.ID, "sending back to "+want.To+" needs a note saying what should change")
		return
	}
	exp := store.Expect{Stage: b.Stage, Revision: rev}
	var moved *store.Bead
	if want.Kind == "done" {
		moved, err = s.st.Done(ctx, b.ID, want.Outcome, note, exp, s.acting(ctx))
	} else {
		moved, err = s.st.AdvanceNote(ctx, b.ID, want.To, note, exp, s.acting(ctx))
	}
	if err != nil {
		s.beadAgain(w, r, b.ID, err.Error())
		return
	}
	http.Redirect(w, r, "/beads/"+url.PathEscape(moved.ID), http.StatusSeeOther)
}

// beadAnswer answers the form's ask through AnswerAsk, DeferAsk,
// DeclineAsk or GiveSecret, or approves its run or setup card, as the
// request's user. The ask must be open to them: one answered already, or
// asked of another, is refused.
func (s *Server) beadAnswer(w http.ResponseWriter, r *http.Request, b *store.Bead, rev string) {
	ctx := r.Context()
	seq, err := strconv.ParseUint(r.PostFormValue("ask"), 10, 64)
	if err != nil || seq == 0 {
		s.beadAgain(w, r, b.ID, "an answer needs the ask")
		return
	}
	asks, err := s.openBeadAsks(ctx, b.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	i := slices.IndexFunc(asks, func(a store.BeadAsk) bool { return a.Ask == seq })
	if i < 0 {
		s.beadAgain(w, r, b.ID, fmt.Sprintf("ask %d on bead %s is answered already, or asked of another; read it again", seq, b.ID))
		return
	}
	// An approval carries its preset pick as an option too: it is read by
	// the approval, not as a pick.
	if r.PostFormValue("action") == "approve" {
		s.beadApprove(w, r, b, asks[i])
		return
	}
	if r.PostFormValue("action") == "send" {
		ask := asks[i]
		if len(ask.Options) == 0 || ask.Secret != nil || ask.Run != nil || ask.Setup != nil {
			s.beadAgain(w, r, b.ID, fmt.Sprintf("ask %d does not take words as a note", seq))
			return
		}
		words := r.PostFormValue("words")
		if strings.TrimSpace(words) == "" {
			s.beadAgain(w, r, b.ID, "a note needs words")
			return
		}
		// A choice ask can take words as a note without closing: the
		// answer is still the person's pick, so a note leaves it open.
		if err := s.st.AddNote(ctx, b.ID, words, s.acting(ctx)); err != nil {
			s.beadAgain(w, r, b.ID, err.Error())
			return
		}
		http.Redirect(w, r, "/beads/"+url.PathEscape(b.ID), http.StatusSeeOther)
		return
	}
	answer := store.Answer{Ask: seq, Words: r.PostFormValue("words")}
	if opt := r.PostFormValue("option"); opt != "" {
		n, err := strconv.Atoi(opt)
		if err != nil {
			s.beadAgain(w, r, b.ID, "an option is a number, not "+opt)
			return
		}
		answer.Option = n
		// A weighty mapping card's pick writes a user's identity: a weighty
		// approval, which goes on only with the ask's number typed back, and
		// then with its person's word, as the terminal's pick does. The
		// yard weighs the pick again in its write.
		if ask := asks[i]; ask.Identity != nil && len(ask.Weight) > 0 {
			if !typedBack(r, ask) {
				s.beadAgain(w, r, b.ID, notTypedBack(ask))
				return
			}
			if err := s.st.AnswerAsk(ctx, b.ID, seq, n, answer.Words, s.acting(ctx), true); err != nil {
				s.beadAgain(w, r, b.ID, err.Error())
				return
			}
			http.Redirect(w, r, "/beads/"+url.PathEscape(b.ID), http.StatusSeeOther)
			return
		}
	} else {
		switch r.PostFormValue("action") {
		case "answer":
			// Words alone answer an ask that offers none.
		case "later":
			answer.Later = true
		case "decline":
			answer.Decline = true
		case "give":
			s.beadGiveSecret(w, r, b, asks[i])
			return
		default:
			s.beadAgain(w, r, b.ID, "pick an option, answer with words, put it aside with Later, or decline it")
			return
		}
	}
	if err := s.st.AddAnswerExpected(ctx, b.ID, answer, s.acting(ctx), rev); err != nil {
		s.beadAgain(w, r, b.ID, err.Error())
		return
	}
	http.Redirect(w, r, "/beads/"+url.PathEscape(b.ID), http.StatusSeeOther)
}

// beadGiveSecret gives the secret ask asks for through GiveSecret, as the
// request's user: each name the ask asks for is read from its own field,
// which does not echo. The values land in the file the source's credential
// is read from, and the answer on the bead holds the names that were given
// and never a value, as a terminal giving does. An ask that asks for no
// secret, and a name with no value, are refused with the page redrawn, and
// neither refusal repeats a value that was sent.
//
// Every secret ask is weighty, and the page, which may be plain HTTP,
// takes no secret behind its gate: it says nothing of its person's having
// weighed it, the yard refuses the giving in its own words, which the page
// shows, and the card is given in a terminal as its line names.
func (s *Server) beadGiveSecret(w http.ResponseWriter, r *http.Request, b *store.Bead, ask store.BeadAsk) {
	ctx := r.Context()
	if ask.Secret == nil {
		s.beadAgain(w, r, b.ID, fmt.Sprintf("ask %d asks for no secret: pick an option, answer with words, put it aside with Later, or decline it", ask.Ask))
		return
	}
	values := make(map[string]string, len(ask.Secret.Env))
	for _, name := range ask.Secret.Env {
		values[name] = r.PostFormValue("secret-" + name)
		if values[name] == "" {
			s.beadAgain(w, r, b.ID, fmt.Sprintf("ask %d asks for %s: %s was not given", ask.Ask, strings.Join(ask.Secret.Env, " and "), name))
			return
		}
	}
	if _, err := s.st.GiveSecret(ctx, b.ID, ask.Ask, values, s.acting(ctx), false); err != nil {
		s.beadAgain(w, r, b.ID, err.Error())
		return
	}
	http.Redirect(w, r, "/beads/"+url.PathEscape(b.ID), http.StatusSeeOther)
}

// beadAgain re-reads the bead page after a POST that wrote nothing, with
// the reason on the redrawn page.
func (s *Server) beadAgain(w http.ResponseWriter, r *http.Request, id, why string) {
	d, err := s.beadPage(r.Context(), id, r.Host)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Error = why
	s.respond(w, r, Page{Template: "bead", Title: id, Menu: "beads", Data: d, head: d.Head, stream: "seq"})
}

// revOrNone is the revision a form carried as the refusal names it: what
// a request without one sent.
func revOrNone(rev string) string {
	if rev == "" {
		return "none"
	}
	return rev
}
