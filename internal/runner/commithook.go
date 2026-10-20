package runner

import (
	"cmp"
	"fmt"
	"log/slog"
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
// message fails the commit before the yard normalizes it.
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

# Drop every harness trailer. One interpret-trailers pass replaces one
# same-key trailer, so loop until git's own parse shows none left; bounded,
# since harnesses add a few at most.
i=0
while [ "$i" -lt 10 ] && git interpret-trailers --parse < "$msg" 2>/dev/null | grep -qiE '^(co-authored-by|signed-off-by)[[:space:]:]'; do
	git interpret-trailers --trim-empty --if-exists replace --trailer 'Co-Authored-By:' --trailer 'Signed-off-by:' --in-place "$msg" || exit $?
	i=$((i + 1))
done

# The yard's own trailers, replacing whatever is there (a hand-written
# value, another session's on amend), so they never duplicate.
exec git interpret-trailers --if-exists replace --trailer "Yardr-Bead: $bead" --trailer "Yardr-Agent: $agent" --in-place "$msg"
`

// EnsureCommitHook writes the yard's commit-msg hook under home
// (config.GitHooks) when it is missing or outdated, and returns the hooks
// directory the session's core.hooksPath points at. The write is atomic (a
// temp file plus rename), so sessions starting together never leave a
// half-written hook for a commit to run.
func EnsureCommitHook(home string) (string, error) {
	dir := config.At(home).GitHooks()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("git hooks dir %s: %w", dir, err)
	}
	path := filepath.Join(dir, "commit-msg")
	if cur, err := os.ReadFile(path); err != nil || string(cur) != commitHook {
		tmp, err := os.CreateTemp(dir, ".commit-msg-*")
		if err != nil {
			return "", fmt.Errorf("git commit-msg hook: %w", err)
		}
		name := tmp.Name()
		_, werr := tmp.WriteString(commitHook)
		cerr := tmp.Chmod(0o755) // git runs a hook only when it executes
		xerr := tmp.Close()
		rerr := error(nil)
		if werr == nil && cerr == nil && xerr == nil {
			rerr = os.Rename(name, path)
		}
		if werr != nil || cerr != nil || xerr != nil || rerr != nil {
			os.Remove(name)
			return "", fmt.Errorf("git commit-msg hook: %w",
				cmp.Or(werr, cerr, xerr, rerr))
		}
		return dir, nil
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm()&0o111 == 0 {
		// Ours in content but no longer executable (a restore, a copy).
		if err := os.Chmod(path, 0o755); err != nil {
			return "", fmt.Errorf("git commit-msg hook %s: %w", path, err)
		}
	}
	return dir, nil
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
// shared .git/config is never written. A home that holds no hooks dir (a
// test's, or one the yard cannot write) leaves git alone: the session still
// starts, and its commits carry no yard trailers.
func commitHookEnv(home string, log *slog.Logger) []string {
	if home == "" {
		return nil
	}
	dir, err := EnsureCommitHook(home)
	if err != nil {
		if log != nil {
			log.Debug("yard commit-msg hook not installed", "home", home, "err", err)
		}
		return nil
	}
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=" + dir,
	}
}
