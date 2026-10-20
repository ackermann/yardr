package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
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
//	YARDR_TEST      the depot's test command (yardr depot add --test), for the
//	                script to run with sh -c if it tests; unset when the
//	                depot has none
//	YARDR_TYPE      the bead's type (task, train, wagon, ...)
//	YARDR_TRAIN      the train the bead is a child of, empty for none: with
//	                YARDR_TYPE, how merge-local knows a wagon of a train,
//	                which lands on its train's branch, not the base
//	YARDR_KIND      exec: the session runs a script, not an agent (a herdr
//	                session's is its harness kind); with YARDR_MODEL, what the
//	                yard's commit-msg hook writes into Yardr-Agent
//	YARDR_MODEL     the agent model: the bead's metadata.model, else the
//	                group's config.model
//	YARDR_GIT_SYSTEM_CLEAN  1 when the install's system gitconfig names
//	                no core.hooksPath and no include (checked once per
//	                session), else 0: the hooks' word for the one file
//	                they cannot read themselves (see systemGitconfigClean)
//	YARDR_BIN       this yardr, so the script's yardr commands reach it
//	YARDR_HOME      this yardr's state directory
//
// A commit the session makes goes through the yard's commit-msg hook
// (config.GitHooks, pointed at with core.hooksPath in the session's
// environment): harness trailers go, Yardr-Bead and Yardr-Agent go on (see
// commitHook).
//
// And the entries of the group's config.env_file, as a herdr group's agents
// get theirs (see envFile): what the script needs to act outside the yard,
// such as the path of a deploy key, which belongs neither in the store, where
// cmd is, nor in the daemon's own environment.
type Exec struct {
	Home string // state directory; bead worktrees default to <Home>/worktrees
	Bin  string // this yardr's executable
	// Log, if set, gets the key names (never the values) an env_file loads.
	Log *slog.Logger

	noReap bool // tests: start without the reaper, as after a daemon replace
	// describe and kill are a fake process table and kill for tests; nil
	// uses the real ones (see procStat and syscall.Kill).
	describe func(pid int) (stat, error)
	kill     func(pid int, sig syscall.Signal) error
}

