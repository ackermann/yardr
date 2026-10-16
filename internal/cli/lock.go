package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/peer"
)

// errDaemonRunning is what a second daemon on the same home fails with.
var errDaemonRunning = errors.New("yardr daemon already running")

// daemonLockWait is how long a starting daemon waits for a held lock
// before it refuses: a replaced daemon's new image takes the lock its old
// image held. A variable so a test need not sit it out.
var daemonLockWait = time.Second

// lockDaemon takes an exclusive flock on path, retrying for up to wait
// (a replaced daemon's new image may race a lingering holder), and writes
// this pid and its start time into it: the pid for the error a second
// daemon prints, both for the stop that verifies the holder before
// signalling it. The descriptor is close-on-exec (os.OpenFile's default),
// so the lock never leaks into runner children and is released by a
// self-replacing exec. unlock releases it.
func lockDaemon(path string, wait time.Duration) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				who := "unknown"
				if pid := config.DaemonPID(path); pid > 0 {
					who = strconv.Itoa(pid)
				}
				return nil, fmt.Errorf("%w (pid %s holds %s)", errDaemonRunning, who, path)
			}
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := f.Truncate(0); err == nil {
		// The pid and the start time the OS reports for it, by which a
		// stop verifies the pid is still this daemon's before signalling
		// it. Where the OS reports no start time the pid stands alone,
		// as before.
		id := strconv.Itoa(os.Getpid())
		if st, err := peer.Describe(os.Getpid()); err == nil {
			id += " " + strconv.FormatUint(st.Start, 10)
		}
		_, _ = f.WriteAt([]byte(id+"\n"), 0)
	}
	return func() {
		_ = f.Truncate(0)
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// homeCheck is how often the daemon looks whether its home is still its own.
// Tests shorten it.
var homeCheck = config.HomeCheck

// whileHomeStands returns a context that ends, with one line in the log,
// when the daemon's home is gone or its lock file is no longer the one it
// holds: the daemon then stops as on SIGTERM, with exit status 0, so that a
// login service does not start it again in a home yardr would make anew.
// Like the server (see config.Held), a daemon is started detached and
// nothing else stops one whose home was removed under it. The lock must be
// held already. release ends the watch.
// A daemon a test starts stops the same way when the test process it was
// told about (YARDR_TEST_OWNER) is gone, as the server does.
//
// The home watch ends the context with a homeGone cause (see
// homeLostCause), so the daemon tells this end from a SIGTERM apart: on
// this one it closes its herdr sessions first, on a SIGTERM it leaves
// them for down or the daemon that takes them over.
func whileHomeStands(ctx context.Context, cfg *config.Config, every time.Duration, log *slog.Logger) (context.Context, func(), error) {
	held, err := cfg.Hold(cfg.DaemonLock())
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancelCause(ctx)
	gone := held.Watch(ctx, every)
	owner, ownerStart, watched := config.TestOwner()
	var ownerGone <-chan struct{}
	if watched {
		ownerGone = config.WatchOwner(ctx, every, owner, ownerStart)
	} else if raw := config.Getenv(config.TestOwnerEnv); raw != "" {
		log.Warn("ignoring a malformed YARDR_TEST_OWNER", "value", raw)
	}
	go func() {
		select {
		case why := <-gone:
			log.Warn("the yardr daemon's home is no longer its own; stopping", "home", cfg.Home, "reason", why)
			cancel(homeGone{reason: why})
		case <-ownerGone:
			log.Warn("the test that started the yardr daemon is gone; stopping", "owner", owner)
			cancel(nil)
		case <-ctx.Done():
		}
	}()
	return ctx, func() { cancel(nil); held.Close() }, nil
}

// homeGone is the cause with which the home watch ends the daemon's
// context when its home is lost: the watch's reason, not a new flag.
type homeGone struct{ reason string }

func (e homeGone) Error() string { return e.reason }

// homeLostCause is the reason the home watch ended ctx for, "" when
// something else (a SIGTERM, a self-replace) ended it.
func homeLostCause(err error) string {
	var gone homeGone
	if errors.As(err, &gone) {
		return gone.reason
	}
	return ""
}

// closeHomeSessions ends every herdr session in the snapshot the daemon
// kept: each crew member's line and conversation sessions and each running
// bead session, through the runner's Stop with the handle on record. It is
// what the daemon does when its home is gone under it: nothing else can
// close such a pane, the yard is gone and `yardr down` has nothing to read.
// The store is behind the server's socket in the home, so no listing can be
// made now; the snapshot was taken while it could. Only herdr sessions are
// closed; anything of another runner is left as a SIGTERM leaves it. There
// is no handoff and nothing is written to the store, and each failure is
// logged and skipped.
func closeHomeSessions(ctx context.Context, sessions []dispatch.HomeSession, runners map[string]dispatch.Runner, log *slog.Logger) {
	herdr := runners["herdr"]
	if herdr == nil {
		log.Warn("herdr's runner is gone; the yard's herdr panes are left running")
		return
	}
	for _, s := range sessions {
		if s.Runner != "herdr" || len(s.Handle) == 0 {
			continue
		}
		if err := herdr.Stop(ctx, s.Handle); err != nil {
			log.Warn("could not close the herdr session", "session", s.String(), "agent", s.Agent, "err", err)
			continue
		}
		log.Info("closed the herdr session", "session", s.String(), "agent", s.Agent)
	}
}
