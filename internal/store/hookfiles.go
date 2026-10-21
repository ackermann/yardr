package store

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"
)

// The files a hook delivery carries. The envelope names them by the
// source's own refs (its "files"), recorded with the event as they came;
// the receiver fetches the bytes after the delivery is kept and answered,
// and what it fetched is recorded here, beside the event, since an event
// is never changed once logged. One record per delivery, under the event's
// seq: each file as a blob the yard holds, or, for one it could not take,
// the reason where its hash would be (BlobRef.NotTaken), with the size the
// source gave. A reverse entry per blob, hash then seq, finds the delivery
// that names a blob without scanning every record (DeliveryBlob).
//
// A hook event's own "files" are the source's refs, never BlobRefs:
// EventFiles reads mail and quiet_failed alone, and these records are
// read here, so the two are never taken for each other.
var (
	bHookFiles     = []byte("hook_files")      // <seq>: the delivery's files, []BlobRef
	bHookFileBlobs = []byte("hook_file_blobs") // <hash>\x00<seq>: empty
)

func init() { allBuckets = append(allBuckets, bHookFiles, bHookFileBlobs) }

// RecordDeliveryFiles keeps what the receiver fetched of the hook event
// seq's files: each a blob the yard holds (put first, PutBlob), or one it
// did not take, with its reason in NotTaken and no hash. A delivery's
// files are recorded once: a second record is refused, as is one for an
// event that is no hook delivery, one naming a blob the yard does not
// hold, and one with a name no letter may carry (CheckBlobName). Like
// RecordHook it is not on the API: only the receiver, in the
// process that owns the store, records what a delivery carried.
func (s *Store) RecordDeliveryFiles(ctx context.Context, seq uint64, files []BlobRef) error {
	if len(files) > MaxMailFiles {
		return fmt.Errorf("a delivery carries at most %d files, not %d", MaxMailFiles, len(files))
	}
	files = slices.Clone(files)
	for i, f := range files {
		if err := CheckBlobName(f.Name); err != nil {
			return fmt.Errorf("delivery %d: %w", seq, err)
		}
		if f.NotTaken != "" {
			files[i].Hash, files[i].Type = "", ""
			continue
		}
		// The type and size are what the yard holds, as a mail's are.
		ref, err := s.Blob(f.Hash)
		if err != nil {
			return fmt.Errorf("delivery %d: %w", seq, err)
		}
		ref.Name = f.Name
		files[i] = *ref
	}
	return s.write(func(w *wtx) error {
		var e Event
		if ok, err := get(w.Tx, bEvents, seqKey(seq), &e); err != nil {
			return err
		} else if !ok || e.Kind != "hook" {
			return fmt.Errorf("event %d is no hook delivery: %w", seq, ErrNotFound)
		}
		k := seqKey(seq)
		if w.Bucket(bHookFiles).Get([]byte(k)) != nil {
			return fmt.Errorf("delivery %d has its files recorded already", seq)
		}
		if err := put(w.Tx, bHookFiles, k, files); err != nil {
			return err
		}
		for _, f := range files {
			if f.Hash != "" {
				if err := w.Bucket(bHookFileBlobs).Put([]byte(join(f.Hash, k)), []byte{}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// DeliveryFiles are the files the receiver fetched of the hook event seq,
// as RecordDeliveryFiles kept them; nil for a delivery whose files were
// not fetched (none carried, no credential to fetch with, a fetch the
// server's restart cut short) or that is gone with the log. The bracketed
// note of the envelope's text stands for those.
func (s *Store) DeliveryFiles(ctx context.Context, seq uint64) ([]BlobRef, error) {
	var out []BlobRef
	err := s.read(func(tx *bolt.Tx) error {
		_, err := get(tx, bHookFiles, seqKey(seq), &out)
		return err
	})
	return out, err
}

// DeliveryBlob is the blob hash as a device or the web view is served it
// when a hook delivery kept in the log names it, with the name that
// delivery gives it. ErrNotFound for one no kept delivery names, for one
// the yard no longer holds, and for a name that is no hash, refused as
// Blob refuses it.
//
// No viewer is checked, unlike MailBlob: a hook delivery reaches every
// device user already (it is the yard's, not one user's letter), and a
// hash is the SHA-256 of the bytes, which nobody without them can name.
func (s *Store) DeliveryBlob(ctx context.Context, hash string) (*BlobRef, error) {
	if !blobHash.MatchString(hash) {
		return nil, fmt.Errorf("file %q: %w", hash, ErrNotFound)
	}
	var name string
	named := false
	if err := s.read(func(tx *bolt.Tx) error {
		prefix := []byte(hash + "\x00")
		c := tx.Bucket(bHookFileBlobs).Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			var files []BlobRef
			if ok, err := get(tx, bHookFiles, string(k[len(prefix):]), &files); err != nil {
				return err
			} else if !ok {
				continue
			}
			for _, f := range files {
				if f.Hash == hash {
					name, named = f.Name, true
					return nil
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if !named {
		return nil, fmt.Errorf("file %s: %w", hash, ErrNotFound)
	}
	ref, err := s.Blob(hash)
	if err != nil {
		return nil, err
	}
	ref.Name = name
	return ref, nil
}

// pruneDeliveryFiles drops the files record of the event under key k, a
// delivery leaving the log, and its reverse entries: its blobs are then
// named by nothing of it, and pruneBlobs takes them when nothing else
// names them either.
func pruneDeliveryFiles(tx *bolt.Tx, k string) error {
	var files []BlobRef
	if ok, err := get(tx, bHookFiles, k, &files); err != nil || !ok {
		return err
	}
	for _, f := range files {
		if f.Hash != "" {
			if err := tx.Bucket(bHookFileBlobs).Delete([]byte(join(f.Hash, k))); err != nil {
				return err
			}
		}
	}
	return tx.Bucket(bHookFiles).Delete([]byte(k))
}
