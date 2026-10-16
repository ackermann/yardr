package store_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
)

// A move that expects a session or a stage the bead no longer has is
// refused and changes nothing: an agent kept or orphaned after its bead
// moved on cannot end the session that owns it now, and a decision made on
// an old snapshot is not applied. Without an expectation (a person acting
// outside any session) nothing is checked.
func TestStaleMovesRefused(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	for _, g := range []string{"builders", "reviewers"} {
		if err := s.PutGroup(ctx, store.Group{Name: g, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Done(ctx, b.ID, "", "", store.Expect{Session: builder.ID}, "builder"); err != nil {
		t.Fatalf("owner's own done refused: %v", err)
	}
	reviewer, err := s.Claim(ctx, b.ID, "reviewers", "review", "yard")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetBead(ctx, b.ID)

	// The builder's session ended with its done; a second call from it
	// (kept tab, orphaned agent) must not touch the reviewer's work.
	old := store.Expect{Session: builder.ID}
	stale := map[string]func() (*store.Bead, error){
		"done":          func() (*store.Bead, error) { return s.Done(ctx, b.ID, "", "again", old, "builder") },
		"advance":       func() (*store.Bead, error) { return s.AdvanceNote(ctx, b.ID, "decide", "again", old, "builder") },
		"close":         func() (*store.Bead, error) { return s.CloseBead(ctx, b.ID, "done", old, "builder") },
		"plain advance": func() (*store.Bead, error) { return s.Advance(ctx, b.ID, "decide", old, "builder") },
		"release":       func() (*store.Bead, error) { return s.Release(ctx, b.ID, "giving up", old, "builder") },
		"reopen":        func() (*store.Bead, error) { return s.Reopen(ctx, b.ID, old, "builder") },
		// A review form that snapshotted the bead in new.
		"stage": func() (*store.Bead, error) {
			return s.AdvanceNote(ctx, b.ID, "backlog", "", store.Expect{Stage: "new"}, "mckean")
		},
		"stage close": func() (*store.Bead, error) { return s.CloseBead(ctx, b.ID, "x", store.Expect{Stage: "new"}, "mckean") },
	}
	for name, move := range stale {
		_, err := move()
		want := "stale: bead " + b.ID + " is now in review owned by " + reviewer.ID
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
	}
	if cur, _ := s.GetBead(ctx, b.ID); cur.Revision != before.Revision {
		t.Fatalf("a refused move changed the bead: %+v", cur)
	}
	if notes, _ := s.Notes(ctx, b.ID); len(notes) != 0 {
		t.Fatalf("a refused move left a note: %+v", notes)
	}
	if sess, _ := s.GetSession(ctx, reviewer.ID); sess.EndedAt != nil {
		t.Fatalf("a refused move ended the owner's session: %+v", sess)
	}

	// The owner, expecting what is true, moves it.
	got, err := s.Done(ctx, b.ID, "changes", "", store.Expect{Session: reviewer.ID, Stage: "review"}, "reviewer")
	if err != nil || got.Stage != "new" {
		t.Fatalf("owner's done = %+v, %v", got, err)
	}

	// A person with no session acts on an owned bead unchecked.
	third, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if got, err = s.CloseBead(ctx, b.ID, "dropped", store.Expect{}, "yardmaster"); err != nil || got.Status != "closed" {
		t.Fatalf("yardmaster's close = %+v, %v", got, err)
	}
	if sess, _ := s.GetSession(ctx, third.ID); sess.EndedAt == nil {
		t.Fatal("close did not end the owning session")
	}
	// Closing again, or reopening, from that ended session is stale too.
	_, err = s.CloseBead(ctx, b.ID, "done", store.Expect{Session: third.ID}, "builder")
	if err == nil || !strings.Contains(err.Error(), "is now in new (closed) owned by no session") {
		t.Fatalf("close from an ended session: %v", err)
	}
	_, err = s.Reopen(ctx, b.ID, store.Expect{Session: third.ID}, "builder")
	if err == nil || !strings.Contains(err.Error(), "is now in new (closed) owned by no session") {
		t.Fatalf("reopen from an ended session: %v", err)
	}
	if cur, _ := s.GetBead(ctx, b.ID); cur.Status != "closed" {
		t.Fatalf("a stale reopen opened the bead: %+v", cur)
	}

	// A session this yard never started is refused outright, owner or not.
	_, err = s.Reopen(ctx, b.ID, store.Expect{Session: "forged"}, "builder")
	if !errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "session forged: not found") {
		t.Fatalf("reopen from an unknown session: %v", err)
	}
}

// A call from a session's pane is checked as that session whatever its
// environment says: the store knows the pane by the pid its runner put on
// the handle (a herdr tab's shell here), the call's process descends from
// it, and the bead the session is expected to own comes from the store's
// record of the session. Unsetting $YARDR_SESSION in the pane is therefore
// not "no check", and naming another session there is refused.
func TestPaneIsCheckedAsItsSessionWhateverItSays(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	for _, g := range []string{"builders", "reviewers"} {
		if err := s.PutGroup(ctx, store.Group{Name: g, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Depot: "yard", Stage: "review"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Claim(ctx, a.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Claim(ctx, b.ID, "reviewers", "review", "yard")
	if err != nil {
		t.Fatal(err)
	}
	// This test process is the shell of a's tab: in the owning process the
	// caller is told to the store; over the socket the server sees it.
	me := os.Getpid()
	handle := map[string]any{"agent": "yard-a-new", "shell_pid": me}
	if st, err := peer.Describe(me); err == nil {
		handle["shell_start"] = st.Start
	}
	if err := s.MarkRunning(ctx, owner.ID, handle); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	unset := store.Expect{}

	// With no session in its environment, the pane is still a's session:
	// b, another live session's bead, is refused as not its own.
	want := "stale: bead " + b.ID + " is now in review owned by " + other.ID + "; expected no session (session " + owner.ID + " owns " + a.ID
	for name, move := range map[string]func() (*store.Bead, error){
		"release": func() (*store.Bead, error) { return s.Release(pane, b.ID, "mine now", unset, "yard-a-new") },
		"advance": func() (*store.Bead, error) { return s.Advance(pane, b.ID, "new", unset, "yard-a-new") },
		"done":    func() (*store.Bead, error) { return s.Done(pane, b.ID, "", "", unset, "yard-a-new") },
	} {
		if _, err := move(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s of another session's bead from a pane with no $YARDR_SESSION: %v, want %q", name, err, want)
		}
	}
	if sess, _ := s.GetSession(ctx, other.ID); sess.EndedAt != nil {
		t.Fatalf("a refused move ended the owner's session: %+v", sess)
	}
	// Naming b's session from a's pane does not make the pane b's.
	_, err = s.Done(pane, b.ID, "", "", store.Expect{Session: other.ID}, "yard-a-new")
	if err == nil || !strings.Contains(err.Error(), "this pane is session "+owner.ID+", not "+other.ID) {
		t.Fatalf("another session named from the pane: %v", err)
	}
	// Its own bead the pane moves, with or without its environment.
	if got, err := s.Done(pane, a.ID, "", "", unset, "yard-a-new"); err != nil || got.Stage != "review" {
		t.Fatalf("the pane's own done = %+v, %v", got, err)
	}
	// The session has ended, its shell still runs (an agent kept open):
	// the pane is still that session's, and its bead has moved on.
	_, err = s.Release(pane, a.ID, "again", unset, "yard-a-new")
	if err == nil || !strings.Contains(err.Error(), "stale: bead "+a.ID+" is now in review owned by no session; expected session "+owner.ID) {
		t.Fatalf("release from the ended session's pane: %v", err)
	}
}

// The pane's process may be far above the caller (shell, agent, a shell
// the agent runs a command in): the store walks up the process tree. An
// exec runner's script is found by the pid and start time it recorded.
func TestPaneIsFoundUpTheProcessTree(t *testing.T) {
	t.Parallel()
	if len(peer.Ancestors(os.Getpid())) < 2 {
		t.Skip("this OS does not tell a process's parent")
	}
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "integrators", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Claim(ctx, a.ID, "integrators", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	// The script is this process's parent; this process is a command it runs.
	script := os.Getppid()
	handle := map[string]any{"pid": script}
	if st, err := peer.Describe(script); err == nil {
		handle["start"] = st.Start
	}
	if err := s.MarkRunning(ctx, owner.ID, handle); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, os.Getpid())
	if _, err := s.Done(pane, a.ID, "", "", store.Expect{}, "integrators"); err != nil {
		t.Fatalf("the script's own done: %v", err)
	}
	_, err = s.Release(pane, a.ID, "again", store.Expect{}, "integrators")
	if err == nil || !strings.Contains(err.Error(), "expected session "+owner.ID) {
		t.Fatalf("release from the ended script's pane: %v", err)
	}
}

// The session check is for the bead the session was started for: another
// bead moved from the same pane (a reviewer advancing a follow-up it filed,
// the yardmaster from its own pane) is checked as the actor's, like a person at
// a shell, except that one owned by a live session is refused rather than
// taken from under its agent.
func TestSessionCheckIsForTheSessionsOwnBead(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	for _, g := range []string{"builders", "reviewers"} {
		if err := s.PutGroup(ctx, store.Group{Name: g, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Claim(ctx, a.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	// What a's pane sends: its session; the bead it was started for the
	// store knows from its own record of the session.
	pane := store.Expect{Session: owner.ID}

	// b is owned by no session: a's pane moves it, as the actor.
	got, err := s.AdvanceNote(ctx, b.ID, "review", "filed from a", pane, "yard-a-new")
	if err != nil || got.Stage != "review" {
		t.Fatalf("advance of an unowned bead from another bead's pane = %+v, %v", got, err)
	}
	events, _ := s.Events(ctx, b.ID, 1)
	if len(events) != 1 || events[0].Kind != "advanced" || events[0].Actor != "yard-a-new" {
		t.Fatalf("the move is not the actor's: %+v", events)
	}
	if cur, _ := s.GetBead(ctx, a.ID); cur.Session == nil || *cur.Session != owner.ID {
		t.Fatalf("moving b touched a's session: %+v", cur)
	}

	// b owned by a live session: refused, and nothing changes.
	other, err := s.Claim(ctx, b.ID, "reviewers", "review", "yard")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetBead(ctx, b.ID)
	want := "stale: bead " + b.ID + " is now in review owned by " + other.ID + "; expected no session (session " + owner.ID + " owns " + a.ID
	for name, move := range map[string]func() (*store.Bead, error){
		"advance": func() (*store.Bead, error) { return s.AdvanceNote(ctx, b.ID, "new", "again", pane, "yard-a-new") },
		"done":    func() (*store.Bead, error) { return s.Done(ctx, b.ID, "", "again", pane, "yard-a-new") },
		"close":   func() (*store.Bead, error) { return s.CloseBead(ctx, b.ID, "x", pane, "yard-a-new") },
		"hold":    func() (*store.Bead, error) { return s.SetHold(ctx, b.ID, true, pane, "yard-a-new") },
	} {
		if _, err := move(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s of a bead another session owns: %v, want %q", name, err, want)
		}
	}
	if cur, _ := s.GetBead(ctx, b.ID); cur.Revision != before.Revision {
		t.Fatalf("a refused move changed the bead: %+v", cur)
	}
	if sess, _ := s.GetSession(ctx, other.ID); sess.EndedAt != nil {
		t.Fatalf("a refused move ended the owner's session: %+v", sess)
	}

	// Its own bead the session still moves, and is still refused on once
	// the bead has moved on.
	if _, err := s.Done(ctx, a.ID, "", "", pane, "yard-a-new"); err != nil {
		t.Fatalf("the owner's own done: %v", err)
	}
	if _, err := s.Done(ctx, a.ID, "", "again", pane, "yard-a-new"); err == nil || !strings.Contains(err.Error(), "owned by no session; expected session "+owner.ID) {
		t.Fatalf("done again from the ended session: %v", err)
	}
}

// A pane is its session's only while the process the runner recorded still
// runs: the pid of a shell that closed under a live session (before the
// daemon noticed) may already be someone else's, and that someone is not
// checked as the session.
func TestReusedPidIsNobodysPane(t *testing.T) {
	t.Parallel()
	if !peer.HaveStat() {
		t.Skip("this OS reports no process start time")
	}
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	for _, g := range []string{"builders", "reviewers"} {
		if err := s.PutGroup(ctx, store.Group{Name: g, Runner: "herdr"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Depot: "yard", Stage: "review"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Claim(ctx, a.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, b.ID, "reviewers", "review", "yard"); err != nil {
		t.Fatal(err)
	}
	// a's live session recorded this pid as its shell, started at another
	// time: this process is a later one with the pid, not the pane.
	me := os.Getpid()
	st, err := peer.Describe(me)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRunning(ctx, owner.ID, map[string]any{"agent": "yard-a-new", "shell_pid": me, "shell_start": st.Start + 1}); err != nil {
		t.Fatal(err)
	}
	// Naming the session is checked as it, pane or not: b is another live
	// session's bead, not the named session's own.
	pane := store.WithCaller(ctx, me)
	_, err = s.Done(pane, b.ID, "", "", store.Expect{Session: owner.ID}, "mckean")
	if err == nil || !strings.Contains(err.Error(), "expected no session (session "+owner.ID+" owns "+a.ID) {
		t.Fatalf("naming the session from a process that is not its pane: %v", err)
	}
	// Naming none, the same move from a's pane would be refused the same
	// way (TestPaneIsCheckedAsItsSessionWhateverItSays); from a process that
	// only has the pane's old pid it is a person's, checked as the actor's
	// alone.
	if _, err := s.Release(pane, b.ID, "not that pane", store.Expect{}, "mckean"); err != nil {
		t.Fatalf("release from a process with a dead pane's pid: %v", err)
	}
}
