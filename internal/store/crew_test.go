package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func yardmaster(t *testing.T, s store.API) *store.CrewMember {
	t.Helper()
	r, err := s.PutCrew(context.Background(), store.CrewMember{Name: "yardmaster", Runner: "herdr",
		Config: map[string]any{"kind": "claude"}, On: []string{"gave_up", "session_blocked"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCrewJournalIsNotWork(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r := yardmaster(t, s)
	j, err := s.GetBead(ctx, r.Journal)
	if err != nil || j.Type != store.TypeJournal {
		t.Fatalf("journal bead: %+v %v", j, err)
	}
	ready, _ := s.Ready(ctx)
	disp, _ := s.Dispatchable(ctx)
	if len(ready) != 0 || len(disp) != 0 {
		t.Fatalf("journal counted as work: ready %d dispatchable %d", len(ready), len(disp))
	}
	// Redefining keeps runtime state and the journal.
	if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "yardmaster"}); err != nil {
		t.Fatal(err)
	}
	r2, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", MaxAge: "6h"}, "me")
	if err != nil || r2.State != "running" || r2.Journal != r.Journal || r2.MaxAge != "6h" {
		t.Fatalf("redefine: %+v %v", r2, err)
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", MaxAge: "soon"}, "me"); err == nil {
		t.Fatal("bad duration accepted")
	}
}

// A session that was gone keeps its handle in Left, in the write that
// stops it; one that yardr stopped leaves none.
func TestCrewGoneKeepsItsHandle(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	yardmaster(t, s)
	left := func() string {
		t.Helper()
		r, err := s.GetCrew(ctx, "yardmaster")
		if err != nil || r.Live() || r.Handle != nil {
			t.Fatalf("crew member %+v, %v", r, err)
		}
		var ws []string
		for _, h := range r.Left {
			ws = append(ws, h["workspace"].(string))
		}
		return strings.Join(ws, " ")
	}
	for _, ws := range []string{"w1", "w2", "w3"} {
		if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "yardmaster", "workspace": ws}); err != nil {
			t.Fatal(err)
		}
		end := s.CrewGone
		if ws == "w2" {
			end = s.CrewStopped
		}
		if err := end(ctx, "yardmaster", "the session is gone"); err != nil {
			t.Fatal(err)
		}
	}
	if got := left(); got != "w1 w3" {
		t.Fatalf("left = %q, want w1 w3", got)
	}
	// Not running: nothing to keep, and no second event.
	if err := s.CrewGone(ctx, "yardmaster", "again"); err != nil || left() != "w1 w3" {
		t.Fatalf("gone twice: left %q, %v", left(), err)
	}
	evs, _ := s.Events(ctx, "yardmaster-journal", 100)
	stopped := 0
	for _, e := range evs {
		if e.Kind == "crew_stopped" {
			stopped++
		}
	}
	if stopped != 3 {
		t.Fatalf("%d crew_stopped events, want 3", stopped)
	}
	if err := s.CrewLeft(ctx, "yardmaster", []map[string]any{{"workspace": "w3"}}); err != nil || left() != "w3" {
		t.Fatalf("after CrewLeft: left %q, %v", left(), err)
	}
	// Redefining the crew member keeps them.
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil || left() != "w3" {
		t.Fatalf("after a redefinition: left %q, %v", left(), err)
	}
}

