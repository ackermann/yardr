package peer

import (
	"errors"
	"fmt"
	"io/fs"

	"golang.org/x/sys/unix"
)

// macOS reports the peer's pid through LOCAL_PEERPID and, having no /proc,
// a process's state, start time and parent through kern.proc.pid.
func peerPID(fd int) (int, error) {
	return unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
}

func HaveStat() bool { return true }

// stat reads kern.proc.pid. The start time is microseconds since the
// epoch, as the kernel stamps it at fork. A pid with no process is
// fs.ErrNotExist: sysctl answers it with no record, which the wrapper
// reports as EIO.
func stat(pid int) (Stat, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, unix.EIO) || errors.Is(err, unix.ESRCH) {
		return Stat{}, fmt.Errorf("pid %d: %w", pid, fs.ErrNotExist)
	}
	if err != nil {
		return Stat{}, fmt.Errorf("pid %d: %w", pid, err)
	}
	p := k.Proc
	// p_stat: SIDL 1, SRUN 2, SSLEEP 3, SSTOP 4, SZOMB 5.
	state := byte('?')
	if p.P_stat >= 1 && p.P_stat <= 5 {
		state = "IRSTZ"[p.P_stat-1]
	}
	start := uint64(p.P_starttime.Sec)*1_000_000 + uint64(p.P_starttime.Usec)
	return Stat{State: state, Start: start, PPID: int(k.Eproc.Ppid)}, nil
}
