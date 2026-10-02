package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
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
	b, err := Brief(job, "web-3-new", "/wt/web-3", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"You build things.", "Add login", "Use OAuth.", "/wt/web-3",
		"yardr bead advance web-3 --to", "YARDR_ACTOR=web-3-new"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief missing %q:\n%s", want, b)
		}
	}
	job.Group.Config["prompt"] = "missing.md"
	if _, err := Brief(job, "x", "", ""); err == nil {
		t.Fatal("missing prompt file not reported")
	}
}

// An epic's brief says it is one, names its children and gives the range
// to review against the rig's base, not the last child's diff.
func TestBriefForAnEpic(t *testing.T) {
	job := dispatch.Job{
		Bead:     store.Bead{ID: "web-e", Title: "Login", Type: store.TypeEpic, Stage: "review"},
		Group:    store.Group{Name: "web/reviewers"},
		Rig:      &store.Rig{Name: "web", Base: "main"},
		Session:  store.Session{ID: "s1"},
		Base:     "main",
		Children: []string{"web-1", "web-2"},
	}
	b, err := Brief(job, "a", "/wt/web-e", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"This bead is an epic", "Children: web-1, web-2.", "`main..yard/web-e`",
		"git diff main...yard/web-e", "lands on `main` as one unit"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief missing %q:\n%s", want, b)
		}
	}
	job.Bead.Type = "feature"
	if b, _ := Brief(job, "a", "", ""); strings.Contains(b, "is an epic") {
		t.Errorf("a feature's brief calls it an epic:\n%s", b)
	}
}

// fakeGh puts a gh on PATH that records its argv in calls and runs script
// (sh) for it, so a brief's gh pr checks needs no GitHub.
func fakeGh(t *testing.T, script string) (calls string) {
	t.Helper()
	bin := t.TempDir()
	calls = filepath.Join(bin, "calls")
	body := "#!/bin/sh\necho \"$*\" >>" + calls + "\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// An epic landing through a pull request (pr_url set by integrate-epic)
// has the PR, its checks as gh prints them and the review-on-GitHub rules
// in its brief. gh exits 8 while checks are pending and still prints the
// table, which is pasted, not reported as unavailable.
func TestBriefForAnEpicWithAPullRequest(t *testing.T) {
	const url = "https://github.com/o/r/pull/7"
	calls := fakeGh(t, "printf 'gate\\tpass\\t1m\\nlint\\tpending\\t0\\n'; exit 8")
	job := dispatch.Job{
		Bead: store.Bead{ID: "web-e", Title: "Login", Type: store.TypeEpic, Stage: "review",
			Metadata: map[string]any{"pr_url": url}},
		Group:   store.Group{Name: "web/reviewers"},
		Rig:     &store.Rig{Name: "web", Base: "main", Path: t.TempDir()},
		Session: store.Session{ID: "s1"},
		Base:    "main",
	}
	b, err := Brief(job, "a", "/wt/web-e", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Pull request", "lands through the pull request " + url,
		"gate\tpass\t1m\nlint\tpending\t0", "git push --force-with-lease origin yard/web-e",
		"gh pr review " + url, "--approve", "--request-changes", "--comment", "Never merge"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "unavailable") {
		t.Errorf("pending checks reported as unavailable:\n%s", b)
	}
	if got, _ := os.ReadFile(calls); strings.TrimSpace(string(got)) != "pr checks "+url {
		t.Errorf("gh calls = %q, want one pr checks %s", got, url)
	}

	// gh failing without a table: the brief still has the PR and says why
	// the checks are missing.
	fakeGh(t, "echo 'HTTP 502: Bad Gateway' >&2; exit 1")
	b, err = Brief(job, "a", "/wt/web-e", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"lands through the pull request " + url, "checks: unavailable (HTTP 502: Bad Gateway)"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief missing %q:\n%s", want, b)
		}
	}
}

