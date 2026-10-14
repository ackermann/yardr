package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/yardkey"
)

// homeYard is a yard linked with north, which asks north's mckean and is
// asked by north for its own mckean, with the clock in the test's hand.
type homeYard struct {
	*Store
	ctx context.Context
	key string
	now time.Time
	n   int
}

func newHomeYard(t *testing.T) *homeYard {
	t.Helper()
	y := &homeYard{ctx: context.Background(), now: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
	clock = func() time.Time { return y.now }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	y.Store = s
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	y.key = yardkey.Public(k)
	if _, err := s.AddPeer(y.ctx, Peer{Name: "north", Key: y.key}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{MailPeers: "north"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(y.ctx, "mckean", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllowPeer(y.ctx, "north", PeerAllow{Send: []string{PeerAsk}, Receive: []string{PeerAsk}, Expose: []string{"mckean"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	return y
}

// take is what became of the message north posted.
func (y *homeYard) take(t *testing.T, m PeerMessage) PeerTaken {
	t.Helper()
	got, err := y.PeerReceive(y.ctx, "north", y.key, []PeerMessage{m})
	if err != nil {
		t.Fatal(err)
	}
	return got[0]
}

// answer is an answer north sends home, under an id of its own.
func (y *homeYard) answer(a peerAnswer) PeerMessage {
	y.n++
	a.V, a.To = PeerAskVersion, "yardmaster"
	body, _ := json.Marshal(a)
	return PeerMessage{ID: fmt.Sprintf("ans%d", y.n), Kind: PeerAsk, Re: "the-ask", Body: body}
}

// asked is a bead of this yard with an ask on it, addressed to.
func (y *homeYard) asked(t *testing.T, ask Ask) (string, uint64) {
	t.Helper()
	b, err := y.CreateBead(y.ctx, NewBead{Title: "Move the links"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := y.AddAsk(y.ctx, b.ID, ask, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return b.ID, seq
}

func (y *homeYard) state(t *testing.T, bead string, seq uint64) ResolvedAsk {
	t.Helper()
	notes, err := y.Notes(y.ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range ResolveAsks(notes, y.now) {
		if r.Note.Seq == seq {
			return r
		}
	}
	t.Fatalf("bead %s has no ask %d", bead, seq)
	return ResolvedAsk{}
}

// yardMail is the text of the mail the yard wrote to name, oldest first.
func (y *homeYard) yardMail(t *testing.T, name string) []string {
	t.Helper()
	evs, err := y.Events(y.ctx, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if e.Kind == "mail" && e.Actor == "yard" && e.Data["to"] == name {
			out = append(out, e.Data["text"].(string))
		}
	}
	return out
}

func (y *homeYard) refusals(t *testing.T, reason string) int {
	t.Helper()
	evs, err := y.Events(y.ctx, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == "peer_message_refused" && e.Data["reason"] == reason {
			n++
		}
	}
	return n
}

func northAsk() Ask {
	a := linksAsk()
	a.To = "mckean@north"
	return a
}

// An answer a peer sends home lands as a note of kind answer on the bead
// the ask stands on, written by name@peer, by the rules any answer lands
// by: a newer pick stands and every pick stays, and the same id twice is
// taken once.
func TestPeerAnswerLands(t *testing.T) {
	y := newHomeYard(t)
	bead, seq := y.asked(t, northAsk())

	pick := y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 1, Words: "tonight"})
	if got := y.take(t, pick); got.Refused != "" || got.Duplicate {
		t.Fatalf("a pick sent home = %+v", got)
	}
	if got := y.take(t, pick); !got.Duplicate {
		t.Fatalf("the same answer twice = %+v", got)
	}
	r := y.state(t, bead, seq)
	if r.State != AskAnswered || len(r.Answers) != 1 || r.Current.Note.Author != "mckean@north" ||
		r.Current.Answer.Option != 1 || r.Current.Answer.Words != "tonight" {
		t.Fatalf("after the pick: %+v", r)
	}
	// It is shown as any pick is, by whom it is.
	if lines := strings.Join(r.Lines(), "\n"); !strings.Contains(lines, "mckean@north picked 1 (Move them now): tonight") {
		t.Fatalf("lines:\n%s", lines)
	}

	// A pick by hand in this yard, then a newer one from the peer: by note
	// order the newest stands, and all three stay.
	y.now = y.now.Add(time.Minute)
	if err := y.AddAnswer(y.ctx, bead, Answer{Ask: seq, Option: 2}, "jo"); err != nil {
		t.Fatal(err)
	}
	if r := y.state(t, bead, seq); r.Current.Note.Author != "jo" || r.Current.Answer.Option != 2 {
		t.Fatalf("after the pick by hand: %+v", r.Current)
	}
	// The peer's clock is nobody's: the note is stamped here, on arrival.
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 3})); got.Refused != "" {
		t.Fatalf("a newer pick = %+v", got)
	}
	r = y.state(t, bead, seq)
	if r.State != AskAnswered || len(r.Answers) != 3 || r.Current.Note.Author != "mckean@north" || r.Current.Answer.Option != 3 ||
		!r.Current.Note.At.Equal(y.now) {
		t.Fatalf("after both yards picked: %+v", r)
	}

	// Anything but a newer pick on a picked ask is refused, naming the
	// state.
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Later: true})); got.Refused != RefusedAskAnswered {
		t.Fatalf("Later on a picked ask = %+v", got)
	}
	if len(y.state(t, bead, seq).Answers) != 3 {
		t.Fatal("a refused answer left a note")
	}
}

// Later and Decline travel as answers do, and so do words on an ask that
// wants them. A declined ask and one answered with words are ended: a late
// answer is refused, naming the state.
func TestPeerAnswerLaterDeclineWords(t *testing.T) {
	y := newHomeYard(t)
	bead, seq := y.asked(t, northAsk())

	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Later: true})); got.Refused != "" {
		t.Fatalf("Later = %+v", got)
	}
	r := y.state(t, bead, seq)
	if r.State != AskAside || r.Current.Note.Author != "mckean@north" || !r.ReturnAt.Equal(y.now.Add(LaterFor)) {
		t.Fatalf("after Later: %+v", r)
	}
	// Here the asker's next note brings it back, by the rule of any Later.
	y.now = y.now.Add(time.Hour)
	if err := y.AddNote(y.ctx, bead, "the copy passed again", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if r := y.state(t, bead, seq); r.State != AskOpen {
		t.Fatalf("after the asker wrote: %v", r.State)
	}
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Decline: true, Words: "not mine"})); got.Refused != "" {
		t.Fatalf("Decline = %+v", got)
	}
	r = y.state(t, bead, seq)
	if r.State != AskDeclined || declinedLine(*r.Current) != "mckean@north declined to decide this: not mine" {
		t.Fatalf("after Decline: %+v", r)
	}
	for _, late := range []peerAnswer{{Option: 1}, {Later: true}, {Decline: true, Words: "again"}} {
		late.From, late.Bead, late.Seq = "mckean", bead, seq
		if got := y.take(t, y.answer(late)); got.Refused != RefusedAskDeclined {
			t.Fatalf("%+v on a declined ask = %+v", late, got)
		}
	}
	// By hand too, as before.
	if err := y.AddAnswer(y.ctx, bead, Answer{Ask: seq, Option: 1}, "jo"); err == nil || !strings.Contains(err.Error(), "was declined") {
		t.Fatalf("a pick by hand on a declined ask = %v", err)
	}

	words := wordsAsk("mckean@north")
	bead, seq = y.asked(t, words)
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 1})); got.Refused != RefusedNotAsk {
		t.Fatalf("a pick on an ask that wants words = %+v", got)
	}
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Words: "warmly"})); got.Refused != "" {
		t.Fatalf("words = %+v", got)
	}
	if r := y.state(t, bead, seq); r.State != AskAnswered || r.Current.Note.Author != "mckean@north" || r.Current.Answer.Words != "warmly" {
		t.Fatalf("after words: %+v", r)
	}
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Words: "or coolly"})); got.Refused != RefusedAskAnswered {
		t.Fatalf("words on an answered ask = %+v", got)
	}
	if y.refusals(t, RefusedAskDeclined) != 3 || y.refusals(t, RefusedAskAnswered) != 1 {
		t.Fatal("the refusals were not each logged")
	}
}

