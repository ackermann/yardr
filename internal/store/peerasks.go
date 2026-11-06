package store

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// PeerAsk is an ask of a person of a linked yard (docs/peers.md): the
// question stands among their decisions there, and their pick comes back
// onto the bead it was asked on. It is queued in the outbox for the peer
// in the write that stores the ask (AddAsk), and crosses as any message
// does. One kind covers both directions: an answer is a message of kind
// ask whose Re is the ask message's id, as a pong is a ping with Re.
const PeerAsk = "ask"

// PeerAskVersion is the v of the ask bodies this build writes. A body of
// another v is refused (RefusedVersion), as a bead's is.
const PeerAskVersion = 1

// Why an ask that came in was not taken, as the sender is told, beside
// RefusedKind and RefusedTooLarge.
const (
	// RefusedNoUser is an ask for a name that is exposed and no user of
	// this yard has: crew members have no decisions queue.
	RefusedNoUser = "no_such_user"
	// RefusedNotAsk is a body that is no ask: no question, a context line
	// holding a break, a sender's name that is none, a bead that is none.
	RefusedNotAsk = "not_ask"
)

// Why an answer that came home was not taken, as the yard it was given in
// is told. The last three are the ask's end: no later answer changes them,
// so the yard that hears one marks its copy of the question with it
// (answerGivenUp) instead of opening it again.
const (
	// RefusedWrongPerson is an answer by another name than the one the
	// ask is addressed to.
	RefusedWrongPerson = "wrong_person"
	// RefusedNoSuchAsk is an answer to no ask this yard asked that yard:
	// no such bead, no ask of that seq on it, or one addressed elsewhere.
	RefusedNoSuchAsk = "no_such_ask"
	// RefusedAskDeclined and RefusedAskAnswered are an answer to an ask
	// that takes none any more, naming the state it ended in.
	RefusedAskDeclined = "ask_declined"
	RefusedAskAnswered = "ask_answered"
	// RefusedAskTakenBack is an answer to an ask its asker took back.
	RefusedAskTakenBack = "ask_taken_back"
	// RefusedAskSeatLeft is an answer to the yard's login card on a crew
	// journal after its seat left the login screen: the yard takes the
	// card back. Local answers alone are refused with it (addAnswer);
	// peer answers never see it.
	RefusedAskSeatLeft = "ask_seat_left"
)

// askOutcomes are the refusals that end a foreign ask.
var askOutcomes = []string{RefusedAskDeclined, RefusedAskAnswered, RefusedAskTakenBack, RefusedNoSuchAsk}

// peerAsk is the body of a message of kind PeerAsk carrying the question:
// v, the bead's handle, title, topic and stage, the bead's body cut at
// 16 KB saying so, the ask's seq, asker and since, the ask's JSON whole,
// and each about-bead as id, title and stage. To is the person of the
// peer's yard who must answer, From the crew member or user of this yard
// who asked.
type peerAsk struct {
	V      int           `json:"v"`
	To     string        `json:"to"`
	From   string        `json:"from"`
	Bead   string        `json:"bead"`
	Title  string        `json:"title"`
	Topic  string        `json:"topic,omitempty"`
	Stage  string        `json:"stage"`
	Labels []string      `json:"labels,omitempty"`
	Body   string        `json:"body,omitempty"`
	Seq    uint64        `json:"seq"`
	Since  time.Time     `json:"since"`
	Ask    Ask           `json:"ask"`
	About  []peerAskBead `json:"about,omitempty"`
}

// peerAnswer is the body of a message of kind PeerAsk that answers one: its
// Re is the ask message's id. It names the ask by the bead and the ask's
// seq, as the asking yard has them, and holds the pick, the words, Later
// or Decline. From is the person who answered, To the asker it goes home
// to.
//
// Settled makes it a settle instead, which goes the other way: the asking
// yard's word that the ask ended there, SettledAnswered, SettledDeclined or
// SettledTakenBack, or that it was put aside there, SettledAside, for a yard
// the ask stands in (settleCopies). It names the bead and the seq as an
// answer does and carries nothing else, but for an aside its return: no text
// of anyone's, no pick and no name. A build from before it reads it as an
// answer by nobody and refuses it as no ask.
type peerAnswer struct {
	V       int    `json:"v"`
	To      string `json:"to"`
	From    string `json:"from"`
	Bead    string `json:"bead"`
	Seq     uint64 `json:"seq"`
	Option  int    `json:"option,omitempty"`
	Words   string `json:"words,omitempty"`
	Later   bool   `json:"later,omitempty"`
	Decline bool   `json:"decline,omitempty"`
	Settled string `json:"settled,omitempty"`
	// ReturnAt is when an aside comes back, on a settle that says
	// SettledAside; absent on any other settle.
	ReturnAt *time.Time `json:"return_at,omitempty"`
}

// peerAskBead is one about-bead of a cross-yard ask, as carried: id,
// title and stage, text the other yard reads but never follows.
type peerAskBead struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Stage string `json:"stage"`
}

// bPeerAsks holds the foreign asks this yard took, by the peer's key and
// the message id: the peer by its key, never by the name this yard has
// for it, so a rename changes nothing in it. It is no part of any bead:
// what anyone may edit of a bead cannot change whose question it is.
var bPeerAsks = []byte("peer_asks")

// bAskCopies holds where an ask was sent, by the bead's UUID and the ask's
// seq. It stays after an ask ends as proof of which peers received it, so
// a late reach answer is accepted only from a name that got the copy.
// askCopy.Ended prevents another settle from being sent for that copy. An
// ask addressed to <person>@<peer> is in it too, as the one copy that yard
// holds, to be told when it is taken back.
var bAskCopies = []byte("ask_copies")

