// Package rigcheck finds and runs a rig's merge gate: the command that must
// exit 0 before a bead's branch may land on the rig's base.
//
// The gate comes from the store (`yardr rig add --check <cmd>`, the
// operator's override) or else from the repo: .yardr/check at the tip of the
// rig's base branch. It is never read from the bead's branch or worktree, so
// a bead cannot lower its own bar; an edit to .yardr/check counts only once
// it has landed on the base.
//
// When the base has a remote, the check is read from the remote-tracking
// ref after fetching it, not from the local branch: refs are shared by every
// worktree of the rig, so a bead could `git update-ref` the local base to a
// commit of its own choosing. A rig without a remote reads the local base.
//
// A child of an epic lands on the epic's branch, not the base, but its gate
// is still the base's: read from the base exactly as above, and run on the
// child's branch. So an epic cannot change the bar its children land under.
//
// A rig with no gate at all fails ("no gate") unless it opts out with
// allow_no_check: a missing file must not read as a passing one.
//
// A dir rig is not a repository and has no base: its .yardr/check is read
// from the rig's directory as it is, and run in place. Without one a dir rig
// passes ("no gate"), the opposite of a git rig on purpose: nothing of a dir
// rig's merges anywhere, so there is no bar to lower.
package rigcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/store"
)

// RepoPath is where a rig keeps its check, relative to the repo root.
const RepoPath = ".yardr/check"

// FetchTimeout bounds the fetch of the base before the check is read: a
// dead remote must not hang the gate, and with it the merge queue.
var FetchTimeout = 60 * time.Second

// GitTimeout bounds each other git call made to find the gate, so a wedged
// repository (a stale lock, a hung filesystem) fails the run instead of
// hanging it.
var GitTimeout = 30 * time.Second

// DefaultTimeout bounds a check run when the caller sets no deadline of its
// own (yardr rig check --timeout).
const DefaultTimeout = 30 * time.Minute

// Check is a rig's gate as resolved right now.
type Check struct {
	// Source says where the gate came from: "store", "repo@<sha>" (the base
	// commit it was read from), "dir" (a dir rig's own file), or "" when
	// the rig has none.
	Source string `json:"source"`
	// From says which ref the repo's check was read from and how, e.g.
	// "origin/main (fetched)" or "main (no remote)".
	From string `json:"from,omitempty"`
	// Cmd is the store override, run with sh -c.
	Cmd string `json:"cmd,omitempty"`
	// Script is the content of .yardr/check at the base.
	Script []byte `json:"-"`
	// File is a dir rig's .yardr/check, run where it is.
	File string `json:"file,omitempty"`
}

// None reports whether the rig has no gate.
func (c *Check) None() bool { return c.Source == "" }

// Resolve finds the rig's gate. The store override wins; otherwise
// .yardr/check is read from the base commit with git, not from any
// checkout, so whatever a bead's branch holds there does not matter.
func Resolve(ctx context.Context, rig store.Rig) (*Check, error) {
	if rig.Check != "" {
		return &Check{Source: "store", Cmd: rig.Check}, nil
	}
	if rig.IsDir() {
		return resolveDir(rig)
	}
	ref, from, err := BaseRef(ctx, rig)
	if err != nil {
		return nil, err
	}
	sha, err := git(ctx, rig.Path, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("rig %s: base %q does not resolve: %w", rig.Name, ref, err)
	}
	sha = strings.TrimSpace(sha)
	// ls-tree tells "no check" apart from a failing git: an absent file is
	// no gate, which Run decides about, not a broken one.
	if out, err := git(ctx, rig.Path, "ls-tree", "--name-only", sha, "--", RepoPath); err != nil {
		return nil, err
	} else if strings.TrimSpace(out) == "" {
		return &Check{From: from}, nil
	}
	script, err := git(ctx, rig.Path, "show", sha+":"+RepoPath)
	if err != nil {
		return nil, err
	}
	return &Check{Source: "repo@" + sha[:min(12, len(sha))], From: from, Script: []byte(script)}, nil
}

// resolveDir finds a dir rig's .yardr/check in its directory.
func resolveDir(rig store.Rig) (*Check, error) {
	file := filepath.Join(rig.Path, filepath.FromSlash(RepoPath))
	info, err := os.Stat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return &Check{}, nil
	case err != nil:
		return nil, fmt.Errorf("rig %s: %w", rig.Name, err)
	case info.IsDir():
		return nil, fmt.Errorf("rig %s: %s is a directory, not a check", rig.Name, file)
	}
	script, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("rig %s: %w", rig.Name, err)
	}
	return &Check{Source: "dir", Script: script, File: file}, nil
}

