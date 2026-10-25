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
// The writes delegate to AddAnswer, TakeBackAsk and AddNote: one
// implementation, not two. Every refusal is theirs, word for word. A secret ask is answered by
// GiveSecret (secretask.go), which the listener serves to a device of the
// operator and to nobody else. A run ask is approved by ApproveAsk
// (runask.go), which the listener serves likewise: a client reads the
// command and the yard's words for it here.
package store

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/eta"
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
	// Secret is what a secret ask asks for, in the yard's words; absent on
	// any other ask.
	Secret *SecretAsked `json:"secret,omitempty"`
	// Concealed says whether a secret ask's value is drawn hidden: false
	// where the source marks every asked name as no secret, absent
	// (concealed) on any other ask.
	Concealed *bool `json:"concealed,omitempty"`
	// Run is the command a run ask proposes, with the yard's words for
	// what it changes; absent on any other ask.
	Run *RunAsked `json:"run,omitempty"`
	// Setup is the setup a setup ask proposes, with the yard's words for
	// what approving it does; absent on any other ask.
	Setup *SetupAsked `json:"setup,omitempty"`
	// Identity is what a mapping ask maps; absent on any other ask.
	Identity *IdentityAsked `json:"identity,omitempty"`
	// Weight is the card's weight, each class with the yard's line, as
	// the yard weighed it when it wrote the card; absent on a routine
	// card.
	Weight []AskWeight `json:"weight,omitempty"`
}

// DecidedAsk is one ask the person answered, declined or put aside, or its
// asker took back: the queue's row with what was done with it.
type DecidedAsk struct {
	Bead      string    `json:"bead"`
	BeadTitle string    `json:"bead_title"`
	Topic     string    `json:"topic"`
	Ask       uint64    `json:"ask"`
	Question  string    `json:"question"`
	Asker     string    `json:"asker"`
	Since     time.Time `json:"since"`
	// State is answered, put aside, declined or taken back.
	State string `json:"state"`
	// Option is the picked option's number, with its label; absent on a
	// words answer, an aside ask, a declined one and one taken back.
	Option      int    `json:"option,omitempty"`
	OptionLabel string `json:"option_label,omitempty"`
	// Words went with the pick, are the words answer, or say why the
	// question is declined or taken back.
	Words string `json:"words,omitempty"`
	// By is who took the ask back, its asker or the operator, which reads
	// "taken back by <by>: <words>"; absent on any other state, and on a
	// question another yard took back, whose words say where.
	By string `json:"by,omitempty"`
	// ReturnAt is when a put-aside ask comes back; absent otherwise.
	ReturnAt *time.Time `json:"return_at,omitempty"`
	// Secret is what a secret ask asks for; Given are the names it was
	// given values for, which reads "gave <names>". Both absent on any
	// other ask, and Given until it is given.
	Secret *SecretAsked `json:"secret,omitempty"`
	// Concealed says whether a secret ask's value is drawn hidden: false
	// where the source marks every asked name as no secret, absent
	// (concealed) on any other ask.
	Concealed *bool    `json:"concealed,omitempty"`
	Given     []string `json:"given,omitempty"`
	// Run is the command a run ask proposes; Ran is the command the yard
	// ran when it was approved, with what it printed, which reads
	// "approved, ran: <command>". Both absent on any other ask, and Ran
	// until it is approved.
	Run *RunAsked `json:"run,omitempty"`
	Ran *AskRan   `json:"ran,omitempty"`
	// Setup is the setup a setup ask proposes, in the yard's words: such
	// an ask is approved by ApproveSetup with the preset pick and the
	// account together. Absent on any other ask.
	Setup *SetupAsked `json:"setup,omitempty"`
	// Identity is what a mapping ask maps; absent on any other ask.
	Identity *IdentityAsked `json:"identity,omitempty"`
	// Weight is the card's weight, each class with the yard's line, as
	// the yard weighed it when it wrote the card; absent on a routine
	// card.
	Weight []AskWeight `json:"weight,omitempty"`
}

// AskQueue is what a person has to decide and has dealt with.
type AskQueue struct {
	Open []OpenAsk `json:"open"` // every open ask, oldest first
	// Decided are the answered, declined, put-aside and taken-back asks,
	// newest decision first.
	Decided []DecidedAsk `json:"decided"`
}

