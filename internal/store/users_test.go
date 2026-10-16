package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func party(t *testing.T, s store.API, name string) {
	t.Helper()
	if _, err := s.PutCrew(context.Background(), store.CrewMember{Name: name, Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
}

func userNames(t *testing.T, s store.API) string {
	t.Helper()
	us, err := s.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, u := range us {
		names = append(names, u.Name)
	}
	return strings.Join(names, ",")
}

// A user is a record: added, listed, read and removed, with the events
// that say so.
func TestUserRecord(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	u, err := s.AddUser(ctx, "ada", "jo")
	if err != nil || u.Name != "ada" || u.CreatedBy != "jo" || u.CreatedAt.IsZero() {
		t.Fatalf("add = %+v, %v", u, err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("adding a user twice = %v", err)
	}
	for _, bad := range []string{"", "a@b", "two words", "-x"} {
		if _, err := s.AddUser(ctx, bad, "jo"); err == nil {
			t.Errorf("user %q accepted", bad)
		}
	}
	if got, err := s.GetUser(ctx, "ada"); err != nil || got.Name != "ada" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.GetUser(ctx, "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get of no user = %v", err)
	}
	if _, err := s.AddUser(ctx, "Grace.H", "jo"); err != nil || userNames(t, s) != "Grace.H,ada" {
		t.Fatalf("users = %q, %v", userNames(t, s), err)
	}
	if err := s.DeleteUser(ctx, "ada", "jo"); err != nil || userNames(t, s) != "Grace.H" {
		t.Fatalf("after rm: %q, %v", userNames(t, s), err)
	}
	if err := s.DeleteUser(ctx, "ada", "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removing no user = %v", err)
	}
	evs, _ := s.Events(ctx, "", 10)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind+":"+fmt.Sprint(e.Data["user"]))
	}
	if got := strings.Join(kinds, " "); got != "user_added:ada user_added:Grace.H user_removed:ada" {
		t.Fatalf("events = %s", got)
	}
}

// The operator is a user: naming one adds the user, and that user is not
// removed while the setting names them.
func TestOperatorIsAUser(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, "operator", "two words", "jo"); err == nil {
		t.Fatal("operator with an invalid user name was accepted")
	}
	if all, _ := s.Settings(ctx); all["operator"] != "" || userNames(t, s) != "" {
		t.Fatalf("invalid operator was stored: %v, users %q", all, userNames(t, s))
	}
	if err := s.SetSetting(ctx, "operator", "jo", "jo"); err != nil || userNames(t, s) != "jo" {
		t.Fatalf("users = %q, %v", userNames(t, s), err)
	}
	// Set again, nothing is added twice.
	if err := s.SetSetting(ctx, "operator", "jo", "jo"); err != nil || userNames(t, s) != "jo" {
		t.Fatalf("set twice: %q, %v", userNames(t, s), err)
	}
	if err := s.DeleteUser(ctx, "jo", "jo"); err == nil || !strings.Contains(err.Error(), "is the yard's operator and is not removed") {
		t.Fatalf("removing the operator's user = %v", err)
	}
	// Another operator: their user is added, and the earlier one's can go.
	if err := s.SetSetting(ctx, "operator", "sam", "jo"); err != nil || userNames(t, s) != "jo,sam" {
		t.Fatalf("users = %q, %v", userNames(t, s), err)
	}
	if err := s.DeleteUser(ctx, "jo", "sam"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, "sam", "sam"); err == nil {
		t.Fatal("the new operator's user was removed")
	}
}

