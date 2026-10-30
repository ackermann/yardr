package store_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// northSays is what north says of itself with the names it exposes, each a
// user of its yard.
func northSays(names ...string) *store.PeerSays {
	says := store.Says("v1")
	for _, n := range names {
		says.Roster = append(says.Roster, store.PeerRosterEntry{Name: n, Kind: store.RosterUser, Role: "user", Mail: true})
	}
	return says
}

// trustYard is rosterYard whose link takes mail for ada and the yardmaster,
// and whose peer north exposes mckean and clerk.
func trustYard(t *testing.T) *peerYard {
	t.Helper()
	y, _ := rosterYard(t)
	y.allow(t, store.PeerAllow{Receive: []string{"mail"}, Expose: []string{"ada", "yardmaster"}})
	if err := y.own.PeerHeard(y.ctx, "north", y.key, northSays("mckean", "clerk")); err != nil {
		t.Fatal(err)
	}
	return y
}

// The operator says that a name on a peer's roster is trusted as a user of
// this yard, and nobody else does; a name the peer does not expose, and a
// user this yard has not, are refused with which it was. To stop is
// anyone's.
func TestPeerAllowTrust(t *testing.T) {
	t.Parallel()
	y := trustYard(t)
	refused := []struct {
		trust map[string]string
		actor string
		want  string
	}{
		{map[string]string{"mckean": "ada"}, "yardmaster", "only the operator (jo) may trust a name of a peer as a user of the yard; this is yardmaster"},
		{map[string]string{"nobody": "ada"}, "jo", `peer north: it exposes nobody called "nobody" to this yard, and only a name on its roster is trusted (yardr peer show north lists whom it exposes)`},
		{map[string]string{"mckean": "bob"}, "jo", `peer north: "bob" is no user of this yard, and mckean@north is trusted as one of them (yardr user list)`},
		// A crew member is no person to speak as.
		{map[string]string{"mckean": "yardmaster"}, "jo", `peer north: "yardmaster" is no user of this yard, and mckean@north is trusted as one of them (yardr user list)`},
	}
	for _, r := range refused {
		if _, err := y.api.AllowPeer(y.ctx, "north", store.PeerAllow{Trust: r.trust}, r.actor); err == nil || err.Error() != r.want {
			t.Errorf("trusting %v as %s = %v", r.trust, r.actor, err)
		}
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Trust != nil {
		t.Fatalf("a refused change trusted %+v", p.Trust)
	}

	p := y.allow(t, store.PeerAllow{Trust: map[string]string{"mckean": "ada", "clerk": "jo"}})
	want := []store.PeerTrust{{Name: "clerk", Peer: "north", As: "jo"}, {Name: "mckean", Peer: "north", As: "ada"}}
	if got := p.Trusted(); !reflect.DeepEqual(got, want) || got[1].String() != "mckean@north -> ada" {
		t.Fatalf("trusted = %+v", got)
	}
	allowed := peerEvents(t, y.api, "peer_allowed")
	if got, _ := json.Marshal(allowed[len(allowed)-1].Data["trust"]); string(got) != `[{"as":"jo","name":"clerk","peer":"north"},{"as":"ada","name":"mckean","peer":"north"}]` {
		t.Fatalf("peer_allowed says trust = %s", got)
	}
	// As every reader of the link has it, and a change of something else
	// leaves it.
	if ps, err := y.api.Peers(y.ctx); err != nil || !reflect.DeepEqual(ps[0].Trusted(), want) {
		t.Fatalf("Peers: trusted = %+v, %v", ps, err)
	}
	if p := y.allow(t, store.PeerAllow{Send: []string{"mail"}}); !reflect.DeepEqual(p.Trusted(), want) {
		t.Fatalf("after another change: %+v", p.Trust)
	}
	if p, err := y.api.AllowPeer(y.ctx, "north", store.PeerAllow{NoTrust: []string{"clerk", "nobody"}}, "yardmaster"); err != nil ||
		!reflect.DeepEqual(p.Trust, map[string]string{"mckean": "ada"}) {
		t.Fatalf("stopped by a crew member: %+v, %v", p, err)
	}
	if p := y.allow(t, store.PeerAllow{NoTrust: []string{"mckean"}}); p.Trust != nil || len(p.Trusted()) != 0 {
		t.Fatalf("with all stopped: %+v", p.Trust)
	}
}

// Mail from a name the link trusts says the user it speaks as, to a crew
// member and to a user alike, and mail from any other name is as it was. It
// is marked when it is taken: a grant given or taken back later changes no
// mail that came before.
func TestPeerMailFromTheTrusted(t *testing.T) {
	t.Parallel()
	y := trustYard(t)
	take := func(m store.PeerMessage) {
		t.Helper()
		if got, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{m}); err != nil || len(got) != 1 || got[0].Refused != "" {
			t.Fatalf("PeerReceive = %+v, %v", got, err)
		}
	}
	take(mail("m1", "yardmaster", "mckean", "before the grant"))
	y.allow(t, store.PeerAllow{Trust: map[string]string{"mckean": "jo"}})
	take(mail("m2", "yardmaster", "mckean", "land it"))
	take(mail("m3", "yardmaster", "clerk", "the books are closed"))
	take(mail("m4", "ada", "mckean", "lunch?"))
	take(mail("m5", "ada", "clerk", "an invoice"))

	got := y.inbox(t, "yardmaster")
	if len(got) != 3 {
		t.Fatalf("the inbox = %+v", got)
	}
	for i, want := range []struct{ as, from string }{
		{"", "mckean@north"},
		{"jo", "mckean@north (as jo)"},
		{"", "clerk@north"},
	} {
		if e := got[i]; store.SpeaksAs(e) != want.as || store.MailFrom(e) != want.from || (e.Data["as"] != nil) != (want.as != "") || !store.FromOutside(e) {
			t.Errorf("mail %d speaks as %q, is from %q, as = %v; want %+v", i, store.SpeaksAs(e), store.MailFrom(e), e.Data["as"], want)
		}
	}
	box, err := y.api.UserInbox(y.ctx, "ada", 0, "ada")
	if err != nil || len(box.Mail) != 2 || box.Mail[0].From != "mckean@north" || box.Mail[0].As != "jo" || box.Mail[1].As != "" {
		t.Fatalf("ada's inbox = %+v, %v", box, err)
	}
	if conv, _, err := y.api.Conversation(y.ctx, "ada", "mckean@north", 0, 0, 0); err != nil || len(conv) != 1 || conv[0].As != "jo" {
		t.Fatalf("the conversation = %+v, %v", conv, err)
	}
	if raw, _ := json.Marshal(box.Mail[1]); strings.Contains(string(raw), `"as"`) {
		t.Fatalf("a letter from an untrusted name says as: %s", raw)
	}

	// Taken back: the next mail speaks as nobody, the one that came keeps
	// what it was taken as.
	y.allow(t, store.PeerAllow{NoTrust: []string{"mckean"}})
	take(mail("m6", "yardmaster", "mckean", "after"))
	if got := y.inbox(t, "yardmaster"); store.SpeaksAs(got[1]) != "jo" || store.SpeaksAs(got[3]) != "" {
		t.Fatalf("after --no-trust: %q, %q", store.SpeaksAs(got[1]), store.SpeaksAs(got[3]))
	}
	// A yard that is linked no more has no grants: its mail speaks as
	// nobody, as it has no address.
	if err := y.api.DeletePeer(y.ctx, "north", "jo"); err != nil {
		t.Fatal(err)
	}
	if got := y.inbox(t, "yardmaster"); store.SpeaksAs(got[1]) != "" || strings.Contains(store.MailFrom(got[1]), "(as ") {
		t.Fatalf("from a yard removed: speaks as %q, from %q", store.SpeaksAs(got[1]), store.MailFrom(got[1]))
	}
}

