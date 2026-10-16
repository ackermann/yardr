package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

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

// yardrDepot is the yard depot with yardFlow, crew members with no subscriptions
// yet, and a bead in stage new.
func yardrDepot(t *testing.T, crew ...string) (store.API, context.Context, *store.Bead) {
	t.Helper()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", yardFlow)
	for _, r := range crew {
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: r, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard", Stage: "new", Labels: []string{"ui"}}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx, b
}

func subscribe(t *testing.T, s store.API, name string, sub store.Subscription) {
	t.Helper()
	if err := s.AddSubscription(context.Background(), name, sub, name); err != nil {
		t.Fatal(err)
	}
}

// heard lists, oldest first, "<kind>:<reason>" for each of the bead's
// events that reached crew member through its subscriptions.
func heard(t *testing.T, s store.API, bead, member string) []string {
	t.Helper()
	evs, err := s.Events(context.Background(), bead, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if why := store.Reason(e, member); why != "" {
			out = append(out, e.Kind+":"+why)
		}
	}
	return out
}

func wantHeard(t *testing.T, s store.API, bead, member string, want ...string) {
	t.Helper()
	if got := heard(t, s, bead, member); !slices.Equal(got, want) {
		t.Errorf("%s heard %v, want %v", member, got, want)
	}
}

func TestYardrPerBeadAndPerStage(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t, "watcher", "stager", "other")
	other, err := s.CreateBead(ctx, store.NewBead{Title: "other", Depot: "yard", Stage: "new"}, "jane")
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
		if _, err := s.Advance(ctx, id, "review", store.Expect{}, "jane"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Advance(ctx, b.ID, "approved", store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}

	wantHeard(t, s, b.ID, "watcher", "noted:all", "advanced:all", "advanced:all")
	wantHeard(t, s, other.ID, "watcher")
	wantHeard(t, s, b.ID, "stager", "advanced:all") // entering review, not leaving it
	wantHeard(t, s, other.ID, "stager", "advanced:all")
	wantHeard(t, s, b.ID, "other", "advanced:all")
	wantHeard(t, s, other.ID, "other")
}

func TestYardrLabelDepotTypeFilters(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t, "ui", "yard", "bugs")
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	web, err := s.CreateBead(ctx, store.NewBead{Title: "web", Depot: "web", Type: "bug", Labels: []string{"ui"}}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	subscribe(t, s, "ui", yardrSub(store.ScopeAll, "label=ui", "kind=noted"))
	subscribe(t, s, "yard", yardrSub(store.ScopeAll, "depot=yard", "kind=noted"))
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
// it (and not again when the assembly closes it as merged), or when it is
// closed directly.
func TestYardrDoneOnce(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t, "done")
	subscribe(t, s, "done", yardrSub(store.ScopeAll, "state=done"))

	for _, st := range []string{"review", "approved", "merged"} {
		if _, err := s.Advance(ctx, b.ID, st, store.Expect{}, "jane"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CloseBead(ctx, b.ID, "merged abc123", store.Expect{}, "assembly"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, b.ID, "done", "closed:done")

	direct, err := s.CreateBead(ctx, store.NewBead{Title: "direct", Depot: "yard"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseBead(ctx, direct.ID, "merged def456", store.Expect{}, "assembly"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reopen(ctx, direct.ID, store.Expect{}, "jane"); err != nil {
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
	t.Parallel()
	s, ctx, b := yardrDepot(t, "jane", "noter", "mentioned", "bystander")
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
	if _, err := s.Advance(ctx, b.ID, "review", store.Expect{}, "someone"); err != nil {
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
	t.Parallel()
	s, ctx, b := yardrDepot(t, "yardmaster")
	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "bead="+b.UUID)) // stored as the handle
	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "bead="+b.ID, "state=done"))
	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "kind=closed"))
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil || r.Subscriptions[0].Where["bead"] != b.ID {
		t.Fatalf("subscriptions = %v, %v", r.Subscriptions, err)
	}
	if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, b.ID, "yardmaster", "closed:done")
	evs, _ := s.Events(ctx, b.ID, 1)
	if ended := evs[0].Data["watches_ended"]; ended == nil {
		t.Fatalf("closed event %v names no ended watches", evs[0].Data)
	}
	r, _ = s.GetCrew(ctx, "yardmaster")
	if len(r.Subscriptions) != 1 || r.Subscriptions[0].Where["kind"] != "closed" {
		t.Fatalf("after close: %v", r.Subscriptions)
	}
	// Reopened, it is no longer watched.
	if _, err := s.Reopen(ctx, b.ID, store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote(ctx, b.ID, "back", "jane"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, b.ID, "yardmaster", "closed:done")

	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "bead="+b.ID, "kind=noted"))
	if n, err := s.Unwatch(ctx, "yardmaster", b.ID, "yardmaster"); err != nil || n != 1 {
		t.Fatalf("unwatch = %d, %v", n, err)
	}
	if _, err := s.Unwatch(ctx, "yardmaster", b.ID, "yardmaster"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second unwatch err = %v", err)
	}
}