// Crew members and users share one space of names, refused both ways, and the
// operator cannot be given a crew member's name.
func TestOneNameOneParty(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "yardmaster")
	if _, err := s.AddUser(ctx, "yardmaster", "jo"); err == nil || !strings.Contains(err.Error(), "that is a crew member's name") {
		t.Fatalf("user with a crew member's name = %v", err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "ada", Runner: "herdr"}, "jo"); err == nil || !strings.Contains(err.Error(), "that is a user's name") {
		t.Fatalf("crew member with a user's name = %v", err)
	}
	if _, err := s.GetCrew(ctx, "ada"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused crew member exists: %v", err)
	}
	if err := s.SetSetting(ctx, "operator", "yardmaster", "jo"); err == nil || !strings.Contains(err.Error(), "that is a crew member's name") {
		t.Fatalf("operator with a crew member's name = %v", err)
	}
	if all, _ := s.Settings(ctx); all["operator"] != "" || userNames(t, s) != "ada" {
		t.Fatalf("the refused setting was stored: %v, users %q", all, userNames(t, s))
	}
	// A crew member that is there is still redefined.
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", MaxAge: "1h"}, "jo"); err != nil {
		t.Fatal(err)
	}
}

func letters(ls []store.Letter) string {
	var out []string
	for _, l := range ls {
		read := "unread"
		if l.Read {
			read = "read"
		}
		out = append(out, fmt.Sprintf("%s>%s %s %s", l.From, l.To, l.Text, read))
	}
	return strings.Join(out, " | ")
}

// A conversation is read from its newest end in pages: limit answers the
// newest of what after and before leave, oldest first, and says whether it
// left older mail out. Without limit and before it answers as it always did.
func TestConversationPages(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "yardmaster")
	party(t, s, "clerk")
	if err := s.SetSetting(ctx, "operator", "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	// Seven letters, both directions, with mail of other conversations of
	// the two in between.
	for i := 1; i <= 7; i++ {
		from, to := "jo", "yardmaster"
		if i%2 == 0 {
			from, to = to, from
		}
		for _, m := range [][3]string{{to, fmt.Sprintf("m%d", i), from}, {"jo", "not this", "clerk"}, {"yardmaster", "nor this", "clerk"}} {
			if err := s.Mail(ctx, m[0], m[1], m[2]); err != nil {
				t.Fatal(err)
			}
		}
	}
	texts := func(ls []store.Letter) string {
		var out []string
		for _, l := range ls {
			out = append(out, l.Text)
		}
		return strings.Join(out, " ")
	}
	all, more, err := s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || more || texts(all) != "m1 m2 m3 m4 m5 m6 m7" {
		t.Fatalf("without limit and before = %s, more %v, %v", texts(all), more, err)
	}
	seq := func(i int) uint64 { return all[i-1].Seq }
	for _, c := range []struct {
		says   string
		after  uint64
		limit  int
		before uint64
		want   string
		more   bool
	}{
		{"limit alone: the newest", 0, 3, 0, "m5 m6 m7", true},
		{"the page before", 0, 3, seq(5), "m2 m3 m4", true},
		{"the oldest page", 0, 3, seq(2), "m1", false},
		{"nothing is older", 0, 3, seq(1), "", false},
		{"a limit of exactly all", 0, 7, 0, "m1 m2 m3 m4 m5 m6 m7", false},
		{"a limit of more than all", 0, 9, 0, "m1 m2 m3 m4 m5 m6 m7", false},
		{"a limit below none is none", 0, -1, 0, "m1 m2 m3 m4 m5 m6 m7", false},
		{"before alone", 0, 0, seq(4), "m1 m2 m3", false},
		{"before a seq of other mail", 0, 0, seq(4) - 1, "m1 m2 m3", false},
		{"after and before", seq(2), 0, seq(6), "m3 m4 m5", false},
		{"after with a limit that leaves a gap", seq(2), 2, 0, "m6 m7", true},
		{"after with a limit that leaves none", seq(5), 2, 0, "m6 m7", false},
	} {
		got, more, err := s.Conversation(ctx, "jo", "yardmaster", c.after, c.limit, c.before)
		if err != nil || got == nil || texts(got) != c.want || more != c.more {
			t.Errorf("%s (after %d, limit %d, before %d) = %q, more %v, %v; want %q, more %v", c.says, c.after, c.limit, c.before, texts(got), more, err, c.want, c.more)
		}
	}
	// A page says who read what as the whole does, and is the same from
	// the other side.
	if err := s.AckInbox(ctx, "yardmaster", seq(5)); err != nil {
		t.Fatal(err)
	}
	page, more, _ := s.Conversation(ctx, "yardmaster", "jo", 0, 3, 0)
	if got := letters(page); !more || got != "jo>yardmaster m5 read | yardmaster>jo m6 unread | jo>yardmaster m7 unread" {
		t.Errorf("the newest page from the other side = %s, more %v", got, more)
	}
}

