package runner

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

var procOnce = sync.OnceValue(func() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
})

// haveProcStat is false where there is no /proc; liveness then falls back
// to kill(0).
func haveProcStat() bool { return procOnce() }

// procStat reads state and start time from /proc/<pid>/stat. The start
// time is in clock ticks after boot. A pid with no process is
// fs.ErrNotExist.
func procStat(pid int) (stat, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return stat{}, err
	}
	// The command name may hold spaces and parentheses; fields resume after
	// the last ')'. State is field 3, starttime field 22.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return stat{}, fmt.Errorf("pid %d: unexpected stat %q", pid, b)
	}
	f := strings.Fields(string(b[i+1:]))
	if len(f) < 20 {
		return stat{}, fmt.Errorf("pid %d: unexpected stat %q", pid, b)
	}
	start, err := strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return stat{}, fmt.Errorf("pid %d: start time: %w", pid, err)
	}
	return stat{state: f[0][0], start: start}, nil
}