// A watch on a bead that is already closed would end before it began.
func TestWatchOnClosedBeadRefused(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t, "yardmaster")
	if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	err := s.AddSubscription(ctx, "yardmaster", yardrSub(store.ScopeAll, "bead="+b.ID, "state=done"), "me")
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("watch on closed bead err = %v", err)
	}
	if r, _ := s.GetCrew(ctx, "yardmaster"); len(r.Subscriptions) != 0 {
		t.Fatalf("subscriptions = %v", r.Subscriptions)
	}
	// A train filter is not a watch: a closed train still gathers its children.
	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "train="+b.ID))
}

// deliveredTo counts the events in the whole log that reached crew member
// through its subscriptions.
func deliveredTo(t *testing.T, s store.API, member string) int {
	t.Helper()
	evs, err := s.EventsSince(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if store.Reason(e, member) != "" {
			n++
		}
	}
	return n
}

// Two crew members subscribed to everything can mention each other in their
// journals without nudging each other forever: a mention counts in the
// entry that makes it, not in every later entry on the same journal.
func TestYardrJournalMentionsDoNotPingPong(t *testing.T) {
	t.Parallel()
	s, ctx, _ := yardrDepot(t, "a", "b")
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
	t.Parallel()
	s, ctx, _ := yardrDepot(t, "all", "memories")
	subscribe(t, s, "all", yardrSub(store.ScopeAll))
	subscribe(t, s, "memories", yardrSub(store.ScopeAll, "type="+store.TypeMemory))
	m, err := s.CreateBead(ctx, store.NewBead{Title: "lesson", Type: store.TypeMemory, Stage: "memory"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, m.ID, "all")
	wantHeard(t, s, m.ID, "memories", "created:all")
}

// A crew member never hears of its own session starting or stopping, nor its
// own journal entries, whoever logs them (the daemon acts as "yard"), not
// even when it subscribes to its journal or has the kind in --on.
func TestCrewDoesNotHearOfItself(t *testing.T) {
	t.Parallel()
	s, ctx, _ := yardrDepot(t, "other")
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr",
		On: []string{"crew_started", "crew_stopped"}}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"yardmaster", "other"} {
		subscribe(t, s, r, yardrSub(store.ScopeAll, "type="+store.TypeJournal))
	}
	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "bead=yardmaster-journal"))
	if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"pane": "p1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Journal(ctx, "yardmaster", store.NoteJournal, "starting on the queue", "yardmaster-session"); err != nil {
		t.Fatal(err)
	}
	if err := s.CrewStopped(ctx, "yardmaster", "test"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, "yardmaster-journal", "yardmaster")
	wantHeard(t, s, "yardmaster-journal", "other", "subscribed:all", "crew_started:all", "journal:all", "crew_stopped:all")
	ownJournal := func() []string {
		t.Helper()
		page, err := s.Inbox(ctx, "yardmaster", 0)
		if err != nil {
			t.Fatal(err)
		}
		var kinds []string
		for _, e := range page.Events {
			if e.Bead != nil && *e.Bead == "yardmaster-journal" {
				kinds = append(kinds, e.Kind)
			}
		}
		return kinds
	}
	if got := ownJournal(); len(got) != 0 {
		t.Fatalf("yardmaster's inbox has %v from its own journal", got)
	}
	// Someone else noting on its journal still reaches it.
	if err := s.AddNote(ctx, "yardmaster-journal", "@yardmaster see yard-1", "jane"); err != nil {
		t.Fatal(err)
	}
	if got := ownJournal(); !slices.Equal(got, []string{"noted"}) {
		t.Fatalf("yardmaster's inbox after a note has %v from its journal", got)
	}
}

