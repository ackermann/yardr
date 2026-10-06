package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// threads lists the crew member's threads as "source/thread how".
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
	member(t, s, "inv", false, sub(store.ScopeInvolved))
	member(t, s, "ment", false, sub(store.ScopeMentions))
	member(t, s, "all", false, sub(store.ScopeAll))
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
	// Following needs no identity: it is the crew member's own say-so.
	member(t, s, "noid-inv", true, store.Subscription{Source: "linear", Scope: store.ScopeInvolved})
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
		t.Fatalf("unknown crew member: %v", err)
	}
	if _, err := s.Threads(ctx, "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown crew member's threads: %v", err)
	}

	// Redefining keeps participation; deleting forgets it, so a new
	// crew member under the name starts with none.
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "noid-inv", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := threads(t, s, "noid-inv"); len(got) != 1 {
		t.Fatalf("after redefine: %v", got)
	}
	if err := s.DeleteCrew(ctx, "noid-inv", "", "me"); err != nil {
		t.Fatal(err)
	}
	member(t, s, "noid-inv", true, store.Subscription{Source: "linear", Scope: store.ScopeInvolved})
	if got := threads(t, s, "noid-inv"); len(got) != 0 {
		t.Fatalf("recreated crew member inherits threads: %v", got)
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
	member(t, s, "inv", false)
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
		t.Fatalf("unknown crew member: %v", err)
	}
	// A crew member deleted and made again starts with no replies.
	if err := s.DeleteCrew(ctx, "inv", "", "me"); err != nil {
		t.Fatal(err)
	}
	member(t, s, "inv", false)
	fresh, err := s.Replying(ctx, "inv", 7, "", "hello")
	if err != nil || fresh.Key == rp.Key || fresh.Attempts != 1 {
		t.Fatalf("after recreating the crew member: %+v, %v", fresh, err)
	}
}

// A reply makes the crew member a participant at once, with a replied event
// on its journal; the webhook's echo of the reply changes nothing.
func TestReplied(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	member(t, s, "inv", false, store.Subscription{Source: "linear", Scope: store.ScopeInvolved})
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
	r, _ := s.GetCrew(ctx, "inv")
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
		t.Fatalf("unknown crew member: %v", err)
	}
	if err := s.Replied(ctx, "inv", store.Reply{Source: "linear"}, "inv"); err == nil {
		t.Fatal("replied without a thread")
	}
}

