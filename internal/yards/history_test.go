package yards_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yards"
)

// A person has two yards: home, where they are the user mckean and the
// yardmaster works, and airy, where they are mckean too. Home trusts
// mckean@airy as mckean. What they write the yardmaster from airy, and what
// the yardmaster answers there, crosses the link and is, at home, in
// mckean's conversation with the yardmaster, in order with the mail between
// the two at home. A name of airy that is not trusted is in none of it,
// nothing of it is unread for mckean, the pages count it, and trust taken
// back keeps it.
func TestTrustedNameIsOneHistoryAcrossYards(t *testing.T) {
	home, airy := newYard(t), newYard(t)
	ctx := context.Background()
	mail := []string{"mail"}
	toAiry := home.add(t, "airy", airy, true, mail, mail)
	toHome := airy.add(t, "home", home, true, mail, mail)
	for _, y := range []*yard{home, airy} {
		if _, err := y.api.AddUser(ctx, "mckean", "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := airy.api.AddUser(ctx, "guest", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := home.api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{store.MailPeers: "airy"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	// Each posts with the roster it exposes, as the store hands the peer out.
	toAiry = home.expose(t, "airy", "yardmaster")
	toHome = airy.expose(t, "home", "mckean", "guest")
	says := yards.Says()
	for _, name := range []string{"mckean", "guest"} {
		says.Roster = append(says.Roster, store.PeerRosterEntry{Name: name, Kind: store.RosterUser, Mail: true})
	}
	if err := home.own.PeerHeard(ctx, "airy", airy.pub(), says); err != nil {
		t.Fatal(err)
	}
	if err := airy.own.PeerHeard(ctx, "home", home.pub(), yards.Says()); err != nil {
		t.Fatal(err)
	}
	if _, err := home.api.AllowPeer(ctx, "airy", store.PeerAllow{Trust: map[string]string{"mckean": "mckean"}}, "jo"); err != nil {
		t.Fatal(err)
	}

	// fromAiry is mail written at airy to home's yardmaster, and delivered;
	// toAiryFrom is the yardmaster's mail to a name of airy, likewise.
	fromAiry := func(who, text string) {
		t.Helper()
		if _, err := airy.api.MailPeer(ctx, "yardmaster", "home", text, who); err != nil {
			t.Fatal(err)
		}
		airy.post(t, toHome)
	}
	toAiryFrom := func(who, text string) {
		t.Helper()
		if _, err := home.api.MailPeer(ctx, who, "airy", text, "yardmaster"); err != nil {
			t.Fatal(err)
		}
		home.post(t, toAiry)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(home.api.Mail(ctx, "yardmaster", "at home: paint the shed", "mckean"))
	fromAiry("mckean", "at airy: and the fence")
	toAiryFrom("mckean", "the fence is next")
	fromAiry("guest", "a guest writes")
	toAiryFrom("guest", "welcome")
	must(home.api.Mail(ctx, "mckean", "the shed is painted", "yardmaster"))
	fromAiry("mckean", "at airy: thanks")

	said := func(ls []store.Letter) string {
		var out []string
		for _, l := range ls {
			s := fmt.Sprintf("%s>%s %s", l.From, l.To, l.Text)
			if l.As != "" {
				s += " (as " + l.As + ")"
			}
			if l.Delivery != "" {
				s += " [" + l.Delivery + "]"
			}
			out = append(out, s)
		}
		return strings.Join(out, "\n")
	}
	want := []string{
		"mckean>yardmaster at home: paint the shed",
		"mckean@airy>yardmaster at airy: and the fence (as mckean)",
		"yardmaster>mckean@airy the fence is next [sent]",
		"yardmaster>mckean the shed is painted",
		"mckean@airy>yardmaster at airy: thanks (as mckean)",
	}
	all, more, err := home.api.Conversation(ctx, "mckean", "yardmaster", 0, 0, 0)
	if err != nil || more || said(all) != strings.Join(want, "\n") {
		t.Fatalf("mckean with the yardmaster, at home =\n%s\nmore %v, %v; want\n%s", said(all), more, err, strings.Join(want, "\n"))
	}
	// What arrived at airy is there, in the conversation airy has of it.
	if got, _, err := airy.api.Conversation(ctx, "mckean", "yardmaster@home", 0, 0, 0); err != nil || len(got) != 3 {
		t.Fatalf("at airy = %s, %v", said(got), err)
	}

	// The name that is not trusted is its own conversation, and no user's.
	if got, _, err := home.api.Conversation(ctx, "yardmaster", "guest@airy", 0, 0, 0); err != nil || len(got) != 2 {
		t.Fatalf("the yardmaster with guest@airy = %s, %v", said(got), err)
	}

	// Unread for mckean at home is the one mail written to them there.
	box, err := home.api.UserInbox(ctx, "mckean", 0)
	if err != nil || box.Unread != 1 || box.Last != all[3].Seq || len(box.Mail) != 1 {
		t.Fatalf("mckean's inbox at home = %+v, %v", box, err)
	}
	must(home.api.AckUserInbox(ctx, "mckean", all[4].Seq))
	if read := home.events(t, "mail_read"); len(read) != 1 || read[0].Data["reader"] != "mckean" {
		t.Fatalf("mail_read = %+v", read)
	}
	after, _, _ := home.api.Conversation(ctx, "mckean", "yardmaster", 0, 0, 0)
	if !after[3].Read || after[1].Read || after[2].Read || after[4].Read {
		t.Fatalf("after mckean read their inbox:\n%+v", after)
	}

	// In pages, from the newest end.
	page, more, err := home.api.Conversation(ctx, "mckean", "yardmaster", 0, 2, 0)
	if err != nil || !more || said(page) != strings.Join(want[3:], "\n") {
		t.Fatalf("the newest two =\n%s\nmore %v, %v", said(page), more, err)
	}
	page, more, err = home.api.Conversation(ctx, "mckean", "yardmaster", 0, 2, page[0].Seq)
	if err != nil || !more || said(page) != strings.Join(want[1:3], "\n") {
		t.Fatalf("the two before =\n%s\nmore %v, %v", said(page), more, err)
	}
	page, more, err = home.api.Conversation(ctx, "mckean", "yardmaster", 0, 2, page[0].Seq)
	if err != nil || more || said(page) != want[0] {
		t.Fatalf("the oldest page =\n%s\nmore %v, %v", said(page), more, err)
	}

	// Trust taken back: what was said stays mckean's, what follows is the
	// name's alone.
	if _, err := home.api.AllowPeer(ctx, "airy", store.PeerAllow{NoTrust: []string{"mckean"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	fromAiry("mckean", "trusted no more")
	toAiryFrom("mckean", "who is this?")
	if got, _, err := home.api.Conversation(ctx, "mckean", "yardmaster", 0, 0, 0); err != nil || said(got) != strings.Join(want, "\n") {
		t.Fatalf("after the trust went =\n%s\n%v", said(got), err)
	}
	if got, _, err := home.api.Conversation(ctx, "yardmaster", "mckean@airy", 0, 0, 0); err != nil || len(got) != 5 {
		t.Fatalf("the yardmaster with mckean@airy = %s, %v", said(got), err)
	}
}