// UserAsks reads the person's queue: every open ask addressed to them, on
// beads the viewer may see in any stage and from the linked yards, oldest
// first, with what they have dealt with beside it. Only the user reads
// their queue, and an operator any user's (mayReadInbox); actor is who
// reads, and left out is the user. An omitted viewer is the yard itself.
// A foreign ask stands as
// bead@peer, asked by name@peer, with since as sent; it is placed by the
// time this yard took it, never by the sender's clock, and the local asks
// keep their note-seq order. One the person answered, put aside or
// declined here is among the decided from then on, by what this yard knows
// of it (foreignAsk), placed by when they did. A name that is no user of
// the yard is refused, as UserInbox refuses it.
func (s *Store) UserAsks(ctx context.Context, name, actor string, viewer ...string) (*AskQueue, error) {
	if err := s.read(func(tx *bolt.Tx) error {
		if _, err := getUser(tx, name); err != nil {
			return err
		}
		_, err := s.mayReadInbox(ctx, tx, name, actor, "read")
		return err
	}); err != nil {
		return nil, err
	}
	// An ask remains open until answered, declined or returned. Closing its
	// bead does not answer it, so include closed beads in this queue too.
	beads, err := s.ListBeads(ctx, BeadFilter{All: true, Viewer: firstViewer(viewer)})
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
					Asker: r.Note.Author, Since: r.Note.At, Secret: r.Ask.asked(), Concealed: r.Ask.concealed(), Run: r.Ask.proposed(), Setup: r.Ask.setup(), Identity: r.Ask.identityAsked(), Weight: r.Ask.Weight,
				})
				continue
			}
			d := DecidedAsk{
				Bead: b.ID, BeadTitle: b.Title, Topic: topic,
				Ask: r.Note.Seq, Question: r.Ask.Question,
				Asker: r.Note.Author, Since: r.Note.At,
				State: r.State.String(), Secret: r.Ask.asked(), Concealed: r.Ask.concealed(), Run: r.Ask.proposed(), Setup: r.Ask.setup(), Identity: r.Ask.identityAsked(), Weight: r.Ask.Weight,
			}
			// An ask that is not open has the answer its state is from.
			d.decidedBy(r)
			decided = append(decided, decidedByAnswer{d: d, seq: r.Current.Note.Seq, at: r.Current.Note.At})
		}
	}
	slices.SortStableFunc(q.Open, func(a, b OpenAsk) int { return cmp.Compare(a.Ask, b.Ask) })
	foreign, foreignDecided, err := s.foreignAsks(name, now)
	if err != nil {
		return nil, err
	}
	q.Open = mergeOpen(q.Open, foreign)
	// Newest decision first: the answer's note seq orders the decisions
	// as the ask's orders the queue.
	slices.SortStableFunc(decided, func(a, b decidedByAnswer) int { return cmp.Compare(b.seq, a.seq) })
	// And a foreign one among them by when it was decided here.
	slices.SortStableFunc(foreignDecided, func(a, b decidedByAnswer) int { return b.at.Compare(a.at) })
	for len(decided) > 0 || len(foreignDecided) > 0 {
		from := &decided
		if len(decided) == 0 || len(foreignDecided) > 0 && foreignDecided[0].at.After(decided[0].at) {
			from = &foreignDecided
		}
		q.Decided = append(q.Decided, (*from)[0].d)
		*from = (*from)[1:]
	}
	return q, nil
}

// decidedBy fills in what was done with the ask r, which is not open: the
// pick with its label, the words, and when a put-aside one comes back.
func (d *DecidedAsk) decidedBy(r ResolvedAsk) {
	c := r.Current
	d.Option, d.OptionLabel = c.Answer.Option, ""
	if d.Option >= 1 && d.Option <= len(r.Ask.Options) {
		d.OptionLabel = r.Ask.Options[d.Option-1].Label
	}
	d.Words, d.Given, d.Ran = c.Answer.Words, c.Answer.Given, c.Answer.Ran
	if r.State == AskTakenBack {
		d.By = c.Note.Author
	}
	if r.State == AskAside {
		at := r.ReturnAt
		d.ReturnAt = &at
	}
}

