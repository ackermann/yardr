package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// An agent's yardr commands carry its session (YARDR_SESSION, set on its
// pane): once the bead has moved on to a session that owns it now, a kept
// or orphaned agent's done, advance or close is refused and changes nothing.
func TestBeadMovesCarryTheSession(t *testing.T) {
	t.Parallel()
	st, ctx := reviewYard(t, "/tmp/yard")
	if err := st.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := st.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	env := yardEnv(t, st)
	a := appOn(st)
	a.actor = "yard-x-new"
	env["YARDR_SESSION"] = builder.ID
	if _, err := run(t, a.beadCmd(), "advance", b.ID, "--to", "review"); err != nil {
		t.Fatalf("the owning session's advance: %v", err)
	}
	reviewer, err := st.Claim(ctx, b.ID, "reviewers", "review", "yard")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetBead(ctx, b.ID)

	want := "stale: bead " + b.ID + " is now in review owned by " + reviewer.ID
	for _, args := range [][]string{
		{"done", b.ID, "--note", "again"},
		{"advance", b.ID, "--to", "new", "--note", "again"},
		{"close", b.ID, "--reason", "again"},
		{"release", b.ID, "--reason", "again"},
		{"reopen", b.ID},
		{"hold", b.ID},
	} {
		if _, err := run(t, a.beadCmd(), args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s from the old session: %v, want %q", args[0], err, want)
		}
	}
	if cur, _ := st.GetBead(ctx, b.ID); cur.Revision != before.Revision {
		t.Fatalf("a stale command changed the bead: %+v", cur)
	}
	if notes, _ := st.Notes(ctx, b.ID); len(notes) != 0 {
		t.Fatalf("a stale command left a note: %+v", notes)
	}

	// Outside any session (Christopher at a shell) nothing is expected,
	// except the revision when one is given.
	env["YARDR_SESSION"] = ""
	a.actor = "mckean"
	for _, args := range [][]string{
		{"release", b.ID, "--revision", "01OLD"},
		{"reopen", b.ID, "--revision", "01OLD"},
	} {
		if _, err := run(t, a.beadCmd(), args...); err == nil || !strings.Contains(err.Error(), "is at revision "+before.Revision+", not 01OLD") {
			t.Errorf("%s with an old revision: %v", args[0], err)
		}
	}
	if _, err := run(t, a.beadCmd(), "advance", b.ID, "--to", "approved"); err != nil {
		t.Fatalf("advance outside a session: %v", err)
	}
}

// A pane's session applies only to the bead it was started for, which the
// store knows from its record of the session: another bead moved from it (a
// follow-up the agent filed) is checked as the actor's, like a person at a
// shell, except that one owned by a live session is refused.
func TestSessionAppliesToTheOwnBeadOnly(t *testing.T) {
	t.Parallel()
	st, ctx := reviewYard(t, "/tmp/yard")
	if err := st.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "b", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := st.Claim(ctx, a.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	env := yardEnv(t, st)
	app := appOn(st)
	app.actor = "yard-a-new"
	env["YARDR_SESSION"] = owner.ID

	if _, err := run(t, app.beadCmd(), "advance", b.ID, "--to", "review"); err != nil {
		t.Fatalf("advance of an unowned bead from a's pane: %v", err)
	}
	events, _ := st.Events(ctx, b.ID, 1)
	if len(events) != 1 || events[0].Kind != "advanced" || events[0].Actor != "yard-a-new" {
		t.Fatalf("the move is not the actor's: %+v", events)
	}

	other, err := st.Claim(ctx, b.ID, "reviewers", "review", "yard")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetBead(ctx, b.ID)
	want := "stale: bead " + b.ID + " is now in review owned by " + other.ID
	if _, err := run(t, app.beadCmd(), "advance", b.ID, "--to", "approved"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("advance of a bead another session owns: %v, want %q", err, want)
	}
	if cur, _ := st.GetBead(ctx, b.ID); cur.Revision != before.Revision {
		t.Fatalf("a refused move changed the bead: %+v", cur)
	}
	// Its own bead the pane still moves, named by UUID too: the own bead is
	// matched on the resolved bead, not on the handle typed.
	if _, err := run(t, app.beadCmd(), "advance", a.UUID, "--to", "review"); err != nil {
		t.Fatalf("the pane's own advance by UUID: %v", err)
	}
}

// bead advance --note - takes the note from the command's stdin,
// trailing newlines trimmed as before.
func TestBeadAdvanceStdinNote(t *testing.T) {
	t.Parallel()
	st, ctx := reviewYard(t, "/tmp/yard")
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	c := a.beadCmd()
	c.SetIn(strings.NewReader("looks good\n\n"))
	if out, err := run(t, c, "advance", b.ID, "--to", "review", "--note", "-"); err != nil {
		t.Fatalf("advance --note -: %v\n%s", err, out)
	}
	got, err := st.GetBead(ctx, b.ID)
	if err != nil || got.Stage != "review" {
		t.Fatalf("advanced bead = %+v, %v", got, err)
	}
	notes, err := st.Notes(ctx, b.ID)
	if err != nil || len(notes) != 1 || notes[0].Text != "looks good" {
		t.Fatalf("notes = %+v, %v", notes, err)
	}
}

// bead done --note - takes the note from the command's stdin,
// trailing newlines trimmed as before.
func TestBeadDoneStdinNote(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("new -> review on done\nreview -> merged\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "pipe"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "yard", "", "pipe", "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	c := a.beadCmd()
	c.SetIn(strings.NewReader("ready for review\n\n"))
	if out, err := run(t, c, "done", b.ID, "--note", "-"); err != nil {
		t.Fatalf("done --note -: %v\n%s", err, out)
	}
	got, err := st.GetBead(ctx, b.ID)
	if err != nil || got.Stage != "review" {
		t.Fatalf("done bead = %+v, %v", got, err)
	}
	notes, err := st.Notes(ctx, b.ID)
	if err != nil || len(notes) != 1 || notes[0].Text != "ready for review" {
		t.Fatalf("notes = %+v, %v", notes, err)
	}
}

// The review form decides on the bead as it was when the form started; if
// the bead has moved since, the decision is refused rather than applied.
func TestReviewRefusesStaleDecision(t *testing.T) {
	st, ctx := reviewYard(t, "/tmp/yard")
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "approved"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := *b
	// Meanwhile the bead is sent back to new, by someone else.
	if _, err := st.AdvanceNote(ctx, b.ID, "new", "red tests", store.Expect{}, "assembly"); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetBead(ctx, b.ID)

	defer func(orig func(...huh.Field) error) { ask = orig }(ask)
	ask = func(...huh.Field) error { return nil } // an empty note
	a := &app{st: st, actor: "mckean"}
	err = a.reviewAdvance(io.Discard, ctx, st, snapshot, reviewAction{Kind: actAdvance, Stage: "merged", Terminal: true})
	if err == nil || !strings.Contains(err.Error(), "stale: bead "+b.ID+" is now in new") {
		t.Fatalf("stale decision: %v", err)
	}
	if cur, _ := st.GetBead(ctx, b.ID); cur.Revision != before.Revision || cur.Status != "open" {
		t.Fatalf("stale decision changed the bead: %+v", cur)
	}
	// On a fresh snapshot the same decision goes through.
	if err := a.reviewAdvance(io.Discard, ctx, st, *before, reviewAction{Kind: actAdvance, Stage: "review"}); err != nil {
		t.Fatalf("fresh decision: %v", err)
	}
}
