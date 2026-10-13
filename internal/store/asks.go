// Asks are questions as data: one question with enumerable options, asked
// of one person, living as a note of kind ask on any bead in any stage.
// The person answers by picking an option, puts the question aside, or
// declines it, each a note of kind answer; so is the asker taking the
// question back (TakeBackAsk). Both are JSON in Note.Text, one schema each,
// parsed the same everywhere; prime, review and chat render them as
// numbered text, never the raw form.
//
// An ask holds the question in one line, who must answer, two to five
// lines of context, one or more options each naming its consequence in a
// line, the one recommended and why, and the other beads it is about. An
// ask with no options wants words. yardr adds the option "You decide" to
// every ask that has options; the writer does not write it.
//
// An answer names its ask by the ask note's seq and holds either a pick
// (the option's number with any words), words alone (only on an ask with
// no options), Later (put aside: not open for 24 hours or until the
// asker's next note on the bead, whichever is first), or Decline (ends it
// with no option picked). A newer pick supersedes the older one for
// display; every pick stays as a note. TakeBack is not the person's: the
// asker, or the operator, ends an ask that is asked no more, with why.
//
// An ask may ask for a secret instead (secretask.go): its answer is the
// names that were given, written by GiveSecret alone, and the value is in
// no note. Or it may propose a command of the operator's (runask.go): its
// answer is the command the yard ran, written by ApproveAsk alone.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Note kinds for questions and their answers, beside note, journal and
// handoff.
const (
	NoteAsk    = "ask"
	NoteAnswer = "answer"
)

// YouDecide is the label of the option yardr adds to every ask that has
// options. The writer cannot write it: an option of that label,
// letter-case aside, is refused.
const YouDecide = "You decide"

// LaterFor is how long a put-aside ask stays out of the person's queue,
// unless the asker writes on the bead sooner.
const LaterFor = 24 * time.Hour

// AskOption is one option of an ask: its label and its consequence in a
// line each.
type AskOption struct {
	Label       string `json:"label"`
	Consequence string `json:"consequence"`
}

// Ask is the JSON of a note of kind ask: one question as data.
type Ask struct {
	// Question is the question in one line, as a person would ask it.
	Question string `json:"question"`
	// To is who must answer, a user; empty is never stored (the command
	// defaults it to the yard's operator).
	To string `json:"to"`
	// Context is what the question is about, two to five lines; at least
	// one is stored.
	Context []string `json:"context"`
	// Options are the options in the order written, with YouDecide last
	// when the writer gave any. None wants words.
	Options []AskOption `json:"options,omitempty"`
	// Recommend is the recommended option's number, 1-based into Options,
	// 0 for no recommendation; Why says why in a line.
	Recommend int    `json:"recommend,omitempty"`
	Why       string `json:"why,omitempty"`
	// About names the other beads the question is about, by handle; the
	// ask lives on the one bead.
	About []string `json:"about,omitempty"`
	// Secret makes the ask a secret ask: where its answer goes. Such an ask
	// has no options, no recommendation and no about-beads, is for the
	// operator (or, for a user's own address in a source, for that user),
	// and is answered by GiveSecret.
	Secret *AskSecret `json:"secret,omitempty"`
	// Run makes the ask a run ask: the one yardr command its approval runs.
	// Such an ask has no options, no recommendation and no about-beads, is
	// for the operator (or, for a command that is a user's own, for that
	// user), and is approved by ApproveAsk.
	Run *AskRun `json:"run,omitempty"`
}

// Answer is the JSON of a note of kind answer: what one person did with
// one ask. Exactly one of a pick (Option), words alone (Words with no
// Option, Later, Decline or TakeBack), Later, Decline, Given, Ran and
// TakeBack is set.
type Answer struct {
	// Ask is the ask note's seq.
	Ask uint64 `json:"ask"`
	// Option is the picked option's number, 1-based into the ask's
	// options, 0 for none.
	Option int `json:"option,omitempty"`
	// Words go with a pick, answer an ask with no options, or say why a
	// question is declined or taken back.
	Words string `json:"words,omitempty"`
	// Later puts the ask aside; Decline ends it with no option picked.
	Later   bool `json:"later,omitempty"`
	Decline bool `json:"decline,omitempty"`
	// Given are the names a secret ask was given values for: all the
	// answer says of them. Only GiveSecret writes it.
	Given []string `json:"given,omitempty"`
	// Ran is the command an approved run ask ran, with what it printed. Only
	// ApproveAsk writes it.
	Ran *AskRan `json:"ran,omitempty"`
	// TakeBack ends the ask as asked no more: the note's author is the asker
	// or the operator, never the person asked. Only TakeBackAsk writes it.
	TakeBack bool `json:"take_back,omitempty"`
}

// ParseAsk reads the ask a note of kind ask holds.
func ParseAsk(n Note) (Ask, error) {
	var a Ask
	if err := json.Unmarshal([]byte(n.Text), &a); err != nil {
		return Ask{}, fmt.Errorf("ask %d: %w", n.Seq, err)
	}
	return a, nil
}

// ParseAnswer reads the answer a note of kind answer holds.
func ParseAnswer(n Note) (Answer, error) {
	var a Answer
	if err := json.Unmarshal([]byte(n.Text), &a); err != nil {
		return Answer{}, fmt.Errorf("answer %d: %w", n.Seq, err)
	}
	return a, nil
}