// A grant lasts while both its ends do. The user removed takes it off every
// link, and the peer does once it exposes the name no more: whoever has
// either name next is trusted as nobody. Both are logged.
func TestTrustGoesWithItsEnds(t *testing.T) {
	t.Parallel()
	y := trustYard(t)
	y.allow(t, store.PeerAllow{Trust: map[string]string{"mckean": "ada", "clerk": "jo"}})
	if err := y.api.DeleteUser(y.ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); !reflect.DeepEqual(p.Trust, map[string]string{"clerk": "jo"}) {
		t.Fatalf("after the user was removed: %+v", p.Trust)
	}
	if ev := peerEvents(t, y.api, "user_removed"); len(ev) != 1 || fmt.Sprint(ev[0].Data["untrusted"]) != "[mckean@north]" {
		t.Fatalf("user_removed = %+v", ev)
	}
	// A user of the name again is another, whom nobody speaks as.
	if _, err := y.api.AddUser(y.ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Expose: []string{"ada"}})
	if got, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{mail("m1", "ada", "mckean", "hi")}); err != nil || got[0].Refused != "" {
		t.Fatalf("PeerReceive = %+v, %v", got, err)
	}
	if box, _ := y.api.UserInbox(y.ctx, "ada", 0, "ada"); len(box.Mail) != 1 || box.Mail[0].As != "" {
		t.Fatalf("mail to the ada of now = %+v", box.Mail)
	}

	// What the peer says with the same roster drops nothing.
	if err := y.own.PeerHeard(y.ctx, "north", y.key, northSays("clerk", "mckean")); err != nil {
		t.Fatal(err)
	}
	if ev := peerEvents(t, y.api, "peer_trust_dropped"); len(ev) != 0 {
		t.Fatalf("peer_trust_dropped with nothing gone = %+v", ev)
	}
	// The answer to a question takes clerk off the roster and hands over
	// clerk's mail: the grant is gone before the mail is taken.
	body, err := json.Marshal(store.PeerPage{Says: northSays("mckean"), Messages: []store.PeerMessage{mail("m2", "yardmaster", "clerk", "release it")}})
	if err != nil {
		t.Fatal(err)
	}
	batch := store.PeerBatch{RequestSig: "req-1", Signature: store.SignResponse(y.north, 200, "req-1", body), Body: body}
	if res, err := y.api.PeerPulled(y.ctx, "north", batch); err != nil || len(res.Taken) != 1 || res.Taken[0].Refused != "" {
		t.Fatalf("PeerPulled = %+v, %v", res, err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Trust != nil {
		t.Fatalf("after the peer exposed clerk no more: %+v", p.Trust)
	}
	if got := y.inbox(t, "yardmaster"); len(got) != 1 || store.SpeaksAs(got[0]) != "" {
		t.Fatalf("mail of a name just unexposed = %+v", got)
	}
	ev := peerEvents(t, y.api, "peer_trust_dropped")
	if len(ev) != 1 || ev[0].Actor != "peer:north" || ev[0].Data["peer"] != "north" || fmt.Sprint(ev[0].Data["names"]) != "[clerk]" || ev[0].Data["reason"] != "unexposed" {
		t.Fatalf("peer_trust_dropped = %+v", ev)
	}
	// Exposed again, the name is one the operator has trusted as nobody.
	if err := y.own.PeerHeard(y.ctx, "north", y.key, northSays("mckean", "clerk")); err != nil {
		t.Fatal(err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Trust != nil {
		t.Fatalf("exposed again: %+v", p.Trust)
	}
	// A peer that says nothing any more, an older yardr, exposes nobody.
	y.allow(t, store.PeerAllow{Trust: map[string]string{"mckean": "jo"}})
	if err := y.own.PeerHeard(y.ctx, "north", y.key, nil); err != nil {
		t.Fatal(err)
	}
	if p, _ := y.api.GetPeer(y.ctx, "north"); p.Trust != nil || len(peerEvents(t, y.api, "peer_trust_dropped")) != 2 {
		t.Fatalf("after the peer said nothing: %+v", p.Trust)
	}
}

// The conversation with a name of a linked yard is read in pages as one of
// this yard is, and a letter the name wrote as a trusted user says so on
// whichever page it is.
func TestPeerConversationPages(t *testing.T) {
	t.Parallel()
	y := trustYard(t)
	take := func(m store.PeerMessage) {
		t.Helper()
		if got, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{m}); err != nil || len(got) != 1 || got[0].Refused != "" {
			t.Fatalf("PeerReceive = %+v, %v", got, err)
		}
	}
	take(mail("m1", "ada", "mckean", "one"))
	y.allow(t, store.PeerAllow{Trust: map[string]string{"mckean": "jo"}})
	for i, text := range []string{"two", "three", "four", "five"} {
		take(mail(fmt.Sprintf("m%d", i+2), "ada", "mckean", text))
		take(mail(fmt.Sprintf("c%d", i), "ada", "clerk", "not this"))
	}
	said := func(ls []store.Letter) string {
		var out []string
		for _, l := range ls {
			out = append(out, strings.TrimSpace(l.Text+" "+l.As))
		}
		return strings.Join(out, ", ")
	}
	all, more, err := y.api.Conversation(y.ctx, "ada", "mckean@north", 0, 0, 0)
	if err != nil || more || said(all) != "one, two jo, three jo, four jo, five jo" {
		t.Fatalf("the conversation = %s, more %v, %v", said(all), more, err)
	}
	for _, c := range []struct {
		limit  int
		before uint64
		want   string
		more   bool
	}{
		{2, 0, "four jo, five jo", true},
		{2, all[3].Seq, "two jo, three jo", true},
		{2, all[1].Seq, "one", false},
		{0, all[2].Seq, "one, two jo", false},
		{5, 0, "one, two jo, three jo, four jo, five jo", false},
	} {
		got, more, err := y.api.Conversation(y.ctx, "ada", "mckean@north", 0, c.limit, c.before)
		if err != nil || got == nil || said(got) != c.want || more != c.more || (len(got) > 0 && got[0].From != "mckean@north") {
			t.Errorf("limit %d before %d = %q, more %v, %v; want %q, more %v", c.limit, c.before, said(got), more, err, c.want, c.more)
		}
	}
	if got, more, _ := y.api.Conversation(y.ctx, "ada", "mckean@north", all[1].Seq, 2, 0); said(got) != "four jo, five jo" || !more {
		t.Errorf("after the second, the newest two = %q, more %v", said(got), more)
	}
}