func TestGroupSubscriptionDeliversToMemberCrew(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t, "yardmaster", "outsider")
	if err := s.PutGroup(ctx, store.Group{Name: "approvers", Runner: "manual", Members: []string{"mckean", "yardmaster"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	err := s.AddSubscription(ctx, "builders", yardrSub(store.ScopeAll), "me")
	if err == nil || !strings.Contains(err.Error(), "only manual groups") {
		t.Fatalf("herdr group subscribe err = %v", err)
	}
	// The group's subscription is a member's to make.
	if err := s.AddSubscription(ctx, "approvers", yardrSub(store.ScopeAll, "state=done"), "yardmaster"); err != nil {
		t.Fatal(err)
	}
	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "kind=closed"))

	// Redefining the group keeps its subscriptions.
	if err := s.PutGroup(ctx, store.Group{Name: "approvers", Runner: "manual", Limit: 3, Members: []string{"mckean", "yardmaster"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetGroup(ctx, "approvers"); len(g.Subscriptions) != 1 {
		t.Fatalf("group subscriptions after redefine: %v", g.Subscriptions)
	}
	if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, b.ID, "yardmaster", "closed:done via approvers")
	wantHeard(t, s, b.ID, "outsider")
	page, err := s.Inbox(ctx, "yardmaster", 0)
	if err != nil || page.Total != 1 {
		t.Fatalf("yardmaster inbox = %+v, %v", page, err)
	}
	if n, err := s.RemoveSubscriptions(ctx, "approvers", store.SourceYardr, "", nil, "yardmaster"); err != nil || n != 1 {
		t.Fatalf("group unsubscribe = %d, %v", n, err)
	}
}

// --on keeps working as implicit "yardr all kind=<k>" subscriptions: a
// crew member with --on gave_up and one subscribed to it hear the same.
func TestOnMatchesKindSubscription(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t, "subbed")
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "legacy", Runner: "herdr", On: []string{"gave_up"}}, "me"); err != nil {
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

func TestYardrTrainAndGroupKeys(t *testing.T) {
	t.Parallel()
	s, ctx, train := yardrDepot(t, "train", "builders")
	// Only a train can be a parent.
	typ := store.TypeTrain
	train, err := s.UpdateBead(ctx, train.ID, store.BeadPatch{Type: &typ}, store.Expect{}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateBead(ctx, store.NewBead{Title: "child", Depot: "yard", Stage: "new"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := s.CreateBead(ctx, store.NewBead{Title: "stranger", Depot: "yard", Stage: "new"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddDep(ctx, train.ID, child.ID, "parent", "", "", "jane"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "yard-builders", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	subscribe(t, s, "train", yardrSub(store.ScopeAll, "train="+train.ID, "kind=noted"))
	subscribe(t, s, "builders", yardrSub(store.ScopeAll, "group=yard-builders", "kind=advanced"))
	for _, b := range []*store.Bead{train, child, stranger} {
		if err := s.AddNote(ctx, b.ID, "hi", "jane"); err != nil {
			t.Fatal(err)
		}
	}
	wantHeard(t, s, train.ID, "train", "noted:all")
	wantHeard(t, s, child.ID, "train", "noted:all")
	wantHeard(t, s, stranger.ID, "train")

	if _, err := s.Claim(ctx, child.ID, "yard-builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, child.ID, "review", store.Expect{}, "yard-child-new"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, stranger.ID, "review", store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, child.ID, "builders", "advanced:all")
	wantHeard(t, s, stranger.ID, "builders")
}

// Matching runs under the write lock, so it compiles each crew member's
// mention pattern once, not once per event.
func TestYardrMentionPatternCompiledOncePerCrew(t *testing.T) {
	t.Parallel()
	s, owned := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	names := []string{"a", "b", "c"}
	for _, r := range names {
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: r, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
		subscribe(t, s, r, yardrSub(store.ScopeMentions))
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard", Stage: "new"}, "jane")
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
		t.Fatalf("%d patterns compiled over %d events, want %d (one per crew member)", got, n+1, len(names))
	}
	if got := deliveredTo(t, s, "a"); got != 7 {
		t.Fatalf("a reached %d times, want 7", got)
	}
}

// A key of a filter may hold several values, any of which matches; the
// keys stay ANDed. The values are kept sorted, each once, so the same ones
// in another order are the same subscription (yardr-03fj).
func TestYardrSeveralValuesForOneKey(t *testing.T) {
	t.Parallel()
	s, ctx, b := yardrDepot(t, "clerk", "trains")
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	web, err := s.CreateBead(ctx, store.NewBead{Title: "web", Depot: "web"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	kinds := func(ks ...string) string { return "kind=" + store.JoinWhere(ks) }
	subscribe(t, s, "clerk", yardrSub(store.ScopeAll, "depot=yard", kinds("session_blocked", "gave_up")))
	subscribe(t, s, "clerk", yardrSub(store.ScopeAll, "depot=yard", "kind="+store.JoinWhere([]string{"gave_up", "session_blocked", "gave_up"})))
	r, err := s.GetCrew(ctx, "clerk")
	if err != nil || len(r.Subscriptions) != 1 || r.Subscriptions[0].String() != "yardr all depot=yard kind=gave_up kind=session_blocked" {
		t.Fatalf("clerk's subscriptions = %v, %v; want the one, its kinds sorted", r.Subscriptions, err)
	}
	for _, id := range []string{b.ID, web.ID} {
		for _, kind := range []string{"session_blocked", "gave_up", "session_unblocked"} {
			if err := s.LogDispatch(ctx, id, kind, "yard", map[string]any{"stage": "new"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	wantHeard(t, s, b.ID, "clerk", "session_blocked:all", "gave_up:all")
	wantHeard(t, s, web.ID, "clerk")

	// Each value of train is a bead, kept as its handle; a watch is of one.
	subscribe(t, s, "trains", yardrSub(store.ScopeAll, "train="+store.JoinWhere([]string{b.UUID, web.ID}), "kind=noted"))
	for _, id := range []string{b.ID, web.ID} {
		if err := s.AddNote(ctx, id, "hi", "jane"); err != nil {
			t.Fatal(err)
		}
		wantHeard(t, s, id, "trains", "noted:all")
	}
	for sub, want := range map[string]string{
		"bead=" + store.JoinWhere([]string{b.ID, web.ID}):    "2 beads in one subscription",
		"kind=" + store.JoinWhere([]string{"held", ""}):      "empty value for kind",
		"state=" + store.JoinWhere([]string{"done", "open"}): "only done",
		"train=" + store.JoinWhere([]string{b.ID, "nope-0"}): "not found",
	} {
		if err := s.AddSubscription(ctx, "clerk", yardrSub(store.ScopeAll, sub), "clerk"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", sub, err, want)
		}
	}
	if n, err := s.RemoveSubscriptions(ctx, "clerk", store.SourceYardr, "", map[string]string{"depot": "yard", "kind": store.JoinWhere([]string{"session_blocked", "gave_up"})}, "clerk"); err != nil || n != 1 {
		t.Errorf("removed %d, %v; want the one, by its values in any order", n, err)
	}
}

// A subscription stored before a key could take several values matches as
// it did: its record is as that build wrote it, one string for each key,
// and a comma, a bar, a space or a line break in the string separates
// nothing. The comma and the bar ride a label; the space and the break
// ride an actor, since a label holds neither (yardr-edhj).
func TestSubscriptionStoredBeforeSeveralValues(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "yardr.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"old", "odd", "said", "line"} {
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	stored := map[string]string{
		"old":  `{"source":"yardr","scope":"all","where":{"depot":"yard","kind":"noted","label":"ui"}}`,
		"odd":  `{"source":"yardr","scope":"all","where":{"kind":"noted","label":"ui,api|xy"}}`,
		"said": `{"source":"yardr","scope":"all","where":{"kind":"noted","actor":"jo bloggs"}}`,
		"line": `{"source":"yardr","scope":"all","where":{"kind":"noted","actor":"line1\nline2"}}`,
	}
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("crew"))
		for name, sub := range stored {
			rec := strings.TrimSuffix(string(b.Get([]byte(name))), "}")
			if strings.Contains(rec, `"subscriptions"`) {
				t.Fatalf("%s already has subscriptions: %s", name, rec)
			}
			if err := b.Put([]byte(name), []byte(rec+`,"subscriptions":[`+sub+`]}`)); err != nil {
				return err
			}
		}
		return nil
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	before := bucketDump(t, path, "crew")

	if s, err = store.Open(path); err != nil {
		t.Fatal(err)
	}
	beads := map[string]string{}
	for _, label := range []string{"ui", "api", "ui,api|xy"} {
		b, err := s.CreateBead(ctx, store.NewBead{Title: label, Depot: "yard", Labels: []string{label}}, "jane")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AddNote(ctx, b.ID, "hi", "jane"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetHold(ctx, b.ID, true, store.Expect{}, "jane"); err != nil {
			t.Fatal(err)
		}
		beads[label] = b.ID
	}
	// The space and the break ride notes' actors, on a bead no label
	// filter hears: a note by half the stored value must not match.
	chat, err := s.CreateBead(ctx, store.NewBead{Title: "chat", Depot: "yard", Labels: []string{"chat"}}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"jo bloggs", "jo", "line1\nline2", "line1"} {
		if err := s.AddNote(ctx, chat.ID, "hi", actor); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetHold(ctx, chat.ID, true, store.Expect{}, "jane"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, beads["ui"], "old", "noted:all")
	wantHeard(t, s, beads["api"], "old")
	wantHeard(t, s, beads["ui,api|xy"], "old")
	wantHeard(t, s, beads["ui"], "odd")
	wantHeard(t, s, beads["api"], "odd")
	wantHeard(t, s, beads["ui,api|xy"], "odd", "noted:all")
	wantHeard(t, s, chat.ID, "said", "noted:all")
	wantHeard(t, s, beads["ui"], "said")
	wantHeard(t, s, chat.ID, "line", "noted:all")
	wantHeard(t, s, beads["ui"], "line")
	r, err := s.GetCrew(ctx, "odd")
	if err != nil || len(r.Subscriptions) != 1 || r.Subscriptions[0].String() != "yardr all kind=noted label=ui,api|xy" {
		t.Fatalf("odd's subscriptions = %v, %v", r, err)
	}
	// Nor was a record rewritten to be read.
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for k, v := range bucketDump(t, path, "crew") {
		if before[k] != v {
			t.Errorf("%s was rewritten:\n%s\nwas\n%s", k, v, before[k])
		}
	}
}
