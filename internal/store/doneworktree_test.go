package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A session's done is refused while its worktree has uncommitted changes:
// the refusal names each changed file and the three ways out. Once the
// worktree reads clean the same done passes. The refused done moves
// nothing and leaves no note behind.
func TestDoneRefusedWhileWorktreeDirty(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	dir := gitRepo(t, map[string]string{"main.go": "package main\n"})
	if err := s.SetHandle(ctx, builder.ID, map[string]any{"worktree": dir}); err != nil {
		t.Fatal(err)
	}
	mine := store.Expect{Session: builder.ID}

	// An untracked scratch file, the case that held two landings: named,
	// with the three ways out.
	if err := os.WriteFile(filepath.Join(dir, "scratch.py"), []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = s.Done(ctx, b.ID, "", "built it", mine, "builder")
	if err == nil {
		t.Fatal("done with an untracked file passed")
	}
	for _, want := range []string{
		"bead " + b.ID, "uncommitted changes", `"scratch.py"`,
		"commit them", "remove them", "attach them to a note",
		"yardr bead note " + b.ID + " --file ", "try again",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
	if cur, _ := s.GetBead(ctx, b.ID); cur.Stage != "new" {
		t.Fatalf("refused done moved the bead to %s", cur.Stage)
	}
	if notes, _ := s.Notes(ctx, b.ID); len(notes) != 0 {
		t.Fatalf("refused done left a note: %+v", notes)
	}

	// A modified tracked file refuses too, and committing it passes.
	if err := os.Remove(filepath.Join(dir, "scratch.py")); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "scratch")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.py", "two.py"} {
		if err := os.WriteFile(filepath.Join(nested, name), []byte("print(1)\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.Done(ctx, b.ID, "", "", mine, "builder")
	if err == nil {
		t.Fatal("done with untracked files in a directory passed")
	}
	for _, want := range []string{`"scratch/one.py"`, `"scratch/two.py"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name nested file %s", err, want)
		}
	}
	if err := os.RemoveAll(nested); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Done(ctx, b.ID, "", "", mine, "builder"); err == nil {
		t.Fatal("done with a modified tracked file passed")
	} else if !strings.Contains(err.Error(), `"main.go"`) {
		t.Fatalf("refusal %q does not name the modified file", err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "the work")
	got, err := s.Done(ctx, b.ID, "", "built it", mine, "builder")
	if err != nil || got.Stage != "review" {
		t.Fatalf("done with a clean worktree = %+v, %v", got, err)
	}
}

// Only the session's own done is gated by its worktree: a person moving
// the bead, another bead's session, a session whose handle names no
// worktree, and a session of a runner with no worktree of its own (exec,
// manual, peer) are not refused.
func TestDoneWorktreeGatesOnlyTheSessionsOwnDone(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	dirty := gitRepo(t, map[string]string{"main.go": "package main\n"})
	if err := os.WriteFile(filepath.Join(dirty, "scratch.py"), []byte("print(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetHandle(ctx, builder.ID, map[string]any{"worktree": dirty}); err != nil {
		t.Fatal(err)
	}

	// Another bead's session is not gated by its own dirty worktree.
	other, err := s.CreateBead(ctx, store.NewBead{Title: "y", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Done(ctx, other.ID, "", "", store.Expect{Session: builder.ID}, "builder")
	if err != nil || got.Stage != "review" {
		t.Fatalf("another bead's done = %+v, %v", got, err)
	}

	// The operator's done on the same bead is not refused either.
	third, err := s.CreateBead(ctx, store.NewBead{Title: "z", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.Done(ctx, third.ID, "", "", store.Expect{}, "mckean")
	if err != nil || got.Stage != "review" {
		t.Fatalf("operator's done = %+v, %v", got, err)
	}

	// A session whose handle names no worktree is not checked.
	plain, err := s.CreateBead(ctx, store.NewBead{Title: "w", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "crafters", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	worker, err := s.Claim(ctx, plain.ID, "crafters", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.Done(ctx, plain.ID, "", "", store.Expect{Session: worker.ID}, "builder")
	if err != nil || got.Stage != "review" {
		t.Fatalf("done with no worktree in the handle = %+v, %v", got, err)
	}

	if _, err := s.AddPeer(ctx, store.Peer{Name: "north", Key: otherKey(t)}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllowPeer(ctx, "north", store.PeerAllow{Send: []string{"bead"}}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, runner := range []string{"exec", "manual", "peer"} {
		group := runner + "-group"
		g := store.Group{Name: group, Runner: runner}
		switch runner {
		case "exec":
			g.Config = map[string]any{"cmd": "true"}
		case "peer":
			g.Config = map[string]any{"peer": "north"}
		}
		if err := s.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
		rb, err := s.CreateBead(ctx, store.NewBead{Title: runner, Depot: "yard", Stage: "new"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, rb.ID, group, "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetHandle(ctx, sess.ID, map[string]any{"worktree": dirty}); err != nil {
			t.Fatal(err)
		}
		got, err := s.Done(ctx, rb.ID, "", "", store.Expect{Session: sess.ID}, runner)
		if err != nil || got.Stage != "review" {
			t.Errorf("%s session's done = %+v, %v", runner, got, err)
		}
	}
}

// The refusal names the first twenty changed files and counts the rest,
// so a worktree with hundreds of them still refuses in one screen.
func TestDoneWorktreeRefusalCapsAtTwenty(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	dir := gitRepo(t, map[string]string{"main.go": "package main\n"})
	for i := range 22 {
		name := filepath.Join(dir, "scratch.go")
		if i > 0 {
			// Beside the first, not under a new directory, which
			// porcelain would collapse to one entry.
			name = filepath.Join(dir, strings.Repeat("s", i)+".go")
		}
		if err := os.WriteFile(name, []byte("package scratch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetHandle(ctx, builder.ID, map[string]any{"worktree": dir}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Done(ctx, b.ID, "", "", store.Expect{Session: builder.ID}, "builder")
	if err == nil {
		t.Fatal("done with 22 untracked files passed")
	}
	for _, want := range []string{"(22 files)", `"scratch.go"`, "and 2 more"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
}

// A worktree git cannot read holds the done too: a done whose work
// cannot be shown to be committed is not the session's to report. The
// refusal names the worktree and why it could not be read.
func TestDoneWorktreeUnreadableHoldsDone(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "worktree")
	if err := s.SetHandle(ctx, builder.ID, map[string]any{"worktree": gone}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Done(ctx, b.ID, "", "", store.Expect{Session: builder.ID}, "builder")
	if err == nil {
		t.Fatal("done with an unreadable worktree passed")
	}
	for _, want := range []string{"bead " + b.ID, gone, "could not be checked", "try again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}
}

// The refusal quotes each changed file, so a name with terminal controls
// or line breaks never reaches the agent's terminal raw.
func TestDoneWorktreeRefusalQuotesNames(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	builder, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	dir := gitRepo(t, map[string]string{"main.go": "package main\n"})
	if err := os.WriteFile(filepath.Join(dir, "owned\x1b]0;.go"), []byte("package owned\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHandle(ctx, builder.ID, map[string]any{"worktree": dir}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Done(ctx, b.ID, "", "", store.Expect{Session: builder.ID}, "builder")
	if err == nil {
		t.Fatal("done with an untracked file passed")
	}
	if strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("refusal carries the file's escapes: %q", err)
	}
	if !strings.Contains(err.Error(), "owned") {
		t.Errorf("refusal %q lost the file's name", err)
	}
}