// A crew member opens threads only in the sources its definition allows.
// Opening puts the thread on record with a key before it is opened: the
// same source, place, title and body gets the same key, anything else its
// own. Opened makes the thread the crew member's at once, so what others say
// there reaches an involved subscription with no mention.
func TestOpening(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	member(t, s, "inv", false, store.Subscription{Source: "linear", Scope: store.ScopeInvolved})
	head, _ := s.Head(ctx)

	_, err := s.Opening(ctx, "inv", "linear", "YRD", "Web view", "body")
	if err == nil || !strings.Contains(err.Error(), "may not open threads in linear") ||
		!strings.Contains(err.Error(), "yardr crew set inv open_threads=linear") {
		t.Fatalf("not allowed: %v", err)
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("the refusal logged %d events", now-head)
	}
	allow := func(v any) {
		t.Helper()
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: "inv", Runner: "herdr", Config: map[string]any{store.OpenThreads: v}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	// Allowed in another source only: still refused, and told what to set.
	allow("github")
	if _, err := s.Opening(ctx, "inv", "linear", "YRD", "Web view", "body"); err == nil ||
		!strings.Contains(err.Error(), "yardr crew set inv open_threads=github,linear") {
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
		URL: "https://l/YRD-9", Key: rp.Key}
	if err := s.Opened(ctx, "inv", opened, "inv"); err != nil {
		t.Fatal(err)
	}
	evs, err := s.EventsSince(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1]
	if last.Kind != "thread_opened" || last.Actor != "inv" || last.Data["thread"] != rp.Key || last.Data["in"] != "YRD" ||
		last.Data["title"] != "Web view" || last.Data["url"] != "https://l/YRD-9" || last.Data["message"] != nil {
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
		t.Fatalf("unknown crew member: %v", err)
	}
}

// AllowOpening adds sources to what a crew member may open threads in and
// takes none away, whatever form the setting has; only what is new is
// logged, with the pack that asked and who granted it.
func TestAllowOpeningAddsOnly(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.AllowOpening(ctx, "inv", []string{"linear"}, "starter", "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a crew member the yard lacks: %v", err)
	}
	def := store.CrewMember{Name: "inv", Runner: "herdr", Dir: "/work", On: []string{"held"},
		Config: map[string]any{"kind": "claude", store.OpenThreads: []any{"github"}}}
	if _, err := s.PutCrew(ctx, def, "me"); err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	added, err := s.AllowOpening(ctx, "inv", []string{"linear", "github", "linear"}, "starter", "jo")
	if err != nil || !reflect.DeepEqual(added, []string{"linear"}) {
		t.Fatalf("added %v, %v", added, err)
	}
	r, err := s.GetCrew(ctx, "inv")
	if err != nil || !reflect.DeepEqual(r.MayOpen(), []string{"github", "linear"}) {
		t.Fatalf("may open %v, %v", r.MayOpen(), err)
	}
	// The rest of the definition is as it was.
	if r.Config["kind"] != "claude" || r.Dir != "/work" || !reflect.DeepEqual(r.On, []string{"held"}) {
		t.Fatalf("the definition changed: %+v", r)
	}
	evs, _ := s.EventsSince(ctx, head, 0)
	if len(evs) != 1 || evs[0].Kind != "open_threads_granted" || evs[0].Actor != "jo" || evs[0].Data["pack"] != "starter" ||
		evs[0].Data["crew"] != "inv" || fmt.Sprint(evs[0].Data["sources"]) != "[linear]" {
		t.Fatalf("events = %+v", evs)
	}
	// Again, and with nothing new: nothing is written.
	head, _ = s.Head(ctx)
	if added, err := s.AllowOpening(ctx, "inv", []string{"github", "linear"}, "starter", "jo"); err != nil || len(added) != 0 {
		t.Fatalf("again: %v, %v", added, err)
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("granting nothing new logged %d events", now-head)
	}
	if after, _ := s.GetCrew(ctx, "inv"); after.Revision != r.Revision {
		t.Fatal("granting nothing new changed the crew member's revision")
	}
	if _, err := s.AllowOpening(ctx, "inv", []string{"a,b"}, "starter", "jo"); err == nil {
		t.Fatal("a source with a comma was taken")
	}
}

