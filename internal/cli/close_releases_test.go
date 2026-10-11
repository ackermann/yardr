package cli

import (
	"encoding/json"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// Closing a blocker names exactly the open targets whose gate this close
// opens, sorted; a plain edge holds in every stage.
func TestBeadCloseReleasesOpenedTargets(t *testing.T) {
	st, _ := waitingYard(t)
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.beadCmd(), "close", "yard-1", "--reason", "done")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 closed\nreleases yard-2, yard-3\n"; out != want {
		t.Fatalf("bead close yard-1 = %q, want %q", out, want)
	}
}

// Closing one of several holding blockers names none of them: every target
// stays held by the other blocker, so there is no second line.
func TestBeadCloseNamesNoneWhenAnotherBlockerHolds(t *testing.T) {
	st, ctx := waitingYard(t)
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yard-4", Title: "other", Depot: "yard", Stage: "new"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"yard-2", "yard-3"} {
		if err := st.AddDep(ctx, "yard-4", to, store.DepBlocks, "", "", "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.beadCmd(), "close", "yard-1", "--reason", "done")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 closed\n"; out != want {
		t.Fatalf("bead close yard-1 with yard-4 holding = %q, want %q", out, want)
	}
}

// A close names only what it opens: the target held by another blocker is
// left out, the one held only by the closed bead is named.
func TestBeadCloseReleasesOnlyOpened(t *testing.T) {
	st, ctx := waitingYard(t)
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yard-4", Title: "other", Depot: "yard", Stage: "new"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddDep(ctx, "yard-4", "yard-3", store.DepBlocks, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.beadCmd(), "close", "yard-1", "--reason", "done")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 closed\nreleases yard-2\n"; out != want {
		t.Fatalf("bead close yard-1 = %q, want %q", out, want)
	}
}

// A staged edge that holds nothing before the close is not newly released
// by it: the target of an --at edge it is not in is already open, so only
// the plain target is named.
func TestBeadCloseSkipsAlreadyOpenStagedTarget(t *testing.T) {
	st, ctx := waitingYard(t)
	if err := st.RemoveDep(ctx, "yard-1", "yard-2", store.DepBlocks, "", "review", "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.beadCmd(), "close", "yard-1", "--reason", "done")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 closed\nreleases yard-3\n"; out != want {
		t.Fatalf("bead close yard-1 with yard-2 already open = %q, want %q", out, want)
	}
}

// An --until edge released before the close is not newly released by it
// either: with the blocker in review the target's gate is already open.
func TestBeadCloseSkipsUntilReleasedTarget(t *testing.T) {
	st, ctx := waitingYard(t)
	if _, err := st.Advance(ctx, "yard-1", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.beadCmd(), "close", "yard-1", "--reason", "done")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 closed\nreleases yard-3\n"; out != want {
		t.Fatalf("bead close yard-1 with yard-2 until-released = %q, want %q", out, want)
	}
}

// A closed target is not named: the gate opens for open beads only.
func TestBeadCloseSkipsClosedTarget(t *testing.T) {
	st, ctx := waitingYard(t)
	if _, err := st.CloseBead(ctx, "yard-2", "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.beadCmd(), "close", "yard-1", "--reason", "done")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 closed\nreleases yard-3\n"; out != want {
		t.Fatalf("bead close yard-1 with yard-2 closed = %q, want %q", out, want)
	}
}

// bead close --json is the bead as before, with the releases added; with
// nothing released the field is absent, so the shape is unchanged.
func TestBeadCloseJSONCarriesReleases(t *testing.T) {
	st, _ := waitingYard(t)
	j := &app{st: st, actor: "me", json: true}

	out, err := run(t, j.beadCmd(), "close", "yard-1", "--reason", "done")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bead close --json = %q (%v)", out, err)
	}
	if got["id"] != "yard-1" || got["status"] != "closed" {
		t.Fatalf("bead close --json bead fields = %v", got)
	}
	rels, ok := got["releases"].([]any)
	if !ok || len(rels) != 2 || rels[0] != "yard-2" || rels[1] != "yard-3" {
		t.Fatalf("bead close --json releases = %v", got["releases"])
	}

	// Nothing released: the bead alone, as before.
	st2, ctx2 := waitingYard(t)
	if _, err := st2.CloseBead(ctx2, "yard-2", "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st2.CloseBead(ctx2, "yard-3", "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	j2 := &app{st: st2, actor: "me", json: true}
	out, err = run(t, j2.beadCmd(), "close", "yard-1", "--reason", "done")
	if err != nil {
		t.Fatal(err)
	}
	var alone map[string]any
	if err := json.Unmarshal([]byte(out), &alone); err != nil {
		t.Fatalf("bead close --json = %q (%v)", out, err)
	}
	if _, ok := alone["releases"]; ok {
		t.Fatalf("bead close --json with nothing released carries releases: %v", alone["releases"])
	}
	if alone["id"] != "yard-1" || alone["status"] != "closed" {
		t.Fatalf("bead close --json bead fields = %v", alone)
	}
}