func init() { allBuckets = append(allBuckets, bPeerAsks, bAskCopies) }

// askCopy is one copy of an ask: the peer by its key, the name it was sent
// to there, and the message that carried it. As is "" for an ask addressed
// to the name itself, which no reach sent. Ended records that the local
// ask ended; the record stays as proof of the copy, but is not settled again.
type askCopy struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	As      string `json:"as"`
	Message string `json:"message"`
	Ended   bool   `json:"ended,omitempty"`
}

// foreignAsk is a question another yard asked a person of this yard: the
// message id, the peer by its key, the sender's bead, title, topic, stage
// and cut body, the ask's seq, asker, since and JSON, and the about-beads
// as text. TakenAt is when this yard took it, by its own clock: UserAsks
// places it by then, never by the sender's clock.
//
// Answers are what the person did with it here, oldest first: each was
// queued home as a message, and stands here from then on, since the queue
// is the person's and they have dealt with it. One whose message is given
// up is taken out again, which opens the question as it was. Outcome is
// the asking yard's word that the ask ended there (askOutcomes), heard as
// the refusal of a late answer or as a settle (takeSettle): it stands
// over every answer, and the ask takes none after it. An aside is that
// yard's word that it was put aside there instead, heard as a settle: it
// stands only while the person's own answers leave the ask open, and the
// ask still takes one.
type foreignAsk struct {
	Message string        `json:"message"`
	Key     string        `json:"key"`
	To      string        `json:"to"`
	From    string        `json:"from"`
	Bead    string        `json:"bead"`
	Title   string        `json:"title"`
	Topic   string        `json:"topic,omitempty"`
	Stage   string        `json:"stage"`
	Labels  []string      `json:"labels,omitempty"`
	Body    string        `json:"body,omitempty"`
	Seq     uint64        `json:"seq"`
	Since   time.Time     `json:"since"`
	Ask     Ask           `json:"ask"`
	About   []peerAskBead `json:"about,omitempty"`
	TakenAt time.Time     `json:"taken_at"`

	Answers   []foreignAnswer `json:"answers,omitempty"`
	Outcome   string          `json:"outcome,omitempty"`
	OutcomeAt *time.Time      `json:"outcome_at,omitempty"`
	// AsideAt is when the asking yard's word came that the ask was put
	// aside there, AsideUntil when it comes back, as the settle carried
	// it: until then the ask stands in the person's decided as put aside
	// there. An end there clears both.
	AsideAt    *time.Time `json:"aside_at,omitempty"`
	AsideUntil *time.Time `json:"aside_until,omitempty"`
}

// asided reports whether the ask stands put aside by the asking yard's
// word at the time at: told, and not come back yet.
func (f foreignAsk) asided(at time.Time) bool {
	return f.AsideAt != nil && f.AsideUntil != nil && at.Before(*f.AsideUntil)
}