// A crew member sets the status of a thread only in the sources its definition
// allows, and only in a thread it takes part in. SettingState says so before
// the source is called and writes nothing; StateSet refuses the same and
// otherwise logs thread_state on the crew member's journal. The delivery its
// own change causes does not reach it.
func TestSettingState(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	member(t, s, "inv", false, store.Subscription{Source: "linear", Scope: store.ScopeInvolved})
	if err := s.FollowThread(ctx, "inv", "linear", "T", "me"); err != nil {
		t.Fatal(err)
	}
	ts := store.ThreadState{Source: "linear", Thread: "T", Title: "Web view", URL: "https://linear.app/acme/issue/YRD-1",
		State: "review", Status: "In Progress", Was: "Todo", Instead: "started"}
	allow := func(v any) {
		t.Helper()
		r, err := s.GetCrew(ctx, "inv")
		if err != nil {
			t.Fatal(err)
		}
		r.Config, r.Revision = map[string]any{}, ""
		if v != nil {
			r.Config[store.SetState] = v
		}
		if _, err := s.PutCrew(ctx, *r, "me"); err != nil {
			t.Fatal(err)
		}
	}
	head, _ := s.Head(ctx)
	refused := func(thread ...string) error {
		t.Helper()
		_, err := s.SettingState(ctx, "inv", thread[0], thread[1])
		if err == nil {
			t.Fatalf("%v: allowed", thread)
		}
		set := ts
		set.Source, set.Thread = thread[0], thread[1]
		if again := s.StateSet(ctx, "inv", set, "inv"); again == nil || again.Error() != err.Error() {
			t.Fatalf("%v: StateSet says %v, SettingState %v", thread, again, err)
		}
		if now, _ := s.Head(ctx); now != head {
			t.Fatalf("%v: the refusal logged %d events", thread, now-head)
		}
		return err
	}

	// Off unless set, and the refusal says what to set.
	err := refused("linear", "T")
	if !strings.Contains(err.Error(), "crew member inv may not set the status of threads in linear") ||
		!strings.Contains(err.Error(), "yardr crew set inv set_state=linear") {
		t.Fatalf("not allowed: %v", err)
	}
	// Opening threads is another setting, and another source no help.
	r, _ := s.GetCrew(ctx, "inv")
	r.Config = map[string]any{store.OpenThreads: "linear", store.SetState: "github"}
	if _, err := s.PutCrew(ctx, *r, "me"); err != nil {
		t.Fatal(err)
	}
	head, _ = s.Head(ctx)
	if err := refused("linear", "T"); !strings.Contains(err.Error(), "yardr crew set inv set_state=github,linear") {
		t.Fatalf("allowed elsewhere: %v", err)
	}
	if r, _ := s.GetCrew(ctx, "inv"); !reflect.DeepEqual(r.MaySetState(), []string{"github"}) || !reflect.DeepEqual(r.MayOpen(), []string{"linear"}) {
		t.Fatalf("may set %v, may open %v", r.MaySetState(), r.MayOpen())
	}

	for _, v := range []any{"linear", " github , linear", []any{"linear", "github"}} {
		allow(v)
		p, err := s.SettingState(ctx, "inv", "linear", "T")
		if err != nil || p.Thread != "T" || p.How != store.ParticipantFollowed {
			t.Fatalf("set_state=%v: %+v, %v", v, p, err)
		}
	}
	head, _ = s.Head(ctx)
	// Allowed in the source, but no part in the thread.
	for _, thread := range [][]string{{"linear", "U"}, {"github", "T"}} {
		err := refused(thread...)
		if want := fmt.Sprintf("crew member inv takes no part in %s thread %s", thread[0], thread[1]); !strings.Contains(err.Error(), want) ||
			!strings.Contains(err.Error(), fmt.Sprintf("yardr thread follow inv --source %s %s", thread[0], thread[1])) {
			t.Fatalf("%v: %v", thread, err)
		}
	}
	for _, thread := range [][]string{{"", "T"}, {"linear", ""}} {
		if err := refused(thread...); !strings.Contains(err.Error(), "source and thread are required") {
			t.Fatalf("%v: %v", thread, err)
		}
	}
	if _, err := s.SettingState(ctx, "ghost", "linear", "T"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown crew member: %v", err)
	}
	if err := s.StateSet(ctx, "ghost", ts, "ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown crew member: %v", err)
	}
	for _, bad := range []store.ThreadState{{Source: "linear", Thread: "T", State: "done"}, {Source: "linear", Thread: "T", Status: "Done"}} {
		if err := s.StateSet(ctx, "inv", bad, "inv"); err == nil {
			t.Fatalf("%+v recorded", bad)
		}
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("the refusals logged %d events", now-head)
	}

	// The operator speaks as the crew member: the event names both.
	if err := s.StateSet(ctx, "inv", ts, "jo"); err != nil {
		t.Fatal(err)
	}
	evs, _ := s.EventsSince(ctx, head, 0)
	inv, _ := s.GetCrew(ctx, "inv")
	want := map[string]any{"crew": "inv", "source": "linear", "thread": "T", "state": "review", "status": "In Progress",
		"was": "Todo", "instead": "started", "title": "Web view", "url": "https://linear.app/acme/issue/YRD-1"}
	if len(evs) != 1 || evs[0].Kind != "thread_state" || evs[0].Actor != "jo" || evs[0].Bead == nil || *evs[0].Bead != inv.Journal || !reflect.DeepEqual(evs[0].Data, want) {
		t.Fatalf("events = %+v\nwant data %v", evs, want)
	}
	if got := threads(t, s, "inv"); !reflect.DeepEqual(got, []string{"linear/T followed"}) {
		t.Fatalf("threads %v", got)
	}

	// Linear tells of the change: the crew member is its actor, so it is not
	// told; the same change by someone else reaches it as a participant.
	change := hooks.Envelope{Source: "linear", Type: "issue", Action: "update", Actor: hooks.Party{ID: "inv-id"},
		Thread: hooks.Thread{ID: "T"}, Text: "changed stateId", Entity: "T"}
	if got := deliveries(t, s, own, change); len(got) != 0 {
		t.Fatalf("its own change reached %v", got)
	}
	change.Actor = hooks.Party{ID: "u-jane"}
	if got := deliveries(t, s, own, change); got["inv"] != store.ReasonParticipant {
		t.Fatalf("someone else's change: %v", got)
	}
}
