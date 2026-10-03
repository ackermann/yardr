// Package peer tells who is on the other end of a unix socket (the pid of
// the connecting process) and what a process is (its state, start time and
// parent), as the OS reports them. The store uses it to recognise a call
// from a session's pane (the herdr tab's shell, an exec runner's script) by
// the process tree, not by what the caller says of itself (see
// store.Expect); the exec runner, to tell its script from a later process
// that reuses its pid.
package peer

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
)

// ErrUnknown means the connection does not tell its peer: not a unix
// socket, or an OS without peer credentials.
var ErrUnknown = errors.New("peer unknown")

// PID is the pid of the process on the other end of c, a unix socket
// connection accepted by this process.
func PID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("%w: %T is not a unix socket", ErrUnknown, c)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	pid := 0
	cerr := raw.Control(func(fd uintptr) { pid, err = peerPID(int(fd)) })
	if cerr != nil {
		return 0, cerr
	}
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnknown, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("%w: the kernel reports pid %d", ErrUnknown, pid)
	}
	return pid, nil
}

// Stat is what the OS says of a process: its state (R, S, Z, ...), its
// start time, which tells it from a later process with the same pid and is
// compared for equality only, and its parent's pid.
type Stat struct {
	State byte
	Start uint64
	PPID  int
}

// Describe reads what the OS says of the process pid. A pid with no
// process is fs.ErrNotExist; where the OS tells nothing (HaveStat is
// false) it is errors.ErrUnsupported.
func Describe(pid int) (Stat, error) { return stat(pid) }

// Parent is the parent pid of pid: 0 when pid has none or is gone, or
// where the OS does not tell.
func Parent(pid int) (int, error) {
	if pid <= 1 {
		return 0, nil
	}
	st, err := stat(pid)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errors.ErrUnsupported) {
		return 0, nil
	}
	return st.PPID, err
}

// Alive reports whether pid is the process st describes, still running:
// a reaped or missing pid, a zombie and a process started at another time
// (the pid reused) are not. Where the OS tells nothing, a live pid is
// taken for it.
func Alive(pid int, start uint64) bool {
	if !HaveStat() {
		return kill0(pid)
	}
	st, err := stat(pid)
	return err == nil && st.State != 'Z' && st.Start == start
}

// Ancestors is pid and every process above it, nearest first, as far as
// the OS tells: pid alone where it tells nothing. A tree deeper than
// maxDepth is cut there, so a corrupt ppid cannot loop.
func Ancestors(pid int) []int {
	out := []int{pid}
	for len(out) < maxDepth {
		p, err := Parent(out[len(out)-1])
		if err != nil || p <= 1 {
			break
		}
		out = append(out, p)
	}
	return out
}

const maxDepth = 64

// kill0 reports whether pid exists, by signalling it nothing.
func kill0(pid int) bool {
	err := unixKill(pid, 0)
	return err == nil || errors.Is(err, errPerm)
}
