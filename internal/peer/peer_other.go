//go:build !linux && !darwin

package peer

import (
	"errors"
	"fmt"
)

// Elsewhere the OS tells neither who connects nor what a process is: a
// call from a pane is checked by what the caller says of itself only, and
// liveness falls back to kill(0).
func peerPID(int) (int, error) { return 0, ErrUnknown }

func HaveStat() bool { return false }

func stat(pid int) (Stat, error) {
	return Stat{}, fmt.Errorf("pid %d: %w", pid, errors.ErrUnsupported)
}