func (e *Exec) Start(ctx context.Context, job dispatch.Job) (map[string]any, error) {
	// The store asks this of every group it writes. Asked again for one
	// stored before it did, of what the command needs only: a key nothing
	// reads is doctor's to report, and no reason not to run.
	for _, err := range store.ConfigProblems(store.GroupConfig, job.Group.Name, "exec", job.Group.Config) {
		if !errors.Is(err, store.ErrUnknownConfigKey) {
			return nil, err
		}
	}
	hookEnv, err := commitHookEnv(e.Home)
	if err != nil {
		return nil, err
	}
	cmdline := str(job.Group.Config, "cmd", "")
	dir := str(job.Group.Config, "dir", "")
	if dir == "" && job.Depot != nil {
		dir = job.Depot.Path
	}
	if dir == "" {
		dir = os.TempDir()
	}
	// Before anything is made: a group whose env_file is refused starts
	// nothing until it is fixed.
	secrets, err := envFile(e.Log, job.Group.Config, "group "+job.Group.Name)
	if err != nil {
		return nil, err
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
	// The file's entries come after what is inherited, so they win over the
	// daemon's own, and are held to what the daemon's are: none of what
	// withheld leaves out, and no key yardr sets itself (withEnvFile).
	cmd.Env = slices.Concat(inherited(job),
		withEnvFile(slices.Concat(Env(job, job.Group.Name), e.env(job, hookEnv)), withheld(job, secrets)))
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
	dir := filepath.Join(config.At(e.Home).Logs(), "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// osUserLookup is user.Current as a variable, so a test can be a machine
// whose user lookup fails or blocks (yardr-tbm4, yardr-8ms7).
var osUserLookup = user.Current

// inherited is this process's environment as the command inherits it:
// without what withheld leaves out, and with USER and LOGNAME set. The
// daemon may have started with neither (server.ServerEnv passes none),
// and each one missing or empty is the OS's own name for the process's
// uid instead, so a script that looks the user up by name answers with
// one. The OS is asked only when one of them is missing or empty, where
// its lookup can hang past signals, and a lookup that answers with no
// user leaves them as they are.
func inherited(job dispatch.Job) []string {
	return withUser(withheld(job, os.Environ()))
}

// withUser is env with USER and LOGNAME set: each one missing or empty
// taken from the OS's own name for the process's uid, an empty entry
// dropped so the fallback is the only one. One that is set is kept.
func withUser(env []string) []string {
	var haveUser, haveLogin bool
	out := slices.DeleteFunc(env, func(kv string) bool {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "USER":
			haveUser = v != ""
			return v == ""
		case "LOGNAME":
			haveLogin = v != ""
			return v == ""
		}
		return false
	})
	if haveUser && haveLogin {
		return out
	}
	if who, err := config.LookupUsername(osUserLookup); err == nil && who != "" {
		if !haveUser {
			out = append(out, "USER="+who)
		}
		if !haveLogin {
			out = append(out, "LOGNAME="+who)
		}
	}
	return out
}

// withheld is environ, this process's or an env_file's, without what the
// command gets from neither: the settings of the yard's server, which are
// not the work's (config.WithoutServerSettings); a test command, so that one
// the daemon was itself started with cannot read as the depot's, which may
// have none; the yard's word about the system gitconfig, so that one the
// daemon was itself started with cannot read as this session's (see
// systemGitconfigClean); for a dir depot's bead, which has no base, branch
// or worktree, any of those too; and git's GIT_CONFIG_COUNT/KEY_n/VALUE_n,
// which would otherwise win over (or add to) the core.hooksPath the session
// points at its own commit-msg hook with (see commitHookEnv): a repeated
// variable is answered with its first, so one inherited from the daemon
// would beat the session's appended one. GIT_CONFIG_PARAMETERS is
// command-line config and has higher precedence still, so the session
// clears it as well.
func withheld(job dispatch.Job, environ []string) []string {
	env := config.WithoutServerSettings(environ)
	names := []string{"TEST", "GIT_SYSTEM_CLEAN"}
	if job.Depot != nil && job.Depot.IsDir() {
		names = append(names, "BASE", "BRANCH", "WORKTREE")
	}
	var drop []string // "YARDR_TEST=", "YARDR_BASE=", ...
	for _, name := range names {
		drop = append(drop, config.Env(name, "")...)
	}
	return slices.DeleteFunc(env, func(kv string) bool {
		if strings.HasPrefix(kv, "GIT_CONFIG_COUNT=") ||
			strings.HasPrefix(kv, "GIT_CONFIG_KEY_") ||
			strings.HasPrefix(kv, "GIT_CONFIG_VALUE_") ||
			strings.HasPrefix(kv, "GIT_CONFIG_PARAMETERS=") {
			return true
		}
		return slices.ContainsFunc(drop, func(p string) bool { return strings.HasPrefix(kv, p) })
	})
}

// env is the exec-only part of the command's environment.
func (e *Exec) env(job dispatch.Job, hookEnv []string) []string {
	var env []string
	if e.Home != "" {
		env = append(env, config.Env("HOME", e.Home)...)
	}
	if e.Bin != "" {
		env = append(env, config.Env("BIN", e.Bin)...)
	}
	// What the yard's commit-msg hook writes into Yardr-Agent (see
	// commitHook): exec for the kind, since the session runs a script, not
	// an agent. Beside it, the core.hooksPath that points the session's git
	// at the hook, through the environment, so the depot's shared
	// .git/config is never written.
	env = slices.Concat(env, config.Env("KIND", "exec"), config.Env("MODEL", sessionModel(job)))
	env = append(env, hookEnv...)
	// Always set, empty for a bead with no train, so that one the daemon was
	// itself started with cannot read as this bead's.
	env = slices.Concat(env, config.Env("TYPE", job.Bead.Type), config.Env("TRAIN", job.Train))
	// Unset, not empty, for a depot with none: a script asks whether there
	// is one with ${YARDR_TEST+x} or -n alike.
	if depot := job.Depot; depot != nil && depot.Test != "" {
		env = append(env, config.Env("TEST", depot.Test)...)
	}
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
			root = config.At(e.Home).Worktrees()
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
	if !e.canStat() {
		if wpid, _ := syscall.Wait4(pid, nil, syscall.WNOHANG, nil); wpid == pid {
			return false, nil
		}
		// Without process state only the signal tells: EPERM is another
		// user's process, not ours.
		err = e.signal(pid, 0)
		return err == nil, nil
	}
	st, err := e.statOf(pid)
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

// Stop signals the session's process group, but only when its leader is
// still the session's own process: the pid and start time must match the
// handle. A pid that is gone, or that another process has reused, is never
// signalled — the group id may now belong to unrelated processes — and a
// signal refused with EPERM reached nothing of ours either: another
// user's group, or one whose members are all zombies. None of the three
// is an error: a stop that errored would be retried on every pass,
// rolling the same dice again. (yardr-4wfs: the stop of a stale session
// signalled whatever had reused its pid, and EPERM retried 30,000 times.)
func (e *Exec) Stop(_ context.Context, h map[string]any) error {
	pid, err := pidOf(h)
	if err != nil {
		return nil // nothing was started
	}
	if e.canStat() {
		st, err := e.statOf(pid)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // gone: the pid names no process
		}
		if err != nil {
			return err // identity is unknown: let teardown retry
		}
		if !owns(h, st) {
			return nil // reused: another process has the pid now
		}
	}
	// The process runs in its own session; signal the whole group.
	if err := e.signal(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
		return err
	}
	// Wait for the leader in the background so it never lingers as a
	// zombie: after a daemon replace nothing else waits for it, and the
	// teardown is the last look at the session. Where the start's own
	// reaper still runs, one of the two waiters wins and the other's
	// error is dropped; where the pid is not our child at all, ECHILD
	// answers at once.
	go func() {
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &status, 0, nil)
	}()
	return nil
}

// owns reports whether st is the process the handle started: the pid and
// start time must both match. A handle with no start time recorded — from
// before starts were recorded, or a start the OS would not describe —
// owns nothing: on a platform that verifies, an unverifiable pid is never
// signalled. (Alive and Stop call this only where they can verify; where
// the OS reports no state, the pid is trusted as before.)
func owns(h map[string]any, st stat) bool {
	want, ok := uintOf(h["start"])
	return ok && want == st.start
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

// canStat reports whether statOf verifies a process: the fake table in
// tests always does, the OS only where it reports process state.
func (e *Exec) canStat() bool { return e.describe != nil || haveProcStat() }

// statOf reads a process's state and start time, from the fake table in
// tests, from the OS otherwise. A pid with no process is fs.ErrNotExist.
func (e *Exec) statOf(pid int) (stat, error) {
	if e.describe != nil {
		return e.describe(pid)
	}
	return procStat(pid)
}

// signal sends sig to pid (negative: the process group), through the fake
// table in tests.
func (e *Exec) signal(pid int, sig syscall.Signal) error {
	if e.kill != nil {
		return e.kill(pid, sig)
	}
	return syscall.Kill(pid, sig)
}

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
