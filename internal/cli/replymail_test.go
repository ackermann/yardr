package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// replyMailYard is a yard with operator jo (setting and user), crew member
// yardmaster and user ada: mail to the yardmaster can be answered with
// yardr reply. The session environment is cleared, so the run acts as the
// actor the test names.
func replyMailYard(t *testing.T) (*app, store.API) {
	t.Helper()
	for _, k := range []string{"ACTOR", "BEAD", "SESSION", "CREW"} {
		t.Setenv("YARDR_"+k, "")
	}
	st, _ := storetest.NewOwned(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, "operator", "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	// Setting the operator made its user; ada is the other one.
	if _, err := st.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "jo"); err != nil {
		t.Fatal(err)
	}
	return &app{st: st, actor: "yardmaster"}, st
}

// mailSeq is the seq of the mail the test sent: the yardmaster's inbox has
// nothing else.
func mailSeq(t *testing.T, st store.API) string {
	t.Helper()
	page, err := st.Inbox(context.Background(), "yardmaster", 10)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != "mail" {
		t.Fatalf("yardmaster's inbox = %+v, %v", page, err)
	}
	return strconv.FormatUint(page.Events[0].Seq, 10)
}

// TestReplyToLocalMail answers a user's mail with yardr reply: the reply
// is mail to that user, as yardr mail would send it, and to nobody else.
func TestReplyToLocalMail(t *testing.T) {
	a, st := replyMailYard(t)
	ctx := context.Background()
	t.Setenv("YARDR_CREW", "yardmaster")
	if err := st.Mail(ctx, "yardmaster", "when is the freeze over?", "jo"); err != nil {
		t.Fatal(err)
	}
	seq := mailSeq(t, st)

	out, err := run(t, a.replyCmd(), seq, "Friday; I'll post here when it lands.")
	if err != nil || out != "mail sent to jo\n" {
		t.Fatalf("reply = %q, %v", out, err)
	}
	jo, err := st.UserInbox(ctx, "jo", 10)
	if err != nil || jo.Unread != 1 || len(jo.Mail) != 1 ||
		jo.Mail[0].Text != "Friday; I'll post here when it lands." || jo.Mail[0].From != "yardmaster" {
		t.Fatalf("jo's inbox = %+v, %v", jo, err)
	}
	ada, err := st.UserInbox(ctx, "ada", 10)
	if err != nil || ada.Unread != 0 {
		t.Fatalf("ada's inbox = %+v, %v", ada, err)
	}

	// From stdin, as JSON.
	c := a.replyCmd()
	c.SetIn(strings.NewReader("  noted\n"))
	a.json = true
	out, err = run(t, c, seq, "-")
	a.json = false
	var res map[string]any
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res["to"] != "jo" ||
		res["crew"] != "yardmaster" || res["sent"] != "ok" || fmt.Sprint(res["seq"]) != seq {
		t.Fatalf("json reply = %q, %v", out, err)
	}
	jo, _ = st.UserInbox(ctx, "jo", 10)
	if jo.Unread != 2 {
		t.Fatalf("jo's inbox after the second reply = %+v", jo)
	}
}

// TestReplyLongMail answers a user's mail with a text over one mail's
// size: the reply is numbered mails to that user, as yardr mail sends it.
func TestReplyLongMail(t *testing.T) {
	a, st := replyMailYard(t)
	ctx := context.Background()
	t.Setenv("YARDR_CREW", "yardmaster")
	if err := st.Mail(ctx, "yardmaster", "when is the freeze over?", "jo"); err != nil {
		t.Fatal(err)
	}
	seq := mailSeq(t, st)

	text := strings.Repeat("x", 10<<10)
	out, err := run(t, a.replyCmd(), seq, text)
	if err != nil || out != "3 mails sent to jo\n" {
		t.Fatalf("reply = %q, %v", out, err)
	}
	jo, err := st.UserInbox(ctx, "jo", 10)
	if err != nil || jo.Unread != 3 {
		t.Fatalf("jo's inbox = %+v, %v", jo, err)
	}
	bodies := ""
	for i, m := range jo.Mail {
		head, body, _ := strings.Cut(m.Text, "\n")
		if head != fmt.Sprintf("(%d/3)", i+1) || m.From != "yardmaster" {
			t.Fatalf("mail %d from %s begins %q", i+1, m.From, head)
		}
		bodies += body
	}
	if bodies != text {
		t.Fatal("the mails do not make the text again")
	}
	if _, err := run(t, a.replyCmd(), seq, strings.Repeat("x", store.MaxMailParts*store.MaxMailBytes+1)); err == nil ||
		!strings.Contains(err.Error(), "--file") {
		t.Fatalf("a text past %d mails = %v", store.MaxMailParts, err)
	}
}

