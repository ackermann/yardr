package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// reachYard is the home yard with an operator, a second user ada and a
// crew member allowed no peer, whose peer north exposes mckean and ada as
// people and its yardmaster as a crew member, and trusts mckean@north as
// its own mckean.
func reachYard(t *testing.T) *homeYard {
	t.Helper()
	y := newHomeYard(t)
	if err := y.SetSetting(y.ctx, OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddUser(y.ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.PutCrew(y.ctx, CrewMember{Name: "clerk", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.says(t, "north", RosterUser, RosterUser)
	y.allow(t, PeerAllow{Trust: map[string]string{"mckean": "mckean"}})
	return y
}

// says is the peer telling its roster: mckean and ada of the kinds given,
// and its yardmaster, a crew member.
func (y *homeYard) says(t *testing.T, peer, mckean, ada string) {
	t.Helper()
	says := Says("v1")
	says.Roster = []PeerRosterEntry{
		{Name: "ada", Kind: ada, Mail: true},
		{Name: "mckean", Kind: mckean, Mail: true},
		{Name: "yardmaster", Kind: RosterCrew, Mail: true},
	}
	key := y.key
	if peer != "north" {
		p, err := y.GetPeer(y.ctx, peer)
		if err != nil {
			t.Fatal(err)
		}
		key = p.Key
	}
	if err := y.PeerHeard(y.ctx, peer, key, says); err != nil {
		t.Fatal(err)
	}
}

func (y *homeYard) allow(t *testing.T, ch PeerAllow) *Peer {
	t.Helper()
	p, err := y.AllowPeer(y.ctx, "north", ch, "jo")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// copies are the questions that wait for peer, as they are addressed: the
// message's to, the ask's to and who asked.
func (y *homeYard) copies(t *testing.T, peer string) []string {
	t.Helper()
	out, err := y.PeerOutbox(y.ctx, peer, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range out {
		var a peerAsk
		if err := json.Unmarshal(m.Body, &a); err != nil || m.Kind != PeerAsk || m.Re != "" {
			t.Fatalf("in the outbox: %+v, %v", m, err)
		}
		got = append(got, fmt.Sprintf("%s %s from %s", a.To, a.Ask.To, a.From))
	}
	return got
}

func (y *homeYard) kinds(t *testing.T, kind string) []Event {
	t.Helper()
	evs, err := y.Events(y.ctx, "", 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// Reach is a second grant on a link, the operator's alone to give and
// anyone's to take back: only for a name the link trusts as that user
// already, and only for one the roster says is a person. It goes with the
// trust it stands on, with the user, and with the roster's person.
func TestPeerAllowReach(t *testing.T) {
	y := reachYard(t)
	y.allow(t, PeerAllow{Trust: map[string]string{"yardmaster": "ada"}})
	for _, r := range []struct {
		reach map[string]string
		actor string
		want  string
	}{
		{map[string]string{"mckean": "mckean"}, "yardmaster", "only the operator (jo) may send a user's asks to a name of a peer; this is yardmaster"},
		{map[string]string{"ada": "ada"}, "jo", "peer north: the link does not trust ada@north as ada, and reach is given only to a name whose word is that user's already (yardr peer allow north --trust ada@north=ada first)"},
		{map[string]string{"mckean": "ada"}, "jo", "peer north: the link does not trust mckean@north as ada, and reach is given only to a name whose word is that user's already (yardr peer allow north --trust mckean@north=ada first)"},
		// A crew member has no decisions queue, whoever it is trusted as.
		{map[string]string{"yardmaster": "ada"}, "jo", "peer north: yardmaster is no person on the roster it last said, and only a person has decisions to be asked (yardr peer show north lists whom it exposes)"},
	} {
		if _, err := y.AllowPeer(y.ctx, "north", PeerAllow{Reach: r.reach}, r.actor); err == nil || err.Error() != r.want {
			t.Errorf("reach %v by %s = %v", r.reach, r.actor, err)
		}
	}
	if p, _ := y.GetPeer(y.ctx, "north"); p.Reach != nil {
		t.Fatalf("a refused change gave reach: %+v", p.Reach)
	}

	// Trust and reach in one command: trust is changed first.
	p := y.allow(t, PeerAllow{Trust: map[string]string{"ada": "ada"}, Reach: map[string]string{"ada": "ada", "mckean": "mckean"}})
	want := []PeerTrust{{Name: "ada", Peer: "north", As: "ada"}, {Name: "mckean", Peer: "north", As: "mckean"}}
	if got := p.Reaching(); !reflect.DeepEqual(got, want) {
		t.Fatalf("reaching = %+v", got)
	}
	allowed := y.kinds(t, "peer_allowed")
	if got, _ := json.Marshal(allowed[len(allowed)-1].Data["reach"]); string(got) != `[{"as":"ada","name":"ada","peer":"north"},{"as":"mckean","name":"mckean","peer":"north"}]` {
		t.Fatalf("peer_allowed says reach = %s", got)
	}
	// To take it back is anyone's, and leaves the trust.
	p, err := y.AllowPeer(y.ctx, "north", PeerAllow{NoReach: []string{"ada", "nobody"}}, "yardmaster")
	if err != nil || !reflect.DeepEqual(p.Reach, map[string]string{"mckean": "mckean"}) || p.Trust["ada"] != "ada" {
		t.Fatalf("taken back by a crew member: %+v, %v", p, err)
	}

	// Trust taken back, or given as another user, takes the reach along.
	if p := y.allow(t, PeerAllow{NoTrust: []string{"mckean"}}); p.Reach != nil {
		t.Fatalf("reach without trust: %+v", p.Reach)
	}
	allowed = y.kinds(t, "peer_allowed")
	if got, ok := allowed[len(allowed)-1].Data["reach"]; !ok {
		t.Fatalf("peer_allowed does not say the reach went: %v", got)
	}
	y.allow(t, PeerAllow{Trust: map[string]string{"mckean": "mckean"}, Reach: map[string]string{"mckean": "mckean"}})
	if p := y.allow(t, PeerAllow{Trust: map[string]string{"mckean": "ada"}}); p.Reach != nil {
		t.Fatalf("reach for mckean of a name now trusted as ada: %+v", p.Reach)
	}

	// The roster's person gone: the name a crew member now, then exposed
	// no more. The first leaves the trust, both are logged.
	y.allow(t, PeerAllow{Trust: map[string]string{"mckean": "mckean"}, Reach: map[string]string{"mckean": "mckean", "ada": "ada"}})
	y.says(t, "north", RosterCrew, RosterUser)
	p, _ = y.GetPeer(y.ctx, "north")
	if !reflect.DeepEqual(p.Reach, map[string]string{"ada": "ada"}) || p.Trust["mckean"] != "mckean" {
		t.Fatalf("after mckean@north became a crew member: reach %+v, trust %+v", p.Reach, p.Trust)
	}
	says := Says("v1")
	says.Roster = []PeerRosterEntry{{Name: "mckean", Kind: RosterUser}}
	y.now = y.now.Add(2 * peerSaidEvery)
	if err := y.PeerHeard(y.ctx, "north", y.key, says); err != nil {
		t.Fatal(err)
	}
	if p, _ = y.GetPeer(y.ctx, "north"); p.Reach != nil || p.Trust["ada"] != "" {
		t.Fatalf("after ada was exposed no more: reach %+v, trust %+v", p.Reach, p.Trust)
	}
	var dropped []string
	for _, e := range y.kinds(t, "peer_reach_dropped") {
		dropped = append(dropped, fmt.Sprintf("%v %v %v", e.Data["peer"], e.Data["names"], e.Data["reason"]))
	}
	if !slices.Equal(dropped, []string{"north [mckean] unexposed", "north [ada] unexposed"}) {
		t.Fatalf("peer_reach_dropped = %q", dropped)
	}

	// The user removed takes both grants, and the event names both.
	y.allow(t, PeerAllow{Reach: map[string]string{"mckean": "mckean"}})
	if err := y.DeleteUser(y.ctx, "mckean", "jo"); err != nil {
		t.Fatal(err)
	}
	if p, _ = y.GetPeer(y.ctx, "north"); p.Reach != nil || p.Trust != nil {
		t.Fatalf("after the user was removed: reach %+v, trust %+v", p.Reach, p.Trust)
	}
	removed := y.kinds(t, "user_removed")
	if e := removed[len(removed)-1]; fmt.Sprint(e.Data["unreached"]) != "[mckean@north]" || fmt.Sprint(e.Data["untrusted"]) != "[mckean@north]" {
		t.Fatalf("user_removed = %+v", e.Data)
	}
}

// An ask for a user of this yard is also queued for each name a link gives
// reach for them, addressed as that yard reads it, whoever asked: a crew
// member allowed the peer, one that is not, and a bead's agent. Nothing is
// queued by trust alone, for a crew name, for another user, over a link
// that carries no ask out, or for an ask that was open when reach was
// given.
func TestAskCopiedByReach(t *testing.T) {
	y := reachYard(t)
	y.allow(t, PeerAllow{Trust: map[string]string{"yardmaster": "mckean"}})
	bead, before := y.asked(t, linksAsk())
	if got := y.copies(t, "north"); len(got) != 0 {
		t.Fatalf("trust alone sent %q", got)
	}
	y.allow(t, PeerAllow{Reach: map[string]string{"mckean": "mckean"}})
	if got := y.copies(t, "north"); len(got) != 0 {
		t.Fatalf("giving reach sent what was open: %q", got)
	}

	for _, asker := range []string{"yardmaster", "clerk", "yd-k3m9-new", "ada"} {
		if _, err := y.AddAsk(y.ctx, bead, linksAsk(), asker); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"mckean mckean@north from yardmaster", "mckean mckean@north from clerk",
		"mckean mckean@north from yd-k3m9-new", "mckean mckean@north from ada",
	}
	if got := y.copies(t, "north"); !slices.Equal(got, want) {
		t.Fatalf("copies = %q", got)
	}
	// The ask is stored for the user, as it was written, and stands here.
	notes, _ := y.Notes(y.ctx, bead)
	if a, err := ParseAsk(notes[len(notes)-1]); err != nil || a.To != "mckean" {
		t.Fatalf("stored ask is to %q, %v", a.To, err)
	}
	if q := y.queue(t); len(q.Open) != 5 || q.Open[0].Ask != before {
		t.Fatalf("the home queue: %+v", q.Open)
	}
	// The copy is the message a yard takes as an ask today.
	out, _ := y.PeerOutbox(y.ctx, "north", 0)
	var sent peerAsk
	if err := json.Unmarshal(out[0].Body, &sent); err != nil || !validAsk(sent.To, sent.Ask) || sent.Bead != bead || sent.Title != "Move the links" || sent.V != PeerAskVersion {
		t.Fatalf("the copy: %+v, %v", sent, err)
	}

	// Another user's ask, and one for a name that is nobody's.
	ask := linksAsk()
	for _, to := range []string{"ada", "ghost"} {
		ask.To = to
		if _, err := y.AddAsk(y.ctx, bead, ask, "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	if got := y.copies(t, "north"); len(got) != len(want) {
		t.Fatalf("an ask for another user was copied: %q", got)
	}
	// An asker whose name no other yard is told: the ask stands here alone.
	if _, err := y.AddAsk(y.ctx, bead, linksAsk(), "DOMAIN\\jo"); err != nil {
		t.Fatal(err)
	}
	// A link that carries no ask out.
	y.allow(t, PeerAllow{NoSend: []string{PeerAsk}})
	if _, err := y.AddAsk(y.ctx, bead, linksAsk(), "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if got := y.copies(t, "north"); len(got) != len(want) {
		t.Fatalf("copies after those that are none: %q", got)
	}
	if got := y.refusals(t, RefusedKind); got != 0 {
		t.Fatalf("a copy not sent was logged as refused %d times", got)
	}
	ps, _ := y.Peers(y.ctx)
	if got := AskReach(ps, "mckean", "yardmaster"); len(got) != 0 {
		t.Fatalf("AskReach over a link without ask = %+v", got)
	}

	// A second link with reach for the same user: one copy at each.
	y.allow(t, PeerAllow{Send: []string{PeerAsk}})
	if _, err := y.AddPeer(y.ctx, Peer{Name: "south", Key: strings.Repeat("s", len(y.key))}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.says(t, "south", RosterUser, RosterUser)
	if _, err := y.AllowPeer(y.ctx, "south", PeerAllow{Send: []string{PeerAsk}, Trust: map[string]string{"mckean": "mckean"}, Reach: map[string]string{"mckean": "mckean"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddAsk(y.ctx, bead, linksAsk(), "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if n, s := y.copies(t, "north"), y.copies(t, "south"); len(n) != len(want)+1 || !slices.Equal(s, []string{"mckean mckean@south from yardmaster"}) {
		t.Fatalf("with two links: north %q, south %q", n, s)
	}
	ps, _ = y.Peers(y.ctx)
	if got := AskReach(ps, "mckean", "yardmaster"); len(got) != 2 || got[0].Address() != "mckean@north" || got[1].Address() != "mckean@south" {
		t.Fatalf("AskReach = %+v", got)
	}
}

// The answer of a name with reach lands on the ask for the user as a note
// by name@peer, marked as the user's word, and takes the ask out of the
// home queue. From any other name, and once the grant is taken back, it is
// an answer to no ask this yard asked that yard.
func TestReachAnswerLands(t *testing.T) {
	y := reachYard(t)
	y.allow(t, PeerAllow{Reach: map[string]string{"mckean": "mckean"}})
	bead, seq := y.asked(t, linksAsk())

	// A name the link trusts and gives no reach, and one it does not know.
	y.allow(t, PeerAllow{Trust: map[string]string{"ada": "mckean"}})
	for _, from := range []string{"ada", "yardmaster", "ghost"} {
		if got := y.take(t, y.answer(peerAnswer{From: from, Bead: bead, Seq: seq, Option: 1})); got.Refused != RefusedNoSuchAsk {
			t.Fatalf("an answer from %s = %+v", from, got)
		}
	}
	// An ask for another user is not mckean@north's to answer.
	other := linksAsk()
	other.To = "ada"
	otherSeq, err := y.AddAsk(y.ctx, bead, other, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: otherSeq, Option: 1})); got.Refused != RefusedNoSuchAsk {
		t.Fatalf("an answer to another user's ask = %+v", got)
	}
	if r := y.state(t, bead, seq); r.State != AskOpen || len(y.queue(t).Open) != 1 {
		t.Fatalf("after the refused answers: %v, %+v", r.State, y.queue(t).Open)
	}

	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 1, Words: "tonight"})); got.Refused != "" {
		t.Fatalf("the answer of the name with reach = %+v", got)
	}
	r := y.state(t, bead, seq)
	if r.State != AskAnswered || r.Current.Note.Author != "mckean@north" || r.Current.Answer.Option != 1 {
		t.Fatalf("after the pick: %+v", r)
	}
	ps, _ := y.Peers(y.ctx)
	if lines := strings.Join(r.LinesMarked(TrustedAs(ps)), "\n"); !strings.Contains(lines, "mckean@north (as mckean) picked 1 (Move them now): tonight") {
		t.Fatalf("lines:\n%s", lines)
	}
	if q := y.queue(t); len(q.Open) != 0 || len(q.Decided) != 1 || q.Decided[0].Ask != seq {
		t.Fatalf("the home queue after the pick: %+v", q)
	}

	// The grant taken back before the answer arrives.
	bead, seq = y.asked(t, linksAsk())
	y.allow(t, PeerAllow{NoReach: []string{"mckean"}})
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: bead, Seq: seq, Option: 2})); got.Refused != RefusedNoSuchAsk {
		t.Fatalf("an answer after the grant was taken back = %+v", got)
	}
	if r := y.state(t, bead, seq); r.State != AskOpen {
		t.Fatalf("the ask after it: %v", r.State)
	}
}

// An answer is words on the bead and moves nothing: on a bead in a stage
// of people it leaves the bead there, and a name with reach is no member
// of the stage's group.
func TestReachAnswerMovesNothing(t *testing.T) {
	y := reachYard(t)
	y.allow(t, PeerAllow{Reach: map[string]string{"mckean": "mckean"}})
	if err := y.PutDepot(y.ctx, Depot{Name: "yard", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	f, err := ParseFlow("\nnew -> decide\ndecide -> new | done\ndone terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "yard"
	if _, err := y.PutFlow(y.ctx, *f, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.SetFlowPointer(y.ctx, "yard", "", "yard", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(y.ctx, Group{Name: "decisions", Runner: "manual", Members: []string{"mckean"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddRoute(y.ctx, Route{Stage: "decide", Group: "decisions"}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := y.CreateBead(y.ctx, NewBead{Title: "Move the links", Depot: "yard", Stage: "decide"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := y.AddAsk(y.ctx, b.ID, linksAsk(), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if got := y.take(t, y.answer(peerAnswer{From: "mckean", Bead: b.ID, Seq: seq, Option: 1})); got.Refused != "" {
		t.Fatalf("the answer = %+v", got)
	}
	after, err := y.GetBead(y.ctx, b.ID)
	if err != nil || after.Stage != "decide" || after.Status != b.Status {
		t.Fatalf("the bead after the answer: %+v, %v", after, err)
	}
	if _, err := y.Advance(y.ctx, b.ID, "new", Expect{}, "mckean@north"); err == nil || !strings.Contains(err.Error(), "only members of group decisions may move it out of stage decide") {
		t.Fatalf("a move by the name with reach = %v", err)
	}
}

// A copy given up is told to the person it was for, by mail from the yard,
// and not to the asker, who addressed nobody at the other yard. The ask is
// untouched.
func TestReachCopyGivenUp(t *testing.T) {
	y := reachYard(t)
	y.allow(t, PeerAllow{Reach: map[string]string{"mckean": "mckean"}})
	bead, seq := y.asked(t, linksAsk())
	out, _ := y.PeerOutbox(y.ctx, "north", 0)
	if err := y.PeerRefused(y.ctx, "north", out[0].Seq, "peer north: refused by the peer: "+PeerRefusal("north", PeerAsk, RefusedRate)); err != nil {
		t.Fatal(err)
	}
	if mail := y.yardMail(t, "yardmaster"); len(mail) != 0 {
		t.Fatalf("the asker was told: %q", mail)
	}
	mail := y.yardMail(t, "mckean")
	head := fmt.Sprintf(`a question for you ("Move this yard's links to the declared source now?", ask %d on %s) was also sent to mckean@north, and that copy of `, seq, bead)
	tail := fmt.Sprintf(" It is open in this yard, where it is answered: yardr bead answer %s %d", bead, seq)
	if len(mail) != 1 || !strings.HasPrefix(mail[0], head) || !strings.Contains(mail[0], " was not delivered: refused by the peer: ") || !strings.HasSuffix(mail[0], tail) {
		t.Fatalf("mail = %q", mail)
	}
	for _, e := range y.kinds(t, "mail") {
		if e.Actor == "yard" && (e.Data["to_user"] != true || e.Data["undelivered"] != out[0].ID) {
			t.Fatalf("the mail's data: %+v", e.Data)
		}
	}
	if r := y.state(t, bead, seq); r.State != AskOpen || len(y.queue(t).Open) != 1 {
		t.Fatalf("the ask after its copy was given up: %v", r.State)
	}
}