// Mail to a user lands in that user's inbox alone, unread until they
// acknowledge it, and never in a crew member's.
func TestUserInbox(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "yardmaster")
	party(t, s, "clerk")
	for _, u := range []string{"jo", "ada"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range [][3]string{{"jo", "one", "yardmaster"}, {"ada", "for ada", "yardmaster"}, {"jo", "two", "clerk"}, {"jo", "three", "yardmaster"}} {
		if err := s.Mail(ctx, m[0], m[1], m[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Mail(ctx, "jo", strings.Repeat("x", store.MaxMailBytes+1), "yardmaster"); err == nil {
		t.Fatal("mail over the limit to a user accepted")
	}
	if err := s.Mail(ctx, "nobody", "hi", "yardmaster"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("mail to nobody = %v", err)
	}
	if _, err := s.UserInbox(ctx, "yardmaster", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a crew member read as a user = %v", err)
	}

	box, err := s.UserInbox(ctx, "jo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := letters(box.Mail); got != "yardmaster>jo one unread | clerk>jo two unread | yardmaster>jo three unread" ||
		box.Unread != 3 || strings.Join(box.From, ",") != "yardmaster,clerk" || box.Last != box.Mail[2].Seq {
		t.Fatalf("jo's inbox = %s (%+v)", got, box)
	}
	if ada, _ := s.UserInbox(ctx, "ada", 0); letters(ada.Mail) != "yardmaster>ada for ada unread" {
		t.Fatalf("ada's inbox = %s", letters(ada.Mail))
	}
	for _, r := range []string{"yardmaster", "clerk"} {
		if page, _ := s.Inbox(ctx, r, 0); page.Total != 0 {
			t.Fatalf("%s's inbox has mail to a user: %+v", r, page.Events)
		}
	}

	// Acknowledged through the second: the unread come first, then the
	// read, newest first. The limit counts all that is shown.
	if err := s.AckUserInbox(ctx, "jo", box.Mail[1].Seq); err != nil {
		t.Fatal(err)
	}
	box, _ = s.UserInbox(ctx, "jo", 0)
	if got := letters(box.Mail); got != "yardmaster>jo three unread | clerk>jo two read | yardmaster>jo one read" ||
		box.Unread != 1 || strings.Join(box.From, ",") != "yardmaster" {
		t.Fatalf("after the ack = %s (%+v)", got, box)
	}
	if two, _ := s.UserInbox(ctx, "jo", 2); letters(two.Mail) != "yardmaster>jo three unread | clerk>jo two read" || two.Unread != 1 {
		t.Fatalf("limit 2 = %s", letters(two.Mail))
	}
	if ada, _ := s.UserInbox(ctx, "ada", 0); ada.Unread != 1 {
		t.Fatalf("jo's ack read ada's mail: %+v", ada)
	}
	// An ack never moves back.
	if err := s.AckUserInbox(ctx, "jo", 1); err != nil {
		t.Fatal(err)
	}
	if box, _ = s.UserInbox(ctx, "jo", 0); box.Unread != 1 {
		t.Fatalf("an older ack unread mail: %+v", box)
	}
	if err := s.AckUserInbox(ctx, "nobody", 9); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ack of no user = %v", err)
	}
}

// A conversation is the mail between two parties in both directions, in
// order, each with whether its receiver acknowledged it: a crew member with
// its inbox, a user with theirs.
func TestConversation(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "yardmaster")
	party(t, s, "clerk")
	if err := s.SetSetting(ctx, "operator", "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, m := range [][3]string{
		{"yardmaster", "freeze", "jo"}, {"clerk", "not this", "jo"}, {"jo", "noted", "yardmaster"}, {"jo", "not this either", "clerk"},
		{"yardmaster", "lifted", "jo"}, {"clerk", "between crew members", "yardmaster"}, {"jo", "ok", "yardmaster"},
	} {
		if err := s.Mail(ctx, m[0], m[1], m[2]); err != nil {
			t.Fatal(err)
		}
	}
	const all = "jo>yardmaster freeze unread | yardmaster>jo noted unread | jo>yardmaster lifted unread | yardmaster>jo ok unread"
	conv, _, err := s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || letters(conv) != all {
		t.Fatalf("conversation = %s, %v", letters(conv), err)
	}
	// The same from the other side.
	if rev, _, _ := s.Conversation(ctx, "yardmaster", "jo", 0, 0, 0); letters(rev) != all {
		t.Fatalf("from the yardmaster's side = %s", letters(rev))
	}
	if rr, _, _ := s.Conversation(ctx, "yardmaster", "clerk", 0, 0, 0); letters(rr) != "yardmaster>clerk between crew members unread" {
		t.Fatalf("between crew members = %s", letters(rr))
	}
	if after, _, _ := s.Conversation(ctx, "jo", "yardmaster", conv[1].Seq, 0, 0); letters(after) != "jo>yardmaster lifted unread | yardmaster>jo ok unread" {
		t.Fatalf("after %d = %s", conv[1].Seq, letters(after))
	}
	if none, _, err := s.Conversation(ctx, "jo", "yardmaster", conv[3].Seq, 0, 0); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("after the last = %v, %v", none, err)
	}

	// The yardmaster acknowledges its inbox through the first mail: that one is
	// read. jo acknowledges all of theirs.
	if err := s.AckInbox(ctx, "yardmaster", conv[0].Seq); err != nil {
		t.Fatal(err)
	}
	if err := s.AckUserInbox(ctx, "jo", conv[3].Seq); err != nil {
		t.Fatal(err)
	}
	conv, _, _ = s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if got := letters(conv); got != "jo>yardmaster freeze read | yardmaster>jo noted read | jo>yardmaster lifted unread | yardmaster>jo ok read" {
		t.Fatalf("read state = %s", got)
	}
	if err := s.AckInbox(ctx, "yardmaster", conv[2].Seq); err != nil {
		t.Fatal(err)
	}
	if conv, _, _ = s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0); !conv[2].Read {
		t.Fatalf("the yardmaster's ack did not read it: %s", letters(conv))
	}
}

