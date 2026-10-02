package cli

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// errDaemonRunning is what a second daemon on the same home fails with.
var errDaemonRunning = errors.New("yardr daemon already running")

// lockDaemon takes an exclusive flock on path, retrying for up to wait
// (a replaced daemon's new image may race a lingering holder), and writes
// this pid into it for the error a second daemon prints. The descriptor is
// close-on-exec (os.OpenFile's default), so the lock never leaks into
// runner children and is released by a self-replacing exec. unlock
// releases it.
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
			holder, _ := os.ReadFile(path)
			f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				pid := strings.TrimSpace(string(holder))
				if pid == "" {
					pid = "unknown"
				}
				return nil, fmt.Errorf("%w (pid %s holds %s)", errDaemonRunning, pid, path)
			}
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() {
		_ = f.Truncate(0)
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