// foreignOpen is one foreign ask as a row of the queue, with when this
// yard took it, which orders it among the local asks.
type foreignOpen struct {
	row   OpenAsk
	taken time.Time
}

// foreignAsks reads the person's foreign asks at the time at: every
// question another yard asked them that is open, oldest taken first, and
// those they have dealt with here. The peer part is the link's name today,
// from its key, so a rename follows; a question whose link is gone is left
// out, as a queue must stay answerable.
//
// A decided one is what this yard knows of it: the answer queued home,
// which stands here from when it was given, the asking yard's word that
// the ask ended there (foreignAsk.Outcome), said in the row's words under
// the state it ended in, since nothing else of it crossed, or that yard's
// word that it was put aside there, which stands among the decided with
// its return until it comes back.
func (s *Store) foreignAsks(name string, at time.Time) ([]foreignOpen, []decidedByAnswer, error) {
	var out []foreignOpen
	var decided []decidedByAnswer
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bPeerAsks, "", func(_ []byte, rec *foreignAsk) error {
			if rec.To != name {
				return nil
			}
			p, err := peerByKey(tx, rec.Key)
			if err != nil || p == nil {
				return err
			}
			row := rec.row(p)
			d := DecidedAsk{
				Bead: row.Bead, BeadTitle: row.BeadTitle, Topic: row.Topic,
				Ask: row.Ask, Question: row.Question, Asker: row.Asker, Since: row.Since,
			}
			r := rec.resolved(row.Bead, row.Asker, at)
			switch {
			case rec.Outcome != "":
				d.State = outcomeState(rec.Outcome).String()
				d.Words = outcomeWords(rec.Outcome, p.Name)
				decided = append(decided, decidedByAnswer{d: d, at: *rec.OutcomeAt})
			case r.State == AskOpen && rec.asided(at):
				d.State = AskAside.String()
				d.Words = asideWords(p.Name, *rec.AsideUntil)
				d.ReturnAt = rec.AsideUntil
				decided = append(decided, decidedByAnswer{d: d, at: *rec.AsideAt})
			case r.State == AskOpen:
				out = append(out, foreignOpen{row: row, taken: rec.TakenAt})
			default:
				d.State = r.State.String()
				d.decidedBy(r)
				decided = append(decided, decidedByAnswer{d: d, at: r.Current.Note.At})
			}
			return nil
		})
	})
	if err != nil {
		return nil, nil, err
	}
	slices.SortStableFunc(out, func(a, b foreignOpen) int {
		if a.taken.Equal(b.taken) {
			if a.row.Bead != b.row.Bead {
				return strings.Compare(a.row.Bead, b.row.Bead)
			}
			return cmp.Compare(a.row.Ask, b.row.Ask)
		}
		if a.taken.Before(b.taken) {
			return -1
		}
		return 1
	})
	return out, decided, nil
}

// mergeOpen unions the local asks, in their note-seq order, with the
// foreign asks, oldest taken first, by when this yard has them: a local
// ask by when it was written here, a foreign one by when it was taken.
// Both clocks are this yard's, never the sender's.
func mergeOpen(local []OpenAsk, foreign []foreignOpen) []OpenAsk {
	if len(foreign) == 0 {
		return local
	}
	out := make([]OpenAsk, 0, len(local)+len(foreign))
	i, j := 0, 0
	for i < len(local) && j < len(foreign) {
		if !foreign[j].taken.Before(local[i].Since) {
			out = append(out, local[i])
			i++
			continue
		}
		out = append(out, foreign[j].row)
		j++
	}
	out = append(out, local[i:]...)
	for _, f := range foreign[j:] {
		out = append(out, f.row)
	}
	return out
}

