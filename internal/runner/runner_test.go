package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mckean/yard/internal/dispatch"
	"github.com/mckean/yard/internal/store"
	"github.com/mckean/yard/internal/storetest"
)

func TestAgentName(t *testing.T) {
	cases := map[[2]string]string{
		{"web-12", "review"}:                     "web-12-review",
		{"Web-12", "Needs Review!"}:              "web-12-needs-review",
		{"12", "new"}:                            "y12-new",
		{"averyveryverylongprefix-123", "stage"}: "averyveryverylongprefix-123-stag",
	}
	for in, want := range cases {
		if got := AgentName(in[0], in[1]); got != want {
			t.Errorf("AgentName(%q, %q) = %q, want %q", in[0], in[1], got, want)
		}
	}
}

func TestBriefIncludesPromptAndCommands(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "builder.md"), []byte("You build things."), 0o600); err != nil {
		t.Fatal(err)
	}
	job := dispatch.Job{
		Bead:    store.Bead{ID: "web-3", Title: "Add login", Body: "Use OAuth.", Type: "feature", Stage: "new"},
		Group:   store.Group{Name: "web/builders", Config: map[string]any{"prompt": "builder.md"}},
		Rig:     &store.Rig{Name: "web", Path: dir},
		Session: store.Session{ID: "s1"},
	}
	b, err := Brief(job, "web-3-new", "/wt/web-3")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"You build things.", "Add login", "Use OAuth.", "/wt/web-3",
		"yard bead advance web-3 --to", "YARD_ACTOR=web-3-new"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief missing %q:\n%s", want, b)
		}
	}
	job.Group.Config["prompt"] = "missing.md"
	if _, err := Brief(job, "x", ""); err == nil {
		t.Fatal("missing prompt file not reported")
	}
}

