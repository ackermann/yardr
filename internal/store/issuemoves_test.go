package store_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// moveFlow is the starter flow's shape for tasks, with a stage a bead can
// be sent back into itself from.
const moveFlow = `
backlog  -> new
new      -> review on done | new on failed | backlog
review   -> merged on done | new on changes
merged terminal
`

const moveEpicFlow = `
open   -> review on done
review -> done on done
done terminal
`

var linearMap = map[string]string{"task:new": "started", "task:review": "review", "task:merged": "done",
	"epic:open": "started", "epic:done": "done"}

// moveYard is a yard whose rig follows moveFlow, with resident mayor, who
// has an identity in both sources and may set a status in them, and a
// linear hook that maps as it.
func moveYard(t *testing.T) (store.API, *store.Store, context.Context) {
	t.Helper()
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.PutRig(ctx, store.Rig{Name: "yard", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yard", "", moveFlow)
	putFlow(t, s, "yard", "epic", moveEpicFlow)
	moveMayor(t, s, "linear,github")
	for _, src := range []string{"linear", "github"} {
		if err := s.SetIdentity(ctx, "mayor", src, store.Identity{ID: "u-mayor"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	putHook(t, s, store.Hook{Name: "linear", Source: "linear", SecretFile: "/s", As: "mayor", Map: linearMap})
	return s, own, ctx
}

// moveMayor creates or redefines resident mayor with set_state for sources ("":
// none).
func moveMayor(t *testing.T, s store.API, sources string) {
	t.Helper()
	def := store.Resident{Name: "mayor", Runner: "herdr", Config: map[string]any{}}
	if sources != "" {
		def.Config[store.SetState] = sources
	}
	if _, err := s.PutResident(context.Background(), def, "me"); err != nil {
		t.Fatal(err)
	}
}

func putHook(t *testing.T, s store.API, h store.Hook) {
	t.Helper()
	if _, err := s.PutHook(context.Background(), h, "me"); err != nil {
		t.Fatal(err)
	}
}

// moves is the moves still to make, each as "bead hook url state@stage".
func moves(t *testing.T, s store.API) []string {
	t.Helper()
	ms, err := s.IssueMoves(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%s %s %s %s@%s", m.Bead, m.Hook, m.URL, m.State, m.Stage))
	}
	return out
}

func wantMoves(t *testing.T, s store.API, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := moves(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("moves %v, want %v", got, want)
	}
}

// tracked creates a bead of typ in stage that tracks the issues.
func tracked(t *testing.T, s store.API, id, typ, stage string, issues ...string) {
	t.Helper()
	nb := store.NewBead{ID: id, Title: id, Rig: "yard", Type: typ, Stage: stage}
	for _, u := range issues {
		nb.Links = append(nb.Links, tracks(u))
	}
	if _, err := s.CreateBead(context.Background(), nb, "me"); err != nil {
		t.Fatal(err)
	}
}

func lastEvent(t *testing.T, s store.API, bead string) store.Event {
	t.Helper()
	evs, err := s.Events(context.Background(), bead, 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("events of %s: %v, %v", bead, evs, err)
	}
	return evs[0]
}

// A map is refused without the resident to act as, with a word that is no
// state, with a line that names no type and stage, and for a resident that
// does not exist or has no identity in the hook's source.
func TestHookMapRefusals(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	moveMayor(t, s, "linear")
	head, _ := s.Head(ctx)
	hook := func(as string, m map[string]string) store.Hook {
		return store.Hook{Name: "linear", Source: "linear", SecretFile: "/s", As: as, Map: m}
	}
	for _, c := range []struct {
		name string
		h    store.Hook
		want string
	}{
		{"no resident to act as", hook("", map[string]string{"task:new": "started"}), "needs the resident it acts as"},
		{"unknown word", hook("mayor", map[string]string{"task:new": "closed"}), `"closed" is no state: want started, review, done`},
		{"no stage", hook("mayor", map[string]string{"task": "started"}), "want <bead type>:<stage>=<state>"},
		{"bad type", hook("mayor", map[string]string{"Task:new": "started"}), "bead type"},
		{"bad stage", hook("mayor", map[string]string{"task:in review": "review"}), "stage"},
		{"no such resident", hook("deputy", map[string]string{"task:new": "started"}), "resident deputy"},
		{"no identity in the source", hook("mayor", map[string]string{"task:new": "started"}), "has no identity in linear"},
	} {
		_, err := s.PutHook(ctx, c.h, "me")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		if c.name == "no such resident" && !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: %v is not ErrNotFound", c.name, err)
		}
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("the refusals logged %d events", now-head)
	}
	if _, err := s.GetHook(ctx, "linear"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused hook was saved: %v", err)
	}
	// An identity in another source does not count.
	if err := s.SetIdentity(ctx, "mayor", "github", store.Identity{Key: "mayor-bot"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutHook(ctx, hook("mayor", linearMap), "me"); err == nil || !strings.Contains(err.Error(), "yardr resident identity mayor --source linear") {
		t.Fatalf("identity elsewhere: %v", err)
	}
	if err := s.SetIdentity(ctx, "mayor", "linear", store.Identity{ID: "u-mayor"}, "me"); err != nil {
		t.Fatal(err)
	}
	saved, err := s.PutHook(ctx, hook("mayor", linearMap), "me")
	if err != nil || saved.As != "mayor" || !reflect.DeepEqual(saved.Map, linearMap) {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	got, err := s.GetHook(ctx, "linear")
	if err != nil || got.As != "mayor" || !reflect.DeepEqual(got.Map, linearMap) {
		t.Fatalf("read back %+v, %v", got, err)
	}
	evs, _ := s.Events(ctx, "", 1)
	if e := evs[0]; e.Kind != "hook_added" || e.Data["as"] != "mayor" || len(e.Data["map"].(map[string]any)) != len(linearMap) {
		t.Fatalf("event %+v", e)
	}
	// add replaces: a hook saved without them has neither.
	if plain, err := s.PutHook(ctx, hook("", nil), "me"); err != nil || plain.As != "" || plain.Map != nil {
		t.Fatalf("replaced %+v, %v", plain, err)
	}
}

// A bead entering a mapped stage queues the move of the issue it tracks,
// whatever moved it; nothing else does.
func TestStageQueuesIssueMove(t *testing.T) {
	s, _, ctx := moveYard(t)
	tracked(t, s, "yard-a", "task", "backlog", linA)
	wantMoves(t, s) // backlog is not in the map

	// An outcome.
	if _, err := s.Done(ctx, "yard-a", "", "", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	wantMoves(t, s, "yard-a linear "+linA+" started@new")
	ms, _ := s.IssueMoves(ctx)
	adv := lastEvent(t, s, "yard-a")
	if m := ms[0]; adv.Kind != "advanced" || m.Seq != adv.Seq || m.ID != fmt.Sprintf("%d/linear", adv.Seq) || m.As != "mayor" ||
		m.Type != "task" || m.Source != "linear" || m.Thread != "" || m.At.IsZero() || m.Attempts != 0 || m.RetryAt != nil {
		t.Fatalf("move %+v for event %+v", m, adv)
	}
	// Sent back into the stage it is in: it entered nothing.
	if _, err := s.Done(ctx, "yard-a", "failed", "", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	wantMoves(t, s, "yard-a linear "+linA+" started@new")
	// An advance, then an outcome into the end stage, which closes the bead:
	// the close adds no move of its own, and the closed bead's link counts.
	if _, err := s.Advance(ctx, "yard-a", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.Done(ctx, "yard-a", "", "", store.Expect{}, "me")
	if err != nil || b.Status != "closed" {
		t.Fatalf("into merged: %+v, %v", b, err)
	}
	wantMoves(t, s, "yard-a linear "+linA+" started@new", "yard-a linear "+linA+" review@review", "yard-a linear "+linA+" done@merged")

	// Sent back from review to new: new is mapped, so it is queued, and the
	// forward-only rule is the daemon's to apply when it reads the issue.
	tracked(t, s, "yard-b", "task", "review", linB)
	wantMoves(t, s, "yard-a linear "+linA+" started@new", "yard-a linear "+linA+" review@review", "yard-a linear "+linA+" done@merged",
		"yard-b linear "+linB+" review@review") // created in a mapped stage, with its link
	created, _ := s.Events(ctx, "yard-b", 5)
	ms, _ = s.IssueMoves(ctx)
	if created[0].Kind != "created" || ms[3].Seq != created[0].Seq {
		t.Fatalf("move %+v is not of the created event %+v", ms[3], created[0])
	}
}

// What queues nothing: a relates link, no link, a type or a stage the map
// has no line for, an issue of another source than the hook's, and a hook
// without a map.
func TestStageQueuesNothing(t *testing.T) {
	s, _, ctx := moveYard(t)
	nb := store.NewBead{ID: "yard-rel", Title: "x", Rig: "yard", Stage: "backlog", Links: []store.NewLink{relates(linA)}}
	if _, err := s.CreateBead(ctx, nb, "me"); err != nil {
		t.Fatal(err)
	}
	tracked(t, s, "yard-none", "task", "backlog")
	tracked(t, s, "yard-bug", "bug", "backlog", linB)
	tracked(t, s, "yard-gh", "task", "backlog", ghA)
	for _, id := range []string{"yard-rel", "yard-none", "yard-bug", "yard-gh"} {
		if _, err := s.Advance(ctx, id, "new", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	wantMoves(t, s)
	// A link made later counts from the next stage on.
	if _, err := s.AddLink(ctx, "yard-none", tracks("https://linear.app/acme/issue/YRD-3"), "me"); err != nil {
		t.Fatal(err)
	}
	wantMoves(t, s)
	if _, err := s.Advance(ctx, "yard-none", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	wantMoves(t, s, "yard-none linear https://linear.app/acme/issue/YRD-3 review@review")
	// A hook redefined without its map hears nothing more.
	putHook(t, s, store.Hook{Name: "linear", Source: "linear", SecretFile: "/s"})
	if _, err := s.Advance(ctx, "yard-none", "merged", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	wantMoves(t, s, "yard-none linear https://linear.app/acme/issue/YRD-3 review@review")
}

// An epic is a bead of type epic with its own lines: the issue it tracks
// follows it, and its children's stages move nothing. A bead tracking an
// issue in each source moves both, each through its source's hook.
func TestEpicAndTwoSources(t *testing.T) {
	s, _, ctx := moveYard(t)
	putHook(t, s, store.Hook{Name: "gh", Source: "github", SecretFile: "/s", As: "mayor",
		Map: map[string]string{"task:new": "started", "task:merged": "done"}})
	tracked(t, s, "yard-epic", "epic", "open", linA)
	wantMoves(t, s, "yard-epic linear "+linA+" started@open")
	for _, child := range []string{"yard-c1", "yard-c2"} {
		tracked(t, s, child, "task", "backlog")
		if err := s.AddDep(ctx, "yard-epic", child, store.DepParent, "me"); err != nil {
			t.Fatal(err)
		}
		for _, stage := range []string{"new", "review", "merged"} {
			if _, err := s.Advance(ctx, child, stage, store.Expect{}, "me"); err != nil {
				t.Fatal(err)
			}
		}
	}
	wantMoves(t, s, "yard-epic linear "+linA+" started@open")
	for _, stage := range []string{"review", "done"} {
		if _, err := s.Advance(ctx, "yard-epic", stage, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	wantMoves(t, s, "yard-epic linear "+linA+" started@open", "yard-epic linear "+linA+" done@done")

	tracked(t, s, "yard-two", "task", "backlog", linB, ghA)
	if _, err := s.Advance(ctx, "yard-two", "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	got := moves(t, s)[2:]
	want := []string{"yard-two gh " + ghA + " started@new", "yard-two linear " + linB + " started@new"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("moves %v, want %v", got, want)
	}
	// Removing a hook drops what it had still to do, and says so.
	if err := s.DeleteHook(ctx, "linear", "me"); err != nil {
		t.Fatal(err)
	}
	wantMoves(t, s, "yard-two gh "+ghA+" started@new")
	evs, _ := s.Events(ctx, "", 1)
	if e := evs[0]; e.Kind != "hook_removed" || fmt.Sprint(e.Data["issue_moves_dropped"]) != "3" {
		t.Fatalf("event %+v", e)
	}
}

// A move ends once, with the event that says how; a failed try only waits.
func TestIssueMoveEnds(t *testing.T) {
	s, _, ctx := moveYard(t)
	tracked(t, s, "yard-a", "task", "backlog", linA)
	for _, stage := range []string{"new", "review", "merged"} {
		if _, err := s.Advance(ctx, "yard-a", stage, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	ms, _ := s.IssueMoves(ctx)
	if len(ms) != 3 {
		t.Fatalf("moves %+v", ms)
	}
	started, review, done := ms[0], ms[1], ms[2]

	// A failed try: counted, kept, no event.
	head, _ := s.Head(ctx)
	retry := time.Date(2026, 10, 5, 12, 0, 30, 0, time.UTC)
	for i := 1; i <= 2; i++ {
		if err := s.IssueMoveFailed(ctx, started.ID, "linear: connection refused", retry); err != nil {
			t.Fatal(err)
		}
	}
	ms, _ = s.IssueMoves(ctx)
	if m := ms[0]; m.Attempts != 2 || m.Error != "linear: connection refused" || m.RetryAt == nil || !m.RetryAt.Equal(retry) {
		t.Fatalf("after two failed tries: %+v", m)
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("failed tries logged %d events", now-head)
	}

	// Moved: thread_state on the bead, by the hook.
	ts := store.ThreadState{Thread: "uuid-1", Title: "Web view", Status: "In Progress", Was: "Todo", Instead: "started"}
	if why, err := s.IssueMoveAllowed(ctx, started.ID); err != nil || why != "" {
		t.Fatalf("allowed: %q, %v", why, err)
	}
	if err := s.IssueMoved(ctx, started.ID, ts); err != nil {
		t.Fatal(err)
	}
	e := lastEvent(t, s, "yard-a")
	want := map[string]any{"hook": "linear", "resident": "mayor", "source": "linear", "url": linA, "stage": "new", "state": "started",
		"thread": "uuid-1", "title": "Web view", "status": "In Progress", "was": "Todo", "instead": "started"}
	if e.Kind != "thread_state" || e.Actor != "hook:linear" || fmt.Sprint(e.Data["cause"]) != fmt.Sprint(started.Seq) {
		t.Fatalf("event %+v", e)
	}
	for k, v := range want {
		if e.Data[k] != v {
			t.Errorf("thread_state %s = %v, want %v", k, e.Data[k], v)
		}
	}
	// Never twice.
	if err := s.IssueMoved(ctx, started.ID, ts); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("moved twice: %v", err)
	}
	if err := s.IssueMoveSkipped(ctx, started.ID, store.IssueSkip{Reason: store.SkipPast}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("skipped after moved: %v", err)
	}
	if err := s.IssueMoveFailed(ctx, started.ID, "x", retry); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed after moved: %v", err)
	}

	// Skipped, with each reason; anything else is no reason.
	if err := s.IssueMoveSkipped(ctx, review.ID, store.IssueSkip{Reason: "bored"}); err == nil || !strings.Contains(err.Error(), "no reason") {
		t.Fatalf("bad reason: %v", err)
	}
	if err := s.IssueMoved(ctx, review.ID, store.ThreadState{}); err == nil {
		t.Fatal("moved with no status")
	}
	if err := s.IssueMoveFailed(ctx, review.ID, "linear: 502", retry); err != nil {
		t.Fatal(err)
	}
	skip := store.IssueSkip{Reason: store.SkipFailed, Detail: "gave up after 2 tries: linear: 502"}
	if err := s.IssueMoveSkipped(ctx, review.ID, skip); err != nil {
		t.Fatal(err)
	}
	e = lastEvent(t, s, "yard-a")
	if e.Kind != "thread_state_skipped" || e.Actor != "hook:linear" || e.Data["reason"] != "failed" || e.Data["detail"] != skip.Detail ||
		e.Data["stage"] != "review" || e.Data["state"] != "review" || e.Data["resident"] != "mayor" || e.Data["url"] != linA ||
		fmt.Sprint(e.Data["attempts"]) != "1" || fmt.Sprint(e.Data["cause"]) != fmt.Sprint(review.Seq) {
		t.Fatalf("event %+v", e)
	}
	skip = store.IssueSkip{Reason: store.SkipPast, Detail: "the issue is Done, which is at or past done", Status: "Done", Title: "Web view", Thread: "uuid-1"}
	if err := s.IssueMoveSkipped(ctx, done.ID, skip); err != nil {
		t.Fatal(err)
	}
	e = lastEvent(t, s, "yard-a")
	if e.Kind != "thread_state_skipped" || e.Data["reason"] != "already_past" || e.Data["status"] != "Done" || e.Data["thread"] != "uuid-1" || e.Data["attempts"] != nil {
		t.Fatalf("event %+v", e)
	}
	wantMoves(t, s)
	for _, id := range []string{"x", "12", "/linear", "12/"} {
		if _, err := s.IssueMoveAllowed(ctx, id); err == nil || !strings.Contains(err.Error(), "want <event seq>/<hook>") {
			t.Errorf("id %q: %v", id, err)
		}
	}
}

// Without set_state for the source nothing may be recorded as moved, and
// the refusal names the setting; so does a resident that is gone.
func TestIssueMoveNeedsTheGrant(t *testing.T) {
	s, _, ctx := moveYard(t)
	tracked(t, s, "yard-a", "task", "new", linA)
	ms, _ := s.IssueMoves(ctx)
	id := ms[0].ID
	ts := store.ThreadState{Status: "In Progress"}
	for _, c := range []struct{ sources, want string }{
		{"", "--set set_state=linear on yardr resident add mayor"},
		{"github", "--set set_state=github,linear on yardr resident add mayor"},
	} {
		moveMayor(t, s, c.sources)
		head, _ := s.Head(ctx)
		why, err := s.IssueMoveAllowed(ctx, id)
		if err != nil || !strings.Contains(why, "resident mayor may not set the status of threads in linear") || !strings.Contains(why, c.want) {
			t.Fatalf("set_state %q: %q, %v", c.sources, why, err)
		}
		if err := s.IssueMoved(ctx, id, ts); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("set_state %q: moved: %v", c.sources, err)
		}
		if now, _ := s.Head(ctx); now != head {
			t.Fatalf("the refusal logged %d events", now-head)
		}
	}
	wantMoves(t, s, "yard-a linear "+linA+" started@new")
	if err := s.DeleteResident(ctx, "mayor", "", "me"); err != nil {
		t.Fatal(err)
	}
	if why, err := s.IssueMoveAllowed(ctx, id); err != nil || why != "resident mayor, which hook linear acts as, is gone" {
		t.Fatalf("gone: %q, %v", why, err)
	}
	if err := s.IssueMoveSkipped(ctx, id, store.IssueSkip{Reason: store.SkipNoGrant, Detail: "x"}); err != nil {
		t.Fatal(err)
	}
	if e := lastEvent(t, s, "yard-a"); e.Kind != "thread_state_skipped" || e.Data["reason"] != "no_grant" {
		t.Fatalf("event %+v", e)
	}
}

// No echo: what a hook did as a resident does not reach that resident, the
// source's delivery of the change neither, and neither queues a move.
func TestIssueMoveDoesNotEcho(t *testing.T) {
	s, own, ctx := moveYard(t)
	all := store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll}
	for _, name := range []string{"mayor", "deputy"} {
		if name == "deputy" {
			resident(t, s, name, false)
		}
		for _, sub := range []store.Subscription{all, {Source: "linear", Scope: store.ScopeAll}} {
			if err := s.AddSubscription(ctx, name, sub, "me"); err != nil {
				t.Fatal(err)
			}
		}
	}
	tracked(t, s, "yard-a", "task", "backlog", linA)
	if _, err := s.Advance(ctx, "yard-a", "new", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, "yard-a", "review", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	ms, _ := s.IssueMoves(ctx)
	if err := s.IssueMoved(ctx, ms[0].ID, store.ThreadState{Status: "In Progress"}); err != nil {
		t.Fatal(err)
	}
	reached := func(e store.Event) string {
		d, _ := e.Data["deliveries"].(map[string]any)
		out := []string{}
		for _, name := range []string{"deputy", "mayor"} {
			if _, ok := d[name]; ok {
				out = append(out, name)
			}
		}
		return strings.Join(out, ",")
	}
	if e := lastEvent(t, s, "yard-a"); e.Kind != "thread_state" || reached(e) != "deputy" {
		t.Fatalf("thread_state reached %q: %+v", reached(e), e)
	}
	// Nothing was done as the mayor when a move is skipped: it hears of it.
	if err := s.IssueMoveSkipped(ctx, ms[1].ID, store.IssueSkip{Reason: store.SkipNoGrant, Detail: "x"}); err != nil {
		t.Fatal(err)
	}
	if e := lastEvent(t, s, "yard-a"); e.Kind != "thread_state_skipped" || reached(e) != "deputy,mayor" {
		t.Fatalf("thread_state_skipped reached %q: %+v", reached(e), e)
	}
	// Linear's delivery of the change, made by the mayor's identity there.
	env := hooks.Envelope{Source: "linear", Type: "Issue", Action: "update", Actor: hooks.Party{ID: "u-mayor"},
		Thread: hooks.Thread{ID: "uuid-1", URL: linA}}
	seq, dup, err := own.RecordHook(ctx, "linear", env.Map(), []byte("{}"), nil)
	if err != nil || dup {
		t.Fatal(err, dup)
	}
	evs, _ := s.EventsSince(ctx, seq-1, 1)
	if e := evs[0]; e.Kind != "hook" || reached(e) != "deputy" {
		t.Fatalf("the delivery reached %q: %+v", reached(e), e)
	}
	wantMoves(t, s)
}

// A move outlives the process that queued it.
func TestIssueMoveSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yard.db")
	ctx := context.Background()
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutRig(ctx, store.Rig{Name: "yard", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	moveMayor(t, s, "linear")
	if err := s.SetIdentity(ctx, "mayor", "linear", store.Identity{ID: "u-mayor"}, "me"); err != nil {
		t.Fatal(err)
	}
	putHook(t, s, store.Hook{Name: "linear", Source: "linear", SecretFile: "/s", As: "mayor", Map: linearMap})
	tracked(t, s, "yard-a", "task", "new", linA)
	if err := s.IssueMoveFailed(ctx, moves0(t, s).ID, "linear: connection refused", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	again := storetest.Open(t, path)
	if m := moves0(t, again); m.Bead != "yard-a" || m.State != "started" || m.Attempts != 1 || m.Error == "" {
		t.Fatalf("after the restart: %+v", m)
	}
}

func moves0(t *testing.T, s store.API) store.IssueMove {
	t.Helper()
	ms, err := s.IssueMoves(context.Background())
	if err != nil || len(ms) != 1 {
		t.Fatalf("moves %+v, %v", ms, err)
	}
	return ms[0]
}