// gh pr checks is bounded: a gh that hangs leaves the checks unavailable
// rather than holding the session's start up.
func TestBriefPullRequestChecksTimeOut(t *testing.T) {
	fakeGh(t, "exec sleep 30")
	defer func(d time.Duration) { checksTimeout = d }(checksTimeout)
	checksTimeout = 100 * time.Millisecond
	start := time.Now()
	got := prChecks(t.TempDir(), "https://github.com/o/r/pull/7")
	if !strings.HasPrefix(got, "checks: unavailable (gh pr checks took longer than") {
		t.Errorf("prChecks = %q", got)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("prChecks took %s", d)
	}
}

// An epic without pr_url lands locally: its brief is the same as before
// pull requests, and gh is never run.
func TestBriefForAnEpicWithoutAPullRequest(t *testing.T) {
	calls := fakeGh(t, "exit 1")
	job := dispatch.Job{
		Bead:     store.Bead{ID: "web-e", Title: "Login", Type: store.TypeEpic, Stage: "review"},
		Group:    store.Group{Name: "web/reviewers"},
		Rig:      &store.Rig{Name: "web", Base: "main"},
		Session:  store.Session{ID: "s1"},
		Base:     "main",
		Children: []string{"web-1"},
	}
	b, err := Brief(job, "a", "/wt/web-e", "")
	if err != nil {
		t.Fatal(err)
	}
	want := "This bead is an epic. Its branch `yard/web-e` holds the work of its children, each landed on it after its own review; it lands on `main` as one unit." +
		" Children: web-1. The whole change is `main..yard/web-e` (`git log main..yard/web-e`, `git diff main...yard/web-e`): work on it as a whole, not child by child.\n\n" +
		"An epic sent back to `open` waits there only while a child is open: before sending it back, add the fix as a child (`yardr bead create ... -r <rig>`, then `yardr dep add web-e <child> -k parent`), or it is integrated again unchanged.\n\n" +
		"## Login"
	if !strings.Contains(b, want) {
		t.Errorf("epic part of the brief is not the local-landing one:\n%s", b)
	}
	for _, not := range []string{"Pull request", "gh pr", "force-with-lease"} {
		if strings.Contains(b, not) {
			t.Errorf("brief without pr_url mentions %q:\n%s", not, b)
		}
	}
	if _, err := os.Stat(calls); err == nil {
		t.Error("gh was run for an epic without pr_url")
	}
}

