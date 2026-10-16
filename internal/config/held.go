package config

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/peer"
)

// HomeCheck is how often a server and a daemon look whether the home they
// serve is still theirs.
const HomeCheck = time.Minute

// Held is a home as the process serving it found it: the directory, and
// the files in it the process made (the server's socket, the daemon's
// lock). A home that is removed under a server or a daemon leaves a process
// nothing can reach and nothing stops: a test's temp directory, a yard torn
// down. So each looks, on a timer, whether what it holds is still there, and
// stops when it is not.
type Held struct {
	home  string
	dir   *os.File // kept open: the directory's inode is then not given to another
	was   os.FileInfo
	files []heldFile
}

type heldFile struct {
	path string
	was  os.FileInfo
}

// Hold records the home and the files, which must exist.
func (c *Config) Hold(files ...string) (*Held, error) {
	dir, err := os.Open(c.Home)
	if err != nil {
		return nil, err
	}
	h := &Held{home: c.Home, dir: dir}
	if h.was, err = dir.Stat(); err != nil {
		dir.Close()
		return nil, err
	}
	for _, p := range files {
		was, err := os.Stat(p)
		if err != nil {
			dir.Close()
			return nil, err
		}
		h.files = append(h.files, heldFile{p, was})
	}
	return h, nil
}

// Close lets go of the directory.
func (h *Held) Close() { h.dir.Close() }

// Lost says what of the held home is no longer there or no longer the one
// that was held, "" when all of it is. Only a clear answer counts: a path
// that cannot be read just now (permissions, a volume that hiccups) is not
// taken for gone.
func (h *Held) Lost() string {
	st, err := os.Stat(h.home)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return h.home + " is gone"
	case err != nil:
		return ""
	case !os.SameFile(st, h.was):
		return h.home + " is another directory than the one this process started in"
	}
	for _, f := range h.files {
		st, err := os.Stat(f.path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return f.path + " is gone"
		case err == nil && !os.SameFile(st, f.was):
			return f.path + " is another file than the one this process made"
		}
	}
	return ""
}

// TestOwnerEnv names the test-only variable in which a test names its
// process to the server and the daemon it starts: "<pid> <start>", the pid
// and the start time peer.Describe reports for it (the daemon-lock idiom).
// A test binary killed by its timeout runs no cleanup, so without the watch
// the server and the daemon it started outlive it, their homes intact and
// nothing ending them. A real yard never sets it; it is deliberately not a
// ServerSetting, so it is never blanked into work sessions nor baked into
// login-service files.
const TestOwnerEnv = "TEST_OWNER"

// TestOwner is the owner the test process named in TestOwnerEnv: false when
// the variable is missing or malformed, in which case there is no watch.
func TestOwner() (pid int, start uint64, ok bool) {
	raw := Getenv(TestOwnerEnv)
	if raw == "" {
		return 0, 0, false
	}
	fields := strings.Fields(raw)
	if len(fields) != 2 {
		return 0, 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return 0, 0, false
	}
	start, err = strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return pid, start, true
}

// WatchOwner looks every interval and sends once, on the channel it
// returns, when the owner process (pid with start, as TestOwner reports it)
// is gone; it ends with that, or when ctx does. The server and the daemon
// watch the test process that started them so, on the home-check tick.
func WatchOwner(ctx context.Context, every time.Duration, pid int, start uint64) <-chan struct{} {
	gone := make(chan struct{}, 1)
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !peer.Alive(pid, start) {
					gone <- struct{}{}
					return
				}
			}
		}
	}()
	return gone
}

// Watch looks every interval and sends what was lost, once, on the channel
// it returns; it ends with that, or when ctx does.
func (h *Held) Watch(ctx context.Context, every time.Duration) <-chan string {
	lost := make(chan string, 1)
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if why := h.Lost(); why != "" {
					lost <- why
					return
				}
			}
		}
	}()
	return lost
}