// decidedByAnswer is a decided ask with the deciding answer's note seq,
// which orders the Decided section, and when it was decided, which places
// a foreign ask among the local ones.
type decidedByAnswer struct {
	d   DecidedAsk
	seq uint64
	at  time.Time
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
	// Via is the device the note was written through, "" for none.
	Via string `json:"via,omitempty"`
	// Files are the files the note carries; absent on a note without
	// any. `GET /blobs/<hash>` answers the bytes.
	Files []BlobRef `json:"files,omitempty"`
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
	// State is open, answered, put aside, declined or taken back.
	State string `json:"state"`
	// Option is the picked option's number, with any words; words alone
	// answer an ask with no options, or say why it is declined or taken
	// back.
	Option int    `json:"option,omitempty"`
	Words  string `json:"words,omitempty"`
	// By is who took the ask back; absent on any other state.
	By string `json:"by,omitempty"`
	// ReturnAt is when a put-aside ask comes back; absent otherwise.
	ReturnAt *time.Time `json:"return_at,omitempty"`
	// PicksBefore is how many picks there were before the current one, so
	// a client can say "changed"; absent when there were none.
	PicksBefore int `json:"picks_before,omitempty"`
	// Secret is what a secret ask asks for, in the yard's words: such an
	// ask has no options and takes no words, and is answered by GiveSecret.
	// Given are the names it was given values for, which reads "gave
	// <names>". Both absent on any other ask, and Given until it is given.
	Secret *SecretAsked `json:"secret,omitempty"`
	// Concealed says whether a secret ask's value is drawn hidden: false
	// where the source marks every asked name as no secret, absent
	// (concealed) on any other ask.
	Concealed *bool    `json:"concealed,omitempty"`
	Given     []string `json:"given,omitempty"`
	// Run is the command a run ask proposes, in the yard's words: such an
	// ask has no options and takes no words, and is approved by ApproveAsk.
	// Ran is the command the yard ran when it was approved, with what it
	// printed, which reads "approved, ran: <command>". Both absent on any
	// other ask, and Ran until it is approved.
	Run *RunAsked `json:"run,omitempty"`
	Ran *AskRan   `json:"ran,omitempty"`
	// Setup is the setup a setup ask proposes, in the yard's words: such
	// an ask is approved by ApproveSetup with the preset pick and the
	// account together. Absent on any other ask.
	Setup *SetupAsked `json:"setup,omitempty"`
	// Identity is what a mapping ask maps; absent on any other ask.
	Identity *IdentityAsked `json:"identity,omitempty"`
	// Weight is the card's weight, each class with the yard's line, as
	// the yard weighed it when it wrote the card; absent on a routine
	// card.
	Weight []AskWeight `json:"weight,omitempty"`
}

// BeadView is one bead whole: body, stage, labels, notes, its asks, and
// for each about-bead its id, title and stage.
type BeadView struct {
	ID     string     `json:"id"`
	Title  string     `json:"title"`
	Body   string     `json:"body"`
	Stage  string     `json:"stage"`
	Topic  string     `json:"topic"`
	Labels []string   `json:"labels"`
	Notes  []BeadNote `json:"notes"`
	Asks   []BeadAsk  `json:"asks"`
	// Rounds is Bead.Rounds: the stages the bead has reached, each with the
	// times an outcome brought it back to it. Absent on a bead from before
	// the record, and on one of another yard's.
	Rounds map[string]int `json:"rounds,omitempty"`
	// Moves is the bead's recorded stage-move history, oldest first: its
	// advanced events still in the log, at most beadMovesCap of them.
	// Absent where none remain, and on one of another yard's.
	Moves []BeadMove `json:"moves,omitempty"`
	// Now is the live session and latest recorded activity, when either
	// remains. Absent on one of another yard's.
	Now *BeadNow `json:"now,omitempty"`
	// ETA and Late are the bead's estimate, absent when there is nothing
	// to say; the same shapes bead show --json carries.
	ETA  *eta.ETA  `json:"eta,omitempty"`
	Late *eta.Late `json:"late,omitempty"`
}

// BeadMove is one stage move of a bead: the stages it left and entered
// and when, with the outcome that moved it where one did, and the stage
// a round limit kept it from where one did (instead_of). Every field is
// in the advanced event's told set (eventDataTold), so a move is told to
// any viewer whole, with no rpc-layer filtering.
type BeadMove struct {
	From      string    `json:"from"`
	To        string    `json:"to"`
	At        time.Time `json:"at"`
	Outcome   string    `json:"outcome,omitempty"`
	InsteadOf string    `json:"instead_of,omitempty"`
}

