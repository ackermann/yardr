package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// A blob is a file mail carries: an image, for now. The yard keeps it in
// its home under BlobsDir, in a file named by the SHA-256 of its bytes, so
// the same picture sent twice is one file and a letter names it by what it
// is. A blob is written once and never changed. The log has only the name
// (the files of a mail event), so the store file does not grow by the
// picture; a blob no letter names any more goes with Prune, and nothing
// else removes one.
//
// The file has no record beside it: its type is read from its first bytes
// each time it is asked for, as it was when it was put.

// BlobsDir is the directory of the blobs, in the yard's home.
const BlobsDir = "blobs"

const (
	// BlobMaxSetting is the most bytes one blob may have, and
	// DefaultBlobMax what a yard without the setting takes.
	BlobMaxSetting = "blob_max_bytes"
	DefaultBlobMax = 8 << 20
	// MaxMailFiles bounds the files of one mail.
	MaxMailFiles = 8
	// BlobGrace is how long Prune leaves a blob that no letter names: the
	// mail that will name it is sent after it was put, a paste some time
	// before the line it goes with.
	BlobGrace = time.Hour
)

// BlobTypes are the types a blob may have, as its bytes say.
var BlobTypes = []string{"image/png", "image/jpeg"}

// ErrNoPeerFiles refuses mail with files to another yard: a link carries
// no file until file is a kind it can be allowed.
var ErrNoPeerFiles = errors.New("images do not cross the link yet")

// BlobRef names a blob where a letter carries it: the SHA-256 of its bytes
// in hex, which is its file's name, the type its bytes say, and how many
// they are.
type BlobRef struct {
	Hash  string `json:"hash"`
	Type  string `json:"type"`
	Bytes int64  `json:"bytes"`
}

// blobHash is what names a blob, and so all a path under BlobsDir is made
// of.
var blobHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// BlobPath is the file of the blob hash in the yard at home.
func BlobPath(home, hash string) string { return filepath.Join(home, BlobsDir, hash) }

// BlobPath is the file of the blob hash in this yard.
func (s *Store) BlobPath(hash string) string { return BlobPath(s.home, hash) }

// FilesLine says the files of a letter where mail is shown, each by the
// path of its blob in the yard at home, so a crew member reads the picture
// with what its harness reads any file with: "1 image: <path>". "" for a
// letter without files.
func FilesLine(home string, files []BlobRef) string {
	if len(files) == 0 {
		return ""
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = BlobPath(home, f.Hash)
	}
	noun := "image"
	if len(files) > 1 {
		noun = "images"
	}
	return fmt.Sprintf("%d %s: %s", len(files), noun, strings.Join(paths, " "))
}

// WithFiles is the text of a letter as one line shows it: the text, then
// its files (FilesLine).
func WithFiles(text, home string, files []BlobRef) string {
	line := FilesLine(home, files)
	switch {
	case line == "":
		return text
	case text == "":
		return line
	}
	return text + "  " + line
}

// MailFiles are the files the mail e carries, nil for mail without any.
func MailFiles(e Event) []BlobRef {
	raw, ok := e.Data["files"]
	if !ok || e.Kind != "mail" {
		return nil
	}
	// As the log holds them: decoded, a list of objects.
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out []BlobRef
	if json.Unmarshal(b, &out) != nil || len(out) == 0 {
		return nil
	}
	return out
}

// blobType is the type the bytes say, and whether a blob may have it.
func blobType(head []byte) (string, bool) {
	typ := http.DetectContentType(head)
	return typ, slices.Contains(BlobTypes, typ)
}

// blobMax is the yard's cap on one blob; a setting that does not parse
// counts as unset.
func (s *Store) blobMax() (limit int64, err error) {
	limit = DefaultBlobMax
	err = s.read(func(tx *bolt.Tx) error {
		var v string
		if ok, err := get(tx, bSettings, BlobMaxSetting, &v); err != nil {
			return err
		} else if ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				limit = n
			}
		}
		return nil
	})
	return limit, err
}

