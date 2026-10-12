// Asks are questions as data: one question with enumerable options, asked
// of one person, living as a note of kind ask on any bead in any stage.
// The person answers by picking an option, puts the question aside, or
// declines it, each a note of kind answer. Both are JSON in Note.Text, one
// schema each, parsed the same everywhere; prime, review and chat render
// them as numbered text, never the raw form.
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
// display; every pick stays as a note.
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
}

// Answer is the JSON of a note of kind answer: what one person did with
// one ask. Exactly one of a pick (Option), words alone (Words with no
// Option, Later or Decline), Later and Decline is set.
type Answer struct {
	// Ask is the ask note's seq.
	Ask uint64 `json:"ask"`
	// Option is the picked option's number, 1-based into the ask's
	// options, 0 for none.
	Option int `json:"option,omitempty"`
	// Words go with a pick, answer an ask with no options, or say why a
	// question is declined.
	Words string `json:"words,omitempty"`
	// Later puts the ask aside; Decline ends it with no option picked.
	Later   bool `json:"later,omitempty"`
	Decline bool `json:"decline,omitempty"`
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
// A To of <person>@<peer> asks a person of a linked yard: in the same
// write that stores the ask, the yard queues one message of kind ask for
// the peer (peerAsk), where it stands among the person's decisions. The
// gates are mail's: the peer must be one, the link must carry ask out, a
// crew member asking must name the peer in its peers setting, and a bead's
// agent may not ask across. A refused ask stores nothing.
func (s *Store) AddAsk(ctx context.Context, id string, ask Ask, actor string) (uint64, error) {
	ask.Question = strings.TrimSpace(ask.Question)
	if ask.Question == "" {
		return 0, fmt.Errorf("an ask needs a question")
	}
	ask.To = strings.TrimSpace(ask.To)
	if ask.To == "" {
		return 0, fmt.Errorf("an ask needs --to: who must answer")
	}
	var context []string
	for _, line := range ask.Context {
		if line = strings.TrimSpace(line); line != "" {
			context = append(context, line)
		}
	}
	if len(context) == 0 {
		return 0, fmt.Errorf("an ask needs --context: two to five lines of what it is about, at least one")
	}
	ask.Context = context
	for i, o := range ask.Options {
		ask.Options[i].Label = strings.TrimSpace(o.Label)
		ask.Options[i].Consequence = strings.TrimSpace(o.Consequence)
		o = ask.Options[i]
		if o.Label == "" {
			return 0, fmt.Errorf("option %d needs a label: --option \"<label>: <consequence>\"", i+1)
		}
		if o.Consequence == "" {
			return 0, fmt.Errorf("option %d (%s) needs a consequence: --option %q", i+1, o.Label, o.Label+": <what picking it does>")
		}
		if strings.EqualFold(o.Label, YouDecide) {
			return 0, fmt.Errorf("option %d: yardr adds %q itself, to every ask with options: leave it out", i+1, YouDecide)
		}
	}
	switch {
	case ask.Recommend != 0 && len(ask.Options) == 0:
		return 0, fmt.Errorf("--recommend %d: the ask has no options to recommend", ask.Recommend)
	case ask.Recommend < 0 || ask.Recommend > len(ask.Options):
		return 0, fmt.Errorf("--recommend %d: the ask has options 1..%d", ask.Recommend, len(ask.Options))
	case ask.Recommend != 0 && strings.TrimSpace(ask.Why) == "":
		return 0, fmt.Errorf("--recommend %d needs --why: why this option, in a line", ask.Recommend)
	case ask.Recommend == 0 && strings.TrimSpace(ask.Why) != "":
		return 0, fmt.Errorf("--why needs --recommend: which option it is for")
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
	if outside {
		switch {
		case !peerMailName.MatchString(to):
			return 0, fmt.Errorf("ask to %q: that is no person's name", to)
		case !peerMailName.MatchString(actor):
			return 0, fmt.Errorf("ask from %q: a name of letters, digits, '.', '_', '-' and '/' is what another yard is told", actor)
		}
	}
	var seq uint64
	err := s.write(func(w *wtx) error {
		row, err := w.loadBead(id)
		if err != nil {
			return err
		}
		// Echo handles, whatever form of id was typed.
		var handles []string
		var aboutBeads []peerAskBead
		seen := map[string]bool{}
		for _, b := range about {
			other, err := w.loadBead(b)
			if err != nil {
				return fmt.Errorf("about: %w", err)
			}
			if seen[other.UUID] {
				return fmt.Errorf("about: %s is named twice", other.ID)
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
				return err
			}
			var r CrewMember
			if ok, err := get(w.Tx, bCrew, actor, &r); err != nil {
				return err
			} else if ok && !slices.Contains(r.MayMail(), peer) {
				return fmt.Errorf("crew member %s may not ask %s@%s: its definition does not allow it. The operator allows it with yardr crew set %s %s=%s",
					actor, to, peer, actor, MailPeers, strings.Join(append(r.MayMail(), peer), ","))
			} else if !ok && w.Bucket(bUsers).Get([]byte(actor)) == nil {
				if _, this, err := notOperator(ctx, w.Tx, actor); err != nil {
					return err
				} else if this != "" {
					return fmt.Errorf("only a crew member allowed %s (yardr crew set <crew member> %s=%s) or a user of the yard (yardr user list) may ask another yard; this is %s",
						peer, MailPeers, peer, this)
				}
			}
			if !p.carries(PeerAsk, false) {
				return fmt.Errorf("peer %s: the link does not carry ask out: the operator allows it with yardr peer allow %s --send ask", peer, peer)
			}
		}
		ask.About = handles
		text, err := json.Marshal(ask)
		if err != nil {
			return err
		}
		n, err := w.addNoted(row, NoteAsk, string(text), actor)
		if err != nil {
			return err
		}
		seq = n.Seq
		if !outside {
			return nil
		}
		body, err := json.Marshal(peerAsk{
			V: PeerAskVersion, To: to, From: actor,
			Bead: row.ID, Title: row.Title, Topic: topicOf(row.Bead), Stage: row.Stage,
			Labels: row.Labels, Body: cutNote(row.Body),
			Seq: seq, Since: n.At, Ask: ask, About: aboutBeads,
		})
		if err != nil {
			return err
		}
		m := PeerMessage{ID: newPeerMessageID(), Kind: PeerAsk, Body: body}
		if err := m.check(); err != nil {
			return err
		}
		out, err := w.queuePeer(p, m, actor)
		if err != nil {
			return err
		}
		if out == nil {
			// Checked above, in the same write: the link cannot have
			// stopped carrying ask since.
			return fmt.Errorf("peer %s: the link does not carry ask out: the operator allows it with yardr peer allow %s --send ask", peer, peer)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return seq, nil
}

// AddAnswer records ans on the bead id as a note of kind answer, written
// by actor: a pick of one of the ask's options with any words, words
// alone on an ask with no options, Later, or Decline with why.
//
// A declined ask and an ask answered with words take no further answer; a
// picked ask takes only a newer pick, which supersedes for display. Words
// alone on an ask with options are refused: they go as a plain note and
// leave the ask open.
func (s *Store) AddAnswer(ctx context.Context, id string, ans Answer, actor string) error {
	ans.Words = strings.TrimSpace(ans.Words)
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
	if ans.Decline && ans.Words == "" {
		return fmt.Errorf("declining ask %d needs words: --decline \"<why you will not decide it>\"", ans.Ask)
	}
	if ans.Later && (ans.Option != 0 || ans.Words != "") {
		return fmt.Errorf("--later puts ask %d aside with nothing else", ans.Ask)
	}
	return s.write(func(w *wtx) error {
		row, err := w.loadBead(id)
		if err != nil {
			return err
		}
		notes, err := beadNotes(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		var askNote *Note
		for i := range notes {
			if notes[i].Seq == ans.Ask {
				askNote = &notes[i]
			}
		}
		if askNote == nil {
			return fmt.Errorf("bead %s has no ask %d", row.ID, ans.Ask)
		}
		if askNote.Kind != NoteAsk {
			return fmt.Errorf("note %d on bead %s is not an ask", ans.Ask, row.ID)
		}
		ask, err := ParseAsk(*askNote)
		if err != nil {
			return err
		}
		resolved := ResolveAsks(notes, now())
		var current *ResolvedAsk
		for i := range resolved {
			if resolved[i].Note.Seq == ans.Ask {
				current = &resolved[i]
			}
		}
		if current == nil {
			return fmt.Errorf("bead %s has no ask %d", row.ID, ans.Ask)
		}
		switch current.State {
		case AskDeclined:
			return fmt.Errorf("ask %d was declined: %s", ans.Ask, declinedLine(*current.Current))
		case AskAnswered:
			if len(ask.Options) == 0 {
				return fmt.Errorf("ask %d is answered already", ans.Ask)
			}
			if ans.Option == 0 {
				return fmt.Errorf("ask %d is answered already: a newer pick replaces it, nothing else", ans.Ask)
			}
		}
		switch {
		case ans.Option != 0 && len(ask.Options) == 0:
			return fmt.Errorf("ask %d wants words: answer with yardr bead answer %s %d --words \"<text>\"", ans.Ask, row.ID, ans.Ask)
		case ans.Option != 0 && (ans.Option < 1 || ans.Option > len(ask.Options)):
			return fmt.Errorf("ask %d has options 1..%d", ans.Ask, len(ask.Options))
		case ans.Option == 0 && !ans.Later && !ans.Decline && len(ask.Options) > 0:
			return fmt.Errorf("ask %d has options: words alone go as yardr bead note %s \"...\" and leave the ask open; pick one with yardr bead answer %s %d <n>",
				ans.Ask, row.ID, row.ID, ans.Ask)
		case ans.Option == 0 && !ans.Later && !ans.Decline && ans.Words == "":
			return fmt.Errorf("ask %d wants words: yardr bead answer %s %d --words \"<text>\"", ans.Ask, row.ID, ans.Ask)
		}
		text, err := json.Marshal(ans)
		if err != nil {
			return err
		}
		_, err = w.addNoted(row, NoteAnswer, string(text), actor)
		return err
	})
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

// declinedLine is a declined ask's end as every view shows it.
func declinedLine(a ResolvedAnswer) string {
	return fmt.Sprintf("%s declined to decide this: %s", a.Note.Author, a.Answer.Words)
}

// AskState is what an ask waits for: nothing yet, a pick that stands, a
// return from being put aside, or nobody, declined.
type AskState int

const (
	// AskOpen waits for an answer.
	AskOpen AskState = iota
	// AskAnswered has a pick, or words on an ask with no options, that stands.
	AskAnswered
	// AskAside was put aside and has not come back yet.
	AskAside
	// AskDeclined was declined: ended with no option picked.
	AskDeclined
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
func (r ResolvedAsk) Lines() []string {
	if r.AskErr != nil {
		return []string{fmt.Sprintf("ask %d: not a question any more (%v)", r.Note.Seq, r.AskErr)}
	}
	head := fmt.Sprintf("ask %d · %s · to %s · asked by %s · %s ago",
		r.Note.Seq, r.State, r.Ask.To, r.Note.Author, askAge(r.Note.At, r.now))
	if r.State == AskAside {
		head = fmt.Sprintf("ask %d · put aside until %s · to %s · asked by %s · %s ago",
			r.Note.Seq, r.ReturnAt.Local().Format("2006-01-02 15:04"), r.Ask.To, r.Note.Author, askAge(r.Note.At, r.now))
	}
	out := []string{head, r.Ask.Question}
	for _, line := range r.Ask.Context {
		out = append(out, "  "+line)
	}
	if len(r.Ask.Options) == 0 {
		out = append(out, "  words wanted: answer with words")
	} else {
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
	switch r.State {
	case AskAnswered:
		c := *r.Current
		if len(r.Ask.Options) == 0 {
			out = append(out, fmt.Sprintf("  %s answered: %s", c.Note.Author, c.Answer.Words))
		} else {
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
			line := fmt.Sprintf("  %s picked %d%s%s", c.Note.Author, c.Answer.Option, was, label)
			if c.Answer.Words != "" {
				line += ": " + c.Answer.Words
			}
			out = append(out, line)
		}
	case AskAside:
		out = append(out, fmt.Sprintf("  back sooner if %s writes", r.Note.Author))
	case AskDeclined:
		out = append(out, "  "+declinedLine(*r.Current))
	}
	switch {
	case r.State == AskDeclined:
	case r.State == AskAnswered && len(r.Ask.Options) == 0:
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
			out = append(out, strings.Join(r.Lines(), "\n"))
			continue
		}
		if n.Kind == NoteAnswer {
			out = append(out, fmt.Sprintf("[%s %s]\nanswer %d names no ask of this bead",
				n.At.Local().Format("2006-01-02 15:04"), n.Author, n.Seq))
			continue
		}
		out = append(out, fmt.Sprintf("[%s %s]\n%s",
			n.At.Local().Format("2006-01-02 15:04"), n.Author, strings.TrimSpace(n.Text)))
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
