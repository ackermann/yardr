// Decisions over the socket: a person's open asks as a queue, what they
// have dealt with, and one bead whole, for a program that is not yardr
// (footplate). docs/api.md promises these calls; the test of internal/rpc
// holds the page to the server.
//
// The queue is a new read of its own: every open ask addressed to the
// person, on the yard's open beads in any stage, oldest ask first. It is
// not yardr review's queue, which walks beads in manual stages by bead
// age and stays as it is.
//
// The writes delegate to AddAnswer and AddNote: one implementation, not
// two. Every refusal is theirs, word for word.
package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// OpenAsk is one open ask as a row of a person's queue: the question in
// plain words, whose it is and how long it waits, with the bead it stands
// on. What the card shows beyond the row (context, options,
// recommendation) is read with Bead.
type OpenAsk struct {
	Bead      string `json:"bead"`
	BeadTitle string `json:"bead_title"`
	// Topic is the bead's first topic: label, "" when it has none.
	Topic string `json:"topic"`
	// Ask is the ask note's seq: what an answer names.
	Ask      uint64    `json:"ask"`
	Question string    `json:"question"`
	Asker    string    `json:"asker"`
	Since    time.Time `json:"since"`
}

// DecidedAsk is one ask the person answered, declined or put aside: the
// queue's row with what they did with it.
type DecidedAsk struct {
	Bead      string    `json:"bead"`
	BeadTitle string    `json:"bead_title"`
	Topic     string    `json:"topic"`
	Ask       uint64    `json:"ask"`
	Question  string    `json:"question"`
	Asker     string    `json:"asker"`
	Since     time.Time `json:"since"`
	// State is answered, put aside or declined.
	State string `json:"state"`
	// Option is the picked option's number, with its label; absent on a
	// words answer, an aside ask and a declined one.
	Option      int    `json:"option,omitempty"`
	OptionLabel string `json:"option_label,omitempty"`
	// Words went with the pick, are the words answer, or say why the
	// question is declined.
	Words string `json:"words,omitempty"`
	// ReturnAt is when a put-aside ask comes back; absent otherwise.
	ReturnAt *time.Time `json:"return_at,omitempty"`
}

// AskQueue is what a person has to decide and has dealt with.
type AskQueue struct {
	Open []OpenAsk `json:"open"` // every open ask, oldest first
	// Decided are the answered, declined and put-aside asks, newest
	// decision first.
	Decided []DecidedAsk `json:"decided"`
}

// UserAsks reads the person's queue: every open ask addressed to them, on
// the yard's open beads in any stage, oldest ask first, with what they
// have dealt with beside it. A name that is no user of the yard is
// refused, as UserInbox refuses it.
func (s *Store) UserAsks(ctx context.Context, name string) (*AskQueue, error) {
	if err := s.read(func(tx *bolt.Tx) error {
		_, err := getUser(tx, name)
		return err
	}); err != nil {
		return nil, err
	}
	// An ask remains open until answered, declined or returned. Closing its
	// bead does not answer it, so include closed beads in this queue too.
	beads, err := s.ListBeads(ctx, BeadFilter{All: true})
	if err != nil {
		return nil, err
	}
	now := now()
	q := &AskQueue{Open: []OpenAsk{}, Decided: []DecidedAsk{}}
	var decided []decidedByAnswer
	for _, b := range beads {
		notes, err := s.Notes(ctx, b.ID)
		if err != nil {
			return nil, err
		}
		topic := topicOf(b)
		for _, r := range ResolveAsks(notes, now) {
			if r.AskErr != nil || r.Ask.To != name {
				continue
			}
			if r.State == AskOpen {
				q.Open = append(q.Open, OpenAsk{
					Bead: b.ID, BeadTitle: b.Title, Topic: topic,
					Ask: r.Note.Seq, Question: r.Ask.Question,
					Asker: r.Note.Author, Since: r.Note.At,
				})
				continue
			}
			d := DecidedAsk{
				Bead: b.ID, BeadTitle: b.Title, Topic: topic,
				Ask: r.Note.Seq, Question: r.Ask.Question,
				Asker: r.Note.Author, Since: r.Note.At,
				State: r.State.String(),
			}
			seq := uint64(0)
			if c := r.Current; c != nil {
				d.Option, d.OptionLabel = c.Answer.Option, ""
				if d.Option >= 1 && d.Option <= len(r.Ask.Options) {
					d.OptionLabel = r.Ask.Options[d.Option-1].Label
				}
				d.Words = c.Answer.Words
				if r.State == AskAside {
					at := r.ReturnAt
					d.ReturnAt = &at
				}
				seq = c.Note.Seq
			}
			decided = append(decided, decidedByAnswer{d: d, seq: seq})
		}
	}
	slices.SortStableFunc(q.Open, func(a, b OpenAsk) int { return cmp.Compare(a.Ask, b.Ask) })
	// Newest decision first: the answer's note seq orders the decisions
	// as the ask's orders the queue.
	slices.SortStableFunc(decided, func(a, b decidedByAnswer) int { return cmp.Compare(b.seq, a.seq) })
	for _, d := range decided {
		q.Decided = append(q.Decided, d.d)
	}
	return q, nil
}

// decidedByAnswer is a decided ask with the deciding answer's note seq,
// which orders the Decided section.
type decidedByAnswer struct {
	d   DecidedAsk
	seq uint64
}