// A relative prompt the rig does not have is read from the yard's home, so
// the quick start's prompt=roles/reviewer.md works on a fresh rig from
// ~/.yardr/roles/reviewer.md; the rig's own copy wins when it has one.
func TestBriefFindsPromptInHome(t *testing.T) {
	rig, home := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "roles", "reviewer.md"), []byte("Home reviewer."), 0o600); err != nil {
		t.Fatal(err)
	}
	job := dispatch.Job{
		Bead:    store.Bead{ID: "web-3", Title: "t", Type: "task", Stage: "review"},
		Group:   store.Group{Name: "web/reviewers", Config: map[string]any{"prompt": "roles/reviewer.md"}},
		Rig:     &store.Rig{Name: "web", Path: rig},
		Session: store.Session{ID: "s1"},
	}
	b, err := Brief(job, "a", "", home)
	if err != nil || !strings.Contains(b, "Home reviewer.") {
		t.Fatalf("brief from home = %v\n%s", err, b)
	}
	if err := os.MkdirAll(filepath.Join(rig, "roles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rig, "roles", "reviewer.md"), []byte("Rig reviewer."), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err = Brief(job, "a", "", home); err != nil || !strings.Contains(b, "Rig reviewer.") || strings.Contains(b, "Home reviewer.") {
		t.Fatalf("rig prompt should win = %v\n%s", err, b)
	}
	job.Group.Config["prompt"] = "roles/missing.md"
	_, err = Brief(job, "a", "", home)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(rig, "roles/missing.md")) || !strings.Contains(err.Error(), filepath.Join(home, "roles/missing.md")) {
		t.Fatalf("missing prompt error should name both places: %v", err)
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
	plain, err := Brief(job, "a", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "hand the bead to the next stage: `yardr bead advance web-3 --to <stage>`") ||
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
	b, err := Brief(job, "a", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"advance to one of the next stages below",
		"## Next stages",
		"moves out of `new`",
		"- `review`: an agent in `reviewers` picks it up",
		"  `yardr bead advance web-3 --to review`",
		"- `signoff`: a human in `humans` picks it up",
		"- `lint`: `ci` runs its command on it",
		"- `backlog`: no group works this stage",
		"- `dropped`: closes the bead",
		"  `yardr bead advance web-3 --to dropped`",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("brief missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "--to <stage>") {
		t.Errorf("brief with a flow still offers any stage:\n%s", b)
	}

	job.Next = []dispatch.NextStage{}
	b, _ = Brief(job, "a", "", "")
	if !strings.Contains(b, "allows no move out of `new`") {
		t.Errorf("empty next stages not explained:\n%s", b)
	}
}

// With outcomes the brief lists them instead of stage names: one line each
// saying where the flow sends the bead, and the exact bead done command.
func TestBriefListsOutcomes(t *testing.T) {
	job := dispatch.Job{
		Bead:    store.Bead{ID: "web-3", Title: "Add login", Type: "task", Stage: "new"},
		Group:   store.Group{Name: "builders"},
		Session: store.Session{ID: "s1"},
		Next: []dispatch.NextStage{
			{Stage: "review", Group: "reviewers", Runner: "herdr"},
			{Stage: "decide", Group: "deciders", Runner: "manual"},
			{Stage: "backlog"},
		},
		Outcomes: []dispatch.NextOutcome{
			{Outcome: "done", NextStage: dispatch.NextStage{Stage: "review", Group: "reviewers", Runner: "herdr"}},
			{Outcome: "question", NextStage: dispatch.NextStage{Stage: "decide", Group: "deciders", Runner: "manual"}},
		},
	}
	b, err := Brief(job, "a", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"report how it ended with one of the outcomes below; the flow picks the next stage",
		"Reporting an outcome, closing or holding ends your session",
		"## Outcomes",
		"Report how your work in `new` ended",
		"- `done` (to `review`): an agent in `reviewers` picks it up",
		"  `yardr bead done web-3`\n",
		"- `question` (to `decide`): a human in `deciders` picks it up",
		"  `yardr bead done web-3 --outcome question`",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("brief missing %q:\n%s", want, b)
		}
	}
	for _, not := range []string{"## Next stages", "yardr bead advance", "backlog"} {
		if strings.Contains(b, not) {
			t.Errorf("brief with outcomes still offers %q:\n%s", not, b)
		}
	}
}

func TestExecLifecycle(t *testing.T) {
	e := NewExec()
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "out")
	job := dispatch.Job{
		Bead:    store.Bead{ID: "yd-1", Stage: "new"},
		Group:   store.Group{Name: "g", Config: map[string]any{"cmd": "echo $YARDR_BEAD $YARDR_STAGE > " + out + "; sleep 30"}},
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

// A refinery script lands a bead from its environment alone: rig, base,
// branch, worktree and this yardr, with the group as actor.
func TestExecEnvNamesTheBeadsWork(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	out := filepath.Join(dir, "env")
	e := &Exec{Home: "/yh", Bin: "/bin/yardr"}
	vars := "YARDR_BEAD YARDR_ACTOR YARDR_RIG YARDR_RIG_PATH YARDR_BASE YARDR_BRANCH YARDR_WORKTREE YARDR_BIN YARDR_HOME YARD_WORKTREE"
	job := dispatch.Job{
		Bead:    store.Bead{ID: "yd-7", Stage: "approved"},
		Group:   store.Group{Name: "refinery", Config: map[string]any{"cmd": "printenv " + vars + " > " + out}},
		Rig:     &store.Rig{Name: "web", Path: dir, Base: "main"},
		Session: store.Session{ID: "s1"},
	}
	if _, err := e.Start(ctx, job); err != nil {
		t.Fatal(err)
	}
	want := "yd-7 refinery web " + dir + " main yard/yd-7 /yh/worktrees/web/yd-7 /bin/yardr /yh /yh/worktrees/web/yd-7"
	var got string
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(out)
		if got = strings.Join(strings.Fields(string(b)), " "); got == want {
			return
		}
	}
	t.Fatalf("env = %q, want %q", got, want)
}

