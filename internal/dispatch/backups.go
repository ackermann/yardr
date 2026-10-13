package dispatch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"yardr.dev/yardr/internal/store"
)

// BackupFailed is the event of a dated copy of the store that could not be
// made or kept to its count, logged once per outage; it is of the class
// attention, so the yardmaster hears of it.
const BackupFailed = "backup_failed"

// backups keeps the dated copies of the store the yard's settings ask for
// (store.BackupPlanOf, read on every pass, so a change holds on the next):
// when the newest is older than backup_every, or there is none, the server
// writes one more (Store.Backup, as yardr store backup does) and the
// daemon's own copies beyond backup_keep are removed, oldest first.
//
// When the last copy was made is read from the names in the directory, so
// a daemon that was restarted, or did not run for a while, goes by the
// copies that are there and makes the one that is overdue on its first
// pass.
//
// A copy that fails is told once, as backup_failed, and tried again after
// Backoff, doubling up to BackoffMax: a full disk is not written to on
// every pass. The next copy that is made ends the outage.
func (d *Dispatcher) backups(ctx context.Context) {
	settings, err := d.Store.Settings(ctx)
	if err != nil {
		d.Log.Warn("backups: reading the settings", "err", err)
		return
	}
	plan := store.BackupPlanOf(settings, d.Home)
	if d.backupDown != nil && d.backupDownDir != plan.Dir {
		d.backupDown = nil
		d.backupDownDir = ""
	}
	if plan.Every <= 0 {
		d.backupDown = nil
		d.backupDownDir = ""
		return
	}
	now := d.now()
	if o := d.backupDown; o != nil && now.Before(o.until) {
		return
	}
	made, err := d.backup(ctx, plan, now)
	switch {
	case err == nil:
		if made != "" {
			d.Log.Info("store backed up", "path", made, "keep", plan.Keep)
			d.backupDown = nil
			d.backupDownDir = ""
		}
	case ctx.Err() != nil:
		// The daemon was told to stop: the next one makes the copy.
	default:
		o := d.backupDown
		if o == nil {
			o = &outage{delay: d.Backoff}
			d.backupDown = o
			d.backupDownDir = plan.Dir
			d.Log.Warn("store backup failed", "dir", plan.Dir, "err", err)
			if lerr := d.Store.LogDispatch(ctx, "", BackupFailed, actor, map[string]any{
				"dir": plan.Dir, "every": store.Span(plan.Every), "reason": err.Error()}); lerr != nil {
				d.Log.Warn("backup_failed event", "err", lerr)
			}
		} else {
			o.delay = min(2*o.delay, d.BackoffMax)
		}
		o.until = now.Add(o.delay)
	}
}

// backup makes the copy of plan that is due at now and returns its path,
// "" when none is due yet.
func (d *Dispatcher) backup(ctx context.Context, plan store.BackupPlan, now time.Time) (string, error) {
	copies, err := store.DatedBackups(plan.Dir)
	if err != nil {
		return "", err
	}
	if n := len(copies); n > 0 && now.Sub(copies[n-1].At) < plan.Every {
		return "", nil
	}
	// The directory itself is made, as the home's own is on a yard that
	// never had one; what it is in is not. A directory on a volume that is
	// not mounted is a failure to tell, not a place to make on the disk
	// beneath it.
	if err := os.Mkdir(plan.Dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	res, err := d.Store.Backup(ctx, filepath.Join(plan.Dir, store.BackupName(now)))
	if err != nil {
		return "", err
	}
	// Listed again: the copy just made is one of them, or stands in for
	// one of the same second.
	if copies, err = store.DatedBackups(plan.Dir); err != nil {
		return "", err
	}
	var failed []error
	for _, c := range copies[:max(len(copies)-plan.Keep, 0)] {
		if err := os.Remove(c.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failed = append(failed, err)
		}
	}
	if len(failed) > 0 {
		return "", fmt.Errorf("%s was made, and older copies beyond the %d kept could not be removed: %w", res.Path, plan.Keep, errors.Join(failed...))
	}
	return res.Path, nil
}