func TestJournalCapsAndHandoff(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	yardmaster(t, s)
	if err := s.Journal(ctx, "yardmaster", store.NoteJournal, strings.Repeat("x", store.MaxJournalBytes+1), "yardmaster"); err == nil {
		t.Fatal("oversized journal entry accepted")
	}
	if err := s.Journal(ctx, "yardmaster", "diary", "x", "yardmaster"); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if err := s.Journal(ctx, "yardmaster", store.NoteHandoff, "Next: review webapp-3", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	r, _ := s.GetCrew(ctx, "yardmaster")
	if r.RestartRequested != "handoff" || r.LastHandoffAt == nil {
		t.Fatalf("handoff did not request a restart: %+v", r)
	}
}

func TestInboxFilteringAndAck(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	yardmaster(t, s)
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	_ = s.LogDispatch(ctx, b.ID, "gave_up", "yard", nil)               // subscribed
	_ = s.LogDispatch(ctx, b.ID, "unrouted", "yard", nil)              // not subscribed
	_ = s.LogDispatch(ctx, b.ID, "session_blocked", "yardmaster", nil) // its own action
	_ = s.Mail(ctx, "yardmaster", "deploy freeze until Friday", "chris")
	if err := s.Mail(ctx, "nobody", "hi", "chris"); err == nil {
		t.Fatal("mail to an unknown crew member accepted")
	}
	page, err := s.Inbox(ctx, "yardmaster", 10)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range page.Events {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "gave_up,mail" || page.Total != 2 {
		t.Fatalf("inbox = %v (total %d)", kinds, page.Total)
	}
	if err := s.AckInbox(ctx, "yardmaster", page.Last); err != nil {
		t.Fatal(err)
	}
	if page, _ = s.Inbox(ctx, "yardmaster", 10); page.Total != 0 {
		t.Fatalf("inbox after ack = %d", page.Total)
	}
}

func TestPrimeIsBounded(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	yardmaster(t, s)
	_ = s.CrewStarted(ctx, "yardmaster", nil)
	_ = s.Journal(ctx, "yardmaster", store.NoteJournal, "old context", "yardmaster")
	_ = s.Journal(ctx, "yardmaster", store.NoteHandoff, "HANDOFF-1", "yardmaster")
	for i := 0; i < store.PrimeEntries+5; i++ {
		_ = s.Journal(ctx, "yardmaster", store.NoteJournal, "entry", "yardmaster")
	}
	_ = s.CrewStarted(ctx, "yardmaster", nil) // the session after the handoff
	p, err := s.CrewPrime(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if p.Handoff == nil || p.Handoff.Text != "HANDOFF-1" {
		t.Fatalf("handoff = %+v", p.Handoff)
	}
	if len(p.Since) != store.PrimeEntries || p.Omitted != 5 || p.NoHandoff {
		t.Fatalf("since %d omitted %d nohandoff %v", len(p.Since), p.Omitted, p.NoHandoff)
	}

	// That session ends without a handoff: the next prime falls back to
	// what happened since the last handoff.
	time.Sleep(5 * time.Millisecond)
	_ = s.Mail(ctx, "yardmaster", "are you there?", "chris")
	_ = s.CrewStarted(ctx, "yardmaster", nil)
	p, _ = s.CrewPrime(ctx, "yardmaster")
	if !p.NoHandoff || p.EventCounts["mail"] != 1 || len(p.Recent) == 0 || len(p.Recent) > store.PrimeFallback {
		t.Fatalf("fallback: nohandoff %v counts %v recent %d", p.NoHandoff, p.EventCounts, len(p.Recent))
	}
	if p.Handoff == nil || p.Handoff.Text != "HANDOFF-1" {
		t.Fatal("the last handoff must still be shown")
	}
}

func TestMemoriesScopeSearchAndExpiry(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Hour)
	_, _ = s.Remember(ctx, store.NewMemory{Title: "Deploy freeze", Body: "until Friday", Scope: "crew:yardmaster", Expires: &future}, "me")
	_, _ = s.Remember(ctx, store.NewMemory{Title: "Screenshots on UI beads", Scope: "all"}, "me")
	_, _ = s.Remember(ctx, store.NewMemory{Title: "Builder-only tip", Scope: "group:builders"}, "me")
	_, _ = s.Remember(ctx, store.NewMemory{Title: "Old news", Scope: "all", Expires: &past}, "me")

	m, _ := s.Recall(ctx, "crew:yardmaster", "")
	if len(m) != 3 { // own + two "all", not the builders' one
		t.Fatalf("yardmaster sees %d memories: %+v", len(m), m)
	}
	if m, _ = s.Recall(ctx, "crew:yardmaster", "FRIDAY"); len(m) != 1 || m[0].Title != "Deploy freeze" {
		t.Fatalf("search = %+v", m)
	}
	ready, _ := s.Ready(ctx)
	if len(ready) != 0 {
		t.Fatal("memories counted as work")
	}
	if n, err := s.ExpireMemories(ctx); err != nil || n != 1 {
		t.Fatalf("expired %d (%v)", n, err)
	}
	if m, _ = s.Recall(ctx, "", ""); len(m) != 3 {
		t.Fatalf("after expiry %d live memories", len(m))
	}
}

// The prime carries the most recently written memory titles, newest
// first, and says how many it leaves out; rewriting a memory moves it back
// to the front.
func TestPrimeMemoriesAreCapped(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	yardmaster(t, s)
	for i := 0; i < store.PrimeMemories+3; i++ {
		if _, err := s.Remember(ctx, store.NewMemory{Title: fmt.Sprintf("memory %02d", i)}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	oldest, err := s.Recall(ctx, "crew:yardmaster", "memory 00")
	if err != nil || len(oldest) != 1 {
		t.Fatalf("recall memory 00 = %+v, %v", oldest, err)
	}
	time.Sleep(2 * time.Millisecond) // a strictly later write
	body := "rewritten"
	if _, err := s.UpdateBead(ctx, oldest[0].ID, store.BeadPatch{Body: &body}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	p, err := s.CrewPrime(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Memories) != store.PrimeMemories || p.MemoriesOmitted != 3 {
		t.Fatalf("memories %d omitted %d", len(p.Memories), p.MemoriesOmitted)
	}
	if p.Memories[0].Title != "memory 00" {
		t.Fatalf("first = %q, want the rewritten memory", p.Memories[0].Title)
	}
	kept := map[string]bool{}
	for _, m := range p.Memories {
		kept[m.Title] = true
	}
	for i := 0; i < store.PrimeMemories+3; i++ {
		want := i == 0 || i >= 4
		if kept[fmt.Sprintf("memory %02d", i)] != want {
			t.Fatalf("memory %02d kept = %v, want %v", i, !want, want)
		}
	}
}

// yardr recall <words> finds journal entries too: the journal read back,
// which the prime's recent entries and crew show's last lines are not.
func TestRecallFindsJournalEntries(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	yardmaster(t, s)
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Journal(ctx, "yardmaster", store.NoteJournal, "Deploys are frozen until Friday", "yardmaster"))
	must(s.Journal(ctx, "yardmaster", store.NoteHandoff, "Handing off: the deploy freeze holds", "yardmaster"))
	must(s.Journal(ctx, "clerk", store.NoteJournal, "Filed the deploy paperwork", "clerk"))

	hits, err := s.RecallJournal(ctx, "crew:yardmaster", "DEPLOY")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Crew != "yardmaster" || hits[1].Crew != "yardmaster" {
		t.Fatalf("yardmaster's hits = %+v", hits)
	}
	if hits[0].Note.Kind != store.NoteHandoff || !strings.Contains(hits[0].Note.Text, "freeze holds") {
		t.Fatalf("newest first = %+v", hits[0])
	}
	if hits[1].Note.Text != "Deploys are frozen until Friday" {
		t.Fatalf("oldest = %+v", hits[1])
	}
	if hits, _ := s.RecallJournal(ctx, "", "deploy"); len(hits) != 3 {
		t.Fatalf("every journal = %d hits", len(hits))
	}
	if hits, _ := s.RecallJournal(ctx, "all", "deploy"); len(hits) != 3 {
		t.Fatalf("scope all = %d hits", len(hits))
	}
	if hits, _ := s.RecallJournal(ctx, "crew:clerk", "deploy"); len(hits) != 1 || hits[0].Crew != "clerk" {
		t.Fatalf("clerk's = %+v", hits)
	}
	for _, tc := range []struct{ scope, query string }{
		{"crew:yardmaster", "nothing like this"},
		{"crew:yardmaster", ""},
		{"crew:nobody", "deploy"},
	} {
		if hits, err := s.RecallJournal(ctx, tc.scope, tc.query); err != nil || len(hits) != 0 {
			t.Fatalf("recall %q %q = %+v, %v", tc.scope, tc.query, hits, err)
		}
	}
}

func TestBeadListHidesKnowledgeBeads(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	yardmaster(t, s)
	_, _ = s.Remember(ctx, store.NewMemory{Title: "m"}, "me")
	_, _ = s.CreateBead(ctx, store.NewBead{Title: "work"}, "me")
	all, _ := s.ListBeads(ctx, store.BeadFilter{})
	mem, _ := s.ListBeads(ctx, store.BeadFilter{Type: store.TypeMemory})
	if len(all) != 1 || all[0].Title != "work" || len(mem) != 1 {
		t.Fatalf("list %d (%v), memories %d", len(all), all, len(mem))
	}
}

// An event already in the log keeps the kind an earlier release gave it,
// and is read as one of the kind new events carry. A peer still on v0.6.0
// refuses mail to nobody in the word it had then, which is read like the
// one this build sends.
func TestAnOldEventKindNamesTheNew(t *testing.T) {
	for old, want := range map[string]string{"resident_gave_up": "crew_gave_up", "resident_status": "crew_status",
		"epic_branched": "train_branched", "part_without_epic": "wagon_without_train",
		"rig_updated": "depot_updated", "rig_removed": "depot_removed", "bad_rig": "bad_depot",
		"crew_started": "crew_started", "held": "held"} {
		if got := store.EventKind(old); got != want {
			t.Errorf("EventKind(%s) = %s, want %s", old, got, want)
		}
	}
	for _, reason := range []string{"no_such_resident", "no_such_crew"} {
		if got := store.PeerRefusal("north", store.PeerMail, reason); got != "it has no such crew member" {
			t.Errorf("PeerRefusal(%s) = %q", reason, got)
		}
	}
	if store.RefusedNoCrew != "no_such_crew" {
		t.Errorf("the refusal on the link is %q, want no_such_crew", store.RefusedNoCrew)
	}
}
