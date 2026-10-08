// Package depotfile reads a file of a depot where a bead cannot have changed
// it: at the tip of the depot's base, or in the directory itself for a depot
// that has none. A brief's role prompt is read so, and a pack asks it whether
// a depot has a role of its own.
package depotfile

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GitTimeout bounds each git call made here, so a wedged repository (a stale
// lock, a hung filesystem) fails the read instead of hanging it: a brief is
// written while the agent's pane waits.
var GitTimeout = 30 * time.Second

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