// An answer is taken only for an ask this yard asked that yard's person:
// anything else is unsolicited and refused as no such ask, whoever is
// trusted, and one by another name as the wrong person. Nothing of a
// refused answer lands.
func TestPeerAnswerUnsolicited(t *testing.T) {
	y := newHomeYard(t)
	bead, seq := y.asked(t, northAsk())
	local := linksAsk()
	local.To = "mckean"
	localSeq, err := y.AddAsk(y.ctx, bead, local, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.AddNote(y.ctx, bead, "a plain note", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	notes, _ := y.Notes(y.ctx, bead)
	plain := notes[len(notes)-1].Seq
	// Another yard, and an ask for its person.
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "south.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddPeer(y.ctx, Peer{Name: "south", Key: yardkey.Public(k)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AllowPeer(y.ctx, "south", PeerAllow{Send: []string{PeerAsk}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{MailPeers: "north,south"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	south := linksAsk()
	south.To = "mckean@south"
	southSeq, err := y.AddAsk(y.ctx, bead, south, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	before := len(notes) + 1

	for name, tc := range map[string]struct {
		a    peerAnswer
		want string
	}{
		"no such bead":           {peerAnswer{From: "mckean", Bead: "yd-nowhere", Seq: seq, Option: 1}, RefusedNoSuchAsk},
		"no bead at all":         {peerAnswer{From: "mckean", Bead: "no bead", Seq: seq, Option: 1}, RefusedNoSuchAsk},
		"no such seq":            {peerAnswer{From: "mckean", Bead: bead, Seq: 9999, Option: 1}, RefusedNoSuchAsk},
		"a note that is no ask":  {peerAnswer{From: "mckean", Bead: bead, Seq: plain, Option: 1}, RefusedNoSuchAsk},
		"an ask for a user here": {peerAnswer{From: "mckean", Bead: bead, Seq: localSeq, Option: 1}, RefusedNoSuchAsk},
		"another yard's ask":     {peerAnswer{From: "mckean", Bead: bead, Seq: southSeq, Option: 1}, RefusedNoSuchAsk},
		"another person":         {peerAnswer{From: "jo", Bead: bead, Seq: seq, Option: 1}, RefusedWrongPerson},
		"no answer":              {peerAnswer{From: "mckean", Bead: bead, Seq: seq}, RefusedNotAsk},
		"a pick and Later":       {peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 1, Later: true}, RefusedNotAsk},
		"no option of the ask":   {peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 9}, RefusedNotAsk},
		"words alone":            {peerAnswer{From: "mckean", Bead: bead, Seq: seq, Words: "hm"}, RefusedNotAsk},
		"a name that is none":    {peerAnswer{From: "mc@kean", Bead: bead, Seq: seq, Option: 1}, RefusedNotAsk},
		"words too large":        {peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 1, Words: strings.Repeat("w", MaxPeerMailBytes+1)}, RefusedTooLarge},
	} {
		if got := y.take(t, y.answer(tc.a)); got.Refused != tc.want {
			t.Errorf("%s = %+v, want %s", name, got, tc.want)
		}
	}
	m := y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 1})
	m.Body = json.RawMessage(strings.Replace(string(m.Body), `"v":1`, `"v":2`, 1))
	if got := y.take(t, m); got.Refused != RefusedVersion {
		t.Fatalf("another v = %+v", got)
	}
	// Trusting the name changes nothing of it: trust is whose word an
	// answer is shown as, never whether one is taken.
	if notes, _ := y.Notes(y.ctx, bead); len(notes) != before {
		t.Fatalf("refused answers left notes: %d, want %d", len(notes), before)
	}
	for _, s := range []uint64{seq, localSeq, southSeq} {
		if r := y.state(t, bead, s); r.State != AskOpen {
			t.Fatalf("ask %d after the refusals: %v", s, r.State)
		}
	}
	// A refused answer is not kept as seen: its id may come again.
	if y.refusals(t, RefusedNoSuchAsk) != 6 || y.refusals(t, RefusedWrongPerson) != 1 {
		t.Fatalf("refusals logged: no_such_ask %d, wrong_person %d", y.refusals(t, RefusedNoSuchAsk), y.refusals(t, RefusedWrongPerson))
	}

	// The link must carry ask in for the answer too.
	if _, err := y.AllowPeer(y.ctx, "north", PeerAllow{NoReceive: []string{PeerAsk}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 1})); got.Refused != RefusedKind {
		t.Fatalf("an answer over a link that takes no ask = %+v", got)
	}
}

