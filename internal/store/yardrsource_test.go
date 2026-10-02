package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
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
	if err := s.PutRig(ctx, store.Rig{Name: "web", Base: "origin/main"}); err != nil {
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
	if _, err := s.CloseBead(ctx, b.ID, "merged abc123", "refinery"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, b.ID, "done", "closed:done")

	direct, err := s.CreateBead(ctx, store.NewBead{Title: "direct", Rig: "yard"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseBead(ctx, direct.ID, "merged def456", "refinery"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reopen(ctx, direct.ID, "jane"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseBead(ctx, direct.ID, "again", "jane"); err != nil {
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
	if _, err := s.CloseBead(ctx, b.ID, "done", "someone"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, b.ID, "jane", "noted:participant", "noted:participant", "advanced:participant", "closed:participant")
	wantHeard(t, s, b.ID, "noter", "advanced:participant", "closed:participant") // not its own notes
	wantHeard(t, s, b.ID, "mentioned", "noted:mentioned", "advanced:mentioned", "closed:done")
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
	if _, err := s.CloseBead(ctx, b.ID, "done", "jane"); err != nil {
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
	if _, err := s.CloseBead(ctx, b.ID, "done", "jane"); err != nil {
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
	child, err := s.CreateBead(ctx, store.NewBead{Title: "child", Rig: "yard"}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := s.CreateBead(ctx, store.NewBead{Title: "stranger", Rig: "yard"}, "jane")
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
