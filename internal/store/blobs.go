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
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// A blob is a file mail or a bead's note carries: any bytes under the
// cap, in one of three kinds the yard tells apart by the bytes
// (http.DetectContentType): an image (a PNG or a JPEG), a page (HTML),
// and a file (everything else). The yard keeps it in its home under
// BlobsDir, in a file named by the SHA-256 of its bytes, so the same
// bytes sent twice are one file and a letter names them by what they
// are. A blob is written once and never changed. The log has only the
// name (the files of a mail event, of a note its row), so the store file
// does not grow by the bytes; a blob nothing names any more goes with
// Prune, and nothing else removes one.
//
// The file has no record beside it: its type is read from its first bytes
// each time it is asked for, as it was when it was put. A file's name is
// kept on the letter that carries the blob, not beside the blob file,
// which stays content-addressed and shared.

// BlobsDir is the directory of the blobs, in the yard's home.
const BlobsDir = "blobs"

const (
	// BlobMaxSetting is the most bytes one blob may have, and
	// DefaultBlobMax what a yard without the setting takes.
	BlobMaxSetting = "blob_max_bytes"
	DefaultBlobMax = 8 << 20
	// MaxMailFiles bounds the files of one mail.
	MaxMailFiles = 64
	// MaxNoteFiles bounds the files of one note, as MaxMailFiles bounds a
	// mail's.
	MaxNoteFiles = MaxMailFiles
	// FilesMaxSetting is the count Settings answers beside blob_max_bytes:
	// how many files one mail or note may carry. The yard answers its own
	// count; it is not set.
	FilesMaxSetting = "files_max"
	// BlobGrace is how long Prune leaves a blob that nothing names: the
	// mail or note that will name it is sent after it was put, a paste
	// some time before the line it goes with.
	BlobGrace = time.Hour
)

var bBlobPuts = []byte("blob_puts") // <hash>: the user whose device put it

func init() { allBuckets = append(allBuckets, bBlobPuts) }

// blobPage is the type of a page: HTML, as the bytes say. FilesLine
// counts it apart from images, and the view runs it where a file only
// downloads.
const blobPage = "text/html; charset=utf-8"

// blobImages are the types of an image, as the bytes say: what Kind
// counts apart from a page and a file.
var blobImages = []string{"image/png", "image/jpeg"}

// BlobRef names a blob where a letter carries it: the SHA-256 of its bytes
// in hex, which is its file's name, the type its bytes say, and how many
// they are. Name is the file's base name as the sender gave it, kept on
// the letter, not beside the blob file; empty on a paste, and on a letter
// from before names.
//
// NotTaken is set on a file of mail from another yard that this yard did
// not take, and says why (peerfiles.go): the yard holds no blob for it,
// and hash, type, bytes and name are the sender's word. Where a
// conversation shows mail this yard sent to another, it is set on a file
// that yard did not take, with its reason (peerConversation).
type BlobRef struct {
	Hash     string `json:"hash"`
	Type     string `json:"type"`
	Bytes    int64  `json:"bytes"`
	Name     string `json:"name,omitempty"`
	NotTaken string `json:"not_taken,omitempty"`
}

// IsPage is whether the blob is a page: HTML, as the bytes say.
func (f BlobRef) IsPage() bool { return f.Type == blobPage }

// Kind is the blob's kind, as its bytes say: "image" for a PNG or a
// JPEG, "page" for HTML, "file" for everything else.
func (f BlobRef) Kind() string {
	switch {
	case f.Type == blobPage:
		return "page"
	case slices.Contains(blobImages, f.Type):
		return "image"
	default:
		return "file"
	}
}

// BlobFile is a file a letter carries, as Mail, MailPeer, AddNote and
// AddNotedAs take it: the hash PutBlob answered and the name the sender
// gives it. The name is the file's base name; empty on a paste, and on
// an image or a page sent without one.
type BlobFile struct {
	Hash string `json:"hash"`
	Name string `json:"name,omitempty"`
}

// MaxBlobName is how many bytes a file's name may have.
const MaxBlobName = 255

