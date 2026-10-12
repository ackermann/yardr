package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/yardkey"
)

// settles are the settles that wait for peer, each as "<settled> <bead>
// <seq> re <the copy's message>". One that carries more than that fails
// the test: a settle has no text of anyone's, no pick and no name.
func (y *homeYard) settles(t *testing.T, peer string) []string {
	t.Helper()
	out, err := y.PeerOutbox(y.ctx, peer, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range out {
		var a peerAnswer
		if err := json.Unmarshal(m.Body, &a); err != nil || m.Kind != PeerAsk {
			t.Fatalf("in the outbox: %+v, %v", m, err)
		}
		if a.Settled == "" || m.Failed != nil {
			continue
		}
		if want := (peerAnswer{V: PeerAskVersion, Bead: a.Bead, Seq: a.Seq, Settled: a.Settled}); a != want || m.Re == "" {
			t.Fatalf("a settle that says more than the end: %+v re %q", a, m.Re)
		}
		got = append(got, fmt.Sprintf("%s %s %d re %s", a.Settled, a.Bead, a.Seq, m.Re))
	}
	return got
}

// copyOf is the message that carried the ask seq to peer.
func (y *homeYard) copyOf(t *testing.T, peer string, seq uint64) PeerOut {
	t.Helper()
	out, err := y.PeerOutbox(y.ctx, peer, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range out {
		var a peerAsk
		if m.Re == "" && json.Unmarshal(m.Body, &a) == nil && a.Seq == seq {
			return m
		}
	}
	t.Fatalf("no copy of ask %d waits for %s", seq, peer)
	return PeerOut{}
}

// twoReach is the reach yard with a second link, south, and reach for
// mckean on both.
func twoReach(t *testing.T) *homeYard {
	t.Helper()
	y := reachYard(t)
	y.allow(t, PeerAllow{Reach: map[string]string{"mckean": "mckean"}})
	if _, err := y.AddPeer(y.ctx, Peer{Name: "south", Key: strings.Repeat("s", len(y.key))}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.says(t, "south", RosterUser, RosterUser)
	if _, err := y.AllowPeer(y.ctx, "south", PeerAllow{Send: []string{PeerAsk}, Receive: []string{PeerAsk},
		Trust: map[string]string{"mckean": "mckean"}, Reach: map[string]string{"mckean": "mckean"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	return y
}

// An answer that ends a copied ask in the home yard queues one settle for
// each name it was copied to: a pick, words or a decline, by hand here.
// Later queues none, and neither does a newer pick on an ask that was told
// already. The settle names the bead and the seq, and answers the copy.
func TestAskSettledWhereCopied(t *testing.T) {
	y := twoReach(t)
	bead, seq := y.asked(t, linksAsk())
	north, south := y.copyOf(t, "north", seq), y.copyOf(t, "south", seq)

	if err := y.DeferAsk(y.ctx, bead, seq, "mckean"); err != nil {
		t.Fatal(err)
	}
	if n, s := y.settles(t, "north"), y.settles(t, "south"); len(n)+len(s) != 0 {
		t.Fatalf("Later was told across: %q, %q", n, s)
	}
	if err := y.AnswerAsk(y.ctx, bead, seq, 1, "tonight", "mckean"); err != nil {
		t.Fatal(err)
	}
	wantN := []string{fmt.Sprintf("answered %s %d re %s", bead, seq, north.ID)}
	wantS := []string{fmt.Sprintf("answered %s %d re %s", bead, seq, south.ID)}
	if n, s := y.settles(t, "north"), y.settles(t, "south"); !slices.Equal(n, wantN) || !slices.Equal(s, wantS) {
		t.Fatalf("after the pick: %q, %q", n, s)
	}
	// A newer pick replaces the answer here and tells nothing more.
	if err := y.AnswerAsk(y.ctx, bead, seq, 2, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if n, s := y.settles(t, "north"), y.settles(t, "south"); len(n) != 1 || len(s) != 1 {
		t.Fatalf("after a newer pick: %q, %q", n, s)
	}

	// A decline, and words on an ask with no options.
	bead2, seq := y.asked(t, linksAsk())
	if err := y.DeclineAsk(y.ctx, bead2, seq, "not mine to decide", "mckean"); err != nil {
		t.Fatal(err)
	}
	if n := y.settles(t, "north"); len(n) != 2 || !strings.HasPrefix(n[1], fmt.Sprintf("declined %s %d re ", bead2, seq)) {
		t.Fatalf("after the decline: %q", n)
	}
	words := linksAsk()
	words.Options, words.Recommend, words.Why = nil, 0, ""
	bead3, seq := y.asked(t, words)
	if err := y.AnswerAsk(y.ctx, bead3, seq, 0, "move them on Friday", "mckean"); err != nil {
		t.Fatal(err)
	}
	if s := y.settles(t, "south"); len(s) != 3 || !strings.HasPrefix(s[2], fmt.Sprintf("answered %s %d re ", bead3, seq)) {
		t.Fatalf("after the words: %q", s)
	}
}

// An answer that came from one yard is told to the others the ask was
// copied to, and not to the yard it came from: the third yard's pick
// settles the ask at the second.
func TestAskSettledFromThirdYard(t *testing.T) {
	y := twoReach(t)
	bead, seq := y.asked(t, linksAsk())
	south := y.copyOf(t, "south", seq)
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 2})); got.Refused != "" {
		t.Fatalf("north's answer = %+v", got)
	}
	if n, s := y.settles(t, "north"), y.settles(t, "south"); len(n) != 0 ||
		!slices.Equal(s, []string{fmt.Sprintf("answered %s %d re %s", bead, seq, south.ID)}) {
		t.Fatalf("after north's pick: north %q, south %q", n, s)
	}

	// Later from there ends nothing, and its decline is a decline.
	bead, seq = y.asked(t, linksAsk())
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Later: true})); got.Refused != "" {
		t.Fatalf("north's Later = %+v", got)
	}
	if s := y.settles(t, "south"); len(s) != 1 {
		t.Fatalf("after north's Later: %q", s)
	}
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Decline: true, Words: "ask jo"})); got.Refused != "" {
		t.Fatalf("north's decline = %+v", got)
	}
	if n, s := y.settles(t, "north"), y.settles(t, "south"); len(n) != 0 || len(s) != 2 || !strings.HasPrefix(s[1], "declined ") {
		t.Fatalf("after north's decline: north %q, south %q", n, s)
	}
}

