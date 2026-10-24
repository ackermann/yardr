package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// Subscribing to what is already there says so and names what was asked for,
// not the last subscription of the list, which is another one (yardr-kk3a).
func TestSubscribeAlreadyThere(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	if out := mustAs(t, st, "jo", "subscribe", "jo", "yardr", "--where", "type=wagon"); out != "jo subscribed: yardr all type=wagon\n" {
		t.Fatalf("first: %q", out)
	}
	if out := mustAs(t, st, "jo", "subscribe", "jo", "yardr", "--where", "kind=noted"); out != "jo subscribed: yardr all kind=noted\n" {
		t.Fatalf("second: %q", out)
	}
	if out := mustAs(t, st, "jo", "subscribe", "jo", "yardr", "--where", "type=wagon"); out != "jo already subscribed: yardr all type=wagon\n" {
		t.Fatalf("the first again: %q", out)
	}
	u, err := st.GetUser(context.Background(), "jo")
	// The two asked for, beside the operator's failure subscription.
	if err != nil || len(u.Subscriptions) != 3 {
		t.Fatalf("subscriptions = %+v, %v", u, err)
	}
}

// notedBead is a bead whose notes, written by jane, notify whoever
// subscribed to kind=noted.
func notedBead(t *testing.T, st store.API) (note func(string), id string) {
	t.Helper()
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "work"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	return func(text string) {
		t.Helper()
		if err := st.AddNote(ctx, b.ID, text, "jane"); err != nil {
			t.Fatal(err)
		}
	}, b.ID
}

// A user subscribes as a crew member does, and so does the operator for them;
// a crew member and another user are refused in the store's own words, for
// subscribe and unsubscribe alike. yardr user show lists what a user
// subscribes to.
func TestUserSubscribes(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	declareGitHubPack(t, st, "", "", "jo")
	ctx := context.Background()
	mustAs(t, st, "jo", "user", "add", "ada")

	if out := mustAs(t, st, "jo", "subscribe", "jo", "yardr", "--done"); out != "jo subscribed: yardr all state=done\n" {
		t.Fatalf("jo subscribes: %q", out)
	}
	if out := mustAs(t, st, "ada", "subscribe", "ada", "yardr", "--where", "kind=noted", "--where", "depot=web"); out != "ada subscribed: yardr all depot=web kind=noted\n" {
		t.Fatalf("ada subscribes: %q", out)
	}
	// The operator, for another user.
	var subs []store.Subscription
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "subscribe", "ada", "yardr", "--scope", "mentions", "--json")), &subs); err != nil ||
		len(subs) != 2 || subs[1].String() != "yardr mentions" {
		t.Fatalf("subscribe --json = %+v, %v", subs, err)
	}
	// A hook source knows no user: it defaults to all, and refuses a scope
	// that would never match.
	out := mustAs(t, st, "jo", "subscribe", "jo", "github")
	if out != "jo subscribed: github all\n" {
		t.Fatalf("subscribe github default scope: %q", out)
	}
	refusedAs(t, st, "jo", "user jo has no identity or thread participation in github; use --scope all",
		"subscribe", "jo", "github", "--scope", "involved")
	if out := mustAs(t, st, "jo", "subscribe", "jo", "github", "--scope", "all"); strings.Contains(out, "note:") {
		t.Fatalf("subscribe github --scope all:\n%s", out)
	}

	// The store's refusal, as it is.
	const member = "yardmaster is a crew member, and a crew member does not decide what a person is told: " +
		"ada subscribes themselves, or the operator (jo) does it for them"
	for _, args := range [][]string{
		{"subscribe", "ada", "yardr", "--done"},
		{"unsubscribe", "ada", "yardr"},
	} {
		if _, err := as(t, st, "yardmaster", args...); err == nil || err.Error() != member {
			t.Fatalf("yardmaster: yardr %s = %v\nwant %s", strings.Join(args, " "), err, member)
		}
	}
	refusedAs(t, st, "ada", "ada does not decide what jo is told: jo subscribes themselves, or the operator does it for them",
		"subscribe", "jo", "yardr", "--done")
	refusedAs(t, st, "yard-10-new", "yard-10-new does not decide what ada is told", "unsubscribe", "ada", "yardr")
	if u, err := st.GetUser(ctx, "ada"); err != nil || len(u.Subscriptions) != 2 {
		t.Fatalf("a refused change changed ada's subscriptions: %+v, %v", u, err)
	}

	show := mustAs(t, st, "ada", "user", "show", "jo")
	wantText(t, show, "  subscribed: yardr all state=done\n", "  subscribed: github all\n")
	var v userView
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "user", "show", "ada", "--json")), &v); err != nil ||
		len(v.Subscriptions) != 2 || v.Subscriptions[0].String() != "yardr all depot=web kind=noted" {
		t.Fatalf("user show --json = %+v, %v", v, err)
	}

	if out := mustAs(t, st, "jo", "unsubscribe", "jo", "github", "--scope", "all"); out != "jo: 1 subscription(s) removed\n" {
		t.Fatalf("unsubscribe: %q", out)
	}
	if out := mustAs(t, st, "jo", "unsubscribe", "ada", "yardr"); out != "ada: 2 subscription(s) removed\n" {
		t.Fatalf("the operator unsubscribes ada: %q", out)
	}
	if show := mustAs(t, st, "jo", "user", "show", "jo"); strings.Contains(show, "github") {
		t.Fatalf("show jo after the unsubscribe:\n%s", show)
	}
	if show := mustAs(t, st, "jo", "user", "show", "ada"); strings.Contains(show, "subscribed") {
		t.Fatalf("show ada after the unsubscribe:\n%s", show)
	}
}

