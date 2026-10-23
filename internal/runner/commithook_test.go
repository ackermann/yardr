package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
// base (no YARDR_* or GIT_CONFIG_*, a hermetic identity and config, an
// empty home, no editor) plus the session's own entries, as a pane or exec
// command gets them. The home is empty so the hooks' own reads of the
// global config see nothing of the operator's: what the hooks resolve is
// the test's alone. A test that needs a home with content of its own sets
// HOME in env, which wins over the base.
func sessionGit(t *testing.T, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	var base []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "YARDR_") || strings.HasPrefix(k, "GIT_CONFIG") ||
			k == "GIT_EDITOR" || k == "VISUAL" || k == "EDITOR" ||
			k == "XDG_CONFIG_HOME" {
			continue
		}
		base = append(base, kv)
	}
	base = append(base,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"HOME="+t.TempDir())
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

// sessionRepo is a fresh repository on branch main with one empty root
// commit, copied from the seed (see seedRepo) rather than made by git.
func sessionRepo(t *testing.T) string {
	t.Helper()
	return seedRepo(t)
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
		// The tests' git reads no system config (see sessionGit), so the
		// yard's word about it holds here as it does in a session whose
		// system file is clean.
		"YARDR_GIT_SYSTEM_CLEAN=1",
	}
}

// sessionTestEnv is the session env with a hooks dir holding the yard's
// commit-msg hook plus the named pass-through shims (see testHookEnv).
func sessionTestEnv(t *testing.T, home string, job dispatch.Job, name string, shims ...string) []string {
	t.Helper()
	return (&Herdr{YardHome: home}).agentEnvWithHook(job, name, testHookEnv(t, home, shims...))
}

// shellQuote quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// logGitBin installs a git that logs every call to a file of its own and
// runs the real git: with its dir first on PATH, a hook run directly
// reveals every nested git call it makes.
func logGitBin(t *testing.T) (bin, log string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin, log = t.TempDir(), filepath.Join(t.TempDir(), "calls")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %s\nexec %s \"$@\"\n",
		shellQuote(log), shellQuote(realGit))
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

// execPathOverlay is logGitBin as a GIT_EXEC_PATH dir: git prepends its
// exec dir to hooks' PATH and that dir holds a git, so a PATH wrapper never
// sees nested calls from real git runs; the overlay's git does, while its
// subcommands run as usual through symlinks to the real ones.
func execPathOverlay(t *testing.T) (dir, log string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(realGit, "--exec-path").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	execPath := strings.TrimSpace(string(out))
	entries, err := os.ReadDir(execPath)
	if err != nil {
		t.Fatal(err)
	}
	dir, log = t.TempDir(), filepath.Join(t.TempDir(), "calls")
	for _, e := range entries {
		if err := os.Symlink(filepath.Join(execPath, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	os.Remove(filepath.Join(dir, "git"))
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %s\nexec %s \"$@\"\n",
		shellQuote(log), shellQuote(realGit))
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, log
}

// nestedCalls reads a log written by logGitBin or execPathOverlay: one
// logged git call per line, empty when nothing was logged.
func nestedCalls(t *testing.T, log string) []string {
	t.Helper()
	raw, err := os.ReadFile(log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// directHookEnv is the environment for running a hook body directly: the
// scrubbed base (no YARDR_*, GIT_* or editor, an empty home) with bindir
// first on PATH, plus extra. Unlike sessionGit it sets no global-config
// override, so a case controls every config file the hook may read.
func directHookEnv(t *testing.T, bindir string, extra ...string) []string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "YARDR_") || strings.HasPrefix(k, "GIT_") ||
			k == "EDITOR" || k == "VISUAL" || k == "XDG_CONFIG_HOME" {
			continue
		}
		env = append(env, kv)
	}
	base := []string{"HOME=" + t.TempDir(), "PATH=" + bindir + ":" + os.Getenv("PATH")}
	return append(env, append(base, extra...)...)
}

// runHook runs the hook body at path in cwd with env and args, and returns
// its combined output and exit code.
func runHook(t *testing.T, path, cwd string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(path, args...)
	cmd.Dir, cmd.Env = cwd, env
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), exit.ExitCode()
	}
	t.Fatalf("run %s: %v\n%s", path, err, out)
	return "", 0
}

