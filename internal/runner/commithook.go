package runner

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
)

// repoHooksFind is the shell both hooks share for finding the repo's own
// hooks: what git would run without the session's core.hooksPath override.
// Every hook git fires runs this first, so it reads the answer from the
// filesystem instead of asking git; a git call per firing costs a session's
// every commit, push and checkout tens of milliseconds. Anything it cannot
// prove falls back to asking git, so the fast read changes nothing git runs.
const repoHooksFind = `# find_repo_hooks sets repo_hooks to the hooks git would run without
# this session's core.hooksPath override, and always succeeds: the fast
# read first, git itself past the override when anything is unclear.
find_repo_hooks() {
	repo_hooks=
	if fast_repo_hooks; then
		return 0
	fi
	# What git would run without the yard's hook, read past this session's
	# core.hooksPath override: --git-path honours a configured hooksPath
	# (a relative one it returns relative to here, where git runs hooks),
	# and otherwise names the git dir's hooks, in a linked worktree the
	# common dir's.
	repo_hooks=$(env -u GIT_CONFIG_COUNT -u GIT_CONFIG_KEY_0 -u GIT_CONFIG_VALUE_0 -u GIT_CONFIG_PARAMETERS git rev-parse --git-path hooks 2>/dev/null || true)
}

# fast_repo_hooks is find_repo_hooks without asking git. It sets
# repo_hooks and returns 0 when certain; any doubt returns 1 and the
# caller asks git instead.
fast_repo_hooks() {
	# The install's system config is the one file this cannot name (its
	# path depends on the install), so the yard reads it when the session
	# starts and vouches for it here: no core.hooksPath and no include.
	# Without that word, ask git. Every other file git reads past the
	# session's override is read below, on every firing, so only a root
	# edit of the system file mid-session escapes this.
	[ "${YARDR_GIT_SYSTEM_CLEAN:-}" = 1 ] || return 1
	# The git dir: GIT_DIR wins when absolute (a relative one resolves
	# against the caller's directory, not this one); else .git beside
	# this directory, a dir or a worktree's git file; else this
	# directory itself when it is a bare repository's. Git runs hooks
	# from the worktree root, or from the git dir when bare, so beside
	# this directory is where discovery would have found it.
	gitdir=${GIT_DIR:-}
	case "$gitdir" in
		''|/*) ;;
		*) return 1 ;;
	esac
	if [ -z "$gitdir" ]; then
		if [ -d .git ]; then
			gitdir=.git
		elif [ -f .git ]; then
			IFS= read -r line < .git 2>/dev/null || return 1
			case "$line" in
				'gitdir: '*) gitdir=${line#gitdir: } ;;
				*) return 1 ;;
			esac
			[ -n "$gitdir" ] || return 1
		elif [ -f HEAD ] && [ -d objects ] && [ -d refs ]; then
			gitdir=.
		else
			return 1
		fi
	fi
	# A linked worktree keeps its hooks with the common dir's.
	commondir=$gitdir
	if [ -f "$gitdir/commondir" ]; then
		IFS= read -r line < "$gitdir/commondir" 2>/dev/null || return 1
		case "$line" in
			'') return 1 ;;
			/*) commondir=$line ;;
			*) commondir=$gitdir/$line ;;
		esac
	fi
	# A core.hooksPath in what git reads, or any include that could
	# carry one from a file not read here, and the hooks are wherever
	# that says: ask git. The match is a substring, so a commented or
	# oddly-cased key still falls back; over-matching only costs a call.
	home=${HOME:-}
	xdg=${XDG_CONFIG_HOME:-$home/.config}
	cfg=$(cat "$commondir/config" "$gitdir/config" "$gitdir/config.worktree" \
		"${GIT_CONFIG_GLOBAL:-}" "$home/.gitconfig" "$xdg/git/config" \
		"${GIT_CONFIG_SYSTEM:-}" 2>/dev/null) || true
	case "$cfg" in
		*[Hh][Oo][Oo][Kk][Ss][Pp][Aa][Tt][Hh]*|*[Ii][Nn][Cc][Ll][Uu][Dd][Ee]*) return 1 ;;
	esac
	repo_hooks=$commondir/hooks
}
`