// A user's inbox is shown in two parts, Mail and Notifications, each with
// its unread count; --mail and --notifications show one, and --ack <seq>
// marks read what is through the seq in the parts shown and nothing of
// the other.
func TestUserInboxParts(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	ctx := context.Background()
	if out := mustAs(t, st, "jo", "inbox"); out != "Mail: none\n\nNotifications: none\n" {
		t.Fatalf("an empty inbox: %q", out)
	}
	mustAs(t, st, "jo", "subscribe", "jo", "yardr", "--where", "kind=noted")
	note, id := notedBead(t, st)
	for _, text := range []string{"one", "two", "three"} {
		note(text)
	}
	for _, text := range []string{"hello", "are you there?"} {
		if err := st.Mail(ctx, "jo", text, "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	counts := func() (mail, told int) {
		t.Helper()
		var box userBox
		if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "inbox", "--json")), &box); err != nil || box.Mailbox == nil || box.Notifications == nil {
			t.Fatalf("inbox --json = %+v, %v", box, err)
		}
		return box.Unread, box.Notifications.Unread
	}

	out := mustAs(t, st, "jo", "inbox")
	row := id + ` noted (all) by jane note=`
	wantText(t, out, "Mail: 2 unread\n", "mail from yardmaster: hello\n", "mail from yardmaster: are you there?\n",
		"\nNotifications: 3 unread\n", row+"1 on \"work\"\n", row+"2 on \"work\"\n", row+"3 on \"work\"\n")
	if strings.Contains(out, "(read)") || strings.Contains(out, "acknowledged") {
		t.Fatalf("inbox:\n%s", out)
	}
	// One part each, under its own name.
	if out := mustAs(t, st, "jo", "inbox", "--mail"); !strings.HasPrefix(out, "Mail: 2 unread\n") || strings.Contains(out, "otifications") || strings.Contains(out, "noted") {
		t.Fatalf("inbox --mail:\n%s", out)
	}
	if out := mustAs(t, st, "jo", "inbox", "--notifications"); !strings.HasPrefix(out, "Notifications: 3 unread\n") || strings.Contains(out, "Mail") || strings.Contains(out, "hello") {
		t.Fatalf("inbox --notifications:\n%s", out)
	}
	var page store.NotificationPage
	js := mustAs(t, st, "jo", "inbox", "--notifications", "--json")
	if err := json.Unmarshal([]byte(js), &struct {
		Notifications *store.NotificationPage `json:"notifications"`
	}{&page}); err != nil || page.Unread != 3 || len(page.Notifications) != 3 || strings.Contains(js, `"mail"`) {
		t.Fatalf("inbox --notifications --json: %v\n%s", err, js)
	}
	var box store.Mailbox
	js = mustAs(t, st, "jo", "inbox", "--mail", "--json")
	if err := json.Unmarshal([]byte(js), &box); err != nil || box.Unread != 2 || len(box.Mail) != 2 || strings.Contains(js, `"notifications"`) {
		t.Fatalf("inbox --mail --json: %v\n%s", err, js)
	}
	if m, n := counts(); m != 2 || n != 3 {
		t.Fatalf("reading acknowledged: %d mail, %d notifications unread", m, n)
	}

	// Acknowledging the notifications shown, the oldest of three, leaves
	// the rest of them and all of the mail.
	out = mustAs(t, st, "jo", "inbox", "--notifications", "-n", "1", "--ack", fmt.Sprint(page.Notifications[0].Seq))
	wantText(t, out, "Notifications: 3 unread\n", row+"1 on", "… 2 more unread\n", "acknowledged 1\n", "2 newer stay unread\n")
	if m, n := counts(); m != 2 || n != 2 || strings.Contains(out, row+"2") {
		t.Fatalf("after --notifications -n 1 --ack: %d mail, %d notifications unread\n%s", m, n, out)
	}
	out = mustAs(t, st, "jo", "inbox", "--notifications", "--ack", fmt.Sprint(page.Notifications[2].Seq))
	wantText(t, out, "Notifications: 2 unread\n", "(read) "+row+"1 on", row+"2 on", row+"3 on", "acknowledged 2\n")
	if m, n := counts(); m != 2 || n != 0 {
		t.Fatalf("after --notifications --ack: %d mail, %d notifications unread", m, n)
	}
	// And the mail, which leaves a notification that came since.
	note("four")
	out = mustAs(t, st, "jo", "inbox", "--mail", "--ack", fmt.Sprint(box.Last))
	wantText(t, out, "Mail: 2 unread\n", "acknowledged 2\n")
	if m, n := counts(); m != 0 || n != 1 {
		t.Fatalf("after --mail --ack: %d mail, %d notifications unread", m, n)
	}
	out = mustAs(t, st, "jo", "inbox")
	wantText(t, out, "Mail: nothing unread; the latest read:\n", "mail from yardmaster (read): hello\n",
		"\nNotifications: 1 unread\n", "(read) "+row+"3 on", row+"4 on")
	// Both parts in one order: the unread over the read, the latest read
	// first, as the mail is listed.
	if strings.Index(out, row+"4 on") > strings.Index(out, "(read) "+row+"3 on") ||
		strings.Index(out, "(read) "+row+"3 on") > strings.Index(out, "(read) "+row+"2 on") {
		t.Fatalf("the notifications' order:\n%s", out)
	}

	// Neither flag is both parts, for --ack too.
	if err := st.Mail(ctx, "jo", "still there?", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	mailBox, err := st.UserInbox(ctx, "jo", 1, "jo")
	if err != nil {
		t.Fatal(err)
	}
	notesBox, err := st.UserNotifications(ctx, "jo", 1, "jo")
	if err != nil {
		t.Fatal(err)
	}
	out = mustAs(t, st, "jo", "inbox", "--ack", fmt.Sprint(max(mailBox.Last, notesBox.Last)))
	if strings.Count(out, "acknowledged 1\n") != 2 {
		t.Fatalf("inbox --ack:\n%s", out)
	}
	if m, n := counts(); m != 0 || n != 0 {
		t.Fatalf("after --ack: %d mail, %d notifications unread", m, n)
	}
	if out := mustAs(t, st, "jo", "inbox", "--mail", "--notifications"); !strings.Contains(out, "Mail: nothing unread") ||
		!strings.Contains(out, "\nNotifications: nothing unread; the latest read:\n") {
		t.Fatalf("inbox --mail --notifications:\n%s", out)
	}

	// A crew member's inbox has no parts.
	refusedAs(t, st, "jo", "--mail and --notifications are the parts of a user's inbox", "inbox", "--crew", "yardmaster", "--mail")
}

