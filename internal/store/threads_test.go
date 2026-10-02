package store_test

import (
	"context"
	"errors"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// threads lists the resident's threads as "source/thread how".
func threads(t *testing.T, s store.API, name string) []string {
	t.Helper()
	ps, err := s.Threads(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, p := range ps {
		out = append(out, p.Source+"/"+p.Thread+" "+p.How)
	}
	return out
}

func TestThreadParticipation(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	sub := func(scope string) store.Subscription { return store.Subscription{Source: "linear", Scope: scope} }
	resident(t, s, "inv", false, sub(store.ScopeInvolved))
	resident(t, s, "ment", false, sub(store.ScopeMentions))
	resident(t, s, "all", false, sub(store.ScopeAll))
	jane := hooks.Party{ID: "jane-id", Name: "Jane"}
	on := func(actor hooks.Party, thread string, mentions ...string) hooks.Envelope {
		return hooks.Envelope{Source: "linear", Type: "comment", Action: "create", Actor: actor,
			Thread: hooks.Thread{ID: thread, Title: "Issue " + thread}, Mentions: mentions}
	}

	steps := []struct {
		name string
		env  hooks.Envelope
		want map[string]string
	}{
		{"before taking part", on(jane, "T"), map[string]string{"all": "all"}},
		// inv comments: its own action never reaches it, but makes it a
		// participant; so does ment's, though its scope ignores threads.
		{"inv comments", on(hooks.Party{ID: "inv-id"}, "T"), map[string]string{"all": "all"}},
		{"ment comments", on(hooks.Party{ID: "ment-id"}, "T"), map[string]string{"all": "all", "inv": "participant"}},
		{"others comment later", on(jane, "T"), map[string]string{"all": "all", "inv": "participant"}},
		{"inv comments again", on(hooks.Party{ID: "inv-id"}, "T"), map[string]string{"all": "all"}},
		{"another thread", on(jane, "U"), map[string]string{"all": "all"}},
		{"a mention says more", on(jane, "T", "inv"), map[string]string{"all": "all", "inv": "mentioned"}},
		{"acting by profile key", on(hooks.Party{ID: "all"}, "T"), map[string]string{"inv": "participant"}},
		{"scope all reports participation", on(jane, "T"),
			map[string]string{"all": "participant", "inv": "participant"}},
		{"same id, other source", hooks.Envelope{Source: "token", Type: "message", Actor: jane,
			Thread: hooks.Thread{ID: "T"}}, map[string]string{}},
	}
	for _, c := range steps {
		if got := deliveries(t, s, c.env); !same(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if got := threads(t, s, "inv"); len(got) != 1 || got[0] != "linear/T acted" {
		t.Fatalf("inv's threads: %v", got)
	}
	ps, _ := s.Threads(ctx, "inv")
	if ps[0].Title != "Issue T" || ps[0].Since.IsZero() {
		t.Fatalf("participation: %+v", ps[0])
	}

	if err := s.UnfollowThread(ctx, "inv", "linear", "T", "me"); err != nil {
		t.Fatal(err)
	}
	if got := deliveries(t, s, on(jane, "T")); !same(got, map[string]string{"all": "participant"}) {
		t.Errorf("after unfollow: %v", got)
	}
	if err := s.UnfollowThread(ctx, "inv", "linear", "T", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unfollow twice: %v", err)
	}
	// Acting there again makes it a participant again.
	deliveries(t, s, on(hooks.Party{ID: "inv-id"}, "T"))
	if got := deliveries(t, s, on(jane, "T")); got["inv"] != "participant" {
		t.Errorf("after acting again: %v", got)
	}
}

func TestFollowThread(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	// Following needs no identity: it is the resident's own say-so.
	resident(t, s, "noid-inv", true, store.Subscription{Source: "linear", Scope: store.ScopeInvolved})
	jane := hooks.Party{ID: "jane-id"}
	comment := hooks.Envelope{Source: "linear", Type: "comment", Actor: jane, Thread: hooks.Thread{ID: "T"}}

	if got := deliveries(t, s, comment); len(got) != 0 {
		t.Fatalf("before follow: %v", got)
	}
	for range 2 { // following twice keeps one
		if err := s.FollowThread(ctx, "noid-inv", "linear", "T", "me"); err != nil {
			t.Fatal(err)
		}
	}
	if got := threads(t, s, "noid-inv"); len(got) != 1 || got[0] != "linear/T followed" {
		t.Fatalf("threads: %v", got)
	}
	if got := deliveries(t, s, comment); got["noid-inv"] != "participant" {
		t.Fatalf("after follow: %v", got)
	}
	page, err := s.Inbox(ctx, "noid-inv", 0)
	if err != nil || page.Total != 1 || store.HookReason(page.Events[0], "noid-inv") != store.ReasonParticipant {
		t.Fatalf("inbox: %+v %v", page, err)
	}

	for _, bad := range [][2]string{{"", "T"}, {"linear", ""}} {
		if err := s.FollowThread(ctx, "noid-inv", bad[0], bad[1], "me"); err == nil {
			t.Errorf("followed %v", bad)
		}
	}
	if err := s.FollowThread(ctx, "ghost", "linear", "T", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown resident: %v", err)
	}
	if _, err := s.Threads(ctx, "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown resident's threads: %v", err)
	}

	// Redefining keeps participation; deleting forgets it, so a new
	// resident under the name starts with none.
	if _, err := s.PutResident(ctx, store.Resident{Name: "noid-inv", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := threads(t, s, "noid-inv"); len(got) != 1 {
		t.Fatalf("after redefine: %v", got)
	}
	if err := s.DeleteResident(ctx, "noid-inv", "me"); err != nil {
		t.Fatal(err)
	}
	resident(t, s, "noid-inv", true, store.Subscription{Source: "linear", Scope: store.ScopeInvolved})
	if got := threads(t, s, "noid-inv"); len(got) != 0 {
		t.Fatalf("recreated resident inherits threads: %v", got)
	}
}
