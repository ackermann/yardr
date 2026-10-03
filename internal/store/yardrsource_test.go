package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// yardrSub is a subscription to yardr's own bead events.
func yardrSub(scope string, where ...string) store.Subscription {
	sub := store.Subscription{Source: store.SourceYardr, Scope: scope, Where: map[string]string{}}
	for _, kv := range where {
		k, v, _ := strings.Cut(kv, "=")
		sub.Where[k] = v
	}
	return sub
}

// yardrRig is the yard rig with yardFlow, residents with no subscriptions
// yet, and a bead in stage new.
func yardrRig(t *testing.T, residents ...string) (store.API, context.Context, *store.Bead) {
	t.Helper()
	s, ctx := flowRig(t)
	putFlow(t, s, "yard", "", yardFlow)
	for _, r := range residents {
		if _, err := s.PutResident(ctx, store.Resident{Name: r, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "work", Rig: "yard", Stage: "new", Labels: []string{"ui"}}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx, b
}

func subscribe(t *testing.T, s store.API, name string, sub store.Subscription) {
	t.Helper()
	if err := s.AddSubscription(context.Background(), name, sub, "me"); err != nil {
		t.Fatal(err)
	}
}

// heard lists, oldest first, "<kind>:<reason>" for each of the bead's
// events that reached resident through its subscriptions.
func heard(t *testing.T, s store.API, bead, resident string) []string {
	t.Helper()
	evs, err := s.Events(context.Background(), bead, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if why := store.Reason(e, resident); why != "" {
			out = append(out, e.Kind+":"+why)
		}
	}
	return out
}

func wantHeard(t *testing.T, s store.API, bead, resident string, want ...string) {
	t.Helper()
	if got := heard(t, s, bead, resident); !slices.Equal(got, want) {
		t.Errorf("%s heard %v, want %v", resident, got, want)
	}
}

func TestYardrPerBeadAndPerStage(t *testing.T) {
	s, ctx, b := yardrRig(t, "watcher", "stager", "other")
	other, err := s.CreateBead(ctx, store.NewBead{Title: "other", Rig: "yard", Stage: "new"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	subscribe(t, s, "watcher", yardrSub(store.ScopeAll, "bead="+b.ID))
	subscribe(t, s, "stager", yardrSub(store.ScopeAll, "stage=review"))
	subscribe(t, s, "other", yardrSub(store.ScopeAll, "kind=advanced", "from=review"))

	if err := s.AddNote(ctx, b.ID, "looking", "jane"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{b.ID, other.ID} {
		if _, err := s.Advance(ctx, id, "review", "jane"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Advance(ctx, b.ID, "approved", "jane"); err != nil {
		t.Fatal(err)
	}

	wantHeard(t, s, b.ID, "watcher", "noted:all", "advanced:all", "advanced:all")
	wantHeard(t, s, other.ID, "watcher")
	wantHeard(t, s, b.ID, "stager", "advanced:all") // entering review, not leaving it
	wantHeard(t, s, other.ID, "stager", "advanced:all")
	wantHeard(t, s, b.ID, "other", "advanced:all")
	wantHeard(t, s, other.ID, "other")
}

func TestYardrLabelRigTypeFilters(t *testing.T) {
	s, ctx, b := yardrRig(t, "ui", "yard", "bugs")
	if err := s.PutRig(ctx, store.Rig{Name: "web", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	web, err := s.CreateBead(ctx, store.NewBead{Title: "web", Rig: "web", Type: "bug", Labels: []string{"ui"}}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	subscribe(t, s, "ui", yardrSub(store.ScopeAll, "label=ui", "kind=noted"))
	subscribe(t, s, "yard", yardrSub(store.ScopeAll, "rig=yard", "kind=noted"))
	subscribe(t, s, "bugs", yardrSub(store.ScopeAll, "type=bug", "kind=noted"))
	for _, id := range []string{b.ID, web.ID} {
		if err := s.AddNote(ctx, id, "hi", "jane"); err != nil {
			t.Fatal(err)
		}
	}
	wantHeard(t, s, b.ID, "ui", "noted:all")
	wantHeard(t, s, web.ID, "ui", "noted:all")
	wantHeard(t, s, b.ID, "yard", "noted:all")
	wantHeard(t, s, web.ID, "yard")
	wantHeard(t, s, b.ID, "bugs")
	wantHeard(t, s, web.ID, "bugs", "noted:all")

	err = s.AddSubscription(ctx, "ui", yardrSub(store.ScopeAll, "team=ENG"), "me")
	if err == nil || !strings.Contains(err.Error(), "cannot filter yardr on \"team\"") {
		t.Fatalf("hook key on yardr err = %v", err)
	}
	err = s.AddSubscription(ctx, "ui", yardrSub(store.ScopeAll, "state=open"), "me")
	if err == nil || !strings.Contains(err.Error(), "only done") {
		t.Fatalf("state=open err = %v", err)
	}
	err = s.AddSubscription(ctx, "ui", yardrSub(store.ScopeAll, "bead=nope-0000"), "me")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown bead err = %v", err)
	}
}

// A done subscription hears of a bead once: when a terminal stage closes
// it (and not again when the refinery closes it as merged), or when it is
// closed directly.
func TestYardrDoneOnce(t *testing.T) {
	s, ctx, b := yardrRig(t, "done")
	subscribe(t, s, "done", yardrSub(store.ScopeAll, "state=done"))

	for _, st := range []string{"review", "approved", "merged"} {
		if _, err := s.Advance(ctx, b.ID, st, "jane"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CloseBead(ctx, b.ID, "merged abc123", store.Expect{}, "refinery"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, b.ID, "done", "closed:done")

	direct, err := s.CreateBead(ctx, store.NewBead{Title: "direct", Rig: "yard"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseBead(ctx, direct.ID, "merged def456", store.Expect{}, "refinery"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reopen(ctx, direct.ID, "jane"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseBead(ctx, direct.ID, "again", store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, direct.ID, "done", "closed:done")

	page, err := s.Inbox(ctx, "done", 0)
	if err != nil || page.Total != 2 {
		t.Fatalf("inbox = %+v, %v", page, err)
	}
}

func TestYardrInvolved(t *testing.T) {
	s, ctx, b := yardrRig(t, "jane", "noter", "mentioned", "bystander")
	for _, r := range []string{"jane", "noter", "mentioned", "bystander"} {
		subscribe(t, s, r, yardrSub(store.ScopeInvolved))
	}
	subscribe(t, s, "mentioned", yardrSub(store.ScopeMentions, "state=done"))
	if err := s.AddNote(ctx, b.ID, "on it", "noter"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote(ctx, b.ID, "@mentioned can you look? (not @mentioned-not, mail@bystander)", "noter"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, b.ID, "review", "someone"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "someone"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, b.ID, "jane", "noted:participant", "noted:participant", "advanced:participant", "closed:participant")
	wantHeard(t, s, b.ID, "noter", "advanced:participant", "closed:participant") // not its own notes
	// The mention is in one note: the advance after it does not mention
	// anyone, but the done subscription looks at the whole bead.
	wantHeard(t, s, b.ID, "mentioned", "noted:mentioned", "closed:done")
	wantHeard(t, s, b.ID, "bystander")
}

func TestBeadWatchEndsOnClose(t *testing.T) {
	s, ctx, b := yardrRig(t, "mayor")
	subscribe(t, s, "mayor", yardrSub(store.ScopeAll, "bead="+b.UUID)) // stored as the handle
	subscribe(t, s, "mayor", yardrSub(store.ScopeAll, "bead="+b.ID, "state=done"))
	subscribe(t, s, "mayor", yardrSub(store.ScopeAll, "kind=closed"))
	r, err := s.GetResident(ctx, "mayor")
	if err != nil || r.Subscriptions[0].Where["bead"] != b.ID {
		t.Fatalf("subscriptions = %v, %v", r.Subscriptions, err)
	}
	if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, b.ID, "mayor", "closed:done")
	evs, _ := s.Events(ctx, b.ID, 1)
	if ended := evs[0].Data["watches_ended"]; ended == nil {
		t.Fatalf("closed event %v names no ended watches", evs[0].Data)
	}
	r, _ = s.GetResident(ctx, "mayor")
	if len(r.Subscriptions) != 1 || r.Subscriptions[0].Where["kind"] != "closed" {
		t.Fatalf("after close: %v", r.Subscriptions)
	}
	// Reopened, it is no longer watched.
	if _, err := s.Reopen(ctx, b.ID, "jane"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote(ctx, b.ID, "back", "jane"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, b.ID, "mayor", "closed:done")

	subscribe(t, s, "mayor", yardrSub(store.ScopeAll, "bead="+b.ID, "kind=noted"))
	if n, err := s.Unwatch(ctx, "mayor", b.ID, "me"); err != nil || n != 1 {
		t.Fatalf("unwatch = %d, %v", n, err)
	}
	if _, err := s.Unwatch(ctx, "mayor", b.ID, "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second unwatch err = %v", err)
	}
}

// A watch on a bead that is already closed would end before it began.
func TestWatchOnClosedBeadRefused(t *testing.T) {
	s, ctx, b := yardrRig(t, "mayor")
	if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	err := s.AddSubscription(ctx, "mayor", yardrSub(store.ScopeAll, "bead="+b.ID, "state=done"), "me")
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("watch on closed bead err = %v", err)
	}
	if r, _ := s.GetResident(ctx, "mayor"); len(r.Subscriptions) != 0 {
		t.Fatalf("subscriptions = %v", r.Subscriptions)
	}
	// An epic filter is not a watch: a closed epic still gathers its children.
	subscribe(t, s, "mayor", yardrSub(store.ScopeAll, "epic="+b.ID))
}

// deliveredTo counts the events in the whole log that reached resident
// through its subscriptions.
func deliveredTo(t *testing.T, s store.API, resident string) int {
	t.Helper()
	evs, err := s.EventsSince(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if store.Reason(e, resident) != "" {
			n++
		}
	}
	return n
}

// Two residents subscribed to everything can mention each other in their
// journals without nudging each other forever: a mention counts in the
// entry that makes it, not in every later entry on the same journal.
func TestYardrJournalMentionsDoNotPingPong(t *testing.T) {
	s, ctx, _ := yardrRig(t, "a", "b")
	subscribe(t, s, "a", yardrSub(store.ScopeAll))
	subscribe(t, s, "b", yardrSub(store.ScopeAll))
	journal := func(who, text string) {
		t.Helper()
		if err := s.Journal(ctx, who, store.NoteJournal, text, who); err != nil {
			t.Fatal(err)
		}
	}
	journal("a", "@b can you take the review?")
	if got := deliveredTo(t, s, "b"); got != 1 {
		t.Fatalf("b reached %d times after a's mention, want 1", got)
	}
	journal("b", "@a on it")
	if got := deliveredTo(t, s, "a"); got != 1 {
		t.Fatalf("a reached %d times after b's reply, want 1", got)
	}
	for range 5 { // each acting on its nudge, mentioning no one
		journal("a", "noted, carrying on")
		journal("b", "reviewing")
	}
	if a, b := deliveredTo(t, s, "a"), deliveredTo(t, s, "b"); a != 1 || b != 1 {
		t.Fatalf("after 10 more entries a reached %d, b %d times, want 1 and 1", a, b)
	}
	evs, _ := s.Events(ctx, "a-journal", 100)
	for _, e := range evs {
		if why := store.Reason(e, "b"); why != "" && why != store.ReasonMentioned {
			t.Fatalf("b heard %s on a's journal as %q", e.Kind, why)
		}
	}
	// Naming the journal brings it back into the source.
	subscribe(t, s, "b", yardrSub(store.ScopeAll, "bead=a-journal"))
	journal("a", "plain entry")
	if got := deliveredTo(t, s, "b"); got != 2 {
		t.Fatalf("b subscribed to a's journal reached %d times, want 2", got)
	}
}

// Memory beads are knowledge, not events to hear about, unless asked for.
func TestYardrMemoryOutOfSource(t *testing.T) {
	s, ctx, _ := yardrRig(t, "all", "memories")
	subscribe(t, s, "all", yardrSub(store.ScopeAll))
	subscribe(t, s, "memories", yardrSub(store.ScopeAll, "type="+store.TypeMemory))
	m, err := s.CreateBead(ctx, store.NewBead{Title: "lesson", Type: store.TypeMemory, Stage: "memory"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, m.ID, "all")
	wantHeard(t, s, m.ID, "memories", "created:all")
}

// A resident never hears of its own session starting or stopping, nor its
// own journal entries, whoever logs them (the daemon acts as "yard"), not
// even when it subscribes to its journal or has the kind in --on.
func TestResidentDoesNotHearOfItself(t *testing.T) {
	s, ctx, _ := yardrRig(t, "other")
	if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr",
		On: []string{"resident_started", "resident_stopped"}}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"mayor", "other"} {
		subscribe(t, s, r, yardrSub(store.ScopeAll, "type="+store.TypeJournal))
	}
	subscribe(t, s, "mayor", yardrSub(store.ScopeAll, "bead=mayor-journal"))
	if err := s.ResidentStarted(ctx, "mayor", map[string]any{"pane": "p1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Journal(ctx, "mayor", store.NoteJournal, "starting on the queue", "mayor-session"); err != nil {
		t.Fatal(err)
	}
	if err := s.ResidentStopped(ctx, "mayor", "test"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, "mayor-journal", "mayor")
	wantHeard(t, s, "mayor-journal", "other", "subscribed:all", "resident_started:all", "journal:all", "resident_stopped:all")
	ownJournal := func() []string {
		t.Helper()
		page, err := s.Inbox(ctx, "mayor", 0)
		if err != nil {
			t.Fatal(err)
		}
		var kinds []string
		for _, e := range page.Events {
			if e.Bead != nil && *e.Bead == "mayor-journal" {
				kinds = append(kinds, e.Kind)
			}
		}
		return kinds
	}
	if got := ownJournal(); len(got) != 0 {
		t.Fatalf("mayor's inbox has %v from its own journal", got)
	}
	// Someone else noting on its journal still reaches it.
	if err := s.AddNote(ctx, "mayor-journal", "@mayor see yard-1", "jane"); err != nil {
		t.Fatal(err)
	}
	if got := ownJournal(); !slices.Equal(got, []string{"noted"}) {
		t.Fatalf("mayor's inbox after a note has %v from its journal", got)
	}
}

func TestGroupSubscriptionDeliversToMemberResidents(t *testing.T) {
	s, ctx, b := yardrRig(t, "mayor", "outsider")
	if err := s.PutGroup(ctx, store.Group{Name: "approvers", Runner: "manual", Members: []string{"mckean", "mayor"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}); err != nil {
		t.Fatal(err)
	}
	err := s.AddSubscription(ctx, "builders", yardrSub(store.ScopeAll), "me")
	if err == nil || !strings.Contains(err.Error(), "only manual groups") {
		t.Fatalf("herdr group subscribe err = %v", err)
	}
	subscribe(t, s, "approvers", yardrSub(store.ScopeAll, "state=done"))
	subscribe(t, s, "mayor", yardrSub(store.ScopeAll, "kind=closed"))

	// Redefining the group keeps its subscriptions.
	if err := s.PutGroup(ctx, store.Group{Name: "approvers", Runner: "manual", Limit: 3, Members: []string{"mckean", "mayor"}}); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetGroup(ctx, "approvers"); len(g.Subscriptions) != 1 {
		t.Fatalf("group subscriptions after redefine: %v", g.Subscriptions)
	}
	if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, b.ID, "mayor", "closed:done via approvers")
	wantHeard(t, s, b.ID, "outsider")
	page, err := s.Inbox(ctx, "mayor", 0)
	if err != nil || page.Total != 1 {
		t.Fatalf("mayor inbox = %+v, %v", page, err)
	}
	if n, err := s.RemoveSubscriptions(ctx, "approvers", store.SourceYardr, "", nil, "me"); err != nil || n != 1 {
		t.Fatalf("group unsubscribe = %d, %v", n, err)
	}
}

// --on keeps working as implicit "yardr all kind=<k>" subscriptions: a
// resident with --on gave_up and one subscribed to it hear the same.
func TestOnMatchesKindSubscription(t *testing.T) {
	s, ctx, b := yardrRig(t, "subbed")
	if _, err := s.PutResident(ctx, store.Resident{Name: "legacy", Runner: "herdr", On: []string{"gave_up"}}, "me"); err != nil {
		t.Fatal(err)
	}
	subscribe(t, s, "subbed", yardrSub(store.ScopeAll, "kind=gave_up"))
	if err := s.LogDispatch(ctx, b.ID, "gave_up", "yard", map[string]any{"stage": "new"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote(ctx, b.ID, "noise", "jane"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"legacy", "subbed"} {
		page, err := s.Inbox(ctx, r, 0)
		if err != nil || page.Total != 1 || page.Events[0].Kind != "gave_up" {
			t.Fatalf("%s inbox = %+v, %v", r, page, err)
		}
	}
}

func TestYardrEpicAndGroupKeys(t *testing.T) {
	s, ctx, epic := yardrRig(t, "epic", "builders")
	// Only an epic can be a parent.
	typ := store.TypeEpic
	epic, err := s.UpdateBead(ctx, epic.ID, store.BeadPatch{Type: &typ}, store.Expect{}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateBead(ctx, store.NewBead{Title: "child", Rig: "yard", Stage: "new"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := s.CreateBead(ctx, store.NewBead{Title: "stranger", Rig: "yard", Stage: "new"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, epic.ID, child.ID, "parent", "jane"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "yard-builders", Runner: "herdr"}); err != nil {
		t.Fatal(err)
	}
	subscribe(t, s, "epic", yardrSub(store.ScopeAll, "epic="+epic.ID, "kind=noted"))
	subscribe(t, s, "builders", yardrSub(store.ScopeAll, "group=yard-builders", "kind=advanced"))
	for _, b := range []*store.Bead{epic, child, stranger} {
		if err := s.AddNote(ctx, b.ID, "hi", "jane"); err != nil {
			t.Fatal(err)
		}
	}
	wantHeard(t, s, epic.ID, "epic", "noted:all")
	wantHeard(t, s, child.ID, "epic", "noted:all")
	wantHeard(t, s, stranger.ID, "epic")

	if _, err := s.Claim(ctx, child.ID, "yard-builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, child.ID, "review", "yard-child-new"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, stranger.ID, "review", "jane"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, child.ID, "builders", "advanced:all")
	wantHeard(t, s, stranger.ID, "builders")
}

// Matching runs under the write lock, so it compiles each resident's
// mention pattern once, not once per event.
func TestYardrMentionPatternCompiledOncePerResident(t *testing.T) {
	s, owned := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.PutRig(ctx, store.Rig{Name: "yard", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	names := []string{"a", "b", "c"}
	for _, r := range names {
		if _, err := s.PutResident(ctx, store.Resident{Name: r, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
		subscribe(t, s, r, yardrSub(store.ScopeMentions))
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "work", Rig: "yard", Stage: "new"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	const n = 20
	for i := range n {
		if err := s.AddNote(ctx, b.ID, "@"+names[i%len(names)]+" ping", "jane"); err != nil {
			t.Fatal(err)
		}
	}
	if got := owned.MentionCompiles(); got != int64(len(names)) {
		t.Fatalf("%d patterns compiled over %d events, want %d (one per resident)", got, n+1, len(names))
	}
	if got := deliveredTo(t, s, "a"); got != 7 {
		t.Fatalf("a reached %d times, want 7", got)
	}
}
