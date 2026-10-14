package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"yardr.dev/yardr/internal/depotfile"
	"yardr.dev/yardr/internal/dispatch"
)

// BriefDir is where briefs for the worktree at path go: yardr/ in the
// worktree's own git directory (.git/worktrees/<name>/yardr for a linked
// worktree). That is outside the tracked tree, so a brief never shows up as
// a change, and .yardr/ in the tree is left for committed config.
func BriefDir(ctx context.Context, worktree string) (string, error) {
	return depotfile.GitPath(ctx, worktree, "yardr")
}

// DirBriefDir is where briefs for a bead of the dir depot at path go:
// .yardr/briefs/ in the directory itself, since there is no git directory
// to hide them in. The folder ignores itself, for a dir depot that happens to
// be a checkout.
func DirBriefDir(path string) string {
	return filepath.Join(path, ".yardr", "briefs")
}

// WorktreeSession is who works in a bead's worktree: the session a herdr
// group last started there, as its tab's environment names it. It is kept
// next to the briefs (see BriefDir) for a yardr command whose environment
// is another session's: an agent whose commands run in a process that
// outlives its pane (Codex's shared background server) hands them the
// environment of whichever session started that process. The worktree a
// command runs in is the one thing such a process does not get wrong.
type WorktreeSession struct {
	Bead    string `json:"bead"`
	Session string `json:"session"`
	Actor   string `json:"actor"`
	Stage   string `json:"stage"`
	Group   string `json:"group"`
}

const worktreeSessionFile = "session.json"

// writeWorktreeSession records job's session as the one working the
// worktree whose brief directory is dir, in place of an earlier stage's.
func writeWorktreeSession(dir string, job dispatch.Job, actor string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(WorktreeSession{Bead: job.Bead.ID, Session: job.Session.ID, Actor: actor,
		Stage: job.Bead.Stage, Group: job.Group.Name})
	if err != nil {
		return err
	}
	// Renamed into place, so a command never reads half of one.
	tmp := filepath.Join(dir, worktreeSessionFile+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, worktreeSessionFile))
}

// SessionOf is the session working the bead worktree that dir is in, nil
// when dir is in none: outside a checkout, in one no herdr group started a
// session in, or in a dir depot, whose beads share one directory.
func SessionOf(ctx context.Context, dir string) *WorktreeSession {
	briefs, err := BriefDir(ctx, dir)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(briefs, worktreeSessionFile))
	if err != nil {
		return nil
	}
	ws := new(WorktreeSession)
	if json.Unmarshal(raw, ws) != nil || ws.Bead == "" || ws.Session == "" {
		return nil
	}
	return ws
}

// writeBrief writes a session's brief into dir, which it creates.
func writeBrief(dir, session, brief string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "brief-"+session+".md")
	if err := os.WriteFile(path, []byte(brief), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