// config.worktrees moves the worktree as it does for herdr groups.
func TestExecEnvWorktreesConfig(t *testing.T) {
	e := &Exec{Home: "/yh"}
	job := dispatch.Job{
		Bead:  store.Bead{ID: "yd-7"},
		Group: store.Group{Name: "refinery", Config: map[string]any{"worktrees": "/wt"}},
		Rig:   &store.Rig{Name: "web", Path: "/r", Base: "main"},
	}
	if env := e.env(job); !slices.Contains(env, "YARDR_WORKTREE=/wt/web/yd-7") {
		t.Fatalf("config.worktrees ignored: %v", env)
	}
	// A child of an epic lands on the epic's branch, not the rig's base.
	if env := e.env(job); !slices.Contains(env, "YARDR_BASE=main") {
		t.Fatalf("no epic: %v", env)
	}
	job.Epic, job.Base = "yd-e", "yard/yd-e"
	if env := e.env(job); !slices.Contains(env, "YARDR_BASE=yard/yd-e") {
		t.Fatalf("child of an epic: %v", env)
	}
	// Without a rig there is no worktree, but the branch is still known.
	job.Rig = nil
	env := e.env(job)
	if !slices.Contains(env, "YARDR_BRANCH=yard/yd-7") || slices.ContainsFunc(env, func(s string) bool {
		return strings.HasPrefix(s, "YARDR_WORKTREE=")
	}) {
		t.Fatalf("env without a rig: %v", env)
	}
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
		if _, err := writeBrief(ctx, path, "s", "brief"); err != nil {
			t.Fatal(err)
		}
		// a pre-yardr brief dir, ignored by its own .gitignore
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
	cases := []struct{ rig, group, epic, bead, want string }{
		{"yard", "yard-builders", "yard-sv6b", "yard-2cet", "builders [yard-sv6b] yard-2cet"},
		{"yard", "yard-builders", "", "yard-jtyz", "builders yard-jtyz"},
		// A group without the rig prefix stays whole.
		{"yard", "builders", "yard-sv6b", "yard-2cet", "builders [yard-sv6b] yard-2cet"},
		{"yard", "builders", "", "yard-jtyz", "builders yard-jtyz"},
		{"yard", "", "yard-sv6b", "yard-2cet", "[yard-sv6b] yard-2cet"},
		{"yard", "", "", "yard-jtyz", "yard-jtyz"},
	}
	for _, c := range cases {
		if got := WorkspaceLabel(c.rig, c.group, c.epic, c.bead); got != c.want {
			t.Errorf("WorkspaceLabel(%q, %q, %q, %q) = %q, want %q", c.rig, c.group, c.epic, c.bead, got, c.want)
		}
	}
}

