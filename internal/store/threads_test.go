package store_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
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
	s, own := storetest.NewOwned(t)
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
		if got := deliveries(t, s, own, c.env); !same(got, c.want) {
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
	if got := deliveries(t, s, own, on(jane, "T")); !same(got, map[string]string{"all": "participant"}) {
		t.Errorf("after unfollow: %v", got)
	}
	if err := s.UnfollowThread(ctx, "inv", "linear", "T", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unfollow twice: %v", err)
	}
	// Acting there again makes it a participant again.
	deliveries(t, s, own, on(hooks.Party{ID: "inv-id"}, "T"))
	if got := deliveries(t, s, own, on(jane, "T")); got["inv"] != "participant" {
		t.Errorf("after acting again: %v", got)
	}
}

func TestFollowThread(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	// Following needs no identity: it is the resident's own say-so.
	resident(t, s, "noid-inv", true, store.Subscription{Source: "linear", Scope: store.ScopeInvolved})
	jane := hooks.Party{ID: "jane-id"}
	comment := hooks.Envelope{Source: "linear", Type: "comment", Actor: jane, Thread: hooks.Thread{ID: "T"}}

	if got := deliveries(t, s, own, comment); len(got) != 0 {
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
	if got := deliveries(t, s, own, comment); got["noid-inv"] != "participant" {
		t.Fatalf("after follow: %v", got)
	}
	page, err := s.Inbox(ctx, "noid-inv", 0)
	if err != nil || page.Total != 1 || store.Reason(page.Events[0], "noid-inv") != store.ReasonParticipant {
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
	if err := s.DeleteResident(ctx, "noid-inv", "", "me"); err != nil {
		t.Fatal(err)
	}
	resident(t, s, "noid-inv", true, store.Subscription{Source: "linear", Scope: store.ScopeInvolved})
	if got := threads(t, s, "noid-inv"); len(got) != 0 {
		t.Fatalf("recreated resident inherits threads: %v", got)
	}
}

// A reply is on record before it is posted, under a key chosen for it (a
// UUID v4, Linear's form of a client-chosen id): the same answer to the
// same delivery gets the same record back, counted as another attempt;
// another body, conversation or delivery gets its own. Replied marks it
// posted with the comment's id, which the replied event carries too.
func TestReplying(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	resident(t, s, "inv", false)
	uuid4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

	rp, err := s.Replying(ctx, "inv", 7, "", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !uuid4.MatchString(rp.Key) || rp.Attempts != 1 || rp.Posted || rp.Seq != 7 || rp.At.IsZero() {
		t.Fatalf("first attempt: %+v", rp)
	}
	again, err := s.Replying(ctx, "inv", 7, "", "hello")
	if err != nil || again.Key != rp.Key || again.Attempts != 2 || again.Posted {
		t.Fatalf("second attempt: %+v, %v", again, err)
	}
	seen := map[string]bool{rp.Key: true}
	for _, c := range []struct {
		seq      uint64
		to, body string
	}{{7, "", "other"}, {7, "c1", "hello"}, {8, "", "hello"}} {
		o, err := s.Replying(ctx, "inv", c.seq, c.to, c.body)
		if err != nil || seen[o.Key] || o.Attempts != 1 {
			t.Fatalf("%+v: %+v, %v", c, o, err)
		}
		seen[o.Key] = true
	}

	posted := store.Reply{Source: "linear", Thread: "T", URL: "https://l/T#c-1", Key: rp.Key, Message: "c-1", Seq: 7}
	if err := s.Replied(ctx, "inv", posted, "inv"); err != nil {
		t.Fatal(err)
	}
	done, err := s.Replying(ctx, "inv", 7, "", "hello")
	if err != nil || !done.Posted || done.Message != "c-1" || done.URL != "https://l/T#c-1" || done.Key != rp.Key || done.Attempts != 3 ||
		done.Source != "linear" || done.Thread != "T" {
		t.Fatalf("after Replied: %+v, %v", done, err)
	}
	evs, err := s.EventsSince(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1]
	if last.Kind != "replied" || last.Data["message"] != "c-1" || fmt.Sprint(last.Data["seq"]) != "7" {
		t.Fatalf("replied event: %+v", last)
	}

	if _, err := s.Replying(ctx, "inv", 0, "", "hello"); err == nil {
		t.Fatal("no seq")
	}
	if _, err := s.Replying(ctx, "inv", 7, "", ""); err == nil {
		t.Fatal("no body")
	}
	if _, err := s.Replying(ctx, "ghost", 7, "", "hello"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown resident: %v", err)
	}
	// A resident deleted and made again starts with no replies.
	if err := s.DeleteResident(ctx, "inv", "", "me"); err != nil {
		t.Fatal(err)
	}
	resident(t, s, "inv", false)
	fresh, err := s.Replying(ctx, "inv", 7, "", "hello")
	if err != nil || fresh.Key == rp.Key || fresh.Attempts != 1 {
		t.Fatalf("after recreating the resident: %+v, %v", fresh, err)
	}
}

// A reply makes the resident a participant at once, with a replied event
// on its journal; the webhook's echo of the reply changes nothing.
func TestReplied(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	resident(t, s, "inv", false, store.Subscription{Source: "linear", Scope: store.ScopeInvolved})
	rp := store.Reply{Source: "linear", Thread: "T", Title: "Issue T", ThreadURL: "https://l/T", URL: "https://l/T#c-1"}
	if err := s.Replied(ctx, "inv", rp, "inv"); err != nil {
		t.Fatal(err)
	}
	if got := threads(t, s, "inv"); len(got) != 1 || got[0] != "linear/T replied" {
		t.Fatalf("threads: %v", got)
	}
	ps, _ := s.Threads(ctx, "inv")
	if ps[0].Title != "Issue T" || ps[0].URL != "https://l/T" {
		t.Fatalf("participation: %+v", ps[0])
	}
	evs, err := s.EventsSince(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got *store.Event
	for i := range evs {
		if evs[i].Kind == "replied" {
			got = &evs[i]
		}
	}
	r, _ := s.GetResident(ctx, "inv")
	if got == nil || got.Actor != "inv" || got.Bead == nil || *got.Bead != r.Journal ||
		got.Data["source"] != "linear" || got.Data["thread"] != "T" || got.Data["url"] != "https://l/T#c-1" {
		t.Fatalf("replied event: %+v", got)
	}
	// The echo, and a second reply, keep how it joined.
	deliveries(t, s, own, hooks.Envelope{Source: "linear", Type: "comment", Actor: hooks.Party{ID: "inv-id"}, Thread: hooks.Thread{ID: "T"}})
	if err := s.Replied(ctx, "inv", rp, "inv"); err != nil {
		t.Fatal(err)
	}
	if got := threads(t, s, "inv"); len(got) != 1 || got[0] != "linear/T replied" {
		t.Fatalf("threads after echo: %v", got)
	}
	if got := deliveries(t, s, own, hooks.Envelope{Source: "linear", Type: "comment", Actor: hooks.Party{ID: "jane"},
		Thread: hooks.Thread{ID: "T"}}); got["inv"] != "participant" {
		t.Fatalf("after reply: %v", got)
	}
	if err := s.Replied(ctx, "ghost", rp, "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown resident: %v", err)
	}
	if err := s.Replied(ctx, "inv", store.Reply{Source: "linear"}, "inv"); err == nil {
		t.Fatal("replied without a thread")
	}
}

// A resident opens threads only in the sources its definition allows.
// Opening puts the thread on record with a key before it is opened: the
// same source, place, title and body gets the same key, anything else its
// own. Opened makes the thread the resident's at once, so what others say
// there reaches an involved subscription with no mention.
func TestOpening(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	resident(t, s, "inv", false, store.Subscription{Source: "linear", Scope: store.ScopeInvolved})
	head, _ := s.Head(ctx)

	_, err := s.Opening(ctx, "inv", "linear", "YRD", "Web view", "body")
	if err == nil || !strings.Contains(err.Error(), "may not open threads in linear") ||
		!strings.Contains(err.Error(), "--set open_threads=linear on yardr resident add inv") {
		t.Fatalf("not allowed: %v", err)
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("the refusal logged %d events", now-head)
	}
	allow := func(v any) {
		t.Helper()
		if _, err := s.PutResident(ctx, store.Resident{Name: "inv", Runner: "herdr", Config: map[string]any{store.OpenThreads: v}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	// Allowed in another source only: still refused, and told what to set.
	allow("github")
	if _, err := s.Opening(ctx, "inv", "linear", "YRD", "Web view", "body"); err == nil ||
		!strings.Contains(err.Error(), "--set open_threads=github,linear ") {
		t.Fatalf("allowed elsewhere: %v", err)
	}
	for _, v := range []any{"linear", " github , linear", []any{"linear", "github"}} {
		allow(v)
		if _, err := s.Opening(ctx, "inv", "linear", "YRD", "probe", ""); err != nil {
			t.Fatalf("open_threads=%v: %v", v, err)
		}
	}

	uuid4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	rp, err := s.Opening(ctx, "inv", "linear", "YRD", "Web view", "body")
	if err != nil || !uuid4.MatchString(rp.Key) || rp.Attempts != 1 || rp.Posted || rp.Seq != 0 || rp.In != "YRD" || rp.At.IsZero() {
		t.Fatalf("first attempt: %+v, %v", rp, err)
	}
	again, err := s.Opening(ctx, "inv", "linear", "YRD", "Web view", "body")
	if err != nil || again.Key != rp.Key || again.Attempts != 2 || again.Posted || !again.At.Equal(rp.At) {
		t.Fatalf("second attempt: %+v, %v", again, err)
	}
	seen := map[string]bool{rp.Key: true}
	for _, c := range [][4]string{{"github", "YRD", "Web view", "body"}, {"linear", "ENG", "Web view", "body"},
		{"linear", "YRD", "Other", "body"}, {"linear", "YRD", "Web view", "other"}} {
		o, err := s.Opening(ctx, "inv", c[0], c[1], c[2], c[3])
		if err != nil || seen[o.Key] || o.Attempts != 1 {
			t.Fatalf("%v: %+v, %v", c, o, err)
		}
		seen[o.Key] = true
	}
	// A reply with the same text is no attempt at the thread.
	if r, err := s.Replying(ctx, "inv", 7, "", "body"); err != nil || seen[r.Key] {
		t.Fatalf("a reply: %+v, %v", r, err)
	}

	jane := hooks.Envelope{Source: "linear", Type: "comment", Action: "create", Actor: hooks.Party{ID: "jane-id"},
		Thread: hooks.Thread{ID: rp.Key}}
	if got := deliveries(t, s, own, jane); len(got) != 0 {
		t.Fatalf("before it is opened: %v", got)
	}
	opened := store.Reply{Source: "linear", In: "YRD", Thread: rp.Key, Title: "Web view", ThreadURL: "https://l/YRD-9",
		URL: "https://l/YRD-9", Key: rp.Key, Message: rp.Key}
	if err := s.Opened(ctx, "inv", opened, "inv"); err != nil {
		t.Fatal(err)
	}
	evs, err := s.EventsSince(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1]
	if last.Kind != "thread_opened" || last.Actor != "inv" || last.Data["thread"] != rp.Key || last.Data["in"] != "YRD" ||
		last.Data["title"] != "Web view" || last.Data["url"] != "https://l/YRD-9" {
		t.Fatalf("thread_opened event: %+v", last)
	}
	if got := threads(t, s, "inv"); len(got) != 1 || got[0] != "linear/"+rp.Key+" opened" {
		t.Fatalf("threads: %v", got)
	}
	if got := deliveries(t, s, own, jane); got["inv"] != "participant" {
		t.Fatalf("a comment in the opened thread, no mention: %v", got)
	}
	done, err := s.Opening(ctx, "inv", "linear", "YRD", "Web view", "body")
	if err != nil || !done.Posted || done.Key != rp.Key || done.Thread != rp.Key || done.ThreadURL != "https://l/YRD-9" || done.Attempts != 3 {
		t.Fatalf("after Opened: %+v, %v", done, err)
	}

	for _, c := range [][3]string{{"", "YRD", "t"}, {"linear", "", "t"}, {"linear", "YRD", ""}} {
		if _, err := s.Opening(ctx, "inv", c[0], c[1], c[2], "b"); err == nil {
			t.Fatalf("%q: accepted", c)
		}
	}
	if err := s.Opened(ctx, "inv", store.Reply{Source: "linear", Thread: "T"}, "inv"); err == nil {
		t.Fatal("Opened without a key")
	}
	if _, err := s.Opening(ctx, "ghost", "linear", "YRD", "t", "b"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown resident: %v", err)
	}
}