// An ask addressed to person@old is still that yard's to answer after the
// link was renamed, and the answer is written by the name the yard has
// today. Once another yard has the old name, it is not.
func TestPeerAnswerAfterRename(t *testing.T) {
	y := newHomeYard(t)
	bead, seq := y.asked(t, northAsk())
	if _, err := y.RenamePeer(y.ctx, "north", "up", "jo"); err != nil {
		t.Fatal(err)
	}
	take := func(a peerAnswer) PeerTaken {
		t.Helper()
		got, err := y.PeerReceive(y.ctx, "up", y.key, []PeerMessage{y.answer(a)})
		if err != nil {
			t.Fatal(err)
		}
		return got[0]
	}
	if got := take(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 1}); got.Refused != "" {
		t.Fatalf("an answer after the rename = %+v", got)
	}
	if r := y.state(t, bead, seq); r.Current == nil || r.Current.Note.Author != "mckean@up" {
		t.Fatalf("after the rename: %+v", r.Current)
	}
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "other.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddPeer(y.ctx, Peer{Name: "north", Key: yardkey.Public(k)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if got := take(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 2}); got.Refused != RefusedNoSuchAsk {
		t.Fatalf("an answer for a name another yard has now = %+v", got)
	}
}

// foreign is a question north asks this yard's mckean, as ask 3 on its bead
// yd-links, taken under the message id.
func (y *homeYard) foreign(t *testing.T, id string, ask Ask) {
	t.Helper()
	ask.To = "mckean@south"
	if len(ask.Options) > 0 {
		ask.Options = append(ask.Options, AskOption{Label: YouDecide, Consequence: "leave it to yardmaster"})
	}
	body, _ := json.Marshal(peerAsk{V: PeerAskVersion, To: "mckean", From: "yardmaster", Bead: "yd-links", Title: "Move the links",
		Stage: "build", Seq: 3, Since: y.now.Add(-time.Hour), Ask: ask})
	if got := y.take(t, PeerMessage{ID: id, Kind: PeerAsk, Body: body}); got.Refused != "" {
		t.Fatalf("the foreign ask = %+v", got)
	}
}