// yardHooks installs the yard's full hooks directory under a throwaway
// home and returns it.
func yardHooks(t *testing.T) string {
	t.Helper()
	dir, err := EnsureCommitHook(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
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
	// Planted through remove-and-write: writing in place would corrupt
	// the shared cached inode every home links.
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
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
	// Planted through remove-and-write, not chmod: the installed file
	// shares its inode with the cache, and chmod would de-execute that.
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte(commitHook), 0o644); err != nil {
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
	// Planted through remove-and-write, as above: no write into the
	// shared inode.
	if err := os.Remove(one); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(one, []byte("# old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "pre-push")
	// Planted through remove-and-write, as above: no chmod of the
	// shared inode.
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(passthroughHook), 0o644); err != nil {
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
		"GIT_CONFIG_PARAMETERS=", "YARDR_GIT_SYSTEM_CLEAN=1",
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
	for _, want := range []string{"YARDR_KIND=exec", "YARDR_MODEL=omni", "GIT_CONFIG_COUNT=1", "YARDR_GIT_SYSTEM_CLEAN=1"} {
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
// at its own commit-msg hook with. Neither does it inherit the yard's word
// about the system gitconfig, which is this session's to give.
func TestWithheldDropsGitConfig(t *testing.T) {
	t.Parallel()
	job := dispatch.Job{Depot: &store.Depot{Name: "web", Base: "main"}}
	got := withheld(job, []string{"PATH=/bin", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=a",
		"GIT_CONFIG_VALUE_0=b", "GIT_CONFIG_KEY_1=c", "GIT_CONFIG_VALUE_1=d",
		"GIT_CONFIG_PARAMETERS='core.hooksPath=/tmp/other'", "YARDR_TEST=x",
		"YARDR_GIT_SYSTEM_CLEAN=1"})
	for _, kv := range got {
		if strings.HasPrefix(kv, "GIT_CONFIG") || strings.HasPrefix(kv, "YARDR_TEST=") ||
			strings.HasPrefix(kv, "YARDR_GIT_SYSTEM_CLEAN=") {
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

// shimSetup builds one direct-shim case: the directory to run the shim in,
// the environment entries past the scrubbed base, and the marker the repo
// hook touches when it runs ("" when the case installs no repo hook).
type shimSetup func(t *testing.T) (cwd string, extra []string, marker string)

// shimMarkerHook is a repo hook body touching marker, then refusing when
// refuse is set.
func shimMarkerHook(marker string, refuse bool) string {
	body := "#!/bin/sh\necho ran > " + marker + "\n"
	if refuse {
		body += "exit 1\n"
	}
	return body
}

// The shim finds the repo's hooks from the filesystem, with no git call,
// in every plain layout (checkout, linked worktree, bare, GIT_DIR, git
// file), and runs what it finds there; anything it cannot prove — a
// hooksPath or include in any config it reads, no word from the yard
// about the system config, an odd git dir — falls back to asking git,
// which still runs the repo's hook. Each case runs the shim directly, so
// the routing is what's pinned, at shell speed.
func TestShimFindsRepoHooksWithoutGit(t *testing.T) {
	t.Parallel()
	hooks := yardHooks(t) // installed once: the cases only run them
	clean := []string{"YARDR_GIT_SYSTEM_CLEAN=1"}
	markerIn := func(t *testing.T, dir string) string {
		t.Helper()
		return filepath.Join(dir, "hook.ran")
	}
	customHooks := func(t *testing.T, marker string, refuse bool) string {
		t.Helper()
		custom := t.TempDir()
		writeRepoHook(t, custom, "pre-commit", shimMarkerHook(marker, refuse))
		return custom
	}
	for _, tc := range []struct {
		name    string
		hook    string
		setup   shimSetup
		wantRan bool
		wantGit bool
		want    int
	}{
		{
			name: "no hooks",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				return sessionRepo(t), clean, ""
			},
		},
		{
			name: "repo hook runs",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				hooks := filepath.Join(repo, ".git", "hooks")
				if err := os.MkdirAll(hooks, 0o700); err != nil {
					t.Fatal(err)
				}
				marker := markerIn(t, repo)
				writeRepoHook(t, hooks, "pre-commit", shimMarkerHook(marker, false))
				return repo, clean, marker
			},
			wantRan: true,
		},
		{
			name: "unexecutable hook skipped",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				hooks := filepath.Join(repo, ".git", "hooks")
				if err := os.MkdirAll(hooks, 0o700); err != nil {
					t.Fatal(err)
				}
				marker := markerIn(t, repo)
				if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte(shimMarkerHook(marker, false)), 0o644); err != nil {
					t.Fatal(err)
				}
				return repo, clean, marker
			},
		},
		{
			name: "refusing hook refuses",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				hooks := filepath.Join(repo, ".git", "hooks")
				if err := os.MkdirAll(hooks, 0o700); err != nil {
					t.Fatal(err)
				}
				marker := markerIn(t, repo)
				writeRepoHook(t, hooks, "pre-commit", shimMarkerHook(marker, true))
				return repo, clean, marker
			},
			wantRan: true,
			want:    1,
		},
		{
			name: "linked worktree",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				wt := filepath.Join(t.TempDir(), "wt")
				gitT(t, repo, "worktree", "add", "-q", "-b", "yard/wt", wt)
				hooks := filepath.Join(repo, ".git", "hooks")
				if err := os.MkdirAll(hooks, 0o700); err != nil {
					t.Fatal(err)
				}
				marker := markerIn(t, repo)
				writeRepoHook(t, hooks, "pre-commit", shimMarkerHook(marker, false))
				return wt, clean, marker
			},
			wantRan: true,
		},
		{
			name: "bare repository",
			hook: "pre-receive",
			setup: func(t *testing.T) (string, []string, string) {
				bare := t.TempDir()
				gitT(t, bare, "init", "-q", "--bare", "-b", "main")
				hooks := filepath.Join(bare, "hooks")
				marker := markerIn(t, bare)
				writeRepoHook(t, hooks, "pre-receive", shimMarkerHook(marker, false))
				return bare, clean, marker
			},
			wantRan: true,
		},
		{
			name: "absolute GIT_DIR",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				hooks := filepath.Join(repo, ".git", "hooks")
				if err := os.MkdirAll(hooks, 0o700); err != nil {
					t.Fatal(err)
				}
				marker := markerIn(t, repo)
				writeRepoHook(t, hooks, "pre-commit", shimMarkerHook(marker, false))
				return t.TempDir(), append(slices.Clone(clean), "GIT_DIR="+filepath.Join(repo, ".git")), marker
			},
			wantRan: true,
		},
		{
			name: "relative GIT_DIR asks git",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				return repo, append(slices.Clone(clean), "GIT_DIR=.git"), ""
			},
			wantGit: true,
		},
		{
			name: "configured hooksPath asks git and runs",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				marker := markerIn(t, repo)
				custom := customHooks(t, marker, false)
				gitT(t, repo, "config", "core.hooksPath", custom)
				return repo, clean, marker
			},
			wantRan: true,
			wantGit: true,
		},
		{
			name: "repo include asks git and runs",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				marker := markerIn(t, repo)
				custom := customHooks(t, marker, false)
				if err := os.WriteFile(filepath.Join(repo, ".git", "extra"), []byte("[core]\n\thooksPath = "+custom+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				gitT(t, repo, "config", "include.path", "extra")
				return repo, clean, marker
			},
			wantRan: true,
			wantGit: true,
		},
		{
			name: "home gitconfig asks git and runs",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				home := t.TempDir()
				marker := markerIn(t, repo)
				custom := customHooks(t, marker, false)
				if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[core]\n\thooksPath = "+custom+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return repo, append(slices.Clone(clean), "HOME="+home), marker
			},
			wantRan: true,
			wantGit: true,
		},
		{
			name: "xdg config asks git and runs",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				xdg := t.TempDir()
				if err := os.MkdirAll(filepath.Join(xdg, "git"), 0o700); err != nil {
					t.Fatal(err)
				}
				marker := markerIn(t, repo)
				custom := customHooks(t, marker, false)
				if err := os.WriteFile(filepath.Join(xdg, "git", "config"), []byte("[core]\n\thooksPath = "+custom+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return repo, append(slices.Clone(clean), "XDG_CONFIG_HOME="+xdg), marker
			},
			wantRan: true,
			wantGit: true,
		},
		{
			name: "global override asks git and runs",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				marker := markerIn(t, repo)
				custom := customHooks(t, marker, false)
				global := filepath.Join(t.TempDir(), "gitconfig")
				if err := os.WriteFile(global, []byte("[core]\n\thooksPath = "+custom+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return repo, append(slices.Clone(clean), "GIT_CONFIG_GLOBAL="+global), marker
			},
			wantRan: true,
			wantGit: true,
		},
		{
			name: "system override asks git and runs",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				marker := markerIn(t, repo)
				custom := customHooks(t, marker, false)
				system := filepath.Join(t.TempDir(), "gitconfig")
				if err := os.WriteFile(system, []byte("[core]\n\thooksPath = "+custom+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return repo, append(slices.Clone(clean), "GIT_CONFIG_SYSTEM="+system), marker
			},
			wantRan: true,
			wantGit: true,
		},
		{
			name: "system unclean asks git",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				return sessionRepo(t), []string{"YARDR_GIT_SYSTEM_CLEAN=0"}, ""
			},
			wantGit: true,
		},
		{
			name: "system unknown asks git",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				return sessionRepo(t), nil, ""
			},
			wantGit: true,
		},
		{
			name: "garbage git file asks git",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				os.RemoveAll(filepath.Join(repo, ".git"))
				if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("garbage\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return repo, clean, ""
			},
			wantGit: true,
		},
		{
			name: "commented hooksPath asks git",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				if _, err := f.WriteString("; core.hooksPath = /nowhere\n"); err != nil {
					t.Fatal(err)
				}
				return repo, clean, ""
			},
			wantGit: true,
		},
		{
			name: "include in a URL asks git",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				gitT(t, repo, "config", "remote.origin.url", "https://example.com/xincludey.git")
				return repo, clean, ""
			},
			wantGit: true,
		},
		{
			name: "git file to an absolute dir",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				work, gitdir := t.TempDir(), t.TempDir()
				hooks := filepath.Join(gitdir, "hooks")
				if err := os.MkdirAll(hooks, 0o700); err != nil {
					t.Fatal(err)
				}
				marker := markerIn(t, work)
				writeRepoHook(t, hooks, "pre-commit", shimMarkerHook(marker, false))
				if err := os.WriteFile(filepath.Join(work, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return work, clean, marker
			},
			wantRan: true,
		},
		{
			name: "relative commondir",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				wt := filepath.Join(t.TempDir(), "wt")
				gitT(t, repo, "worktree", "add", "-q", "-b", "yard/wt", wt)
				raw, err := os.ReadFile(filepath.Join(wt, ".git"))
				if err != nil {
					t.Fatal(err)
				}
				gitdir := strings.TrimSpace(strings.TrimPrefix(string(raw), "gitdir: "))
				rel, err := filepath.Rel(gitdir, filepath.Join(repo, ".git"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte(rel+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				hooks := filepath.Join(repo, ".git", "hooks")
				if err := os.MkdirAll(hooks, 0o700); err != nil {
					t.Fatal(err)
				}
				marker := markerIn(t, repo)
				writeRepoHook(t, hooks, "pre-commit", shimMarkerHook(marker, false))
				return wt, clean, marker
			},
			wantRan: true,
		},
		{
			name: "empty commondir asks git",
			hook: "pre-commit",
			setup: func(t *testing.T) (string, []string, string) {
				repo := sessionRepo(t)
				wt := filepath.Join(t.TempDir(), "wt")
				gitT(t, repo, "worktree", "add", "-q", "-b", "yard/wt", wt)
				raw, err := os.ReadFile(filepath.Join(wt, ".git"))
				if err != nil {
					t.Fatal(err)
				}
				gitdir := strings.TrimSpace(strings.TrimPrefix(string(raw), "gitdir: "))
				if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte(""), 0o644); err != nil {
					t.Fatal(err)
				}
				return wt, clean, ""
			},
			wantGit: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bin, log := logGitBin(t)
			cwd, extra, marker := tc.setup(t)
			_, code := runHook(t, filepath.Join(hooks, tc.hook), cwd, directHookEnv(t, bin, extra...))
			if code != tc.want {
				t.Errorf("exit = %d, want %d", code, tc.want)
			}
			ran := false
			if marker != "" {
				_, err := os.Stat(marker)
				ran = err == nil
			}
			if ran != tc.wantRan {
				t.Errorf("repo hook ran = %v, want %v", ran, tc.wantRan)
			}
			calls := nestedCalls(t, log)
			if got := len(calls) > 0; got != tc.wantGit {
				t.Errorf("nested git calls = %v, want git %v", calls, tc.wantGit)
			}
		})
	}
}

