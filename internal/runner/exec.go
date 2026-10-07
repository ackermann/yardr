package runner

import (
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
	"syscall"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
)

// Exec runs config.cmd with sh -c, detached, logging to config.log or
// <Home>/logs/sessions/yard-<session>.log (see logPath). The command is expected to advance or close
// the bead itself; exiting without doing so ends the session as died.
//
// Besides Env, the command gets what a script needs to land or test a bead's
// work without hard-coding the depot (see contrib/assembly/merge-local):
//
//	YARDR_DEPOT, YARDR_DEPOT_PATH   the bead's depot, when it has one
//	YARDR_DEPOT_KIND  git, or dir for a plain directory: a dir depot's bead gets
//	                none of the three below, and runs in the depot's directory
//	YARDR_BASE      what the bead lands on: its train's branch for a child of
//	                a train, else the depot's base (see BaseOf). For a bead
//	                that came from a linked yard which named where its work
//	                starts, that commit: nothing of it lands here
//	YARDR_BRANCH    the bead's branch, yard/<bead>
//	YARDR_WORKTREE  where the herdr runner keeps that branch checked out
//	                (<worktrees>/<depot>/<bead>; config.worktrees as for herdr)
//	YARDR_TYPE      the bead's type (task, train, wagon, ...)
//	YARDR_TRAIN      the train the bead is a child of, empty for none: with
//	                YARDR_TYPE, how merge-local knows a wagon of a train,
//	                which lands under the depot's light gate
//	YARDR_BIN       this yardr, so the script's yardr commands reach it
//	YARDR_HOME      this yardr's state directory
type Exec struct {
	Home string // state directory; bead worktrees default to <Home>/worktrees
	Bin  string // this yardr's executable

	noReap bool // tests: start without the reaper, as after a daemon replace
}

func NewExec() *Exec { return &Exec{} }

func (e *Exec) Start(ctx context.Context, job dispatch.Job) (map[string]any, error) {
	// The store asks this of every group it writes. Asked again for one
	// stored before it did, of what the command needs only: a key nothing
	// reads is doctor's to report, and no reason not to run.
	for _, err := range store.ConfigProblems(store.GroupConfig, job.Group.Name, "exec", job.Group.Config) {
		if !errors.Is(err, store.ErrUnknownConfigKey) {
			return nil, err
		}
	}
	cmdline := str(job.Group.Config, "cmd", "")
	dir := str(job.Group.Config, "dir", "")
	if dir == "" && job.Depot != nil {
		dir = job.Depot.Path
	}
	if dir == "" {
		dir = os.TempDir()
	}
	logPath, err := e.logPath(job)
	if err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()

	cmd := exec.Command("sh", "-c", cmdline)
	cmd.Dir = dir
	cmd.Env = slices.Concat(inherited(job), Env(job, job.Group.Name), e.env(job))
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

// logPath is where the session's output goes: the group's config.log, else a
// file of its own under the yard's home, beside the yard's other logs. Not
// the system temp dir: a yard whose home is removed (an isolated yard's) then
// leaves nothing behind, whatever TMPDIR is. Only an Exec with no home (a
// test's) still logs there.
func (e *Exec) logPath(job dispatch.Job) (string, error) {
	if p := str(job.Group.Config, "log", ""); p != "" {
		return p, nil
	}
	name := "yard-" + job.Session.ID + ".log"
	if e.Home == "" {
		return filepath.Join(os.TempDir(), name), nil
	}
	dir := filepath.Join(e.Home, "logs", "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// inherited is this process's environment as the command inherits it:
// without the settings of the yard's server, which are not the work's
// (config.WithoutServerSettings). For a dir depot's bead, which has no base,
// branch or worktree, any the daemon was itself started with are dropped
// too, so they cannot read as the bead's.
func inherited(job dispatch.Job) []string {
	env := config.WithoutServerSettings(os.Environ())
	if job.Depot == nil || !job.Depot.IsDir() {
		return env
	}
	var drop []string // "YARDR_BASE=", "YARD_BASE=", ...
	for _, name := range []string{"BASE", "BRANCH", "WORKTREE"} {
		drop = append(drop, config.Env(name, "")...)
	}
	return slices.DeleteFunc(env, func(kv string) bool {
		return slices.ContainsFunc(drop, func(p string) bool { return strings.HasPrefix(kv, p) })
	})
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
	// Always set, empty for a bead with no train, so that one the daemon was
	// itself started with cannot read as this bead's.
	// EPIC is what TRAIN was called until this release: a copy of
	// merge-local installed before it reads that one.
	env = slices.Concat(env, config.Env("TYPE", job.Bead.Type), config.Env("TRAIN", job.Train), config.Env("EPIC", job.Train))
	if depot := job.Depot; depot != nil && depot.IsDir() {
		// A dir depot's bead has no branch, base or worktree: it is worked in
		// the depot's directory itself.
		return slices.Concat(env, config.Env("DEPOT", depot.Name), config.Env("DEPOT_PATH", depot.Path),
			config.Env("DEPOT_KIND", depot.Kind))
	}
	env = append(env, config.Env("BRANCH", Branch(job.Bead))...)
	if depot := job.Depot; depot != nil {
		env = slices.Concat(env, config.Env("DEPOT", depot.Name), config.Env("DEPOT_PATH", depot.Path),
			config.Env("DEPOT_KIND", store.DepotGit), config.Env("BASE", BaseOf(job)))
		root := str(job.Group.Config, "worktrees", "")
		if root == "" && e.Home != "" {
			root = filepath.Join(e.Home, "worktrees")
		}
		if root != "" {
			env = append(env, config.Env("WORKTREE", filepath.Join(root, depot.Name, job.Bead.ID))...)
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
	if !haveProcStat() {
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
	if haveProcStat() {
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

// stat is what tells one process from another with the same pid: its
// state and start time, as the OS reports them (see procStat).
type stat struct {
	state byte   // R, S, Z, ...
	start uint64 // the process's start time, compared for equality only
}

// haveProcStat is false where the OS reports no process state; liveness
// then falls back to kill(0), and a handle's pid is trusted.
func haveProcStat() bool { return peer.HaveStat() }

// procStat reads a process's state and start time. A pid with no process
// is fs.ErrNotExist.
func procStat(pid int) (stat, error) {
	st, err := peer.Describe(pid)
	if err != nil {
		return stat{}, err
	}
	return stat{state: st.State, start: st.Start}, nil
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