func TestTabLabel(t *testing.T) {
	cases := []struct{ rig, group, name, want string }{
		{"yard", "yard-codex-review", "yard-29", "codex-review yard-29"},
		{"yard", "yard-builders", "yard-20", "builders yard-20"},
		{"webapp", "builders", "webapp-1", "builders webapp-1"},
		{"webapp", "yard-builders", "yard-3", "yard-builders yard-3"},
		{"yard", "builders", "review", "builders review"},
		// Only a leading "<rig>-" goes: not the bare rig, not a longer word.
		{"yard", "yardstick", "yard-29", "yardstick yard-29"},
		{"yard", "", "yard-7", "yard-7"},
		{"yard", "", "new", "new"},
		{"", "yard-builders", "yard-7", "yard-builders yard-7"},
	}
	for _, c := range cases {
		if got := TabLabel(c.rig, c.group, c.name); got != c.want {
			t.Errorf("TabLabel(%q, %q, %q) = %q, want %q", c.rig, c.group, c.name, got, c.want)
		}
	}
}

// herdr shows workspace and tab labels: the workspace is the group and the
// bead, with the epic between them for a child of one, and each stage's
// tab leads with the group;
// on the first stage (worktree created) and on a later one (worktree
// reopened, and relabelled).
func TestHerdrStartLabels(t *testing.T) {
	ctx := context.Background()
	wt := t.TempDir()
	gitT(t, wt, "init", "-q", "-b", "main")
	start := func(t *testing.T, createScript, stage, group, epic string) string {
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
			Epic:    epic,
		}
		if epic != "" {
			// Start makes the epic's branch from the base, so give it one.
			gitT(t, job.Rig.Path, "init", "-q", "-b", "main")
			gitT(t, job.Rig.Path, "commit", "-q", "--allow-empty", "-m", "init")
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
	calls := start(t, created, "new", "yard-builders", "")
	if !strings.Contains(calls, "worktree create ") || !strings.Contains(calls, "--label builders yard-13\n") {
		t.Fatalf("first stage workspace not labelled with the group and the bead:\n%s", calls)
	}
	if strings.Contains(calls, "workspace rename") {
		t.Fatalf("first stage relabelled:\n%s", calls)
	}
	if !strings.Contains(calls, "tab create --workspace w1 --cwd "+wt+" --label builders new --env ") {
		t.Fatalf("first stage tab not labelled group first:\n%s", calls)
	}

	exists := `echo '{"error":{"code":"worktree_exists","message":"branch exists"}}' >&2; exit 1`
	calls = start(t, exists, "review", "yard-codex-review", "")
	for _, want := range []string{
		"worktree open --cwd ",
		"workspace rename w1 codex-review yard-13\n",
		"tab create --workspace w1 --cwd " + wt + " --label codex-review review --env ",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("later stage missing %q:\n%s", want, calls)
		}
	}

	// A child of an epic: the workspace reads the epic after the group, the
	// tab and the agent name are as for any bead.
	calls = start(t, created, "new", "yard-builders", "yard-12")
	if !strings.Contains(calls, "worktree create ") || !strings.Contains(calls, "--label builders [yard-12] yard-13\n") {
		t.Fatalf("child's workspace not labelled with its epic:\n%s", calls)
	}
	if !strings.Contains(calls, "--label builders new --env ") {
		t.Fatalf("child's tab label changed:\n%s", calls)
	}
	calls = start(t, exists, "review", "yard-codex-review", "yard-12")
	if !strings.Contains(calls, "workspace rename w1 codex-review [yard-12] yard-13\n") {
		t.Fatalf("child's reopened workspace not relabelled with its epic:\n%s", calls)
	}
}

