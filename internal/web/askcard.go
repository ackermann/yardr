package web

import (
	"context"
	"net/url"
	"time"

	"yardr.dev/yardr/internal/store"
)

// AskCard is one open ask as the ask partial renders it: the bead page's
// ask and a wait's ask reason read as one card in all three places. The
// bead page builds it from its stored ask, Home and the column from their
// reason; the partial never sees which.
type AskCard struct {
	Bead string
	Ask  uint64
	// OnBead is whether the card stands on its bead's page: it then opens
	// with the ask line and answers with the page's full form, giving and
	// approving where the gate allows. Away from it the card opens with
	// the head and answers with the row's form, which offers no giving or
	// approving.
	OnBead bool
	// Yard is the yard's name, Shade its chip shade (yardSlot): the head's
	// dot stands in the yard's colour, a number and never inline style.
	Yard  string
	Shade int
	// Topic is the bead's topic without its prefix, "" where it has none:
	// the head then reads Everything else, as footplate's does.
	Topic string
	// Weight is the card's weight, each class with the yard's line, absent
	// on a routine card.
	Weight []store.AskWeight
	// Question is the question; Age how long it waits, asked beside it.
	Question string
	Age      string
	// Asker is who asked; Talk is the conversation with them
	// (/talk?with=<asker>), "" where the viewing user may not write to
	// them.
	Asker string
	Talk  string
	// Context are the ask's context lines; Says names their speaker above
	// them on a run or setup card (<asker> says:).
	Context []string
	Says    bool
	// Options are the ask's options in order; Recommend the recommended
	// one's number with why in a line, both absent when nothing is
	// recommended.
	Options   []store.AskOption
	Recommend int
	Why       string
	// Secret, Run and Setup are the yard's blocks, each nil on any other
	// ask; Mapping is whether the ask maps an identity.
	Secret  *cardSecret
	Run     *cardRun
	Setup   *cardSetup
	Mapping bool
	// About are the other beads the ask is about; Held the held messages
	// the addressed person may read. Both stand on the bead's page alone.
	About []store.AboutBead
	Held  []string
	// User is who the run and setup sentences name: the ask's To, who an
	// approval would be called as.
	User string
	// Form is whether the answer form stands: the view acts. Rev is the
	// bead's revision the form carries; Action where it posts. Picks is
	// whether the option buttons stand, Words the words field, Answer the
	// words-alone button.
	Form   bool
	Rev    string
	Action string
	Picks  bool
	Words  bool
	Answer bool
}

// cardSecret is what a secret ask asks for, in the yard's words, with the
// names its giving takes values for.
type cardSecret struct {
	Line string
	Env  []string
}

// cardRun is the command a run ask proposes: whole, with the yard's
// sentences for what it does, the command that takes it back with who may
// run it, and the names its approval gives values for.
type cardRun struct {
	Command string
	Does    []string
	Back    string
	BackBy  string
	Ask     []string
}

// cardSetup is the setup a setup ask proposes: the pack, the yard's
// sentences, the take-back with who may run it, and the names its approval
// takes unseen beside the preset pick.
type cardSetup struct {
	Pack    string
	Does    []string
	Back    string
	BackBy  string
	Account []string
}

// cardSite is where a card stands: the yard for its head and sentences,
// and the crew and users the viewing user may write to one of.
type cardSite struct {
	Yard  string
	Shade int
	Crew  []store.CrewMember
	Users []store.User
}

// cardSite reads where the request's cards stand: the yard's name and chip
// shade, and the crew and users for the Ask about this link. The link
// stands only where the view acts, so a view that acts as no user reads no
// crew and no users.
func (s *Server) cardSite(ctx context.Context) (cardSite, error) {
	yard := s.yardName(ctx)
	site := cardSite{Yard: yard, Shade: yardSlot(yard)}
	if s.acting(ctx) == "" {
		return site, nil
	}
	var err error
	if site.Crew, err = s.st.Crew(ctx); err != nil {
		return cardSite{}, err
	}
	if site.Users, err = s.st.Users(ctx); err != nil {
		return cardSite{}, err
	}
	return site, nil
}