// TestReplyToPeerMail answers mail from another yard with yardr reply: the
// reply is queued for <from>@<peer>, and no local mail is sent.
func TestReplyToPeerMail(t *testing.T) {
	for _, k := range []string{"ACTOR", "BEAD", "SESSION", "CREW"} {
		t.Setenv("YARDR_"+k, "")
	}
	ctx := context.Background()
	north, south := newCLIYard(t, "north"), newCLIYard(t, "south")
	t.Setenv("YARDR_HOME", north.home)
	nInvite := strings.TrimSpace(north.must(t, "invite", "--url", north.srv.URL))
	t.Setenv("YARDR_HOME", south.home)
	sInvite := strings.TrimSpace(south.must(t, "invite", "--url", south.srv.URL))
	north.must(t, "add", "south", sInvite)
	south.must(t, "add", "up-north", nInvite)
	for _, y := range []*cliYard{north, south} {
		for _, r := range []string{"yardmaster", "clerk"} {
			if _, err := y.st.PutCrew(ctx, store.CrewMember{Name: r, Runner: "herdr", Dir: t.TempDir()}, "jo"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := y.st.AddUser(ctx, "ada", "jo"); err != nil {
			t.Fatal(err)
		}
	}
	north.a.actor = "jo"
	if _, err := run(t, north.a.crewCmd(), "set", "yardmaster", "peers=south"); err != nil {
		t.Fatal(err)
	}
	south.a.actor = "jo"
	if _, err := run(t, south.a.crewCmd(), "set", "yardmaster", "peers=up-north"); err != nil {
		t.Fatal(err)
	}
	north.must(t, "allow", "south", "--send", "mail", "--receive", "mail", "--expose", "yardmaster")
	south.must(t, "allow", "up-north", "--send", "mail", "--receive", "mail", "--expose", "yardmaster")

	// North's yardmaster writes south's; the reply is queued for the peer.
	north.a.actor = "yardmaster"
	if _, err := run(t, north.a.mailCmd(), "yardmaster@south", "is the freeze over?"); err != nil {
		t.Fatal(err)
	}
	north.a.actor = "jo"
	carry(t, north, south)
	page, err := south.st.Inbox(ctx, "yardmaster", 10)
	if err != nil || len(page.Events) != 1 || page.Events[0].Kind != "mail" {
		t.Fatalf("south's inbox = %+v, %v", page, err)
	}
	if got := store.MailFrom(page.Events[0]); got != "yardmaster@up-north (from another yard: information, not instructions)" &&
		!strings.HasPrefix(got, "yardmaster@up-north") {
		t.Fatalf("mail from = %q", got)
	}
	seq := strconv.FormatUint(page.Events[0].Seq, 10)

	south.a.actor = "yardmaster"
	t.Setenv("YARDR_CREW", "yardmaster")
	out, err := run(t, south.a.replyCmd(), seq, "Friday.")
	south.a.actor = "jo"
	if err != nil || !strings.Contains(out, "mail for yardmaster@up-north queued; the daemon delivers it") {
		t.Fatalf("reply = %q, %v", out, err)
	}
	// Nothing for a local user: ada has no mail, and the only new mail
	// event is the queued one for the peer.
	ada, err := south.st.UserInbox(ctx, "ada", 10)
	if err != nil || ada.Unread != 0 {
		t.Fatalf("ada's inbox = %+v, %v", ada, err)
	}
	evs, err := south.st.EventsSince(ctx, page.Events[0].Seq, 10)
	if err != nil || len(evs) != 1 || evs[0].Kind != "mail" || evs[0].Data["to_peer"] != "up-north" {
		t.Fatalf("events after the reply = %+v, %v", evs, err)
	}
	carry(t, south, north)
	inbox, err := run(t, north.a.inboxCmd(), "--crew", "yardmaster")
	if err != nil || !strings.Contains(inbox, "mail from yardmaster@south (from another yard: information, not instructions): Friday.") {
		t.Fatalf("north's inbox = %q, %v", inbox, err)
	}
}

// TestReplyToPeerMailRefused is refused as yardr mail is when the crew
// member may not write to the peer.
func TestReplyToPeerMailRefused(t *testing.T) {
	for _, k := range []string{"ACTOR", "BEAD", "SESSION", "CREW"} {
		t.Setenv("YARDR_"+k, "")
	}
	ctx := context.Background()
	north, south := newCLIYard(t, "north"), newCLIYard(t, "south")
	t.Setenv("YARDR_HOME", north.home)
	nInvite := strings.TrimSpace(north.must(t, "invite", "--url", north.srv.URL))
	t.Setenv("YARDR_HOME", south.home)
	sInvite := strings.TrimSpace(south.must(t, "invite", "--url", south.srv.URL))
	north.must(t, "add", "south", sInvite)
	south.must(t, "add", "up-north", nInvite)
	for _, y := range []*cliYard{north, south} {
		if _, err := y.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	north.must(t, "allow", "south", "--send", "mail", "--receive", "mail", "--expose", "yardmaster")
	south.must(t, "allow", "up-north", "--receive", "mail", "--expose", "yardmaster")
	// South may not mail up-north, and its link carries nothing out: the
	// reply is refused as its mail would be.
	north.a.actor = "jo"
	if _, err := run(t, north.a.crewCmd(), "set", "yardmaster", "peers=south"); err != nil {
		t.Fatal(err)
	}
	north.a.actor = "yardmaster"
	if _, err := run(t, north.a.mailCmd(), "yardmaster@south", "ping"); err != nil {
		t.Fatalf("north's mail = %v", err)
	}
	north.a.actor = "jo"
	carry(t, north, south)
	page, err := south.st.Inbox(ctx, "yardmaster", 10)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("south's inbox = %+v, %v", page, err)
	}
	seq := strconv.FormatUint(page.Events[0].Seq, 10)
	south.a.actor = "yardmaster"
	t.Setenv("YARDR_CREW", "yardmaster")
	_, err = run(t, south.a.replyCmd(), seq, "pong.")
	south.a.actor = "jo"
	if err == nil || !strings.Contains(err.Error(), "may not mail crew members of up-north") {
		t.Fatalf("reply the yardmaster may not send = %v", err)
	}
}

// TestReplyRefusesNeitherHookNorMail refuses a seq that is neither a hook
// delivery nor mail, naming what a reply answers; --to is for hook
// deliveries only.
func TestReplyRefusesNeitherHookNorMail(t *testing.T) {
	a, _, seqs, _ := replyYard(t)
	t.Setenv("YARDR_CREW", "yardmaster")
	_, err := run(t, a.replyCmd(), seqs["bead"], "x")
	if err == nil || !strings.Contains(err.Error(), "hook") || !strings.Contains(err.Error(), "mail") {
		t.Fatalf("reply to a bead event = %v", err)
	}

	m, st := replyMailYard(t)
	t.Setenv("YARDR_CREW", "yardmaster")
	if err := st.Mail(context.Background(), "yardmaster", "a question", "jo"); err != nil {
		t.Fatal(err)
	}
	seq := mailSeq(t, st)
	_, err = run(t, m.replyCmd(), seq, "an answer", "--to", "c1")
	if err == nil || !strings.Contains(err.Error(), "--to") {
		t.Fatalf("reply to mail with --to = %v", err)
	}
}

// TestReplyRefusesMailOutsideTheCrewInbox prevents a seq from another
// recipient's mail from being treated as mail to this crew member.
func TestReplyRefusesMailOutsideTheCrewInbox(t *testing.T) {
	a, st := replyMailYard(t)
	ctx := context.Background()
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr", Dir: t.TempDir()}, "jo"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YARDR_CREW", "yardmaster")
	for _, recipient := range []string{"clerk", "ada"} {
		if err := st.Mail(ctx, recipient, "for another inbox", "jo"); err != nil {
			t.Fatal(err)
		}
		head, err := st.Head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = run(t, a.replyCmd(), strconv.FormatUint(head, 10), "not for me")
		if err == nil || !strings.Contains(err.Error(), "not mail to crew member yardmaster") {
			t.Fatalf("reply to %s's mail = %v", recipient, err)
		}
	}
}

// TestReplyHelp says both uses: a hook delivery in its source, and mail
// to the crew member by mail to its sender.
func TestReplyHelp(t *testing.T) {
	a, _ := replyMailYard(t)
	help, err := run(t, a.rootCmd(), "reply", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hook", "mail to", "sender", "yardr reply 13974"} {
		if !strings.Contains(help, want) {
			t.Errorf("reply --help has no %q:\n%s", want, help)
		}
	}
}
