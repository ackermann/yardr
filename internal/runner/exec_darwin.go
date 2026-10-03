package runner

import (
	"errors"
	"fmt"
	"io/fs"

	"golang.org/x/sys/unix"
)

// macOS has no /proc; the kernel answers the same questions through sysctl.
func haveProcStat() bool { return true }

// procStat reads state and start time from kern.proc.pid. The start time
// is microseconds since the epoch, as the kernel stamps it at fork. A pid
// with no process is fs.ErrNotExist: sysctl answers it with no record,
// which the wrapper reports as EIO.
func procStat(pid int) (stat, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, unix.EIO) || errors.Is(err, unix.ESRCH) {
		return stat{}, fmt.Errorf("pid %d: %w", pid, fs.ErrNotExist)
	}
	if err != nil {
		return stat{}, fmt.Errorf("pid %d: %w", pid, err)
	}
	p := k.Proc
	// p_stat: SIDL 1, SRUN 2, SSLEEP 3, SSTOP 4, SZOMB 5.
	state := byte('?')
	if p.P_stat >= 1 && p.P_stat <= 5 {
		state = "IRSTZ"[p.P_stat-1]
	}
	start := uint64(p.P_starttime.Sec)*1_000_000 + uint64(p.P_starttime.Usec)
	return stat{state: state, start: start}, nil
}