// beadMovesCap is how many of a bead's stage moves Bead serves: the
// newest that many, oldest first. Past that it is a log, not a glance.
const beadMovesCap = 100

// BeadNow is who works a bead now: the live session working it, if one
// is, and the bead's latest activity if its event remains in the log.
// The session's handle and error never leave: a handle names the yard's
// machine, and an error names what failed on it. No terminal text
// crosses either: the activity is a bare kind and time ("noted 2m ago"),
// and content stays in Notes under its redaction.
type BeadNow struct {
	Session  *BeadSession  `json:"session,omitempty"`
	Activity *BeadActivity `json:"activity,omitempty"`
}

// BeadSession is the live session working a bead, as Bead tells it: the
// group, the harness kind and model it started with, its state, and
// since when. The handle and the error are not served.
type BeadSession struct {
	Group     string    `json:"group"`
	Kind      string    `json:"kind"`
	Model     string    `json:"model"`
	State     string    `json:"state"`
	StartedAt time.Time `json:"started_at"`
}

// BeadActivity is the latest recorded activity on a bead: the kind of its
// newest event still in the log and when it was written. The kind alone,
// never the event's data.
type BeadActivity struct {
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
}

// Bead reads one bead whole for viewer, "" for the yard itself. An ask
// or answer that does not parse is left out, as the queue leaves it out:
// corrupt notes are yardr's own surfaces to show, not a client's. A bead
// the viewer may not see reads as one the yard has not got, and so does
// each of its about-beads: their handles stay, as for a bead since
// removed, but no title or stage.
//
// A ref of bead@peer reads a question another yard asked instead: the
// view is the foreign card's one step away, synthesized from what the
// asking yard carried (foreignBead), and held to may(see) for the viewer.
func (s *Store) Bead(ctx context.Context, id string, viewer ...string) (*BeadView, error) {
	who := firstViewer(viewer)
	// A bead's handle never holds an "@" (validID).
	if bead, peer, foreign := strings.Cut(id, "@"); foreign {
		if err := s.mayRead(ctx, who, ActSee, Object{Kind: KindBead, Name: id}); err != nil {
			return nil, fmt.Errorf("bead %s: %w", id, ErrNotFound)
		}
		return s.foreignBead(ctx, bead, peer, who)
	}
	b, err := s.GetBead(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.mayRead(ctx, who, ActSee, Object{Kind: KindBead, Name: b.ID}); err != nil {
		return nil, fmt.Errorf("bead %s: %w", id, ErrNotFound)
	}
	notes, err := s.Notes(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	v := &BeadView{
		ID: b.ID, Title: b.Title, Body: b.Body, Stage: b.Stage, Topic: topicOf(*b), Labels: b.Labels,
		Notes: []BeadNote{}, Asks: []BeadAsk{}, Rounds: b.Rounds,
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
		v.Notes = append(v.Notes, BeadNote{Seq: n.Seq, Kind: kind, Author: n.Author, Text: n.Text, At: n.At, Via: n.Via, Files: n.Files})
	}
	for _, r := range ResolveAsks(notes, now()) {
		if r.AskErr != nil {
			continue
		}
		a := BeadAsk{
			Ask: r.Note.Seq, To: r.Ask.To, Asker: r.Note.Author, Since: r.Note.At,
			Question: r.Ask.Question, Context: r.Ask.Context, Options: r.Ask.Options,
			Recommend: r.Ask.Recommend, Why: r.Ask.Why, State: r.State.String(), Secret: r.Ask.asked(), Concealed: r.Ask.concealed(), Run: r.Ask.proposed(), Setup: r.Ask.setup(), Identity: r.Ask.identityAsked(), Weight: r.Ask.Weight,
		}
		if a.Options == nil {
			a.Options = []AskOption{}
		}
		for _, h := range r.Ask.About {
			about := AboutBead{ID: h}
			if other, err := s.GetBead(ctx, h); err == nil {
				if s.mayRead(ctx, who, ActSee, Object{Kind: KindBead, Name: other.ID}) == nil {
					about.Title, about.Stage = other.Title, other.Stage
				}
			} else if !errors.Is(err, ErrNotFound) {
				return nil, err
			}
			a.About = append(a.About, about)
		}
		if c := r.Current; c != nil {
			a.Option, a.Words, a.Given, a.Ran = c.Answer.Option, c.Answer.Words, c.Answer.Given, c.Answer.Ran
			if r.State == AskTakenBack {
				a.By = c.Note.Author
			}
			if r.State == AskAside {
				at := r.ReturnAt
				a.ReturnAt = &at
			}
			a.PicksBefore = picksBefore(r)
		}
		v.Asks = append(v.Asks, a)
	}
	moves, activity, err := s.beadMoves(b.ID)
	if err != nil {
		return nil, err
	}
	v.Moves = moves
	var now BeadNow
	if activity != nil {
		now.Activity = activity
	}
	sess, err := s.ListSessions(ctx, SessionFilter{Bead: b.ID})
	if err != nil {
		return nil, err
	}
	// Live sessions, oldest first; the newest is who works it now.
	if len(sess) > 0 {
		live := sess[len(sess)-1]
		now.Session = &BeadSession{Group: live.Group, Kind: live.Kind, Model: live.Model, State: live.State, StartedAt: live.StartedAt}
	}
	if now.Activity != nil || now.Session != nil {
		v.Now = &now
	}
	// The estimate reads what the view already read; a bead shown
	// without one is still shown.
	if est, err := s.Estimate(ctx, id); err == nil && est != nil {
		v.ETA, v.Late = est.ETA, est.Late
	}
	return v, nil
}

// beadMoves is the bead's stage-move history with its latest activity:
// the bead's advanced events oldest first, at most beadMovesCap of them
// (the newest), and the kind and time of its newest event of any kind.
// It walks the bead's event index newest first, stopping after finding
// the cap and holding only the entries it serves.
func (s *Store) beadMoves(id string) ([]BeadMove, *BeadActivity, error) {
	var moves []BeadMove
	var activity *BeadActivity
	err := s.read(func(tx *bolt.Tx) error {
		u, err := resolve(tx, id)
		if err != nil {
			return err
		}
		prefix := []byte(u + "\x00")
		c := tx.Bucket(bEventsBy).Cursor()
		k, _ := c.Seek([]byte(u + "\x01"))
		if k == nil {
			k, _ = c.Last()
		} else {
			k, _ = c.Prev()
		}
		for ; k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Prev() {
			var e Event
			if ok, err := get(tx, bEvents, string(k[len(prefix):]), &e); err != nil {
				return err
			} else if !ok {
				continue
			}
			// The first key is the bead's newest event.
			if activity == nil {
				activity = &BeadActivity{Kind: e.Kind, At: e.At}
			}
			if e.Kind != "advanced" {
				continue
			}
			from, _ := e.Data["from"].(string)
			to, _ := e.Data["to"].(string)
			outcome, _ := e.Data["outcome"].(string)
			insteadOf, _ := e.Data["instead_of"].(string)
			moves = append(moves, BeadMove{From: from, To: to, At: e.At, Outcome: outcome, InsteadOf: insteadOf})
			if len(moves) == beadMovesCap {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	slices.Reverse(moves)
	return moves, activity, nil
}

// picksBefore is how many picks came before the ask's current one, so a
// client can say "changed": counted over the answers in order, up to the
// one the state comes from. Only a current pick has any.
func picksBefore(r ResolvedAsk) int {
	n := 0
	if c := r.Current; c != nil && c.Answer.Option != 0 {
		for _, ans := range r.Answers {
			if ans.Note.Seq == c.Note.Seq {
				break
			}
			if ans.Answer.Option != 0 {
				n++
			}
		}
	}
	return n
}

// foreignBead reads the view of a bead@peer ref: the questions the yard
// peer asked on its bead, as the card's one step away, in the same shape
// as a bead of this yard. The title, stage, topic and labels are carried,
// the body under one first line saying the bead lives in the other yard
// and at which stage; there are no notes, since notes stay local; the
// asks are whole with their state as this yard knows it, answered, put
// aside or declined here or ended there; the about-beads are as carried,
// text never followed.
//
// Where the bead is the one that yard works for a bead of this yard (its
// counterpart, by this yard's own record of the pair), the body says so in
// a second line and this yard's bead is the first of each ask's
// about-beads: the person sees their own bead and not only a handle of
// another yard.
func (s *Store) foreignBead(ctx context.Context, bead, peer, viewer string) (*BeadView, error) {
	var recs []foreignAsk
	var p *Peer
	var mine *AboutBead
	works := "works"
	err := s.read(func(tx *bolt.Tx) error {
		var err error
		p, err = loadPeer(tx, peer)
		if err != nil {
			return err
		}
		if recs, err = foreignAsksForBead(tx, p, bead); err != nil {
			return err
		}
		// The handle is the peer's word, said in a bead event or its return
		// for a stay this yard began: no ask names a bead of this yard.
		return each(tx, bPeerSent, "", func(_ []byte, sent *peerSent) error {
			var row beadRow
			if sent.Key != p.Key || sent.Was != "" || sent.Theirs != bead || mine != nil && sent.EndedAt != nil {
				return nil
			}
			if ok, err := get(tx, bBeads, sent.Bead, &row); err != nil || !ok {
				return err
			}
			mine, works = &AboutBead{ID: row.ID}, "works"
			if s.may(ctx, tx, viewer, ActSee, Object{Kind: KindBead, Name: row.ID}) == nil {
				mine.Title, mine.Stage = row.Title, row.Stage
			}
			if sent.EndedAt != nil {
				works = "worked"
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, fmt.Errorf("bead %s@%s: no question from %s stands on it: %w", bead, p.Name, p.Name, ErrNotFound)
	}
	id := bead + "@" + p.Name
	// The bead's own lines from what was carried last: a bead asked
	// twice may have been renamed or moved between the two.
	meta := recs[0]
	for _, rec := range recs[1:] {
		if rec.TakenAt.After(meta.TakenAt) {
			meta = rec
		}
	}
	body := "This bead lives in " + p.Name + "'s yard, at stage " + meta.Stage + "."
	if mine != nil {
		body += fmt.Sprintf("\nThis is the bead %s %s for this yard's %s.", p.Name, works, mine.ID)
	}
	if meta.Body != "" {
		body += "\n" + meta.Body
	}
	labels := meta.Labels
	if labels == nil {
		labels = []string{}
	}
	v := &BeadView{
		ID: id, Title: meta.Title, Body: body, Stage: meta.Stage, Topic: meta.Topic, Labels: labels,
		Notes: []BeadNote{}, Asks: []BeadAsk{},
	}
	at := now()
	for _, rec := range recs {
		asker := rec.From + "@" + p.Name
		a := BeadAsk{
			Ask: rec.Seq, To: rec.To, Asker: asker, Since: rec.Since,
			Question: rec.Ask.Question, Context: rec.Ask.Context, Options: rec.Ask.Options,
			Recommend: rec.Ask.Recommend, Why: rec.Ask.Why,
		}
		if a.Options == nil {
			a.Options = []AskOption{}
		}
		if mine != nil {
			a.About = append(a.About, *mine)
		}
		for _, about := range rec.About {
			a.About = append(a.About, AboutBead(about))
		}
		if rec.Outcome != "" {
			a.State = outcomeState(rec.Outcome).String()
			a.Words = outcomeWords(rec.Outcome, p.Name)
		} else {
			r := rec.resolved(id, asker, at)
			a.State = r.State.String()
			if c := r.Current; c != nil {
				a.Option, a.Words = c.Answer.Option, c.Answer.Words
				if r.State == AskAside {
					returnAt := r.ReturnAt
					a.ReturnAt = &returnAt
				}
				a.PicksBefore = picksBefore(r)
			} else if rec.asided(at) {
				a.State = AskAside.String()
				a.Words = asideWords(p.Name, *rec.AsideUntil)
				a.ReturnAt = rec.AsideUntil
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
//
// A pick on a mapping ask that writes a user's identity is a weighty
// approval: weighed is the client's word that its person saw the weight
// first, and the pick is refused without it (weighedRefusal). The yard
// checks what the client says and no more. Every other answer ignores it.
func (s *Store) AnswerAsk(ctx context.Context, id string, ask uint64, option int, words, actor string, weighed bool) error {
	return s.addAnswer(ctx, id, Answer{Ask: ask, Option: option, Words: words}, actor, "", weighed)
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
