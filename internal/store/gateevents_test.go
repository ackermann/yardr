package store_test

import (
	"context"
	"slices"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// gateEvent is an unblocked or reblocked event as the tests read it: its
// kind and the blockers it names.
type gateEvent struct {
	kind     string
	blockers []string
}

// gateEvents lists the bead's gate events, oldest first.
func gateEvents(t *testing.T, s store.API, ctx context.Context, id string) []gateEvent {
	t.Helper()
	evs, err := s.Events(ctx, id, 1000)
	if err != nil {
		t.Fatal(err)
	}
	out := []gateEvent{}
	for _, e := range evs {
		if e.Kind != "unblocked" && e.Kind != "reblocked" {
			continue
		}
		bs := []string{}
		if l, ok := e.Data["blockers"].([]any); ok {
			for _, v := range l {
				bs = append(bs, v.(string))
			}
		}
		out = append(out, gateEvent{e.Kind, bs})
	}
	return out
}

func wantGate(t *testing.T, s store.API, ctx context.Context, id string, want []gateEvent) {
	t.Helper()
	got := gateEvents(t, s, ctx, id)
	equal := len(got) == len(want)
	for i := range got {
		if !equal {
			break
		}
		equal = got[i].kind == want[i].kind && slices.Equal(got[i].blockers, want[i].blockers)
	}
	if !equal {
		t.Fatalf("%s gate events = %+v, want %+v", id, got, want)
	}
}

func addDep(t *testing.T, s store.API, ctx context.Context, from, to, kind, at, until string) {
	t.Helper()
	if err := s.AddDep(ctx, from, to, kind, at, until, "me"); err != nil {
		t.Fatal(err)
	}
}

func rmDep(t *testing.T, s store.API, ctx context.Context, from, to, kind, at, until string) {
	t.Helper()
	if err := s.RemoveDep(ctx, from, to, kind, at, until, "me"); err != nil {
		t.Fatal(err)
	}
}

func advance(t *testing.T, s store.API, ctx context.Context, id, stage string) {
	t.Helper()
	if _, err := s.Advance(ctx, id, stage, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
}

// Adding a holding edge flips the open gate: exactly one reblocked naming
// the blocker. Adding while held, and removing while another edge holds,
// flip nothing; removing the last one logs exactly one unblocked, and
// removing a missing edge logs none.
func TestGateEventsAddRemove(t *testing.T) {
	s, ctx := untilYard(t)
	addDep(t, s, ctx, "y-block", "y-target", store.DepBlocks, "", "")
	wantGate(t, s, ctx, "y-target", []gateEvent{{"reblocked", []string{"y-block"}}})
	addDep(t, s, ctx, "y-plain", "y-target", store.DepBlocks, "", "")
	wantGate(t, s, ctx, "y-target", []gateEvent{{"reblocked", []string{"y-block"}}})
	rmDep(t, s, ctx, "y-block", "y-target", store.DepBlocks, "", "")
	wantGate(t, s, ctx, "y-target", []gateEvent{{"reblocked", []string{"y-block"}}})
	rmDep(t, s, ctx, "y-plain", "y-target", store.DepBlocks, "", "")
	wantGate(t, s, ctx, "y-target", []gateEvent{
		{"reblocked", []string{"y-block"}},
		{"unblocked", []string{"y-plain"}},
	})
	rmDep(t, s, ctx, "y-plain", "y-target", store.DepBlocks, "", "")
	wantGate(t, s, ctx, "y-target", []gateEvent{
		{"reblocked", []string{"y-block"}},
		{"unblocked", []string{"y-plain"}},
	})
}

// An --at edge for a stage the target is not in is already released when
// added: no flip. The target walking into its stage logs reblocked on its
// own move, and walking out logs unblocked; moves between other stages
// log none.
func TestGateEventsAtTargetMoves(t *testing.T) {
	s, ctx := untilYard(t)
	addDep(t, s, ctx, "y-block", "y-target", store.DepBlocks, "approved", "")
	wantGate(t, s, ctx, "y-target", []gateEvent{})
	advance(t, s, ctx, "y-target", "review")
	wantGate(t, s, ctx, "y-target", []gateEvent{})
	advance(t, s, ctx, "y-target", "approved")
	wantGate(t, s, ctx, "y-target", []gateEvent{{"reblocked", []string{"y-block"}}})
	advance(t, s, ctx, "y-target", "new")
	wantGate(t, s, ctx, "y-target", []gateEvent{
		{"reblocked", []string{"y-block"}},
		{"unblocked", []string{"y-block"}},
	})
}

// A target that walks into its --at stage names every blocker newly
// holding it, in one event.
func TestGateEventsOwnMoveNamesEveryNewBlocker(t *testing.T) {
	s, ctx := untilYard(t)
	addDep(t, s, ctx, "y-block", "y-target", store.DepBlocks, "approved", "")
	addDep(t, s, ctx, "y-plain", "y-target", store.DepBlocks, "approved", "")
	wantGate(t, s, ctx, "y-target", []gateEvent{})
	advance(t, s, ctx, "y-target", "review")
	advance(t, s, ctx, "y-target", "approved")
	wantGate(t, s, ctx, "y-target", []gateEvent{{"reblocked", []string{"y-block", "y-plain"}}})
}

// Through --until the blocker's move goes either way: reaching the stage
// opens the gate, going back closes it. A blocker move that leaves the
// gate unchanged logs no gate event.
func TestGateEventsUntilBlockerMoves(t *testing.T) {
	s, ctx := untilYard(t)
	addDep(t, s, ctx, "y-block", "y-target", store.DepBlocks, "", "review")
	wantGate(t, s, ctx, "y-target", []gateEvent{{"reblocked", []string{"y-block"}}})
	advance(t, s, ctx, "y-block", "review")
	wantGate(t, s, ctx, "y-target", []gateEvent{
		{"reblocked", []string{"y-block"}},
		{"unblocked", []string{"y-block"}},
	})
	advance(t, s, ctx, "y-block", "new")
	wantGate(t, s, ctx, "y-target", []gateEvent{
		{"reblocked", []string{"y-block"}},
		{"unblocked", []string{"y-block"}},
		{"reblocked", []string{"y-block"}},
	})
	// Held before and after: no gate event.
	advance(t, s, ctx, "y-block", "backlog")
	wantGate(t, s, ctx, "y-target", []gateEvent{
		{"reblocked", []string{"y-block"}},
		{"unblocked", []string{"y-block"}},
		{"reblocked", []string{"y-block"}},
	})

	// A plain edge holds through every blocker move: the moves log none.
	s, ctx = untilYard(t)
	addDep(t, s, ctx, "y-block", "y-target", store.DepBlocks, "", "")
	advance(t, s, ctx, "y-block", "review")
	advance(t, s, ctx, "y-block", "approved")
	wantGate(t, s, ctx, "y-target", []gateEvent{{"reblocked", []string{"y-block"}}})
}

// Closing a blocker opens its targets' gates, each with its own event;
// reopening closes them again. A close that leaves another blocker
// holding logs none on that target.
func TestGateEventsCloseReopen(t *testing.T) {
	s, ctx := untilYard(t)
	addDep(t, s, ctx, "y-block", "y-target", store.DepBlocks, "", "")
	addDep(t, s, ctx, "y-block", "y-plain", store.DepBlocks, "", "")
	if _, err := s.CloseBead(ctx, "y-block", "merged", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"y-target", "y-plain"} {
		wantGate(t, s, ctx, id, []gateEvent{
			{"reblocked", []string{"y-block"}},
			{"unblocked", []string{"y-block"}},
		})
	}
	if _, err := s.Reopen(ctx, "y-block", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"y-target", "y-plain"} {
		wantGate(t, s, ctx, id, []gateEvent{
			{"reblocked", []string{"y-block"}},
			{"unblocked", []string{"y-block"}},
			{"reblocked", []string{"y-block"}},
		})
	}

	s, ctx = untilYard(t)
	addDep(t, s, ctx, "y-block", "y-target", store.DepBlocks, "", "")
	addDep(t, s, ctx, "y-plain", "y-target", store.DepBlocks, "", "")
	if _, err := s.CloseBead(ctx, "y-block", "merged", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	wantGate(t, s, ctx, "y-target", []gateEvent{{"reblocked", []string{"y-block"}}})
	if _, err := s.Reopen(ctx, "y-block", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	wantGate(t, s, ctx, "y-target", []gateEvent{{"reblocked", []string{"y-block"}}})
}

// A move into a terminal stage closes the blocker, which opens its
// targets' gates like a close.
func TestGateEventsTerminalMoveCloses(t *testing.T) {
	s, ctx := untilYard(t)
	addDep(t, s, ctx, "y-block", "y-target", store.DepBlocks, "", "")
	advance(t, s, ctx, "y-block", "review")
	advance(t, s, ctx, "y-block", "approved")
	wantGate(t, s, ctx, "y-target", []gateEvent{{"reblocked", []string{"y-block"}}})
	advance(t, s, ctx, "y-block", "merged")
	wantGate(t, s, ctx, "y-target", []gateEvent{
		{"reblocked", []string{"y-block"}},
		{"unblocked", []string{"y-block"}},
	})
}

// A held bead whose gate opens gets the event and still waits unrouted.
func TestGateEventsHeldBeadStillWaits(t *testing.T) {
	s, ctx := untilYard(t)
	addDep(t, s, ctx, "y-block", "y-target", store.DepBlocks, "", "")
	if _, err := s.SetHold(ctx, "y-target", true, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseBead(ctx, "y-block", "merged", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	wantGate(t, s, ctx, "y-target", []gateEvent{
		{"reblocked", []string{"y-block"}},
		{"unblocked", []string{"y-block"}},
	})
	if readyHas(t, s, ctx, "y-target") {
		t.Fatal("the held bead routes after its gate opened")
	}
	if b, err := s.GetBead(ctx, "y-target"); err != nil || !b.Hold {
		t.Fatalf("the gate event unheld the bead: %+v, %v", b, err)
	}
}

// The paired edge a parent edge brings flips the train's gate: adding the
// parent reblocks the train, removing it unblocks it. The parent edge
// itself holds nothing on the child.
func TestGateEventsParentEdges(t *testing.T) {
	s, ctx := untilYard(t)
	if _, err := s.CreateBead(ctx, store.NewBead{ID: "y-train", Title: "train", Type: store.TypeTrain, Depot: "yard", Stage: "new"}, "me"); err != nil {
		t.Fatal(err)
	}
	addDep(t, s, ctx, "y-train", "y-target", store.DepParent, "", "")
	wantGate(t, s, ctx, "y-train", []gateEvent{{"reblocked", []string{"y-target"}}})
	wantGate(t, s, ctx, "y-target", []gateEvent{})
	rmDep(t, s, ctx, "y-train", "y-target", store.DepParent, "", "")
	wantGate(t, s, ctx, "y-train", []gateEvent{
		{"reblocked", []string{"y-target"}},
		{"unblocked", []string{"y-target"}},
	})
	wantGate(t, s, ctx, "y-target", []gateEvent{})
}

// The yardmaster subscribes to the gate events like any other bead kinds
// and hears them in its inbox.
func TestGateEventsYardmasterHears(t *testing.T) {
	s, ctx := untilYard(t)
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "kind=unblocked"))
	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "kind=reblocked"))
	addDep(t, s, ctx, "y-block", "y-target", store.DepBlocks, "", "")
	if _, err := s.CloseBead(ctx, "y-block", "merged", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	wantHeard(t, s, "y-target", "yardmaster", "reblocked:all", "unblocked:all")
}

// The stale-review watch ends when the blocker closes, so after a later
// reopen it hears nothing; the reblocked event on the target is what
// tells the yardmaster, through its subscription to the kind.
func TestGateEventsReopenAfterWatchEnded(t *testing.T) {
	s, ctx := untilYard(t)
	for _, name := range []string{"yardmaster", "watcher"} {
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "bead=y-block", "kind=advanced"))
	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "kind=unblocked"))
	subscribe(t, s, "yardmaster", yardrSub(store.ScopeAll, "kind=reblocked"))
	subscribe(t, s, "watcher", yardrSub(store.ScopeAll, "bead=y-block", "kind=advanced"))
	addDep(t, s, ctx, "y-block", "y-target", store.DepBlocks, "", "")
	advance(t, s, ctx, "y-block", "review")
	if _, err := s.CloseBead(ctx, "y-block", "merged", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// The close ended the watch on the blocker.
	evs, err := s.Events(ctx, "y-block", 1000)
	if err != nil {
		t.Fatal(err)
	}
	ended := []string{}
	for _, e := range evs {
		if e.Kind != "closed" {
			continue
		}
		if l, ok := e.Data["watches_ended"].([]any); ok {
			for _, v := range l {
				ended = append(ended, v.(string))
			}
		}
	}
	slices.Sort(ended)
	if !slices.Equal(ended, []string{"watcher", "yardmaster"}) {
		t.Fatalf("watches ended by the close = %v, want watcher and yardmaster", ended)
	}
	if _, err := s.Reopen(ctx, "y-block", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	wantGate(t, s, ctx, "y-target", []gateEvent{
		{"reblocked", []string{"y-block"}},
		{"unblocked", []string{"y-block"}},
		{"reblocked", []string{"y-block"}},
	})
	// The yardmaster hears the return through its kind subscription; the
	// watch hears the blocker's moves only while the blocker is open.
	wantHeard(t, s, "y-target", "yardmaster", "reblocked:all", "unblocked:all", "reblocked:all")
	wantHeard(t, s, "y-block", "yardmaster", "advanced:all")
	wantHeard(t, s, "y-target", "watcher")
	wantHeard(t, s, "y-block", "watcher", "advanced:all")
}
