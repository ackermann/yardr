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

// A command that a deadline ended says whose it was. Its Timeout runs on a
// child of the caller's context, so either deadline ends it the same way and
// only the text tells them apart (yardr-qpqy: the daemon's push log named a
// command's 30 s Timeout for a caller's 10 s wait). In the two rows that
// run the command the deadline that ends it is 20 ms away and the other an
// hour, so how fast the machine is cannot make it the other one.
func TestCommandSaysWhoseDeadlineEndedIt(t *testing.T) {
	const soon, hour = 20 * time.Millisecond, time.Hour
	within := func(d time.Duration) func() (context.Context, context.CancelFunc) {
		return func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), d)
		}
	}
	gaveUp := func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, cancel
	}
	for _, tc := range []struct {
		name    string
		caller  func() (context.Context, context.CancelFunc)
		timeout time.Duration
		want    string
	}{
		{"the caller's deadline is the nearer", within(soon), hour, "test command: no answer before the caller's deadline"},
		{"its own timeout is the nearer", within(hour), soon, "test command: no answer within 20ms"},
		// Nothing runs in the last two: the caller's context is over when the
		// command is asked for. With both deadlines past the caller's is said,
		// and a caller that gave up is told so, as before.
		{"both are past before it runs", within(-time.Second), time.Nanosecond, "test command: no answer before the caller's deadline"},
		{"the caller gave up", gaveUp, hour, "test command: context canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.caller()
			defer cancel()
			c := Command{What: "test command", Argv: []string{"sleep", "300"}, Dir: t.TempDir(), Timeout: tc.timeout, Max: 1024}
			if _, err := c.Output(ctx); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