func (y *homeYard) queue(t *testing.T) *AskQueue {
	t.Helper()
	q, err := y.UserAsks(y.ctx, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// outbox is what waits for north, with the answers' bodies read.
func (y *homeYard) outbox(t *testing.T) ([]PeerOut, []peerAnswer) {
	t.Helper()
	out, err := y.PeerOutbox(y.ctx, "north", 0)
	if err != nil {
		t.Fatal(err)
	}
	var as []peerAnswer
	for _, m := range out {
		var a peerAnswer
		if err := json.Unmarshal(m.Body, &a); err != nil {
			t.Fatal(err)
		}
		as = append(as, a)
	}
	return out, as
}

// Answering a foreign ask queues the answer home as a message of kind ask
// whose re is the ask message's id, and writes no note anywhere: the
// routing is AddAnswer's, which the socket's writes and yardr bead answer
// share. The question is among the decided from then on, a newer pick
// stands, and Later is 24 hours by this yard's clock and nothing else.
func TestForeignAskAnswered(t *testing.T) {
	y := newHomeYard(t)
	y.foreign(t, "a1", linksAsk())
	if q := y.queue(t); len(q.Open) != 1 || q.Open[0].Bead != "yd-links@north" || len(q.Decided) != 0 {
		t.Fatalf("the queue = %+v", q)
	}

	// Refused, with nothing queued: another's answer, words alone, an
	// option the ask has not, no such ask, no such bead, no such yard.
	for name, tc := range map[string]struct {
		id    string
		ans   Answer
		actor string
		want  string
	}{
		"another person": {"yd-links@north", Answer{Ask: 3, Option: 1}, "jo", "ask 3 on yd-links@north is for mckean: north takes no one else's answer to it"},
		"words alone":    {"yd-links@north", Answer{Ask: 3, Words: "hm"}, "mckean", `words for the asker go as mail, yardr mail yardmaster@north "..."`},
		"no such option": {"yd-links@north", Answer{Ask: 3, Option: 9}, "mckean", "ask 3 has options 1..3"},
		"no such ask":    {"yd-links@north", Answer{Ask: 4, Option: 1}, "mckean", "bead yd-links@north has no ask 4"},
		"no such bead":   {"yd-other@north", Answer{Ask: 3, Option: 1}, "mckean", "no question from north stands on it"},
		"no such yard":   {"yd-links@nowhere", Answer{Ask: 3, Option: 1}, "mckean", "nowhere"},
	} {
		if err := y.AddAnswer(y.ctx, tc.id, tc.ans, tc.actor); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s = %v, want %q", name, err, tc.want)
		}
	}
	// The link must carry ask out, as the answer is a message of the kind.
	if _, err := y.AllowPeer(y.ctx, "north", PeerAllow{NoSend: []string{PeerAsk}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.AnswerAsk(y.ctx, "yd-links@north", 3, 1, "", "mckean"); err == nil || !strings.Contains(err.Error(), "yardr peer allow north --send ask") {
		t.Fatalf("an answer over a link that carries no ask out = %v", err)
	}
	if out, _ := y.outbox(t); len(out) != 0 {
		t.Fatalf("refused answers queued %+v", out)
	}
	if q := y.queue(t); len(q.Open) != 1 {
		t.Fatalf("refused answers changed the queue: %+v", q)
	}
	if _, err := y.AllowPeer(y.ctx, "north", PeerAllow{Send: []string{PeerAsk}}, "jo"); err != nil {
		t.Fatal(err)
	}

	// Later: aside here for 24 hours, and on its way home.
	if err := y.DeferAsk(y.ctx, "yd-links@north", 3, "mckean"); err != nil {
		t.Fatal(err)
	}
	out, as := y.outbox(t)
	if len(out) != 1 || out[0].Kind != PeerAsk || out[0].Re != "a1" ||
		as[0] != (peerAnswer{V: PeerAskVersion, To: "yardmaster", From: "mckean", Bead: "yd-links", Seq: 3, Later: true}) {
		t.Fatalf("the Later queued = %+v %+v", out, as)
	}
	q := y.queue(t)
	if len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].State != "put aside" || q.Decided[0].Bead != "yd-links@north" ||
		q.Decided[0].Asker != "yardmaster@north" || q.Decided[0].ReturnAt == nil || !q.Decided[0].ReturnAt.Equal(y.now.Add(LaterFor)) {
		t.Fatalf("after Later: %+v", q)
	}
	y.now = y.now.Add(LaterFor - time.Minute)
	if q := y.queue(t); len(q.Open) != 0 {
		t.Fatal("a put-aside foreign ask came back before its 24 hours")
	}
	y.now = y.now.Add(time.Minute)
	if q := y.queue(t); len(q.Open) != 1 || len(q.Decided) != 0 {
		t.Fatalf("after 24 hours: %+v", q)
	}

	// A pick with words, then a newer pick: the newest stands.
	if err := y.AnswerAsk(y.ctx, "yd-links@north", 3, 1, "tonight", "mckean"); err != nil {
		t.Fatal(err)
	}
	y.now = y.now.Add(time.Minute)
	if err := y.AddAnswer(y.ctx, "yd-links@north", Answer{Ask: 3, Option: 2}, "mckean"); err != nil {
		t.Fatal(err)
	}
	out, as = y.outbox(t)
	if len(out) != 3 || as[1].Option != 1 || as[1].Words != "tonight" || as[2].Option != 2 || out[2].Re != "a1" || out[1].ID == out[2].ID {
		t.Fatalf("the picks queued = %+v", as)
	}
	q = y.queue(t)
	if len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].State != "answered" || q.Decided[0].Option != 2 || q.Decided[0].OptionLabel != "After Friday" {
		t.Fatalf("after two picks: %+v", q)
	}
	// Later on a picked ask is refused here, as on a bead of this yard.
	if err := y.DeferAsk(y.ctx, "yd-links@north", 3, "mckean"); err == nil || !strings.Contains(err.Error(), "answered already") {
		t.Fatalf("Later on a picked foreign ask = %v", err)
	}
	// No bead of this yard got a note, and each answer said noted on the
	// foreign bead, by the person.
	beads, err := y.ListBeads(y.ctx, BeadFilter{All: true})
	if err != nil || len(beads) != 0 {
		t.Fatalf("beads = %+v, %v", beads, err)
	}
	evs, _ := y.Events(y.ctx, "", 500)
	noted := 0
	for _, e := range evs {
		if e.Kind == "noted" && e.Bead != nil && *e.Bead == "yd-links@north" && e.Actor == "mckean" {
			noted++
		}
	}
	if noted != 3 {
		t.Fatalf("noted events by the person = %d, want 3", noted)
	}
}

// Decline ends a foreign ask here as it will there: a late pick is refused
// before anything is queued.
func TestForeignAskDeclined(t *testing.T) {
	y := newHomeYard(t)
	y.foreign(t, "a1", linksAsk())
	if err := y.DeclineAsk(y.ctx, "yd-links@north", 3, "not mine", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, as := y.outbox(t); len(as) != 1 || !as[0].Decline || as[0].Words != "not mine" {
		t.Fatalf("the Decline queued = %+v", as)
	}
	if q := y.queue(t); len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].State != "declined" || q.Decided[0].Words != "not mine" {
		t.Fatalf("after Decline: %+v", q)
	}
	if err := y.AnswerAsk(y.ctx, "yd-links@north", 3, 1, "", "mckean"); err == nil || !strings.Contains(err.Error(), "was declined") {
		t.Fatalf("a pick on a declined foreign ask = %v", err)
	}
	if out, _ := y.outbox(t); len(out) != 1 {
		t.Fatal("a refused pick was queued")
	}
}

// A foreign ask that wants words is answered with words, once.
func TestForeignAskWords(t *testing.T) {
	y := newHomeYard(t)
	y.foreign(t, "a1", Ask{Question: "What tone?", Context: []string{"the greeting."}})
	if err := y.AnswerAsk(y.ctx, "yd-links@north", 3, 1, "", "mckean"); err == nil || !strings.Contains(err.Error(), "wants words") {
		t.Fatalf("a pick on a foreign ask that wants words = %v", err)
	}
	if err := y.AnswerAsk(y.ctx, "yd-links@north", 3, 0, "warmly", "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, as := y.outbox(t); len(as) != 1 || as[0].Words != "warmly" || as[0].Option != 0 {
		t.Fatalf("the words queued = %+v", as)
	}
	if q := y.queue(t); len(q.Decided) != 1 || q.Decided[0].State != "answered" || q.Decided[0].Words != "warmly" {
		t.Fatalf("after words: %+v", q)
	}
	if err := y.AnswerAsk(y.ctx, "yd-links@north", 3, 0, "or coolly", "mckean"); err == nil || !strings.Contains(err.Error(), "answered already") {
		t.Fatalf("words twice = %v", err)
	}
	if err := y.AnswerAsk(y.ctx, "yd-links@north", 3, 0, strings.Repeat("w", MaxPeerMailBytes+1), "mckean"); err == nil {
		t.Fatal("words over what crosses were taken")
	}
}

// An answer given up comes back. The yard not reached in its tries, or a
// refusal another try may not meet, takes the answer out again: the
// question is open as before. The asking yard's word that the ask ended
// there marks the foreign ask with that outcome instead, and it takes no
// further answer. Either way the person gets mail from the yard, and the
// queue says noted.
func TestForeignAnswerGivenUp(t *testing.T) {
	y := newHomeYard(t)
	y.foreign(t, "a1", linksAsk())
	pick := func(n int) PeerOut {
		t.Helper()
		if err := y.AnswerAsk(y.ctx, "yd-links@north", 3, n, "", "mckean"); err != nil {
			t.Fatal(err)
		}
		out, _ := y.outbox(t)
		return out[len(out)-1]
	}

	// Not reached, past the tries.
	m := pick(1)
	for i := 1; i <= 3; i++ {
		if q := y.queue(t); len(q.Open) != 0 || len(q.Decided) != 1 {
			t.Fatalf("while the answer waits: %+v", q)
		}
		if err := y.PeerSendFailed(y.ctx, "north", m.Seq, "peer north: dial tcp: connection refused", y.now.Add(time.Hour), i == 3); err != nil {
			t.Fatal(err)
		}
	}
	if q := y.queue(t); len(q.Open) != 1 || len(q.Decided) != 0 {
		t.Fatalf("after the answer was given up: %+v", q)
	}
	mail := y.yardMail(t, "mckean")
	if len(mail) != 1 || !strings.HasPrefix(mail[0], `your pick of 1 (Move them now) on "Move this yard's links to the declared source now?" (ask 3 on yd-links@north, from yardmaster@north) did not land: north was not reached in 3 tries since `) ||
		!strings.HasSuffix(mail[0], ": dial tcp: connection refused. The question is open again.") {
		t.Fatalf("mail = %q", mail)
	}

	// A refusal that is no end of the ask opens it again too, with an
	// older answer that landed standing as it did.
	first := pick(1)
	if err := y.PeerDelivered(y.ctx, "north", first.Seq); err != nil {
		t.Fatal(err)
	}
	y.now = y.now.Add(time.Minute)
	second := pick(2)
	if err := y.PeerRefused(y.ctx, "north", second.Seq, "peer north: refused by the peer: "+PeerRefusal("north", PeerAsk, RefusedRate)); err != nil {
		t.Fatal(err)
	}
	q := y.queue(t)
	if len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].Option != 1 {
		t.Fatalf("after the newer pick was refused: %+v", q)
	}
	mail = y.yardMail(t, "mckean")
	if len(mail) != 2 || !strings.HasSuffix(mail[1], "did not land: it has taken as much mail from this yard as it takes in an hour. The question stands as answered, by what you did with it before.") {
		t.Fatalf("mail = %q", mail)
	}

	// The ask ended there: marked, by the daemon's word of a post refused.
	third := pick(3)
	if err := y.PeerRefused(y.ctx, "north", third.Seq, "peer north: refused by the peer: "+PeerRefusal("north", PeerAsk, RefusedAskDeclined)); err != nil {
		t.Fatal(err)
	}
	q = y.queue(t)
	if len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].State != "declined" || q.Decided[0].Option != 0 ||
		q.Decided[0].Words != "it was declined in north's yard" {
		t.Fatalf("after ask_declined: %+v", q)
	}
	mail = y.yardMail(t, "mckean")
	if len(mail) != 3 || !strings.HasSuffix(mail[2], "did not land: it was declined in north's yard. It takes no further answer.") {
		t.Fatalf("mail = %q", mail)
	}
	if err := y.AnswerAsk(y.ctx, "yd-links@north", 3, 1, "", "mckean"); err == nil ||
		!strings.Contains(err.Error(), "takes no further answer: it was declined in north's yard") {
		t.Fatalf("a pick on a marked foreign ask = %v", err)
	}
	evs, _ := y.Events(y.ctx, "", 500)
	noted := 0
	for _, e := range evs {
		if e.Kind == "noted" && e.Bead != nil && *e.Bead == "yd-links@north" && e.Actor == "yard" {
			noted++
		}
	}
	if noted != 3 {
		t.Fatalf("noted events of the yard = %d, want 3", noted)
	}
}

