package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/mckean/yard/internal/dispatch"
)

// Manual starts nothing. The bead waits on its group until someone advances
// or closes it.
type Manual struct{}

func (Manual) Start(context.Context, dispatch.Job) (map[string]any, error) {
	return map[string]any{"manual": true}, nil
}
func (Manual) Alive(context.Context, map[string]any) (bool, error) { return true, nil }
func (Manual) Stop(context.Context, map[string]any) error          { return nil }

// Exec runs config.cmd with sh -c, detached, logging to config.log or
// <workdir>/.yard-<session>.log. The command is expected to advance or close
// the bead itself; exiting without doing so ends the session as died.
type Exec struct{}

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
	cmd.Env = append(os.Environ(), Env(job, job.Group.Name)...)
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