// commitHookHead opens the yard's commit-msg hook: every commit an agent
// makes in a session ends the same way, whatever the harness, without the
// agent having to obey.
const commitHookHead = `#!/bin/sh
# The yard's commit-msg hook: every commit an agent makes in a session ends
# the same way. Harness trailers (Co-Authored-By, Signed-off-by) go, whatever
# the harness added, and the yard's own two trailers go on, idempotent on
# amend. Outside a session (no YARDR_BEAD) the message stands as written.
# yardr points the session's git here with core.hooksPath through the
# environment, so the depot's own .git/config is never written.
set -u
msg=${1:?usage: commit-msg <msgfile>}

`

// commitHookTail is the yard's commit-msg hook past the shared repo-hooks
// lookup: the repo's own hook first, then the trailer work, kept to as few
// git calls as the message needs.
const commitHookTail = `# The repo's own commit-msg hook first, so a depot's hooks still run: what
# git would run without this one. A hook that refuses the message fails
# the commit before the yard normalizes it.
find_repo_hooks
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

# Trailer interpretation must not inherit the depot's or operator's
# trailer.* config: separators and key aliases could otherwise leave a
# harness trailer behind or rename one of the yard's two. Run outside the
# worktree with global and system config disabled; Git invokes hooks from
# the worktree, so make the message path absolute first.
case "$msg" in
	/*) ;;
	*) msg=$(pwd)/$msg ;;
esac
trailer_git() {
	env -u GIT_DIR -u GIT_WORK_TREE -u GIT_COMMON_DIR -u GIT_PREFIX \
		-u GIT_CONFIG_COUNT -u GIT_CONFIG_PARAMETERS \
		GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
		git -C / "$@"
}

# No trailer of the four kinds anywhere in the file means the parse would
# find none either: skip the counting and go straight to adding the yard's
# two. grep, not git, decides; a body line naming a trailer still takes
# the counting path, which tells body from trailer block.
n=$(grep -ciE '^(co-authored-by|signed-off-by|yardr-bead|yardr-agent)[[:space:]:]' "$msg" 2>/dev/null || true)
[ -n "$n" ] || n=1
if [ "$n" -eq 0 ]; then
	trailer_git interpret-trailers --if-exists replace --trailer "Yardr-Bead: $bead" --trailer "Yardr-Agent: $agent" --in-place "$msg"
else
	# One parse counts every kind; a parse per kind would quadruple the
	# hook's git calls. Kinds past one drop to one first (one pass
	# replaces one), then a single call drops the rest and writes the
	# yard's two. Empty drops go only to kinds the parse found, and the
	# trim only when it found any: an absent key must gain no empty
	# trailer, and another key's empty trailer is none of this hook's
	# business unless a drop already runs.
	parsed=$(trailer_git interpret-trailers --parse < "$msg" 2>/dev/null || true)
	n_co=$(printf '%s\n' "$parsed" | grep -ciE '^Co-Authored-By[[:space:]:]')
	n_so=$(printf '%s\n' "$parsed" | grep -ciE '^Signed-off-by[[:space:]:]')
	n_bb=$(printf '%s\n' "$parsed" | grep -ciE '^Yardr-Bead[[:space:]:]')
	n_ba=$(printf '%s\n' "$parsed" | grep -ciE '^Yardr-Agent[[:space:]:]')
	drop_excess() {
		token=$1 k=$2
		while [ "$k" -gt 1 ]; do
			trailer_git interpret-trailers --trim-empty --if-exists replace --trailer "$token:" --in-place "$msg" || exit $?
			k=$((k - 1))
		done
	}
	drop_excess 'Co-Authored-By' "$n_co"
	drop_excess 'Signed-off-by' "$n_so"
	drop_excess 'Yardr-Bead' "$n_bb"
	drop_excess 'Yardr-Agent' "$n_ba"
	add_trailers() {
		set --
		[ "$n_co" -gt 0 ] && set -- "$@" --trailer "Co-Authored-By:"
		[ "$n_so" -gt 0 ] && set -- "$@" --trailer "Signed-off-by:"
		[ "$n_bb" -gt 0 ] && set -- "$@" --trailer "Yardr-Bead:"
		[ "$n_ba" -gt 0 ] && set -- "$@" --trailer "Yardr-Agent:"
		if [ $# -gt 0 ]; then
			trailer_git interpret-trailers --trim-empty --if-exists replace "$@" \
				--trailer "Yardr-Bead: $bead" --trailer "Yardr-Agent: $agent" --in-place "$msg"
		else
			trailer_git interpret-trailers --if-exists replace \
				--trailer "Yardr-Bead: $bead" --trailer "Yardr-Agent: $agent" --in-place "$msg"
		fi
	}
	add_trailers
fi
`

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
//
// The hook keeps to as few git calls as the message needs: the repo-hooks
// lookup reads the filesystem (see repoHooksFind), a message naming no
// trailer skips the counting, and one parse counts every kind before a
// single call writes the yard's two.
const commitHook = commitHookHead + repoHooksFind + commitHookTail

