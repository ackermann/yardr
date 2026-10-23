package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// user adds the user name, who then subscribes themselves to subs.
func user(t *testing.T, s store.API, name string, subs ...store.Subscription) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.AddUser(ctx, name, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, sub := range subs {
		if err := s.AddSubscription(ctx, name, sub, name); err != nil {
			t.Fatal(err)
		}
	}
}

func notifications(t *testing.T, s store.API, name string, limit int) *store.NotificationPage {
	t.Helper()
	page, err := s.UserNotifications(context.Background(), name, limit)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

// texts are the notifications' texts, each marked "*" while unread.
func texts(page *store.NotificationPage) []string {
	out := []string{}
	for _, n := range page.Notifications {
		mark := ""
		if !n.Read {
			mark = "*"
		}
		out = append(out, mark+n.Text)
	}
	return out
}

// A user's subscription makes a matching bead event a notification in the
// user's inbox: yardr's own line about the event with the bead's title,
// and no mail. A crew member subscribed to the same hears of it as before.
func TestUserSubscriptionNotifies(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t, "yardmaster")
	user(t, s, "ada", yardrSub(store.ScopeAll, "state=done"), yardrSub(store.ScopeMentions))
	user(t, s, "bob")
	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "state=done"))
	if u, err := s.GetUser(ctx, "ada"); err != nil || len(u.Subscriptions) != 2 || u.Subscriptions[0].String() != "yardr all state=done" {
		t.Fatalf("ada = %+v, %v", u, err)
	}
	head, _ := s.Head(ctx)

	// Not done, and nobody mentioned: nothing.
	if err := s.AddNote(ctx, b.ID, "started", "jane"); err != nil {
		t.Fatal(err)
	}
	if page := notifications(t, s, "ada", 0); len(page.Notifications) != 0 || page.Unread != 0 {
		t.Fatalf("a note notified ada: %+v", page)
	}
	if err := s.AddNote(ctx, b.ID, "ask @ada\nabout it", "jane"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseBead(ctx, b.ID, "merged abc1234", store.Expect{}, "assembly"); err != nil {
		t.Fatal(err)
	}

	page := notifications(t, s, "ada", 0)
	want := []string{
		"*" + b.ID + ` noted (mentioned) by jane note=2 on "work"`,
		"*" + b.ID + ` closed (done) by assembly reason=merged abc1234 on "work"`,
	}
	if got := texts(page); fmt.Sprint(got) != fmt.Sprint(want) || page.Unread != 2 {
		t.Fatalf("ada's notifications = %q (%d unread)\nwant %q", got, page.Unread, want)
	}
	closed := page.Notifications[1]
	if closed.Bead != b.ID || closed.Title != "work" || closed.Commit != "abc1234" || closed.Kind != "closed" ||
		closed.Source != store.SourceYardr || closed.Reason != store.ReasonDone || closed.At.IsZero() || page.Last != closed.Seq {
		t.Fatalf("closed notification = %+v (last %d)", closed, page.Last)
	}
	if noted := page.Notifications[0]; noted.Commit != "" {
		t.Fatalf("a note has a commit: %+v", noted)
	}
	// It is no mail, to nobody: not in ada's mail, not in the log as mail.
	if box, err := s.UserInbox(ctx, "ada", 0); err != nil || len(box.Mail) != 0 || box.Unread != 0 {
		t.Fatalf("ada's mail = %+v, %v", box, err)
	}
	evs, err := s.EventsSince(ctx, head, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Kind == "mail" {
			t.Fatalf("a notification was mailed: %+v", e)
		}
		if e.Seq == closed.Seq && (store.Notified(e, "ada") != store.ReasonDone || store.Reason(e, "ada") != "" ||
			store.Reason(e, "yardmaster") != store.ReasonDone || store.Notified(e, "yardmaster") != "") {
			t.Fatalf("closed event names its receivers as %v", e.Data)
		}
	}
	// The crew member's inbox holds its own delivery and nothing of ada's.
	if in, err := s.Inbox(ctx, "yardmaster", 0); err != nil || in.Total != 1 || in.Events[0].Seq != closed.Seq {
		t.Fatalf("yardmaster's inbox = %+v, %v", in, err)
	}
	// A user with no subscription is told nothing.
	if page := notifications(t, s, "bob", 0); len(page.Notifications) != 0 {
		t.Fatalf("bob was notified: %+v", page)
	}
	if _, err := s.UserNotifications(ctx, "yardmaster", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a crew member's notifications = %v", err)
	}
}

