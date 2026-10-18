package runner

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// After a daemon replace nothing waits for an exec session's process, so
// it exits into a zombie. Alive must reap it and call it gone, and the next
// pass must end the session, or the group's slot stays taken forever.
func TestExecZombieEndsTheSession(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	mustT(t, st.PutGroup(ctx, store.Group{Name: "assembly", Runner: "exec", Limit: 1,
		Config: map[string]any{"cmd": "exit 0", "dir": t.TempDir()}}, "me"))
	_, err := st.AddRoute(ctx, store.Route{Stage: "merge", Group: "assembly"}, "me")
	mustT(t, err)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "land it", Stage: "merge"}, "t")
	mustT(t, err)

	e := &Exec{noReap: true}
	d := &dispatch.Dispatcher{Store: st, Runners: map[string]dispatch.Runner{"exec": e},
		Backoff: time.Hour}
	mustT(t, d.Reconcile(ctx))
	got, err := st.GetBead(ctx, b.ID)
	mustT(t, err)
	if got.Session == nil {
		t.Fatalf("not started: %+v", got)
	}
	s, err := st.GetSession(ctx, *got.Session)
	mustT(t, err)
	pid, err := pidOf(s.Handle)
	mustT(t, err)
	if _, ok := uintOf(s.Handle["start"]); !ok {
		t.Fatalf("handle has no start time: %v", s.Handle)
	}

	// Wait for the zombie: the process has exited and nobody reaped it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if st, err := procStat(pid); err == nil && st.state == 'Z' {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process never became a zombie")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("kill(0) on a zombie: %v; the test no longer shows the bug", err)
	}

	mustT(t, d.Reconcile(ctx))
	s, err = st.GetSession(ctx, s.ID)
	mustT(t, err)
	if s.State != "died" {
		t.Fatalf("session state = %q, want died", s.State)
	}
	if _, err := procStat(pid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("zombie not reaped: %v", err)
	}
	if alive, err := e.Alive(ctx, s.Handle); alive || err != nil {
		t.Fatalf("Alive = %v, %v after reaping", alive, err)
	}
}

// A handle whose pid now belongs to a later process is dead, and Stop
// leaves that process alone.
func TestExecStalePidIsNotOurs(t *testing.T) {
	ctx := context.Background()
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	mustT(t, cmd.Start())
	pid := cmd.Process.Pid
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-exited })

	real, err := procStat(pid)
	mustT(t, err)
	e := &Exec{}
	ours := map[string]any{"pid": pid, "start": float64(real.start)}
	if alive, err := e.Alive(ctx, ours); !alive || err != nil {
		t.Fatalf("Alive with matching start = %v, %v", alive, err)
	}
	stale := map[string]any{"pid": pid, "log": "x", "start": float64(real.start - 1)}
	if alive, err := e.Alive(ctx, stale); alive || err != nil {
		t.Fatalf("Alive with stale start = %v, %v", alive, err)
	}
	mustT(t, e.Stop(ctx, stale))
	select {
	case <-exited:
		t.Fatal("Stop signalled a process the session does not own")
	case <-time.After(300 * time.Millisecond):
	}
}

// A cmd that is no string (`--set cmd=true` stores the boolean) fails the
// session with what was stored and how the string is written, where it said
// only that the group has no cmd. group add refuses such a group; this is
// for one that came another way.
func TestExecSaysWhatCmdIsWhenItIsNoString(t *testing.T) {
	ctx := context.Background()
	start := func(cfg map[string]any) error {
		_, err := (&Exec{noReap: true}).Start(ctx, dispatch.Job{
			Bead:    store.Bead{ID: "b-1"},
			Session: store.Session{ID: "s-1"},
			Group:   store.Group{Name: "tick", Runner: "exec", Config: cfg},
		})
		return err
	}
	for want, cfg := range map[string]map[string]any{
		`group tick: exec runner needs config.cmd as a string, got the JSON boolean true; write --set 'cmd="true"'`:  {"cmd": true},
		`group tick: exec runner needs config.cmd as a string, got the JSON number 1.5; write --set 'cmd="1.5"'`:     {"cmd": 1.5},
		`group tick: exec runner needs config.dir as a string, got the JSON object {"a":1}; write --set 'dir="..."'`: {"cmd": "true", "dir": map[string]any{"a": 1.0}},
		"group tick: exec runner needs config.cmd": {},
	} {
		if err := start(cfg); err == nil || err.Error() != want {
			t.Errorf("Start(%v) = %v, want %s", cfg, err, want)
		}
	}
	if err := start(map[string]any{"cmd": "true", "dir": t.TempDir(), "log": t.TempDir() + "/log"}); err != nil {
		t.Fatalf("the string true is a command: %v", err)
	}
	// A key nothing reads is no reason not to run: the group was stored
	// before the store refused one, and doctor reports it.
	if err := start(map[string]any{"cmd": "true", "dir": t.TempDir(), "log": t.TempDir() + "/log", "idel": "5m"}); err != nil {
		t.Fatalf("a key nothing reads stopped the session: %v", err)
	}
}