// CheckBlobName holds a file's name to what a letter may carry: at most
// MaxBlobName bytes, valid UTF-8, no "/", "\", NUL or control character
// (bidi controls and the line and paragraph separators among them, which
// would turn "exe.pdf" around where the name is drawn), and neither "."
// nor "..". Empty is allowed, for a paste. Mail, MailPeer
// and notes refuse a name that fails, never sanitised; a name arriving
// from a peer is kept empty instead (filing.mailFiles); the view holds a
// name in a blob URL to the same rule.
func CheckBlobName(name string) error {
	switch {
	case name == "":
		return nil
	case len(name) > MaxBlobName:
		return fmt.Errorf("a file's name is at most %d bytes, not %d", MaxBlobName, len(name))
	case name == "." || name == "..":
		return fmt.Errorf("a file's name is never %q", name)
	case strings.Contains(name, "/") || strings.Contains(name, "\\"):
		return fmt.Errorf("a file's name holds no / or \\: %q", name)
	case !utf8.ValidString(name):
		return fmt.Errorf("a file's name is UTF-8: %q", name)
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029' {
			return fmt.Errorf("a file's name holds no control character: %q", name)
		}
	}
	return nil
}

// blobHash is what names a blob, and so all a path under BlobsDir is made
// of.
var blobHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// BlobPath is the file of the blob hash in the yard at home.
func BlobPath(home, hash string) string { return filepath.Join(home, BlobsDir, hash) }

// BlobPath is the file of the blob hash in this yard.
func (s *Store) BlobPath(hash string) string { return BlobPath(s.home, hash) }

// FilesLine says the files of a letter or a note where it is shown, each
// by the path of its blob in the yard at home, so a crew member reads it
// with what its harness reads any file with: "1 image: <path>". A file
// with a name carries it in parentheses after its path, for whoever reads
// it, agent or person: "1 file: <path> (report.pdf)". Pages stand apart
// from images, and files from both: "1 image, 2 files: <path> <path>
// (report.pdf) <path> (data.csv)". One the yard did not take has its
// reason where its path would be: "1 image: not taken: <reason>". "" for
// one without files.
//
// The marked line stays the only line that names a file the yard keeps:
// the name in parentheses is for the reader, never a path the yard reads.
// A name that holds a parenthesis or a quote is quoted inside them, so
// what a sender names a file never closes them and reads as another path.
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
		if name := Letters(f.Name); name != "" {
			if strings.ContainsAny(name, `()"`) {
				name = strconv.Quote(name)
			}
			paths[i] += " (" + name + ")"
		}
	}
	return fmt.Sprintf("%s: %s", filesNoun(files), strings.Join(paths, between))
}

// filesNoun counts the files by kind, images first, then pages, then
// files: "2 images", "1 page", "1 image, 1 page", "1 image, 2 files".
func filesNoun(files []BlobRef) string {
	var images, pages, rest int
	for _, f := range files {
		switch f.Kind() {
		case "page":
			pages++
		case "image":
			images++
		default:
			rest++
		}
	}
	var parts []string
	if images > 0 {
		noun := "image"
		if images > 1 {
			noun = "images"
		}
		parts = append(parts, fmt.Sprintf("%d %s", images, noun))
	}
	if pages > 0 {
		noun := "page"
		if pages > 1 {
			noun = "pages"
		}
		parts = append(parts, fmt.Sprintf("%d %s", pages, noun))
	}
	if rest > 0 {
		noun := "file"
		if rest > 1 {
			noun = "files"
		}
		parts = append(parts, fmt.Sprintf("%d %s", rest, noun))
	}
	return strings.Join(parts, ", ")
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

// NoteFilesMark starts the line that names the files a note or a mail
// carries where it is shown (NoteFilesLine): its words end where the
// line begins, so nothing its writer typed stands for a file the yard
// keeps. Every renderer of notes and mail keeps the mark out of the
// writers' own words (WithoutFilesMark, and Letters where control
// characters go too), so a line starting with it is the yard's, and any
// other "N image(s):", "N page(s):" or "N file(s):" is words.
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
// forge the line by typing the mark (NoteFilesMark). Only the mark at a
// line's first non-whitespace position goes, the only position the line
// is recognised by (a line with HasPrefix(TrimSpace(line), mark)); a mark
// further in is a writer's own diamond and stays byte-identical.
// Whitespace between two marks saves neither: the second would otherwise
// stand one space over and still read as the yard's. A bead's title and
// body are shown so too, where they stand in the same output as
// its notes (bead show, prime, the brief), and a mail's sender where it
// stands on the mail's line (inbox, conversation, chat): they are
// writers' words as well. Residual: a writer's mark at a line's first
// non-space position still drops. That is the safe direction (it is
// exactly the forgery shape), vanishingly rare, and the stored bytes are
// untouched, so any future escape re-renders them.
func WithoutFilesMark(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
		rest := trimmed
		for strings.HasPrefix(rest, NoteFilesMark) {
			rest = rest[len(NoteFilesMark):]
			if next := strings.TrimLeftFunc(rest, unicode.IsSpace); strings.HasPrefix(next, NoteFilesMark) {
				rest = next
			}
		}
		lines[i] = line[:len(line)-len(trimmed)] + rest
	}
	return strings.Join(lines, "\n")
}

