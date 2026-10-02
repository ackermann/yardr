package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
)

// Exec runs config.cmd with sh -c, detached, logging to config.log or
// <workdir>/.yard-<session>.log. The command is expected to advance or close
// the bead itself; exiting without doing so ends the session as died.
//
// Besides Env, the command gets what a script needs to land or test a bead's
// work without hard-coding the rig (see contrib/refinery/merge-local):
//
//	YARDR_RIG, YARDR_RIG_PATH, YARDR_BASE   the bead's rig, when it has one
//	YARDR_BRANCH    the bead's branch, yard/<bead>
//	YARDR_WORKTREE  where the herdr runner keeps that branch checked out
//	                (<worktrees>/<rig>/<bead>; config.worktrees as for herdr)
//	YARDR_BIN       this yardr, so the script's yardr commands reach it
//	YARDR_HOME      this yardr's state directory
type Exec struct {
	Home string // state directory; bead worktrees default to <Home>/worktrees
	Bin  string // this yardr's executable
}

func NewExec() *Exec { return &Exec{} }

func (e *Exec) Start(ctx context.Context, job dispatch.Job) (map[string]any, error) {
	cmdline := str(job.Group.Config, "cmd", "")
	if cmdline == "" {
		return nil, fmt.Errorf("group %s: exec runner needs config.cmd", job.Group.Name)
	}
	dir := str(job.Group.Config, "dir", "")
	if dir == "" && job.Rig != nil {
		dir = job.Rig.Path
	}
	if dir == "" {
		dir = os.TempDir()
	}
	logPath := str(job.Group.Config, "log", filepath.Join(os.TempDir(), "yard-"+job.Session.ID+".log"))
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()

	cmd := exec.Command("sh", "-c", cmdline)
	cmd.Dir = dir
	cmd.Env = slices.Concat(os.Environ(), Env(job, job.Group.Name), e.env(job))
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	// Reap in the background so the child never lingers as a zombie, which
	// would make Alive report it running forever.
	go func() { _ = cmd.Wait() }()
	return map[string]any{"pid": pid, "log": logPath}, nil
}

// env is the exec-only part of the command's environment.
func (e *Exec) env(job dispatch.Job) []string {
	var env []string
	if e.Home != "" {
		env = append(env, config.Env("HOME", e.Home)...)
	}
	if e.Bin != "" {
		env = append(env, config.Env("BIN", e.Bin)...)
	}
	env = append(env, config.Env("BRANCH", Branch(job.Bead))...)
	if rig := job.Rig; rig != nil {
		env = slices.Concat(env, config.Env("RIG", rig.Name), config.Env("RIG_PATH", rig.Path),
			config.Env("BASE", rig.Base))
		root := str(job.Group.Config, "worktrees", "")
		if root == "" && e.Home != "" {
			root = filepath.Join(e.Home, "worktrees")
		}
		if root != "" {
			env = append(env, config.Env("WORKTREE", filepath.Join(root, rig.Name, job.Bead.ID))...)
		}
	}
	return env
}

func (e *Exec) Alive(_ context.Context, h map[string]any) (bool, error) {
	pid, err := pidOf(h)
	if err != nil {
		return false, err
	}
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM), nil
}

func (e *Exec) Stop(_ context.Context, h map[string]any) error {
	pid, err := pidOf(h)
	if err != nil {
		return nil // nothing was started
	}
	// The process runs in its own session; signal the whole group.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func pidOf(h map[string]any) (int, error) {
	switch v := h["pid"].(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case uint64:
		return int(v), nil
	case float64:
		return int(v), nil
	case string:
		return strconv.Atoi(v)
	}
	return 0, fmt.Errorf("handle has no pid: %v", h)
}
