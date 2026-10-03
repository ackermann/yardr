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

// The pane of a claude whose model was refused, as herdr 0.9 printed it
// (pane read --source recent-unwrapped): mostly blank lines, the error in
// the middle.
const stalledPane = `

 ▐▛███▛█   Claude Code v2.1.288
▝▜██████▀  claude-nope-9 · Claude Max

❯ Read /tmp/w/brief.md and follow it.

⏺ There's an issue with the selected model (claude-nope-9). It may not exist or you may not have access to it.
  Run /model to pick a different model.



────────────────
❯
────────────────
  ⏵⏵ auto mode on (shift+tab to cycle)


`

// Tail reads the agent's pane as text and keeps the lines that say
// something, so a stalled session's event shows the agent's error.
func TestHerdrTailKeepsWhatThePaneSays(t *testing.T) {
	ctx := context.Background()
	pane := filepath.Join(t.TempDir(), "pane")
	if err := os.WriteFile(pane, []byte(stalledPane), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Herdr{Bin: fakeHerdrBin(t, `case "$1 $2 $3" in
  "pane read p7") cat `+pane+` ;;
  *) echo '{"error":{"code":"pane_not_found","message":"no such pane"},"id":"x"}' >&2; exit 1 ;;
esac`)}
	tail, err := h.Tail(ctx, map[string]any{"pane": "p7"}, 6)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(tail, "\n")
	if len(lines) != 6 || !strings.Contains(tail, "There's an issue with the selected model (claude-nope-9)") ||
		strings.Contains(tail, "Claude Code v2") {
		t.Fatalf("tail (%d lines) =\n%s", len(lines), tail)
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" || l != strings.TrimRight(l, " ") {
			t.Fatalf("tail keeps a blank line or trailing space: %q", l)
		}
	}
	if _, err := h.Tail(ctx, map[string]any{"pane": "gone"}, 6); err == nil {
		t.Fatal("a pane herdr cannot read must be an error")
	}
	if _, err := h.Tail(ctx, map[string]any{}, 6); err == nil {
		t.Fatal("a handle without a pane must be an error")
	}
}

// Worked tells a session that changed its worktree (a commit, an edit) from
// one that only sat in it; the commits its branch started on do not count.
func TestHerdrWorkedSeesCommitsAndEdits(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "A")
	rig := store.Rig{Name: "r", Path: repo, Base: "main"}
	log := filepath.Join(t.TempDir(), "calls")
	h := &Herdr{Socket: fakeHerdrAPI(t, log), Bin: worktreeHerdr(t, log), WorktreeRoot: t.TempDir()}
	handle, err := h.Start(ctx, dispatch.Job{Bead: store.Bead{ID: "r-1", Title: "x", Stage: "new"},
		Group: store.Group{Name: "builders"}, Rig: &rig, Session: store.Session{ID: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	wt := handle["worktree"].(string)
	worked := func() bool {
		t.Helper()
		w, err := h.Worked(ctx, handle)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	if handle["head"] != gitOutT(t, repo, "rev-parse", "main") || worked() {
		t.Fatalf("a session that did nothing worked; handle = %v", handle)
	}
	if err := os.WriteFile(filepath.Join(wt, "f"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !worked() {
		t.Fatal("an uncommitted file is no work")
	}
	gitT(t, wt, "add", "f")
	gitT(t, wt, "commit", "-q", "-m", "B")
	if !worked() {
		t.Fatal("a commit on a clean worktree is no work")
	}
	// A dir rig's bead has no checkout: nothing to tell from.
	if w, err := h.Worked(ctx, map[string]any{"dir": repo}); w || err != nil {
		t.Fatalf("dir rig: worked = %v, %v", w, err)
	}
}