// A notification is made from what its person is told of the event: an
// operator's row names the event's data whole, and any other person's what
// the kind lists (store.Event.WithoutHeld), so a person who subscribed to the
// events of a kept workspace is not told the worktree's path, which is an
// operator's. A yard with no operator tells every person the same, and the
// row of a person is made when the event is logged: a change of role moves the
// next one with it.
func TestNotificationNamesWhatThePersonIsToldOfTheEvent(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t)
	user(t, s, "op", yardrSub(store.ScopeAll, "kind=workspace_kept"))
	user(t, s, "ada", yardrSub(store.ScopeAll, "kind=workspace_kept"))
	keep := func() {
		t.Helper()
		if err := s.LogDispatch(ctx, b.ID, "workspace_kept", "yard", map[string]any{
			"session": "s1", "worktree": "/home/jo/.yardr/worktrees/yd/work", "reason": "git status: /home/jo/.yardr/worktrees/yd/work has changes"}); err != nil {
			t.Fatal(err)
		}
	}
	role := func(user, role, actor string) {
		t.Helper()
		if _, err := s.SetUser(ctx, user, map[string]string{store.UserRole: role}, actor); err != nil {
			t.Fatal(err)
		}
	}
	// What each is told, and what neither is: the first line is of the yard
	// with no operator.
	const told = ` workspace_kept (all) by yard session=s1 on "work"`
	whole := ` workspace_kept (all) by yard reason=git status: /home/jo/.yardr/worktrees/yd/work has changes session=s1 worktree=/home/jo/.yardr/worktrees/yd/work on "work"`
	keep()
	if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	keep()
	// Another operator is told whole as the first is, by the role alone.
	role("ada", store.RoleOperator, "op")
	keep()
	// And a user made a member is told no more, the next time. The setting
	// names ada first: it keeps its user an operator.
	if err := s.SetSetting(ctx, store.OperatorSetting, "ada", "op"); err != nil {
		t.Fatal(err)
	}
	role("op", store.RoleMember, "ada")
	keep()
	for _, c := range []struct {
		who  string
		want []string
	}{
		{"op", []string{"*" + b.ID + told, "*" + b.ID + whole, "*" + b.ID + whole, "*" + b.ID + told}},
		{"ada", []string{"*" + b.ID + told, "*" + b.ID + told, "*" + b.ID + whole, "*" + b.ID + whole}},
	} {
		if got := texts(notifications(t, s, c.who, 0)); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s's notifications = %q\nwant %q", c.who, got, c.want)
		}
	}
	// The event itself is whole: the socket, the terminal and the agents read it so.
	evs, err := s.Events(ctx, b.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if e := evs[len(evs)-1]; e.Kind != "workspace_kept" || e.Data["worktree"] != "/home/jo/.yardr/worktrees/yd/work" {
		t.Errorf("the event is %+v, want the worktree kept in it", e)
	}
}

// A person is not told of what they did themselves, as a crew member is not.
func TestUserNotToldOfOwnActions(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t)
	user(t, s, "ada", yardrSub(store.ScopeAll))
	if err := s.AddNote(ctx, b.ID, "mine", "ada"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote(ctx, b.ID, "theirs", "jane"); err != nil {
		t.Fatal(err)
	}
	if got := texts(notifications(t, s, "ada", 0)); len(got) != 1 || !strings.Contains(got[0], "by jane") {
		t.Fatalf("ada's notifications = %q", got)
	}
}