// A settle goes only where the question went and where the grant still
// sends: none for an ask from before the grant, for another user's, for
// one addressed across, for a copy that was given up, or to a name whose
// reach was taken back since.
func TestAskSettledOnlyWhereCopied(t *testing.T) {
	y := reachYard(t)
	bead, before := y.asked(t, linksAsk())
	y.allow(t, PeerAllow{Reach: map[string]string{"mckean": "mckean"}})
	ada := linksAsk()
	ada.To = "ada"
	adaSeq, err := y.AddAsk(y.ctx, bead, ada, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	acrossBead, across := y.asked(t, northAsk())
	for _, a := range []struct {
		bead string
		seq  uint64
	}{{bead, before}, {bead, adaSeq}, {acrossBead, across}} {
		if err := y.AnswerAsk(y.ctx, a.bead, a.seq, 1, "", "jo"); err != nil {
			t.Fatal(err)
		}
	}

	// A copy given up: that yard has no question to be told the end of.
	bead, seq := y.asked(t, linksAsk())
	if err := y.PeerRefused(y.ctx, "north", y.copyOf(t, "north", seq).Seq, "peer north: refused by the peer: "+PeerRefusal("north", PeerAsk, RefusedRate)); err != nil {
		t.Fatal(err)
	}
	if err := y.AnswerAsk(y.ctx, bead, seq, 1, "", "mckean"); err != nil {
		t.Fatal(err)
	}

	// The grant taken back between the copy and the answer.
	bead, seq = y.asked(t, linksAsk())
	y.allow(t, PeerAllow{NoReach: []string{"mckean"}})
	if err := y.AnswerAsk(y.ctx, bead, seq, 1, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	// And a link that carries ask out no more.
	y.allow(t, PeerAllow{Reach: map[string]string{"mckean": "mckean"}})
	bead, seq = y.asked(t, linksAsk())
	y.allow(t, PeerAllow{NoSend: []string{PeerAsk}})
	if err := y.AnswerAsk(y.ctx, bead, seq, 1, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if got := y.settles(t, "north"); len(got) != 0 {
		t.Fatalf("settles for what was not copied: %q", got)
	}
	if got := y.refusals(t, RefusedKind); got != 0 {
		t.Fatalf("a settle not sent was logged as refused %d times", got)
	}
}

// A settle given up tells nobody: an older build refuses it as no ask, and
// nothing waits on it. The failure is in the events, as any is.
func TestSettleGivenUpTellsNobody(t *testing.T) {
	y := reachYard(t)
	y.allow(t, PeerAllow{Reach: map[string]string{"mckean": "mckean"}})
	for i, give := range []func(seq uint64) error{
		func(seq uint64) error {
			return y.PeerRefused(y.ctx, "north", seq, "peer north: refused by the peer: "+PeerRefusal("north", PeerAsk, RefusedNotAsk))
		},
		func(seq uint64) error {
			return y.PeerSendFailed(y.ctx, "north", seq, "peer north: dial tcp: connection refused", y.now, true)
		},
	} {
		bead, seq := y.asked(t, linksAsk())
		if err := y.PeerDelivered(y.ctx, "north", y.copyOf(t, "north", seq).Seq); err != nil {
			t.Fatal(err)
		}
		if err := y.AnswerAsk(y.ctx, bead, seq, 1, "", "mckean"); err != nil {
			t.Fatal(err)
		}
		out, _ := y.PeerOutbox(y.ctx, "north", 0)
		if err := give(out[len(out)-1].Seq); err != nil {
			t.Fatal(err)
		}
		if got := y.kinds(t, "peer_message_failed"); len(got) != i+1 {
			t.Fatalf("peer_message_failed = %d, want %d", len(got), i+1)
		}
		if r := y.state(t, bead, seq); r.State != AskAnswered {
			t.Fatalf("the ask after its settle was given up: %v", r.State)
		}
	}
	for _, e := range y.kinds(t, "mail") {
		t.Fatalf("a settle given up wrote mail: %+v", e.Data)
	}
}

// settle is a settle north sends for the ask it copied with the message re.
func (y *homeYard) settle(re, bead string, seq uint64, settled string) PeerMessage {
	y.n++
	body, _ := json.Marshal(peerAnswer{V: PeerAskVersion, Bead: bead, Seq: seq, Settled: settled})
	return PeerMessage{ID: fmt.Sprintf("set%d", y.n), Kind: PeerAsk, Re: re, Body: body}
}

// The yard that takes a settle marks its foreign ask answered, declined or
// taken back, with the words that say where, and refuses a later answer to it before
// anything is queued. Nobody is mailed, and the queue says noted.
func TestSettleTaken(t *testing.T) {
	for settled, want := range map[string][2]string{
		SettledAnswered:  {"answered", "it was answered in north's yard"},
		SettledDeclined:  {"declined", "it was declined in north's yard"},
		SettledTakenBack: {"taken back", "it was taken back in north's yard"},
	} {
		y := newHomeYard(t)
		y.foreign(t, "a1", linksAsk())
		if got := y.take(t, y.settle("a1", "yd-links", 3, settled)); got.Refused != "" || got.Duplicate {
			t.Fatalf("%s: the settle = %+v", settled, got)
		}
		q := y.queue(t)
		if len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].State != want[0] || q.Decided[0].Words != want[1] || q.Decided[0].Option != 0 {
			t.Fatalf("%s: the queue = %+v", settled, q)
		}
		for _, answer := range []func() error{
			func() error { return y.AnswerAsk(y.ctx, "yd-links@north", 3, 1, "", "mckean") },
			func() error { return y.DeferAsk(y.ctx, "yd-links@north", 3, "mckean") },
			func() error { return y.DeclineAsk(y.ctx, "yd-links@north", 3, "no", "mckean") },
		} {
			if err := answer(); err == nil || !strings.Contains(err.Error(), "ask 3 on yd-links@north takes no further answer: "+want[1]) {
				t.Fatalf("%s: an answer after the settle = %v", settled, err)
			}
		}
		if out, _ := y.outbox(t); len(out) != 0 {
			t.Fatalf("%s: a refused answer was queued: %+v", settled, out)
		}
		if mail := y.yardMail(t, "mckean"); len(mail) != 0 {
			t.Fatalf("%s: mail = %q", settled, mail)
		}
		noted := 0
		for _, e := range y.kinds(t, "noted") {
			if e.Bead != nil && *e.Bead == "yd-links@north" && e.Actor == "yard" {
				noted++
			}
		}
		if noted != 1 {
			t.Fatalf("%s: noted events of the yard = %d, want 1", settled, noted)
		}
		// A second settle changes nothing: the first end stands.
		other := SettledAnswered
		if settled == other {
			other = SettledDeclined
		}
		if got := y.take(t, y.settle("a1", "yd-links", 3, other)); got.Refused != "" {
			t.Fatalf("%s: a second settle = %+v", settled, got)
		}
		if q := y.queue(t); q.Decided[0].State != want[0] {
			t.Fatalf("%s: after a second settle: %+v", settled, q.Decided)
		}
	}
}

// A settle for no foreign ask of that peer is dropped: taken, so the yard
// that sent it learns nothing of what this one holds, and changing
// nothing. One that names another bead or seq than its message carried is
// none either, and a word that is neither end is refused as no ask.
func TestSettleForNoAskDropped(t *testing.T) {
	y := newHomeYard(t)
	y.foreign(t, "a1", linksAsk())
	for _, m := range []PeerMessage{
		y.settle("nothing", "yd-links", 3, SettledAnswered),
		y.settle("a1", "yd-other", 3, SettledAnswered),
		y.settle("a1", "yd-links", 4, SettledDeclined),
	} {
		if got := y.take(t, m); got.Refused != "" {
			t.Fatalf("a settle for no ask = %+v", got)
		}
	}
	if got := y.take(t, y.settle("a1", "yd-links", 3, "maybe")); got.Refused != RefusedNotAsk {
		t.Fatalf("a settle of no end = %+v", got)
	}
	if q := y.queue(t); len(q.Open) != 1 || len(q.Decided) != 0 {
		t.Fatalf("the queue after settles for no ask: %+v", q)
	}
	if len(y.kinds(t, "noted")) != 1 {
		t.Fatalf("noted = %+v", y.kinds(t, "noted"))
	}
	if err := y.AnswerAsk(y.ctx, "yd-links@north", 3, 1, "", "mckean"); err != nil {
		t.Fatalf("the ask takes an answer still: %v", err)
	}

	// A foreign ask of another link is not this peer's to settle.
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "east.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddPeer(y.ctx, Peer{Name: "east", Key: yardkey.Public(k)}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AllowPeer(y.ctx, "east", PeerAllow{Receive: []string{PeerAsk}}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.foreign(t, "a2", linksAsk())
	got, err := y.PeerReceive(y.ctx, "east", yardkey.Public(k), []PeerMessage{y.settle("a2", "yd-links", 3, SettledAnswered)})
	if err != nil || got[0].Refused != "" {
		t.Fatalf("east's settle = %+v, %v", got, err)
	}
	if q := y.queue(t); len(q.Open) != 1 {
		t.Fatalf("another link's settle marked north's ask: %+v", q)
	}
}

// A settle that came while the person's answer was on its way stands over
// it: the answer given up afterwards is told as it is, and the mail does
// not say the question is open again.
func TestSettleOverAnswerOnItsWay(t *testing.T) {
	y := newHomeYard(t)
	y.foreign(t, "a1", linksAsk())
	if err := y.AnswerAsk(y.ctx, "yd-links@north", 3, 2, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	out, _ := y.outbox(t)
	if got := y.take(t, y.settle("a1", "yd-links", 3, SettledAnswered)); got.Refused != "" {
		t.Fatalf("the settle = %+v", got)
	}
	if err := y.PeerSendFailed(y.ctx, "north", out[0].Seq, "peer north: no route", y.now, true); err != nil {
		t.Fatal(err)
	}
	mail := y.yardMail(t, "mckean")
	if len(mail) != 1 || !strings.HasSuffix(mail[0], "no route. The question takes no answer all the same: it was answered in north's yard.") {
		t.Fatalf("mail = %q", mail)
	}
	if q := y.queue(t); len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].Words != "it was answered in north's yard" {
		t.Fatalf("the queue = %+v", q)
	}
}

// linked is one of two yards linked with each other, each with a user
// mckean it exposes to the other.
type linked struct {
	*Store
	name, key string
}

// pair is two stores, a and b, linked both ways with ask allowed each way.
// The yard a gives mckean@b reach for its own mckean: an ask for mckean at
// a is copied to b.
func pair(t *testing.T) (a, b *linked) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { at = at.Add(time.Second); return at }
	t.Cleanup(func() { clock = time.Now })
	open := func(name string) *linked {
		s, err := Open(filepath.Join(t.TempDir(), name+".db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(context.Background()) })
		k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), name+".key"))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetSetting(ctx, OperatorSetting, "jo", "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddUser(ctx, "mckean", "jo"); err != nil {
			t.Fatal(err)
		}
		return &linked{Store: s, name: name, key: yardkey.Public(k)}
	}
	a, b = open("a"), open("b")
	for _, l := range [][2]*linked{{a, b}, {b, a}} {
		if _, err := l[0].AddPeer(ctx, Peer{Name: l[1].name, Key: l[1].key}, "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := l[0].AllowPeer(ctx, l[1].name, PeerAllow{Send: []string{PeerAsk}, Receive: []string{PeerAsk}, Expose: []string{"mckean"}}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	says := Says("v1")
	says.Roster = []PeerRosterEntry{{Name: "mckean", Kind: RosterUser, Mail: true}}
	if err := a.PeerHeard(ctx, "b", b.key, says); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AllowPeer(ctx, "b", PeerAllow{Trust: map[string]string{"mckean": "mckean"}, Reach: map[string]string{"mckean": "mckean"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	return a, b
}

// carry hands what waits in from's outbox for to over, as the daemon does:
// each message is taken there, and ended here as delivered or as refused.
// It answers what kinds crossed, a settle as "settle".
func carry(t *testing.T, from, to *linked) []string {
	t.Helper()
	ctx := context.Background()
	out, err := from.PeerOutbox(ctx, to.name, 0)
	if err != nil {
		t.Fatal(err)
	}
	var crossed []string
	for _, m := range out {
		if m.Failed != nil {
			continue
		}
		got, err := to.PeerReceive(ctx, from.name, from.key, []PeerMessage{m.PeerMessage})
		if err != nil {
			t.Fatal(err)
		}
		var a peerAnswer
		_ = json.Unmarshal(m.Body, &a)
		what := "ask"
		switch {
		case a.Settled != "":
			what = "settle"
		case m.Re != "":
			what = "answer"
		}
		if got[0].Refused != "" {
			what += " refused " + got[0].Refused
			err = from.PeerRefused(ctx, to.name, m.Seq, "peer "+to.name+": refused by the peer: "+PeerRefusal(to.name, m.Kind, got[0].Refused))
		} else {
			err = from.PeerDelivered(ctx, to.name, m.Seq)
		}
		if err != nil {
			t.Fatal(err)
		}
		crossed = append(crossed, what)
	}
	return crossed
}

// One ask, picked in each of two yards, in both orders, and with the two
// picks crossing.
//
// Picked at home first, the settle marks the copy: the pick there is
// refused before anything is queued, and the home answer stands. Picked in
// the other yard first, the answer lands at home and that yard is told
// nothing, since it knows; a pick at home afterwards is the newer pick, by
// note order. When the pick there was on its way before the settle came,
// it lands at home as the newer pick, and the copy says it ended at home.
func TestPickInEachYard(t *testing.T) {
	ctx := context.Background()
	a, b := pair(t)
	ask := func() (string, uint64) {
		t.Helper()
		bead, err := a.CreateBead(ctx, NewBead{Title: "Move the links"}, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		seq, err := a.AddAsk(ctx, bead.ID, linksAsk(), "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		if got := carry(t, a, b); !slices.Equal(got, []string{"ask"}) {
			t.Fatalf("the copy: %q", got)
		}
		return bead.ID, seq
	}
	home := func(bead string, seq uint64) ResolvedAsk {
		t.Helper()
		notes, err := a.Notes(ctx, bead)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range ResolveAsks(notes, now()) {
			if r.Note.Seq == seq {
				return r
			}
		}
		t.Fatalf("no ask %d on %s", seq, bead)
		return ResolvedAsk{}
	}
	// there is the copy's row in b's queue: open, or decided.
	there := func(bead string) (open bool, d DecidedAsk) {
		t.Helper()
		q, err := b.UserAsks(ctx, "mckean")
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range q.Open {
			if o.Bead == bead+"@a" {
				return true, DecidedAsk{}
			}
		}
		for _, d := range q.Decided {
			if d.Bead == bead+"@a" {
				return false, d
			}
		}
		t.Fatalf("b has no row for %s@a: %+v", bead, q)
		return false, DecidedAsk{}
	}

	// Home first.
	bead, seq := ask()
	if open, _ := there(bead); !open {
		t.Fatal("the copy is not open at b")
	}
	if err := a.AnswerAsk(ctx, bead, seq, 1, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if got := carry(t, a, b); !slices.Equal(got, []string{"settle"}) {
		t.Fatalf("after the pick at home: %q", got)
	}
	if open, d := there(bead); open || d.State != "answered" || d.Words != "it was answered in a's yard" {
		t.Fatalf("the copy after the settle: open %v, %+v", open, d)
	}
	if err := b.AnswerAsk(ctx, bead+"@a", seq, 2, "", "mckean"); err == nil || !strings.Contains(err.Error(), "takes no further answer: it was answered in a's yard") {
		t.Fatalf("a pick at b after the settle = %v", err)
	}
	if got := carry(t, b, a); len(got) != 0 {
		t.Fatalf("the refused pick crossed: %q", got)
	}
	if r := home(bead, seq); r.State != AskAnswered || r.Current.Answer.Option != 1 || r.Current.Note.Author != "mckean" {
		t.Fatalf("at home: %+v", r.Current)
	}

	// The other yard first.
	bead, seq = ask()
	if err := b.AnswerAsk(ctx, bead+"@a", seq, 2, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if got := carry(t, b, a); !slices.Equal(got, []string{"answer"}) {
		t.Fatalf("the pick at b: %q", got)
	}
	if r := home(bead, seq); r.State != AskAnswered || r.Current.Answer.Option != 2 || r.Current.Note.Author != "mckean@b" {
		t.Fatalf("at home after b's pick: %+v", r.Current)
	}
	if got := carry(t, a, b); len(got) != 0 {
		t.Fatalf("the yard that answered was told: %q", got)
	}
	if err := a.AnswerAsk(ctx, bead, seq, 1, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if got := carry(t, a, b); len(got) != 0 {
		t.Fatalf("a newer pick at home crossed: %q", got)
	}
	if r := home(bead, seq); r.Current.Answer.Option != 1 || len(r.Answers) != 2 {
		t.Fatalf("at home after both picks: %+v", r.Answers)
	}
	if open, d := there(bead); open || d.State != "answered" || d.Option != 2 {
		t.Fatalf("b's row after both picks: open %v, %+v", open, d)
	}

	// The two picks cross.
	bead, seq = ask()
	if err := a.AnswerAsk(ctx, bead, seq, 1, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if err := b.AnswerAsk(ctx, bead+"@a", seq, 2, "", "mckean"); err != nil {
		t.Fatal(err)
	}
	if got := carry(t, b, a); !slices.Equal(got, []string{"answer"}) {
		t.Fatalf("b's crossing pick: %q", got)
	}
	if got := carry(t, a, b); !slices.Equal(got, []string{"settle"}) {
		t.Fatalf("a's crossing settle: %q", got)
	}
	if r := home(bead, seq); r.Current.Answer.Option != 2 || r.Current.Note.Author != "mckean@b" || len(r.Answers) != 2 {
		t.Fatalf("at home after the crossing: %+v", r.Answers)
	}
	if open, d := there(bead); open || d.Words != "it was answered in a's yard" {
		t.Fatalf("b's row after the crossing: open %v, %+v", open, d)
	}
	if err := b.AnswerAsk(ctx, bead+"@a", seq, 1, "", "mckean"); err == nil {
		t.Fatal("a pick at b after the crossing settle was queued")
	}
}
