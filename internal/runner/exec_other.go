//go:build !linux && !darwin

package runner

import (
	"errors"
	"fmt"
)

// Elsewhere nothing reads a process's state or start time; liveness falls
// back to kill(0), and a handle's pid is trusted.
func haveProcStat() bool { return false }

func procStat(pid int) (stat, error) {
	return stat{}, fmt.Errorf("pid %d: %w", pid, errors.ErrUnsupported)
}
