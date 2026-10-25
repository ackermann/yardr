package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// movesFlow is a small flow with a round limit: changes back to new is
// allowed once, then sends the bead to decide instead.
const movesFlow = `
new -> review on done
review -> new on changes max 1 then decide | approved on done
decide -> new
approved -> merged on done
merged terminal
`

// Bead carries the bead's stage-move history with who works it now: its
// advanced events oldest first, each with the outcome that moved it and
// the stage a round limit kept it from, the live session's group, kind,
// model, state and start, and the newest event's bare kind and time. The
// history keeps the newest 100 moves; an ended session leaves now
// without one; a bead the viewer may not see reads as missing, with
// neither.
func TestBeadMovesAndNow(t *testing.T) {
	t.Parallel()
	api, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := api.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, api, "yard", "", movesFlow)
	if err := api.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr",
		Config: map[string]any{"kind": "claude", "model": "opus"}}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := api.CreateBead(ctx, store.NewBead{Title: "moves", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	report := func(outcome, stage string) {
		t.Helper()
		got, err := api.Done(ctx, b.ID, outcome, "", store.Expect{}, "me")
		if err != nil || got.Stage != stage {
			t.Fatalf("%s = %+v, %v; want the bead in %s", outcome, got, err, stage)
		}
	}
	report("done", "review")
	report("changes", "new")
	report("done", "review")
	report("changes", "decide") // at max: decide instead of new
	if _, err := api.Advance(ctx, b.ID, "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}

	v, err := api.Bead(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []store.BeadMove{
		{From: "new", To: "review", Outcome: "done"},
		{From: "review", To: "new", Outcome: "changes"},
		{From: "new", To: "review", Outcome: "done"},
		{From: "review", To: "decide", Outcome: "changes", InsteadOf: "new"},
		{From: "decide", To: "new"},
	}
	if len(v.Moves) != len(want) {
		t.Fatalf("moves = %+v, want %d", v.Moves, len(want))
	}
	for i, w := range want {
		if v.Moves[i].From != w.From || v.Moves[i].To != w.To ||
			v.Moves[i].Outcome != w.Outcome || v.Moves[i].InsteadOf != w.InsteadOf ||
			v.Moves[i].At.IsZero() {
			t.Fatalf("moves[%d] = %+v, want %+v with a time", i, v.Moves[i], w)
		}
	}
	// The advanced events themselves, oldest first, say the same: the
	// history is that listing, not a second record.
	evs, err := api.Events(ctx, b.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var advanced []store.Event
	for _, e := range evs {
		if e.Kind == "advanced" {
			advanced = append(advanced, e)
		}
	}
	if len(advanced) != len(want) {
		t.Fatalf("%d advanced events, want %d", len(advanced), len(want))
	}
	for i, e := range advanced {
		from, _ := e.Data["from"].(string)
		to, _ := e.Data["to"].(string)
		outcome, _ := e.Data["outcome"].(string)
		insteadOf, _ := e.Data["instead_of"].(string)
		if m := v.Moves[i]; m.From != from || m.To != to || !m.At.Equal(e.At) ||
			m.Outcome != outcome || m.InsteadOf != insteadOf {
			t.Fatalf("moves[%d] = %+v, event = %+v", i, m, e)
		}
	}

	// The live session is who works it now, with the newest event's bare
	// kind and time beside it. The session runs with a handle naming the
	// yard's machine, which never leaves, and the note after the claim is
	// the latest activity.
	sess, err := api.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := api.MarkRunning(ctx, sess.ID, map[string]any{"pane": "p1", "worktree": "/secret/worktree"}); err != nil {
		t.Fatal(err)
	}
	if err := api.AddNote(ctx, b.ID, "still at it", "me"); err != nil {
		t.Fatal(err)
	}
	v, err = api.Bead(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Now == nil || v.Now.Session == nil || v.Now.Activity == nil {
		t.Fatalf("now = %+v, want a session and activity", v.Now)
	}
	s := v.Now.Session
	if s.Group != "builders" || s.Kind != "claude" || s.Model != "opus" ||
		s.State != "running" || !s.StartedAt.Equal(sess.StartedAt) {
		t.Fatalf("now.session = %+v, want builders claude opus running since the claim", s)
	}
	newest, err := api.Events(ctx, b.ID, 1)
	if err != nil || len(newest) != 1 {
		t.Fatal(err)
	}
	if v.Now.Activity.Kind != newest[0].Kind || !v.Now.Activity.At.Equal(newest[0].At) {
		t.Fatalf("now.activity = %+v, newest event = %+v", v.Now.Activity, newest[0])
	}
	if v.Now.Activity.Kind != "noted" {
		t.Fatalf("now.activity = %+v, want the note after the claim", v.Now.Activity)
	}
	// The served shapes hold exactly their documented keys: no handle,
	// no error, no event data.
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		Moves []map[string]any `json:"moves"`
		Now   map[string]any   `json:"now"`
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	if keys := slices.Sorted(maps.Keys(shape.Now["session"].(map[string]any))); !slices.Equal(keys, []string{"group", "kind", "model", "started_at", "state"}) {
		t.Fatalf("now.session keys = %v", keys)
	}
	if keys := slices.Sorted(maps.Keys(shape.Now["activity"].(map[string]any))); !slices.Equal(keys, []string{"at", "kind"}) {
		t.Fatalf("now.activity keys = %v", keys)
	}
	if keys := slices.Sorted(maps.Keys(shape.Moves[3])); !slices.Equal(keys, []string{"at", "from", "instead_of", "outcome", "to"}) {
		t.Fatalf("a redirect's move keys = %v", keys)
	}
	if keys := slices.Sorted(maps.Keys(shape.Moves[4])); !slices.Equal(keys, []string{"at", "from", "to"}) {
		t.Fatalf("a hand move's keys = %v", keys)
	}

	// An ended session leaves now without one, with the activity still
	// shown. Its error, naming the yard's machine, leaves nothing.
	if err := api.EndSession(ctx, sess.ID, "died", "pane /secret/pane exited"); err != nil {
		t.Fatal(err)
	}
	v, err = api.Bead(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Now == nil || v.Now.Session != nil || v.Now.Activity == nil || v.Now.Activity.Kind == "" || v.Now.Activity.At.IsZero() {
		t.Fatalf("now after the session died = %+v, want no session with activity", v.Now)
	}
	raw, err = json.Marshal(v.Now)
	if err != nil {
		t.Fatal(err)
	}
	var ended map[string]any
	if err := json.Unmarshal(raw, &ended); err != nil {
		t.Fatal(err)
	}
	if _, ok := ended["session"]; ok || strings.Contains(string(raw), "secret") {
		t.Fatalf("now after the session died = %s", raw)
	}

	// Past 100 moves only the newest 100 are kept, whatever stands
	// between them: notes land among the moves below.
	many, err := api.CreateBead(ctx, store.NewBead{Title: "many"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	type legs struct{ from, to string }
	var written []legs
	stage := "new"
	for i := range 120 {
		next := "red"
		if stage == "red" {
			next = "black"
		}
		if _, err := api.Advance(ctx, many.ID, next, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
		written = append(written, legs{stage, next})
		stage = next
		if i%10 == 9 {
			if err := api.AddNote(ctx, many.ID, "a note among the moves", "me"); err != nil {
				t.Fatal(err)
			}
		}
	}
	v, err = api.Bead(ctx, many.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Moves) != 100 {
		t.Fatalf("%d moves, want the newest 100", len(v.Moves))
	}
	for i, m := range v.Moves {
		w := written[i+20]
		if m.From != w.from || m.To != w.to || m.At.IsZero() {
			t.Fatalf("moves[%d] = %+v, want %v with a time", i, m, w)
		}
	}

	// A bead the viewer may not see reads as missing, with no moves
	// and no now.
	own.SetPolicy(policyFunc(func(ctx context.Context, tx *bolt.Tx, actor string, act store.Action, obj store.Object) error {
		if act == store.ActSee && obj.Kind == store.KindBead && obj.Name == b.ID && actor == "hal" {
			return errors.New("hidden from hal")
		}
		return store.CorePolicy.May(ctx, tx, actor, act, obj)
	}))
	if _, err := api.Bead(ctx, b.ID, "hal"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Bead for hal = %v, want not found", err)
	}
}