// BaseRef is the ref the rig's repo config is read from, and a note on how
// it was chosen. When the base has a remote (rig.Base is "<remote>/<branch>",
// or a local branch whose upstream, or else origin, has it) the
// remote-tracking ref is fetched, bounded by FetchTimeout, with a forced
// refspec, so a tracking ref moved by hand is put back. A fetch that fails
// leaves the tracking ref as last fetched, and the note says so; only a rig
// with no remote, or whose remote lacks the branch, reads the local base.
func BaseRef(ctx context.Context, rig store.Rig) (ref, from string, err error) {
	remote, branch := remoteOf(ctx, rig)
	if remote == "" {
		return rig.Base, rig.Base + " (no remote)", nil
	}
	tracking := remote + "/" + branch
	ferr := fetch(ctx, rig.Path, remote, "+refs/heads/"+branch+":refs/remotes/"+tracking)
	if _, err := git(ctx, rig.Path, "rev-parse", "--verify", "--quiet", "refs/remotes/"+tracking+"^{commit}"); err != nil {
		if tracking == rig.Base {
			return "", "", fmt.Errorf("rig %s: base %q does not resolve (fetch: %v)", rig.Name, rig.Base, ferr)
		}
		return rig.Base, fmt.Sprintf("%s (%s has no %s)", rig.Base, remote, branch), nil
	}
	if ferr != nil {
		return "refs/remotes/" + tracking, fmt.Sprintf("%s (fetch failed, as last fetched: %v)", tracking, ferr), nil
	}
	return "refs/remotes/" + tracking, tracking + " (fetched)", nil
}

// remoteOf names the remote and branch the rig's base tracks, or "" when it
// has none.
func remoteOf(ctx context.Context, rig store.Rig) (remote, branch string) {
	out, err := git(ctx, rig.Path, "remote")
	if err != nil {
		return "", ""
	}
	remotes := strings.Fields(out)
	has := func(r string) bool {
		for _, x := range remotes {
			if x == r {
				return true
			}
		}
		return false
	}
	// A remote-tracking base: origin/main.
	if r, b, ok := strings.Cut(rig.Base, "/"); ok && has(r) {
		return r, b
	}
	// A local branch: its upstream's remote, else origin.
	if _, err := git(ctx, rig.Path, "rev-parse", "--verify", "--quiet", "refs/heads/"+rig.Base); err != nil {
		return "", ""
	}
	if out, err := git(ctx, rig.Path, "config", "--get", "branch."+rig.Base+".remote"); err == nil {
		r := strings.TrimSpace(out)
		b := rig.Base
		if m, err := git(ctx, rig.Path, "config", "--get", "branch."+rig.Base+".merge"); err == nil {
			b = strings.TrimPrefix(strings.TrimSpace(m), "refs/heads/")
		}
		if has(r) {
			return r, b
		}
	}
	if has("origin") {
		return "origin", rig.Base
	}
	return "", ""
}

func fetch(ctx context.Context, repo, remote, refspec string) error {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "fetch", "--quiet", "--no-tags", remote, refspec)
	// A remote asking for credentials fails at once instead of waiting on a
	// terminal nobody watches.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true", "SSH_ASKPASS=true")
	cmd.Stderr = &stderr
	// git may leave a child (ssh) holding its output open after it is
	// killed; do not wait on that past the deadline.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("timed out after %s", FetchTimeout)
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Result is one run of a check.
type Result struct {
	Rig    string `json:"rig"`
	Kind   string `json:"kind,omitempty"` // the rig's: git or dir
	Dir    string `json:"dir"`
	Source string `json:"source"`
	From   string `json:"from,omitempty"`
	Pass   bool   `json:"pass"`
	// ExitCode is the check's exit status (-1 when it was killed).
	ExitCode int `json:"exit_code"`
	// TimedOut: the check ran past the caller's deadline and was killed,
	// with everything it started. That fails the gate.
	TimedOut bool   `json:"timed_out,omitempty"`
	Output   string `json:"output"` // stdout and stderr, interleaved
	// Stranded are the rig's open beads in a stage their flow lacks, which
	// fail the check whatever the gate said. The caller fills it in: Run
	// has no store.
	Stranded []store.StrandedBead `json:"stranded,omitempty"`
	// StageNotes name, per flow governing the rig, the non-terminal stages
	// without a stage file: in Dir for a repository flow, in the yard's
	// home for a store flow. They never fail the check: stage files are
	// optional. The caller fills them in, as it does StrayStageFiles.
	StageNotes []string `json:"stage_notes,omitempty"`
	// ShadowNotes name each file of the rig's repository that nothing reads
	// while a flow in the store, or a prompt from a pack or the yard's home,
	// stands in front of it. They never fail the check: shadowing is how a
	// pack governs a rig. The caller fills them in.
	ShadowNotes []string `json:"shadow_notes,omitempty"`
	// StrayStageFiles are the stage files in Dir named after no stage of
	// their flow, which fail the check whatever the gate said: nobody is
	// ever shown one, so nobody else would see the typo.
	StrayStageFiles []string `json:"stray_stage_files,omitempty"`
	// StrayHomeStageFiles are the same for the store flows governing the
	// rig, whose stage files are in the yard's home: no branch's to fix.
	StrayHomeStageFiles []string `json:"stray_home_stage_files,omitempty"`
}

