package store

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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