// Bare yardr counts the mail as mail, and says the notifications after it
// by their number.
func TestBareYardrSaysNotifications(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	ctx := context.Background()
	mustAs(t, st, "jo", "user", "add", "ada")
	mustAs(t, st, "jo", "subscribe", "jo", "yardr", "--where", "kind=noted")
	note, _ := notedBead(t, st)
	note("one")
	// Notifications alone are not said: nobody is talking, and the line
	// would stand under every command of a person who never acks them.
	if bare := mustAs(t, st, "jo"); strings.Contains(bare, "for jo") || strings.Contains(bare, "notification (") || strings.Contains(bare, "(yardr inbox)") {
		t.Fatalf("bare yardr, one notification and no mail:\n%s", bare)
	}
	note("two")
	for _, from := range []string{"yardmaster", "ada", "yardmaster"} {
		if err := st.Mail(ctx, "jo", "hi", from); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := st.Head(ctx)
	if bare := mustAs(t, st, "jo"); !strings.HasSuffix(bare, "\nMail for jo: 3 unread from yardmaster, ada, and 2 notifications (yardr inbox).\n") {
		t.Fatalf("bare yardr:\n%s", bare)
	}
	if after, _ := st.Head(ctx); after != before {
		t.Fatalf("reading wrote to the store: head %d, was %d", after, before)
	}
	if show := mustAs(t, st, "jo", "user", "show", "jo"); !strings.Contains(show, "  inbox: 3 unread from yardmaster, ada\n  2 notifications unread\n") {
		t.Fatalf("user show:\n%s", show)
	}
	// Another user's, and a crew member's, say nothing of jo's.
	for _, who := range []string{"ada", "yardmaster"} {
		if out := mustAs(t, st, who); strings.Contains(out, "for jo") || strings.Contains(out, "(yardr inbox)") {
			t.Fatalf("bare yardr as %s:\n%s", who, out)
		}
	}
	notesBox, err := st.UserNotifications(ctx, "jo", 1, "jo")
	if err != nil {
		t.Fatal(err)
	}
	mustAs(t, st, "jo", "inbox", "--notifications", "--ack", fmt.Sprint(notesBox.Last))
	if bare := mustAs(t, st, "jo"); !strings.HasSuffix(bare, "\nMail for jo: 3 unread from yardmaster, ada (yardr inbox).\n") {
		t.Fatalf("bare yardr after the notifications were read:\n%s", bare)
	}
	mailBox, err := st.UserInbox(ctx, "jo", 1, "jo")
	if err != nil {
		t.Fatal(err)
	}
	mustAs(t, st, "jo", "inbox", "--mail", "--ack", fmt.Sprint(mailBox.Last))
	if bare := mustAs(t, st, "jo"); strings.Contains(bare, "for jo") || strings.Contains(bare, "(yardr inbox)") {
		t.Fatalf("bare yardr with nothing unread:\n%s", bare)
	}
}

// Where the forms end, yardr setup offers a user to be told when beads
// land: a yes is the subscribe it shows, for the user and the setup's depot.
// It is not offered again to who has it, nor to who is no user.
func TestSetupOffersToBeToldOfLandedBeads(t *testing.T) {
	f := &former{answers: map[string]any{"title": "Say hello", "goal": "HELLO.md says hello.", "landed": true, "link": false},
		goes: []string{goHere, goHere, goHere, goHere, goHere, goHere, goHere}}
	home, st, _ := setupYard(t, f)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "me", "Jo"); err != nil {
		t.Fatal(err)
	}
	// The setup runs as me, who is its operator here: the import
	// subscribes the yard it makes. Linking is declined.
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "Jo"); err != nil {
		t.Fatal(err)
	}
	out := setup(t, st, "--actor", "me")
	wantText(t, out, "The forms end here.", "$ yardr --home "+home+" subscribe me yardr --done --where depot=web-app",
		"me subscribed: yardr all depot=web-app state=done")
	if keys := keysOf(f); len(keys) < 2 || keys[len(keys)-2] != "landed" || keys[len(keys)-1] != "link" {
		t.Fatalf("asked %q\n%s", keys, out)
	}
	if u, err := st.GetUser(ctx, "me"); err != nil || len(u.Subscriptions) != 1 || u.Subscriptions[0].String() != "yardr all depot=web-app state=done" {
		t.Fatalf("me = %+v, %v", u, err)
	}
	f.asked = nil
	if out := setup(t, st, "--actor", "me"); slices.Contains(keysOf(f), "landed") || !strings.Contains(out, "The forms end here.") {
		t.Fatalf("asked %q of who is told already\n%s", keysOf(f), out)
	}

	// A no subscribes nobody, and who is no user is not asked: a
	// subscription is a user's own.
	cfg := &config.Config{Home: home}
	f.answers["landed"] = false
	for who, asked := range map[string]bool{"Jo": true, "yardmaster": false, "pat": false} {
		f.asked = nil
		g := &guide{a: &app{st: st, actor: who}, cfg: cfg, st: st}
		if err := g.offerLanded(ctx); err != nil || slices.Contains(keysOf(f), "landed") != asked {
			t.Fatalf("%s: asked %q, %v", who, keysOf(f), err)
		}
	}
	if u, err := st.GetUser(ctx, "Jo"); err != nil || len(u.Subscriptions) != 1 ||
		u.Subscriptions[0].String() != "yardr all kind=install_failed kind=rolled_back" {
		t.Fatalf("a no subscribed Jo: %+v, %v", u, err)
	}
}
