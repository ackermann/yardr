package store_test

import (
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// historyYard is trustYard whose link carries mail out too, whose clerk may
// write to north, and which trusts mckean@north as jo.
func historyYard(t *testing.T) *peerYard {
	t.Helper()
	y := trustYard(t)
	y.allow(t, store.PeerAllow{Send: []string{"mail"}, Expose: []string{"clerk"}, Trust: map[string]string{"mckean": "jo"}})
	y.member(t, "clerk", "north")
	return y
}

// came takes mail from a name of north.
func (y *peerYard) came(t *testing.T, id, to, from, text string) {
	t.Helper()
	if got, err := y.own.PeerReceive(y.ctx, "north", y.key, []store.PeerMessage{mail(id, to, from, text)}); err != nil || len(got) != 1 || got[0].Refused != "" {
		t.Fatalf("PeerReceive = %+v, %v", got, err)
	}
}

// went writes mail to a name of north.
func (y *peerYard) went(t *testing.T, to, text, actor string) {
	t.Helper()
	if _, err := y.api.MailPeer(y.ctx, to, "north", text, actor); err != nil {
		t.Fatal(err)
	}
}

// history is the conversation as "<from>><to> <text>[ as <user>]" of each
// letter.
func history(ls []store.Letter) string {
	var out []string
	for _, l := range ls {
		s := fmt.Sprintf("%s>%s %s", l.From, l.To, l.Text)
		if l.As != "" {
			s += " as " + l.As
		}
		out = append(out, s)
	}
	return strings.Join(out, " | ")
}

// A user's conversation with a crew member has what the user wrote under a
// name a link trusts as them, and what the crew member wrote to that name,
// in order with the mail between the two as named. Each says how it went.
// Mail of a name that is not trusted, and mail with anyone else, is not in
// it; the address alone is still its own conversation.
func TestConversationHasTheTrustedName(t *testing.T) {
	t.Parallel()
	y := historyYard(t)
	for _, err := range []error{
		y.api.Mail(y.ctx, "clerk", "one", "jo"),
		y.api.Mail(y.ctx, "jo", "two", "clerk"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	y.came(t, "m1", "clerk", "mckean", "three")
	y.went(t, "mckean", "four", "clerk")
	y.came(t, "m2", "clerk", "clerk", "not trusted")
	y.went(t, "clerk", "to one not trusted", "clerk")
	y.came(t, "m3", "yardmaster", "mckean", "to another")
	y.went(t, "mckean", "from another", "jo")
	y.came(t, "m4", "clerk", "mckean", "five")

	want := "jo>clerk one | clerk>jo two | mckean@north>clerk three as jo | clerk>mckean@north four | mckean@north>clerk five as jo"
	all, more, err := y.api.Conversation(y.ctx, "jo", "clerk", 0, 0, 0)
	if err != nil || more || history(all) != want {
		t.Fatalf("jo with clerk =\n%s, more %v, %v; want\n%s", history(all), more, err, want)
	}
	if rev, _, _ := y.api.Conversation(y.ctx, "clerk", "jo", 0, 0, 0); history(rev) != want {
		t.Fatalf("clerk with jo = %s", history(rev))
	}
	// What was written there has a delivery, and what came is read as its
	// receiver reads it.
	if all[3].Delivery != store.MailQueued || all[2].Delivery != "" || all[2].Read {
		t.Fatalf("the letters = %+v", all[2:4])
	}
	if err := y.api.AckInbox(y.ctx, "clerk", all[2].Seq); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := y.api.Conversation(y.ctx, "jo", "clerk", 0, 0, 0); !got[2].Read || got[4].Read {
		t.Fatalf("after clerk read through the third: %+v", got)
	}

	// The other conversations of the same mail.
	for _, c := range []struct{ a, b, want string }{
		{"jo", "yardmaster", "mckean@north>yardmaster to another as jo"},
		// With themselves: what they wrote to their own name there.
		{"jo", "jo", "jo>mckean@north from another"},
		{"clerk", "mckean@north", "mckean@north>clerk three as jo | clerk>mckean@north four | mckean@north>clerk five as jo"},
		{"clerk", "clerk@north", "clerk@north>clerk not trusted | clerk>clerk@north to one not trusted"},
		// ada is a user nobody is trusted as, and a crew member has no
		// name elsewhere.
		{"ada", "clerk", ""},
		{"clerk", "yardmaster", ""},
	} {
		if got, _, err := y.api.Conversation(y.ctx, c.a, c.b, 0, 0, 0); err != nil || history(got) != c.want {
			t.Errorf("%s with %s = %q, %v; want %q", c.a, c.b, history(got), err, c.want)
		}
	}
}

// The grant says who the name is, and nothing of since when: mail from
// before it is in the user's conversation too, both ways, and what came
// then has no mark, since nobody had said whose word it was.
func TestConversationHasTheNamesMailFromBeforeTheGrant(t *testing.T) {
	t.Parallel()
	y := trustYard(t)
	y.allow(t, store.PeerAllow{Send: []string{"mail"}, Expose: []string{"clerk"}})
	y.member(t, "clerk", "north")
	y.came(t, "m1", "clerk", "mckean", "one")
	y.went(t, "mckean", "two", "clerk")
	if got, _, err := y.api.Conversation(y.ctx, "jo", "clerk", 0, 0, 0); err != nil || len(got) != 0 {
		t.Fatalf("without a grant = %q, %v", history(got), err)
	}
	y.allow(t, store.PeerAllow{Trust: map[string]string{"mckean": "jo"}})
	y.came(t, "m2", "clerk", "mckean", "three")
	want := "mckean@north>clerk one | clerk>mckean@north two | mckean@north>clerk three as jo"
	if got, _, err := y.api.Conversation(y.ctx, "jo", "clerk", 0, 0, 0); err != nil || history(got) != want {
		t.Fatalf("with the grant = %q, %v; want %q", history(got), err, want)
	}
}

// Nothing of a trusted name's mail is in the user's inbox here: none of it
// is unread for them, and an acknowledgement past it reads no mail.
func TestTrustedNamesMailIsNotInTheUsersInbox(t *testing.T) {
	t.Parallel()
	y := historyYard(t)
	y.came(t, "m1", "clerk", "mckean", "one")
	y.went(t, "mckean", "two", "clerk")
	y.went(t, "mckean", "three", "clerk")
	all, _, err := y.api.Conversation(y.ctx, "jo", "clerk", 0, 0, 0)
	if err != nil || len(all) != 3 {
		t.Fatalf("the conversation = %s, %v", history(all), err)
	}
	box, err := y.api.UserInbox(y.ctx, "jo", 0, "jo")
	if err != nil || box.Unread != 0 || len(box.Mail) != 0 || box.Last != 0 {
		t.Fatalf("jo's inbox = %+v, %v", box, err)
	}
	was, _ := y.api.GetUser(y.ctx, "jo")
	if err := y.api.AckUserInbox(y.ctx, "jo", all[2].Seq, "jo"); err != nil {
		t.Fatal(err)
	}
	if read := peerEvents(t, y.api, "mail_read"); len(read) != 0 {
		t.Fatalf("an acknowledgement past them read mail: %+v", read)
	}
	// Mail to jo in this yard is unread as ever, beside them.
	if err := y.api.Mail(y.ctx, "jo", "four", "clerk"); err != nil {
		t.Fatal(err)
	}
	box, _ = y.api.UserInbox(y.ctx, "jo", 0, "jo")
	all, _, _ = y.api.Conversation(y.ctx, "jo", "clerk", 0, 0, 0)
	if box.Unread != 1 || box.Last != all[3].Seq || all[3].Read || was.Cursor >= all[3].Seq {
		t.Fatalf("jo's inbox = %+v, the conversation %+v", box, all)
	}
}

// The pages of a conversation count the trusted name's mail with the
// rest: the newest limit of all of it, before a seq and after one.
func TestConversationPagesOverTheTrustedName(t *testing.T) {
	t.Parallel()
	y := historyYard(t)
	for i := 0; i < 3; i++ {
		if err := y.api.Mail(y.ctx, "clerk", fmt.Sprintf("a%d", i), "jo"); err != nil {
			t.Fatal(err)
		}
		y.came(t, fmt.Sprintf("m%d", i), "clerk", "mckean", fmt.Sprintf("b%d", i))
		y.went(t, "mckean", fmt.Sprintf("c%d", i), "clerk")
		y.came(t, fmt.Sprintf("x%d", i), "yardmaster", "mckean", "not this")
	}
	texts := func(ls []store.Letter) string {
		var out []string
		for _, l := range ls {
			out = append(out, l.Text)
		}
		return strings.Join(out, " ")
	}
	all, more, err := y.api.Conversation(y.ctx, "jo", "clerk", 0, 0, 0)
	if err != nil || more || texts(all) != "a0 b0 c0 a1 b1 c1 a2 b2 c2" {
		t.Fatalf("the conversation = %s, more %v, %v", texts(all), more, err)
	}
	for _, c := range []struct {
		after  uint64
		limit  int
		before uint64
		want   string
		more   bool
	}{
		{0, 4, 0, "c1 a2 b2 c2", true},
		{0, 4, all[5].Seq, "b0 c0 a1 b1", true},
		{0, 4, all[1].Seq, "a0", false},
		{0, 9, 0, "a0 b0 c0 a1 b1 c1 a2 b2 c2", false},
		{all[6].Seq, 0, 0, "b2 c2", false},
		{all[2].Seq, 2, all[7].Seq, "c1 a2", true},
	} {
		got, more, err := y.api.Conversation(y.ctx, "jo", "clerk", c.after, c.limit, c.before)
		if err != nil || texts(got) != c.want || more != c.more {
			t.Errorf("after %d limit %d before %d = %q, more %v, %v; want %q, more %v", c.after, c.limit, c.before, texts(got), more, err, c.want, c.more)
		}
	}
}

// Trust taken back keeps the mail of its time in the user's conversation:
// it was their word when it was written. What the name writes afterwards,
// and what is written to it, is the name's alone. So it is when the peer
// takes the name off its roster, and when the name is trusted as someone
// else.
func TestTrustTakenBackKeepsTheHistory(t *testing.T) {
	t.Parallel()
	for _, how := range []string{"stopped", "unexposed", "another"} {
		t.Run(how, func(t *testing.T) {
			y := historyYard(t)
			y.came(t, "m1", "clerk", "mckean", "one")
			y.went(t, "mckean", "two", "clerk")
			switch how {
			case "stopped":
				y.allow(t, store.PeerAllow{NoTrust: []string{"mckean"}})
			case "unexposed":
				if err := y.own.PeerHeard(y.ctx, "north", y.key, northSays("clerk")); err != nil {
					t.Fatal(err)
				}
				if err := y.own.PeerHeard(y.ctx, "north", y.key, northSays("mckean", "clerk")); err != nil {
					t.Fatal(err)
				}
			case "another":
				y.allow(t, store.PeerAllow{Trust: map[string]string{"mckean": "ada"}})
			}
			p, err := y.api.GetPeer(y.ctx, "north")
			if err != nil || p.Trust["mckean"] == "jo" || len(p.Spoke) != 1 || p.Spoke[0].Name != "mckean" || p.Spoke[0].As != "jo" || p.Spoke[0].Until == 0 {
				t.Fatalf("the link = trust %+v, spoke %+v, %v", p.Trust, p.Spoke, err)
			}
			y.came(t, "m2", "clerk", "mckean", "three")
			y.went(t, "mckean", "four", "clerk")

			want := "mckean@north>clerk one as jo | clerk>mckean@north two"
			if got, _, err := y.api.Conversation(y.ctx, "jo", "clerk", 0, 0, 0); err != nil || history(got) != want {
				t.Fatalf("jo with clerk = %q, %v; want %q", history(got), err, want)
			}
			later := ""
			if how == "another" {
				later = "mckean@north>clerk three as ada | clerk>mckean@north four"
			}
			if got, _, err := y.api.Conversation(y.ctx, "ada", "clerk", 0, 0, 0); err != nil || history(got) != later {
				t.Fatalf("ada with clerk = %q, %v; want %q", history(got), err, later)
			}
			if got, _, _ := y.api.Conversation(y.ctx, "clerk", "mckean@north", 0, 0, 0); len(got) != 4 {
				t.Fatalf("the address alone = %s", history(got))
			}
		})
	}
}

// The mail goes by the link and not by its name: after a rename it is in
// the user's conversation under the address of today, and once the link is
// removed it is in none, as the address reaches nobody.
func TestHistoryFollowsTheLink(t *testing.T) {
	t.Parallel()
	y := historyYard(t)
	y.came(t, "m1", "clerk", "mckean", "one")
	y.went(t, "mckean", "two", "clerk")
	if _, err := y.api.RenamePeer(y.ctx, "north", "up", "jo"); err != nil {
		t.Fatal(err)
	}
	want := "mckean@up>clerk one as jo | clerk>mckean@up two"
	if got, _, err := y.api.Conversation(y.ctx, "jo", "clerk", 0, 0, 0); err != nil || history(got) != want {
		t.Fatalf("after the rename = %q, %v; want %q", history(got), err, want)
	}
	if err := y.api.DeletePeer(y.ctx, "up", "jo"); err != nil {
		t.Fatal(err)
	}
	if got, _, err := y.api.Conversation(y.ctx, "jo", "clerk", 0, 0, 0); err != nil || len(got) != 0 {
		t.Fatalf("after the link went = %q, %v", history(got), err)
	}
}