// The commit-msg hook keeps to as few git calls as the message needs: the
// repo-hooks lookup reads the filesystem, a message naming no trailer of
// the four kinds skips the counting for one apply, and one parse counts
// every kind before a single call writes the yard's two. Each case runs
// the hook directly on its message and counts the nested calls.
func TestCommitMsgHookNestedGitCalls(t *testing.T) {
	t.Parallel()
	hook := filepath.Join(yardHooks(t), "commit-msg") // installed once: the cases only run it
	session := []string{"YARDR_BEAD=web-3", "YARDR_GROUP=builders", "YARDR_KIND=claude", "YARDR_MODEL=opus", "YARDR_GIT_SYSTEM_CLEAN=1"}
	dups := func() string {
		var msg strings.Builder
		msg.WriteString("web-3: normalize all trailers\n\n")
		for i := 0; i < 12; i++ {
			msg.WriteString("Co-Authored-By: harness <h@example.com>\n")
			msg.WriteString("Signed-off-by: operator <o@example.com>\n")
		}
		msg.WriteString("Yardr-Bead: old-one\nYardr-Bead: old-two\n")
		msg.WriteString("Yardr-Agent: old-one\nYardr-Agent: old-two\n")
		return msg.String()
	}
	for _, tc := range []struct {
		name        string
		msg         string
		repoHook    string // "" for none, else the repo commit-msg body ("refuse" refuses)
		noBead      bool
		contains    []string
		absent      []string
		unchanged   bool
		unreadable  bool
		wantRepoRan bool
		want        int
		wantFail    bool
		wantResolve int
		wantParse   int
		wantTrailer int
	}{
		{
			name:        "plain",
			msg:         "web-3: plain\n\nA body.\n",
			contains:    []string{"Yardr-Bead: web-3", "Yardr-Agent: builders (claude opus)"},
			wantTrailer: 1,
		},
		{
			name: "signed",
			msg: "web-3: signed\n\nA body.\n\n" +
				"Co-Authored-By: Claude <c@example.com>\n" +
				"Signed-off-by: Operator <o@example.com>\n" +
				"Reviewed-by: Zed <z@example.com>\n",
			contains:  []string{"Yardr-Bead: web-3", "Yardr-Agent: builders (claude opus)", "Reviewed-by: Zed <z@example.com>"},
			absent:    []string{"Co-Authored-By", "Signed-off-by"},
			wantParse: 1, wantTrailer: 2,
		},
		{
			name:     "duplicates",
			msg:      dups(),
			contains: []string{"Yardr-Bead: web-3", "Yardr-Agent: builders (claude opus)"},
			absent:   []string{"Co-Authored-By", "Signed-off-by", "old-one", "old-two"},
			// One parse, eleven and eleven and one and one excess drops,
			// one combined apply.
			wantParse: 1, wantTrailer: 26,
		},
		{
			// A body line naming a trailer takes the counting path, which
			// finds no trailer to drop and adds without trimming: the
			// body and the other key's empty trailer stand.
			name:      "body mention",
			msg:       "web-3: mention\n\nSigned-off-by: Fred wrote this line as prose\n\nOther:\n",
			contains:  []string{"Signed-off-by: Fred wrote this line as prose", "Other:", "Yardr-Bead: web-3"},
			wantParse: 1, wantTrailer: 2,
		},
		{
			// No trailer of the four kinds skips the counting: one apply,
			// and the other key's empty trailer is none of the hook's
			// business.
			name:        "other empty only",
			msg:         "web-3: empty\n\nOther:\n",
			contains:    []string{"Other:", "Yardr-Bead: web-3"},
			wantTrailer: 1,
		},
		{
			name:        "repo hook appends",
			msg:         "web-3: chained\n",
			repoHook:    "#!/bin/sh\necho ran > \"$REPO_MARKER\"\nprintf 'Reviewed-by: Repo <r@example.com>\\n' >> \"$1\"\n",
			contains:    []string{"Reviewed-by: Repo <r@example.com>", "Yardr-Bead: web-3"},
			wantRepoRan: true,
			wantTrailer: 1,
		},
		{
			name:        "repo hook refuses",
			msg:         "web-3: refused\n",
			repoHook:    "refuse",
			wantRepoRan: true,
			want:        1,
		},
		{
			// Outside a session the message stands as written, while the
			// repo's hook still runs, and no trailer call runs at all.
			name:        "outside session",
			msg:         "a change\n\nCo-Authored-By: Someone <s@example.com>\n",
			repoHook:    "#!/bin/sh\necho ran > \"$REPO_MARKER\"\n",
			noBead:      true,
			unchanged:   true,
			wantRepoRan: true,
		},
		{
			name:      "already trailered",
			msg:       "web-3: amend\n\nYardr-Bead: web-3\nYardr-Agent: builders (claude opus)\n",
			contains:  []string{"Yardr-Bead: web-3", "Yardr-Agent: builders (claude opus)"},
			wantParse: 1, wantTrailer: 2,
		},
		{
			// An unreadable message fails the commit instead of
			// committing without the yard's trailers.
			name:       "unreadable",
			msg:        "web-3: unreadable\n",
			unreadable: true,
			wantFail:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := sessionRepo(t)
			var marker string
			if tc.repoHook != "" {
				hooks := filepath.Join(repo, ".git", "hooks")
				if err := os.MkdirAll(hooks, 0o700); err != nil {
					t.Fatal(err)
				}
				marker = filepath.Join(repo, "repo.ran")
				body := tc.repoHook
				if body == "refuse" {
					body = "#!/bin/sh\necho ran > \"$REPO_MARKER\"\nexit 1\n"
				}
				writeRepoHook(t, hooks, "commit-msg", strings.ReplaceAll(body, "$REPO_MARKER", marker))
			}
			msgfile := filepath.Join(t.TempDir(), "msg")
			if err := os.WriteFile(msgfile, []byte(tc.msg), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.unreadable {
				if err := os.Chmod(msgfile, 0o000); err != nil {
					t.Fatal(err)
				}
			}
			bin, log := logGitBin(t)
			extra := slices.Clone(session)
			if tc.noBead {
				extra = []string{"YARDR_GIT_SYSTEM_CLEAN=1"}
			}
			_, code := runHook(t, hook, repo, directHookEnv(t, bin, extra...), msgfile)
			if tc.wantFail {
				if code == 0 {
					t.Fatalf("exit = 0, want failure")
				}
				return
			}
			if code != tc.want {
				t.Fatalf("exit = %d, want %d", code, tc.want)
			}
			if marker != "" {
				_, err := os.Stat(marker)
				if ran := err == nil; ran != tc.wantRepoRan {
					t.Errorf("repo hook ran = %v, want %v", ran, tc.wantRepoRan)
				}
			}
			raw, err := os.ReadFile(msgfile)
			if err != nil {
				t.Fatal(err)
			}
			body := string(raw)
			if tc.unchanged && body != tc.msg {
				t.Errorf("message changed:\n%s", body)
			}
			for _, want := range tc.contains {
				if n := strings.Count(body, want); n != 1 {
					t.Errorf("%q %d times, want once:\n%s", want, n, body)
				}
			}
			for _, gone := range tc.absent {
				if strings.Contains(strings.ToLower(body), strings.ToLower(gone)) {
					t.Errorf("%q survived:\n%s", gone, body)
				}
			}
			var resolve, parse, trailer int
			for _, call := range nestedCalls(t, log) {
				switch {
				case strings.Contains(call, "--git-path hooks"):
					resolve++
				case strings.Contains(call, "interpret-trailers --parse"):
					parse++
				}
				if strings.Contains(call, "interpret-trailers") {
					trailer++
				}
			}
			if resolve != tc.wantResolve || parse != tc.wantParse || trailer != tc.wantTrailer {
				t.Errorf("nested git = %d resolve, %d parse, %d trailer, want %d, %d, %d",
					resolve, parse, trailer, tc.wantResolve, tc.wantParse, tc.wantTrailer)
			}
		})
	}
}