// AddAsk stores ask on the bead id as a note of kind ask, written by
// actor, and answers the note's seq. It refuses an ask with no context, an
// option with no consequence, an option the writer called YouDecide, a
// recommendation that names no option or says no why, and an about-bead
// that does not exist. To must name who answers; the command defaults it.
//
// An ask with Secret asks the operator for a declared source's credential,
// or a user (or the operator for them) for that user's own address in a
// source that tells people. It is checked against the source's declaration
// in the write that stores it (askedSecret) and stored with the names and
// the host the declaration gives, whatever the writer sent for them. It
// takes no options, no recommendation and no about-beads, and nothing of it
// leaves the yard: a To of <person>@<peer> is refused, and no copy is sent
// to a name a link trusts as the one asked.
//
// An ask with Run proposes one command of the operator's, from a fixed
// list, for the operator to approve (ApproveAsk); one row of the list is a
// user's own, and is for that user or the operator. It is parsed and checked
// against the yard as it stands in the write that stores it (askedRun), and
// stored with the yard's own sentences for what the command changes. It
// takes no options, no recommendation and no about-beads, at most
// MaxRunContext lines of context, and is written by a crew member or a
// user, never by a bead's agent. Like a secret ask it stays in the yard: a
// To of <person>@<peer> is refused, and no copy is sent to a name with
// reach.
//
// A To of <person>@<peer> asks a person of a linked yard: in the same
// write that stores the ask, the yard queues one message of kind ask for
// the peer (peerAsk), where it stands among the person's decisions. The
// gates are mail's: the peer must be one, the link must carry ask out, a
// crew member asking must name the peer in its peers setting, and a bead's
// agent may not ask across. A refused ask stores nothing.
//
// An ask for a user of this yard is also sent, in the same write, to each
// name a link gives reach for that user (AskReach, peertrust.go): the same
// message, addressed to the name as its yard knows it, so it stands among
// the person's decisions there as well as here. The writer's gates are not
// asked, since the writer addressed a user of this yard and it is the
// operator's grant that sends the copy: a crew member without the peer and
// a bead's agent are copied too. A copy that cannot be queued is told to
// the person and refuses nothing. Where the copies went is kept until the
// ask ends (bAskCopies), and so is where an ask addressed across went.
func (s *Store) AddAsk(ctx context.Context, id string, ask Ask, actor string) (uint64, error) {
	add, err := newAsk(ctx, ask, actor)
	if err != nil {
		return 0, err
	}
	var seq uint64
	err = s.write(func(w *wtx) error {
		seq, err = add(w, id)
		return err
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// newAsk checks ask as far as it can be checked outside a write and answers
// what stores it on the bead id inside one (AddAsk, AskNew).
func newAsk(ctx context.Context, ask Ask, actor string) (func(w *wtx, id string) (uint64, error), error) {
	ask.Question = strings.TrimSpace(ask.Question)
	if ask.Question == "" {
		return nil, fmt.Errorf("an ask needs a question")
	}
	ask.To = strings.TrimSpace(ask.To)
	if ask.To == "" {
		return nil, fmt.Errorf("an ask needs --to: who must answer")
	}
	var context []string
	for _, line := range ask.Context {
		if line = strings.TrimSpace(line); line != "" {
			context = append(context, line)
		}
	}
	if len(context) == 0 {
		return nil, fmt.Errorf("an ask needs --context: two to five lines of what it is about, at least one")
	}
	ask.Context = context
	for i, o := range ask.Options {
		ask.Options[i].Label = strings.TrimSpace(o.Label)
		ask.Options[i].Consequence = strings.TrimSpace(o.Consequence)
		o = ask.Options[i]
		if o.Label == "" {
			return nil, fmt.Errorf("option %d needs a label: --option \"<label>: <consequence>\"", i+1)
		}
		if o.Consequence == "" {
			return nil, fmt.Errorf("option %d (%s) needs a consequence: --option %q", i+1, o.Label, o.Label+": <what picking it does>")
		}
		if strings.EqualFold(o.Label, YouDecide) {
			return nil, fmt.Errorf("option %d: yardr adds %q itself, to every ask with options: leave it out", i+1, YouDecide)
		}
	}
	switch {
	case ask.Recommend != 0 && len(ask.Options) == 0:
		return nil, fmt.Errorf("--recommend %d: the ask has no options to recommend", ask.Recommend)
	case ask.Recommend < 0 || ask.Recommend > len(ask.Options):
		return nil, fmt.Errorf("--recommend %d: the ask has options 1..%d", ask.Recommend, len(ask.Options))
	case ask.Recommend != 0 && strings.TrimSpace(ask.Why) == "":
		return nil, fmt.Errorf("--recommend %d needs --why: why this option, in a line", ask.Recommend)
	case ask.Recommend == 0 && strings.TrimSpace(ask.Why) != "":
		return nil, fmt.Errorf("--why needs --recommend: which option it is for")
	}
	ask.Why = strings.TrimSpace(ask.Why)
	var about []string
	for _, b := range ask.About {
		if b = strings.TrimSpace(b); b != "" {
			about = append(about, b)
		}
	}
	if len(ask.Options) > 0 {
		options := make([]AskOption, 0, len(ask.Options)+1)
		ask.Options = append(append(options, ask.Options...),
			AskOption{Label: YouDecide, Consequence: "leave it to " + actor})
	}
	to, peer, outside := strings.Cut(ask.To, "@")
	if ask.Secret != nil {
		switch {
		case len(ask.Options) > 0 || ask.Recommend != 0:
			return nil, fmt.Errorf("a secret ask takes no --option and no --recommend: its answer is the value, typed where no agent is shown it")
		case len(about) > 0:
			return nil, fmt.Errorf("a secret ask takes no --about: it is about the source it names")
		case outside:
			return nil, fmt.Errorf("a secret is given in the yard that asks for it, by its operator or the user it is of: %s is of another yard, and nothing of a secret crosses a link", ask.To)
		}
	}
	if ask.Run != nil {
		switch {
		case ask.Secret != nil:
			return nil, fmt.Errorf("an ask asks for a secret or proposes a command to run, not both: --secret or --run")
		case len(ask.Options) > 0 || ask.Recommend != 0:
			return nil, fmt.Errorf("a run ask takes no --option and no --recommend: its answer is the operator's approval of the command, or a decline")
		case len(about) > 0:
			return nil, fmt.Errorf("a run ask takes no --about: it is about what its command names")
		case len(ask.Context) > MaxRunContext:
			return nil, fmt.Errorf("a run ask takes at most %d lines of --context, and this has %d: the command and what the yard says of it must stay in sight", MaxRunContext, len(ask.Context))
		case outside:
			return nil, fmt.Errorf("a command is approved in the yard that runs it, by its operator: %s is of another yard, and no approval crosses a link", ask.To)
		}
		// What can be refused without the yard's records is refused before
		// the write: a command that is not on the list.
		if _, _, err := parseRun(ask.Run.Argv); err != nil {
			return nil, err
		}
	}
	if outside {
		switch {
		case !peerMailName.MatchString(to):
			return nil, fmt.Errorf("ask to %q: that is no person's name", to)
		case !peerMailName.MatchString(actor):
			return nil, fmt.Errorf("ask from %q: a name of letters, digits, '.', '_', '-' and '/' is what another yard is told", actor)
		}
	}
	return func(w *wtx, id string) (uint64, error) {
		row, err := w.loadBead(id)
		if err != nil {
			return 0, err
		}
		// Echo handles, whatever form of id was typed.
		var handles []string
		var aboutBeads []peerAskBead
		seen := map[string]bool{}
		for _, b := range about {
			other, err := w.loadBead(b)
			if err != nil {
				return 0, fmt.Errorf("about: %w", err)
			}
			if seen[other.UUID] {
				return 0, fmt.Errorf("about: %s is named twice", other.ID)
			}
			seen[other.UUID] = true
			handles = append(handles, other.ID)
			aboutBeads = append(aboutBeads, peerAskBead{ID: other.ID, Title: other.Title, Stage: other.Stage})
		}
		var p *Peer
		if outside {
			// Before anything is stored: a refused ask leaves no note.
			p, err = loadPeer(w.Tx, peer)
			if err != nil {
				return 0, err
			}
			var r CrewMember
			if ok, err := get(w.Tx, bCrew, actor, &r); err != nil {
				return 0, err
			} else if ok && !slices.Contains(r.MayMail(), peer) {
				return 0, fmt.Errorf("crew member %s may not ask %s@%s: its definition does not allow it. The operator allows it with yardr crew set %s %s=%s",
					actor, to, peer, actor, MailPeers, strings.Join(append(r.MayMail(), peer), ","))
			} else if !ok && w.Bucket(bUsers).Get([]byte(actor)) == nil {
				if _, this, err := notOperator(ctx, w.Tx, actor); err != nil {
					return 0, err
				} else if this != "" {
					return 0, fmt.Errorf("only a crew member allowed %s (yardr crew set <crew member> %s=%s) or a user of the yard (yardr user list) may ask another yard; this is %s",
						peer, MailPeers, peer, this)
				}
			}
			if !p.carries(PeerAsk, false) {
				return 0, fmt.Errorf("peer %s: the link does not carry ask out: the operator allows it with yardr peer allow %s --send ask", peer, peer)
			}
		}
		// A copy of ask, so that a batch which runs this twice checks what
		// the writer sent both times.
		ask := ask
		if ask.Secret != nil {
			if ask.Secret, err = w.askedSecret(*ask.Secret, ask.To); err != nil {
				return 0, err
			}
		}
		if ask.Run != nil {
			if ask.Run, err = w.askedRun(ctx, *ask.Run, ask.To, actor); err != nil {
				return 0, err
			}
		}
		ask.About = handles
		text, err := json.Marshal(ask)
		if err != nil {
			return 0, err
		}
		n, err := w.addNoted(row, NoteAsk, string(text), actor)
		if err != nil {
			return 0, err
		}
		seq := n.Seq
		if !outside {
			// In the person's queue from this write on: told outside the
			// yard too, when a source tells them.
			if err := w.queueAskPush(ask.To, row.UUID, seq, ""); err != nil {
				return 0, err
			}
		}
		// queue puts the ask in the outbox for name at p, as that yard
		// reads it: addressed name@peer.
		queue := func(p *Peer, name string) (*PeerOut, error) {
			sent := ask
			sent.To = name + "@" + p.Name
			body, err := json.Marshal(peerAsk{
				V: PeerAskVersion, To: name, From: actor,
				Bead: row.ID, Title: row.Title, Topic: topicOf(row.Bead), Stage: row.Stage,
				Labels: row.Labels, Body: cutNote(row.Body),
				Seq: seq, Since: n.At, Ask: sent, About: aboutBeads,
			})
			if err != nil {
				return nil, err
			}
			m := PeerMessage{ID: newPeerMessageID(), Kind: PeerAsk, Body: body}
			if err := m.check(); err != nil {
				return nil, err
			}
			return w.queuePeer(p, m, actor)
		}
		if ask.Secret != nil || ask.Run != nil {
			// Nothing of a secret ask crosses a link, and a run ask is
			// approved in this yard alone: no copy is queued.
			return seq, nil
		}
		if !outside {
			var peers []Peer
			if err := each(w.Tx, bPeers, "", func(_ []byte, p *Peer) error {
				peers = append(peers, *p)
				return nil
			}); err != nil {
				return 0, err
			}
			var copies []askCopy
			for _, t := range AskReach(peers, ask.To, actor) {
				i := slices.IndexFunc(peers, func(p Peer) bool { return p.Name == t.Peer })
				out, err := queue(&peers[i], t.Name)
				if err != nil {
					// The ask is stored and stands here; only its copy
					// is not on its way.
					if err := w.copyUndelivered(ask.To, t.Address(), ask.Question, row.ID, seq, "", err.Error()); err != nil {
						return 0, err
					}
				} else if out != nil {
					copies = append(copies, askCopy{Key: peers[i].Key, Name: t.Name, As: ask.To, Message: out.ID})
				}
			}
			if len(copies) == 0 {
				return seq, nil
			}
			// Where it went, for the day it ends here (settleCopies).
			return seq, put(w.Tx, bAskCopies, join(row.UUID, seqKey(seq)), copies)
		}
		out, err := queue(p, to)
		if err != nil {
			return 0, err
		}
		if out == nil {
			// Checked above, in the same write: the link cannot have
			// stopped carrying ask since.
			return 0, fmt.Errorf("peer %s: the link does not carry ask out: the operator allows it with yardr peer allow %s --send ask", peer, peer)
		}
		// The message that yard has it under, for the day it is taken back.
		return seq, put(w.Tx, bAskCopies, join(row.UUID, seqKey(seq)), []askCopy{{Key: p.Key, Name: to, Message: out.ID}})
	}, nil
}

// AskedBead is what AskNew made: the bead and the seq of the ask on it.
type AskedBead struct {
	Bead *Bead  `json:"bead"`
	Ask  uint64 `json:"ask"`
}

// AskNew makes a bead for a question that is about no bead yet, and the
// ask on it, in one write: nb as CreateBead takes it, titled with the
// question, and ask as AddAsk takes it. Whatever either would refuse
// refuses both, so a refused ask leaves no bead behind. Nothing more is
// stored than the two would store.
//
// A bead nb names no stage and no type for is a question (TypeQuestion):
// it stands where a bead of its depot starts (the stage new, under no
// flow, without one) and nothing routes it there, so no group builds a
// question and the dispatcher does not report it unrouted. Its asker
// closes it when the answer is on it. A stage or a type is the caller
// placing the bead, in a depot's decide stage for one: that bead is what
// CreateBead makes of nb, a task without a type, and routed as its is.
func (s *Store) AskNew(ctx context.Context, nb NewBead, ask Ask, actor string) (*AskedBead, error) {
	nb.Title = strings.TrimSpace(ask.Question)
	if nb.Stage == "" && nb.Type == "" {
		nb.Type = TypeQuestion
	}
	add, err := newAsk(ctx, ask, actor)
	if err != nil {
		return nil, err
	}
	create, err := s.newBead(ctx, nb, actor)
	if err != nil {
		return nil, err
	}
	var made *AskedBead
	err = s.write(func(w *wtx) error {
		row, err := create(w)
		if err != nil {
			return err
		}
		seq, err := add(w, row.UUID)
		if err != nil {
			return err
		}
		// The ask re-minted the bead: answer it as stored.
		if row, err = w.loadBead(row.UUID); err != nil {
			return err
		}
		made = &AskedBead{Bead: &row.Bead, Ask: seq}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return made, nil
}

// AddAnswer records ans on the bead id as a note of kind answer, written
// by actor: a pick of one of the ask's options with any words, words
// alone on an ask with no options, Later, or Decline with why.
//
// A declined ask and an ask answered with words take no further answer; a
// picked ask takes only a newer pick, which supersedes for display. Words
// alone on an ask with options are refused: they go as a plain note and
// leave the ask open.
//
// An id of bead@peer names a question another yard asked (a foreign ask):
// the answer is queued home for the peer instead of written as a note
// (answerForeign), where it lands on the bead it was asked on. The socket
// writes and yardr bead answer both come through here, so both answer a
// foreign ask.
//
// An answer that ends an ask which was also sent to other yards (AddAsk)
// is told to each of them, in the same write (settleCopies): a pick, words
// or a decline, never Later.
//
// A secret ask takes Later, and Decline without words, and nothing else
// here: a pick, words and a decline's words are refused without being said
// back, so that no path but GiveSecret takes what a person typed for it.
//
// A run ask takes Later and Decline with its words, and nothing else here:
// a pick and words are refused, so that no path but ApproveAsk runs its
// command, from this yard or over a link.
func (s *Store) AddAnswer(ctx context.Context, id string, ans Answer, actor string) error {
	if err := answerShape(id, &ans); err != nil {
		return err
	}
	// A bead's handle never holds an "@" (validID).
	if bead, peer, foreign := strings.Cut(id, "@"); foreign {
		return s.answerForeign(ctx, bead, peer, ans, actor)
	}
	return s.writeVia(ctx, func(w *wtx) error {
		row, err := w.loadBead(id)
		if err != nil {
			return err
		}
		notes, err := beadNotes(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		if _, err := answerable(row.ID, notes, ans, now()); err != nil {
			return err
		}
		text, err := json.Marshal(ans)
		if err != nil {
			return err
		}
		if _, err = w.addNoted(row, NoteAnswer, string(text), actor); err != nil {
			return err
		}
		return w.settleCopies(row, ans, nil, "")
	})
}

// answerShape refuses an answer that is not exactly one of a pick, words
// alone, Later and Decline, whatever ask it names; id is the bead as the
// refusal says it. Whether a decline needs words is the ask's to say
// (answerable). Given is GiveSecret's to write: an answer that came with
// it would say "gave" of a value no file holds. Ran is ApproveAsk's: an
// answer that came with it would say the yard ran what it did not.
// TakeBack is TakeBackAsk's, which asks who takes it back: here it would
// be anyone's.
func answerShape(id string, ans *Answer) error {
	ans.Words = strings.TrimSpace(ans.Words)
	if ans.TakeBack {
		return fmt.Errorf("answer %d: an ask is taken back by whoever asked it, with yardr bead ask --take-back %s %d \"<why>\"", ans.Ask, id, ans.Ask)
	}
	if len(ans.Given) > 0 {
		return fmt.Errorf("answer %d: a secret is given with yardr bead answer %s %d --secret, and recorded by the yard alone", ans.Ask, id, ans.Ask)
	}
	if ans.Ran != nil {
		return fmt.Errorf("answer %d: a command is approved with yardr bead answer %s %d --approve, and what ran is recorded by the yard alone", ans.Ask, id, ans.Ask)
	}
	if ans.Ask == 0 {
		return fmt.Errorf("an answer needs the ask: yardr bead answer %s <ask> <n> [words]", id)
	}
	// Exactly one of a pick, words alone, Later and Decline.
	kinds := boolCount(ans.Option != 0) + boolCount(ans.Later) + boolCount(ans.Decline)
	if kinds == 0 && ans.Words == "" {
		return fmt.Errorf("answer %d: pick an option, answer with --words, put it aside with --later, or decline it", ans.Ask)
	}
	if kinds > 1 || (kinds == 1 && ans.Option == 0 && ans.Words != "" && !ans.Decline) {
		return fmt.Errorf("answer %d: pick an option, answer with --words, put it aside with --later, or decline it: one of the four", ans.Ask)
	}
	if ans.Later && (ans.Option != 0 || ans.Words != "") {
		return fmt.Errorf("--later puts ask %d aside with nothing else", ans.Ask)
	}
	return nil
}

// answerable decides whether ans may be recorded on the ask it names among
// notes, the notes of the bead id, at the time at: the one set of rules for
// an answer written here, one queued home to another yard, and one another
// yard sent home. With the refusal it answers what the other yard is told
// of one that came from there: RefusedNoSuchAsk for an ask that is none,
// RefusedAskDeclined, RefusedAskAnswered and RefusedAskTakenBack for one
// that takes no further answer, RefusedNotAsk for a decline without its
// words, "" for an answer that does not fit the ask.
func answerable(id string, notes []Note, ans Answer, at time.Time) (string, error) {
	var askNote *Note
	for i := range notes {
		if notes[i].Seq == ans.Ask {
			askNote = &notes[i]
		}
	}
	if askNote == nil {
		return RefusedNoSuchAsk, fmt.Errorf("bead %s has no ask %d", id, ans.Ask)
	}
	if askNote.Kind != NoteAsk {
		return RefusedNoSuchAsk, fmt.Errorf("note %d on bead %s is not an ask", ans.Ask, id)
	}
	ask, err := ParseAsk(*askNote)
	if err != nil {
		return RefusedNoSuchAsk, err
	}
	switch {
	// First, and without what was sent: the words may be the secret, typed
	// into a client that does not know the field.
	case ask.Secret != nil && ans.Decline && ans.Words != "":
		return "", fmt.Errorf("ask %d asks for a secret, and declining it takes no words: yardr bead answer %s %d --decline \"\"", ans.Ask, id, ans.Ask)
	case ask.Secret != nil && (ans.Option != 0 || ans.Words != ""):
		return "", fmt.Errorf("ask %d takes a secret, not words: yardr bead answer %s %d --secret", ans.Ask, id, ans.Ask)
	// A client that does not know the field shows a run ask as one that
	// wants words, and a link carries a pick or words home: neither approves.
	case ask.Run != nil && (ans.Option != 0 || ans.Words != "" && !ans.Decline):
		return "", fmt.Errorf("ask %d takes an approval, not words: yardr bead answer %s %d --approve", ans.Ask, id, ans.Ask)
	case ask.Secret == nil && ans.Decline && ans.Words == "":
		return RefusedNotAsk, fmt.Errorf("declining ask %d needs words: --decline \"<why you will not decide it>\"", ans.Ask)
	}
	resolved := ResolveAsks(notes, at)
	var current *ResolvedAsk
	for i := range resolved {
		if resolved[i].Note.Seq == ans.Ask {
			current = &resolved[i]
		}
	}
	if current == nil {
		return RefusedNoSuchAsk, fmt.Errorf("bead %s has no ask %d", id, ans.Ask)
	}
	switch current.State {
	case AskDeclined:
		return RefusedAskDeclined, fmt.Errorf("ask %d was declined: %s", ans.Ask, declinedLine(*current.Current))
	case AskTakenBack:
		return RefusedAskTakenBack, fmt.Errorf("ask %d is asked no more: %s", ans.Ask, takenBackLine(*current.Current))
	case AskAnswered:
		if ask.Run != nil {
			return RefusedAskAnswered, fmt.Errorf("ask %d was approved already, and the yard ran its command", ans.Ask)
		}
		if len(ask.Options) == 0 {
			return RefusedAskAnswered, fmt.Errorf("ask %d is answered already", ans.Ask)
		}
		if ans.Option == 0 {
			return RefusedAskAnswered, fmt.Errorf("ask %d is answered already: a newer pick replaces it, nothing else", ans.Ask)
		}
	}
	switch {
	case ans.Option != 0 && len(ask.Options) == 0:
		return "", fmt.Errorf("ask %d wants words: answer with yardr bead answer %s %d --words \"<text>\"", ans.Ask, id, ans.Ask)
	case ans.Option != 0 && (ans.Option < 1 || ans.Option > len(ask.Options)):
		return "", fmt.Errorf("ask %d has options 1..%d", ans.Ask, len(ask.Options))
	case ans.Option == 0 && !ans.Later && !ans.Decline && len(ask.Options) > 0:
		return "", fmt.Errorf("ask %d has options: words alone go as yardr bead note %s \"...\" and leave the ask open; pick one with yardr bead answer %s %d <n>",
			ans.Ask, id, id, ans.Ask)
	case ans.Option == 0 && !ans.Later && !ans.Decline && ans.Words == "":
		return "", fmt.Errorf("ask %d wants words: yardr bead answer %s %d --words \"<text>\"", ans.Ask, id, ans.Ask)
	}
	return "", nil
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

// declinedLine is a declined ask's end as every view shows it. Only a
// secret ask is declined without words.
func declinedLine(a ResolvedAnswer) string {
	if a.Answer.Words == "" {
		return fmt.Sprintf("%s declined to give it", a.Note.Author)
	}
	return fmt.Sprintf("%s declined to decide this: %s", a.Note.Author, a.Answer.Words)
}

// takenBackLine is a taken-back ask's end as every view shows it: who took
// it back and why, never as an answer of the person asked.
func takenBackLine(a ResolvedAnswer) string {
	return fmt.Sprintf("taken back by %s: %s", a.Note.Author, a.Answer.Words)
}

// TakeBackAsk ends the ask seq on the bead id as asked no more, with the
// reason why: a note of kind answer written by actor, which is no answer of
// the person asked. The ask leaves their open decisions and reads wherever
// it is shown as "taken back by <actor>: <reason>". It takes no answer
// afterwards.
//
// Only whoever asked it and the operator may: a question is its asker's to
// withdraw, and nobody else's to end in the person's place. An ask that is
// answered, declined or taken back already is refused, naming how it ended;
// one put aside is taken back as an open one is. A bead@peer names a
// question another yard asked, which is taken back there.
//
// Where the ask also stands in other yards (a copy by reach, or an ask
// addressed to <person>@<peer>), each is told in the same write
// (settleCopies), and it leaves the person's decisions there too.
func (s *Store) TakeBackAsk(ctx context.Context, id string, seq uint64, reason, actor string) error {
	reason = strings.TrimSpace(reason)
	switch {
	case seq == 0:
		return fmt.Errorf("taking an ask back needs the ask: yardr bead ask --take-back %s <ask> \"<why>\"", id)
	case reason == "":
		return fmt.Errorf("taking ask %d back needs a reason, which the person reads in its place: yardr bead ask --take-back %s %d \"<why>\"", seq, id, seq)
	case strings.Contains(id, "@"):
		// A bead's handle never holds an "@" (validID).
		return fmt.Errorf("ask %d on %s was asked in another yard, and is taken back there by whoever asked it", seq, id)
	}
	return s.writeVia(ctx, func(w *wtx) error {
		row, err := w.loadBead(id)
		if err != nil {
			return err
		}
		notes, err := beadNotes(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		var r *ResolvedAsk
		resolved := ResolveAsks(notes, now())
		for i := range resolved {
			if resolved[i].Note.Seq == seq {
				r = &resolved[i]
			}
		}
		switch {
		case r == nil && slices.ContainsFunc(notes, func(n Note) bool { return n.Seq == seq }):
			return fmt.Errorf("note %d on bead %s is not an ask", seq, row.ID)
		case r == nil:
			return fmt.Errorf("bead %s has no ask %d", row.ID, seq)
		case r.AskErr != nil:
			return r.AskErr
		}
		if asker := r.Note.Author; actor != asker {
			operator, this, err := notOperator(ctx, w.Tx, actor)
			if err != nil {
				return err
			}
			if this != "" {
				return fmt.Errorf("only %s, who asked it, or %s may take ask %d back; this is %s", asker, operator, seq, this)
			}
		}
		switch r.State {
		case AskAnswered:
			return fmt.Errorf("ask %d is answered already: nothing is left to take back", seq)
		case AskDeclined:
			return fmt.Errorf("ask %d was declined: %s", seq, declinedLine(*r.Current))
		case AskTakenBack:
			return fmt.Errorf("ask %d is taken back already: %s", seq, takenBackLine(*r.Current))
		}
		ans := Answer{Ask: seq, Words: reason, TakeBack: true}
		text, err := json.Marshal(ans)
		if err != nil {
			return err
		}
		if _, err = w.addNoted(row, NoteAnswer, string(text), actor); err != nil {
			return err
		}
		return w.settleCopies(row, ans, nil, "")
	})
}

// AskState is what an ask waits for: nothing yet, a pick that stands, a
// return from being put aside, or nobody, declined or taken back.
type AskState int

const (
	// AskOpen waits for an answer.
	AskOpen AskState = iota
	// AskAnswered has a pick, words on an ask with no options, or the
	// names a secret ask was given, that stands.
	AskAnswered
	// AskAside was put aside and has not come back yet.
	AskAside
	// AskDeclined was declined: ended with no option picked.
	AskDeclined
	// AskTakenBack was taken back by its asker or the operator: asked no
	// more, and no answer of the person.
	AskTakenBack
)

// String is the state as words.
func (s AskState) String() string {
	switch s {
	case AskAnswered:
		return "answered"
	case AskAside:
		return "put aside"
	case AskDeclined:
		return "declined"
	case AskTakenBack:
		return "taken back"
	}
	return "open"
}

// ResolvedAnswer is one answer note parsed.
type ResolvedAnswer struct {
	Note   Note
	Answer Answer
}

// ResolvedAsk is one ask with its answers and its state.
type ResolvedAsk struct {
	Note Note
	Ask  Ask
	// AskErr says the note holds no ask; the views show the seq and why,
	// never the raw text.
	AskErr  error
	Answers []ResolvedAnswer
	State   AskState
	// Current is the answer the state comes from, nil when open.
	Current *ResolvedAnswer
	// ReturnAt is when a put-aside ask comes back, 24 hours after it was
	// put aside; the asker's next note on the bead brings it back sooner.
	ReturnAt time.Time
	now      time.Time
}

// ResolveAsks groups a bead's notes into its asks, oldest first, each with
// its answers in order and its state at now. A newer pick supersedes the
// older one; a put-aside that has run its 24 hours, or that the asker
// wrote over, is as if gone.
func ResolveAsks(notes []Note, now time.Time) []ResolvedAsk {
	var out []ResolvedAsk
	for _, n := range notes {
		if n.Kind != NoteAsk {
			continue
		}
		r := ResolvedAsk{Note: n, now: now}
		r.Ask, r.AskErr = ParseAsk(n)
		if r.AskErr == nil {
			current := -1
			for _, m := range notes {
				if m.Kind != NoteAnswer {
					continue
				}
				// An answer that does not parse names no ask: nothing
				// claims it, and the views say so where it stands.
				a, err := ParseAnswer(m)
				if err != nil || a.Ask != n.Seq {
					continue
				}
				r.Answers = append(r.Answers, ResolvedAnswer{Note: m, Answer: a})
				switch {
				case a.TakeBack:
					r.State, current = AskTakenBack, len(r.Answers)-1
				case a.Decline:
					r.State, current = AskDeclined, len(r.Answers)-1
				case a.Later:
					if laterBack(n.Author, notes, m, now) {
						continue
					}
					r.State, current = AskAside, len(r.Answers)-1
					r.ReturnAt = m.At.Add(LaterFor)
				default:
					r.State, current = AskAnswered, len(r.Answers)-1
				}
			}
			if current >= 0 {
				r.Current = &r.Answers[current]
			}
		}
		out = append(out, r)
	}
	return out
}

// laterBack reports whether a put-aside ask has come back: its 24 hours
// are over, or the asker wrote on the bead after it was put aside.
func laterBack(asker string, notes []Note, later Note, now time.Time) bool {
	if !now.Before(later.At.Add(LaterFor)) {
		return true
	}
	return slices.ContainsFunc(notes, func(n Note) bool {
		return n.Author == asker && n.Seq > later.Seq
	})
}

// beadNotes reads the bead's notes in seq order, mapping the bead to its
// handle, as Notes does.
func beadNotes(tx *bolt.Tx, uuid string) ([]Note, error) {
	var row beadRow
	if ok, err := get(tx, bBeads, uuid, &row); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("bead: %w", ErrNotFound)
		}
		return nil, err
	}
	var out []Note
	err := each(tx, bNotes, uuid+"\x00", func(_ []byte, n *Note) error {
		n.Bead = row.ID
		out = append(out, *n)
		return nil
	})
	return out, err
}

// Lines renders the ask as numbered text: the question, its context, its
// numbered options with the recommendation marked, and its state. Nobody
// reads the raw form.
func (r ResolvedAsk) Lines() []string { return r.lines(nil) }

// LinesMarked renders the ask as Lines does, with an answer's author
// from another yard marked as mail is: "(as <user>)" where as (from
// TrustedAs) trusts the address, the foreign line otherwise. Prime,
// review and chat read asks this way; the socket's decided entries carry
// no author and need no mark.
func (r ResolvedAsk) LinesMarked(as map[string]string) []string {
	return r.lines(func(a string) string { return MarkedAuthor(a, as) })
}

// lines renders the ask, marking an answer's author with mark, identity
// when nil. The asker and the address are this yard's own words and are
// never marked; the answer is the other yard's, and says whose word it
// is.
//
// A run ask is drawn so that the asker's words cannot stand for the yard's:
// each context line begins with who says it, and the yard's part comes
// last and together, the command whole, what the yard says it changes and
// the command that takes it back. The command is never cut or shortened.
func (r ResolvedAsk) lines(mark func(string) string) []string {
	if r.AskErr != nil {
		return []string{fmt.Sprintf("ask %d: not a question any more (%v)", r.Note.Seq, r.AskErr)}
	}
	state := r.State.String()
	if r.Ask.Run != nil && r.State == AskAnswered {
		state = "approved"
	}
	head := fmt.Sprintf("ask %d · %s · to %s · asked by %s · %s ago",
		r.Note.Seq, state, r.Ask.To, r.Note.Author, askAge(r.Note.At, r.now))
	if r.State == AskAside {
		head = fmt.Sprintf("ask %d · put aside until %s · to %s · asked by %s · %s ago",
			r.Note.Seq, r.ReturnAt.Local().Format("2006-01-02 15:04"), r.Ask.To, r.Note.Author, askAge(r.Note.At, r.now))
	}
	out := []string{head, r.Ask.Question}
	for _, line := range r.Ask.Context {
		if r.Ask.Run != nil {
			line = r.Note.Author + " says: " + line
		}
		out = append(out, "  "+line)
	}
	switch {
	case r.Ask.Run != nil && r.State == AskAnswered:
	case r.Ask.Run != nil:
		out = append(out, fmt.Sprintf("  when you approve, the yard runs this as %s, and nothing else:", r.Ask.To),
			"      "+r.Ask.Run.Command())
		for _, does := range r.Ask.Run.Does {
			out = append(out, "  the yard says: "+does)
		}
		out = append(out, "  "+r.Ask.Run.TakenBack())
	case r.Ask.Secret != nil:
		out = append(out, "  "+r.Ask.Secret.Line(),
			"  it goes to a file only the yard's server reads; no agent is shown it")
	case len(r.Ask.Options) == 0:
		out = append(out, "  words wanted: answer with words")
	default:
		for i, o := range r.Ask.Options {
			line := fmt.Sprintf("  %d. %s - %s", i+1, o.Label, o.Consequence)
			if i+1 == r.Ask.Recommend {
				line += fmt.Sprintf(" (recommended: %s)", r.Ask.Why)
			}
			out = append(out, line)
		}
	}
	if len(r.Ask.About) > 0 {
		out = append(out, "  about "+strings.Join(r.Ask.About, ", "))
	}
	who := func(a string) string {
		if mark == nil {
			return a
		}
		return mark(a)
	}
	switch r.State {
	case AskAnswered:
		c := *r.Current
		switch {
		case r.Ask.Run != nil && c.Answer.Ran != nil:
			out = append(out, fmt.Sprintf("  %s approved, ran: %s", ViaMark(who(c.Note.Author), c.Note.Via), c.Answer.Ran.Command()))
			// A command that changed several things printed a line for each.
			for _, line := range strings.Split(c.Answer.Ran.Result, "\n") {
				out = append(out, "      "+line)
			}
			out = append(out, "  "+r.Ask.Run.TakenBack())
		case r.Ask.Secret != nil:
			out = append(out, fmt.Sprintf("  %s gave %s", ViaMark(who(c.Note.Author), c.Note.Via), strings.Join(c.Answer.Given, " and ")))
		case len(r.Ask.Options) == 0:
			out = append(out, fmt.Sprintf("  %s answered: %s", ViaMark(who(c.Note.Author), c.Note.Via), c.Answer.Words))
		default:
			prev := 0
			for _, a := range r.Answers {
				if a.Note.Seq == c.Note.Seq {
					break
				}
				// Only the immediately current answer is replaced. A later
				// answer puts the ask aside, so a pick after it is not a
				// change to the pick that preceded it.
				prev = a.Answer.Option
			}
			was := ""
			if prev != 0 {
				was = fmt.Sprintf(" (was %d until %s)", prev, c.Note.At.Local().Format("15:04"))
			}
			label := ""
			if c.Answer.Option >= 1 && c.Answer.Option <= len(r.Ask.Options) {
				label = " (" + r.Ask.Options[c.Answer.Option-1].Label + ")"
			}
			line := fmt.Sprintf("  %s picked %d%s%s", ViaMark(who(c.Note.Author), c.Note.Via), c.Answer.Option, was, label)
			if c.Answer.Words != "" {
				line += ": " + c.Answer.Words
			}
			out = append(out, line)
		}
	case AskAside:
		out = append(out, fmt.Sprintf("  back sooner if %s writes", r.Note.Author))
	case AskDeclined:
		c := *r.Current
		c.Note.Author = ViaMark(who(c.Note.Author), c.Note.Via)
		out = append(out, "  "+declinedLine(c))
	case AskTakenBack:
		c := *r.Current
		c.Note.Author = ViaMark(who(c.Note.Author), c.Note.Via)
		out = append(out, "  "+takenBackLine(c))
	}
	switch {
	case r.State == AskDeclined, r.State == AskTakenBack:
	case r.State == AskAnswered && len(r.Ask.Options) == 0:
	case r.Ask.Run != nil:
		out = append(out, fmt.Sprintf("  approve with: yardr bead answer %s %d --approve", r.Note.Bead, r.Note.Seq))
	case r.Ask.Secret != nil:
		out = append(out, fmt.Sprintf("  answer with: yardr bead answer %s %d --secret", r.Note.Bead, r.Note.Seq))
	case len(r.Ask.Options) == 0:
		out = append(out, fmt.Sprintf("  answer with: yardr bead answer %s %d --words \"<text>\"", r.Note.Bead, r.Note.Seq))
	default:
		out = append(out, fmt.Sprintf("  answer with: yardr bead answer %s %d <n> [words]", r.Note.Bead, r.Note.Seq))
	}
	return out
}

// NoteBlocks renders a bead's notes for humans: a plain note as its time,
// author and text, an ask as numbered text with its state, an answer
// folded into its ask. An answer that names no ask of the bead says so
// where it stands. Nobody reads the raw form.
func NoteBlocks(notes []Note, now time.Time) []string {
	return blocks(notes, now, nil)
}

// NoteBlocksMarked renders a bead's notes as NoteBlocks does, with an
// answer's author from another yard marked as mail is (LinesMarked).
func NoteBlocksMarked(notes []Note, now time.Time, as map[string]string) []string {
	mark := func(a string) string { return MarkedAuthor(a, as) }
	return blocks(notes, now, mark)
}

func blocks(notes []Note, now time.Time, mark func(string) string) []string {
	who := func(a string) string {
		if mark == nil {
			return a
		}
		return mark(a)
	}
	resolved := ResolveAsks(notes, now)
	byAsk := map[uint64]ResolvedAsk{}
	claimed := map[uint64]bool{}
	for _, r := range resolved {
		byAsk[r.Note.Seq] = r
		for _, a := range r.Answers {
			claimed[a.Note.Seq] = true
		}
	}
	var out []string
	for _, n := range notes {
		if n.Kind == NoteAnswer && claimed[n.Seq] {
			continue
		}
		if r, ok := byAsk[n.Seq]; ok {
			out = append(out, strings.Join(r.lines(mark), "\n"))
			continue
		}
		if n.Kind == NoteAnswer {
			out = append(out, fmt.Sprintf("[%s %s]\nanswer %d names no ask of this bead",
				n.At.Local().Format("2006-01-02 15:04"), who(n.Author), n.Seq))
			continue
		}
		out = append(out, fmt.Sprintf("[%s %s]\n%s",
			n.At.Local().Format("2006-01-02 15:04"), ViaMark(who(n.Author), n.Via), strings.TrimSpace(n.Text)))
	}
	return out
}

// askAge is how long ago t was, coarsely.
func askAge(t, now time.Time) string {
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