// The other ends of an ask, heard by the word of a peer that asks for its
// messages: answered there marks the question answered, and an ask that
// yard no longer has as declined, with the words that say which.
func TestForeignAnswerRefusedByPull(t *testing.T) {
	for reason, want := range map[string][2]string{
		RefusedAskAnswered: {"answered", "it was answered in north's yard"},
		RefusedNoSuchAsk:   {"declined", "it is no longer asked in north's yard"},
		RefusedWrongPerson: {"", ""},
	} {
		y := newHomeYard(t)
		y.foreign(t, "a1", linksAsk())
		if err := y.DeferAsk(y.ctx, "yd-links@north", 3, "mckean"); err != nil {
			t.Fatal(err)
		}
		out, _ := y.outbox(t)
		if _, err := y.PeerHandOver(y.ctx, "north", y.key, 0, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := y.PeerHandOver(y.ctx, "north", y.key, out[0].Seq, map[uint64]Refusal{out[0].Seq: {Reason: reason}}); err != nil {
			t.Fatal(err)
		}
		q := y.queue(t)
		if want[0] == "" {
			// No end of the ask: the right person may still answer.
			if len(q.Open) != 1 || len(q.Decided) != 0 {
				t.Fatalf("%s: %+v", reason, q)
			}
			continue
		}
		if len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].State != want[0] || q.Decided[0].Words != want[1] || q.Decided[0].ReturnAt != nil {
			t.Fatalf("%s: %+v", reason, q)
		}
		if mail := y.yardMail(t, "mckean"); len(mail) != 1 || !strings.HasPrefix(mail[0], "putting it aside on ") || !strings.Contains(mail[0], want[1]) {
			t.Fatalf("%s: mail = %q", reason, mail)
		}
	}
}