// A hook delivery reaches a user through a subscription of scope all, in
// the words a crew member's inbox has for it. A user has no identity in a
// source, so the narrower scopes reach nobody.
func TestUserHookNotification(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	user(t, s, "ada", store.Subscription{Source: "linear", Scope: store.ScopeAll, Where: map[string]string{"type": "comment"}})
	user(t, s, "bob")
	for _, scope := range []string{store.ScopeMentions, store.ScopeInvolved} {
		if err := s.AddSubscription(ctx, "bob", store.Subscription{Source: "linear", Scope: scope}, "bob"); err == nil ||
			!strings.Contains(err.Error(), "has no identity or thread participation in linear") {
			t.Fatalf("bob's linear %s subscription = %v", scope, err)
		}
	}
	record(t, own, hooks.Envelope{Source: "linear", Type: "issue", Action: "create", Text: "not a comment"})
	long := strings.Repeat("word ", 100)
	seq := record(t, own, hooks.Envelope{Source: "linear", Type: "comment", Action: "create",
		Actor: hooks.Party{ID: "u1", Name: "Ann"}, Thread: hooks.Thread{ID: "t1", Title: "Fix it"},
		Mentions: []string{"bob"}, Text: "can you\nlook? " + long, URL: "https://linear.example/c/1"})
	page := notifications(t, s, "ada", 0)
	if len(page.Notifications) != 1 {
		t.Fatalf("ada's notifications = %+v", page)
	}
	n := page.Notifications[0]
	if !strings.HasPrefix(n.Text, `hook (all) linear comment create by Ann on "Fix it": can you look? word word`) ||
		!strings.HasSuffix(n.Text, "…  https://linear.example/c/1") || strings.Contains(n.Text, "\n") ||
		n.Seq != seq || n.Source != "linear" || n.Kind != "hook" || n.Bead != "" || n.URL != "https://linear.example/c/1" {
		t.Fatalf("notification = %+v", n)
	}
	if page := notifications(t, s, "bob", 0); len(page.Notifications) != 0 {
		t.Fatalf("bob, with no identity in linear, was notified: %+v", page)
	}
	evs, err := s.EventsSince(ctx, seq-1, 1)
	if err != nil || len(evs) != 1 || store.Notified(evs[0], "ada") != store.ReasonAll {
		t.Fatalf("hook event = %+v, %v", evs, err)
	}
}

func TestHookNotificationStaysOnOneSafeLine(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	user(t, s, "ada", store.Subscription{Source: "github", Scope: store.ScopeAll})
	record(t, own, hooks.Envelope{Source: "github", Type: "issue", Action: "opened",
		URL: "https://example.test/issue/1\n\x1b[2J"})
	page := notifications(t, s, "ada", 0)
	if len(page.Notifications) != 1 || strings.ContainsAny(page.Notifications[0].Text, "\n\r\x1b") {
		t.Fatalf("notification text is not a safe single line: %+v", page.Notifications)
	}
}

// What a person is told is theirs to decide, or the operator's for them:
// a crew member may neither subscribe a user nor remove what a user
// subscribes to, and is told why.
func TestCrewMayNotSubscribeAUser(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t, "yardmaster")
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	user(t, s, "ada")
	user(t, s, "bob")
	done := yardrSub(store.ScopeAll, "state=done")
	const refusal = "a crew member does not decide what a person is told"
	if err := s.AddSubscription(ctx, "ada", done, "yardmaster"); err == nil || !strings.Contains(err.Error(), refusal) ||
		!strings.Contains(err.Error(), "yardmaster is a crew member") || !strings.Contains(err.Error(), "the operator (jo)") {
		t.Fatalf("a crew member subscribing a user = %v", err)
	}
	// Nor another user, nor a session that is no crew member.
	for _, actor := range []string{"bob", "yard-1-new", ""} {
		if err := s.AddSubscription(ctx, "ada", done, actor); err == nil || !strings.Contains(err.Error(), "does not decide what ada is told") {
			t.Fatalf("%q subscribing ada = %v", actor, err)
		}
	}
	if u, _ := s.GetUser(ctx, "ada"); len(u.Subscriptions) != 0 {
		t.Fatalf("a refused subscription was kept: %v", u.Subscriptions)
	}

	// The user themselves, and the operator for them.
	if err := s.AddSubscription(ctx, "ada", done, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSubscription(ctx, "ada", yardrSub(store.ScopeAll, "bead="+b.ID), "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSubscription(ctx, "ada", done, "jo"); err != nil { // the same again: kept once
		t.Fatal(err)
	}
	if u, _ := s.GetUser(ctx, "ada"); len(u.Subscriptions) != 2 {
		t.Fatalf("ada's subscriptions = %v", u.Subscriptions)
	}
	// What a crew member's subscription is refused, a user's is too.
	if err := s.AddSubscription(ctx, "ada", yardrSub(store.ScopeAll, "nope=x"), "ada"); err == nil {
		t.Fatal("a filter on an unknown key accepted for a user")
	}

	if _, err := s.RemoveSubscriptions(ctx, "ada", store.SourceYardr, "", nil, "yardmaster"); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("a crew member unsubscribing a user = %v", err)
	}
	if _, err := s.Unwatch(ctx, "ada", b.ID, "yardmaster"); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("a crew member ending a user's watch = %v", err)
	}
	if n, err := s.Unwatch(ctx, "ada", b.ID, "ada"); err != nil || n != 1 {
		t.Fatalf("ada's unwatch = %d, %v", n, err)
	}
	if n, err := s.RemoveSubscriptions(ctx, "ada", store.SourceYardr, "", nil, "jo"); err != nil || n != 1 {
		t.Fatalf("the operator's unsubscribe = %d, %v", n, err)
	}
	if _, err := s.RemoveSubscriptions(ctx, "ada", store.SourceYardr, "", nil, "ada"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unsubscribing from nothing = %v", err)
	}
	// A crew member still subscribes itself; another crew member's
	// subscriptions are that member's or the operator's to change.
	if err := s.AddSubscription(ctx, "yardmaster", done, "yardmaster"); err != nil {
		t.Fatal(err)
	}
}

