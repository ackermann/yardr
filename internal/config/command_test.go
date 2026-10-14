package config

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/peer"
)

// A command's timeout kills its group, but only while its leader is still
// the command: a pid the kernel has handed to another process since is
// never signalled. A leader that is gone may still have a child in the
// group, so the signal goes ahead then. (yardr-4wfs: the kills after the
// command was reaped fired at whatever group had the pid next.)
func TestCommandTimeoutSignalsOnlyItsOwnGroup(t *testing.T) {
	for _, tc := range []struct {
		name      string
		baseline  func() (peer.Stat, error)
		current   func() (peer.Stat, error)
		wantKills int
	}{
		{
			name:      "a pid another process reused is never signalled",
			baseline:  func() (peer.Stat, error) { return peer.Stat{State: 'R', Start: 7}, nil },
			current:   func() (peer.Stat, error) { return peer.Stat{State: 'R', Start: 8}, nil },
			wantKills: 0,
		},
		{
			name:      "an unreadable process table is not a missing leader",
			baseline:  func() (peer.Stat, error) { return peer.Stat{State: 'R', Start: 7}, nil },
			current:   func() (peer.Stat, error) { return peer.Stat{}, errors.New("process table unavailable") },
			wantKills: 0,
		},
		{
			name:      "an unknown leader is never signalled",
			baseline:  func() (peer.Stat, error) { return peer.Stat{}, fs.ErrNotExist },
			current:   func() (peer.Stat, error) { return peer.Stat{State: 'R', Start: 8}, nil },
			wantKills: 0,
		},
		{
			name:      "a gone leader may still have a child: signalled",
			baseline:  func() (peer.Stat, error) { return peer.Stat{State: 'R', Start: 7}, nil },
			current:   func() (peer.Stat, error) { return peer.Stat{}, fs.ErrNotExist },
			wantKills: 3,
		},
		{
			name:      "the command's own leader is signalled",
			baseline:  func() (peer.Stat, error) { return peer.Stat{State: 'R', Start: 7}, nil },
			current:   func() (peer.Stat, error) { return peer.Stat{State: 'R', Start: 7}, nil },
			wantKills: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var kills []int
			c := Command{
				What: "test command", Argv: []string{"sleep", "0.2"},
				Dir: t.TempDir(), Timeout: 20 * time.Millisecond, Max: 1024,
				describe: func(pid int) (peer.Stat, error) {
					calls++
					if calls == 1 {
						return tc.baseline()
					}
					return tc.current()
				},
				kill: func(pid int, sig syscall.Signal) error {
					if sig != syscall.SIGKILL || pid >= 0 {
						t.Errorf("signal(%d, %v), want SIGKILL to the group", pid, sig)
					}
					kills = append(kills, pid)
					return nil
				},
			}
			_, err := c.Output(context.Background())
			if err == nil || !strings.Contains(err.Error(), "no answer within 20ms") {
				t.Fatalf("err = %v, want the timeout", err)
			}
			if len(kills) != tc.wantKills {
				t.Fatalf("kills = %d, want %d", len(kills), tc.wantKills)
			}
		})
	}
}