// PutBlob keeps data as a blob and answers its name. The type is what the
// bytes say, never what the caller does, and anything but BlobTypes is
// refused, as is more than the yard's cap (BlobMaxSetting); nothing is
// stored then. Bytes the yard has already are not written again: the
// answer is the same, and the file is only marked as put now, so that a
// blob no letter names yet is not pruned before the mail it was put for is
// sent (BlobGrace).
func (s *Store) PutBlob(ctx context.Context, data []byte) (*BlobRef, error) {
	limit, err := s.blobMax()
	if err != nil {
		return nil, err
	}
	typ, ok := blobType(data)
	switch {
	case int64(len(data)) > limit:
		return nil, fmt.Errorf("the file is %d bytes, over the yard's limit of %d (yardr settings set %s <bytes>)", len(data), limit, BlobMaxSetting)
	case !ok:
		return nil, fmt.Errorf("the file is %s: mail carries only %s", typ, strings.Join(BlobTypes, " and "))
	}
	sum := sha256.Sum256(data)
	ref := &BlobRef{Hash: hex.EncodeToString(sum[:]), Type: typ, Bytes: int64(len(data))}
	path := s.BlobPath(ref.Hash)
	if st, err := os.Stat(path); err == nil && st.Size() == ref.Bytes {
		at := time.Now()
		return ref, os.Chtimes(path, at, at)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Whole or not at all: a reader never finds half a picture under the
	// name of all of it.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".put-*")
	if err != nil {
		return nil, err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	return ref, nil
}

// Blob is the blob hash as the yard holds it; ErrNotFound for one it does
// not, and for a name that is no hash.
func (s *Store) Blob(hash string) (*BlobRef, error) {
	if !blobHash.MatchString(hash) {
		return nil, fmt.Errorf("image %q: %w", hash, ErrNotFound)
	}
	f, err := os.Open(s.BlobPath(hash))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("image %s: %w", hash, ErrNotFound)
	} else if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	// A file someone else left in the directory is no blob of the yard.
	typ, ok := blobType(head[:n])
	if !ok {
		return nil, fmt.Errorf("image %s: %w", hash, ErrNotFound)
	}
	return &BlobRef{Hash: hash, Type: typ, Bytes: st.Size()}, nil
}

// mailFiles are the blobs the hashes name, as a mail event carries them.
// One the yard does not hold refuses the mail: a letter never names a
// picture nobody can read.
func (s *Store) mailFiles(hashes []string) ([]BlobRef, error) {
	if len(hashes) > MaxMailFiles {
		return nil, fmt.Errorf("mail carries at most %d images, not %d", MaxMailFiles, len(hashes))
	}
	var out []BlobRef
	for _, h := range hashes {
		ref, err := s.Blob(h)
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: the yard does not hold it; put it first (PutBlob, or yardr mail --file)", err)
		} else if err != nil {
			return nil, err
		}
		out = append(out, *ref)
	}
	return out, nil
}

// pruneBlobs removes the blobs no letter of the log names, and answers how
// many. One put within BlobGrace stays, named or not: its mail may be on
// its way. So does a put that did not finish, for as long.
func (s *Store) pruneBlobs() (int, error) {
	dir := filepath.Join(s.home, BlobsDir)
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) || len(files) == 0 {
		return 0, nil
	} else if err != nil {
		return 0, err
	}
	named := map[string]bool{}
	err = s.read(func(tx *bolt.Tx) error {
		prefix := []byte("mail\x00")
		c := tx.Bucket(bEventsKd).Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			var e Event
			if _, err := get(tx, bEvents, string(k[len(prefix):]), &e); err != nil {
				return err
			}
			for _, f := range MailFiles(e) {
				named[f.Hash] = true
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	n, young := 0, now().Add(-BlobGrace)
	for _, f := range files {
		// Asked now, after the letters were read: a blob put again since
		// is young again.
		st, err := f.Info()
		if err != nil || f.IsDir() || named[f.Name()] || !st.ModTime().Before(young) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, f.Name())); err != nil {
			return n, err
		}
		if blobHash.MatchString(f.Name()) {
			n++
		}
	}
	return n, nil
}