// A question given up is told to whoever asked it, as mail from the yard,
// and stays open on its bead, answerable there by hand.
func TestPeerAskUndelivered(t *testing.T) {
	y := newHomeYard(t)
	bead, seq := y.asked(t, northAsk())
	out, _ := y.outbox(t)
	if err := y.PeerRefused(y.ctx, "north", out[0].Seq, "peer north: refused by the peer: "+PeerRefusal("north", PeerAsk, RefusedNotExposed)); err != nil {
		t.Fatal(err)
	}
	mail := y.yardMail(t, "yardmaster")
	want := fmt.Sprintf(`("Move this yard's links to the declared source now?", ask %d on %s) was not delivered: refused by the peer: it exposes nobody of that name to this yard (yardr peer show north lists whom it does). It stays open on the bead, where it is answered by hand: yardr bead answer %s %d`, seq, bead, bead, seq)
	if len(mail) != 1 || !strings.HasPrefix(mail[0], "your question for mckean@north of ") || !strings.HasSuffix(mail[0], want) {
		t.Fatalf("mail = %q", mail)
	}
	if r := y.state(t, bead, seq); r.State != AskOpen {
		t.Fatalf("the ask after it was given up: %v", r.State)
	}
	if err := y.AddAnswer(y.ctx, bead, Answer{Ask: seq, Option: 1}, "jo"); err != nil {
		t.Fatal(err)
	}

	// A user who asked is told as a user is.
	bead, _ = y.asked(t, northAsk())
	if _, err := y.AddAsk(y.ctx, bead, northAsk(), "mckean"); err != nil {
		t.Fatal(err)
	}
	out, _ = y.outbox(t)
	last := out[len(out)-1]
	if err := y.PeerSendFailed(y.ctx, "north", last.Seq, "peer north: no route", y.now, true); err != nil {
		t.Fatal(err)
	}
	evs, _ := y.Events(y.ctx, "", 500)
	told := false
	for _, e := range evs {
		if e.Kind == "mail" && e.Actor == "yard" && e.Data["to"] == "mckean" && e.Data["to_user"] == true {
			told = strings.Contains(e.Data["text"].(string), "was not delivered: no route.")
		}
	}
	if !told {
		t.Fatal("a user's question given up was not told")
	}
}
