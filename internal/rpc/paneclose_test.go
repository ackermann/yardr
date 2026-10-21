package rpc_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// openBlockers is how many beads hold b now.
func openBlockers(t *testing.T, st *store.Store, b *store.Bead) int {
	t.Helper()
	got, err := st.OpenBlockers(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	return len(got)
}

// A bead's agent closes its own bead and no other. CloseBead is an agent's
// call, and check lets a pane write any bead no session owns, so without
// the store's refusal a builder could close the weakness the security stage
// made block its bead and land it past the wait. A move into a terminal
// stage closes a bead too, and is refused the same way.
func TestSocketRefusesABeadsPaneClosingAnotherBead(t *testing.T) {
	st, c, y := holdYard(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := st.PutFlow(ctx, trains, "jo")
	must(err)
	landing, err := st.CreateBead(ctx, store.NewBead{Title: "landing", Stage: "open", Metadata: map[string]any{"flow": "trains"}}, "jo")
	must(err)
	sess, err := st.Claim(ctx, y.mine.ID, "builders", "new", "yard")
	must(err)
	must(st.MarkRunning(ctx, sess.ID, thisPane(t, "yard-mine-new")))
	if kind, err := st.CallerOf(store.WithCaller(ctx, os.Getpid())); err != nil || kind != store.CallerBead {
		t.Fatalf("this process is %v, %v: want a bead's pane", kind, err)
	}
	// The security stage's weakness: unowned, and blocking the agent's bead.
	must(st.AddDep(ctx, y.blocker.ID, y.mine.ID, store.DepBlocks, "", "", "jo"))
	if got := openBlockers(t, st, y.mine); got != 1 {
		t.Fatalf("the agent's bead has %d open blockers, want 1", got)
	}

	for _, b := range []*store.Bead{y.blocker, y.other} {
		_, err := c.CloseBead(ctx, b.ID, "fixed", store.Expect{}, "jo")
		if err == nil || !strings.Contains(err.Error(), "may close only its own bead "+y.mine.ID) || !strings.Contains(err.Error(), "yardr bead note "+b.ID) {
			t.Errorf("CloseBead(%s) from a bead's pane = %v, want it refused, pointing at a note", b.ID, err)
		}
		if got, err := st.GetBead(ctx, b.ID); err != nil || got.Status == "closed" {
			t.Errorf("%s after the refused close: %+v, %v", b.ID, got, err)
		}
	}
	if got := openBlockers(t, st, y.mine); got != 1 {
		t.Errorf("the agent's bead has %d open blockers after the refused close, want 1", got)
	}
	if _, err := c.AdvanceNote(ctx, landing.ID, "landed", "", store.Expect{}, "jo"); err == nil || !strings.Contains(err.Error(), "may close only its own bead") {
		t.Errorf("advancing another bead into a terminal stage from a bead's pane = %v, want it refused", err)
	}
	if got := stageOf(t, st, landing); got != "open" {
		t.Errorf("the refused move left the bead in %s", got)
	}

	// Its own bead is the agent's to close.
	if b, err := c.CloseBead(ctx, y.mine.ID, "done", store.Expect{Session: sess.ID}, "jo"); err != nil || b.Status != "closed" {
		t.Errorf("CloseBead of its own bead from a bead's pane: %+v, %v", b, err)
	}
}

// stageOf is the stage the store has the bead in.
func stageOf(t *testing.T, st *store.Store, b *store.Bead) string {
	t.Helper()
	got, err := st.GetBead(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got.Stage
}

// A person, a crew member's pane and the yard itself close any bead: that
// is how a weakness an agent noted as fixed gets closed.
func TestSocketServesAPersonAndACrewPaneClosingAnyBead(t *testing.T) {
	for _, who := range []string{"the yard", "a person", "a crew member's pane"} {
		t.Run(who, func(t *testing.T) {
			st, c, y := holdYard(t)
			ctx := context.Background()
			if err := st.AddDep(ctx, y.blocker.ID, y.mine.ID, store.DepBlocks, "", "", "jo"); err != nil {
				t.Fatal(err)
			}
			api := store.API(c)
			switch who {
			case "the yard":
				api = st
			case "a crew member's pane":
				if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "jo"); err != nil {
					t.Fatal(err)
				}
				if err := st.CrewStartedFor(ctx, "yardmaster", "", thisPane(t, "yard-yardmaster")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := api.CloseBead(ctx, y.blocker.ID, "fixed", store.Expect{}, "jo"); err != nil {
				t.Errorf("CloseBead of another bead by %s: %v", who, err)
			}
			if got := openBlockers(t, st, y.mine); got != 0 {
				t.Errorf("the bead has %d open blockers after %s closed its blocker, want none", got, who)
			}
		})
	}
}

// The depot's pointers choose a bead's flow by its type (resolveFlow), so a
// bead's agent changing a type or a depot chooses a flow, as a pin does:
// its own bead's past review, or a blocker's past the stage its until edge
// waits for. Both are refused on any bead; a patch that names what the bead
// already has changes nothing and is served. The yard changes either.
func TestSocketRefusesABeadsPaneChangingATypeOrADepot(t *testing.T) {
	st, c, y := holdYard(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutDepot(ctx, store.Depot{Name: "notes", Path: t.TempDir(), Kind: store.DepotDir}, "jo"))
	sess, err := st.Claim(ctx, y.mine.ID, "builders", "new", "yard")
	must(err)
	must(st.MarkRunning(ctx, sess.ID, thisPane(t, "yard-mine-new")))

	chore, notes := "chore", "notes"
	for _, r := range []struct {
		what string
		bead *store.Bead
		exp  store.Expect
		p    store.BeadPatch
	}{
		{"type", y.mine, store.Expect{Session: sess.ID}, store.BeadPatch{Type: &chore}},
		{"type", y.blocker, store.Expect{}, store.BeadPatch{Type: &chore}},
		{"depot", y.mine, store.Expect{Session: sess.ID}, store.BeadPatch{Depot: &notes}},
		{"depot", y.other, store.Expect{}, store.BeadPatch{Depot: &notes}},
	} {
		_, err := c.UpdateBead(ctx, r.bead.ID, r.p, r.exp, "jo")
		if err == nil || !strings.Contains(err.Error(), "may not change a bead's "+r.what) {
			t.Errorf("UpdateBead of %s's %s from a bead's pane = %v, want it refused", r.bead.ID, r.what, err)
		}
		if got, err := st.GetBead(ctx, r.bead.ID); err != nil || got.Type != "task" || got.Depot != nil {
			t.Errorf("%s after the refused update: %+v, %v", r.bead.ID, got, err)
		}
	}
	task := "task"
	if _, err := c.UpdateBead(ctx, y.mine.ID, store.BeadPatch{Type: &task, Priority: new(int)}, store.Expect{Session: sess.ID}, "jo"); err != nil {
		t.Errorf("UpdateBead naming its bead's own type from a bead's pane: %v", err)
	}

	if b, err := st.UpdateBead(ctx, y.blocker.ID, store.BeadPatch{Type: &chore, Depot: &notes}, store.Expect{}, "jo"); err != nil || b.Type != chore || b.Depot == nil {
		t.Errorf("UpdateBead of a type and a depot by the yard: %+v, %v", b, err)
	}
}

// A bead's agent files a bead where the flow puts it first, or where people
// look before work starts; not with a flow pin of its choosing, nor into a
// stage further on that agents or a script work, which would skip the
// stages before it (a bead filed into approved lands unreviewed). The yard
// files anywhere.
func TestSocketRefusesABeadsPaneFilingPastItsFlowsStart(t *testing.T) {
	st, c, _ := paneYard(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 3}, "jo"))
	must(st.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Members: []string{"jo"}}, "jo"))
	_, err := st.PutFlow(ctx, store.Flow{Name: "lanes", Stages: []string{"backlog", "new", "decide", "approved", "merged"},
		Transitions: map[string][]string{"backlog": {"new"}, "new": {"approved", "decide"}, "decide": {"new"}, "approved": {"merged"}},
		Terminal:    []string{"merged"}}, "jo")
	must(err)
	_, err = st.PutFlow(ctx, trains, "jo")
	must(err)
	must(st.PutDepot(ctx, store.Depot{Name: "notes", Path: t.TempDir(), Kind: store.DepotDir}, "jo"))
	must(st.SetFlowPointer(ctx, "notes", "", "lanes", "jo"))
	for stage, group := range map[string]string{"new": "builders", "approved": "builders", "decide": "decisions"} {
		_, err := st.AddRoute(ctx, store.Route{Stage: stage, Group: group}, "jo")
		must(err)
	}
	mine, err := st.CreateBead(ctx, store.NewBead{Title: "mine", Depot: "notes", Stage: "new"}, "jo")
	must(err)
	sess, err := st.Claim(ctx, mine.ID, "builders", "new", "yard")
	must(err)
	must(st.MarkRunning(ctx, sess.ID, thisPane(t, "yard-mine-new")))

	if _, err := c.CreateBead(ctx, store.NewBead{Title: "pinned", Depot: "notes", Metadata: map[string]any{"flow": "trains"}}, "jo"); err == nil || !strings.Contains(err.Error(), "may not pin a new bead's flow") {
		t.Errorf("CreateBead with a flow pin from a bead's pane = %v, want it refused", err)
	}
	for _, stage := range []string{"new", "approved"} {
		if _, err := c.CreateBead(ctx, store.NewBead{Title: "past " + stage, Depot: "notes", Stage: stage}, "jo"); err == nil || !strings.Contains(err.Error(), "only in its flow's first stage backlog") {
			t.Errorf("CreateBead into %s from a bead's pane = %v, want it refused", stage, err)
		}
	}
	for _, stage := range []string{"", "backlog", "decide"} {
		if b, err := c.CreateBead(ctx, store.NewBead{Title: "into " + stage, Depot: "notes", Stage: stage}, "jo"); err != nil || b.Stage != or(stage, "backlog") {
			t.Errorf("CreateBead into %q from a bead's pane: %+v, %v", stage, b, err)
		}
	}

	if b, err := st.CreateBead(ctx, store.NewBead{Title: "the yard's", Depot: "notes", Stage: "approved"}, "jo"); err != nil || b.Stage != "approved" {
		t.Errorf("CreateBead into approved by the yard: %+v, %v", b, err)
	}
	if b, err := st.CreateBead(ctx, store.NewBead{Title: "the yard's pin", Metadata: map[string]any{"flow": "trains"}}, "jo"); err != nil || store.PinnedFlow(b) != "trains" {
		t.Errorf("CreateBead with a pin by the yard: %+v, %v", b, err)
	}
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
