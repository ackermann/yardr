package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mckean/yard/internal/store"
	"github.com/mckean/yard/internal/storetest"
)

func mayor(t *testing.T, s store.API) *store.Resident {
	t.Helper()
	r, err := s.PutResident(context.Background(), store.Resident{Name: "mayor", Runner: "herdr",
		Config: map[string]any{"kind": "claude"}, On: []string{"gave_up", "session_blocked"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResidentJournalIsNotWork(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	r := mayor(t, s)
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
	if err := s.ResidentStarted(ctx, "mayor", map[string]any{"agent": "mayor"}); err != nil {
		t.Fatal(err)
	}
	r2, err := s.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr", MaxAge: "6h"}, "me")
	if err != nil || r2.State != "running" || r2.Journal != r.Journal || r2.MaxAge != "6h" {
		t.Fatalf("redefine: %+v %v", r2, err)
	}
	if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", MaxAge: "soon"}, "me"); err == nil {
		t.Fatal("bad duration accepted")
	}
}

func TestJournalCapsAndHandoff(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	mayor(t, s)
	if err := s.Journal(ctx, "mayor", store.NoteJournal, strings.Repeat("x", store.MaxJournalBytes+1), "mayor"); err == nil {
		t.Fatal("oversized journal entry accepted")
	}
	if err := s.Journal(ctx, "mayor", "diary", "x", "mayor"); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if err := s.Journal(ctx, "mayor", store.NoteHandoff, "Next: review webapp-3", "mayor"); err != nil {
		t.Fatal(err)
	}
	r, _ := s.GetResident(ctx, "mayor")
	if r.RestartRequested != "handoff" || r.LastHandoffAt == nil {
		t.Fatalf("handoff did not request a restart: %+v", r)
	}
}

func TestInboxFilteringAndAck(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	mayor(t, s)
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	_ = s.LogEvent(ctx, b.ID, "gave_up", "yard", nil)          // subscribed
	_ = s.LogEvent(ctx, b.ID, "tick", "yard", nil)             // not subscribed
	_ = s.LogEvent(ctx, b.ID, "session_blocked", "mayor", nil) // its own action
	_ = s.Mail(ctx, "mayor", "deploy freeze until Friday", "chris")
	if err := s.Mail(ctx, "nobody", "hi", "chris"); err == nil {
		t.Fatal("mail to an unknown resident accepted")
	}
	page, err := s.Inbox(ctx, "mayor", 10)
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
	if err := s.AckInbox(ctx, "mayor", page.Last); err != nil {
		t.Fatal(err)
	}
	if page, _ = s.Inbox(ctx, "mayor", 10); page.Total != 0 {
		t.Fatalf("inbox after ack = %d", page.Total)
	}
}

func TestPrimeIsBounded(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	mayor(t, s)
	_ = s.ResidentStarted(ctx, "mayor", nil)
	_ = s.Journal(ctx, "mayor", store.NoteJournal, "old context", "mayor")
	_ = s.Journal(ctx, "mayor", store.NoteHandoff, "HANDOFF-1", "mayor")
	for i := 0; i < store.PrimeEntries+5; i++ {
		_ = s.Journal(ctx, "mayor", store.NoteJournal, "entry", "mayor")
	}
	_ = s.ResidentStarted(ctx, "mayor", nil) // the session after the handoff
	p, err := s.ResidentPrime(ctx, "mayor")
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
	_ = s.Mail(ctx, "mayor", "are you there?", "chris")
	_ = s.ResidentStarted(ctx, "mayor", nil)
	p, _ = s.ResidentPrime(ctx, "mayor")
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
	_, _ = s.Remember(ctx, store.NewMemory{Title: "Deploy freeze", Body: "until Friday", Scope: "resident:mayor", Expires: &future}, "me")
	_, _ = s.Remember(ctx, store.NewMemory{Title: "Screenshots on UI beads", Scope: "all"}, "me")
	_, _ = s.Remember(ctx, store.NewMemory{Title: "Builder-only tip", Scope: "group:builders"}, "me")
	_, _ = s.Remember(ctx, store.NewMemory{Title: "Old news", Scope: "all", Expires: &past}, "me")

	m, _ := s.Recall(ctx, "resident:mayor", "")
	if len(m) != 3 { // own + two "all", not the builders' one
		t.Fatalf("mayor sees %d memories: %+v", len(m), m)
	}
	if m, _ = s.Recall(ctx, "resident:mayor", "FRIDAY"); len(m) != 1 || m[0].Title != "Deploy freeze" {
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

func TestBeadListHidesKnowledgeBeads(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	mayor(t, s)
	_, _ = s.Remember(ctx, store.NewMemory{Title: "m"}, "me")
	_, _ = s.CreateBead(ctx, store.NewBead{Title: "work"}, "me")
	all, _ := s.ListBeads(ctx, store.BeadFilter{})
	mem, _ := s.ListBeads(ctx, store.BeadFilter{Type: store.TypeMemory})
	if len(all) != 1 || all[0].Title != "work" || len(mem) != 1 {
		t.Fatalf("list %d (%v), memories %d", len(all), all, len(mem))
	}
}
