package web_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// feedSection is the feed part of a home page: its entries, without the
// overview above them.
func feedSection(t *testing.T, text string) string {
	t.Helper()
	_, s, _ := strings.Cut(squash(text), `<section class="feed"`)
	s, _, _ = strings.Cut(s, `</section>`)
	if s == "" {
		t.Fatal("the page has no feed")
	}
	return s
}

// ownedFixture serves a yard the test can log any event kind on, as
// TestFeedOtherWords does.
func ownedFixture(t *testing.T, as string) (*fixture, *store.Store) {
	t.Helper()
	api, own := storetest.NewOwned(t)
	srv, err := web.NewServer(api, filepath.Join(t.TempDir(), ".depot"), nil)
	must(t, err)
	var token string
	if as != "" {
		token = actAs(t, srv, as)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f := &fixture{t: t, st: api, ts: ts}
	if as != "" {
		f.signIn(srv, token)
	}
	return f, own
}

// Home's feed keeps what a person wants on a phone, each one line, with
// "All activity" carrying the rest to the full feed. A landing stands
// once: its move to the terminal stage is hidden beside it.
func TestHomeFeedKeeps(t *testing.T) {
	t.Parallel()
	// The mail below is to me, and mail goes to its parties alone.
	f, own := ownedFixture(t, "me")
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "line", Base: "origin/main"}, "me"))
	fl, err := store.ParseFlow("new -> review on done\nreview -> merged on done\nmerged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f.st.PutFlow(ctx, *fl, "me")
	must(t, err)
	must(t, f.st.SetFlowPointer(ctx, "line", "", "code", "me"))
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door", Stage: "new", Depot: "line"}, "me")
	must(t, err)
	_, err = f.st.SetHold(ctx, b.ID, true, store.Expect{}, "builder")
	must(t, err)
	_, err = f.st.SetHold(ctx, b.ID, false, store.Expect{}, "me")
	must(t, err)
	must(t, f.st.LogDispatch(ctx, b.ID, "session_blocked", "yard", map[string]any{"session": "s1"}))
	must(t, f.st.LogDispatch(ctx, b.ID, "session_unblocked", "yard", map[string]any{"session": "s1"}))
	must(t, f.st.LogDispatch(ctx, b.ID, "gave_up", "yard", map[string]any{"stage": "new", "failures": 3}))
	must(t, own.LogEvent(ctx, b.ID, "crew_gave_up", "yard", map[string]any{"crew": "brakeman"}))
	must(t, f.st.LogDispatch(ctx, b.ID, "session_harness_error", "yard",
		map[string]any{"session": "s1", "reason": "at_capacity", "line": "the model is at capacity"}))
	must(t, own.LogEvent(ctx, b.ID, "session_failed", "yard", map[string]any{"session": "s1", "reason": "the pane died"}))
	// The yard breathing around them: claims, starts, turns, prompts,
	// plumbing and bookkeeping, none of it for the first screen.
	must(t, own.LogEvent(ctx, b.ID, "started", "yard", map[string]any{"session": "s1"}))
	must(t, own.LogEvent(ctx, b.ID, "claimed", "yard", map[string]any{"group": "crew", "stage": "new"}))
	title := "a door, hung"
	_, err = f.st.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &title}, store.Expect{}, "me")
	must(t, err)
	other, err := f.st.CreateBead(ctx, store.NewBead{Title: "a window", Stage: "new", Depot: "line"}, "me")
	must(t, err)
	must(t, f.st.AddDep(ctx, other.ID, b.ID, "blocks", "", "", "me"))
	for _, kind := range []string{"subscribed", "server_replaced", "crew_started", "crew_stopped", "crew_rotation_asked"} {
		must(t, own.LogEvent(ctx, b.ID, kind, "yard", map[string]any{"crew": "brakeman"}))
	}
	must(t, own.LogEvent(ctx, b.ID, "crew_status", "yard",
		map[string]any{"crew": "brakeman", "status": "idle", "was": "blocked"}))
	must(t, own.LogEvent(ctx, b.ID, "mail", "yard", map[string]any{"to": "me", "text": "hello"}))
	for _, kind := range []string{"workspace_removed", "daemon_replaced", "session_prompted", "session_idle",
		"session_stalled", "move_refused"} {
		must(t, f.st.LogDispatch(ctx, b.ID, kind, "yard", map[string]any{"session": "s1"}))
	}
	_, err = f.st.Done(ctx, b.ID, "done", "", store.Expect{}, "builder")
	must(t, err)
	_, err = f.st.AdvanceNote(ctx, b.ID, "merged", "assembly: merged 70dabcd", store.Expect{}, "assembly")
	must(t, err)

	s := feedSection(t, f.get("/").body)
	for _, want := range []string{
		`<span class="held">held</span>`,
		`<span>no longer held</span>`,
		`<span>session blocked</span>`,
		`<span>session unblocked</span>`,
		`<span>gave up</span>`,
		`<span>crew gave up</span>`,
		`<span class="held">harness stopped on an error: at capacity</span>`,
		`<span class="held">session ended: failed</span>`,
		`<span>noted</span>`,
		`<span class="landed">landed in merged at 70dabcd</span>`,
		`<a class="item older label" href="/?all=1">All activity ›</a>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the feed lacks %q", want)
		}
	}
	for _, not := range []string{
		`<span>created</span>`,
		`claimed by`,
		`session started`,
		`<span>updated</span>`,
		`dep added`,
		`subscribed`,
		`workspace removed`,
		`daemon replaced`,
		`server replaced`,
		`crew started`,
		`crew stopped`,
		`crew rotation asked`,
		`crew status`,
		`session prompted`,
		`session idle`,
		`session stalled`,
		`move refused`,
		`<span>mail`,
		`moved from new to review (done)`,
		// The landing's move, hidden beside it.
		`moved from review to merged`,
		`>Older<`,
		// One line each: no note stands under a row.
		`entry-note`,
	} {
		if strings.Contains(s, not) {
			t.Errorf("the feed has %q", not)
		}
	}
	if n := strings.Count(s, "<article"); n != 10 {
		t.Errorf("the feed shows %d entries, want the 10 kept", n)
	}
	// The stream the page hangs its feed on carries the kept entries.
	if body := f.get("/").body; !strings.Contains(squash(body), `sse-connect="/events?after=`) ||
		!strings.Contains(squash(body), `&amp;format=home`) {
		t.Error("the page's stream is not the home one")
	}

	// The JSON keeps every event, kept or not.
	h := homeJSON(t, f, "/")
	kinds := map[string]bool{}
	for _, e := range h.Events {
		kinds[e.Kind] = true
	}
	for _, k := range []string{"created", "started", "claimed", "updated", "dep_added", "subscribed",
		"workspace_removed", "daemon_replaced", "server_replaced", "crew_started", "crew_stopped",
		"crew_rotation_asked", "crew_status", "session_prompted", "session_idle", "session_stalled",
		"move_refused", "mail", "advanced", "closed"} {
		if !kinds[k] {
			t.Errorf("the JSON lacks a %s event: nothing left it out of the page", k)
		}
	}
	if n := len(h.Events); n <= 10 {
		t.Errorf("the JSON holds %d events, want the full page behind the 10 shown", n)
	}
}

