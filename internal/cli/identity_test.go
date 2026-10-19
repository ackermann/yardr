package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// worktreeOf makes a checkout that yardr started ws in, as the herdr runner
// leaves a bead's worktree, and returns its path.
func worktreeOf(t *testing.T, ws runner.WorktreeSession) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	briefs, err := runner.BriefDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ws)
	if err := os.MkdirAll(briefs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(briefs, "session.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// twoBuilders is reviewYard with room for two builders at once, and a flow
// whose new has one way out, so that done reports it.
func twoBuilders(t *testing.T) (store.API, context.Context) {
	t.Helper()
	st, ctx := reviewYard(t, "/tmp/yard")
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("new -> review\nreview -> approved\napproved terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "yard"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	return st, ctx
}

// errOutput is the buffer a test hands its app as the error stream, for
// the identity line: a run through the root records its own, a direct
// subcommand call reads the app's.
func errOutput(t *testing.T) *bytes.Buffer {
	t.Helper()
	return &bytes.Buffer{}
}

// yardr-8bhk: Codex ran every session's commands in one background server,
// with the environment of the session that started it. A note and a done
// run in bead B's worktree with session A's YARDR_ACTOR, YARDR_SESSION and
// YARDR_BEAD are B's all the same: attributed to B's agent and accepted as
// B's session, with a line on stderr saying whose environment it was.
func TestWorktreeNamesTheSessionWhenTheEnvironmentIsAnothers(t *testing.T) {
	st, ctx := twoBuilders(t)
	beadA, err := st.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	beadB, err := st.CreateBead(ctx, store.NewBead{Title: "b", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sessA, err := st.Claim(ctx, beadA.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	sessB, err := st.Claim(ctx, beadB.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	actorA, actorB := beadA.ID+"-new", beadB.ID+"-new"
	t.Setenv("YARDR_ACTOR", actorA)
	t.Setenv("YARDR_SESSION", sessA.ID)
	t.Setenv("YARDR_BEAD", beadA.ID)
	t.Chdir(worktreeOf(t, runner.WorktreeSession{Bead: beadB.ID, Session: sessB.ID, Actor: actorB, Stage: "new", Group: "builders"}))
	errs := errOutput(t)

	a := &app{st: st, stderr: errs}
	if _, err := run(t, a.beadCmd(), "note", beadB.ID, "from b"); err != nil {
		t.Fatalf("note in b's worktree: %v", err)
	}
	notes, _ := st.Notes(ctx, beadB.ID)
	if len(notes) != 1 || notes[0].Author != actorB {
		t.Fatalf("the note is not b's: %+v", notes)
	}
	for _, want := range []string{actorA, beadA.ID, sessA.ID, beadB.ID, "acting as " + actorB, sessB.ID} {
		if !strings.Contains(errs.String(), want) {
			t.Errorf("stderr does not name %q: %s", want, errs.String())
		}
	}

	// Without the worktree this done is refused: session A does not own B.
	if _, err := run(t, (&app{st: st, stderr: errs}).beadCmd(), "done", beadB.ID); err != nil {
		t.Fatalf("done in b's worktree: %v", err)
	}
	b, _ := st.GetBead(ctx, beadB.ID)
	if b.Stage != "review" {
		t.Fatalf("b is in %s, want review", b.Stage)
	}
	events, _ := st.Events(ctx, beadB.ID, 1)
	if len(events) != 1 || events[0].Actor != actorB {
		t.Fatalf("the move is not b's: %+v", events)
	}
	// A's own bead and session are untouched.
	if cur, _ := st.GetBead(ctx, beadA.ID); cur.Stage != "new" || cur.Session == nil || *cur.Session != sessA.ID {
		t.Fatalf("a changed: %+v", cur)
	}
}

// The control for the test above: the same commands outside B's worktree
// act as the environment says, so the done is refused as another session's.
func TestEnvironmentNamesTheSessionOutsideAWorktree(t *testing.T) {
	st, ctx := twoBuilders(t)
	beadA, _ := st.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "me")
	beadB, _ := st.CreateBead(ctx, store.NewBead{Title: "b", Depot: "yard", Stage: "new"}, "me")
	sessA, err := st.Claim(ctx, beadA.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	sessB, err := st.Claim(ctx, beadB.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("YARDR_ACTOR", beadA.ID+"-new")
	t.Setenv("YARDR_SESSION", sessA.ID)
	t.Setenv("YARDR_BEAD", beadA.ID)
	t.Chdir(t.TempDir())
	errs := errOutput(t)

	want := "stale: bead " + beadB.ID + " is now in new owned by " + sessB.ID
	if _, err := run(t, (&app{st: st, stderr: errs}).beadCmd(), "done", beadB.ID); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("done of b as session a: %v, want %q", err, want)
	}
	if errs.Len() != 0 {
		t.Fatalf("stderr = %q", errs.String())
	}
}

// Only another bead's environment gives way to the worktree's session. An
// earlier stage's agent left open in the worktree names the same bead and
// stays itself, so its move is still refused as stale; and someone with no
// bead in the environment (a person, a crew member) acts as themselves there.
func TestWorktreeLeavesTheBeadsOwnEnvironmentAlone(t *testing.T) {
	st, ctx := reviewYard(t, "/tmp/yard")
	if err := st.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, _ := st.CreateBead(ctx, store.NewBead{Title: "b", Depot: "yard", Stage: "new"}, "me")
	builder, err := st.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Advance(ctx, b.ID, "review", store.Expect{Session: builder.ID}, b.ID+"-new"); err != nil {
		t.Fatal(err)
	}
	reviewer, err := st.Claim(ctx, b.ID, "reviewers", "review", "yard")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(worktreeOf(t, runner.WorktreeSession{Bead: b.ID, Session: reviewer.ID, Actor: b.ID + "-review", Stage: "review", Group: "reviewers"}))
	errs := errOutput(t)

	t.Setenv("YARDR_ACTOR", b.ID+"-new")
	t.Setenv("YARDR_SESSION", builder.ID)
	t.Setenv("YARDR_BEAD", b.ID)
	want := "stale: bead " + b.ID + " is now in review owned by " + reviewer.ID
	if _, err := run(t, (&app{st: st, stderr: errs}).beadCmd(), "done", b.ID); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("done from the builder left open: %v, want %q", err, want)
	}

	t.Setenv("YARDR_ACTOR", "yardmaster")
	t.Setenv("YARDR_SESSION", "")
	t.Setenv("YARDR_BEAD", "")
	if _, err := run(t, (&app{st: st, stderr: errs}).beadCmd(), "note", b.ID, "looked in"); err != nil {
		t.Fatal(err)
	}
	notes, _ := st.Notes(ctx, b.ID)
	if len(notes) != 1 || notes[0].Author != "yardmaster" {
		t.Fatalf("the note is not the yardmaster's: %+v", notes)
	}
	if errs.Len() != 0 {
		t.Fatalf("stderr = %q", errs.String())
	}
}

// The session the run acts as goes with every call over the socket, so a
// call made before its session is on record is refused as its session's.
func TestStoreSendsTheRunsSession(t *testing.T) {
	newHome(t)
	t.Setenv("YARDR_SESSION", "s1")
	old := connect
	connect = func(context.Context, *config.Config) (store.API, error) {
		return rpc.NewClient(filepath.Join(t.TempDir(), "s")), nil
	}
	t.Cleanup(func() { connect = old })
	st, err := (&app{}).store(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c, ok := st.(*rpc.Client)
	if !ok || c.Session != "s1" {
		t.Fatalf("the store = %#v, want a client sending session s1", st)
	}
}

// A successful pre-run actor lookup is the actor for the whole command.
// Re-resolving it in who() could time out on a later OS lookup and silently
// attribute an operation to "unknown" after the command had passed pre-run.
func TestPreRunActorIsStableForTheCommand(t *testing.T) {
	t.Setenv("YARDR_ACTOR", "")
	t.Setenv("YARDR_SESSION", "")
	t.Setenv("YARDR_BEAD", "")
	t.Setenv("USER", "first")
	a := &app{}
	root := a.rootCmd()
	if err := root.Flags().Set("version", "true"); err != nil {
		t.Fatal(err)
	}
	if err := root.PersistentPreRunE(root, nil); err != nil {
		t.Fatal(err)
	}

	t.Setenv("USER", "second")
	if got := a.who(); got != "first" {
		t.Fatalf("who() after pre-run = %q, want the actor resolved at pre-run", got)
	}
}
