package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// backupYard is a dispatcher on a store, in the store's home as the
// daemon is, with a clock the test moves.
func backupYard(t *testing.T) (store.API, *dispatch.Dispatcher, *time.Time) {
	t.Helper()
	home := t.TempDir()
	s := storetest.NewIn(t, home)
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{}, Home: home}
	now := time.Date(2026, 10, 13, 14, 0, 0, 0, time.UTC)
	dispatch.SetClock(d, func() time.Time { return now })
	return s, d, &now
}

// names are the files in dir, sorted; none for a directory that is not
// there.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func eventsOf(t *testing.T, s store.API, kind string) []store.Event {
	t.Helper()
	evs, err := s.EventsSince(context.Background(), 0, 0)
	must(t, err)
	return slices.DeleteFunc(evs, func(e store.Event) bool { return e.Kind != kind })
}

// A yard that did not ask keeps no copy. With backup_every set, the pass
// makes one in the home's backups, named by date and time, which opens as
// the store; the next is made once the period has gone by and not before.
func TestPassBacksUpTheStoreWhenDue(t *testing.T) {
	t.Parallel()
	s, d, now := backupYard(t)
	ctx := context.Background()
	dir := filepath.Join(d.Home, "backups")
	b, err := s.CreateBead(ctx, store.NewBead{Title: "kept"}, "me")
	must(t, err)

	must(t, d.Reconcile(ctx))
	must(t, s.SetSetting(ctx, store.BackupEverySetting, "0", "me"))
	must(t, d.Reconcile(ctx))
	if got := names(t, dir); len(got) != 0 {
		t.Fatalf("copies in a yard that asked for none: %v", got)
	}

	must(t, s.SetSetting(ctx, store.BackupEverySetting, "1d", "me"))
	must(t, d.Reconcile(ctx))
	first := "yard-20261013T140000Z.db"
	if got := names(t, dir); !slices.Equal(got, []string{first}) {
		t.Fatalf("after the first pass: %v, want %s", got, first)
	}
	if st, err := os.Stat(filepath.Join(dir, first)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the copy: %v %v", st, err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("the directory: %v %v", st, err)
	}
	if got, err := storetest.Open(t, filepath.Join(dir, first)).GetBead(ctx, b.ID); err != nil || got.Title != "kept" {
		t.Fatalf("bead in the copy = %+v, %v", got, err)
	}

	// Not before it is due, however many passes run, and a daemon that
	// starts anew goes by the copies that are there.
	*now = now.Add(24*time.Hour - time.Second)
	must(t, d.Reconcile(ctx))
	again := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{}, Home: d.Home}
	dispatch.SetClock(again, func() time.Time { return *now })
	must(t, again.Reconcile(ctx))
	if got := names(t, dir); !slices.Equal(got, []string{first}) {
		t.Fatalf("a copy before one was due: %v", got)
	}
	*now = now.Add(time.Second)
	must(t, again.Reconcile(ctx))
	if got := names(t, dir); !slices.Equal(got, []string{first, "yard-20261014T140000Z.db"}) {
		t.Fatalf("after a day: %v", got)
	}
	if evs := eventsOf(t, s, dispatch.BackupFailed); len(evs) != 0 {
		t.Fatalf("failures told: %+v", evs)
	}
}

// The daemon keeps backup_keep dated copies in backup_dir and removes its
// own older ones. A file there it did not name stays as it is: a copy made
// by hand, and one whose name only looks like a dated copy's.
func TestPassKeepsTheCountAndLeavesForeignFiles(t *testing.T) {
	t.Parallel()
	s, d, now := backupYard(t)
	ctx := context.Background()
	dir := t.TempDir()
	foreign := []string{"notes.txt", "yard-2026-10-01.db", "yard-20261001T000000Z.db.old", "yard.db.before-the-move"}
	for _, name := range foreign {
		must(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600))
	}
	// A directory with a dated copy's name is no copy of the daemon's.
	must(t, os.Mkdir(filepath.Join(dir, "yard-20260101T000000Z.db"), 0o700))
	must(t, s.SetSetting(ctx, store.BackupDirSetting, dir, "me"))
	must(t, s.SetSetting(ctx, store.BackupKeepSetting, "2", "me"))
	must(t, s.SetSetting(ctx, store.BackupEverySetting, "12h", "me"))

	for range 5 {
		must(t, d.Reconcile(ctx))
		*now = now.Add(12 * time.Hour)
	}
	want := []string{"notes.txt", "yard-20260101T000000Z.db", "yard-2026-10-01.db", "yard-20261001T000000Z.db.old",
		"yard-20261015T020000Z.db", "yard-20261015T140000Z.db", "yard.db.before-the-move"}
	slices.Sort(want)
	if got := names(t, dir); !slices.Equal(got, want) {
		t.Fatalf("the directory holds\n%v, want\n%v", got, want)
	}
	for _, name := range foreign {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != name {
			t.Fatalf("%s = %q, %v", name, got, err)
		}
	}
	if got := names(t, filepath.Join(d.Home, "backups")); len(got) != 0 {
		t.Fatalf("copies in the home with backup_dir set: %v", got)
	}
}

