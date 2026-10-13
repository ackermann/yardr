package store

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// BackupResult is the backup file written, its size and the last event it
// holds.
type BackupResult struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	Head  uint64 `json:"head"`
}

// Backup writes a consistent copy of the store to path while it keeps
// serving: one read transaction streams every page (bbolt's Tx.WriteTo), so
// writers carry on and the copy is the store as of one commit. path is
// written by the process that owns the store, so it must be absolute; the
// copy goes to a temporary file beside it and is renamed into place only
// once complete, so an interrupted backup never leaves a torn file at path.
func (s *Store) Backup(ctx context.Context, path string) (*BackupResult, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("backup path %q is not absolute", path)
	}
	if same, err := sameFile(path, s.db.Path()); err != nil {
		return nil, err
	} else if same {
		return nil, fmt.Errorf("backup path %s is the store itself", path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	res := &BackupResult{Path: path}
	err = s.read(func(tx *bolt.Tx) error {
		if k, _ := tx.Bucket(bEvents).Cursor().Last(); k != nil {
			res.Head = binary.BigEndian.Uint64(k)
		}
		n, err := tx.WriteTo(tmp)
		res.Bytes = n
		return err
	})
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, err
	}
	return res, nil
}

// sameFile reports whether a and b name the same existing file.
func sameFile(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(sa, sb), nil
}

// The settings by which the daemon keeps dated copies of the store.
const (
	// BackupEverySetting is how often a copy is made; unset or 0, never.
	BackupEverySetting = "backup_every"
	// BackupKeepSetting is how many dated copies are kept.
	BackupKeepSetting = "backup_keep"
	// BackupDirSetting is the directory they are kept in.
	BackupDirSetting = "backup_dir"

	DefaultBackupKeep = 7
)

// BackupPlan is what the yard's settings ask of the daemon: a dated copy of
// the store every Every (0: none), the last Keep of them kept, in Dir.
type BackupPlan struct {
	Every time.Duration `json:"-"`
	Keep  int           `json:"keep"`
	Dir   string        `json:"dir"`
}

// BackupPlanOf reads the plan from settings, for the yard whose home is
// home: unset, the directory is <home>/backups.
func BackupPlanOf(settings map[string]string, home string) BackupPlan {
	p := BackupPlan{Keep: DefaultBackupKeep, Dir: settings[BackupDirSetting]}
	// "0" is no span, and reads as the setting unset does: never.
	if d, err := ParseSpan(settings[BackupEverySetting]); err == nil {
		p.Every = d
	}
	if n, err := strconv.Atoi(settings[BackupKeepSetting]); err == nil && n > 0 {
		p.Keep = n
	}
	if p.Dir == "" && home != "" {
		p.Dir = filepath.Join(home, "backups")
	}
	return p
}

// A dated copy is yard-<UTC date and time>.db. The name is what marks a
// file as one the daemon made and may remove: a copy made by hand beside
// them (yard.db.before-the-move) has another and is never touched.
const (
	backupPrefix = "yard-"
	backupSuffix = ".db"
	backupTime   = "20060102T150405Z"
)

// BackupName is the file name of the dated copy made at t.
func BackupName(t time.Time) string {
	return backupPrefix + t.UTC().Format(backupTime) + backupSuffix
}

// DatedBackup is one dated copy: its file and the time its name says.
type DatedBackup struct {
	Path string    `json:"path"`
	At   time.Time `json:"at"`
}

// DatedBackups are the dated copies in dir, oldest first: the regular
// files whose name is exactly a BackupName. A directory that is not there
// holds none.
func DatedBackups(dir string) ([]DatedBackup, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []DatedBackup
	for _, e := range entries {
		name := e.Name()
		stamp, ok := strings.CutPrefix(name, backupPrefix)
		if stamp, ok = strings.CutSuffix(stamp, backupSuffix); !ok || !e.Type().IsRegular() {
			continue
		}
		// Held to the name BackupName gives, so nothing that only looks
		// like one (yard-2026.db, a date written another way) is taken.
		at, err := time.Parse(backupTime, stamp)
		if err != nil || BackupName(at) != name {
			continue
		}
		out = append(out, DatedBackup{Path: filepath.Join(dir, name), At: at})
	}
	slices.SortFunc(out, func(a, b DatedBackup) int { return a.At.Compare(b.At) })
	return out, nil
}
