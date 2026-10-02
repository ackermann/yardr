package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
//	YARDR_RIG, YARDR_RIG_PATH   the bead's rig, when it has one
//	YARDR_BASE      what the bead lands on: its epic's branch for a child of
//	                an epic, else the rig's base (see BaseOf)
//	YARDR_BRANCH    the bead's branch, yard/<bead>
//	YARDR_WORKTREE  where the herdr runner keeps that branch checked out
//	                (<worktrees>/<rig>/<bead>; config.worktrees as for herdr)
//	YARDR_BIN       this yardr, so the script's yardr commands reach it
//	YARDR_HOME      this yardr's state directory
type Exec struct {
	Home string // state directory; bead worktrees default to <Home>/worktrees
	Bin  string // this yardr's executable

	noReap bool // tests: start without the reaper, as after a daemon replace
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
		if errors.Is(err, exec.ErrNotFound) {
			return nil, dispatch.Runtime(err) // no shell: no bead can start
		}
		return nil, err
	}
	pid := cmd.Process.Pid
	h := map[string]any{"pid": pid, "log": logPath}
	// The start time tells this process apart from a later one that reuses
	// its pid. It is set at fork, so reading it before the exec is fine.
	if st, err := procStat(pid); err == nil {
		h["start"] = st.start
	}
	// Reap in the background so the child does not linger as a zombie. This
	// goroutine dies when the daemon replaces itself; Alive reaps then.
	if !e.noReap {
		go func() { _ = cmd.Wait() }()
	}
	return h, nil
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
			config.Env("BASE", BaseOf(job)))
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

// Alive reports whether the session's process still runs. A zombie is
// reaped and counts as gone: after a daemon replace nothing else waits for
// it, and kill(pid, 0) alone would call it running forever. A pid that now
// belongs to a process started later than the session's is gone too.
func (e *Exec) Alive(_ context.Context, h map[string]any) (bool, error) {
	pid, err := pidOf(h)
	if err != nil {
		return false, err
	}
	if !hasProc() {
		if wpid, _ := syscall.Wait4(pid, nil, syscall.WNOHANG, nil); wpid == pid {
			return false, nil
		}
		err = syscall.Kill(pid, 0)
		return err == nil || errors.Is(err, syscall.EPERM), nil
	}
	st, err := procStat(pid)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !owns(h, st) {
		return false, nil
	}
	if st.state == 'Z' {
		// Checked before waiting so a reused pid's exit status, which some
		// other cmd.Wait in this daemon expects, is never taken. ECHILD
		// means it is not our child; it is gone either way.
		_, _ = syscall.Wait4(pid, nil, syscall.WNOHANG, nil)
		return false, nil
	}
	return true, nil
}

// Stop signals the session's process group, unless its pid now belongs to
// another process. A leader that is gone entirely still gets the signal:
// while any of its group lives the kernel does not hand its pid out again,
// so -pid can only reach what the session left behind.
func (e *Exec) Stop(_ context.Context, h map[string]any) error {
	pid, err := pidOf(h)
	if err != nil {
		return nil // nothing was started
	}
	if hasProc() {
		if st, err := procStat(pid); err == nil && !owns(h, st) {
			return nil
		}
	}
	// The process runs in its own session; signal the whole group.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

// owns reports whether st is the process the handle started. Handles from
// before start times were recorded trust the pid.
func owns(h map[string]any, st stat) bool {
	want, ok := uintOf(h["start"])
	return !ok || want == st.start
}

type stat struct {
	state byte   // R, S, Z, ...
	start uint64 // clock ticks after boot
}

var procOnce = sync.OnceValue(func() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
})

// hasProc is false where there is no /proc; liveness falls back to kill(0).
func hasProc() bool { return procOnce() }

// procStat reads state and start time from /proc/<pid>/stat.
func procStat(pid int) (stat, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return stat{}, err
	}
	// The command name may hold spaces and parentheses; fields resume after
	// the last ')'. State is field 3, starttime field 22.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return stat{}, fmt.Errorf("pid %d: unexpected stat %q", pid, b)
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return stat{}, fmt.Errorf("pid %d: unexpected stat %q", pid, b)
	}
	start, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return stat{}, fmt.Errorf("pid %d: start time: %w", pid, err)
	}
	return stat{state: f[0][0], start: start}, nil
}

func uintOf(v any) (uint64, bool) {
	switch v := v.(type) {
	case uint64:
		return v, true
	case int:
		return uint64(v), true
	case int64:
		return uint64(v), true
	case float64:
		return uint64(v), true
	case string:
		n, err := strconv.ParseUint(v, 10, 64)
		return n, err == nil
	}
	return 0, false
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