// A move stands on home when it goes into or out of a manual stage, or
// back on failed or changes; every other move is agents doing their jobs.
// A close by hand, a release, a reopen and a firing timer stand.
func TestHomeFeedMoves(t *testing.T) {
	t.Parallel()
	f, own := ownedFixture(t, "")
	ctx := context.Background()
	must(t, f.st.PutDepot(ctx, store.Depot{Name: "line", Base: "origin/main"}, "me"))
	fl, err := store.ParseFlow("new -> review on done | decide\ndecide -> new\n" +
		"review -> approved on done | new on changes\napproved -> new on failed | merged on done\nmerged terminal\n")
	must(t, err)
	fl.Name = "code"
	_, err = f.st.PutFlow(ctx, *fl, "me")
	must(t, err)
	must(t, f.st.SetFlowPointer(ctx, "line", "", "code", "me"))
	must(t, f.st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual"}, "me"))
	_, err = f.st.AddRoute(ctx, store.Route{Stage: "decide", Group: "decisions"}, "me")
	must(t, err)
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door", Stage: "new", Depot: "line"}, "me")
	must(t, err)
	_, err = f.st.AdvanceNote(ctx, b.ID, "decide", "", store.Expect{}, "builder")
	must(t, err)
	_, err = f.st.AdvanceNote(ctx, b.ID, "new", "", store.Expect{}, "me")
	must(t, err)
	_, err = f.st.Done(ctx, b.ID, "done", "", store.Expect{}, "builder")
	must(t, err)
	_, err = f.st.Done(ctx, b.ID, "changes", "", store.Expect{}, "reviewer")
	must(t, err)
	_, err = f.st.Done(ctx, b.ID, "done", "", store.Expect{}, "builder")
	must(t, err)
	_, err = f.st.Done(ctx, b.ID, "done", "", store.Expect{}, "reviewer")
	must(t, err)
	_, err = f.st.Done(ctx, b.ID, "failed", "", store.Expect{}, "assembly")
	must(t, err)
	_, err = f.st.Done(ctx, b.ID, "done", "", store.Expect{}, "builder")
	must(t, err)
	_, err = f.st.Done(ctx, b.ID, "done", "", store.Expect{}, "reviewer")
	must(t, err)
	_, err = f.st.Done(ctx, b.ID, "done", "", store.Expect{}, "assembly")
	must(t, err)
	d, err := f.st.CreateBead(ctx, store.NewBead{Title: "a window", Stage: "new", Depot: "line"}, "me")
	must(t, err)
	// A release changes nothing on an unclaimed bead and logs nothing, so
	// the event is written as the store would write it.
	must(t, own.LogEvent(ctx, d.ID, "released", "me", map[string]any{"reason": "try again"}))
	_, err = f.st.CloseBead(ctx, d.ID, "dropped", store.Expect{}, "me")
	must(t, err)
	_, err = f.st.Reopen(ctx, d.ID, store.Expect{}, "me")
	must(t, err)
	must(t, own.LogEvent(ctx, b.ID, "timer", "yard",
		map[string]any{"by": "yard", "to": "me", "text": "timer: stand up"}))

	s := feedSection(t, f.get("/").body)
	for _, want := range []string{
		`<span>moved from new to decide</span>`,
		`<span>moved from decide to new</span>`,
		`<span>moved from review to new (changes)</span>`,
		`<span>moved from approved to new (failed)</span>`,
		`<span class="landed">landed in merged</span>`,
		`<span>released, to be routed again</span>`,
		`<span>closed: dropped</span>`,
		`<span>reopened</span>`,
		`<span>timer of yard for me</span>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the feed lacks %q", want)
		}
	}
	for _, not := range []string{
		`moved from new to review (done)`,
		`moved from review to approved (done)`,
		`moved from approved to merged`,
		`<span>created</span>`,
	} {
		if strings.Contains(s, not) {
			t.Errorf("the feed has %q", not)
		}
	}
	if n := strings.Count(s, "<article"); n != 9 {
		t.Errorf("the feed shows %d entries, want the 9 kept", n)
	}
}

// Past ten kept events the feed names ten and "All activity" carries the
// rest to the full feed; with nothing kept and nothing at all the feed
// says so.
func TestHomeFeedTen(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	b := bead(t, f.st, "a door")
	for i := range 12 {
		must(t, f.st.AddNote(ctx, b.ID, fmt.Sprint("remark ", i), "me"))
	}
	s := feedSection(t, f.get("/").body)
	if n := strings.Count(s, "<article"); n != 10 {
		t.Errorf("the feed shows %d entries, want 10", n)
	}
	if !strings.Contains(s, `href="/?all=1">All activity ›</a>`) {
		t.Error("the feed lacks All activity behind its 10")
	}
	// The 10 shown are the 10 newest, each row carrying its seq as id.
	h := homeJSON(t, f, "/")
	var want []string
	for _, e := range h.Events[:10] {
		want = append(want, fmt.Sprintf(`id="e%d"`, e.Seq))
	}
	for _, id := range want {
		if !strings.Contains(s, id) {
			t.Errorf("the feed lacks the row %s of the 10 newest", id)
		}
	}
	for _, e := range h.Events[10:] {
		if strings.Contains(s, fmt.Sprintf(`id="e%d"`, e.Seq)) {
			t.Errorf("the feed shows e%d past its 10", e.Seq)
		}
	}
	all := feedSection(t, f.get("/?all=1").body)
	if n := strings.Count(all, "<article"); n != 13 {
		t.Errorf("the full feed shows %d entries, want the 12 notes and the creation", n)
	}
	if strings.Contains(all, "All activity") {
		t.Error("the full feed names All activity, and it is there")
	}

	empty := newFixture(t)
	s = feedSection(t, empty.get("/").body)
	if !strings.Contains(s, "Nothing has happened yet.") || strings.Contains(s, "All activity") {
		t.Errorf("the empty feed says %q", s)
	}
}

// ?all=1 changes the page, never the JSON: / and /?all=1 carry the same
// full events (TestFeedHTML tries the pages).
func TestHomeFeedAllJSON(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	landing(t, f.st)
	ctx := context.Background()
	for i := range 60 {
		_, err := f.st.CreateBead(ctx, store.NewBead{Title: fmt.Sprint("bead ", i), Stage: "new"}, "me")
		must(t, err)
	}
	plain, full := homeJSON(t, f, "/"), homeJSON(t, f, "/?all=1")
	if fmt.Sprint(plain.Events) != fmt.Sprint(full.Events) {
		t.Error("the JSON events differ between / and /?all=1")
	}
	if len(plain.Events) != 50 {
		t.Errorf("the JSON holds %d events, want the full 50", len(plain.Events))
	}
}

// The home stream puts only a kept event's fragment on top and passes the
// rest as their ids alone, so the client's cursor still passes them. A
// kept fragment is a message: the column still re-reads on it.
func TestHomeStream(t *testing.T) {
	t.Parallel()
	f, own := ownedFixture(t, "")
	ctx := context.Background()
	b := bead(t, f.st, "a door")
	head, err := f.st.Head(ctx)
	must(t, err)
	s := f.stream(t, fmt.Sprintf("/events?after=%d&format=home", head))
	must(t, f.st.AddNote(ctx, b.ID, "two\nlines", "me"))
	c := bead(t, f.st, "a window")
	_, err = f.st.SetHold(ctx, c.ID, true, store.Expect{}, "me")
	must(t, err)
	must(t, own.LogEvent(ctx, c.ID, "advanced", "builder",
		map[string]any{"from": "new", "to": "review"}))
	must(t, own.LogEvent(ctx, c.ID, "advanced", "assembly",
		map[string]any{"from": "review", "to": "merged"}))
	must(t, own.LogEvent(ctx, c.ID, "closed", "assembly", map[string]any{"reason": "merged"}))

	if fr := s.next(t); !strings.Contains(fr.data, `<span>noted</span>`) {
		t.Errorf("the note came as %q, want its fragment", fr.data)
	}
	if fr := s.next(t); fr.data != "" {
		t.Errorf("the creation came as %q, want its id alone", fr.data)
	}
	if fr := s.next(t); !strings.Contains(fr.data, `<span class="held">held</span>`) {
		t.Errorf("the hold came as %q, want its fragment", fr.data)
	}
	if fr := s.next(t); fr.data != "" {
		t.Errorf("the routine move came as %q, want its id alone", fr.data)
	}
	if fr := s.next(t); fr.data != "" {
		t.Errorf("the landing's move came as %q, want its id alone", fr.data)
	}
	if fr := s.next(t); !strings.Contains(fr.data, `<span class="landed">landed in merged</span>`) {
		t.Errorf("the landing came as %q, want its fragment", fr.data)
	}
	s.quiet(t)
}

// Home's feed leaves out a note by the bead's own agent or the landing and
// keeps one by a user or a crew member. ?all=1, the JSON and the bead's own
// page keep every note, as today.
func TestHomeFeedNotes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	must(t, f.st.PutGroup(ctx, store.Group{Name: "yardr-refinery", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"))
	_, err := f.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me")
	must(t, err)
	agent := bead(t, f.st, "the agents bead")
	must(t, f.st.AddNote(ctx, agent.ID, "an agent line", agent.ID+"-new"))
	landed := bead(t, f.st, "the landed bead")
	must(t, f.st.AddNote(ctx, landed.ID, "a landing line", "yardr-refinery"))
	user := bead(t, f.st, "the users bead")
	must(t, f.st.AddNote(ctx, user.ID, "a user line", "me"))
	crew := bead(t, f.st, "the crews bead")
	must(t, f.st.AddNote(ctx, crew.ID, "a crew line", "yardmaster"))

	s := feedSection(t, f.get("/").body)
	if n := strings.Count(s, "<article"); n != 2 {
		t.Errorf("the feed shows %d entries, want the user and crew notes", n)
	}
	for _, id := range []string{user.ID, crew.ID} {
		if !strings.Contains(s, `<a class="bead" href="/beads/`+id+`">`) {
			t.Errorf("the feed lacks the note on %s", id)
		}
	}
	for _, id := range []string{agent.ID, landed.ID} {
		if strings.Contains(s, `/beads/`+id) {
			t.Errorf("the feed shows the note on %s", id)
		}
	}
	if !strings.Contains(s, `href="/?all=1">All activity ›</a>`) {
		t.Error("the feed names no All activity behind its two")
	}

	all := feedSection(t, f.get("/?all=1").body)
	if n := strings.Count(all, "<span>noted</span>"); n != 4 {
		t.Errorf("the full feed shows %d notes, want every one", n)
	}
	h := homeJSON(t, f, "/")
	var kinds int
	for _, e := range h.Events {
		if e.Kind == "noted" {
			kinds++
		}
	}
	if kinds != 4 {
		t.Errorf("the JSON holds %d notes, want every one", kinds)
	}
	for _, c := range []struct{ id, note string }{
		{agent.ID, "an agent line"},
		{landed.ID, "a landing line"},
	} {
		if body := f.get("/beads/" + c.id).body; !strings.Contains(body, c.note) {
			t.Errorf("the bead's page lacks its %q", c.note)
		}
	}
}