// A user's watch on a bead ends when the bead closes, after it told of the
// close, as a crew member's does.
func TestUserWatchEndsOnClose(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t)
	user(t, s, "ada", yardrSub(store.ScopeAll, "bead="+b.ID))
	if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	if got := texts(notifications(t, s, "ada", 0)); len(got) != 1 || !strings.Contains(got[0], "closed (all) by jane") {
		t.Fatalf("ada's notifications = %q", got)
	}
	if u, _ := s.GetUser(ctx, "ada"); len(u.Subscriptions) != 0 {
		t.Fatalf("after the close ada still watches: %v", u.Subscriptions)
	}
	evs, _ := s.Events(ctx, b.ID, 1)
	if ended := fmt.Sprint(evs[0].Data["watches_ended"]); ended != "[ada]" {
		t.Fatalf("watches ended = %s", ended)
	}
}

// A user's inbox has two parts, mail and notifications. Each counts its
// own unread and is acknowledged by itself: reading the one leaves the
// other as it was.
func TestUserInboxPartsAckApart(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t, "yardmaster")
	user(t, s, "ada", yardrSub(store.ScopeAll, "kind=noted"))
	note := func(text string) {
		t.Helper()
		if err := s.AddNote(ctx, b.ID, text, "jane"); err != nil {
			t.Fatal(err)
		}
	}
	mail := func(text string) {
		t.Helper()
		if err := s.Mail(ctx, "ada", text, "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	// Interleaved, so that one cursor could not tell them apart.
	note("n1")
	mail("m1")
	note("n2")
	mail("m2")
	note("n3")
	counts := func() string {
		t.Helper()
		box, err := s.UserInbox(ctx, "ada", 0)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("mail %d notifications %d", box.Unread, notifications(t, s, "ada", 0).Unread)
	}
	if got := counts(); got != "mail 2 notifications 3" {
		t.Fatalf("unread = %s", got)
	}
	head, _ := s.Head(ctx)

	// The notifications through the second, which lies after the first mail.
	page := notifications(t, s, "ada", 0)
	if err := s.AckUserNotifications(ctx, "ada", page.Notifications[1].Seq); err != nil {
		t.Fatal(err)
	}
	if got := counts(); got != "mail 2 notifications 1" {
		t.Fatalf("after acking two notifications: %s", got)
	}
	page = notifications(t, s, "ada", 0)
	if got := texts(page); len(got) != 3 || strings.HasPrefix(got[0], "*") || strings.HasPrefix(got[1], "*") ||
		!strings.HasPrefix(got[2], "*") || page.Last != page.Notifications[2].Seq {
		t.Fatalf("notifications = %q", got)
	}
	evs := kindsAfter(t, s, head)
	if len(evs) != 1 || evs[0].Kind != "notifications_read" || evs[0].Actor != "ada" || evs[0].Data["reader"] != "ada" {
		t.Fatalf("events of the ack = %+v", evs)
	}

	// All the mail, which lies after every notification but the last.
	box, _ := s.UserInbox(ctx, "ada", 0)
	if err := s.AckUserInbox(ctx, "ada", box.Last); err != nil {
		t.Fatal(err)
	}
	if got := counts(); got != "mail 0 notifications 1" {
		t.Fatalf("after acking the mail: %s", got)
	}
	// Past everything: the mail's cursor is not the notifications'.
	end, _ := s.Head(ctx)
	if err := s.AckUserNotifications(ctx, "ada", end); err != nil {
		t.Fatal(err)
	}
	mail("m3")
	note("n4")
	if got := counts(); got != "mail 1 notifications 1" {
		t.Fatalf("new after both acks: %s", got)
	}
	// An ack never moves back, and one that passes no notification is no
	// event.
	head, _ = s.Head(ctx)
	if err := s.AckUserNotifications(ctx, "ada", 1); err != nil {
		t.Fatal(err)
	}
	if got := counts(); got != "mail 1 notifications 1" || len(kindsAfter(t, s, head)) != 0 {
		t.Fatalf("an older ack: %s, events %+v", got, kindsAfter(t, s, head))
	}
	if err := s.AckUserNotifications(ctx, "nobody", 9); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ack of no user = %v", err)
	}

	// The limit shows the unread from the oldest on, then the newest read
	// before them, all oldest first.
	note("n5")
	short := func(limit int) string {
		var out []string
		for _, n := range notifications(t, s, "ada", limit).Notifications {
			mark := ""
			if !n.Read {
				mark = "*"
			}
			out = append(out, fmt.Sprintf("%s%d", mark, n.Seq-page.Notifications[0].Seq))
		}
		return strings.Join(out, " ")
	}
	all := strings.Fields(short(0))
	if len(all) != 5 || !strings.HasPrefix(all[3], "*") || strings.HasPrefix(all[2], "*") {
		t.Fatalf("all = %q", all)
	}
	if got, want := short(1), all[3]; got != want {
		t.Fatalf("limit 1 = %q; want the oldest unread %q", got, want)
	}
	if got, want := short(3), strings.Join(all[2:], " "); got != want {
		t.Fatalf("limit 3 = %q; want %q", got, want)
	}
	if p := notifications(t, s, "ada", 1); p.Unread != 2 || p.Last != notifications(t, s, "ada", 0).Notifications[4].Seq {
		t.Fatalf("limit 1 counts %d unread, last %d", p.Unread, p.Last)
	}
}