// The brief goes into the worktree's git dir, outside the tracked tree:
// nothing in the checkout changes, so .yardr/ there can be committed config,
// and the agent is pointed at the brief by its absolute path.
func TestHerdrBriefOutsideTheTree(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "yard-13")
	gitT(t, repo, "worktree", "add", "-q", "-b", "yard/yard-13", wt)

	log := filepath.Join(t.TempDir(), "calls")
	h := &Herdr{Bin: fakeHerdrBin(t, `printf '%s\n' "$*" >> `+log+`
case "$1 $2" in
  "worktree create") echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"`+wt+`"}}}' ;;
  "tab create") echo '{"result":{"tab":{"tab_id":"t2"},"root_pane":{"pane_id":"p2"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	job := dispatch.Job{
		Bead:    store.Bead{ID: "yard-13", Title: "Gate", Stage: "new"},
		Group:   store.Group{Name: "builders"},
		Rig:     &store.Rig{Name: "yard", Path: repo, Base: "main"},
		Session: store.Session{ID: "s1"},
	}
	handle, err := h.Start(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	brief, _ := handle["brief"].(string)
	gitDir := gitOutT(t, wt, "rev-parse", "--absolute-git-dir")
	if !filepath.IsAbs(brief) || filepath.Dir(brief) != filepath.Join(gitDir, "yardr") {
		t.Fatalf("brief at %q, want it under %s/yardr", brief, gitDir)
	}
	if b, err := os.ReadFile(brief); err != nil || !strings.Contains(string(b), "Gate") {
		t.Fatalf("brief not written: %v\n%s", err, b)
	}
	if st := gitOutT(t, wt, "status", "--porcelain", "--ignored"); st != "" {
		t.Fatalf("brief left files in the worktree:\n%s", st)
	}
	if _, err := os.Stat(filepath.Join(wt, ".yard")); !os.IsNotExist(err) {
		t.Fatal("still writes .yard/ into the worktree")
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "agent prompt yard-13-new Read "+brief+" and follow it.\n") {
		t.Fatalf("agent not pointed at the brief by its path:\n%s", calls)
	}
}

// A bead agent's identity is in its environment, set by herdr on the tab
// it runs in, not in an export the prompt asks for: its yardr calls are
// attributed to it, reach this yard, and carry its session.
func TestHerdrStartPutsAgentEnvOnItsTab(t *testing.T) {
	ctx := context.Background()
	wt := t.TempDir()
	gitT(t, wt, "init", "-q", "-b", "main")
	for _, open := range []bool{false, true} {
		log := filepath.Join(t.TempDir(), "calls")
		h := &Herdr{YardHome: "/y", YardBin: "/bin/yardr", Bin: fakeHerdrBin(t, `printf '%s\n' "$*" >> `+log+`
case "$1 $2" in
  "worktree create") echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"`+wt+`"},"already_open":`+fmt.Sprint(open)+`}}' ;;
  "tab create") echo '{"result":{"tab":{"tab_id":"t2"},"root_pane":{"pane_id":"p2"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
		job := dispatch.Job{
			Bead:    store.Bead{ID: "yard-13", Stage: "new"},
			Group:   store.Group{Name: "yard-builders"},
			Rig:     &store.Rig{Name: "yard", Path: t.TempDir(), Base: "main"},
			Session: store.Session{ID: "s1"},
		}
		handle, err := h.Start(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		if handle["tab"] != "t2" || handle["pane"] != "p2" {
			t.Fatalf("agent not in the tab created with its env: %v", handle)
		}
		raw, _ := os.ReadFile(log)
		calls := string(raw)
		var tabCall string
		for _, l := range strings.Split(calls, "\n") {
			if strings.HasPrefix(l, "tab create ") {
				tabCall = l + " "
			}
		}
		for _, e := range []string{"YARDR_ACTOR=yard-13-new", "YARDR_HOME=/y", "YARDR_BEAD=yard-13",
			"YARDR_SESSION=s1", "YARDR_BIN=/bin/yardr", "YARD_ACTOR=yard-13-new"} {
			if !strings.Contains(tabCall, " --env "+e+" ") {
				t.Errorf("already_open=%v: tab create lacks --env %s:\n%s", open, e, tabCall)
			}
		}
		if !strings.Contains(calls, "agent start yard-13-new --kind claude --pane p2") {
			t.Errorf("agent not started in the env tab's pane:\n%s", calls)
		}
		if strings.Contains(calls, "export") {
			t.Errorf("prompt still asks for an export:\n%s", calls)
		}
		// A workspace opened for this stage has an idle root tab, closed;
		// an already open one's root tab is someone else's and kept.
		if closed := strings.Contains(calls, "tab close t1\n"); closed == open {
			t.Errorf("already_open=%v: root tab closed=%v:\n%s", open, closed, calls)
		}
	}
}