// With no config.log a session logs under the yard's home, where it was the
// system temp dir: an isolated yard, whose environment has no TMPDIR, left
// one file per session in /tmp that removing the yard did not take
// (yardr-q2g8). config.log still wins.
func TestExecLogsUnderTheHome(t *testing.T) {
	ctx := context.Background()
	home, tmp := filepath.Join(t.TempDir(), "home"), t.TempDir()
	t.Setenv("TMPDIR", tmp)
	e := &Exec{Home: home, noReap: true}
	start := func(session string, cfg map[string]any) string {
		t.Helper()
		h, err := e.Start(ctx, dispatch.Job{
			Bead:    store.Bead{ID: "b-1"},
			Session: store.Session{ID: session},
			Group:   store.Group{Name: "tick", Runner: "exec", Config: cfg},
		})
		mustT(t, err)
		pid, err := pidOf(h)
		mustT(t, err)
		_, _ = syscall.Wait4(pid, nil, 0, nil) // the command's output is all written
		log, _ := h["log"].(string)
		if b, err := os.ReadFile(log); err != nil || string(b) != "said\n" {
			t.Errorf("session %s: log %s holds %q (%v), want what the command said", session, log, b, err)
		}
		return log
	}
	if got, want := start("s-1", map[string]any{"cmd": "echo said"}), filepath.Join(home, "logs", "sessions", "yard-s-1.log"); got != want {
		t.Errorf("default log = %s, want %s", got, want)
	}
	set := filepath.Join(t.TempDir(), "own.log")
	if got := start("s-2", map[string]any{"cmd": "echo said", "log": set}); got != set {
		t.Errorf("log with config.log = %s, want %s", got, set)
	}
	if left, err := os.ReadDir(tmp); err != nil || len(left) != 0 {
		t.Errorf("the temp dir holds %v (%v), want nothing", left, err)
	}
}

