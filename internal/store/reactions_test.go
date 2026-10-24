package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A party to a letter puts one reaction on it: a new one replaces the old
// one, and --clear takes it away. The reaction shows on the letter wherever
// it is read.
func TestReact(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "yardmaster")
	for _, u := range []string{"jo", "ada"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Mail(ctx, "jo", "the shed is painted", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	conv, _, err := s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(conv) != 1 {
		t.Fatalf("conversation = %+v, %v", conv, err)
	}
	seq := conv[0].Seq
	reactions := func() []store.Reaction {
		t.Helper()
		conv, _, err := s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
		if err != nil || len(conv) != 1 {
			t.Fatalf("conversation = %+v, %v", conv, err)
		}
		return conv[0].Reactions
	}
	if got := reactions(); len(got) != 0 {
		t.Fatalf("reactions before any = %+v", got)
	}
	if err := s.React(ctx, seq, "👍", "jo"); err != nil {
		t.Fatal(err)
	}
	if got := reactions(); len(got) != 1 || got[0] != (store.Reaction{From: "jo", Emoji: "👍"}) {
		t.Fatalf("after jo's 👍 = %+v", got)
	}
	// The inbox holds the same letter.
	box, err := s.UserInbox(ctx, "jo", 0, "jo")
	if err != nil || len(box.Mail) != 1 || len(box.Mail[0].Reactions) != 1 ||
		box.Mail[0].Reactions[0] != (store.Reaction{From: "jo", Emoji: "👍"}) {
		t.Fatalf("jo's inbox = %+v, %v", box, err)
	}
	// A new one replaces the old one.
	if err := s.React(ctx, seq, "❤️", "jo"); err != nil {
		t.Fatal(err)
	}
	if got := reactions(); len(got) != 1 || got[0] != (store.Reaction{From: "jo", Emoji: "❤️"}) {
		t.Fatalf("after jo's ❤️ = %+v", got)
	}
	// The sender holds one too, beside the receiver's.
	if err := s.React(ctx, seq, "🎉", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if got := reactions(); len(got) != 2 ||
		got[0] != (store.Reaction{From: "jo", Emoji: "❤️"}) ||
		got[1] != (store.Reaction{From: "yardmaster", Emoji: "🎉"}) {
		t.Fatalf("after the yardmaster's 🎉 = %+v", got)
	}
	// Clearing takes one away and leaves the other's.
	if err := s.React(ctx, seq, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if got := reactions(); len(got) != 1 || got[0].From != "yardmaster" {
		t.Fatalf("after jo's clear = %+v", got)
	}
	if err := s.React(ctx, seq, "", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if got := reactions(); len(got) != 0 {
		t.Fatalf("after both clear = %+v", got)
	}
}

// Only the letter's sender or receiver may react, only with one of the set,
// and only mail its sender wrote takes one.
func TestReactRefused(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "yardmaster")
	for _, u := range []string{"jo", "ada"} {
		if _, err := s.AddUser(ctx, u, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Mail(ctx, "jo", "the shed is painted", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	conv, _, err := s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(conv) != 1 {
		t.Fatalf("conversation = %+v, %v", conv, err)
	}
	seq := conv[0].Seq
	if err := s.React(ctx, seq, "👍", "ada"); err == nil ||
		!strings.Contains(err.Error(), "only its sender or receiver may react") {
		t.Fatalf("a stranger's reaction = %v", err)
	}
	if err := s.React(ctx, seq, "💩", "jo"); err == nil {
		t.Fatal("an emoji outside the set was accepted")
	} else {
		for _, want := range []string{"use one of", "👍", "👎", "❤️", "🎉", "👀", "✅"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the refusal names no set: %v", err)
			}
		}
	}
	if err := s.React(ctx, 99999, "👍", "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a reaction to no mail = %v", err)
	}
	// Another kind's event is no letter: the reaction just logged is one.
	if err := s.React(ctx, seq, "👍", "jo"); err != nil {
		t.Fatal(err)
	}
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.React(ctx, head, "👍", "jo"); err == nil || !strings.Contains(err.Error(), "only mail takes reactions") {
		t.Fatalf("a reaction to a reaction = %v", err)
	}
	// A timer's letter takes none.
	tm, err := s.AddTimer(ctx, store.NewTimer{To: "jo", Reason: "stand-up", Due: time.Now().Add(time.Minute)}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FireTimers(ctx, time.Now().Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	box, err := s.UserInbox(ctx, "jo", 0, "jo")
	if err != nil {
		t.Fatal(err)
	}
	var timer uint64
	for _, l := range box.Mail {
		if l.Timer == tm.ID {
			timer = l.Seq
		}
	}
	if timer == 0 {
		t.Fatalf("no timer mail in %+v", box.Mail)
	}
	if err := s.React(ctx, timer, "👍", "jo"); err == nil || !strings.Contains(err.Error(), "takes no reactions") {
		t.Fatalf("a reaction to a timer's letter = %v", err)
	}
}

// A letter without reactions marshals as it always did: no "reactions" key.
func TestLetterWithoutReactionsMarshalsAsBefore(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "yardmaster")
	if _, err := s.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mail(ctx, "jo", "the shed is painted", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	conv, _, err := s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(conv) != 1 {
		t.Fatalf("conversation = %+v, %v", conv, err)
	}
	raw, err := json.Marshal(conv[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "reactions") {
		t.Fatalf("a letter without reactions marshals with the key: %s", raw)
	}
	if err := s.React(ctx, conv[0].Seq, "👍", "jo"); err != nil {
		t.Fatal(err)
	}
	conv, _, _ = s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	raw, err = json.Marshal(conv[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"reactions":[{"from":"jo","emoji":"👍"}]`) {
		t.Fatalf("a letter with a reaction marshals as %s", raw)
	}
}

// A reaction to a crew member's own mail lands in that crew member's inbox;
// nothing reaches a user's inbox, and a clear tells no inbox.
func TestReactionInbox(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	party(t, s, "yardmaster")
	if _, err := s.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mail(ctx, "jo", "the shed is painted, and the fence", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	conv, _, err := s.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(conv) != 1 {
		t.Fatalf("conversation = %+v, %v", conv, err)
	}
	seq := conv[0].Seq
	if err := s.React(ctx, seq, "👍", "jo"); err != nil {
		t.Fatal(err)
	}
	page, err := s.Inbox(ctx, "yardmaster", 0)
	if err != nil {
		t.Fatal(err)
	}
	var found *store.Event
	for i, e := range page.Events {
		if e.Kind == "reaction" {
			found = &page.Events[i]
		}
	}
	if found == nil {
		t.Fatalf("the yardmaster's inbox holds no reaction: %+v", page.Events)
	}
	if found.Actor != "jo" {
		t.Fatalf("the reaction is by %s: %+v", found.Actor, found)
	}
	if emoji, _ := found.Data["emoji"].(string); emoji != "👍" {
		t.Fatalf("the reaction is %q: %+v", emoji, found)
	}
	// Nothing reaches the user's inbox: the mail alone, unread once.
	box, err := s.UserInbox(ctx, "jo", 0, "jo")
	if err != nil || box.Unread != 1 || len(box.Mail) != 1 {
		t.Fatalf("jo's inbox = %+v, %v", box, err)
	}
	// The sender's own reaction tells nobody: its other party is the user.
	if err := s.React(ctx, seq, "🎉", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if page, _ := s.Inbox(ctx, "yardmaster", 0); page.Total != 1 {
		t.Fatalf("the yardmaster's own reaction is in its inbox: %+v", page.Events)
	}
	if box, _ := s.UserInbox(ctx, "jo", 0, "jo"); box.Unread != 1 {
		t.Fatalf("jo's inbox = %+v", box)
	}
	// A clear tells no inbox either.
	if err := s.AckInbox(ctx, "yardmaster", found.Seq); err != nil {
		t.Fatal(err)
	}
	if err := s.React(ctx, seq, "", "jo"); err != nil {
		t.Fatal(err)
	}
	if page, _ := s.Inbox(ctx, "yardmaster", 0); page.Total != 0 {
		t.Fatalf("the clear is in the inbox: %+v", page.Events)
	}
}