// A user's inbox keeps the newest MaxNotifications: nobody acknowledges a
// stream, so the oldest are dropped as more arrive. Another user's are
// not counted against it.
func TestNotificationBound(t *testing.T) {
	t.Parallel()
	s, own := storetest.NewOwned(t)
	user(t, s, "ada", store.Subscription{Source: "linear", Scope: store.ScopeAll})
	user(t, s, "bob", store.Subscription{Source: "linear", Scope: store.ScopeAll, Where: map[string]string{"type": "issue"}})
	record(t, own, hooks.Envelope{Source: "linear", Type: "issue", Text: "for both"})
	// One short of the bound, then over it as deliveries arrive.
	const over = 3
	seqs, err := own.BulkNotify("ada", store.MaxNotifications-2)
	if err != nil {
		t.Fatal(err)
	}
	for i := range over + 1 {
		seqs = append(seqs, record(t, own, hooks.Envelope{Source: "linear", Type: "comment", Text: fmt.Sprint("c", i)}))
	}
	page := notifications(t, s, "ada", 0)
	if len(page.Notifications) != store.MaxNotifications || page.Unread != store.MaxNotifications {
		t.Fatalf("ada keeps %d notifications, %d unread; want %d", len(page.Notifications), page.Unread, store.MaxNotifications)
	}
	if first, last := page.Notifications[0], page.Notifications[len(page.Notifications)-1]; first.Seq != seqs[over-1] || last.Seq != seqs[len(seqs)-1] {
		t.Fatalf("ada keeps %d..%d; want the newest, %d..%d", first.Seq, last.Seq, seqs[over-1], seqs[len(seqs)-1])
	}
	if page := notifications(t, s, "bob", 0); len(page.Notifications) != 1 {
		t.Fatalf("bob's one notification went with ada's: %+v", page)
	}
}

// A removed user's notifications go with them: a later user of the name
// starts with none, and with none of the subscriptions.
func TestUserRemovedWithNotifications(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t)
	user(t, s, "ada", yardrSub(store.ScopeAll))
	if err := s.AddNote(ctx, b.ID, "one", "jane"); err != nil {
		t.Fatal(err)
	}
	if page := notifications(t, s, "ada", 0); page.Unread != 1 {
		t.Fatalf("ada's notifications = %+v", page)
	}
	if err := s.DeleteUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	user(t, s, "ada")
	if err := s.AddNote(ctx, b.ID, "two", "jane"); err != nil {
		t.Fatal(err)
	}
	if page := notifications(t, s, "ada", 0); len(page.Notifications) != 0 {
		t.Fatalf("the new ada has the old one's notifications: %+v", page)
	}
}