// talkTo is the conversation with the asker, "" where the viewing user may
// not write to them: the talk page's own check, which holds a conversation
// with a crew member or another user of the yard.
func (c cardSite) talkTo(as, asker string) string {
	if as == "" || !isTalkPeer(c.Crew, c.Users, as, asker) {
		return ""
	}
	return "/talk?with=" + url.QueryEscape(asker)
}

// Card is the bead page's ask as the ask partial renders it: under the
// page's own head, so with the ask line, and with the page's full form.
func (p BeadPage) Card(a store.BeadAsk) AskCard {
	out := AskCard{
		Bead: p.Bead.ID, Ask: a.Ask, OnBead: true,
		Yard: p.site.Yard, Shade: p.site.Shade,
		Weight: a.Weight, Question: a.Question, Age: age(time.Since(a.Since)),
		Asker: a.Asker, Talk: p.site.talkTo(p.As, a.Asker),
		Context: a.Context, Options: a.Options, Recommend: a.Recommend, Why: a.Why,
		Mapping: a.Identity != nil, About: a.About, Held: p.HeldPayloads[a.Ask], User: a.To,
		Form: true, Rev: p.Bead.Revision, Action: p.Self(),
		Picks: a.Setup == nil && (len(a.Weight) == 0 || a.Identity == nil),
		Words: a.Secret == nil && a.Run == nil && a.Setup == nil,
	}
	out.Answer = out.Words && len(a.Options) == 0
	out.Says = a.Run != nil || a.Setup != nil
	if a.Secret != nil {
		out.Secret = &cardSecret{Line: a.Secret.Line, Env: a.Secret.Env}
	}
	if a.Run != nil {
		out.Run = &cardRun{Command: a.Run.Command, Does: a.Run.Does, Back: a.Run.Back, BackBy: a.Run.BackBy, Ask: a.Run.Ask}
	}
	if a.Setup != nil {
		out.Setup = &cardSetup{Pack: a.Setup.Pack, Does: a.Setup.Does, Back: a.Setup.Back, BackBy: a.Setup.BackBy, Account: a.Setup.Account}
	}
	return out
}

// Card is the wait's ask reason as the ask partial renders it: away from
// its bead's page, so with the head, and with the row's form, which offers
// no giving or approving.
func (w Waiting) Card(r Reason) AskCard {
	out := AskCard{
		Bead: w.Bead, Ask: r.Ask,
		Yard: w.site.Yard, Shade: w.site.Shade, Topic: w.Topic,
		Weight: r.Weight, Question: r.Text, Age: age(time.Since(r.Since)),
		Asker: r.Asker, Talk: w.site.talkTo(w.As, r.Asker),
		Context: r.Context, Options: r.Options, Recommend: r.Recommend, Why: r.RecommendWhy,
		Mapping: r.Identity != nil, User: r.To,
		Form: w.As != "", Rev: w.Revision, Action: "/beads/" + url.PathEscape(w.Bead),
		Picks: len(r.Weight) == 0 || r.Identity == nil,
		Words: r.SecretLine == "",
	}
	out.Answer = out.Words && len(r.Options) == 0 && r.RunCommand == ""
	out.Says = r.RunCommand != "" || r.SetupPack != ""
	if r.SecretLine != "" {
		out.Secret = &cardSecret{Line: r.SecretLine}
	}
	if r.RunCommand != "" {
		out.Run = &cardRun{Command: r.RunCommand, Does: r.RunDoes, Back: r.RunBack, BackBy: r.RunBackBy}
	}
	if r.SetupPack != "" {
		out.Setup = &cardSetup{Pack: r.SetupPack, Does: r.SetupDoes, Back: r.SetupBack, BackBy: r.SetupBackBy}
	}
	return out
}

// FirstCard is the wait's oldest open ask as the ask partial renders it,
// nil when the bead carries no ask.
func (w Waiting) FirstCard() *AskCard {
	if r := w.FirstAsk(); r != nil {
		c := w.Card(*r)
		return &c
	}
	return nil
}
