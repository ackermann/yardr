package peer

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// Linux reports the peer's credentials through SO_PEERCRED, and a
// process's state, start time and parent in /proc/<pid>/stat.
func peerPID(fd int) (int, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(cred.Pid), nil
}

var procOnce = sync.OnceValue(func() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
})

// HaveStat is false where there is no /proc.
func HaveStat() bool { return procOnce() }

// stat reads /proc/<pid>/stat. The start time is in clock ticks after
// boot. A pid with no process is fs.ErrNotExist.
func stat(pid int) (Stat, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return Stat{}, err
	}
	// The command name may hold spaces and parentheses; fields resume after
	// the last ')'. State is field 3, ppid field 4, starttime field 22.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return Stat{}, fmt.Errorf("pid %d: unexpected stat %q", pid, b)
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return Stat{}, fmt.Errorf("pid %d: unexpected stat %q", pid, b)
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return Stat{}, fmt.Errorf("pid %d: ppid: %w", pid, err)
	}
	start, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return Stat{}, fmt.Errorf("pid %d: start time: %w", pid, err)
	}
	return Stat{State: f[0][0], Start: start, PPID: ppid}, nil
}
