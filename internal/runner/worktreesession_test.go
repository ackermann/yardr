package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// yardr-8bhk: a herdr group leaves in the bead's worktree who it started
// there, outside the tracked tree, so a yardr command run in it with
// another session's environment can still tell whose it is. A later stage
// replaces the earlier one's, and a subdirectory is the worktree's too.
func TestHerdrStartRecordsTheWorktreesSession(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "yard-13")
	gitT(t, repo, "worktree", "add", "-q", "-b", "yard/yard-13", wt)

	if ws := SessionOf(ctx, wt); ws != nil {
		t.Fatalf("a worktree no session was started in names %+v", ws)
	}
	log := filepath.Join(t.TempDir(), "calls")
	h := &Herdr{Socket: fakeHerdrAPI(t, log), Bin: fakeHerdrBin(t, `printf '%s\n' "$*" >> `+log+`
case "$1 $2" in
  "worktree create") echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"`+wt+`"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	job := dispatch.Job{
		Bead:    store.Bead{ID: "yard-13", Title: "Gate", Stage: "new"},
		Group:   store.Group{Name: "builders"},
		Rig:     &store.Rig{Name: "yard", Path: repo, Base: "main"},
		Session: store.Session{ID: "s1"},
	}
	if _, err := h.Start(ctx, job); err != nil {
		t.Fatal(err)
	}
	want := WorktreeSession{Bead: "yard-13", Session: "s1", Actor: "yard-13-new", Stage: "new", Group: "builders"}
	if ws := SessionOf(ctx, wt); ws == nil || *ws != want {
		t.Fatalf("the worktree names %+v, want %+v", ws, want)
	}

	job.Bead.Stage, job.Group.Name, job.Session.ID = "review", "reviewers", "s2"
	if _, err := h.Start(ctx, job); err != nil {
		t.Fatal(err)
	}
	want = WorktreeSession{Bead: "yard-13", Session: "s2", Actor: "yard-13-review", Stage: "review", Group: "reviewers"}
	sub := filepath.Join(wt, "internal")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if ws := SessionOf(ctx, sub); ws == nil || *ws != want {
		t.Fatalf("after the next stage the worktree names %+v, want %+v", ws, want)
	}
	// The rig's own checkout and a plain directory are nobody's.
	for _, dir := range []string{repo, t.TempDir()} {
		if ws := SessionOf(ctx, dir); ws != nil {
			t.Fatalf("%s names %+v", dir, ws)
		}
	}
	gitT(t, wt, "clean", "-fdq")
	if st := gitOutT(t, wt, "status", "--porcelain", "--ignored"); st != "" {
		t.Fatalf("the session file left files in the worktree:\n%s", st)
	}
}

// yardr-8bhk: Codex is started with --no-daemon, bead agent and crew member
// alike, so its commands run under its own pane with its own environment;
// no other kind gets the flag, and a group that passes it gets it once.
func TestAgentArgsCodexRunsWithoutItsSharedDaemon(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]any
		bead string
		want string
	}{
		{"codex", map[string]any{"kind": "codex"}, "", "--no-daemon"},
		{"codex with args and a model", map[string]any{"kind": "codex", "args": []any{"--approve-for-me"}, "model": "gpt-6-luna"}, "",
			"--approve-for-me --no-daemon --model gpt-6-luna"},
		{"codex with a bead's model", map[string]any{"kind": "codex", "model": "gpt-6-luna"}, "gpt-6-astra",
			"--no-daemon --model gpt-6-astra"},
		{"codex already told", map[string]any{"kind": "codex", "args": []any{"--no-daemon", "--approve-for-me"}}, "",
			"--no-daemon --approve-for-me"},
		{"claude", map[string]any{"kind": "claude", "args": []any{"--permission-mode", "auto"}}, "", "--permission-mode auto"},
		{"no kind is claude", map[string]any{}, "", ""},
	}
	for _, c := range cases {
		if got := strings.Join(agentArgs(c.cfg, c.bead), " "); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
