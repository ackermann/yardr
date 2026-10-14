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
	"unicode"

	bolt "go.etcd.io/bbolt"
)

// A blob is a file mail or a bead's note carries: an image, for now. The
// yard keeps it in its home under BlobsDir, in a file named by the SHA-256
// of its bytes, so the same picture sent twice is one file and a letter
// names it by what it is. A blob is written once and never changed. The
// log has only the name (the files of a mail event, of a note its row), so
// the store file does not grow by the picture; a blob nothing names any
// more goes with Prune, and nothing else removes one.
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
	// MaxNoteFiles bounds the files of one note, as MaxMailFiles bounds a
	// mail's.
	MaxNoteFiles = MaxMailFiles
	// BlobGrace is how long Prune leaves a blob that nothing names: the
	// mail or note that will name it is sent after it was put, a paste
	// some time before the line it goes with.
	BlobGrace = time.Hour
)

// BlobTypes are the types a blob may have, as its bytes say.
var BlobTypes = []string{"image/png", "image/jpeg"}

// BlobRef names a blob where a letter carries it: the SHA-256 of its bytes
// in hex, which is its file's name, the type its bytes say, and how many
// they are.
//
// NotTaken is set on a file of mail from another yard that this yard did
// not take, and says why (peerfiles.go): the yard holds no blob for it,
// and hash, type and bytes are the sender's word. Where a conversation
// shows mail this yard sent to another, it is set on a file that yard did
// not take, with its reason (peerConversation).
type BlobRef struct {
	Hash     string `json:"hash"`
	Type     string `json:"type"`
	Bytes    int64  `json:"bytes"`
	NotTaken string `json:"not_taken,omitempty"`
}

// blobHash is what names a blob, and so all a path under BlobsDir is made
// of.
var blobHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// BlobPath is the file of the blob hash in the yard at home.
func BlobPath(home, hash string) string { return filepath.Join(home, BlobsDir, hash) }

// BlobPath is the file of the blob hash in this yard.
func (s *Store) BlobPath(hash string) string { return BlobPath(s.home, hash) }

// FilesLine says the files of a letter or a note where it is shown, each
// by the path of its blob in the yard at home, so a crew member reads the
// picture with what its harness reads any file with: "1 image: <path>".
// One the yard did not take has its reason where its path would be:
// "1 image: not taken: <reason>". "" for one without files.
func FilesLine(home string, files []BlobRef) string {
	if len(files) == 0 {
		return ""
	}
	paths, between := make([]string, len(files)), " "
	for i, f := range files {
		paths[i] = BlobPath(home, f.Hash)
		if f.NotTaken != "" {
			// A reason has spaces of its own.
			paths[i], between = "not taken: "+f.NotTaken, "; "
		}
	}
	noun := "image"
	if len(files) > 1 {
		noun = "images"
	}
	return fmt.Sprintf("%d %s: %s", len(files), noun, strings.Join(paths, between))
}

// WithFiles is the text of a letter or a note as one line shows it: the
// text, then its files (FilesLine).
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

// NoteFilesMark starts the line that names the images a note or a mail
// carries where it is shown (NoteFilesLine): its words end where the
// line begins, so nothing its writer typed stands for a file the yard
// keeps. Every renderer of notes and mail keeps the mark out of the
// writers' own words (WithoutFilesMark, and the terminal's noteLetters
// for notes), so a line starting with it is the yard's, and any other
// "N image(s):" is words.
const NoteFilesMark = "◆"

// NoteFilesLine is the files of a note or a mail where it is shown: the
// files line (FilesLine) on a line of its own, starting with the mark
// no writer's words can carry (NoteFilesMark). "" for one without files.
func NoteFilesLine(home string, files []BlobRef) string {
	if line := FilesLine(home, files); line != "" {
		return NoteFilesMark + " " + line
	}
	return ""
}

// WithoutFilesMark is s without the mark that starts a note's or a mail's
// files line: what a writer's own words are shown as, so a writer cannot
// forge the line by typing the mark (NoteFilesMark). A bead's title and
// body are shown so too, where they stand in the same output as its notes
// (bead show, prime, the brief), and a mail's sender where it stands on
// the mail's line (inbox, conversation, chat): they are writers' words
// as well.
func WithoutFilesMark(s string) string {
	return strings.ReplaceAll(s, NoteFilesMark, "")
}

// Letters is a writer's words as a terminal is given them: without what
// would move the cursor or turn the direction of text around (every
// control character but the line break and the tab, and the marks of
// direction), and without the mark (WithoutFilesMark). An escape sequence
// is left as the letters that followed its escape: a terminal draws the
// mark for one of them ("\x1b(0`"), so words that keep their escapes
// forge the files line without typing the mark.
func Letters(s string) string {
	return WithoutFilesMark(strings.Map(func(r rune) rune {
		switch {
		case r == '\n', r == '\t':
			return r
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r):
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "")))
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
	err = s.read(func(tx *bolt.Tx) error {
		limit, err = bytesSetting(tx, BlobMaxSetting, DefaultBlobMax)
		return err
	})
	return limit, err
}

