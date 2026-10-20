package runner

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
)

// commitHook is the yard's commit-msg hook: every commit an agent makes in a
// session ends the same way, whatever the harness, without the agent having
// to obey. Harnesses sign their commits each in their own way (Claude Code's
// Co-Authored-By, Codex's Signed-off-by); the hook drops every Co-Authored-By
// and Signed-off-by trailer, then appends the yard's own two with git
// interpret-trailers, replacing so that amending never duplicates them:
//
//	Yardr-Bead: <bead id>
//	Yardr-Agent: <group> (<kind> <model>)
//
// The values come from the session's environment (YARDR_BEAD, YARDR_GROUP,
// YARDR_KIND, YARDR_MODEL). Outside a session, with no YARDR_BEAD, the
// message stands as written. The repo's own commit-msg hook, when one
// exists, runs first, so a depot's hooks still run; one that refuses the
// message fails the commit before the yard normalizes it. The repo's other
// hooks still run through passthroughHook, since core.hooksPath would
// otherwise hide every one of them behind this directory.
const commitHook = `#!/bin/sh
# The yard's commit-msg hook: every commit an agent makes in a session ends
# the same way. Harness trailers (Co-Authored-By, Signed-off-by) go, whatever
# the harness added, and the yard's own two trailers go on, idempotent on
# amend. Outside a session (no YARDR_BEAD) the message stands as written.
# yardr points the session's git here with core.hooksPath through the
# environment, so the depot's own .git/config is never written.
set -u
msg=${1:?usage: commit-msg <msgfile>}

# The repo's own commit-msg hook first, so a depot's hooks still run: what
# git would run without this one (the configured hooksPath, else the git
# dir's hooks), read past this session's core.hooksPath override. A hook
# that refuses the message fails the commit before the yard normalizes it.
hooks_path=$(env -u GIT_CONFIG_COUNT -u GIT_CONFIG_KEY_0 -u GIT_CONFIG_VALUE_0 git config core.hooksPath 2>/dev/null || true)
if [ -n "$hooks_path" ]; then
	case "$hooks_path" in
	/*) repo_hooks=$hooks_path ;;
	*) top=$(git rev-parse --show-toplevel 2>/dev/null || true)
	   repo_hooks=${top:+$top/}$hooks_path ;;
	esac
else
	# --git-path honours core.hooksPath, so read it past the override too:
	# without that it names this hook's own directory.
	repo_hooks=$(env -u GIT_CONFIG_COUNT -u GIT_CONFIG_KEY_0 -u GIT_CONFIG_VALUE_0 git rev-parse --git-path hooks 2>/dev/null || true)
fi
if [ -n "${repo_hooks:-}" ] && [ -x "$repo_hooks/commit-msg" ] && [ ! "$repo_hooks/commit-msg" -ef "$0" ]; then
	"$repo_hooks/commit-msg" "$@" || exit $?
fi

# Outside a session there is no bead to name.
bead=${YARDR_BEAD:-}
[ -n "$bead" ] || exit 0

# Trailer values are single lines; a newline in one would forge trailers.
bead=$(printf '%s' "$bead" | tr -d '\n\r')
group=$(printf '%s' "${YARDR_GROUP:-}" | tr -d '\n\r')
kind=$(printf '%s' "${YARDR_KIND:-}" | tr -d '\n\r')
model=$(printf '%s' "${YARDR_MODEL:-}" | tr -d '\n\r')
[ -n "$group" ] || group=$bead
inner=$kind
[ -n "$model" ] && inner="${inner:+$inner }$model"
agent=$group
[ -n "$inner" ] && agent="$group ($inner)"

# One interpret-trailers pass replaces one same-key trailer, so remove each
# kind until git's own parse shows none left. Do the same for old Yardr
# trailers: replace would otherwise leave an earlier duplicate behind.
drop_trailers() {
	token=$1
	while git interpret-trailers --parse < "$msg" 2>/dev/null | grep -qiE "^${token}[[:space:]:]"; do
		git interpret-trailers --trim-empty --if-exists replace --trailer "$token:" --in-place "$msg" || exit $?
	done
}
drop_trailers 'Co-Authored-By'
drop_trailers 'Signed-off-by'
drop_trailers 'Yardr-Bead'
drop_trailers 'Yardr-Agent'

# The yard's own trailers, replacing whatever is there (a hand-written
# value, another session's on amend), so they never duplicate.
exec git interpret-trailers --if-exists replace --trailer "Yardr-Bead: $bead" --trailer "Yardr-Agent: $agent" --in-place "$msg"
`

// passthroughHooks are git's other hooks, besides commit-msg: the hooks a
// session's git would have run from the repo's own hooks directory before
// the yard pointed core.hooksPath at its own. core.hooksPath names one
// directory for every hook, so a yard directory holding only commit-msg
// would silently skip a repo's pre-commit, pre-push and the rest — checks
// that can refuse a commit or a push. Each name below gets the
// passthroughHook shim instead, which runs the repo's hook of the same
// name as git would have. fsmonitor-watchman is the one name left out:
// its stdout is a protocol (the paths that changed), so a shim that finds
// no repo hook must not answer in its place with nothing changed.
var passthroughHooks = []string{
	"applypatch-msg", "pre-applypatch", "post-applypatch",
	"pre-commit", "pre-merge-commit", "prepare-commit-msg",
	"post-commit", "pre-rebase", "post-checkout", "post-merge",
	"pre-push", "pre-receive", "update", "proc-receive",
	"post-receive", "post-update", "reference-transaction",
	"push-to-checkout", "pre-auto-gc", "post-rewrite",
	"sendemail-validate", "post-index-change",
}

