package store_test

import (
	"errors"
	"os"
	"slices"
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

// merge-ok is the yard's permission to merge a pull request (merge-pr
// merges only what carries it): a bead's agent says it on its own bead only,
// whatever actor the command names, which is the reviewer's part for a bot's
// bump (merge-pr merges that at the head it has). merge_head, the commit a
// maintainer's yes recorded, it does not say at all, its own bead included.
// reviewed_head, the commit its reviewer read, which pr-ci tests and no
// other, it says on its own bead only. ci_head, the commit the yard's CI
// passed on, and accepted_head, the commit a maintainer accepted, it does not
// say at all. Anyone else (no pane at all) is checked as before, and any other
// label or metadata still goes anywhere.
func TestMergeOKIsTheOwningSessionToSay(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateBead(ctx, store.NewBead{Title: "c", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sha := "9f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b"
	if got, err := s.CreateBead(ctx, store.NewBead{
		ID: "yard-person-pr", Title: "person", Depot: "yard", Stage: "new",
		Labels: []string{"merge-ok"}, Metadata: map[string]any{"merge_head": sha, "ci_head": sha, "reviewed_head": sha, "accepted_head": sha},
	}, "me"); err != nil {
		t.Fatalf("a person creating a bead with merge permission, CI verdict, reviewed head and accepted head: %v", err)
	} else if !slices.Contains(got.Labels, "merge-ok") || got.Metadata["merge_head"] != sha || got.Metadata["ci_head"] != sha || got.Metadata["reviewed_head"] != sha || got.Metadata["accepted_head"] != sha {
		t.Fatalf("a person's merge permission, CI verdict, reviewed head and accepted head were not stored: %+v", got)
	}
	if got, err := s.UpdateBead(ctx, c.ID, store.BeadPatch{Metadata: map[string]any{"merge_head": sha, "ci_head": sha, "reviewed_head": sha, "accepted_head": sha}}, store.Expect{}, "me"); err != nil || got.Metadata["merge_head"] != sha || got.Metadata["ci_head"] != sha || got.Metadata["reviewed_head"] != sha || got.Metadata["accepted_head"] != sha {
		t.Fatalf("a person's merge_head, ci_head, reviewed_head and accepted_head: %+v, %v", got, err)
	}
	owner, err := s.Claim(ctx, a.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	// c carries the label before the pane is on record: over the socket
	// the server reads the caller from its peer, so past MarkRunning
	// every call of this process is the pane's.
	if _, err := s.UpdateBead(ctx, c.ID, store.BeadPatch{AddLabels: []string{"merge-ok"}}, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// This test process is the shell of a's tab.
	if err := s.MarkRunning(ctx, owner.ID, paneShell(t, "yard-a-new")); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, os.Getpid())
	unset := store.Expect{}
	want := "may say merge-ok only on its own bead"

	// Creating a bead directly with a permission, verdict, reviewed head or
	// accepted head would skip the update guards and could make merge-pr trust a
	// head nobody reviewed or ran, or pr-ci run one nobody read or accepted.
	for i, tc := range []struct {
		name string
		nb   store.NewBead
		want string
	}{
		{name: "merge-ok", nb: store.NewBead{Labels: []string{"merge-ok"}}, want: "may not create a bead with merge-ok"},
		{name: "merge_head", nb: store.NewBead{Metadata: map[string]any{"merge_head": sha}}, want: "may not create a bead with merge_head"},
		{name: "ci_head", nb: store.NewBead{Metadata: map[string]any{"ci_head": sha}}, want: "may not create a bead with ci_head"},
		{name: "reviewed_head", nb: store.NewBead{Metadata: map[string]any{"reviewed_head": sha}}, want: "may not create a bead with reviewed_head"},
		{name: "accepted_head", nb: store.NewBead{Metadata: map[string]any{"accepted_head": sha}}, want: "may not create a bead with accepted_head"},
	} {
		id := []string{"yard-forged-merge-ok", "yard-forged-merge-head", "yard-forged-ci-head", "yard-forged-reviewed-head", "yard-forged-accepted-head"}[i]
		tc.nb.ID, tc.nb.Title, tc.nb.Depot, tc.nb.Stage = id, "forged", "yard", "new"
		if _, err := s.CreateBead(pane, tc.nb, "yard-a-new"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("agent created a bead with %s: %v, want refusal %q", tc.name, err, tc.want)
		}
		if _, err := s.GetBead(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("refused creation with %s left a bead behind: %v", tc.name, err)
		}
	}
	// Granting the permission on another bead is refused, and recording the
	// yes on any bead, the agent's own included, together with the label or
	// without it, and nothing changes.
	noYes := "may not record merge_head"
	for _, tc := range []struct {
		name  string
		bead  string
		patch store.BeadPatch
		want  string
	}{
		{"grant on another bead", b.ID, store.BeadPatch{AddLabels: []string{"merge-ok"}}, want},
		{"yes on another bead", b.ID, store.BeadPatch{Metadata: map[string]any{"merge_head": sha}}, noYes},
		{"yes on its own bead", a.ID, store.BeadPatch{Metadata: map[string]any{"merge_head": sha}}, noYes},
		{"grant with the yes on its own bead", a.ID, store.BeadPatch{AddLabels: []string{"merge-ok"}, Metadata: map[string]any{"merge_head": sha}}, noYes},
	} {
		if _, err := s.UpdateBead(pane, tc.bead, tc.patch, unset, "yard-a-new"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s from its pane: %v, want %q", tc.name, err, tc.want)
		}
	}
	for _, id := range []string{a.ID, b.ID} {
		if got, _ := s.GetBead(ctx, id); len(got.Labels) != 0 || len(got.Metadata) != 0 {
			t.Fatalf("a refused merge-ok or merge_head changed %s: %+v", id, got)
		}
	}
	// Taking it is refused the same way, and it stays on.
	if _, err := s.UpdateBead(pane, c.ID, store.BeadPatch{RemoveLabel: []string{"merge-ok"}}, unset, "yard-a-new"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ungrant of merge-ok on another bead from its pane: %v, want %q", err, want)
	}
	if got, _ := s.GetBead(ctx, c.ID); !slices.Contains(got.Labels, "merge-ok") {
		t.Fatalf("a refused ungrant took merge-ok off: %+v", got)
	}
	// Its own bead the pane says the label on, which is the reviewer's whole
	// part for a bot's bump, and anyone's pane still writes any other label
	// or metadata anywhere.
	if got, err := s.UpdateBead(pane, a.ID, store.BeadPatch{AddLabels: []string{"merge-ok"}}, unset, "yard-a-new"); err != nil {
		t.Fatalf("the pane's own merge-ok: %v", err)
	} else if !slices.Contains(got.Labels, "merge-ok") || got.Metadata["merge_head"] != nil {
		t.Fatalf("the pane's own merge-ok is not on the bead alone: %+v", got)
	}
	if _, err := s.UpdateBead(pane, b.ID, store.BeadPatch{AddLabels: []string{"topic:x"}, Metadata: map[string]any{"seen": true}}, unset, "yard-a-new"); err != nil {
		t.Fatalf("another label from the pane: %v", err)
	}

	// reviewed_head, the head pr-ci tests, is the reviewer's to record on the
	// pull request it read: refused on another bead, accepted on its own, and
	// again over a head already there (a bead that came back to be read at a
	// new head).
	if _, err := s.UpdateBead(pane, b.ID, store.BeadPatch{Metadata: map[string]any{"reviewed_head": sha}}, unset, "yard-a-new"); err == nil || !strings.Contains(err.Error(), "may record reviewed_head only on its own bead") {
		t.Errorf("reviewed_head on another bead from its pane: %v, want it refused", err)
	}
	if got, _ := s.GetBead(ctx, b.ID); got.Metadata["reviewed_head"] != nil {
		t.Fatalf("a refused reviewed_head changed another bead: %+v", got)
	}
	newer := "0a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d3"
	for _, head := range []string{sha, newer} {
		if got, err := s.UpdateBead(pane, a.ID, store.BeadPatch{Metadata: map[string]any{"reviewed_head": head}}, unset, "yard-a-new"); err != nil {
			t.Fatalf("the pane's own reviewed_head %s: %v", head, err)
		} else if got.Metadata["reviewed_head"] != head {
			t.Fatalf("the pane's own reviewed_head %s is not on the bead: %+v", head, got)
		}
	}

	// ci_head, the head the yard's CI passed on, which merge-pr merges on as
	// it finds it, is the script's and a person's to write: the pane may not
	// record it anywhere, its own bead included (it would be the agent that
	// reads a pull request's text deciding that the tests passed).
	for _, id := range []string{a.ID, b.ID} {
		if _, err := s.UpdateBead(pane, id, store.BeadPatch{Metadata: map[string]any{"ci_head": sha}}, unset, "yard-a-new"); err == nil || !strings.Contains(err.Error(), "may not record ci_head") {
			t.Errorf("ci_head on %s from a bead's pane: %v, want it refused", id, err)
		}
		if got, _ := s.GetBead(ctx, id); got.Metadata["ci_head"] != nil {
			t.Fatalf("a refused ci_head changed %s: %+v", id, got)
		}
	}

	// accepted_head, the head a maintainer accepted, which pr-ci tests a pull
	// request that waited in triage at and no other, is a maintainer's and the
	// dispatcher's to write for the source's word: the pane may not record it
	// anywhere either, its own bead included.
	for _, id := range []string{a.ID, b.ID} {
		if _, err := s.UpdateBead(pane, id, store.BeadPatch{Metadata: map[string]any{"accepted_head": sha}}, unset, "yard-a-new"); err == nil || !strings.Contains(err.Error(), "may not record accepted_head") {
			t.Errorf("accepted_head on %s from a bead's pane: %v, want it refused", id, err)
		}
		if got, _ := s.GetBead(ctx, id); got.Metadata["accepted_head"] != nil {
			t.Fatalf("a refused accepted_head changed %s: %+v", id, got)
		}
	}
}
