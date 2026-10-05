package config

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"time"
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