// A copy that cannot be made is told once, as an event of the class the
// yardmaster hears, however many passes fail after it. Once a copy is made
// the outage is over, and the next failure is told again.
func TestBackupFailureIsToldOnce(t *testing.T) {
	t.Parallel()
	s, d, now := backupYard(t)
	ctx := context.Background()
	if !slices.Contains(store.AttentionKinds, dispatch.BackupFailed) {
		t.Fatalf("%s is not of the class attention: %v", dispatch.BackupFailed, store.AttentionKinds)
	}
	gone := filepath.Join(t.TempDir(), "volume")
	dir := filepath.Join(gone, "yard")
	must(t, s.SetSetting(ctx, store.BackupDirSetting, dir, "me"))
	must(t, s.SetSetting(ctx, store.BackupEverySetting, "1h", "me"))

	for range 6 {
		must(t, d.Reconcile(ctx))
		*now = now.Add(2 * time.Hour)
	}
	evs := eventsOf(t, s, dispatch.BackupFailed)
	if len(evs) != 1 || evs[0].Data["dir"] != dir || evs[0].Data["reason"] == "" || evs[0].Data["every"] != "1h" {
		t.Fatalf("backup_failed events = %+v, want one for %s", evs, dir)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatalf("the directory's parent was made: %v", err)
	}

	// The volume is back: the copy that is overdue is made.
	must(t, os.Mkdir(gone, 0o700))
	must(t, d.Reconcile(ctx))
	if got := names(t, dir); len(got) != 1 {
		t.Fatalf("copies once the directory can be made: %v", got)
	}
	// Gone again: told again, once.
	must(t, os.RemoveAll(gone))
	for range 3 {
		*now = now.Add(2 * time.Hour)
		must(t, d.Reconcile(ctx))
	}
	if evs := eventsOf(t, s, dispatch.BackupFailed); len(evs) != 2 {
		t.Fatalf("backup_failed events after a second outage = %d, want 2", len(evs))
	}
}

// A copy that failed is not tried again on the next pass: a full disk is
// written to once per wait, which doubles.
func TestFailedBackupWaitsBeforeTheNextTry(t *testing.T) {
	t.Parallel()
	s, d, now := backupYard(t)
	ctx := context.Background()
	gone := filepath.Join(t.TempDir(), "volume")
	must(t, s.SetSetting(ctx, store.BackupDirSetting, filepath.Join(gone, "yard"), "me"))
	must(t, s.SetSetting(ctx, store.BackupEverySetting, "1h", "me"))
	d.Backoff = time.Minute
	must(t, d.Reconcile(ctx))

	must(t, os.Mkdir(gone, 0o700))
	*now = now.Add(59 * time.Second)
	must(t, d.Reconcile(ctx))
	if got := names(t, gone); len(got) != 0 {
		t.Fatalf("tried again within the wait: %v", got)
	}
	*now = now.Add(time.Second)
	must(t, d.Reconcile(ctx))
	if got := names(t, filepath.Join(gone, "yard")); len(got) != 1 {
		t.Fatalf("copies after the wait: %v", got)
	}
}

// Changing the backup directory during an outage changes the target, so the
// old target's retry delay does not suppress the new target's first attempt.
func TestBackupDirectoryChangeStartsANewOutage(t *testing.T) {
	t.Parallel()
	s, d, _ := backupYard(t)
	ctx := context.Background()
	first := filepath.Join(t.TempDir(), "missing", "first")
	second := filepath.Join(t.TempDir(), "missing", "second")
	d.Backoff = time.Hour
	must(t, s.SetSetting(ctx, store.BackupDirSetting, first, "me"))
	must(t, s.SetSetting(ctx, store.BackupEverySetting, "1h", "me"))
	must(t, d.Reconcile(ctx))
	if got := eventsOf(t, s, dispatch.BackupFailed); len(got) != 1 || got[0].Data["dir"] != first {
		t.Fatalf("first outage events = %+v", got)
	}

	must(t, s.SetSetting(ctx, store.BackupDirSetting, second, "me"))
	must(t, d.Reconcile(ctx))
	got := eventsOf(t, s, dispatch.BackupFailed)
	if len(got) != 2 || got[1].Data["dir"] != second {
		t.Fatalf("directory change did not start a new outage immediately: %+v", got)
	}
}