// topicOf is the bead's topic: its first topic: label, "" when it has
// none. The prefix stays on; the client shows it without.
func topicOf(b Bead) string {
	for _, l := range b.Labels {
		if strings.HasPrefix(l, "topic:") {
			return l
		}
	}
	return ""
}

// BeadNote is one of the bead's notes as a client shows them: every note
// but the asks and answers, which BeadAsk carries folded into their ask.
// Nobody reads the raw form.
type BeadNote struct {
	Seq    uint64    `json:"seq"`
	Kind   string    `json:"kind"` // note, journal or handoff
	Author string    `json:"author"`
	Text   string    `json:"text"`
	At     time.Time `json:"at"`
}

// AboutBead is one bead an ask is about, a step away from the card.
type AboutBead struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Stage string `json:"stage"`
}

// BeadAsk is one of the bead's asks with its options, recommendation and
// state, and what was decided, if anything.
type BeadAsk struct {
	Ask      uint64      `json:"ask"`
	To       string      `json:"to"`
	Asker    string      `json:"asker"`
	Since    time.Time   `json:"since"`
	Question string      `json:"question"`
	Context  []string    `json:"context"`
	Options  []AskOption `json:"options"` // none wants words
	// Recommend is the recommended option's number, with why in a line;
	// both absent when nothing is recommended.
	Recommend int         `json:"recommend,omitempty"`
	Why       string      `json:"why,omitempty"`
	About     []AboutBead `json:"about,omitempty"`
	// State is open, answered, put aside or declined.
	State string `json:"state"`
	// Option is the picked option's number, with any words; words alone
	// answer an ask with no options, or say why it is declined.
	Option int    `json:"option,omitempty"`
	Words  string `json:"words,omitempty"`
	// ReturnAt is when a put-aside ask comes back; absent otherwise.
	ReturnAt *time.Time `json:"return_at,omitempty"`
}

// BeadView is one bead whole: body, stage, labels, notes, its asks, and
// for each about-bead its id, title and stage.
type BeadView struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Body   string     `json:"body"`
	Stage  string     `json:"stage"`
	Labels []string   `json:"labels"`
	Notes  []BeadNote `json:"notes"`
	Asks   []BeadAsk  `json:"asks"`
}

// Bead reads one bead whole. An ask or answer that does not parse is left
// out, as the queue leaves it out: corrupt notes are yardr's own surfaces
// to show, not a client's.
func (s *Store) Bead(ctx context.Context, id string) (*BeadView, error) {
	b, err := s.GetBead(ctx, id)
	if err != nil {
		return nil, err
	}
	notes, err := s.Notes(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	v := &BeadView{
		ID: b.ID, Title: b.Title, Body: b.Body, Stage: b.Stage, Labels: b.Labels,
		Notes: []BeadNote{}, Asks: []BeadAsk{},
	}
	for _, n := range notes {
		if n.Kind == NoteAsk || n.Kind == NoteAnswer {
			continue
		}
		// The oldest notes carry no kind; they are plain notes.
		kind := n.Kind
		if kind == "" {
			kind = NoteNote
		}
		v.Notes = append(v.Notes, BeadNote{Seq: n.Seq, Kind: kind, Author: n.Author, Text: n.Text, At: n.At})
	}
	for _, r := range ResolveAsks(notes, now()) {
		if r.AskErr != nil {
			continue
		}
		a := BeadAsk{
			Ask: r.Note.Seq, To: r.Ask.To, Asker: r.Note.Author, Since: r.Note.At,
			Question: r.Ask.Question, Context: r.Ask.Context, Options: r.Ask.Options,
			Recommend: r.Ask.Recommend, Why: r.Ask.Why, State: r.State.String(),
		}
		if a.Options == nil {
			a.Options = []AskOption{}
		}
		for _, h := range r.Ask.About {
			about := AboutBead{ID: h}
			if other, err := s.GetBead(ctx, h); err == nil {
				about.Title, about.Stage = other.Title, other.Stage
			} else if !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			a.About = append(a.About, about)
		}
		if c := r.Current; c != nil {
			a.Option, a.Words = c.Answer.Option, c.Answer.Words
			if r.State == AskAside {
				at := r.ReturnAt
				a.ReturnAt = &at
			}
		}
		v.Asks = append(v.Asks, a)
	}
	return v, nil
}

// AnswerAsk records the person's answer on the bead: the picked option's
// number with any words, or words alone on an ask with no options. It is
// what yardr bead answer does with an option or --words: words alone on
// an ask with options are refused, and go as a plain note instead.
func (s *Store) AnswerAsk(ctx context.Context, id string, ask uint64, option int, words, actor string) error {
	return s.AddAnswer(ctx, id, Answer{Ask: ask, Option: option, Words: words}, actor)
}

// DeferAsk puts the ask aside: it leaves the person's queue for 24 hours
// or until the asker's next note on the bead, whichever is first. It is
// what yardr bead answer does with --later.
func (s *Store) DeferAsk(ctx context.Context, id string, ask uint64, actor string) error {
	return s.AddAnswer(ctx, id, Answer{Ask: ask, Later: true}, actor)
}

// DeclineAsk ends the ask with no option picked: the person will not
// decide it. It is what yardr bead answer does with --decline, and reads
// wherever the ask is shown as "<user> declined to decide this: <words>".
func (s *Store) DeclineAsk(ctx context.Context, id string, ask uint64, words, actor string) error {
	return s.AddAnswer(ctx, id, Answer{Ask: ask, Words: words, Decline: true}, actor)
}
