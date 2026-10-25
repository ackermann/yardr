package server

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// watchYard is a yard with the operator mckean, who is a user, and a home
// for the daemon's stamp and lock.
func watchYard(t *testing.T) (*store.Store, string) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(ctx) })
	if err := st.SetSetting(ctx, store.OperatorSetting, "mckean", "mckean"); err != nil {
		t.Fatal(err)
	}
	return st, home
}

func watchLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func lostEvents(t *testing.T, st *store.Store) []store.Event {
	t.Helper()
	evs, err := st.EventsOfKind(context.Background(), "daemon_lost", 10)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

// A last pass within the threshold is no loss: one look writes nothing,
// at the threshold to the second.
func TestDaemonWatchOnceFresh(t *testing.T) {
	st, home := watchYard(t)
	ctx := context.Background()
	now := time.Now()
	for _, at := range []time.Time{now.Add(-time.Minute), now.Add(-daemonLostAfter)} {
		if err := config.WritePassStamp(home, config.PassStamp{Build: "test", At: at}); err != nil {
			t.Fatal(err)
		}
		if wrote, err := daemonWatchOnce(ctx, st, home, now, watchLog()); err != nil || wrote {
			t.Fatalf("one look at a pass of %s = %v, %v, want nothing written", at, wrote, err)
		}
	}
	if evs := lostEvents(t, st); len(evs) != 0 {
		t.Fatalf("daemon_lost events = %d", len(evs))
	}
}

// A last pass over three minutes old with no daemon holding the lock is
// one loss: one look writes the event and raises the yard's run card for
// up, and the next looks write nothing more.
func TestDaemonWatchOnceLost(t *testing.T) {
	st, home := watchYard(t)
	ctx := context.Background()
	now := time.Now()
	stamp := now.Add(-daemonLostAfter - time.Minute)
	if err := config.WritePassStamp(home, config.PassStamp{Build: "test", At: stamp}); err != nil {
		t.Fatal(err)
	}
	wrote, err := daemonWatchOnce(ctx, st, home, now, watchLog())
	if err != nil || !wrote {
		t.Fatalf("one look = %v, %v, want the loss written", wrote, err)
	}
	if evs := lostEvents(t, st); len(evs) != 1 {
		t.Fatalf("daemon_lost events = %d, want the one", len(evs))
	}
	beads, err := st.ListBeads(ctx, store.BeadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var card bool
	for _, b := range beads {
		if !strings.Contains(b.Title, "was last alive at") {
			continue
		}
		notes, err := st.Notes(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range store.ResolveAsks(notes, now) {
			if r.Ask.Run != nil && len(r.Ask.Run.Argv) == 1 && r.Ask.Run.Argv[0] == "up" {
				card = true
			}
		}
	}
	if !card {
		t.Fatal("no bead carries the yard's run card for up")
	}
	wrote, err = daemonWatchOnce(ctx, st, home, now.Add(daemonWatchEvery), watchLog())
	if err != nil || wrote {
		t.Fatalf("the next look = %v, %v, want nothing more", wrote, err)
	}
	if evs := lostEvents(t, st); len(evs) != 1 {
		t.Fatalf("daemon_lost events = %d, want the one", len(evs))
	}
}

// A daemon that holds the lock is starting or mid-pass, not lost,
// however old its last pass reads: one look writes nothing.
func TestDaemonWatchOnceHeld(t *testing.T) {
	st, home := watchYard(t)
	ctx := context.Background()
	now := time.Now()
	if err := config.WritePassStamp(home, config.PassStamp{Build: "test", At: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	lock := config.At(home).DaemonLock()
	f, err := os.OpenFile(lock, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if wrote, err := daemonWatchOnce(ctx, st, home, now, watchLog()); err != nil || wrote {
		t.Fatalf("one look = %v, %v, want nothing written", wrote, err)
	}
	if evs := lostEvents(t, st); len(evs) != 0 {
		t.Fatalf("daemon_lost events = %d", len(evs))
	}
}

// A home whose daemon never passed and never started raises nothing: a
// yard that never ran must not wake with a card for it.
func TestDaemonWatchOnceNoStamp(t *testing.T) {
	st, home := watchYard(t)
	wrote, err := daemonWatchOnce(context.Background(), st, home, time.Now(), watchLog())
	if err != nil || wrote {
		t.Fatalf("one look = %v, %v, want nothing written", wrote, err)
	}
	if evs := lostEvents(t, st); len(evs) != 0 {
		t.Fatalf("daemon_lost events = %d", len(evs))
	}
}

// A home whose daemon never passed but started over three minutes ago
// reads stale too: it died before its first pass. The loss dates from
// its start.
func TestDaemonWatchOnceNoStampOldStart(t *testing.T) {
	st, home := watchYard(t)
	ctx := context.Background()
	if err := st.LogDispatch(ctx, "", "daemon_started", "daemon", map[string]any{"to": "test"}); err != nil {
		t.Fatal(err)
	}
	wrote, err := daemonWatchOnce(ctx, st, home, time.Now().Add(daemonLostAfter+time.Minute), watchLog())
	if err != nil || !wrote {
		t.Fatalf("one look = %v, %v, want the loss written", wrote, err)
	}
	if evs := lostEvents(t, st); len(evs) != 1 {
		t.Fatalf("daemon_lost events = %d, want the one", len(evs))
	}
}

// A daemon that started within the threshold never reads stale,
// however little it has passed: it is starting, not lost.
func TestDaemonWatchOnceStarting(t *testing.T) {
	st, home := watchYard(t)
	ctx := context.Background()
	now := time.Now()
	if err := config.WritePassStamp(home, config.PassStamp{Build: "test", At: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := st.LogDispatch(ctx, "", "daemon_started", "daemon", map[string]any{"to": "test"}); err != nil {
		t.Fatal(err)
	}
	if wrote, err := daemonWatchOnce(ctx, st, home, now.Add(time.Minute), watchLog()); err != nil || wrote {
		t.Fatalf("one look = %v, %v, want nothing written", wrote, err)
	}
	if evs := lostEvents(t, st); len(evs) != 0 {
		t.Fatalf("daemon_lost events = %d", len(evs))
	}
}

// The staleness rule, of times alone: a pass over three minutes old is
// stale, else the start it never passed from; a yard with neither, a
// pass or a start at the threshold, and a fresh one never are.
func TestDaemonStale(t *testing.T) {
	now := time.Date(2026, 10, 25, 15, 0, 0, 0, time.UTC)
	old := now.Add(-daemonLostAfter - time.Minute)
	edge := now.Add(-daemonLostAfter)
	fresh := now.Add(-time.Minute)
	for _, c := range []struct {
		name         string
		stamp, start *time.Time
		want         bool
	}{
		{"old pass", &old, nil, true},
		{"pass at the threshold", &edge, nil, false},
		{"fresh pass", &fresh, nil, false},
		{"fresh pass over an old start", &fresh, &old, false},
		{"old pass before a fresh start", &old, &fresh, false},
		{"no pass, old start", nil, &old, true},
		{"no pass, start at the threshold", nil, &edge, false},
		{"no pass, fresh start", nil, &fresh, false},
		{"neither", nil, nil, false},
	} {
		if got := daemonStale(now, c.stamp, c.start); got != c.want {
			t.Errorf("%s: stale = %v, want %v", c.name, got, c.want)
		}
	}
}