// passthroughHook is the shim the yard installs for every hook in
// passthroughHooks: it runs the repo's own hook of the same name (what git
// would run without the session's core.hooksPath override, read past that
// override as the commit-msg hook reads it), with the same arguments and
// stdin, and git sees that hook's exit status. With no repo hook it exits
// 0, as a missing hook does. It never alters a commit message; only
// commit-msg does that.
const passthroughHook = `#!/bin/sh
# The yard's pass-through for a repo hook other than commit-msg: pointing
# the session's git at the yard's hooks for commit-msg must not skip the
# repo's other hooks, so this runs the repo's hook of the same name as git
# would have run it, and git sees its exit status.
set -u
hook=${0##*/}

# What git would run without the yard's hook, read past this session's
# core.hooksPath override: --git-path honours a configured hooksPath (a
# relative one it returns relative to here, where git runs hooks), and
# otherwise names the git dir's hooks, in a linked worktree the common
# dir's. One git call, since hooks like reference-transaction fire often.
repo_hooks=$(env -u GIT_CONFIG_COUNT -u GIT_CONFIG_KEY_0 -u GIT_CONFIG_VALUE_0 git rev-parse --git-path hooks 2>/dev/null || true)
if [ -n "${repo_hooks:-}" ] && [ -x "$repo_hooks/$hook" ] && [ ! "$repo_hooks/$hook" -ef "$0" ]; then
	exec "$repo_hooks/$hook" "$@"
fi
exit 0
`

type hookFile struct{ name, body string }

// hookFiles is every file EnsureCommitHook keeps in the yard's hooks
// directory: the commit-msg hook, then a pass-through for each other hook.
func hookFiles() []hookFile {
	files := []hookFile{{"commit-msg", commitHook}}
	for _, name := range passthroughHooks {
		files = append(files, hookFile{name, passthroughHook})
	}
	return files
}

// EnsureCommitHook writes the yard's hooks under home (config.GitHooks)
// when they are missing or outdated, and returns the hooks directory the
// session's core.hooksPath points at. Each write is atomic (a temp file
// plus rename), so sessions starting together never leave a half-written
// hook for a commit to run.
func EnsureCommitHook(home string) (string, error) {
	dir := config.At(home).GitHooks()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("git hooks dir %s: %w", dir, err)
	}
	for _, f := range hookFiles() {
		if err := ensureHookFile(dir, f); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// ensureHookFile writes one hook file (name, body) in dir when it is
// missing or outdated, atomically, and repairs a hook that lost its
// execute bit (a restore, a copy): git runs a hook only when it executes.
func ensureHookFile(dir string, f hookFile) error {
	path := filepath.Join(dir, f.name)
	if cur, err := os.ReadFile(path); err != nil || string(cur) != f.body {
		tmp, err := os.CreateTemp(dir, "."+f.name+"-*")
		if err != nil {
			return fmt.Errorf("git %s hook: %w", f.name, err)
		}
		name := tmp.Name()
		_, werr := tmp.WriteString(f.body)
		cerr := tmp.Chmod(0o755) // git runs a hook only when it executes
		xerr := tmp.Close()
		rerr := error(nil)
		if werr == nil && cerr == nil && xerr == nil {
			rerr = os.Rename(name, path)
		}
		if werr != nil || cerr != nil || xerr != nil || rerr != nil {
			os.Remove(name)
			return fmt.Errorf("git %s hook: %w", f.name,
				cmp.Or(werr, cerr, xerr, rerr))
		}
		return nil
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm()&0o111 == 0 {
		if err := os.Chmod(path, 0o755); err != nil {
			return fmt.Errorf("git %s hook %s: %w", f.name, path, err)
		}
	}
	return nil
}

// sessionModel is the agent model the session runs: the bead's
// metadata.model, else the group's config.model, as the agent's own
// --model (see agentArgs).
func sessionModel(job dispatch.Job) string {
	model, _ := job.Bead.Metadata["model"].(string)
	return modelFor(job.Group.Config, model)
}

// modelFor resolves the agent model: the bead's override, else the group's
// config.model. AgentArgs and the session environment (YARDR_MODEL) share
// the one rule, so the model the agent runs is the model its commits name.
func modelFor(cfg map[string]any, override string) string {
	if override != "" {
		return override
	}
	return str(cfg, "model", "")
}

// commitHookEnv points the session's git at the yard's commit-msg hook
// (EnsureCommitHook): core.hooksPath through the environment, so the depot's
// shared .git/config is never written. A runner with a home cannot start a
// session without its hook: otherwise commits would silently miss the
// trailers this hook guarantees.
func commitHookEnv(home string) ([]string, error) {
	if home == "" {
		return nil, nil
	}
	dir, err := EnsureCommitHook(home)
	if err != nil {
		return nil, fmt.Errorf("install yard commit-msg hook: %w", err)
	}
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=" + dir,
	}, nil
}