// Run resolves the rig's gate and runs it in dir (a bead's worktree, or the
// rig itself when dir is empty). A failing check is a Result with Pass
// false, not an error; errors mean the check could not be run at all. A rig
// with no gate fails too, unless it sets AllowNoCheck or is a dir rig. A check still running
// at ctx's deadline is killed and fails (TimedOut); finding the gate past
// the deadline is an error.
func Run(ctx context.Context, rig store.Rig, dir string) (*Result, error) {
	if dir == "" {
		dir = rig.Path
	}
	c, err := Resolve(ctx, rig)
	if err != nil {
		return nil, err
	}
	res := &Result{Rig: rig.Name, Kind: rig.Kind, Dir: dir, Source: c.Source, From: c.From}
	if c.None() {
		res.Pass = rig.AllowNoCheck || rig.IsDir()
		if !res.Pass {
			res.ExitCode = 1
			res.Output = fmt.Sprintf("no gate: rig %s has no --check and no %s on %s; add one, or set allow_no_check\n", rig.Name, RepoPath, c.From)
		}
		return res, nil
	}
	var cmd *exec.Cmd
	switch {
	case c.Cmd != "":
		cmd = exec.CommandContext(ctx, "sh", "-c", c.Cmd)
	case c.File != "":
		// A dir rig's check is a file already, with no git directory to
		// copy it to: run it in place, with sh unless it is executable.
		if info, err := os.Stat(c.File); err == nil && bytes.HasPrefix(c.Script, []byte("#!")) && info.Mode()&0o111 != 0 {
			cmd = exec.CommandContext(ctx, c.File)
		} else {
			cmd = exec.CommandContext(ctx, "sh", c.File)
		}
	default:
		// The script is a blob in git, not a file: write it out to run it.
		// It goes under the rig's git dir, not /tmp, which may be noexec,
		// and not the worktree, where it would show up as a change.
		path, err := writeScript(ctx, rig.Path, c.Script)
		if err != nil {
			return nil, err
		}
		defer os.Remove(path)
		if bytes.HasPrefix(c.Script, []byte("#!")) {
			cmd = exec.CommandContext(ctx, path)
		} else {
			cmd = exec.CommandContext(ctx, "sh", path)
		}
	}
	var out bytes.Buffer
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.Env = append(os.Environ(), "YARDR_RIG="+rig.Name)
	// The check runs in its own process group, killed whole at the
	// deadline: killing only sh would leave go test and its test binaries
	// running, holding the output open.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	res.Output = out.String()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.Pass = true
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.ExitCode, res.TimedOut = -1, true
		res.Output += "\nyardr: the check ran past its deadline and was killed\n"
	case ctx.Err() != nil:
		return nil, fmt.Errorf("run check: %w", ctx.Err())
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		return nil, fmt.Errorf("run check: %w", err)
	}
	return res, nil
}

func writeScript(ctx context.Context, repo string, script []byte) (string, error) {
	dir, err := GitPath(ctx, repo, "yardr")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "check-*")
	if err != nil {
		return "", err
	}
	_, werr := f.Write(script)
	cerr := f.Close()
	if err := errors.Join(werr, cerr, os.Chmod(f.Name(), 0o700)); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// GitPath is `git rev-parse --git-path <name>` in dir, made absolute: a
// place inside the checkout's git directory (per worktree for a linked
// worktree) that git never tracks.
func GitPath(ctx context.Context, dir, name string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--git-path", name)
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(out)
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return p, nil
}

// Tail is the last n lines of s.
func Tail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, GitTimeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
		}
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
