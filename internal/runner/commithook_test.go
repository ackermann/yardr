package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// The tests in this file run in parallel: each drives real git commits, and
// the hook behind each commit spawns git several times, so in sequence they
// cost the package tens of seconds of its 60 s budget (docs/testing.md R1).
// Each owns its repositories and homes under t.TempDir and scrubs the
// session environment it passes down, so they share nothing.

// sessionGit runs git in dir with the session environment env: a scrubbed
// base (no YARDR_* or GIT_CONFIG_*, a hermetic identity and config, no
// editor) plus the session's own entries, as a pane or exec command gets
// them.
func sessionGit(t *testing.T, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	var base []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "YARDR_") || strings.HasPrefix(k, "GIT_CONFIG") ||
			k == "GIT_EDITOR" || k == "VISUAL" || k == "EDITOR" {
			continue
		}
		base = append(base, kv)
	}
	base = append(base,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(base, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func sessionGitT(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	out, err := sessionGit(t, dir, env, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

// sessionJob is a bead worked by group, as the harness kind kind with the
// group's model.
func sessionJob(bead, group, kind, model string) dispatch.Job {
	return dispatch.Job{
		Bead:    store.Bead{ID: bead, Type: "task", Stage: "new"},
		Group:   store.Group{Name: group, Config: map[string]any{"kind": kind, "model": model}},
		Session: store.Session{ID: "s1"},
	}
}

func sessionRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
	return repo
}

func sessionLog(t *testing.T, dir string, env []string) string {
	t.Helper()
	return sessionGitT(t, dir, env, "log", "-1", "--format=%B")
}

// testHookEnv installs a hooks dir holding the yard's commit-msg hook plus
// the named pass-through shims, and returns the session lines pointing git
// at it. A pass-through changes nothing where the repo has no hook of that
// name, and each one git runs is two processes (the shim, its rev-parse),
// so tests install only the shims their repo hooks need; the full install
// is covered at the file level by TestEnsureCommitHookInstallsPassthroughs,
// and every shim that fires here runs the same pass-through body.
func testHookEnv(t *testing.T, home string, shims ...string) []string {
	t.Helper()
	dir := config.At(home).GitHooks()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := []hookFile{{"commit-msg", commitHook}}
	for _, name := range shims {
		files = append(files, hookFile{name, passthroughHook})
	}
	for _, f := range files {
		if err := ensureHookFile(dir, f); err != nil {
			t.Fatal(err)
		}
	}
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=" + dir,
		"GIT_CONFIG_PARAMETERS=",
	}
}

// sessionTestEnv is the session env with a hooks dir holding the yard's
// commit-msg hook plus the named pass-through shims (see testHookEnv).
func sessionTestEnv(t *testing.T, home string, job dispatch.Job, name string, shims ...string) []string {
	t.Helper()
	return (&Herdr{YardHome: home}).agentEnvWithHook(job, name, testHookEnv(t, home, shims...))
}

// The yard owns the session's commit-msg hook: it is written under the
// yard's home, executable, and syntactically sound; a second install leaves
// it alone, an outdated or unexecutable one is repaired, and a home that
// holds none is an error, not a half-written hook.
func TestEnsureCommitHook(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir, err := EnsureCommitHook(home)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(home, "githooks") {
		t.Fatalf("dir = %q", dir)
	}
	hook := filepath.Join(dir, "commit-msg")
	body, err := os.ReadFile(hook)
	if err != nil || string(body) != commitHook {
		t.Fatalf("hook = %q, %v", body, err)
	}
	if st, err := os.Stat(hook); err != nil || st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("hook not executable: %v", st)
	}
	if out, err := exec.Command("sh", "-n", hook).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
	before, _ := os.Stat(hook)
	if _, err := EnsureCommitHook(home); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(hook); !after.ModTime().Equal(before.ModTime()) {
		t.Error("a second install rewrote the hook")
	}
	if err := os.WriteFile(hook, []byte("# old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCommitHook(home); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(hook); string(body) != commitHook {
		t.Error("an outdated hook was not replaced")
	}
	if err := os.Chmod(hook, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCommitHook(home); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(hook); st.Mode().Perm()&0o111 == 0 {
		t.Error("an unexecutable hook was not repaired")
	}
	if _, err := EnsureCommitHook(hook); err == nil {
		t.Error("a home that holds no hooks dir installed one")
	}
}

// The yard's hooks directory holds a pass-through for every other hook
// git looks up by name (see passthroughHooks): each is the shim, executable
// and syntactically sound, and an outdated or unexecutable one is repaired
// as the commit-msg hook is.
func TestEnsureCommitHookInstallsPassthroughs(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir, err := EnsureCommitHook(home)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range passthroughHooks {
		hook := filepath.Join(dir, name)
		body, err := os.ReadFile(hook)
		if err != nil || string(body) != passthroughHook {
			t.Fatalf("%s = %q, %v", name, body, err)
		}
		if st, err := os.Stat(hook); err != nil || st.Mode().Perm()&0o111 == 0 {
			t.Fatalf("%s not executable: %v", name, st)
		}
		// Every shim is byte-identical (checked above), so one syntax check
		// covers all of them; each sh -n is a process spawn.
		if i == 0 {
			if out, err := exec.Command("sh", "-n", hook).CombinedOutput(); err != nil {
				t.Fatalf("%s: sh -n: %v\n%s", name, err, out)
			}
		}
	}
	one := filepath.Join(dir, "pre-commit")
	if err := os.WriteFile(one, []byte("# old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "pre-push")
	if err := os.Chmod(other, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCommitHook(home); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(one); string(body) != passthroughHook {
		t.Error("an outdated pass-through was not replaced")
	}
	if st, _ := os.Stat(other); st.Mode().Perm()&0o111 == 0 {
		t.Error("an unexecutable pass-through was not repaired")
	}
}

// writeRepoHook writes one executable hook into dir.
func writeRepoHook(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// otherHooksRepo is a session repo with a hooks dir and the session env to
// commit in it: the yard's commit-msg hook plus the pre-commit and
// prepare-commit-msg shims.
func otherHooksRepo(t *testing.T) (repo, hooks string, env []string) {
	t.Helper()
	repo = sessionRepo(t)
	hooks = filepath.Join(repo, ".git", "hooks")
	if err := os.MkdirAll(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	job := sessionJob("web-3", "builders", "claude", "opus")
	env = sessionTestEnv(t, t.TempDir(), job, "web-3-new", "pre-commit", "prepare-commit-msg")
	return repo, hooks, env
}

// The repo's other hooks still run in a session, not only its commit-msg:
// a pre-commit that refuses blocks the session's commit as it would
// without the yard's hook, prepare-commit-msg runs, and a commit the
// repo's hooks allow still carries the yard's trailers. A depot that
// points its own core.hooksPath elsewhere has its pre-commit run from
// there too.
func TestSessionCommitRunsOtherRepoHooks(t *testing.T) {
	t.Parallel()
	t.Run("refused", func(t *testing.T) {
		t.Parallel()
		repo, hooks, env := otherHooksRepo(t)
		preRan := filepath.Join(repo, ".git", "pre-commit.ran")
		writeRepoHook(t, hooks, "pre-commit", "#!/bin/sh\necho ran > "+preRan+"\nexit 1\n")
		if out, err := sessionGit(t, repo, env, "commit", "--allow-empty", "-m", "web-3: refused"); err == nil {
			t.Fatalf("a refusing repo pre-commit committed:\n%s", out)
		}
		if _, err := os.Stat(preRan); err != nil {
			t.Fatal("the repo's pre-commit hook did not run")
		}
	})
	t.Run("allowed", func(t *testing.T) {
		t.Parallel()
		repo, hooks, env := otherHooksRepo(t)
		preRan := filepath.Join(repo, ".git", "pre-commit.ran")
		prepareRan := filepath.Join(repo, ".git", "prepare-commit-msg.ran")
		writeRepoHook(t, hooks, "pre-commit", "#!/bin/sh\necho ran > "+preRan+"\n")
		writeRepoHook(t, hooks, "prepare-commit-msg", "#!/bin/sh\necho ran > "+prepareRan+"\n")
		sessionGitT(t, repo, env, "commit", "-q", "--allow-empty", "-m", "web-3: allowed")
		if _, err := os.Stat(preRan); err != nil {
			t.Fatal("the repo's pre-commit hook did not run")
		}
		if _, err := os.Stat(prepareRan); err != nil {
			t.Fatal("the repo's prepare-commit-msg hook did not run")
		}
		if body := sessionLog(t, repo, env); strings.Count(body, "Yardr-Bead: web-3") != 1 ||
			strings.Count(body, "Yardr-Agent: builders (claude opus)") != 1 {
			t.Fatalf("yard trailers missing beside repo hooks:\n%s", body)
		}
	})
	// From a depot-configured hooksPath, as for commit-msg.
	t.Run("configured hooksPath", func(t *testing.T) {
		t.Parallel()
		repo, _, env := otherHooksRepo(t)
		custom := filepath.Join(repo, "myhooks")
		if err := os.MkdirAll(custom, 0o755); err != nil {
			t.Fatal(err)
		}
		customRan := filepath.Join(repo, ".git", "custom-pre-commit.ran")
		writeRepoHook(t, custom, "pre-commit", "#!/bin/sh\necho ran > "+customRan+"\nexit 1\n")
		gitT(t, repo, "config", "core.hooksPath", "myhooks")
		if out, err := sessionGit(t, repo, env, "commit", "--allow-empty", "-m", "web-3: refused"); err == nil {
			t.Fatalf("a refusing configured pre-commit committed:\n%s", out)
		}
		if _, err := os.Stat(customRan); err != nil {
			t.Fatal("the depot's configured pre-commit hook did not run")
		}
	})
}

// pushThroughPrePush pushes one session commit past a repo pre-push hook
// that refuses when refuse is set.
func pushThroughPrePush(t *testing.T, refuse bool) {
	t.Helper()
	repo := sessionRepo(t)
	home := t.TempDir()
	bare := t.TempDir()
	gitT(t, bare, "init", "-q", "--bare", "-b", "main")
	ran := filepath.Join(repo, ".git", "pre-push.ran")
	body := "#!/bin/sh\necho ran > " + ran + "\n"
	if refuse {
		body = "#!/bin/sh\necho ran > " + ran + "\nexit 1\n"
	}
	writeRepoHook(t, filepath.Join(repo, ".git", "hooks"), "pre-push", body)
	job := sessionJob("web-3", "builders", "codex", "luna")
	env := sessionTestEnv(t, home, job, "web-3-new", "pre-push")
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty", "-m", "web-3: to push")
	out, err := sessionGit(t, repo, env, "push", bare, "main")
	if refuse && err == nil {
		t.Fatalf("a refusing repo pre-push pushed:\n%s", out)
	}
	if !refuse && err != nil {
		t.Fatalf("an allowing repo pre-push refused:\n%s", out)
	}
	if _, err := os.Stat(ran); err != nil {
		t.Fatal("the repo's pre-push hook did not run")
	}
}

// A repo pre-push hook still guards a session's push: one that refuses
// leaves the remote where it was, one that allows lets the push through.
func TestSessionPushRunsRepoPrePush(t *testing.T) {
	t.Parallel()
	t.Run("refused", func(t *testing.T) {
		t.Parallel()
		pushThroughPrePush(t, true)
	})
	t.Run("allowed", func(t *testing.T) {
		t.Parallel()
		pushThroughPrePush(t, false)
	})
}

// A runner does not start work when it cannot install the hook that gives
// session commits their trailers.
func TestRunnerDoesNotStartWithoutCommitHook(t *testing.T) {
	t.Parallel()
	home := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(home, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "started")
	job := dispatch.Job{
		Bead:  store.Bead{ID: "web-3", Type: "task", Stage: "new"},
		Group: store.Group{Name: "builders", Config: map[string]any{"cmd": "touch " + marker}},
	}
	if _, err := (&Exec{Home: home}).Start(t.Context(), job); err == nil ||
		!strings.Contains(err.Error(), "install yard commit-msg hook") {
		t.Fatalf("exec start error = %v, want a commit-hook install error", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("exec command started without its hook: %v", err)
	}

	job.Depot = &store.Depot{Name: "web", Path: t.TempDir(), Kind: store.DepotGit}
	if _, err := (&Herdr{YardHome: home}).Start(t.Context(), job); err == nil ||
		!strings.Contains(err.Error(), "install yard commit-msg hook") {
		t.Fatalf("herdr start error = %v, want a commit-hook install error", err)
	}
}

// A commit made in a session's worktree by each harness ends in exactly the
// yard's two trailers, whatever the harness signed with (each of several
// Co-Authored-By, in whatever case, and Signed-off-by go); any other
// trailer stays, and the depot's shared .git/config is unchanged.
func TestSessionCommitsCarryYardrTrailers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ kind, model string }{
		{"claude", "claude-opus-4-5"},
		{"codex", "gpt-6-luna"},
		{"muse", "muse-spark"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			carryYardrTrailers(t, tc.kind, tc.model)
		})
	}
}

func carryYardrTrailers(t *testing.T, kind, model string) {
	t.Helper()
	repo := sessionRepo(t)
	before, err := os.ReadFile(filepath.Join(repo, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	job := sessionJob("web-3", "builders", kind, model)
	env := sessionTestEnv(t, home, job, "web-3-new")
	for _, want := range []string{
		"YARDR_KIND=" + kind, "YARDR_MODEL=" + model,
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=" + filepath.Join(home, "githooks"),
		"GIT_CONFIG_PARAMETERS=",
	} {
		found := false
		for _, kv := range env {
			found = found || kv == want
		}
		if !found {
			t.Errorf("session env lacks %q", want)
		}
	}
	msg := "web-3: carry the yard's trailers\n\nA body naming no trailers.\n\n" +
		"Co-Authored-By: Claude Opus 4.5 <noreply@anthropic.com>\n" +
		"co-authored-by: second <two@example.com>\n" +
		"Signed-off-by: Christopher Scott <c@example.com>\n" +
		"Reviewed-by: Zed <z@example.com>\n"
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty", "-m", msg)
	body := sessionLog(t, repo, env)
	for _, want := range []string{"Yardr-Bead: web-3", "Yardr-Agent: builders (" + kind + " " + model + ")", "Reviewed-by: Zed <z@example.com>"} {
		if n := strings.Count(body, want); n != 1 {
			t.Errorf("%q %d times, want once:\n%s", want, n, body)
		}
	}
	for _, gone := range []string{"co-authored-by", "signed-off-by"} {
		if strings.Contains(strings.ToLower(body), gone) {
			t.Errorf("%s survived:\n%s", gone, body)
		}
	}
	if after, _ := os.ReadFile(filepath.Join(repo, ".git", "config")); string(after) != string(before) {
		t.Errorf(".git/config changed:\n%s", after)
	}
}

// Repo trailer config must not rename the yard's trailers, change their
// separators, or make the hook miss harness trailers while parsing.
func TestSessionCommitIgnoresRepoTrailerConfig(t *testing.T) {
	t.Parallel()
	repo := sessionRepo(t)
	for key, value := range map[string]string{
		"trailer.separators":         ";",
		"trailer.yardr-bead.key":     "Other-Bead:",
		"trailer.co-authored-by.key": "Bot:",
		"trailer.signed-off-by.key":  "Signoff:",
	} {
		gitT(t, repo, "config", key, value)
	}
	job := sessionJob("web-3", "builders", "codex", "luna")
	env := sessionTestEnv(t, t.TempDir(), job, "web-3-new")
	message := "web-3: ignore trailer config\n\n" +
		"Co-Authored-By: Claude <c@example.com>\n" +
		"Signed-off-by: Operator <o@example.com>\n"
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty", "-m", message)
	body := sessionLog(t, repo, env)
	for _, want := range []string{"Yardr-Bead: web-3", "Yardr-Agent: builders (codex luna)"} {
		if n := strings.Count(body, want); n != 1 {
			t.Errorf("%q %d times, want once:\n%s", want, n, body)
		}
	}
	for _, gone := range []string{"Co-Authored-By", "Signed-off-by", "Other-Bead", "Bot:", "Signoff:"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(gone)) {
			t.Errorf("%q survived:\n%s", gone, body)
		}
	}
}

// Amending never duplicates the yard's trailers: neither amending in place
// nor amending with a message the harness signed again.
func TestSessionCommitAmendKeepsSingleTrailers(t *testing.T) {
	t.Parallel()
	repo := sessionRepo(t)
	home := t.TempDir()
	job := sessionJob("web-3", "builders", "claude", "opus")
	env := sessionTestEnv(t, home, job, "web-3-new")
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty",
		"-m", "web-3: one\n\nCo-Authored-By: Claude <c@example.com>\n")
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty", "--amend", "--no-edit")
	if body := sessionLog(t, repo, env); strings.Count(body, "Yardr-Bead: web-3") != 1 ||
		strings.Count(body, "Yardr-Agent: builders (claude opus)") != 1 ||
		strings.Contains(strings.ToLower(body), "co-authored-by") {
		t.Fatalf("amended in place:\n%s", body)
	}
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty", "--amend",
		"-m", "web-3: two\n\nCo-Authored-By: Claude <c@example.com>\n")
	if body := sessionLog(t, repo, env); !strings.Contains(body, "web-3: two\n") ||
		strings.Count(body, "Yardr-Bead: web-3") != 1 ||
		strings.Count(body, "Yardr-Agent: builders (claude opus)") != 1 ||
		strings.Contains(strings.ToLower(body), "co-authored-by") {
		t.Fatalf("amended with a signed message:\n%s", body)
	}
}

// Every matching trailer is removed, however many the message contains,
// and duplicate Yardr trailers are replaced by exactly the current pair.
func TestSessionCommitNormalizesDuplicateTrailers(t *testing.T) {
	t.Parallel()
	repo := sessionRepo(t)
	home := t.TempDir()
	job := sessionJob("web-3", "builders", "muse", "spark")
	env := sessionTestEnv(t, home, job, "web-3-new")

	var msg strings.Builder
	msg.WriteString("web-3: normalize all trailers\n\n")
	for i := 0; i < 12; i++ {
		msg.WriteString("Co-Authored-By: harness <h@example.com>\n")
		msg.WriteString("Signed-off-by: operator <o@example.com>\n")
	}
	msg.WriteString("Yardr-Bead: old-one\nYardr-Bead: old-two\n")
	msg.WriteString("Yardr-Agent: old-one\nYardr-Agent: old-two\n")
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty", "-m", msg.String())

	body := sessionLog(t, repo, env)
	for _, gone := range []string{"co-authored-by", "signed-off-by", "Yardr-Bead: old-", "Yardr-Agent: old-"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(gone)) {
			t.Errorf("%q survived:\n%s", gone, body)
		}
	}
	for _, want := range []string{"Yardr-Bead: web-3", "Yardr-Agent: builders (muse spark)"} {
		if n := strings.Count(body, want); n != 1 {
			t.Errorf("%q %d times, want once:\n%s", want, n, body)
		}
	}
}

// The repo's own commit-msg hook still runs: what it adds stays beside the
// yard's trailers, and one that refuses the message fails the commit.
func TestSessionCommitRunsRepoHook(t *testing.T) {
	t.Parallel()
	repo := sessionRepo(t)
	home := t.TempDir()
	ran := filepath.Join(repo, ".git", "repo-hook.ran")
	hook := "#!/bin/sh\necho ran > " + ran + "\nprintf 'Reviewed-by: Repo <r@example.com>\\n' >> \"$1\"\n"
	if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "commit-msg"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	job := sessionJob("web-3", "builders", "codex", "luna")
	env := sessionTestEnv(t, home, job, "web-3-new")
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty",
		"-m", "web-3: chained\n\nSigned-off-by: Christopher Scott <c@example.com>\n")
	if _, err := os.Stat(ran); err != nil {
		t.Fatal("the repo's commit-msg hook did not run")
	}
	body := sessionLog(t, repo, env)
	for _, want := range []string{"Yardr-Bead: web-3", "Yardr-Agent: builders (codex luna)", "Reviewed-by: Repo <r@example.com>"} {
		if n := strings.Count(body, want); n != 1 {
			t.Errorf("%q %d times, want once:\n%s", want, n, body)
		}
	}
	if strings.Contains(strings.ToLower(body), "signed-off-by") {
		t.Errorf("signed-off-by survived:\n%s", body)
	}

	refusing := "#!/bin/sh\necho refused >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "commit-msg"), []byte(refusing), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := sessionGit(t, repo, env, "commit", "--allow-empty", "-m", "web-3: refused"); err == nil {
		t.Fatalf("a refusing repo hook committed:\n%s", out)
	}
}

// A depot that points its own core.hooksPath elsewhere still has its hook
// run: the yard chains to what git would run without the session's
// override, not only to the git dir's hooks.
func TestSessionCommitChainsConfiguredHooksPath(t *testing.T) {
	t.Parallel()
	repo := sessionRepo(t)
	home := t.TempDir()
	ran := filepath.Join(repo, ".git", "custom-hook.ran")
	custom := filepath.Join(repo, "myhooks")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\necho ran > " + ran + "\n"
	if err := os.WriteFile(filepath.Join(custom, "commit-msg"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "config", "core.hooksPath", "myhooks")
	job := sessionJob("web-3", "builders", "claude", "opus")
	env := sessionTestEnv(t, home, job, "web-3-new")
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty", "-m", "web-3: custom hooks path")
	if _, err := os.Stat(ran); err != nil {
		t.Fatal("the depot's configured commit-msg hook did not run")
	}
	if body := sessionLog(t, repo, env); strings.Count(body, "Yardr-Bead: web-3") != 1 {
		t.Fatalf("yard trailers missing:\n%s", body)
	}
}

// A session commits in a linked worktree, not in the depot checkout: its
// commits carry the yard's trailers there too.
func TestSessionCommitInLinkedWorktreeCarriesTrailers(t *testing.T) {
	t.Parallel()
	repo := sessionRepo(t)
	home := t.TempDir()
	wt := filepath.Join(t.TempDir(), "web-3")
	gitT(t, repo, "worktree", "add", "-q", "-b", "yard/web-3", wt)
	job := sessionJob("web-3", "builders", "codex", "luna")
	env := sessionTestEnv(t, home, job, "web-3-new")
	sessionGitT(t, wt, env, "commit", "-q", "--allow-empty",
		"-m", "web-3: from the worktree\n\nSigned-off-by: Christopher Scott <c@example.com>\n")
	body := sessionLog(t, wt, env)
	for _, want := range []string{"Yardr-Bead: web-3", "Yardr-Agent: builders (codex luna)"} {
		if n := strings.Count(body, want); n != 1 {
			t.Errorf("%q %d times, want once:\n%s", want, n, body)
		}
	}
	if strings.Contains(strings.ToLower(body), "signed-off-by") {
		t.Errorf("signed-off-by survived:\n%s", body)
	}
}

// Outside a session the yard's hook leaves the message alone (there is no
// bead to name), while the repo's own hook still runs.
func TestCommitOutsideSessionLeavesMessage(t *testing.T) {
	t.Parallel()
	repo := sessionRepo(t)
	home := t.TempDir()
	ran := filepath.Join(repo, ".git", "repo-hook.ran")
	hook := "#!/bin/sh\necho ran > " + ran + "\n"
	if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "hooks", "commit-msg"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	env := testHookEnv(t, home)
	msg := "a change\n\nCo-Authored-By: Someone <s@example.com>\n"
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty", "-m", msg)
	if _, err := os.Stat(ran); err != nil {
		t.Fatal("the repo's commit-msg hook did not run")
	}
	if body := sessionLog(t, repo, env); !strings.Contains(body, "Co-Authored-By: Someone <s@example.com>") ||
		strings.Contains(body, "Yardr-Bead:") || strings.Contains(body, "Yardr-Agent:") {
		t.Fatalf("message not left alone:\n%s", body)
	}
}

// An exec session runs a script, not an agent: its commits name the group
// with kind exec, and the bead's model override still applies.
func TestExecSessionCommitNamesExec(t *testing.T) {
	t.Parallel()
	repo := sessionRepo(t)
	home := t.TempDir()
	job := dispatch.Job{
		Bead:    store.Bead{ID: "yd-7", Type: "task", Stage: "approved", Metadata: map[string]any{"model": "omni"}},
		Group:   store.Group{Name: "assembly", Config: map[string]any{}},
		Session: store.Session{ID: "s1"},
	}
	e := &Exec{Home: home, Bin: "/bin/yardr"}
	hookEnv := testHookEnv(t, home)
	var env []string
	env = append(env, Env(job, job.Group.Name)...)
	env = append(env, e.env(job, hookEnv)...)
	for _, want := range []string{"YARDR_KIND=exec", "YARDR_MODEL=omni", "GIT_CONFIG_COUNT=1"} {
		found := false
		for _, kv := range env {
			found = found || kv == want
		}
		if !found {
			t.Errorf("exec env lacks %q", want)
		}
	}
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty", "-m", "yd-7: land\n\nSigned-off-by: Christopher Scott <c@example.com>\n")
	if body := sessionLog(t, repo, env); strings.Count(body, "Yardr-Agent: assembly (exec omni)") != 1 ||
		strings.Contains(strings.ToLower(body), "signed-off-by") {
		t.Fatalf("exec commit:\n%s", body)
	}
	job.Bead.Metadata = nil
	env = append(Env(job, job.Group.Name), e.env(job, hookEnv)...)
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty", "-m", "yd-7: again")
	if body := sessionLog(t, repo, env); strings.Count(body, "Yardr-Agent: assembly (exec)") != 1 {
		t.Fatalf("exec commit with no model:\n%s", body)
	}
}

// A commit through the editor carries the yard's trailers too: the hook
// appends past git's comment lines, which the cleanup strips.
func TestEditorCommitCarriesYardrTrailers(t *testing.T) {
	t.Parallel()
	repo := sessionRepo(t)
	home := t.TempDir()
	job := sessionJob("web-3", "builders", "muse", "spark")
	env := sessionTestEnv(t, home, job, "web-3-new")
	editor := filepath.Join(t.TempDir(), "editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'web-3: through the editor\\n' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	env = append(env, "GIT_EDITOR="+editor)
	sessionGitT(t, repo, env, "commit", "-q", "--allow-empty")
	body := sessionLog(t, repo, env)
	for _, want := range []string{"web-3: through the editor", "Yardr-Bead: web-3", "Yardr-Agent: builders (muse spark)"} {
		if n := strings.Count(body, want); n != 1 {
			t.Errorf("%q %d times, want once:\n%s", want, n, body)
		}
	}
}

// The session model is the bead's override, else the group's, as the
// agent's own --model.
func TestSessionModel(t *testing.T) {
	t.Parallel()
	job := func(groupCfg, beadMeta map[string]any) dispatch.Job {
		return dispatch.Job{Group: store.Group{Config: groupCfg}, Bead: store.Bead{Metadata: beadMeta}}
	}
	for _, tc := range []struct {
		name string
		job  dispatch.Job
		want string
	}{
		{"group", job(map[string]any{"model": "haiku"}, nil), "haiku"},
		{"bead beats group", job(map[string]any{"model": "haiku"}, map[string]any{"model": "opus"}), "opus"},
		{"nothing", job(map[string]any{}, nil), ""},
		{"non-string", job(map[string]any{}, map[string]any{"model": 42}), ""},
	} {
		if got := sessionModel(tc.job); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// An exec command inherits no GIT_CONFIG_* from the daemon or its env_file:
// either would win over (or add to) the core.hooksPath the session points
// at its own commit-msg hook with.
func TestWithheldDropsGitConfig(t *testing.T) {
	t.Parallel()
	job := dispatch.Job{Depot: &store.Depot{Name: "web", Base: "main"}}
	got := withheld(job, []string{"PATH=/bin", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=a",
		"GIT_CONFIG_VALUE_0=b", "GIT_CONFIG_KEY_1=c", "GIT_CONFIG_VALUE_1=d",
		"GIT_CONFIG_PARAMETERS='core.hooksPath=/tmp/other'", "YARDR_TEST=x"})
	for _, kv := range got {
		if strings.HasPrefix(kv, "GIT_CONFIG") || strings.HasPrefix(kv, "YARDR_TEST=") {
			t.Errorf("withheld kept %q: %v", kv, got)
		}
	}
	found := false
	for _, kv := range got {
		found = found || kv == "PATH=/bin"
	}
	if !found {
		t.Errorf("withheld dropped PATH: %v", got)
	}
}
