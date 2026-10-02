// Package rigcheck finds and runs a rig's merge gate: the command that must
// exit 0 before a bead's branch may land on the rig's base.
//
// The gate comes from the store (`yardr rig add --check <cmd>`, the
// operator's override) or else from the repo: .yardr/check at the tip of the
// rig's base branch. It is never read from the bead's branch or worktree, so
// a bead cannot lower its own bar; an edit to .yardr/check counts only once
// it has landed on the base.
package rigcheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"yardr.dev/yardr/internal/store"
)

// RepoPath is where a rig keeps its check, relative to the repo root.
const RepoPath = ".yardr/check"

// Check is a rig's gate as resolved right now.
type Check struct {
	// Source says where the gate came from: "store", "repo@<sha>" (the base
	// commit it was read from), or "" when the rig has none.
	Source string `json:"source"`
	// Cmd is the store override, run with sh -c.
	Cmd string `json:"cmd,omitempty"`
	// Script is the content of .yardr/check at the base.
	Script []byte `json:"-"`
}

// None reports whether the rig has no gate: nothing blocks the merge.
func (c *Check) None() bool { return c.Source == "" }

// Resolve finds the rig's gate. The store override wins; otherwise
// .yardr/check is read from the base commit with git, not from any
// checkout, so whatever a bead's branch holds there does not matter.
func Resolve(ctx context.Context, rig store.Rig) (*Check, error) {
	if rig.Check != "" {
		return &Check{Source: "store", Cmd: rig.Check}, nil
	}
	sha, err := git(ctx, rig.Path, "rev-parse", "--verify", "--quiet", rig.Base+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("rig %s: base %q does not resolve: %w", rig.Name, rig.Base, err)
	}
	sha = strings.TrimSpace(sha)
	// ls-tree tells "no check" apart from a failing git: an absent file is
	// not an error, the rig simply has no gate.
	if out, err := git(ctx, rig.Path, "ls-tree", "--name-only", sha, "--", RepoPath); err != nil {
		return nil, err
	} else if strings.TrimSpace(out) == "" {
		return &Check{}, nil
	}
	script, err := git(ctx, rig.Path, "show", sha+":"+RepoPath)
	if err != nil {
		return nil, err
	}
	return &Check{Source: "repo@" + sha[:min(12, len(sha))], Script: []byte(script)}, nil
}

// Result is one run of a check.
type Result struct {
	Rig    string `json:"rig"`
	Dir    string `json:"dir"`
	Source string `json:"source"`
	Pass   bool   `json:"pass"`
	// ExitCode is the check's exit status (-1 when it was killed).
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"` // stdout and stderr, interleaved
}

// Run resolves the rig's gate and runs it in dir (a bead's worktree, or the
// rig itself when dir is empty). A failing check is a Result with Pass
// false, not an error; errors mean the check could not be run at all.
func Run(ctx context.Context, rig store.Rig, dir string) (*Result, error) {
	if dir == "" {
		dir = rig.Path
	}
	c, err := Resolve(ctx, rig)
	if err != nil {
		return nil, err
	}
	res := &Result{Rig: rig.Name, Dir: dir, Source: c.Source}
	if c.None() {
		res.Pass = true
		return res, nil
	}
	var cmd *exec.Cmd
	if c.Cmd != "" {
		cmd = exec.CommandContext(ctx, "sh", "-c", c.Cmd)
	} else {
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
	err = cmd.Run()
	res.Output = out.String()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.Pass = true
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
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