// A session's git operations resolve the repo's hooks without asking git:
// commit, push, worktree and merge run every shim they fire — sender and
// bare receiver, reference transactions included — with no nested
// --git-path call, while commits still carry the yard's trailers.
func TestSessionOpsResolveHooksWithoutGit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		op   func(t *testing.T, repo string, env []string, run func(args ...string) string)
	}{
		{
			name: "commit",
			op: func(t *testing.T, repo string, env []string, run func(args ...string) string) {
				run("commit", "-q", "--allow-empty", "-m", "web-3: plain")
				run("commit", "-q", "--allow-empty", "-m", "web-3: signed\n\nSigned-off-by: Christopher Scott <c@example.com>\n")
				body := run("log", "-1", "--format=%B")
				for _, want := range []string{"Yardr-Bead: web-3", "Yardr-Agent: builders (claude opus)"} {
					if n := strings.Count(body, want); n != 1 {
						t.Errorf("%q %d times, want once:\n%s", want, n, body)
					}
				}
				if strings.Contains(strings.ToLower(body), "signed-off-by") {
					t.Errorf("signed-off-by survived:\n%s", body)
				}
			},
		},
		{
			name: "push",
			op: func(t *testing.T, repo string, env []string, run func(args ...string) string) {
				bare := t.TempDir()
				gitT(t, bare, "init", "-q", "--bare", "-b", "main")
				run("commit", "-q", "--allow-empty", "-m", "web-3: to push")
				run("push", "-q", bare, "main")
			},
		},
		{
			name: "worktree",
			op: func(t *testing.T, repo string, env []string, run func(args ...string) string) {
				wt := filepath.Join(t.TempDir(), "wt")
				run("worktree", "add", "-q", "-b", "yard/wt", wt)
				out, err := sessionGit(t, wt, env, "commit", "-q", "--allow-empty", "-m", "web-3: from the worktree")
				if err != nil {
					t.Fatalf("git commit: %v\n%s", err, out)
				}
				body, err := sessionGit(t, wt, env, "log", "-1", "--format=%B")
				if err != nil {
					t.Fatal(err)
				}
				if strings.Count(body, "Yardr-Bead: web-3") != 1 {
					t.Errorf("yard trailers missing:\n%s", body)
				}
			},
		},
		{
			name: "merge",
			op: func(t *testing.T, repo string, env []string, run func(args ...string) string) {
				run("checkout", "-q", "-b", "feature")
				run("commit", "-q", "--allow-empty", "-m", "web-3: feature")
				run("checkout", "-q", "main")
				run("merge", "-q", "--ff-only", "feature")
			},
		},
		{
			name: "rebase",
			op: func(t *testing.T, repo string, env []string, run func(args ...string) string) {
				run("commit", "-q", "--allow-empty", "-m", "web-3: base")
				run("checkout", "-q", "-b", "feature")
				run("commit", "-q", "--allow-empty", "-m", "web-3: feature\n\nSigned-off-by: Christopher Scott <c@example.com>\n")
				run("rebase", "-q", "main")
				body := run("log", "-1", "--format=%B")
				for _, want := range []string{"Yardr-Bead: web-3", "Yardr-Agent: builders (claude opus)"} {
					if n := strings.Count(body, want); n != 1 {
						t.Errorf("%q %d times, want once:\n%s", want, n, body)
					}
				}
				if strings.Contains(strings.ToLower(body), "signed-off-by") {
					t.Errorf("signed-off-by survived the rebase:\n%s", body)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			overlay, log := execPathOverlay(t)
			home := t.TempDir()
			job := sessionJob("web-3", "builders", "claude", "opus")
			env := append(sessionTestEnv(t, home, job, "web-3-new", passthroughHooks...), "GIT_EXEC_PATH="+overlay)
			repo := sessionRepo(t)
			run := func(args ...string) string {
				t.Helper()
				return sessionGitT(t, repo, env, args...)
			}
			tc.op(t, repo, env, run)
			for _, call := range nestedCalls(t, log) {
				if strings.Contains(call, "--git-path hooks") {
					t.Errorf("nested hook resolution: %q", call)
				}
			}
		})
	}
}