func gitOutT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOut(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
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
	brief, err := Brief(dispatch.Job{Bead: *b, Session: store.Session{ID: "s"}, Group: store.Group{Name: "g"}}, "a", "/w/yard-2", "")
	if err != nil || !strings.Contains(brief, "branch `yard/yard-2`") {
		t.Fatalf("brief names another branch:\n%s", brief)
	}
}

// worktreeHerdr is a fake herdr whose worktree create really makes the
// worktree, from --base, as herdr does; every call is logged to log.
func worktreeHerdr(t *testing.T, log string) string {
	return fakeHerdrBin(t, `printf '%s\n' "$*" >> `+log+`
case "$1 $2" in
  "worktree create")
    shift 2
    while [ $# -gt 1 ]; do
      case "$1" in --cwd) cwd=$2 ;; --branch) br=$2 ;; --base) base=$2 ;; --path) p=$2 ;; esac
      shift 2
    done
    git -C "$cwd" worktree add -q -b "$br" "$p" "$base" >&2 || exit 1
    echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"'"$p"'"}}}' ;;
  "tab create") echo '{"result":{"tab":{"tab_id":"t2"},"root_pane":{"pane_id":"p2"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)
}

// A child of an epic branches from the epic's branch, made from the rig's
// base the first time a child needs it and never moved after; YARDR_BASE
// and the brief name that branch. A bead with no epic is as before.
func TestHerdrChildBranchesFromItsEpic(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "A")
	a := gitOutT(t, repo, "rev-parse", "main")

	s := storetest.New(t)
	rig := store.Rig{Name: "r", Path: repo, Base: "main"}
	if err := s.PutRig(ctx, rig, "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "r-e", Title: "epic", Type: store.TypeEpic, Rig: "r"},
		{ID: "r-1", Title: "child one", Rig: "r"},
		{ID: "r-2", Title: "child two", Rig: "r"},
		{ID: "r-3", Title: "plain", Rig: "r"},
	} {
		if _, err := s.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []string{"r-1", "r-2"} {
		if err := s.AddDep(ctx, "r-e", c, store.DepParent, "me"); err != nil {
			t.Fatal(err)
		}
	}

	log := filepath.Join(t.TempDir(), "calls")
	h := &Herdr{Bin: worktreeHerdr(t, log), WorktreeRoot: t.TempDir(), Store: s}
	// start runs bead id as the dispatcher would, and returns the tab
	// create call (with the agent's env) and the brief.
	start := func(id string) (tabCall, brief string) {
		t.Helper()
		b, err := s.GetBead(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		fam, err := s.Family(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		os.Remove(log)
		handle, err := h.Start(ctx, dispatch.Job{Bead: *b, Group: store.Group{Name: "builders"}, Rig: &rig,
			Session: store.Session{ID: "s-" + id}, Epic: fam.Epic, Base: fam.Base})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(log)
		for _, l := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(l, "tab create ") {
				tabCall = l + " "
			}
		}
		text, _ := os.ReadFile(handle["brief"].(string))
		return tabCall, string(text)
	}
	branched := func() []store.Event {
		t.Helper()
		evs, err := s.Events(ctx, "r-e", 0)
		if err != nil {
			t.Fatal(err)
		}
		var out []store.Event
		for _, e := range evs {
			if e.Kind == "epic_branched" {
				out = append(out, e)
			}
		}
		return out
	}

	tab, brief := start("r-1")
	if got := gitOutT(t, repo, "rev-parse", "yard/r-e"); got != a {
		t.Fatalf("epic branch at %s, want main's tip %s", got, a)
	}
	if got := gitOutT(t, repo, "rev-parse", "yard/r-1"); got != a {
		t.Fatalf("child branched at %s, want %s", got, a)
	}
	if !strings.Contains(tab, " --env YARDR_BASE=yard/r-e ") {
		t.Errorf("child's env lacks YARDR_BASE=yard/r-e:\n%s", tab)
	}
	if !strings.Contains(brief, "child of epic r-e") || !strings.Contains(brief, "lands on that branch") {
		t.Errorf("brief does not say the bead is a child of r-e:\n%s", brief)
	}
	evs := branched()
	if len(evs) != 1 || evs[0].Data["commit"] != a || evs[0].Data["child"] != "r-1" {
		t.Fatalf("epic_branched events = %+v, want one at %s for r-1", evs, a)
	}

	// main moves on and a child lands on the epic branch: the next child
	// starts from the epic branch as it is, which is neither recreated nor
	// moved to main.
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "B")
	wt := filepath.Join(h.WorktreeRoot, "r", "r-1")
	gitT(t, wt, "commit", "-q", "--allow-empty", "-m", "C")
	gitT(t, repo, "branch", "-f", "yard/r-e", "yard/r-1")
	c := gitOutT(t, repo, "rev-parse", "yard/r-1")
	start("r-2")
	if got := gitOutT(t, repo, "rev-parse", "yard/r-e"); got != c {
		t.Fatalf("epic branch moved to %s, want it kept at %s", got, c)
	}
	if got := gitOutT(t, repo, "rev-parse", "yard/r-2"); got != c {
		t.Fatalf("second child branched at %s, want the epic's tip %s", got, c)
	}
	if n := len(branched()); n != 1 {
		t.Fatalf("%d epic_branched events, want the first only", n)
	}

	// A bead with no epic branches from main and is told so.
	b := gitOutT(t, repo, "rev-parse", "main")
	tab, brief = start("r-3")
	if got := gitOutT(t, repo, "rev-parse", "yard/r-3"); got != b {
		t.Fatalf("plain bead branched at %s, want main %s", got, b)
	}
	if !strings.Contains(tab, " --env YARDR_BASE=main ") || strings.Contains(brief, "epic") {
		t.Errorf("plain bead: env %s\nbrief:\n%s", tab, brief)
	}
}

// With a remote base the epic branch is made from the base as just
// fetched, not from a stale remote-tracking ref.
func TestHerdrEpicBranchFromFetchedBase(t *testing.T) {
	ctx := context.Background()
	origin := t.TempDir()
	gitT(t, origin, "init", "-q", "-b", "main")
	gitT(t, origin, "commit", "-q", "--allow-empty", "-m", "A")
	repo := filepath.Join(t.TempDir(), "repo")
	gitT(t, ".", "clone", "-q", origin, repo)
	gitT(t, origin, "commit", "-q", "--allow-empty", "-m", "B")
	b := gitOutT(t, origin, "rev-parse", "main")

	rig := store.Rig{Name: "r", Path: repo, Base: "origin/main"}
	h := &Herdr{Bin: worktreeHerdr(t, filepath.Join(t.TempDir(), "calls")), WorktreeRoot: t.TempDir()}
	_, err := h.Start(ctx, dispatch.Job{Bead: store.Bead{ID: "r-1", Stage: "new"}, Group: store.Group{Name: "g"},
		Rig: &rig, Session: store.Session{ID: "s"}, Epic: "r-e", Base: "yard/r-e"})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOutT(t, repo, "rev-parse", "yard/r-e"); got != b {
		t.Fatalf("epic branch at %s, want origin's fetched tip %s", got, b)
	}
	if up, err := gitOut(ctx, repo, "rev-parse", "--abbrev-ref", "yard/r-e@{upstream}"); err == nil {
		t.Fatalf("epic branch tracks %s; it is not a copy of the base", up)
	}
}
