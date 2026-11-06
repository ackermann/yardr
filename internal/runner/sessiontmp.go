package runner

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"yardr.dev/yardr/internal/dispatch"
)

// TmpdirArg is where an agent's argument names the session's private
// temporary directory, the one place a read-only sandbox lets it write. It
// is for the `read only` preset of harnesses/codex.yaml: a shell makes a
// temporary file for a here-document, so a sandbox that writes nowhere
// refuses `yardr bead note <id> - <<'NOTE'`, and a directory only this
// session may use is the least that lets it through. The runner makes the
// directory when a group's args hold this (sessionTmp), puts its path there
// as it does the socket's, and removes it when the session is stopped (or,
// for a crew member's that was gone, let go: CloseLeft). The path goes in as
// it is, inside whatever quotes the argument has around it, so sessionTmp
// refuses one that could end them.
const TmpdirArg = "{tmpdir}"

// handleTmpdir is the handle key that holds the session's temporary
// directory, for Stop and CloseLeft to remove.
const handleTmpdir = "tmpdir"

// tmpdirPrefix begins the name of every directory sessionTmp makes.
// removeSessionTmp checks it before it deletes one: the handle is the
// store's, and a recursive delete does not take a path on its word.
const tmpdirPrefix = ".tmp-"

// sessionTmp makes the session's temporary directory when its agent's args
// name one (TmpdirArg), puts its path on handle and returns it; "" is a
// session whose args do not, which is every one but a read-only sandbox's.
// The directory is made beside the worktrees (the group's root, else the
// runner's), mode 0700, with a name nothing else has, and its path has its
// symlinks resolved, which is the one a sandbox is asked about. A failure
// removes what was made.
func (h *Herdr) sessionTmp(cfg map[string]any, handle map[string]any) (string, error) {
	if !slices.ContainsFunc(strs(cfg, "args"), func(a string) bool { return strings.Contains(a, TmpdirArg) }) {
		return "", nil
	}
	root, err := filepath.Abs(str(cfg, "worktrees", cmp.Or(h.WorktreeRoot, os.TempDir())))
	if err == nil {
		err = os.MkdirAll(root, 0o700)
	}
	if err != nil {
		return "", fmt.Errorf("temporary directory for the session: %w", err)
	}
	dir, err := os.MkdirTemp(root, tmpdirPrefix)
	if err != nil {
		return "", fmt.Errorf("temporary directory for the session: %w", err)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		err = fmt.Errorf("temporary directory for the session: %w", err)
	} else if strings.ContainsFunc(real, func(r rune) bool { return r == '"' || r == '\\' || unicode.IsControl(r) }) {
		// The sandbox's setting is a TOML or JSON string with the path in
		// it: a quote would end the string and the rest of the path would
		// be a setting of its own.
		err = dispatch.Blocked(fmt.Errorf("the session's temporary directory %q has a quote, a backslash or a control character, which cannot go into the agent's arguments: put the worktrees root elsewhere", real))
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	handle[handleTmpdir] = real
	return real, nil
}

// sessionTmpEnv is what points a session at its temporary directory dir:
// TMPDIR, where mktemp, git and most programs make theirs, and TMPPREFIX,
// where zsh makes the file of a here-document. macOS's own sh and bash
// (3.2) read neither and make that file in /var/tmp, so a sandbox that
// writes only to dir still refuses them. None for no directory.
func sessionTmpEnv(dir string) []string {
	if dir == "" {
		return nil
	}
	return []string{"TMPDIR=" + dir, "TMPPREFIX=" + filepath.Join(dir, "zsh")}
}

// removeSessionTmp removes the temporary directory handle names, when it is
// one sessionTmp made: an absolute, clean path whose last element begins
// with tmpdirPrefix. Anything else the handle holds is left alone. Removing
// what is not there is no error, so Stop can run again.
func removeSessionTmp(handle map[string]any) error {
	dir, _ := handle[handleTmpdir].(string)
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || !strings.HasPrefix(filepath.Base(dir), tmpdirPrefix) {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove the session's temporary directory: %w", err)
	}
	return nil
}