// foreignAnswer is one answer to a foreign ask, as it was queued home: the
// answer's message, what it says, who gave it and when, by this yard's
// clock.
type foreignAnswer struct {
	Message string    `json:"message"`
	Answer  Answer    `json:"answer"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
}

// resolved is the foreign ask with its answers and its state at the time
// at, as this yard knows it: the ask and what was answered here, read as
// the notes of a bead are (ResolveAsks), so one set of rules says what
// stands. id is the bead as bead@peer and asker who asked as name@peer.
// Later is 24 hours by this yard's clock and nothing else: the asker's
// next note does not cross. An Outcome is not in it; the caller reads that
// first.
func (f foreignAsk) resolved(id, asker string, at time.Time) ResolvedAsk {
	return ResolveAsks(f.notes(id, asker), at)[0]
}

// row is the foreign ask as a row of the person's queue: on bead@peer,
// asked by name@peer, by the name this yard has for the link p today.
func (f foreignAsk) row(p *Peer) OpenAsk {
	return OpenAsk{
		Bead: f.Bead + "@" + p.Name, BeadTitle: f.Title, Topic: f.Topic,
		Ask: f.Seq, Question: f.Ask.Question,
		Asker: f.From + "@" + p.Name, Since: f.Since,
	}
}

// notes are the foreign ask and its answers as the notes of a bead would
// hold them: the ask under its own seq, the answers after it in order.
func (f foreignAsk) notes(id, asker string) []Note {
	text, _ := json.Marshal(f.Ask)
	notes := []Note{{Bead: id, Seq: f.Seq, Kind: NoteAsk, Author: asker, Text: string(text), At: f.Since}}
	for i, a := range f.Answers {
		text, _ := json.Marshal(a.Answer)
		notes = append(notes, Note{Bead: id, Seq: f.Seq + 1 + uint64(i), Kind: NoteAnswer, Author: a.By, Text: string(text), At: a.At})
	}
	return notes
}

// checkAsk decides whether the ask m brings from p is taken: it returns
// the ask, or why not. Every ask that comes counts toward the peer's hour
// beside mail, the refused too, so a yard that is refused cannot keep
// asking who lives here. A refusal is logged as peer_message_refused, but
// of those over the limit only the first of the hour: the rest say nothing
// new.
//
// The names the link exposes to p (Peer.Expose) are the only ones an ask
// reaches, and only a user of this yard has a decisions queue: anything
// else is refused as not exposed whether or not anyone here has it, and an
// exposed name nobody has as a user as no such user. tk is what an answer
// that moves its bead needs of its flows (taking).
func (w *wtx) checkAsk(p *Peer, m PeerMessage, way string, tk *taking) (*peerAsk, string, error) {
	p.hour(now())
	p.Mails++
	extra := map[string]any{"direction": "in", "way": way}
	var ask peerAsk
	err := json.Unmarshal(m.Body, &ask)
	if err == nil {
		ask.Title = cleanTitle(ask.Title)
	}
	reason := ""
	switch {
	case p.Mails > p.MailsAnHour():
		reason = RefusedRate
		if p.MailLimited {
			return nil, reason, nil
		}
		p.MailLimited = true
		extra["limit"], extra["until"] = p.MailsAnHour(), p.MailSince.Add(PeerMailWindow)
	case len(m.Body) > maxPeerBody(m.Kind):
		reason = RefusedTooLarge
	case err != nil:
		reason = RefusedNotAsk
	case ask.V != PeerAskVersion:
		extra["v"], extra["known"] = ask.V, PeerAskVersion
		reason = RefusedVersion
	case m.Re != "":
		// An answer, whose Re is the ask message's id, or the asking
		// yard's word that the ask ended there (takeSettle).
		if reason, err = w.takeAnswer(p, m, extra, tk); err != nil || reason == "" {
			return nil, "", err
		}
	case !peerMailName.MatchString(ask.From):
		reason = RefusedNotAsk
	case !validID.MatchString(ask.Bead) || isUUID(ask.Bead):
		reason = RefusedNotAsk
	case ask.Title == "" || !utf8.ValidString(ask.Title):
		reason = RefusedNotAsk
	case !peerStage.MatchString(ask.Stage):
		reason = RefusedNotAsk
	case !utf8.ValidString(ask.Topic) || !utf8.ValidString(ask.Body) ||
		slices.ContainsFunc(ask.Labels, func(l string) bool { return !utf8.ValidString(l) }):
		reason = RefusedNotAsk
	case ask.Seq == 0 || ask.Since.IsZero():
		reason = RefusedNotAsk
	case !validAsk(ask.To, ask.Ask):
		reason = RefusedNotAsk
	case slices.ContainsFunc(ask.About, func(b peerAskBead) bool {
		return !validID.MatchString(b.ID) || isUUID(b.ID) || !utf8.ValidString(b.Title) || !peerStage.MatchString(b.Stage)
	}):
		reason = RefusedNotAsk
	case askBytes(ask.Ask) > MaxPeerMailBytes || len(ask.Body) > MaxPeerNoteBytes:
		reason = RefusedTooLarge
	case !peerMailName.MatchString(ask.To) || !slices.Contains(p.Expose, ask.To):
		reason = RefusedNotExposed
		// Whom it asked for is the operator's to see, when it is a name.
		if peerMailName.MatchString(ask.To) {
			extra["to"] = ask.To
		}
	case w.Bucket(bUsers).Get([]byte(ask.To)) != nil:
		return &ask, "", nil
	default:
		// Exposed and no user since, which unexpose sees to for one
		// removed by name: said as no such user, since crew members
		// have no decisions queue either.
		reason = RefusedNoUser
		extra["to"] = ask.To
	}
	extra["reason"] = reason
	return nil, reason, w.event("", "peer_message_refused", "peer:"+p.Name, peerEvent(p.Name, m, extra))
}

// validAsk reports whether a is an ask to holds: a question, context of
// one line each, the options each naming its consequence, the
// recommendation with its why, and the beads it is about. To is the person
// of this yard who must answer; the ask's To is that person at the
// sender's yard for this one, person@peer as the sender calls this yard.
func validAsk(to string, a Ask) bool {
	if strings.TrimSpace(a.Question) == "" || !utf8.ValidString(a.Question) {
		return false
	}
	// Nothing of a secret crosses a link: another yard's word is not what
	// a card here says a value is for.
	if a.Secret != nil {
		return false
	}
	// Nor a command to approve: what another yard's operator runs is
	// approved in that yard, and a card here would offer what cannot be.
	if a.Run != nil {
		return false
	}
	askTo, peer, ok := strings.Cut(a.To, "@")
	if !ok || askTo != to || peer == "" || strings.Count(a.To, "@") != 1 {
		return false
	}
	context := 0
	for _, line := range a.Context {
		if !utf8.ValidString(line) {
			return false
		}
		// A context line is one line, as an ask written here: a break in
		// it would continue at the column of the yard's own lines on
		// the card.
		if strings.Contains(line, "\n") {
			return false
		}
		if strings.TrimSpace(line) != "" {
			context++
		}
	}
	if context == 0 {
		return false
	}
	for i, o := range a.Options {
		if strings.TrimSpace(o.Label) == "" || strings.TrimSpace(o.Consequence) == "" ||
			!utf8.ValidString(o.Label) || !utf8.ValidString(o.Consequence) {
			return false
		}
		if strings.EqualFold(o.Label, YouDecide) && i != len(a.Options)-1 {
			return false
		}
	}
	switch {
	// A stage's card offers its own options alone, with no You decide (stageAsk).
	case len(a.Options) > 0 && a.Stage == "" && a.Options[len(a.Options)-1].Label != YouDecide:
		return false
	case a.Recommend < 0 || a.Recommend > len(a.Options):
		return false
	case a.Recommend != 0 && a.Options[a.Recommend-1].Label == YouDecide:
		return false
	case a.Recommend != 0 && strings.TrimSpace(a.Why) == "":
		return false
	case a.Recommend == 0 && strings.TrimSpace(a.Why) != "":
		return false
	case !utf8.ValidString(a.Why):
		return false
	}
	seen := map[string]bool{}
	for _, b := range a.About {
		if !validID.MatchString(b) || isUUID(b) || seen[b] {
			return false
		}
		seen[b] = true
	}
	return true
}

// askBytes is what an ask's question, context and options together weigh:
// over mail's 16 KB the ask is refused as too large.
func askBytes(a Ask) int {
	n := len(a.Question)
	for _, line := range a.Context {
		n += len(line)
	}
	for _, o := range a.Options {
		n += len(o.Label) + len(o.Consequence)
	}
	return n
}

// deliverAsk keeps the ask m brought from p as a foreign ask, keyed by the
// peer's key, and logs its arrival as a noted event naming the foreign
// bead bead@peer, which clients receive as stored: footplate re-reads
// UserAsks on it with no change. The peer part is this yard's own name for
// the link, which the sender cannot choose. The yard that takes a question
// into a person's queue tells them of it outside the yard (queueAskPush).
func (w *wtx) deliverAsk(p *Peer, m PeerMessage, ask *peerAsk) error {
	// As an ask written here: a run of spaces in a line would pad it to
	// the terminal's width on the card, so another yard's words fold too.
	ask.Ask.Question = foldQuestion(ask.Ask.Question)
	for i, line := range ask.Ask.Context {
		ask.Ask.Context[i] = foldSpaces(line)
	}
	rec := foreignAsk{
		Message: m.ID, Key: p.Key, To: ask.To, From: ask.From,
		Bead: ask.Bead, Title: ask.Title, Topic: ask.Topic, Stage: ask.Stage,
		Labels: ask.Labels, Body: ask.Body, Seq: ask.Seq, Since: ask.Since,
		Ask: ask.Ask, About: ask.About, TakenAt: now(),
	}
	if err := put(w.Tx, bPeerAsks, join(p.Key, m.ID), rec); err != nil {
		return err
	}
	if err := w.event(ask.Bead+"@"+p.Name, "noted", ask.From+"@"+p.Name, map[string]any{"ask": ask.Seq}); err != nil {
		return err
	}
	if err := w.queueAskPush(ask.To, "", 0, join(p.Key, m.ID)); err != nil {
		return err
	}
	return w.queueFootAsk(ask.To, "", 0, join(p.Key, m.ID))
}

// foreignAsksOnBead are the foreign asks the peer whose key is key took on
// bead, as they are today: none of an earlier link with the same yard.
func foreignAsksOnBead(tx *bolt.Tx, key, bead string) ([]foreignAsk, error) {
	var out []foreignAsk
	err := each(tx, bPeerAsks, join(key, ""), func(_ []byte, rec *foreignAsk) error {
		if rec.Key == key && rec.Bead == bead {
			out = append(out, *rec)
		}
		return nil
	})
	return out, err
}

// dropPeerAsks drops the foreign asks taken from the peer with p's key:
// after peer rm a queue must stay answerable, and kept-but-dead questions
// are worse than mail, which is only history. It answers them by the user
// they were for, so each gets one mail listing theirs as text.
func (w *wtx) dropPeerAsks(p *Peer) (map[string][]foreignAsk, error) {
	var keys []string
	byUser := map[string][]foreignAsk{}
	err := each(w.Tx, bPeerAsks, "", func(k []byte, rec *foreignAsk) error {
		if rec.Key != p.Key {
			return nil
		}
		keys = append(keys, string(k))
		byUser[rec.To] = append(byUser[rec.To], *rec)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if err := w.Bucket(bPeerAsks).Delete([]byte(k)); err != nil {
			return nil, err
		}
	}
	for _, recs := range byUser {
		slices.SortFunc(recs, func(a, b foreignAsk) int {
			if a.TakenAt.Equal(b.TakenAt) {
				return strings.Compare(a.Message, b.Message)
			}
			if a.TakenAt.Before(b.TakenAt) {
				return -1
			}
			return 1
		})
	}
	return byUser, nil
}

// takeAnswer takes the answer m that p sent home to an ask of this yard,
// or says why not. The ask note is the record of what was asked: an answer
// is taken only for an ask that is on the named bead, with the named seq,
// addressed to a person of p's yard, and by that person. Trust gates
// nothing: the yard solicited exactly this answer from exactly this name,
// and the key says whose yard it is.
//
// An ask addressed to a user of this yard is answered from p too, by a
// name the link gives reach for that user as it arrives (Peer.reaches).
// Here the grant does gate: the yard solicited the answer by it, so it
// must stand, and with it taken back, or from any other name, the answer
// is to no ask this yard asked that yard. So is one to an ask the yard
// never copied to that name. The copy record stays after the ask ends,
// so a late answer can still be checked against it. What is taken lands as a
// note of kind answer by the rules every answer lands by (answerable),
// written by name@peer: a newer pick supersedes by note order, never by a
// clock. An ask that takes no further answer refuses one naming its state.
//
// An answer on a stage ask addressed across moves the bead the same way
// one given here does, under the same rules in the write that lands it
// (stageAnswer): a pick or words that fire an outcome move through Done's
// path, one that fires an advance as yardr bead advance moves, and anyone
// but the stage's deciders is refused whole. One over reach is words on
// the bead and moves nothing: the answer lands, and the bead stays in its
// stage for its members to move by hand. tk is what the move needs of the
// bead's flows (taking).
//
// A body that says settled is no answer but the word that an ask copied
// to this yard ended in p's (takeSettle).
//
// extra is what the refusal's event says, which checkAsk logs; it counted
// the message toward p's hour and held its body to its size and v.
func (w *wtx) takeAnswer(p *Peer, m PeerMessage, extra map[string]any, tk *taking) (string, error) {
	var a peerAnswer
	reason := ""
	if err := json.Unmarshal(m.Body, &a); err != nil {
		reason = RefusedNotAsk
	} else if a.Settled != "" {
		return w.takeSettle(p, m, a)
	}
	ans := Answer{Ask: a.Seq, Option: a.Option, Words: a.Words, Later: a.Later, Decline: a.Decline}
	var row *beadRow
	var text []byte
	var reach bool
	move := ""
	switch {
	case reason != "":
	case !peerMailName.MatchString(a.From) || !utf8.ValidString(a.Words) || answerShape(a.Bead, &ans) != nil:
		reason = RefusedNotAsk
	case len(a.Words) > MaxPeerMailBytes:
		reason = RefusedTooLarge
	case !validID.MatchString(a.Bead) || isUUID(a.Bead):
		reason = RefusedNoSuchAsk
	default:
		var err error
		row, err = w.loadBead(a.Bead)
		if errors.Is(err, ErrNotFound) || err == nil && row.ID != a.Bead {
			reason = RefusedNoSuchAsk
			break
		} else if err != nil {
			return "", err
		}
		notes, err := beadNotes(w.Tx, row.UUID)
		if err != nil {
			return "", err
		}
		// Whose it is before what state it is in: a yard that was not
		// asked learns nothing of the ask.
		i := slices.IndexFunc(notes, func(n Note) bool { return n.Seq == a.Seq && n.Kind == NoteAsk })
		if i < 0 {
			reason = RefusedNoSuchAsk
			break
		}
		ask, err := ParseAsk(notes[i])
		to, peer, outside := strings.Cut(ask.To, "@")
		switch {
		case err != nil:
			reason = RefusedNoSuchAsk
		case !outside:
			// An ask for a user of this yard, which p holds only as a
			// copy: whoever has no reach for them was not asked, and
			// neither was a name this ask was never copied to.
			reach = true
			if p.reaches(a.From) != ask.To || w.Bucket(bUsers).Get([]byte(ask.To)) == nil {
				reason = RefusedNoSuchAsk
			} else if copied, err := askCopiedTo(w.Tx, row.UUID, a.Seq, p.Key, a.From); err != nil {
				return "", err
			} else if !copied {
				reason = RefusedNoSuchAsk
			}
		case !p.called(w.Tx, peer):
			reason = RefusedNoSuchAsk
		case to != a.From:
			reason = RefusedWrongPerson
		}
		if reason != "" {
			break
		}
		// The answerer was checked above, against To and the reach
		// grant: the yard records the solicited answer as the
		// addressee's.
		why, err := answerable(row.ID, notes, ans, now(), ask.To, nil)
		if err != nil {
			if reason = why; reason == "" {
				reason = RefusedNotAsk
			}
			extra["detail"] = err.Error()
			break
		}
		// No local caller answers over a link: who asks is the name as
		// written, held to the stage's deciders with the trust mapping.
		move, why, err = w.stageAnswer(context.Background(), row, notes, ans, a.From+"@"+p.Name, tk.flows[row.UUID], tk.homes[row.UUID])
		if err != nil {
			if reason = why; reason == "" {
				reason = RefusedNotAsk
			}
			extra["detail"] = err.Error()
			break
		}
		// Over reach an answer is words on the bead and moves nothing:
		// the pick lands, and the bead stays in its stage.
		if reach {
			move = ""
		}
		if text, err = json.Marshal(ans); err != nil {
			return "", err
		}
	}
	if reason != "" {
		if a.Bead != "" && validID.MatchString(a.Bead) {
			extra["bead"], extra["ask"] = a.Bead, a.Seq
		}
		return reason, nil
	}
	by := a.From + "@" + p.Name
	// Before the note, which a refusal must leave unstored: an outcome
	// the flow has no more, a move it forbids, or a flow that does not
	// read refuses the answer with its reason, and the question stays
	// open for the flow to mend. A flow read for another configuration
	// meanwhile fails the batch instead, to be taken again.
	if move != "" {
		if err := w.checkStageMove(row, move, tk.flows[row.UUID]); err != nil {
			if errors.Is(err, errNeedRepo) {
				return "", changedMeanwhile(err)
			}
			extra["detail"] = err.Error()
			if a.Bead != "" && validID.MatchString(a.Bead) {
				extra["bead"], extra["ask"] = a.Bead, a.Seq
			}
			return RefusedNotAsk, nil
		}
	}
	if _, err := w.addNoted(row, NoteAnswer, string(text), by); err != nil {
		return "", err
	}
	if move != "" {
		if _, err := w.move(row.UUID, by, tk.flows[row.UUID], tk.homes[row.UUID], nil, stagePick(move)); err != nil {
			return "", err
		}
	}
	return "", w.settleCopies(row, ans, p, a.From)
}

// askCopiedTo reports whether the ask seq on the bead uuid was copied to
// name on the peer with key: bAskCopies keeps that proof after the ask ends,
// so a late answer over reach is taken only from a name that got the ask.
func askCopiedTo(tx *bolt.Tx, uuid string, seq uint64, key, name string) (bool, error) {
	var copies []askCopy
	if ok, err := get(tx, bAskCopies, join(uuid, seqKey(seq)), &copies); err != nil || !ok {
		return false, err
	}
	return slices.ContainsFunc(copies, func(c askCopy) bool { return c.Key == key && c.Name == name }), nil
}

// uncopied takes the copy that message carried off the record of where
// the ask seq on the bead uuid went: it was given up, so that yard has no
// question to be told the end of.
func (w *wtx) uncopied(uuid string, seq uint64, message string) error {
	key := join(uuid, seqKey(seq))
	var copies []askCopy
	if ok, err := get(w.Tx, bAskCopies, key, &copies); err != nil || !ok {
		return err
	}
	copies = slices.DeleteFunc(copies, func(c askCopy) bool { return c.Message == message })
	if len(copies) == 0 {
		return w.Bucket(bAskCopies).Delete([]byte(key))
	}
	return put(w.Tx, bAskCopies, key, copies)
}

// called reports whether name is what this yard calls p, or called it
// before a rename and calls no other yard now: an ask addressed to
// person@old is still that yard's to answer.
func (p *Peer) called(tx *bolt.Tx, name string) bool {
	if name == p.Name {
		return true
	}
	return slices.ContainsFunc(p.Was, func(w PeerWas) bool { return w.Name == name }) && tx.Bucket(bPeers).Get([]byte(name)) == nil
}

// foreignAsksForBead are the foreign asks that p asked on its bead,
// oldest seq first: what a view of bead@peer shows whole.
func foreignAsksForBead(tx *bolt.Tx, p *Peer, bead string) ([]foreignAsk, error) {
	var out []foreignAsk
	err := each(tx, bPeerAsks, "", func(_ []byte, rec *foreignAsk) error {
		if rec.Key == p.Key && rec.Bead == bead {
			out = append(out, *rec)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b foreignAsk) int { return cmp.Compare(a.Seq, b.Seq) })
	return out, nil
}

// foreignAskOf is the foreign ask that p asked as ask seq on its bead for
// actor, with the key it is kept under. One ask may stand twice, for two
// names with reach for one user: each answers their own copy. Where none
// of the ask's copies is for the actor, it is the first, so the caller
// refuses the answer as another's.
func foreignAskOf(tx *bolt.Tx, p *Peer, bead string, seq uint64, actor string) (*foreignAsk, string, error) {
	var found, first *foreignAsk
	key, firstKey, onBead := "", "", false
	err := each(tx, bPeerAsks, "", func(k []byte, rec *foreignAsk) error {
		if rec.Key != p.Key || rec.Bead != bead {
			return nil
		}
		onBead = true
		if rec.Seq != seq {
			return nil
		}
		if first == nil {
			c := *rec
			first, firstKey = &c, string(k)
		}
		if rec.To == actor && found == nil {
			c := *rec
			found, key = &c, string(k)
		}
		return nil
	})
	switch {
	case err != nil:
		return nil, "", err
	case found != nil:
		return found, key, nil
	case first != nil:
		return first, firstKey, nil
	case onBead:
		return nil, "", fmt.Errorf("bead %s@%s has no ask %d", bead, p.Name, seq)
	}
	return nil, "", fmt.Errorf("bead %s@%s: no question from %s stands on it: %w", bead, p.Name, p.Name, ErrNotFound)
}

// answeredHere says whether n, a note the peer of a stay sent among those
// written on its bead, is an answer someone of this yard gave to one of
// asks on bead, and who. The peer has it by <name>@<what it calls this
// yard>, but that alias may have changed since the ask was made. The asking
// yard knows the name from Ask.To and the answer from what it kept; both
// must match. A peer can make no other note this yard's own by it, only
// send back an answer given to it.
func answeredHere(asks []foreignAsk, bead string, n PeerNote) (string, bool) {
	name, peer, at := strings.Cut(n.Author, "@")
	if !at || strings.Count(n.Author, "@") != 1 || !peerMailName.MatchString(name) || !peerMailName.MatchString(peer) {
		return "", false
	}
	for _, f := range asks {
		to, _, addressed := strings.Cut(f.Ask.To, "@")
		if f.Bead != bead || !addressed || to != name {
			continue
		}
		for _, a := range f.Answers {
			text, err := json.Marshal(a.Answer)
			if a.By != name || a.Answer.Ask != f.Seq || err != nil || string(text) != n.Text {
				continue
			}
			return a.By, true
		}
	}
	return "", false
}

// answerForeign answers the question the yard peer asked as ask ans.Ask on
// its bead: the answer is queued home as a message of kind ask whose Re is
// the ask message's id, and stands on the foreign ask from now on, so the
// person's queue shows it dealt with. Nothing is written on any bead of
// this yard. Whether it lands is the asking yard's to say; a message given
// up comes back (answerGivenUp).
//
// The rules are those of an answer on a bead of this yard (answerable),
// with three of its own: only the person the question is for answers it,
// since the asking yard takes no one else's; words alone on an ask with
// options are refused with the way to write to the asker, since a note
// does not cross; and the link must carry ask out.
func (s *Store) answerForeign(ctx context.Context, bead, peer string, ans Answer, actor string) error {
	if len(ans.Words) > MaxPeerMailBytes {
		return fmt.Errorf("an answer to another yard carries words of up to %d bytes", MaxPeerMailBytes)
	}
	return s.writeVia(ctx, func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		p, err := loadPeer(w.Tx, peer)
		if err != nil {
			return err
		}
		rec, key, err := foreignAskOf(w.Tx, p, bead, ans.Ask, actor)
		if err != nil {
			return err
		}
		id, asker := bead+"@"+p.Name, rec.From+"@"+p.Name
		if err := w.may(ctx, actor, ActAnswerPeerAsk, Object{
			Kind: KindAsk, Name: id, Seq: ans.Ask, To: rec.To, Does: p.Name,
		}); err != nil {
			return err
		}
		switch {
		case rec.Outcome != "":
			return fmt.Errorf("ask %d on %s takes no further answer: %s", ans.Ask, id, outcomeWords(rec.Outcome, p.Name))
		case ans.Option == 0 && !ans.Later && !ans.Decline && len(rec.Ask.Options) > 0:
			return fmt.Errorf("ask %d has options: words for the asker go as mail, yardr mail %s - with the words on stdin, and leave the ask open; pick one with yardr bead answer %s %d <n>",
				ans.Ask, asker, id, ans.Ask)
		}
		// The answerer was checked above, against rec.To: the ask as
		// the notes carry it is addressed across, so it answers as
		// that addressee.
		if _, err := answerable(id, rec.notes(id, asker), ans, now(), rec.Ask.To, nil); err != nil {
			return err
		}
		if !p.carries(PeerAsk, false) {
			return fmt.Errorf("peer %s: the link does not carry ask out, and an answer goes home as one: the operator allows it with yardr peer allow %s --send ask", peer, peer)
		}
		body, err := json.Marshal(peerAnswer{
			V: PeerAskVersion, To: rec.From, From: actor, Bead: rec.Bead, Seq: rec.Seq,
			Option: ans.Option, Words: ans.Words, Later: ans.Later, Decline: ans.Decline,
		})
		if err != nil {
			return err
		}
		m := PeerMessage{ID: newPeerMessageID(), Kind: PeerAsk, Re: rec.Message, Body: body}
		if err := m.check(); err != nil {
			return err
		}
		if _, err := w.queuePeer(p, m, actor); err != nil {
			return err
		}
		at := now()
		rec.Answers = append(rec.Answers, foreignAnswer{Message: m.ID, Answer: ans, By: actor, At: at})
		if err := put(w.Tx, bPeerAsks, key, rec); err != nil {
			return err
		}
		// Put aside here: what is still queued for the ask goes, and
		// the aside's return is news too, told when its 24 hours are
		// over.
		if ans.Later && !ans.TakeBack && !ans.Decline {
			if err := dropFootAsk(w.Tx, rec.To, "", 0, key); err != nil {
				return err
			}
			if err := w.queueFootForeignAside(rec.To, key, m.ID, at.Add(LaterFor)); err != nil {
				return err
			}
		}
		return w.event(id, "noted", actor, map[string]any{"ask": rec.Seq})
	})
}

// outcomeState is the state a foreign ask stands in under the asking
// yard's word that it ended there: answered where it was answered, taken
// back where its asker took it back, declined where it was declined or is
// asked no more.
func outcomeState(outcome string) AskState {
	switch outcome {
	case RefusedAskAnswered:
		return AskAnswered
	case RefusedAskTakenBack:
		return AskTakenBack
	}
	return AskDeclined
}

// outcomeWords says how a foreign ask ended in the yard peer, by the
// refusal it gave.
func outcomeWords(outcome, peer string) string {
	switch outcome {
	case RefusedAskDeclined:
		return "it was declined in " + peer + "'s yard"
	case RefusedAskAnswered:
		return "it was answered in " + peer + "'s yard"
	case RefusedAskTakenBack:
		return "it was taken back in " + peer + "'s yard"
	}
	return "it is no longer asked in " + peer + "'s yard"
}

// asideWords says how a foreign ask stands put aside in the yard peer
// until returnAt, by the settle that told it. The time reads as a local
// aside's does where it is shown (ResolvedAsk's head).
func asideWords(peer string, returnAt time.Time) string {
	return "put aside in " + peer + "'s yard until " + returnAt.Local().Format("2006-01-02 15:04")
}

// askGivenUp tells of a message of kind ask that was given up, at
// whichever end this yard is: the question's asker that it was not
// delivered (askUndelivered), or the person whose answer did not land
// (answerGivenUp). No failure is silent but a settle's, which nothing
// waits on: an older build refuses it, and the question it was about is
// open there as it was before settles were.
func (w *wtx) askGivenUp(m *PeerOut, reason string, refused bool) error {
	if m.Kind != PeerAsk {
		return nil
	}
	// The daemon's reason for a post begins with the peer, which the
	// mail says already.
	reason = strings.TrimPrefix(reason, "peer "+m.Peer+": ")
	if m.Re == "" {
		return w.askUndelivered(m, reason)
	}
	var a peerAnswer
	if err := json.Unmarshal(m.Body, &a); err == nil && a.Settled != "" {
		return nil
	}
	return w.answerGivenUp(m, reason, refused)
}

// askUndelivered tells whoever asked the question m carried that it was
// given up, as mail from the yard. The ask stays open on its bead,
// readable and answerable there by hand.
//
// A copy of an ask for a user of this yard (AddAsk) is told to that user
// instead: the asker addressed nobody at the other yard. Which it is the
// ask note says, the record of what was asked.
func (w *wtx) askUndelivered(m *PeerOut, reason string) error {
	var ask peerAsk
	if err := json.Unmarshal(m.Body, &ask); err != nil {
		return nil
	}
	if row, err := w.loadBead(ask.Bead); err == nil {
		notes, err := beadNotes(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		if i := slices.IndexFunc(notes, func(n Note) bool { return n.Seq == ask.Seq && n.Kind == NoteAsk }); i >= 0 {
			// A copy that never arrived has no end to be told.
			if err := w.uncopied(row.UUID, ask.Seq, m.ID); err != nil {
				return err
			}
			if asked, err := ParseAsk(notes[i]); err == nil && !strings.Contains(asked.To, "@") {
				return w.copyUndelivered(asked.To, ask.To+"@"+m.Peer, ask.Ask.Question, ask.Bead, ask.Seq, m.ID, "of "+stamp(m.At)+" was not delivered: "+reason)
			}
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	data := map[string]any{"to": ask.From, "undelivered": m.ID, "text": fmt.Sprintf(
		"your question for %s@%s of %s (%q, ask %d on %s) was not delivered: %s. It stays open on the bead, where it is answered by hand: yardr bead answer %s %d",
		ask.To, m.Peer, stamp(m.At), ask.Ask.Question, ask.Seq, ask.Bead, reason, ask.Bead, ask.Seq)}
	switch {
	case w.Bucket(bCrew).Get([]byte(ask.From)) != nil:
	case w.Bucket(bUsers).Get([]byte(ask.From)) != nil:
		data["to_user"] = true
	default:
		return nil
	}
	return w.event("", "mail", "yard", data)
}

// copyUndelivered tells user, by mail from the yard, that the copy of the
// ask seq on bead that was for them at the address of another yard did not
// get there, and why. Nothing changed for the ask: it is open in this
// yard as it was. message is the copy's message, "" for one never queued.
func (w *wtx) copyUndelivered(user, address, question, bead string, seq uint64, message, why string) error {
	if w.Bucket(bUsers).Get([]byte(user)) == nil {
		return nil
	}
	if message == "" {
		why = "was not queued: " + why
	}
	data := map[string]any{"to": user, "to_user": true, "text": fmt.Sprintf(
		"a question for you (%q, ask %d on %s) was also sent to %s, and that copy %s. It is open in this yard, where it is answered: yardr bead answer %s %d",
		question, seq, bead, address, why, bead, seq)}
	if message != "" {
		data["undelivered"] = message
	}
	return w.event("", "mail", "yard", data)
}

// answerGivenUp undoes the answer whose message m was given up, on the
// foreign ask it answered, and tells the person by mail from the yard.
//
// The asking yard's word that the ask ended there (askOutcomes) marks the
// foreign ask with that outcome: opening it again would invite a pick that
// can never land. Anything else takes the answer out again, which opens
// the question as it was before it: a yard not reached in its tries, or a
// refusal another try may not meet. No queue shows as decided what never
// landed.
//
// reason is the refusal in this yard's words (PeerRefusal), which is all
// the daemon passes on of a post that was refused, so the outcome is read
// from those words.
func (w *wtx) answerGivenUp(m *PeerOut, reason string, refused bool) error {
	p, err := loadPeer(w.Tx, m.Peer)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	key := join(p.Key, m.Re)
	var rec foreignAsk
	if ok, err := get(w.Tx, bPeerAsks, key, &rec); err != nil || !ok {
		return err
	}
	i := slices.IndexFunc(rec.Answers, func(a foreignAnswer) bool { return a.Message == m.ID })
	if i < 0 {
		return nil
	}
	id, asker := rec.Bead+"@"+p.Name, rec.From+"@"+p.Name
	text := fmt.Sprintf("%s on %q (ask %d on %s, from %s) did not land: ", answerWords(rec.Answers[i].Answer, rec.Ask), rec.Ask.Question, rec.Seq, id, asker)
	outcome := ""
	if refused {
		for _, o := range askOutcomes {
			if strings.HasSuffix(reason, PeerRefusal(p.Name, PeerAsk, o)) {
				outcome = o
			}
		}
	}
	if outcome != "" {
		at := now()
		rec.Outcome, rec.OutcomeAt = outcome, &at
		text += outcomeWords(outcome, p.Name) + ". It takes no further answer."
	} else {
		rec.Answers = slices.Delete(rec.Answers, i, i+1)
		if refused {
			text += strings.TrimPrefix(reason, "refused by the peer: ") + "."
		} else {
			text += fmt.Sprintf("%s was not reached in %d tries since %s: %s.", p.Name, m.Attempts, stamp(m.At), reason)
		}
		at := now()
		if r := rec.resolved(id, asker, at); rec.Outcome != "" {
			// A settle came while the answer was on its way.
			text += " The question takes no answer all the same: " + outcomeWords(rec.Outcome, p.Name) + "."
		} else if r.State == AskOpen && !rec.asided(at) {
			text += " The question is open again."
		} else if r.State == AskOpen {
			// An aside came while the answer was on its way.
			text += " The question stands " + asideWords(p.Name, *rec.AsideUntil) + "."
		} else {
			text += " The question stands as " + r.State.String() + ", by what you did with it before."
		}
	}
	if err := put(w.Tx, bPeerAsks, key, rec); err != nil {
		return err
	}
	if w.Bucket(bUsers).Get([]byte(rec.To)) != nil {
		if err := w.event("", "mail", "yard", map[string]any{"to": rec.To, "to_user": true, "undelivered": m.ID, "text": text}); err != nil {
			return err
		}
	}
	// The queue changed: a client reads UserAsks again on it.
	return w.event(id, "noted", "yard", map[string]any{"ask": rec.Seq})
}

// answerWords names an answer to the person who gave it.
func answerWords(a Answer, ask Ask) string {
	switch {
	case a.Decline:
		return "your decline"
	case a.Later:
		return "putting it aside"
	case a.Option >= 1 && a.Option <= len(ask.Options):
		return fmt.Sprintf("your pick of %d (%s)", a.Option, ask.Options[a.Option-1].Label)
	}
	return "your answer"
}