// With a flow the brief lists the allowed next stages, when to use each and
// the exact command; without one it keeps the generic advance line.
func TestBriefListsNextStages(t *testing.T) {
	job := dispatch.Job{
		Bead:    store.Bead{ID: "web-3", Title: "Add login", Type: "task", Stage: "new"},
		Group:   store.Group{Name: "builders"},
		Session: store.Session{ID: "s1"},
	}
	plain, err := Brief(job, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "hand the bead to the next stage: `yard bead advance web-3 --to <stage>`") ||
		strings.Contains(plain, "## Next stages") {
		t.Fatalf("brief without a flow changed:\n%s", plain)
	}

	job.Next = []dispatch.NextStage{
		{Stage: "review", Group: "reviewers", Runner: "herdr"},
		{Stage: "signoff", Group: "humans", Runner: "manual"},
		{Stage: "lint", Group: "ci", Runner: "exec"},
		{Stage: "backlog"},
		{Stage: "dropped", Terminal: true},
	}
	b, err := Brief(job, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"advance to one of the next stages below",
		"## Next stages",
		"moves out of `new`",
		"- `review`: an agent in `reviewers` picks it up",
		"  `yard bead advance web-3 --to review`",
		"- `signoff`: a human in `humans` picks it up",
		"- `lint`: `ci` runs its command on it",
		"- `backlog`: no group works this stage",
		"- `dropped`: closes the bead",
		"  `yard bead advance web-3 --to dropped`",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("brief missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "--to <stage>") {
		t.Errorf("brief with a flow still offers any stage:\n%s", b)
	}

	job.Next = []dispatch.NextStage{}
	b, _ = Brief(job, "a", "")
	if !strings.Contains(b, "allows no move out of `new`") {
		t.Errorf("empty next stages not explained:\n%s", b)
	}
}

func TestExecLifecycle(t *testing.T) {
	e := NewExec()
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "out")
	job := dispatch.Job{
		Bead:    store.Bead{ID: "yd-1", Stage: "new"},
		Group:   store.Group{Name: "g", Config: map[string]any{"cmd": "echo $YARD_BEAD $YARD_STAGE > " + out + "; sleep 30"}},
		Session: store.Session{ID: "s1"},
	}
	h, err := e.Start(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if alive, _ := e.Alive(ctx, h); !alive {
		t.Fatal("not alive after start")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) == "yd-1 new" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b, _ := os.ReadFile(out); strings.TrimSpace(string(b)) != "yd-1 new" {
		t.Fatalf("env not passed: %q", b)
	}
	if err := e.Stop(ctx, h); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if alive, _ := e.Alive(ctx, h); !alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("still alive after stop")
}

// fakeHerdrBin writes a script that answers like herdr: results on stdout,
// error objects on stderr with exit status 1.
func fakeHerdrBin(t *testing.T, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHerdrAliveReadsErrorsFromStderr(t *testing.T) {
	ctx := context.Background()
	h := &Herdr{Bin: fakeHerdrBin(t, `case "$3" in
  alive) echo '{"id":"x","result":{"type":"agent_info","agent":{"name":"alive"}}}' ;;
  gone)  echo '{"error":{"code":"agent_not_found","message":"agent target gone not found"},"id":"x"}' >&2; exit 1 ;;
  *)     echo '{"error":{"code":"server_not_running","message":"no herdr server"},"id":"x"}' >&2; exit 1 ;;
esac`)}
	if ok, err := h.Alive(ctx, map[string]any{"agent": "alive"}); !ok || err != nil {
		t.Fatalf("alive agent: %v %v", ok, err)
	}
	if ok, err := h.Alive(ctx, map[string]any{"agent": "gone"}); ok || err != nil {
		t.Fatalf("gone agent must be dead, not unknown: %v %v", ok, err)
	}
	if _, err := h.Alive(ctx, map[string]any{"agent": "x"}); err == nil {
		t.Fatal("herdr down must be unknown (error), not dead")
	}
}

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestHerdrFinishRemovesOnlyCleanWorktrees(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	root := t.TempDir()
	add := func(bead string) string {
		path := filepath.Join(root, bead)
		gitT(t, repo, "worktree", "add", "-q", "-b", "yard/"+bead, path)
		// yard's brief dir, ignored by its own .gitignore
		os.MkdirAll(filepath.Join(path, ".yard"), 0o700)
		os.WriteFile(filepath.Join(path, ".yard", ".gitignore"), []byte("*\n"), 0o600)
		os.WriteFile(filepath.Join(path, ".yard", "brief-s.md"), []byte("brief"), 0o600)
		return path
	}
	log := filepath.Join(t.TempDir(), "calls")
	clean, dirty := add("b1"), add("b2")
	// workspace w1 is b1's checkout; w9 belongs to someone else.
	h := &Herdr{Bin: fakeHerdrBin(t, `echo "$@" >> `+log+`
case "$1 $2 $3" in
  "workspace get w1") echo '{"result":{"type":"workspace_info","workspace":{"workspace_id":"w1","worktree":{"checkout_path":"`+clean+`"}}}}' ;;
  "workspace get w9") echo '{"result":{"type":"workspace_info","workspace":{"workspace_id":"w9","worktree":{"checkout_path":"/elsewhere"}}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}

	if err := h.Finish(ctx, map[string]any{"worktree": clean, "repo": repo, "workspace": "w1"}); err != nil {
		t.Fatalf("clean worktree: %v", err)
	}
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Fatal("clean worktree not removed")
	}
	gitT(t, repo, "rev-parse", "--verify", "yard/b1") // branch kept

	os.WriteFile(filepath.Join(dirty, "wip.txt"), []byte("unsaved"), 0o600)
	err := h.Finish(ctx, map[string]any{"worktree": dirty, "repo": repo, "workspace": "w9"})
	if err == nil || !strings.Contains(err.Error(), "wip.txt") {
		t.Fatalf("dirty worktree must be kept with a reason, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirty, "wip.txt")); err != nil {
		t.Fatal("dirty worktree was touched")
	}

	if err := h.Finish(ctx, map[string]any{"worktree": clean, "repo": repo}); !errors.Is(err, dispatch.ErrNothingLeft) {
		t.Fatalf("already removed: %v", err)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "workspace close w1") || strings.Contains(string(calls), "close w9") {
		t.Fatalf("workspace close calls wrong:\n%s", calls)
	}
}

func TestAgentArgsModel(t *testing.T) {
	job := func(groupCfg, beadMeta map[string]any) dispatch.Job {
		return dispatch.Job{Group: store.Group{Config: groupCfg}, Bead: store.Bead{Metadata: beadMeta}}
	}
	args := []any{"--permission-mode", "auto", "--model", "sonnet"}
	cases := []struct {
		name string
		job  dispatch.Job
		want []string
	}{
		{"group args only", job(map[string]any{"args": args}, nil),
			[]string{"--permission-mode", "auto", "--model", "sonnet"}},
		{"bead overrides group args", job(map[string]any{"args": args}, map[string]any{"model": "opus"}),
			[]string{"--permission-mode", "auto", "--model", "opus"}},
		{"group model key", job(map[string]any{"model": "haiku"}, nil), []string{"--model", "haiku"}},
		{"bead beats group key", job(map[string]any{"model": "haiku"}, map[string]any{"model": "opus"}),
			[]string{"--model", "opus"}},
		{"nothing", job(map[string]any{}, nil), nil},
	}
	for _, c := range cases {
		if got := AgentArgs(c.job); strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// herdr reports an agent stuck at a startup question as agent_not_ready;
// the session must be kept with its first prompt pending, not failed.
func TestStartAgentKeepsNotReadyAgents(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	h := &Herdr{Bin: fakeHerdrBin(t, `echo "$@" >> `+log+`
case "$1 $2" in
  "agent start") echo '{"error":{"code":"agent_not_ready","message":"agent mayor is blocked during startup and is not ready for prompts"},"id":"x"}' >&2; exit 1 ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "mayor"}, "mayor", "prime yourself",
		map[string]any{"agent": "mayor"})
	if err != nil {
		t.Fatalf("not-ready agent treated as a failure: %v", err)
	}
	if handle[dispatch.PendingPrompt] != "prime yourself" {
		t.Fatalf("handle = %v", handle)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "agent prompt") {
		t.Fatalf("prompted an agent that was not ready:\n%s", calls)
	}
}

func TestWorkspaceLabel(t *testing.T) {
	cases := []struct{ rig, group, name, want string }{
		{"yard", "yard-codex-review", "yard-29", "codex-review 29"},
		{"yard", "yard-builders", "yard-20", "builders 20"},
		{"webapp", "builders", "webapp-1", "builders 1"},
		{"webapp", "yard-builders", "yard-3", "yard-builders yard-3"},
		{"yard", "builders", "review", "builders review"},
		// Only a leading "<rig>-" goes: not the bare rig, not a longer word.
		{"yard", "yardstick", "yard", "yardstick yard"},
		{"yard", "", "yard-7", "7"},
		{"yard", "", "new", "new"},
		{"", "yard-builders", "yard-7", "yard-builders yard-7"},
	}
	for _, c := range cases {
		if got := WorkspaceLabel(c.rig, c.group, c.name); got != c.want {
			t.Errorf("WorkspaceLabel(%q, %q, %q) = %q, want %q", c.rig, c.group, c.name, got, c.want)
		}
	}
}

// herdr shows workspace and tab labels; both lead with the group, on the
// first stage (worktree created) and on a later one (worktree reopened).
func TestHerdrStartLabelsGroupFirst(t *testing.T) {
	ctx := context.Background()
	wt := t.TempDir()
	start := func(t *testing.T, createScript, stage, group string) string {
		t.Helper()
		log := filepath.Join(t.TempDir(), "calls")
		h := &Herdr{Bin: fakeHerdrBin(t, `printf '%s\n' "$*" >> `+log+`
case "$1 $2" in
  "worktree create") `+createScript+` ;;
  "worktree open") echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"`+wt+`"},"already_open":true}}' ;;
  "tab create") echo '{"result":{"tab":{"tab_id":"t2"},"root_pane":{"pane_id":"p2"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
		job := dispatch.Job{
			Bead:    store.Bead{ID: "yard-13", Stage: stage},
			Group:   store.Group{Name: group},
			Rig:     &store.Rig{Name: "yard", Path: t.TempDir(), Base: "main"},
			Session: store.Session{ID: "s-" + stage},
		}
		handle, err := h.Start(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		if handle["agent"] != AgentName("yard-13", stage) {
			t.Fatalf("agent name changed: %v", handle["agent"])
		}
		calls, _ := os.ReadFile(log)
		return string(calls)
	}

	created := `echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"` + wt + `"}}}'`
	calls := start(t, created, "new", "yard-builders")
	if !strings.Contains(calls, "worktree create ") || !strings.Contains(calls, "--label builders 13\n") {
		t.Fatalf("first stage not labelled group first:\n%s", calls)
	}
	if strings.Contains(calls, "workspace rename") || strings.Contains(calls, "tab create") {
		t.Fatalf("first stage relabelled or opened a tab:\n%s", calls)
	}

	exists := `echo '{"error":{"code":"worktree_exists","message":"branch exists"}}' >&2; exit 1`
	calls = start(t, exists, "review", "yard-codex-review")
	for _, want := range []string{
		"worktree open --cwd ",
		"workspace rename w1 codex-review 13\n",
		"tab create --workspace w1 --cwd " + wt + " --label codex-review review\n",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("later stage missing %q:\n%s", want, calls)
		}
	}
}

// A bead migrated from counter ids keeps its branch: the old id became its
// handle, and branches are built from the handle.
func TestMigratedBeadKeepsItsBranch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yard.db")
	storetest.LegacyDB(t, path)
	s := storetest.Open(t, path)
	b, err := s.GetBead(context.Background(), "yard-2")
	if err != nil {
		t.Fatal(err)
	}
	if b.UUID == "" {
		t.Fatal("bead was not migrated")
	}
	if got := Branch(*b); got != "yard/yard-2" {
		t.Fatalf("branch = %s, want yard/yard-2", got)
	}
	brief, err := Brief(dispatch.Job{Bead: *b, Session: store.Session{ID: "s"}, Group: store.Group{Name: "g"}}, "a", "/w/yard-2")
	if err != nil || !strings.Contains(brief, "branch `yard/yard-2`") {
		t.Fatalf("brief names another branch:\n%s", brief)
	}
}
