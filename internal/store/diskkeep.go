package store

import (
	"context"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

// DiskKept is the event a landing logs after keeping the machine's disk:
// what free space it found and left, what it rotated and what it removed.
// It is about no bead, and matches a yardr subscription on kind alone.
const DiskKept = "disk_kept"

// DiskKeepGBSetting is the gigabytes of free space on GOCACHE's volume
// below which a landing keeps the disk before its tests run: it rotates
// GOCACHE at a fresh sibling directory and clears stale go-tmp.
const DiskKeepGBSetting = "disk_keep_gb"

// DefaultDiskKeepGB is the threshold while the setting is unset: 30 GB of
// free space, about two hours of build cache at the 12 GB an hour the
// yard's machine grew under load in Oct 2026, so a landing acts long
// before the disk fills.
const DefaultDiskKeepGB = 30

// DiskKeepGB is the disk_keep_gb of settings: the gigabytes of free space
// below which a landing keeps the disk. An unset, unreadable or negative
// value reads as the default; 0 turns the keeping off.
func DiskKeepGB(settings map[string]string) int {
	v, ok := settings[DiskKeepGBSetting]
	if !ok || v == "" {
		return DefaultDiskKeepGB
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return DefaultDiskKeepGB
	}
	return n
}

// bGOCACHEOld holds the GOCACHE directories rotations moved away from and
// that no landing has removed yet: <directory> to the rotation's time. A
// directory is removed once it has stood aside for 30 minutes, so no build
// still writing to it loses files from under it.
var bGOCACHEOld = []byte("gocache_old")

func init() { allBuckets = append(allBuckets, bGOCACHEOld) }

// GOCACHEDir is a GOCACHE directory a landing rotated away from, waiting
// out the grace before another landing removes it.
type GOCACHEDir struct {
	Dir string    `json:"dir"`
	At  time.Time `json:"at"`
}

// RecordGOCACHEOld records dir as rotated away from at at, to be removed
// by a landing once the grace has passed.
func (s *Store) RecordGOCACHEOld(ctx context.Context, dir string, at time.Time) error {
	return s.write(func(w *wtx) error {
		return put(w.Tx, bGOCACHEOld, dir, at)
	})
}

// GOCACHEOldDirs lists the recorded directories.
func (s *Store) GOCACHEOldDirs(ctx context.Context) ([]GOCACHEDir, error) {
	var out []GOCACHEDir
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bGOCACHEOld, "", func(k []byte, v *time.Time) error {
			out = append(out, GOCACHEDir{Dir: string(k), At: *v})
			return nil
		})
	})
	return out, err
}

// RemoveGOCACHEOld drops dir from the recorded directories, after it was
// removed from the disk. A directory that was never recorded is no error:
// the record and the disk meet again on the next landing.
func (s *Store) RemoveGOCACHEOld(ctx context.Context, dir string) error {
	return s.write(func(w *wtx) error {
		return w.Bucket(bGOCACHEOld).Delete([]byte(dir))
	})
}
