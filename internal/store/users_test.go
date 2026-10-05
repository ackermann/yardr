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
	if _, err := s.PutResident(context.Background(), store.Resident{Name: name, Runner: "herdr"}, "jo"); err != nil {
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

// Residents and users share one space of names, refused both ways, and the
// operator cannot be given a resident's name.
func TestOneNameOneParty(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "mayor")
	if _, err := s.AddUser(ctx, "mayor", "jo"); err == nil || !strings.Contains(err.Error(), "that is a resident's name") {
		t.Fatalf("user with a resident's name = %v", err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutResident(ctx, store.Resident{Name: "ada", Runner: "herdr"}, "jo"); err == nil || !strings.Contains(err.Error(), "that is a user's name") {
		t.Fatalf("resident with a user's name = %v", err)
	}
	if _, err := s.GetResident(ctx, "ada"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the refused resident exists: %v", err)
	}
	if err := s.SetSetting(ctx, "operator", "mayor", "jo"); err == nil || !strings.Contains(err.Error(), "that is a resident's name") {
		t.Fatalf("operator with a resident's name = %v", err)
	}
	if all, _ := s.Settings(ctx); all["operator"] != "" || userNames(t, s) != "ada" {
		t.Fatalf("the refused setting was stored: %v, users %q", all, userNames(t, s))
	}
	// A resident that is there is still redefined.
	if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr", MaxAge: "1h"}, "jo"); err != nil {
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

// Mail to a user lands in that user's inbox alone, unread until they
// acknowledge it, and never in a resident's.
func TestUserInbox(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "mayor")
	party(t, s, "clerk")
	for _, u := range []string{"jo", "ada"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range [][3]string{{"jo", "one", "mayor"}, {"ada", "for ada", "mayor"}, {"jo", "two", "clerk"}, {"jo", "three", "mayor"}} {
		if err := s.Mail(ctx, m[0], m[1], m[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Mail(ctx, "jo", strings.Repeat("x", store.MaxMailBytes+1), "mayor"); err == nil {
		t.Fatal("mail over the limit to a user accepted")
	}
	if err := s.Mail(ctx, "nobody", "hi", "mayor"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("mail to nobody = %v", err)
	}
	if _, err := s.UserInbox(ctx, "mayor", 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a resident read as a user = %v", err)
	}

	box, err := s.UserInbox(ctx, "jo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := letters(box.Mail); got != "mayor>jo one unread | clerk>jo two unread | mayor>jo three unread" ||
		box.Unread != 3 || strings.Join(box.From, ",") != "mayor,clerk" || box.Last != box.Mail[2].Seq {
		t.Fatalf("jo's inbox = %s (%+v)", got, box)
	}
	if ada, _ := s.UserInbox(ctx, "ada", 0); letters(ada.Mail) != "mayor>ada for ada unread" {
		t.Fatalf("ada's inbox = %s", letters(ada.Mail))
	}
	for _, r := range []string{"mayor", "clerk"} {
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
	if got := letters(box.Mail); got != "mayor>jo three unread | clerk>jo two read | mayor>jo one read" ||
		box.Unread != 1 || strings.Join(box.From, ",") != "mayor" {
		t.Fatalf("after the ack = %s (%+v)", got, box)
	}
	if two, _ := s.UserInbox(ctx, "jo", 2); letters(two.Mail) != "mayor>jo three unread | clerk>jo two read" || two.Unread != 1 {
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
// order, each with whether its receiver acknowledged it: a resident with
// its inbox, a user with theirs.
func TestConversation(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "mayor")
	party(t, s, "clerk")
	if err := s.SetSetting(ctx, "operator", "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, m := range [][3]string{
		{"mayor", "freeze", "jo"}, {"clerk", "not this", "jo"}, {"jo", "noted", "mayor"}, {"jo", "not this either", "clerk"},
		{"mayor", "lifted", "jo"}, {"clerk", "between residents", "mayor"}, {"jo", "ok", "mayor"},
	} {
		if err := s.Mail(ctx, m[0], m[1], m[2]); err != nil {
			t.Fatal(err)
		}
	}
	const all = "jo>mayor freeze unread | mayor>jo noted unread | jo>mayor lifted unread | mayor>jo ok unread"
	conv, err := s.Conversation(ctx, "jo", "mayor", 0)
	if err != nil || letters(conv) != all {
		t.Fatalf("conversation = %s, %v", letters(conv), err)
	}
	// The same from the other side.
	if rev, _ := s.Conversation(ctx, "mayor", "jo", 0); letters(rev) != all {
		t.Fatalf("from the mayor's side = %s", letters(rev))
	}
	if rr, _ := s.Conversation(ctx, "mayor", "clerk", 0); letters(rr) != "mayor>clerk between residents unread" {
		t.Fatalf("between residents = %s", letters(rr))
	}
	if after, _ := s.Conversation(ctx, "jo", "mayor", conv[1].Seq); letters(after) != "jo>mayor lifted unread | mayor>jo ok unread" {
		t.Fatalf("after %d = %s", conv[1].Seq, letters(after))
	}
	if none, err := s.Conversation(ctx, "jo", "mayor", conv[3].Seq); err != nil || none == nil || len(none) != 0 {
		t.Fatalf("after the last = %v, %v", none, err)
	}

	// The mayor acknowledges its inbox through the first mail: that one is
	// read. jo acknowledges all of theirs.
	if err := s.AckInbox(ctx, "mayor", conv[0].Seq); err != nil {
		t.Fatal(err)
	}
	if err := s.AckUserInbox(ctx, "jo", conv[3].Seq); err != nil {
		t.Fatal(err)
	}
	conv, _ = s.Conversation(ctx, "jo", "mayor", 0)
	if got := letters(conv); got != "jo>mayor freeze read | mayor>jo noted read | jo>mayor lifted unread | mayor>jo ok read" {
		t.Fatalf("read state = %s", got)
	}
	if err := s.AckInbox(ctx, "mayor", conv[2].Seq); err != nil {
		t.Fatal(err)
	}
	if conv, _ = s.Conversation(ctx, "jo", "mayor", 0); !conv[2].Read {
		t.Fatalf("the mayor's ack did not read it: %s", letters(conv))
	}
}

// A user added under a name that had mail before reads from now on, and a
// resident that later takes a removed user's name gets none of their mail.
func TestUserNameReused(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "mayor")
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mail(ctx, "ada", "to the first ada", "mayor"); err != nil {
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
	if err := s.Mail(ctx, "ada", "to the resident", "mayor"); err != nil {
		t.Fatal(err)
	}
	if page, _ := s.Inbox(ctx, "ada", 0); page.Total != 1 || page.Events[0].Data["text"] != "to the resident" {
		t.Fatalf("resident ada's inbox = %+v", page.Events)
	}
}