// passthroughHookHead opens the yard's shim for a repo hook other than
// commit-msg.
const passthroughHookHead = `#!/bin/sh
# The yard's pass-through for a repo hook other than commit-msg: pointing
# the session's git at the yard's hooks for commit-msg must not skip the
# repo's other hooks, so this runs the repo's hook of the same name as git
# would have run it, and git sees its exit status.
set -u
hook=${0##*/}
`

// passthroughHookTail is the shim past the shared repo-hooks lookup: run
// the repo's hook of the same name, or nothing where there is none.
const passthroughHookTail = `find_repo_hooks
if [ -n "${repo_hooks:-}" ] && [ -x "$repo_hooks/$hook" ] && [ ! "$repo_hooks/$hook" -ef "$0" ]; then
	exec "$repo_hooks/$hook" "$@"
fi
exit 0
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
// would run without the session's core.hooksPath override), with the same
// arguments and stdin, and git sees that hook's exit status. With no repo
// hook it exits 0, as a missing hook does. It never alters a commit
// message; only commit-msg does that. The lookup reads the filesystem
// (see repoHooksFind), so a repo with no hook of that name costs the shim
// no git call.
const passthroughHook = passthroughHookHead + repoHooksFind + passthroughHookTail

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

// EnsureCommitHook keeps the yard's hooks under home (config.GitHooks)
// when they are missing or outdated, and returns the hooks directory the
// session's core.hooksPath points at. Missing or outdated files link the
// shared cached copy (see ensureHookFile); each install is atomic, so
// sessions starting together never leave a half-written hook for a
// commit to run.
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

// hookCacheHash names the yard's hooks in the shared cache: every yard
// running this binary installs byte-identical hooks, so one cached copy
// serves them all, and another binary's hooks land beside these under
// their own hash.
var hookCacheHash = fmt.Sprintf("%x", sha256.Sum256([]byte(hookCacheKey())))[:16]

func hookCacheKey() string {
	var sb strings.Builder
	for _, f := range hookFiles() {
		sb.WriteString(f.name)
		sb.WriteByte(0)
		sb.WriteString(f.body)
		sb.WriteByte(0)
	}
	return sb.String()
}

// hookCacheDir is the per-user cache holding one copy of the yard's hooks
// for every home to link: outside any yard's home, so throwaway homes (a
// test's, an isolated yard's) share their inodes with every other yard of
// this user running this binary.
func hookCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	if base == "" {
		return "", fmt.Errorf("no user cache dir")
	}
	return filepath.Join(base, "yardr", "githooks-"+hookCacheHash), nil
}

// ensureCacheFile writes the cached copy of one hook file, and returns its
// path. The cache is content-addressed, so a copy already there with these
// bytes is this binary's, whatever yard wrote it; identical writers racing
// land the same bytes either way.
func ensureCacheFile(f hookFile) (string, error) {
	dir, err := hookCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := writeHookFile(dir, f); err != nil {
		return "", err
	}
	return filepath.Join(dir, f.name), nil
}

// linkFile atomically makes final a hard link to cache: link into a temp
// name beside final, then rename it over final. It reports false, having
// changed nothing, when the two cannot link (another volume, no support),
// in which case the caller writes the bytes instead.
func linkFile(cache, final string) bool {
	tmp, err := os.CreateTemp(filepath.Dir(final), ".hook-link-*")
	if err != nil {
		return false
	}
	name := tmp.Name()
	tmp.Close()
	os.Remove(name)
	if err := os.Link(cache, name); err != nil {
		return false
	}
	if err := os.Rename(name, final); err != nil {
		os.Remove(name)
		return false
	}
	return true
}

// ensureHookFile keeps one hook file (name, body) in dir: missing or
// outdated, it links the shared cached copy, so a fresh home costs no
// first execution per file (macOS evaluates a new executable's trust on
// first run, ~0.1 s of wait per new inode here); where the cache is
// unavailable or unlinkable it writes the bytes instead. A hook that lost
// its execute bit (a restore, a copy) is repaired: git runs a hook only
// when it executes.
func ensureHookFile(dir string, f hookFile) error {
	path := filepath.Join(dir, f.name)
	if cur, err := os.ReadFile(path); err != nil || string(cur) != f.body {
		if cache, cerr := ensureCacheFile(f); cerr == nil && linkFile(cache, path) {
			return nil
		}
		return writeHookFile(dir, f)
	}
	return repairHookExec(path, f)
}

// writeHookFile writes one hook file (name, body) in dir when it is
// missing or outdated, atomically, and repairs a hook that lost its
// execute bit. Sessions starting together never leave a half-written
// hook for a commit to run.
func writeHookFile(dir string, f hookFile) error {
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
	return repairHookExec(path, f)
}

// repairHookExec repairs a hook that lost its execute bit (a restore, a
// copy): git runs a hook only when it executes.
func repairHookExec(path string, f hookFile) error {
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

// systemGitconfigClean reports whether the install's system gitconfig names
// no core.hooksPath and no include: the one file the session's hooks cannot
// read for themselves (its path depends on the install), so the yard checks
// it once per session instead of the hooks asking git on every firing (see
// repoHooksFind). environ is the process environment to check under; git is
// the binary to ask with. A git that never runs, or one that hangs past its
// deadline, answers false, so the hooks ask git every time. A git that runs
// and refuses — no system file, or one it cannot read and ignores — is
// judged by what it printed, which is what git itself would read.
func systemGitconfigClean(git string, environ []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, git, "config", "--system", "--list")
	cmd.Env = withoutGitOverrides(environ)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		if _, ran := err.(*exec.ExitError); !ran {
			return false
		}
	}
	lower := strings.ToLower(string(out))
	return !strings.Contains(lower, "hookspath") && !strings.Contains(lower, "include")
}

// withoutGitOverrides is environ without the entries that would move the
// system gitconfig check off the install's own file: a GIT_CONFIG_SYSTEM
// override (the hooks read the override themselves, on every firing), a
// GIT_CONFIG_NOSYSTEM that some gits let suppress even an explicit
// --system query, and the command-line level entries, which a scoped
// --list ignores but an inherited one has no business joining.
func withoutGitOverrides(environ []string) []string {
	return slices.DeleteFunc(environ, func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return k == "GIT_CONFIG_SYSTEM" || k == "GIT_CONFIG_NOSYSTEM" ||
			k == "GIT_CONFIG_COUNT" || k == "GIT_CONFIG_PARAMETERS" ||
			strings.HasPrefix(k, "GIT_CONFIG_KEY_") || strings.HasPrefix(k, "GIT_CONFIG_VALUE_")
	})
}

// commitHookEnv points the session's git at the yard's commit-msg hook
// (EnsureCommitHook): core.hooksPath through the environment, so the depot's
// shared .git/config is never written. A runner with a home cannot start a
// session without its hook: otherwise commits would silently miss the
// trailers this hook guarantees. YARDR_GIT_SYSTEM_CLEAN vouches for the
// install's system gitconfig (see systemGitconfigClean): 1 when it names no
// core.hooksPath and no include, 0 otherwise, always set so no inherited
// value reads as the session's.
func commitHookEnv(home string) ([]string, error) {
	if home == "" {
		return nil, nil
	}
	dir, err := EnsureCommitHook(home)
	if err != nil {
		return nil, fmt.Errorf("install yard commit-msg hook: %w", err)
	}
	env := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=" + dir,
		// A command-line config inherited from the daemon or env_file has
		// higher precedence than GIT_CONFIG_COUNT and could hide this hook.
		"GIT_CONFIG_PARAMETERS=",
	}
	if systemGitconfigClean("git", os.Environ()) {
		env = append(env, config.Env("GIT_SYSTEM_CLEAN", "1")...)
	} else {
		env = append(env, config.Env("GIT_SYSTEM_CLEAN", "0")...)
	}
	return env, nil
}
