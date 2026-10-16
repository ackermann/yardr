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
	"yardr.dev/yardr/internal/peer"
)

// errDaemonRunning is what a second daemon on the same home fails with.
var errDaemonRunning = errors.New("yardr daemon already running")

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
const homeCheck = config.HomeCheck

// whileHomeStands returns a context that ends, with one line in the log,
// when the daemon's home is gone or its lock file is no longer the one it
// holds: the daemon then stops as on SIGTERM, with exit status 0, so that a
// login service does not start it again in a home yardr would make anew.
// Like the server (see config.Held), a daemon is started detached and
// nothing else stops one whose home was removed under it. The lock must be
// held already. release ends the watch.
// A daemon a test starts stops the same way when the test process it was
// told about (YARDR_TEST_OWNER) is gone, as the server does.
func whileHomeStands(ctx context.Context, cfg *config.Config, every time.Duration, log *slog.Logger) (context.Context, func(), error) {
	held, err := cfg.Hold(cfg.DaemonLock())
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
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
			cancel()
		case <-ownerGone:
			log.Warn("the test that started the yardr daemon is gone; stopping", "owner", owner)
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() { cancel(); held.Close() }, nil
}