// A stop signals only the session's own process group: the leader's pid
// and start time must still match the handle. A pid that is gone, or that
// another process has reused, is never signalled, and neither ESRCH nor
// EPERM from the signal is an error. (yardr-4wfs: the stop of a stale
// session signalled whatever process had the pid next — twice a landing's
// go test — and a group id of another user's answered EPERM, which the
// teardown retried forever.)
func TestExecStopSignalsOnlyAVerifiedLeader(t *testing.T) {
	ctx := context.Background()
	const pid = 424242
	type sent struct {
		pid int
		sig syscall.Signal
	}
	term := []sent{{-pid, syscall.SIGTERM}}
	for _, tc := range []struct {
		name     string
		handle   map[string]any
		describe func(pid int) (stat, error)
		kill     func(pid int, sig syscall.Signal) error
		alive    bool
		want     []sent
	}{
		{
			name:     "a pid with no process is never signalled",
			handle:   map[string]any{"pid": pid, "start": 7},
			describe: func(pid int) (stat, error) { return stat{}, fs.ErrNotExist },
			kill: func(pid int, sig syscall.Signal) error {
				t.Errorf("signal(%d, %v) on a gone pid", pid, sig)
				return nil
			},
		},
		{
			name:     "a pid another process reused is never signalled",
			handle:   map[string]any{"pid": pid, "start": 7},
			describe: func(pid int) (stat, error) { return stat{state: 'R', start: 8}, nil },
			kill: func(pid int, sig syscall.Signal) error {
				t.Errorf("signal(%d, %v) on a reused pid", pid, sig)
				return nil
			},
		},
		{
			name:     "a handle with no start time owns nothing verifiable",
			handle:   map[string]any{"pid": pid},
			describe: func(pid int) (stat, error) { return stat{state: 'S', start: 7}, nil },
			kill: func(pid int, sig syscall.Signal) error {
				t.Errorf("signal(%d, %v) on an unverifiable pid", pid, sig)
				return nil
			},
		},
		{
			name:     "the session's own leader is signalled as a group",
			handle:   map[string]any{"pid": pid, "start": 7},
			describe: func(pid int) (stat, error) { return stat{state: 'S', start: 7}, nil },
			kill:     func(pid int, sig syscall.Signal) error { return nil },
			alive:    true,
			want:     term,
		},
		{
			name:     "the session's own zombie is signalled for what it left behind",
			handle:   map[string]any{"pid": pid, "start": 7},
			describe: func(pid int) (stat, error) { return stat{state: 'Z', start: 7}, nil },
			kill:     func(pid int, sig syscall.Signal) error { return nil },
			want:     term,
		},
		{
			name:     "EPERM signals nothing of ours: gone, not an error",
			handle:   map[string]any{"pid": pid, "start": 7},
			describe: func(pid int) (stat, error) { return stat{state: 'S', start: 7}, nil },
			kill:     func(pid int, sig syscall.Signal) error { return syscall.EPERM },
			alive:    true,
			want:     term,
		},
		{
			name:     "ESRCH is a gone group: not an error",
			handle:   map[string]any{"pid": pid, "start": 7},
			describe: func(pid int) (stat, error) { return stat{state: 'S', start: 7}, nil },
			kill:     func(pid int, sig syscall.Signal) error { return syscall.ESRCH },
			alive:    true,
			want:     term,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []sent
			e := &Exec{describe: tc.describe, kill: func(pid int, s syscall.Signal) error {
				got = append(got, sent{pid, s})
				return tc.kill(pid, s)
			}}
			if alive, err := e.Alive(ctx, tc.handle); alive != tc.alive || err != nil {
				t.Fatalf("Alive = %v, %v; want %v", alive, err, tc.alive)
			}
			if err := e.Stop(ctx, tc.handle); err != nil {
				t.Fatalf("Stop = %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("signals = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExecStopRetriesWhenProcessIdentityIsUnknown(t *testing.T) {
	wantErr := errors.New("process table unavailable")
	var signals []int
	e := &Exec{
		describe: func(int) (stat, error) { return stat{}, wantErr },
		kill: func(pid int, _ syscall.Signal) error {
			signals = append(signals, pid)
			return nil
		},
	}
	err := e.Stop(context.Background(), map[string]any{"pid": 424242, "start": 7})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Stop = %v, want %v so teardown can retry", err, wantErr)
	}
	if len(signals) != 0 {
		t.Fatalf("signals = %v, want none while identity is unknown", signals)
	}
}

// A live session whose process is gone ends, and its stop signals nothing
// and is not retried: the next pass has nothing left to stop.
func TestExecStaleSessionEndsAndStopsOnce(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	mustT(t, st.PutGroup(ctx, store.Group{Name: "assembly", Runner: "exec", Limit: 1,
		Config: map[string]any{"cmd": "exit 0", "dir": t.TempDir()}}, "me"))
	_, err := st.AddRoute(ctx, store.Route{Stage: "merge", Group: "assembly"}, "me")
	mustT(t, err)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "land it", Stage: "merge"}, "t")
	mustT(t, err)

	const stalePid = 424242
	type sent struct {
		pid int
		sig syscall.Signal
	}
	var signals []sent
	e := &Exec{noReap: true,
		describe: func(pid int) (stat, error) {
			if pid == stalePid {
				return stat{}, fs.ErrNotExist
			}
			return procStat(pid)
		},
		kill: func(pid int, s syscall.Signal) error {
			signals = append(signals, sent{pid, s})
			return nil
		}}
	d := &dispatch.Dispatcher{Store: st, Runners: map[string]dispatch.Runner{"exec": e},
		Backoff: time.Hour}
	mustT(t, d.Reconcile(ctx))
	got, err := st.GetBead(ctx, b.ID)
	mustT(t, err)
	if got.Session == nil {
		t.Fatalf("not started: %+v", got)
	}
	s, err := st.GetSession(ctx, *got.Session)
	mustT(t, err)
	realPid, err := pidOf(s.Handle)
	mustT(t, err)
	t.Cleanup(func() {
		_, _ = syscall.Wait4(realPid, nil, syscall.WNOHANG, nil) // exit 0, abandoned
	})

	mustT(t, st.SetHandle(ctx, s.ID, map[string]any{"pid": stalePid, "start": 7}))
	mustT(t, d.Reconcile(ctx))
	s, err = st.GetSession(ctx, s.ID)
	mustT(t, err)
	if s.State != "died" {
		t.Fatalf("session state = %q, want died", s.State)
	}
	if !s.Stopped {
		t.Fatal("stale session not marked stopped")
	}
	if len(signals) != 0 {
		t.Fatalf("signals = %v, want none: a gone pid is never signalled", signals)
	}
	mustT(t, d.Reconcile(ctx))
	if len(signals) != 0 {
		t.Fatalf("signals after a second pass = %v, want none", signals)
	}
	if u, err := st.Unstopped(ctx); err != nil || len(u) != 0 {
		t.Fatalf("Unstopped = %v, %v; want none", u, err)
	}
}

// After a daemon replace nothing waits for an exec session's process, but
// the session is still the daemon's to supervise: passes keep it while it
// runs and end it once it exits. (noReap stands in for the replace: a
// start without the reaper goroutine the exec dropped.)
func TestExecAdoptedSessionRunsUntilItsProcessExits(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	mustT(t, st.PutGroup(ctx, store.Group{Name: "assembly", Runner: "exec", Limit: 1,
		Config: map[string]any{"cmd": "sleep 30", "dir": t.TempDir()}}, "me"))
	_, err := st.AddRoute(ctx, store.Route{Stage: "merge", Group: "assembly"}, "me")
	mustT(t, err)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "land it", Stage: "merge"}, "t")
	mustT(t, err)

	e := &Exec{noReap: true}
	d := &dispatch.Dispatcher{Store: st, Runners: map[string]dispatch.Runner{"exec": e},
		Backoff: time.Hour}
	mustT(t, d.Reconcile(ctx))
	got, err := st.GetBead(ctx, b.ID)
	mustT(t, err)
	if got.Session == nil {
		t.Fatalf("not started: %+v", got)
	}
	s, err := st.GetSession(ctx, *got.Session)
	mustT(t, err)
	pid, err := pidOf(s.Handle)
	mustT(t, err)
	start, ok := uintOf(s.Handle["start"])
	if !ok {
		t.Fatalf("handle has no start time: %v", s.Handle)
	}
	t.Cleanup(func() {
		// Whatever is left of our own process, and only that.
		if st, err := procStat(pid); err == nil && st.start == start {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		_, _ = syscall.Wait4(pid, nil, syscall.WNOHANG, nil)
	})

	// Adopted, not ended: pass after pass it still runs.
	mustT(t, d.Reconcile(ctx))
	s, err = st.GetSession(ctx, s.ID)
	mustT(t, err)
	if s.State != "running" {
		t.Fatalf("adopted session state = %q, want running", s.State)
	}

	// Its process exits: the next pass ends the session and reaps it.
	mustT(t, e.Stop(ctx, s.Handle))
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := procStat(pid)
		if errors.Is(err, fs.ErrNotExist) || st.state == 'Z' {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stopped process never exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
	mustT(t, d.Reconcile(ctx))
	s, err = st.GetSession(ctx, s.ID)
	mustT(t, err)
	if s.State != "died" {
		t.Fatalf("session state = %q, want died", s.State)
	}
	if _, err := procStat(pid); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the adopted process was not reaped: %v", err)
	}
	if !s.Stopped {
		t.Fatal("ended adopted session not marked stopped")
	}
}

// A stop for a session whose process is gone must not touch the process
// group that has the freed pid now: here a group whose leader exited,
// standing in for the next landing's go test. (yardr-4wfs: two landings
// died this way.)
func TestReplacedDaemonLeavesAnUnrelatedGroupAlone(t *testing.T) {
	ctx := context.Background()
	// A group that outlives its leader: sh exits at once, its sleep stays
	// in the group. The sleep's output goes nowhere: it would otherwise
	// hold the pipe Wait waits on open for its whole 30 seconds.
	cmd := exec.Command("sh", "-c", "sleep 30 </dev/null >/dev/null 2>&1 & echo $!")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	var out bytes.Buffer
	cmd.Stdout = &out
	mustT(t, cmd.Start())
	leader := cmd.Process.Pid
	mustT(t, cmd.Wait()) // reaps the leader; its pid is free again
	sleep, err := strconv.Atoi(strings.TrimSpace(out.String()))
	mustT(t, err)
	sleepStat, err := procStat(sleep)
	mustT(t, err)
	t.Cleanup(func() {
		if st, err := procStat(sleep); err == nil && st.start == sleepStat.start {
			_ = syscall.Kill(sleep, syscall.SIGKILL)
		}
	})
	if _, err := procStat(leader); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the leader's pid is not free: %v; the test no longer shows the bug", err)
	}

	st := storetest.New(t)
	mustT(t, st.PutGroup(ctx, store.Group{Name: "assembly", Runner: "exec", Limit: 1,
		Config: map[string]any{"cmd": "exit 0", "dir": t.TempDir()}}, "me"))
	_, err = st.AddRoute(ctx, store.Route{Stage: "merge", Group: "assembly"}, "me")
	mustT(t, err)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "landed", Stage: "merge"}, "t")
	mustT(t, err)
	// The stale record of a landing that merged: ended, never stopped.
	sess, err := st.Claim(ctx, b.ID, "assembly", "merge", "yard")
	mustT(t, err)
	mustT(t, st.SetHandle(ctx, sess.ID, map[string]any{"pid": leader, "start": 1}))
	mustT(t, st.EndSession(ctx, sess.ID, "died", "the script exited"))
	if _, err := st.SetHold(ctx, b.ID, true, store.Expect{}, "t"); err != nil {
		t.Fatalf("hold: %v", err)
	}

	type sent struct {
		pid int
		sig syscall.Signal
	}
	var signals []sent
	e := &Exec{kill: func(pid int, s syscall.Signal) error {
		signals = append(signals, sent{pid, s})
		return syscall.Kill(pid, s)
	}}
	d := &dispatch.Dispatcher{Store: st, Runners: map[string]dispatch.Runner{"exec": e},
		Backoff: time.Hour}
	mustT(t, d.Reconcile(ctx))

	if len(signals) != 0 {
		t.Fatalf("signals = %v, want none: a gone pid is never signalled", signals)
	}
	if err := syscall.Kill(sleep, 0); err != nil {
		t.Fatalf("the unrelated sleep (pid %d) is gone (%v): the stop signalled its group", sleep, err)
	}
	s, err := st.GetSession(ctx, sess.ID)
	mustT(t, err)
	if !s.Stopped {
		t.Fatal("the stale session was not marked stopped")
	}
	if u, err := st.Unstopped(ctx); err != nil || len(u) != 0 {
		t.Fatalf("Unstopped = %v, %v; want none", u, err)
	}
}

