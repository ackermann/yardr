package preview

import (
	"fmt"
	"os"
	"sync"
)

// The captured output of one preview is kept to 5 MiB over three files
// (section 6.3): the live file and two rotations, each a third of the
// allowance, so a preview that logs without end loses its oldest lines
// and never the yard's disk.
const (
	logTotal = 5 << 20
	logFiles = 3
	logEach  = logTotal / logFiles
)

// rotateWriter appends to a preview's log file, rotating it at logEach:
// <app>.log.2 is dropped, .1 becomes .2, the live file becomes .1 and a
// fresh file starts. Writes from the app's stdout and stderr interleave
// here under the one lock.
type rotateWriter struct {
	mu   sync.Mutex
	path string
	f    *os.File
	n    int64
}

func newRotateWriter(path string) (*rotateWriter, error) {
	w := &rotateWriter{path: path}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotateWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.n = f, st.Size()
	return nil
}

func (w *rotateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.n+int64(len(p)) > logEach {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.n += int64(n)
	return n, err
}

func (w *rotateWriter) rotate() error {
	if err := w.f.Close(); err != nil {
		return err
	}
	_ = os.Remove(w.path + ".2")
	_ = os.Rename(w.path+".1", w.path+".2")
	_ = os.Rename(w.path, w.path+".1")
	return w.open()
}

func (w *rotateWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// LogTail returns the last n lines of the preview's log at path, newest
// last, reading the rotations oldest first so the tail is whole where
// the lines span a rotation.
func LogTail(path string, n int) ([]string, error) {
	if n < 0 {
		return nil, fmt.Errorf("preview logs: tail must not be negative")
	}
	var lines []string
	for _, p := range []string{path + ".2", path + ".1", path} {
		raw, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		lines = append(lines, splitLines(string(raw))...)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