// A global hooksPath still guards a session's commits: a refusing
// pre-commit blocks, whether the path is set directly or arrives through
// an include.
func TestSessionCommitHonoursGlobalHooks(t *testing.T) {
	t.Parallel()
	global := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "gitconfig")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, tc := range []struct {
		name   string
		config func(t *testing.T, custom string) string
	}{
		{
			name: "hooksPath",
			config: func(t *testing.T, custom string) string {
				return global(t, "[core]\n\thooksPath = "+custom+"\n")
			},
		},
		{
			name: "include",
			config: func(t *testing.T, custom string) string {
				dir := t.TempDir()
				other := filepath.Join(dir, "other")
				if err := os.WriteFile(other, []byte("[core]\n\thooksPath = "+custom+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "gitconfig")
				if err := os.WriteFile(path, []byte("[include]\n\tpath = other\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := sessionRepo(t)
			ran := filepath.Join(repo, ".git", "global-pre-commit.ran")
			custom := t.TempDir()
			writeRepoHook(t, custom, "pre-commit", "#!/bin/sh\necho ran > "+ran+"\nexit 1\n")
			job := sessionJob("web-3", "builders", "codex", "luna")
			env := append(sessionTestEnv(t, t.TempDir(), job, "web-3-new", "pre-commit"),
				"GIT_CONFIG_GLOBAL="+tc.config(t, custom))
			if out, err := sessionGit(t, repo, env, "commit", "--allow-empty", "-m", "web-3: refused"); err == nil {
				t.Fatalf("a refusing global pre-commit committed:\n%s", out)
			}
			if _, err := os.Stat(ran); err != nil {
				t.Fatal("the global pre-commit hook did not run")
			}
		})
	}
}

// A repo include that points core.hooksPath elsewhere still guards a
// session's commits: a refusing pre-commit blocks.
func TestSessionCommitHonoursRepoInclude(t *testing.T) {
	t.Parallel()
	repo := sessionRepo(t)
	ran := filepath.Join(repo, ".git", "included-pre-commit.ran")
	custom := t.TempDir()
	writeRepoHook(t, custom, "pre-commit", "#!/bin/sh\necho ran > "+ran+"\nexit 1\n")
	if err := os.WriteFile(filepath.Join(repo, ".git", "extra"), []byte("[core]\n\thooksPath = "+custom+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "config", "include.path", "extra")
	job := sessionJob("web-3", "builders", "codex", "luna")
	env := sessionTestEnv(t, t.TempDir(), job, "web-3-new", "pre-commit")
	if out, err := sessionGit(t, repo, env, "commit", "--allow-empty", "-m", "web-3: refused"); err == nil {
		t.Fatalf("a refusing included pre-commit committed:\n%s", out)
	}
	if _, err := os.Stat(ran); err != nil {
		t.Fatal("the included pre-commit hook did not run")
	}
}

// The yard vouches for the install's system gitconfig only when it names
// no core.hooksPath and no include: a git that never runs, or one that
// hangs, vouches for nothing, while one that runs and refuses — no
// system file, or one it cannot read — is judged by what it printed.
// The check runs past every override, so it reads the install's own file.
func TestSystemGitconfigClean(t *testing.T) {
	t.Parallel()
	fakeGit := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "git")
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		// Warm the script once, to completion and unbounded: the first
		// start of a never-run executable pays the machine's first-launch
		// assessment, seconds under builder load, and the check below
		// answers false past its deadline. A stopped start pays the
		// whole assessment again next time, so this run must not go
		// through the check's timeout; the result is ignored.
		_ = exec.Command(path, "config", "--system", "--list").Run()
		return path
	}
	for _, tc := range []struct {
		name string
		git  func(t *testing.T) string
		want bool
	}{
		{"empty", func(t *testing.T) string { return fakeGit(t, "#!/bin/sh\nexit 0\n") }, true},
		{"user only", func(t *testing.T) string { return fakeGit(t, "#!/bin/sh\nprintf 'user.name=x\\n'\n") }, true},
		{"no system file", func(t *testing.T) string { return fakeGit(t, "#!/bin/sh\necho fatal >&2\nexit 128\n") }, true},
		{"hooksPath", func(t *testing.T) string { return fakeGit(t, "#!/bin/sh\nprintf 'core.hookspath=/x\\n'\n") }, false},
		{"hooksPath cased", func(t *testing.T) string { return fakeGit(t, "#!/bin/sh\nprintf 'CORE.HOOKSPATH=/x\\n'\n") }, false},
		{"include", func(t *testing.T) string { return fakeGit(t, "#!/bin/sh\nprintf 'include.path=/x\\n'\n") }, false},
		{"includeIf", func(t *testing.T) string { return fakeGit(t, "#!/bin/sh\nprintf 'includeif.gitdir:/x.path=/y\\n'\n") }, false},
		{"missing git", func(t *testing.T) string { return filepath.Join(t.TempDir(), "no-such-git") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := systemGitconfigClean(tc.git(t), nil); got != tc.want {
				t.Errorf("clean = %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("overrides stripped", func(t *testing.T) {
		t.Parallel()
		dump := filepath.Join(t.TempDir(), "env")
		git := fakeGit(t, "#!/bin/sh\nenv > "+shellQuote(dump)+"\n")
		environ := []string{"PATH=" + os.Getenv("PATH"),
			"GIT_CONFIG_SYSTEM=/x", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=a", "GIT_CONFIG_VALUE_0=b",
			"GIT_CONFIG_PARAMETERS='core.hooksPath=/y'"}
		if !systemGitconfigClean(git, environ) {
			t.Fatal("clean = false, want true")
		}
		raw, err := os.ReadFile(dump)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "GIT_CONFIG") {
				t.Errorf("check kept %q", line)
			}
		}
	})
}

// Two homes' hooks share their inodes through the cache, so a fresh home
// costs no first execution per file; each copy still holds the hook's
// bytes and executes. Where the cache cannot link (another volume,
// none), each home holds its own bytes instead.
func TestEnsureCommitHookSharesInodes(t *testing.T) {
	t.Parallel()
	dir1, err := EnsureCommitHook(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir2, err := EnsureCommitHook(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cache, linkable := "", cacheLinkable(t)
	if linkable {
		var err error
		cache, err = hookCacheDir()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range hookFiles() {
		for _, dir := range []string{dir1, dir2} {
			body, err := os.ReadFile(filepath.Join(dir, f.name))
			if err != nil || string(body) != f.body {
				t.Errorf("%s in %s = %q, %v", f.name, dir, body, err)
			}
			st, err := os.Stat(filepath.Join(dir, f.name))
			if err != nil || st.Mode().Perm()&0o111 == 0 {
				t.Errorf("%s in %s not executable: %v", f.name, dir, st)
			}
		}
		if !linkable {
			continue
		}
		if !sameFile(t, filepath.Join(dir1, f.name), filepath.Join(dir2, f.name)) {
			t.Errorf("%s: homes hold different inodes", f.name)
		}
		if !sameFile(t, filepath.Join(dir1, f.name), filepath.Join(cache, f.name)) {
			t.Errorf("%s: home is not the cached inode", f.name)
		}
	}
}

// cacheLinkable probes whether this machine links cache files into homes:
// a scratch link from the cache dir into a throwaway dir.
func cacheLinkable(t *testing.T) bool {
	t.Helper()
	dir, err := hookCacheDir()
	if err != nil {
		return false
	}
	src := filepath.Join(dir, "commit-msg")
	if _, err := os.Stat(src); err != nil {
		return false
	}
	dst := filepath.Join(t.TempDir(), "probe")
	if !linkFile(src, dst) {
		return false
	}
	os.Remove(dst)
	return true
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	return os.SameFile(fa, fb)
}

// linkFile makes final a hard link to cache atomically: over a missing
// name and over an existing one, and reports false without touching
// final when the two cannot link.
func TestLinkFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	if err := os.WriteFile(cache, []byte("hook\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(dir, "final")
	if !linkFile(cache, final) {
		t.Fatal("link over a missing name failed")
	}
	if !sameFile(t, cache, final) {
		t.Fatal("linked final is not the cached inode")
	}
	other := filepath.Join(dir, "other")
	if err := os.WriteFile(other, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !linkFile(cache, other) {
		t.Fatal("link over an existing name failed")
	}
	if raw, _ := os.ReadFile(other); string(raw) != "hook\n" {
		t.Fatalf("linked over existing = %q", raw)
	}
	if !sameFile(t, cache, other) {
		t.Fatal("linked over existing is not the cached inode")
	}
	missing := filepath.Join(dir, "absent")
	if linkFile(missing, filepath.Join(dir, "untouched")) {
		t.Fatal("link from a missing cache reported success")
	}
	if _, err := os.Stat(filepath.Join(dir, "untouched")); !os.IsNotExist(err) {
		t.Fatalf("failed link touched final: %v", err)
	}
}

// The session env carries the yard's word about the system gitconfig, 1
// when clean and 0 otherwise, agreeing with the check itself.
func TestCommitHookEnvVouchesSystem(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	env, err := commitHookEnv(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=" + filepath.Join(home, "githooks"), "GIT_CONFIG_PARAMETERS="} {
		if !slices.Contains(env, want) {
			t.Errorf("hook env lacks %q: %v", want, env)
		}
	}
	want := "YARDR_GIT_SYSTEM_CLEAN=0"
	if systemGitconfigClean("git", os.Environ()) {
		want = "YARDR_GIT_SYSTEM_CLEAN=1"
	}
	n := 0
	for _, kv := range env {
		if kv == want {
			n++
		}
	}
	if n != 1 {
		t.Errorf("hook env has %d of %q, want 1: %v", n, want, env)
	}
}