// An exec group's command inherits the daemon's environment, and the
// daemon may have started with neither USER nor LOGNAME
// (server.ServerEnv passes none): each one missing or empty is the OS's
// own name for the process's uid instead, so a script that looks the user
// up by name answers with one. One the daemon has is kept, once.
func TestExecEnvCarriesUser(t *testing.T) {
	me, err := config.LookupUsername(user.Current)
	if err != nil || me == "" {
		t.Skipf("the system names no user for this process: %v", err)
	}
	named := func(env []string, key string) (string, int) {
		var got string
		n := 0
		for _, kv := range env {
			if k, v, _ := strings.Cut(kv, "="); k == key {
				got, n = v, n+1
			}
		}
		return got, n
	}
	check := func(t *testing.T, env []string, wantUser, wantLogin string) {
		t.Helper()
		if got, n := named(env, "USER"); got != wantUser || n != 1 {
			t.Fatalf("USER = %q (%d entries), want %q once", got, n, wantUser)
		}
		if got, n := named(env, "LOGNAME"); got != wantLogin || n != 1 {
			t.Fatalf("LOGNAME = %q (%d entries), want %q once", got, n, wantLogin)
		}
	}
	t.Run("kept", func(t *testing.T) {
		t.Setenv("USER", "exec-user")
		t.Setenv("LOGNAME", "exec-login")
		check(t, inherited(dispatch.Job{}), "exec-user", "exec-login")
	})
	t.Run("unset", func(t *testing.T) {
		unset := func(k string) {
			if v, ok := os.LookupEnv(k); ok {
				t.Cleanup(func() { os.Setenv(k, v) })
			}
			os.Unsetenv(k)
		}
		unset("USER")
		unset("LOGNAME")
		check(t, inherited(dispatch.Job{}), me, me)
	})
	t.Run("empty", func(t *testing.T) {
		t.Setenv("USER", "")
		t.Setenv("LOGNAME", "")
		check(t, inherited(dispatch.Job{}), me, me)
	})
}