// bytesSetting is the setting key as a number of bytes, unset when it is
// not set or does not parse as one.
func bytesSetting(tx *bolt.Tx, key string, unset int64) (int64, error) {
	var v string
	if ok, err := get(tx, bSettings, key, &v); err != nil {
		return 0, err
	} else if ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n, nil
		}
	}
	return unset, nil
}

// PutBlob keeps data as a blob and answers its name. The type is what the
// bytes say, never what the caller does, and anything but BlobTypes is
// refused, as is more than the yard's cap (BlobMaxSetting); nothing is
// stored then. Bytes the yard has already are not written again: the
// answer is the same, and the file is only marked as put now, so that a
// blob nothing names yet is not pruned before the mail or note it was put
// for is sent (BlobGrace).
func (s *Store) PutBlob(ctx context.Context, data []byte) (*BlobRef, error) {
	s.blobMu.Lock()
	defer s.blobMu.Unlock()

	limit, err := s.blobMax()
	if err != nil {
		return nil, err
	}
	typ, ok := blobType(data)
	switch {
	case int64(len(data)) > limit:
		return nil, fmt.Errorf("the file is %d bytes, over the yard's limit of %d (yardr settings set %s <bytes>)", len(data), limit, BlobMaxSetting)
	case !ok:
		return nil, fmt.Errorf("the file is %s: the yard keeps only %s", typ, strings.Join(BlobTypes, " and "))
	}
	sum := sha256.Sum256(data)
	return s.keepBlob(&BlobRef{Hash: hex.EncodeToString(sum[:]), Type: typ, Bytes: int64(len(data))}, data)
}

// keepBlob writes data, which ref names truly, where the yard keeps its
// blobs, for PutBlob and for a file a peer sent (takeFile), under blobMu.
func (s *Store) keepBlob(ref *BlobRef, data []byte) (*BlobRef, error) {
	path := s.BlobPath(ref.Hash)
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, data) {
			return nil, fmt.Errorf("blob %s already exists with different bytes", ref.Hash)
		}
		at := time.Now()
		return ref, os.Chtimes(path, at, at)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
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

// NoteBlob is the blob hash as the web view serves it: one a note on some
// bead names, as the yard holds it. ErrNotFound for one no note names — a
// mail's image, one nothing names — for one the yard no longer holds, and
// for a name that is no hash, refused as Blob refuses it: the view has no
// login, so what it serves is only what its bead pages show.
func (s *Store) NoteBlob(ctx context.Context, hash string) (*BlobRef, error) {
	if !blobHash.MatchString(hash) {
		return nil, fmt.Errorf("image %q: %w", hash, ErrNotFound)
	}
	var named bool
	if err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bNotes, "", func(_ []byte, n *Note) error {
			named = named || slices.ContainsFunc(n.Files, func(f BlobRef) bool { return f.Hash == hash })
			return nil
		})
	}); err != nil {
		return nil, err
	}
	if !named {
		return nil, fmt.Errorf("image %s: %w", hash, ErrNotFound)
	}
	return s.Blob(hash)
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

// noteFiles are the blobs the hashes name, as a note carries them. One
// the yard does not hold refuses the note: a note never names a picture
// nobody can read.
func (s *Store) noteFiles(hashes []string) ([]BlobRef, error) {
	if len(hashes) > MaxNoteFiles {
		return nil, fmt.Errorf("a note carries at most %d images, not %d", MaxNoteFiles, len(hashes))
	}
	var out []BlobRef
	for _, h := range hashes {
		ref, err := s.Blob(h)
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: the yard does not hold it; put it first (PutBlob, or yardr bead note --file)", err)
		} else if err != nil {
			return nil, err
		}
		out = append(out, *ref)
	}
	return out, nil
}

// pruneBlobs removes the blobs no letter of the log and no note names, and
// answers how many. One put within BlobGrace stays, named or not: its mail
// or note may be on its way. So does a put that did not finish, for as
// long.
func (s *Store) pruneBlobs() (int, error) {
	s.blobMu.Lock()
	defer s.blobMu.Unlock()

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
				// One not taken names no blob of the yard's.
				if f.NotTaken == "" {
					named[f.Hash] = true
				}
			}
		}
		// And what the notes carry: without this line the sweeper takes
		// every screenshot within the hour.
		if err := each(tx, bNotes, "", func(_ []byte, n *Note) error {
			for _, f := range n.Files {
				named[f.Hash] = true
			}
			return nil
		}); err != nil {
			return err
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