// Letters is a writer's words as a terminal is given them: without what
// would move the cursor, start a line again or turn the direction of text
// around (every control character but the line break and the tab, the line
// and paragraph separators, and the marks of direction), and without the
// mark (WithoutFilesMark). An escape sequence is left as the letters that
// followed its escape: a terminal draws the mark for one of them
// ("\x1b(0`"), so words that keep their escapes forge the files line
// without typing the mark.
func Letters(s string) string {
	return WithoutFilesMark(strings.Map(func(r rune) rune {
		switch {
		case r == '\n', r == '\t':
			return r
		case r == '\u2028', r == '\u2029':
			return -1
		case unicode.IsControl(r), unicode.Is(unicode.Bidi_Control, r):
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "")))
}

// NoteLetters is a writer's words where a note stands: Letters, under the
// name the bead, note and ask renderers use.
func NoteLetters(s string) string {
	return Letters(s)
}

// OneLine is a writer's words where a single line of the yard's stands
// for them: a note's author, a bead's title, an ask's short fields. As
// letters only (NoteLetters), and without the line break, so the words
// cannot start a line of their own where they are shown. The letters come
// first, so the files-mark guard still reads the line starts the writer
// typed: folded first, a forged mark would stand mid-line and stay.
func OneLine(s string) string {
	return strings.ReplaceAll(NoteLetters(s), "\n", " ")
}

// EventFiles are the files the event e carries, nil for one without any:
// a mail's files, or a red quiet run's failing packages' output
// (quiet_failed), each stored as a blob and named as mail names them.
func EventFiles(e Event) []BlobRef {
	raw, ok := e.Data["files"]
	if !ok || (e.Kind != "mail" && e.Kind != QuietFailed) {
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

// blobType is the type the bytes say.
func blobType(head []byte) string {
	return http.DetectContentType(head)
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
// bytes say, never what the caller does; any bytes under the yard's cap
// (BlobMaxSetting) are kept, and more than the cap is refused with nothing
// stored. Bytes the yard has already are not written again: the answer is
// the same, and the file is only marked as put now, so that a blob nothing
// names yet is not pruned before the mail or note it was put for is sent
// (BlobGrace). A put through a device keeps whose device's it was
// (BlobPutBy); put again by another's, the latest putter stands.
func (s *Store) PutBlob(ctx context.Context, data []byte) (*BlobRef, error) {
	s.blobMu.Lock()
	defer s.blobMu.Unlock()

	limit, err := s.blobMax()
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the file is %d bytes, over the yard's limit of %d (yardr settings set %s <bytes>)", len(data), limit, BlobMaxSetting)
	}
	typ := blobType(data)
	sum := sha256.Sum256(data)
	ref, err := s.keepBlob(&BlobRef{Hash: hex.EncodeToString(sum[:]), Type: typ, Bytes: int64(len(data))}, data)
	if err != nil {
		return nil, err
	}
	// Whose device put it is kept with the blob: the listener serves an
	// upload whose mail or note is on its way to that user's own device
	// alone. Only a call through a device names one (WithVia); what the
	// yard's own machine puts, and what it takes from a peer, is read by
	// what names it, never by this.
	if v, ok := ctx.Value(viaKey{}).(via); ok && v.user != "" {
		if err := s.write(func(w *wtx) error {
			return put(w.Tx, bBlobPuts, ref.Hash, v.user)
		}); err != nil {
			return nil, err
		}
	}
	return ref, nil
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
		return nil, fmt.Errorf("file %q: %w", hash, ErrNotFound)
	}
	f, err := os.Open(s.BlobPath(hash))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("file %s: %w", hash, ErrNotFound)
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
	return &BlobRef{Hash: hash, Type: blobType(head[:n]), Bytes: st.Size()}, nil
}

// NoteBlob is the blob hash as the web view serves it to anyone: one a
// note on some bead names, as the yard holds it, with the name the first
// such note gives it. ErrNotFound for one no note names — a mail's file,
// one nothing names — for one the yard no longer holds, and for a name
// that is no hash, refused as Blob refuses it. A mail's file is
// MailBlob's: where the view acts, it serves that to the mail's sender or
// recipient.
func (s *Store) NoteBlob(ctx context.Context, hash string) (*BlobRef, error) {
	if !blobHash.MatchString(hash) {
		return nil, fmt.Errorf("file %q: %w", hash, ErrNotFound)
	}
	var name string
	named := false
	if err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bNotes, "", func(_ []byte, n *Note) error {
			for _, f := range n.Files {
				if f.Hash == hash && !named {
					name, named = f.Name, true
				}
			}
			return nil
		})
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

// MailBlob is the blob hash as the web view serves it to viewer: one a
// mail names where viewer is its sender or recipient, as the yard holds
// it, with the name the first such mail gives it. ErrNotFound for one no
// such mail names — a note's file, one nothing names — for one the yard
// no longer holds, for a viewer that is nobody, and for a name that is no
// hash, refused as Blob refuses it. One a mail carries that the yard did
// not take (BlobRef.NotTaken) names no blob of the yard's.
func (s *Store) MailBlob(ctx context.Context, viewer, hash string) (*BlobRef, error) {
	if !blobHash.MatchString(hash) {
		return nil, fmt.Errorf("file %q: %w", hash, ErrNotFound)
	}
	if viewer == "" {
		return nil, fmt.Errorf("file %s: %w", hash, ErrNotFound)
	}
	name, named, err := s.mailBlobName(hash, func(e Event) bool {
		return e.Actor == viewer || MailReceiver(e) == viewer
	})
	if err != nil {
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

// BlobMailed reports whether any mail names the blob hash: one a mail
// carries that the yard took. One a mail carries that the yard did not
// take (BlobRef.NotTaken) names no blob of the yard's, and neither does a
// name that is no hash. The listener reads it to tell a young blob whose
// mail is already sent, which another user's device is not served, from
// one whose mail is on its way.
func (s *Store) BlobMailed(ctx context.Context, hash string) (bool, error) {
	if !blobHash.MatchString(hash) {
		return false, nil
	}
	_, named, err := s.mailBlobName(hash, func(Event) bool { return true })
	return named, err
}

// BlobPutBy is the user whose device put the blob hash: the one whose own
// device the listener serves it to before any mail or note names it. ""
// for one put on the yard's own machine, taken from a peer, or put before
// the yard kept who put, and for a name that is no hash. What names a
// blob serves it, not this (MailBlob, NoteBlob); the prune takes the
// record with the file.
func (s *Store) BlobPutBy(ctx context.Context, hash string) (string, error) {
	if !blobHash.MatchString(hash) {
		return "", nil
	}
	var by string
	err := s.read(func(tx *bolt.Tx) error {
		_, err := get(tx, bBlobPuts, hash, &by)
		return err
	})
	return by, err
}

// mailBlobName is the name the first mail allow keeps gives the blob
// hash: what MailBlob serves, and what tells a blob some mail names from
// one none does (BlobMailed).
func (s *Store) mailBlobName(hash string, allow func(Event) bool) (name string, named bool, err error) {
	err = s.read(func(tx *bolt.Tx) error {
		prefix := []byte("mail\x00")
		c := tx.Bucket(bEventsKd).Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			var e Event
			if _, err := get(tx, bEvents, string(k[len(prefix):]), &e); err != nil {
				return err
			}
			if !allow(e) {
				continue
			}
			for _, f := range EventFiles(e) {
				if f.Hash == hash && f.NotTaken == "" && !named {
					name, named = f.Name, true
				}
			}
			if named {
				return nil
			}
		}
		return nil
	})
	return name, named, err
}

// mailFiles are the blobs the files name, as a mail event carries them:
// each with the type and size the yard holds, and the name the sender
// gave. One the yard does not hold refuses the mail: a letter never names
// a file nobody can read. A name no letter may carry refuses it too,
// never sanitised.
func (s *Store) mailFiles(files []BlobFile) ([]BlobRef, error) {
	if len(files) > MaxMailFiles {
		return nil, fmt.Errorf("mail carries at most %d files, not %d", MaxMailFiles, len(files))
	}
	var out []BlobRef
	for _, f := range files {
		if err := CheckBlobName(f.Name); err != nil {
			return nil, err
		}
		ref, err := s.Blob(f.Hash)
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: the yard does not hold it; put it first (PutBlob, or yardr mail --file)", err)
		} else if err != nil {
			return nil, err
		}
		ref.Name = f.Name
		out = append(out, *ref)
	}
	return out, nil
}

// noteFiles are the blobs the files name, as a note carries them: each
// with the type and size the yard holds, and the name the sender gave.
// One the yard does not hold refuses the note: a note never names a file
// nobody can read. A name no letter may carry refuses it too, never
// sanitised.
func (s *Store) noteFiles(files []BlobFile) ([]BlobRef, error) {
	if len(files) > MaxNoteFiles {
		return nil, fmt.Errorf("a note carries at most %d files, not %d", MaxNoteFiles, len(files))
	}
	var out []BlobRef
	for _, f := range files {
		if err := CheckBlobName(f.Name); err != nil {
			return nil, err
		}
		ref, err := s.Blob(f.Hash)
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("%w: the yard does not hold it; put it first (PutBlob, or yardr bead note --file)", err)
		} else if err != nil {
			return nil, err
		}
		ref.Name = f.Name
		out = append(out, *ref)
	}
	return out, nil
}

// pruneBlobs removes the blobs no letter of the log, no note and no hook
// delivery kept in the log names, and answers how many. One put within
// BlobGrace stays, named or not: its mail, note or delivery's record may
// be on its way. So does a put that did not finish, for as long.
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
		// The kinds whose events carry files (EventFiles): a mail's, and
		// a red quiet run's failing packages' output.
		for _, prefix := range [][]byte{[]byte("mail\x00"), []byte(QuietFailed + "\x00")} {
			c := tx.Bucket(bEventsKd).Cursor()
			for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
				var e Event
				if _, err := get(tx, bEvents, string(k[len(prefix):]), &e); err != nil {
					return err
				}
				for _, f := range EventFiles(e) {
					// One not taken names no blob of the yard's.
					if f.NotTaken == "" {
						named[f.Hash] = true
					}
				}
			}
		}
		// What the receiver fetched of hook deliveries kept in the log
		// (hookfiles.go): a delivery's record goes with its event.
		if err := tx.Bucket(bHookFiles).ForEach(func(_, raw []byte) error {
			var files []BlobRef
			if err := json.Unmarshal(raw, &files); err != nil {
				return err
			}
			for _, f := range files {
				if f.Hash != "" {
					named[f.Hash] = true
				}
			}
			return nil
		}); err != nil {
			return err
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
	var swept []string
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
			swept = append(swept, f.Name())
		}
	}
	// What went takes whose put it was with it, in one write: a record
	// without its file would serve no read, and the bucket would grow
	// with every upload.
	if len(swept) > 0 {
		err := s.write(func(w *wtx) error {
			for _, hash := range swept {
				if err := w.Bucket(bBlobPuts).Delete([]byte(hash)); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
