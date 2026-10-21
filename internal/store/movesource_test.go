package store_test

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The yard of 11 Oct, as the yardmaster's note has it: the old hook
// under linear, yardmaster's identity and grants under both names,
// subscriptions and followed threads under linear-declared, links under
// linear-declared after the move. MoveSourceRecords folds the old name
// into the new one; running it twice changes nothing.
func TestMoveSourceRecords(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutHook(ctx, store.Hook{Name: "linear", Source: "linear", SecretFile: "/s"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutHook(ctx, store.Hook{Name: "lin-declared", Source: "linear-declared", SecretFile: "/s",
		As: "yardmaster", Map: map[string]string{"task:new": "started"}}, "me"); err == nil {
		// The hook acts as yardmaster, which needs the identity first.
		t.Fatalf("hook with --as and no identity was taken")
	}
	id := store.Identity{ID: "u-yardmaster", Key: "yardmaster"}
	if err := s.SetIdentity(ctx, "yardmaster", "linear", id, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "yardmaster", "linear-declared", id, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutHook(ctx, store.Hook{Name: "lin-declared", Source: "linear-declared", SecretFile: "/s",
		As: "yardmaster", Map: map[string]string{"task:new": "started"}}, "me"); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	r.Config[store.OpenThreads] = "linear,linear-declared"
	r.Config[store.SetState] = "linear,linear-declared"
	if _, err := s.PutCrew(ctx, *r, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSubscription(ctx, "yardmaster", store.Subscription{Source: "linear-declared", Scope: store.ScopeInvolved}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if err := s.FollowThread(ctx, "yardmaster", "linear-declared", "issue-1", "me"); err != nil {
		t.Fatal(err)
	}
	moved, err := s.MoveSourceRecords(ctx, "linear-declared", "linear", "me")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Hooks != 1 || moved.Identities != 1 || moved.Grants != 1 || moved.Subscriptions != 1 || moved.Threads != 1 {
		t.Fatalf("moved = %+v", moved)
	}
	if h, _ := s.GetHook(ctx, "lin-declared"); h.Source != "linear" || h.As != "yardmaster" || h.Map["task:new"] != "started" {
		t.Fatalf("hook = %+v", h)
	}
	if r, _ := s.GetCrew(ctx, "yardmaster"); r.Identities["linear"] != id || len(r.Identities) != 1 {
		t.Fatalf("identities = %+v", r.Identities)
	} else if got := strings.Join(r.MayOpen(), ","); got != "linear" {
		t.Fatalf("open_threads = %q", got)
	} else if got := strings.Join(r.MaySetState(), ","); got != "linear" {
		t.Fatalf("set_state = %q", got)
	} else if len(r.Subscriptions) != 1 || r.Subscriptions[0].Source != "linear" {
		t.Fatalf("subscriptions = %+v", r.Subscriptions)
	}
	threads, err := s.Threads(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].Source != "linear" || threads[0].Thread != "issue-1" {
		t.Fatalf("threads = %+v", threads)
	}
	head, _ := s.Head(ctx)
	again, err := s.MoveSourceRecords(ctx, "linear-declared", "linear", "me")
	if err != nil {
		t.Fatal(err)
	}
	if again.Moved() {
		t.Fatalf("second move = %+v", again)
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("second move logged %d events", now-head)
	}
}

// An identity under both names that disagrees refuses the whole move,
// changing nothing.
func TestMoveSourceRecordsRefusesAClash(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "yardmaster", "linear", store.Identity{ID: "u-one"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "yardmaster", "linear-declared", store.Identity{ID: "u-two"}, "me"); err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	if _, err := s.MoveSourceRecords(ctx, "linear-declared", "linear", "me"); err == nil ||
		!strings.Contains(err.Error(), "one linear-declared identity and another linear identity") {
		t.Fatalf("err = %v", err)
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("refused, yet %d events were logged", now-head)
	}
}

// A user's identity moves with the crew's, and one that differs under
// the two names refuses the move.
func TestMoveSourceRecordsMovesUserIdentities(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.AddUser(ctx, "jo", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdentity(ctx, "jo", "linear-declared", store.Identity{ID: "u-jo"}, "jo"); err != nil {
		t.Fatal(err)
	}
	moved, err := s.MoveSourceRecords(ctx, "linear-declared", "linear", "me")
	if err != nil || moved.Identities != 1 {
		t.Fatalf("moved = %+v, %v", moved, err)
	}
	u, err := s.GetUser(ctx, "jo")
	if err != nil || len(u.Identities) != 1 || u.Identities["linear"].ID != "u-jo" {
		t.Fatalf("identities = %+v, %v", u.Identities, err)
	}
	if err := s.SetIdentity(ctx, "jo", "linear-declared", store.Identity{ID: "u-other"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveSourceRecords(ctx, "linear-declared", "linear", "me"); err == nil ||
		!strings.Contains(err.Error(), "user jo has one linear-declared identity and another linear identity") {
		t.Fatalf("err = %v", err)
	}
}