// A user added under a name that had mail before reads from now on, and a
// crew member that later takes a removed user's name gets none of their mail.
func TestUserNameReused(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "yardmaster")
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mail(ctx, "ada", "to the first ada", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if box, _ := s.UserInbox(ctx, "ada", 0); box.Unread != 0 {
		t.Fatalf("the second ada has the first's unread mail: %+v", box)
	}
	if err := s.DeleteUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	party(t, s, "ada")
	if err := s.Mail(ctx, "ada", "to the crew member", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if page, _ := s.Inbox(ctx, "ada", 0); page.Total != 1 || page.Events[0].Data["text"] != "to the crew member" {
		t.Fatalf("crew member ada's inbox = %+v", page.Events)
	}
}

// kindsAfter are the events after seq, in order.
func kindsAfter(t *testing.T, s store.API, after uint64) []store.Event {
	t.Helper()
	evs, err := s.EventsSince(context.Background(), after, 0)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// An acknowledgement that passes mail is an event, mail_read, of a
// crew member's inbox and of a user's: a view following the stream learns
// from it that what it shows as sent was read. One that passes none is
// not.
func TestMailReadIsAnEvent(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "yardmaster")
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	head := func() uint64 {
		h, err := s.Head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	// Nothing to read: no event.
	at := head()
	if err := s.AckInbox(ctx, "yardmaster", at); err != nil {
		t.Fatal(err)
	}
	if err := s.AckUserInbox(ctx, "ada", at); err != nil {
		t.Fatal(err)
	}
	if evs := kindsAfter(t, s, at); len(evs) != 0 {
		t.Fatalf("acknowledging no mail logged %+v", evs)
	}

	if err := s.Mail(ctx, "yardmaster", "freeze", "ada"); err != nil {
		t.Fatal(err)
	}
	mail := head()
	if err := s.AckInbox(ctx, "yardmaster", mail); err != nil {
		t.Fatal(err)
	}
	evs := kindsAfter(t, s, mail)
	if len(evs) != 1 || evs[0].Kind != "mail_read" || evs[0].Actor != "yardmaster" ||
		evs[0].Data["reader"] != "yardmaster" || fmt.Sprint(evs[0].Data["through"]) != fmt.Sprint(mail) {
		t.Fatalf("after the crew member's ack: %+v", evs)
	}
	// The same again reads nothing new.
	at = head()
	if err := s.AckInbox(ctx, "yardmaster", mail); err != nil {
		t.Fatal(err)
	}
	if evs := kindsAfter(t, s, at); len(evs) != 0 {
		t.Fatalf("a second ack logged %+v", evs)
	}

	if err := s.Mail(ctx, "ada", "until when?", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	mail = head()
	// An ack short of the mail passes none.
	if err := s.AckUserInbox(ctx, "ada", mail-1); err != nil {
		t.Fatal(err)
	}
	if evs := kindsAfter(t, s, mail); len(evs) != 0 {
		t.Fatalf("an ack short of the mail logged %+v", evs)
	}
	if err := s.AckUserInbox(ctx, "ada", mail); err != nil {
		t.Fatal(err)
	}
	if evs := kindsAfter(t, s, mail); len(evs) != 1 || evs[0].Kind != "mail_read" || evs[0].Data["reader"] != "ada" {
		t.Fatalf("after the user's ack: %+v", evs)
	}
}

// What a crew member's session is doing is on its record, and a change of it
// is an event; the same status again writes nothing, and a session that
// starts or stops has none.
func TestCrewStatus(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "yardmaster")
	if err := s.CrewStatus(ctx, "yardmaster", "napping"); err == nil {
		t.Fatal("a status nobody knows was taken")
	}
	if err := s.CrewStatus(ctx, "nobody", "idle"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("status of no crew member = %v", err)
	}
	status := func() string {
		r, err := s.GetCrew(ctx, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		return r.Status
	}
	// Not running: nothing is doing anything.
	if err := s.CrewStatus(ctx, "yardmaster", "idle"); err != nil || status() != "" {
		t.Fatalf("status of a stopped crew member = %q, %v", status(), err)
	}
	if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "a"}); err != nil {
		t.Fatal(err)
	}
	at, _ := s.Head(ctx)
	for _, st := range []string{"working", "working", "blocked", "idle"} {
		if err := s.CrewStatus(ctx, "yardmaster", st); err != nil || status() != st {
			t.Fatalf("status %s = %q, %v", st, status(), err)
		}
	}
	var got []string
	for _, e := range kindsAfter(t, s, at) {
		if e.Kind != "crew_status" || e.Data["crew"] != "yardmaster" || e.Bead != nil {
			t.Fatalf("event %+v", e)
		}
		got = append(got, fmt.Sprintf("%v>%v", e.Data["was"], e.Data["status"]))
	}
	if strings.Join(got, " ") != ">working working>blocked blocked>idle" {
		t.Fatalf("status events = %v", got)
	}
	if err := s.CrewStopped(ctx, "yardmaster", "gone"); err != nil || status() != "" {
		t.Fatalf("status after the stop = %q, %v", status(), err)
	}
}
