package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// A delivery that carries files names them in its envelope by the source's
// own refs (Envelope.Files): what normalize can read of the body, with no
// credential. The bytes stay with the service until the delivery is kept
// and answered; then the receiver fetches each by the source's thread.file
// command, with the source's own credential, into the blob store, and
// records what it fetched beside the event (store.RecordDeliveryFiles).
// A stranger's delivery under unknown is held on its mapping card, not
// kept, so nothing of it is fetched while it waits: FetchHeldFiles
// fetches its files by this same path only when the card's answer
// releases it.
//
// The fetch runs outside the request: the service is answered first, so a
// slow download never makes it retry. A server that stops meanwhile loses
// the fetch in flight, and the delivery keeps its refs and the bracketed
// note of its text, as one whose source has no credential does. A
// redelivery is never fetched twice: only a delivery RecordHook kept anew
// is fetched.

// FileRef is a file a delivery names, as the source names it: its id there
// (a chat service's file id), the file's name where the sender gave one, what
// kind it is (photo, document, voice, video, audio), and its size in bytes
// where the source said, 0 where it did not.
type FileRef struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// FetchTimeout bounds the fetch of one delivery's files, every file of it
// together: the command is killed at source.Timeout each, and this is how
// long a delivery's goroutine may live.
const FetchTimeout = 5 * time.Minute

// fetchDir is where under the yard's home the files being fetched are
// written, one directory per file, removed once the file is a blob.
const fetchDir = "tmp"

// File fetches the file ref names into dir, by thread.file: in {ref, dir,
// max_bytes}, out {path, bytes}. The path must be a regular file inside
// dir of the bytes said, and at most max bytes; the answer is its bytes.
// cred is the source's credential, handed to the command as every thread
// command is handed one.
func (d Declared) File(ctx context.Context, ref FileRef, dir string, max int64, cred string) ([]byte, error) {
	if d.D.Thread == nil || len(d.D.Thread.File) == 0 {
		return nil, fmt.Errorf("source %s has no thread.file: its files cannot be fetched", d.D.Name)
	}
	in := map[string]any{"ref": ref, "dir": dir, "max_bytes": max}
	var path string
	var n int64
	err := d.runThread(ctx, "thread.file", d.D.Thread.File, in, cred, func(out []byte) error {
		var o struct {
			Path  string `json:"path"`
			Bytes int64  `json:"bytes"`
		}
		if err := json.Unmarshal(out, &o); err != nil {
			return fmt.Errorf("its answer is no {path, bytes}: %v", err)
		}
		path, n = o.Path, o.Bytes
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Only what the command wrote where it was told is read: a path
	// anywhere but directly in dir, or a link, is no file of this fetch.
	if !filepath.IsAbs(path) || filepath.Dir(filepath.Clean(path)) != filepath.Clean(dir) {
		return nil, fmt.Errorf("thread.file of source %s answered a path outside the directory it was given", d.D.Name)
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("thread.file of source %s answered no file it wrote", d.D.Name)
	}
	if st.Size() > max {
		return nil, fmt.Errorf("%d bytes, over the limit of %d", st.Size(), max)
	}
	if st.Size() != n {
		return nil, fmt.Errorf("thread.file of source %s said %d bytes and wrote %d", d.D.Name, n, st.Size())
	}
	return os.ReadFile(path)
}

// FetchHeldFiles fetches the files of the hook delivery seq a mapping
// card's answer released, as store.SetHeldFilesFetch registers it: the
// delivery was held, not kept, when it arrived, so its files are
// fetched now that the answer kept it, as a kept delivery's are after
// its answer (serve). It runs off the answering write: the fetch is
// counted before it starts, so whoever waits on it (Wait) finds it,
// and a hook or source gone meanwhile fetches nothing, as a delivery
// whose source cannot fetch does not.
func (rc *Receiver) FetchHeldFiles(hook string, seq uint64, files []store.HeldFile) {
	if len(files) == 0 {
		return
	}
	refs := make([]FileRef, len(files))
	for i, f := range files {
		refs[i] = FileRef{ID: f.ID, Name: f.Name, Type: f.Type, Size: f.Size}
	}
	rc.fetching.Add(1)
	go func() {
		ctx := context.Background()
		h, err := rc.st.GetHook(ctx, hook)
		if err != nil {
			rc.fetching.Done()
			rc.log.Info("released delivery's files not fetched", "hook", hook, "seq", seq, "err", err)
			return
		}
		ad, ok := rc.adapter(ctx, h.Source)
		dec, declared := ad.(Declared)
		if !ok || !declared {
			rc.fetching.Done()
			return
		}
		rc.fetchFiles(hook, seq, dec, refs) // fetchFiles counts the fetch done
	}()
}

// fetchFiles fetches the files the kept delivery seq names, by the hook's
// declared source, and records what came of each. A source that cannot
// fetch (no thread.file) or has no credential set fetches nothing, and the
// delivery keeps its refs alone; any other failure is recorded on the
// file it was of, with its reason, so the delivery says what it carried.
func (rc *Receiver) fetchFiles(hook string, seq uint64, dec Declared, refs []FileRef) {
	defer rc.fetching.Done()
	ctx, cancel := context.WithTimeout(context.Background(), FetchTimeout)
	defer cancel()
	if dec.D.Thread == nil || len(dec.D.Thread.File) == 0 {
		return
	}
	cred, err := rc.sourceCredential(ctx, dec.D)
	if err != nil {
		rc.log.Info("hook delivery's files not fetched", "hook", hook, "seq", seq, "err", err)
		return
	}
	max := rc.blobMax(ctx)
	if len(refs) > store.MaxMailFiles {
		refs = refs[:store.MaxMailFiles]
	}
	var files []store.BlobRef
	for _, ref := range refs {
		files = append(files, rc.fetchFile(ctx, dec, ref, max, cred))
	}
	if err := rc.st.RecordDeliveryFiles(ctx, seq, files); err != nil {
		rc.log.Error("hook delivery's files not recorded", "hook", hook, "seq", seq, "err", err)
	}
}

// fetchFile is one file of a delivery as the yard took it: a blob, or why
// it was not taken, with the size the source said.
func (rc *Receiver) fetchFile(ctx context.Context, dec Declared, ref FileRef, max int64, cred string) store.BlobRef {
	name := ref.Name
	if store.CheckBlobName(name) != nil {
		name = ""
	}
	// The reason is kept beside the delivery and read by every device
	// (DeliveryFiles), so the credential the command was handed is taken
	// out of it first: a command that fails may have printed it, as a
	// build command's stderr is redacted (source.BuildBody).
	redact := func(text string) string {
		if cred != "" {
			text = strings.ReplaceAll(text, cred, "[credential redacted]")
		}
		return text
	}
	notTaken := func(why string) store.BlobRef {
		return store.BlobRef{Name: name, Bytes: ref.Size, NotTaken: redact(why)}
	}
	what := ref.Type
	if what == "" {
		what = "file"
	}
	if ref.Size > max {
		return notTaken(fmt.Sprintf("%s of %d bytes, over the yard's limit of %d (yardr settings set %s <bytes>)", what, ref.Size, max, store.BlobMaxSetting))
	}
	root := filepath.Join(rc.home, fetchDir)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return notTaken(err.Error())
	}
	dir, err := os.MkdirTemp(root, "file-")
	if err != nil {
		return notTaken(err.Error())
	}
	defer os.RemoveAll(dir)
	data, err := dec.File(ctx, ref, dir, max, cred)
	if err != nil {
		return notTaken(fmt.Sprintf("%s not fetched: %v", what, err))
	}
	blob, err := rc.st.PutBlob(ctx, data)
	if err != nil {
		return notTaken(fmt.Sprintf("%s not kept: %v", what, err))
	}
	blob.Name = name
	return *blob
}

// sourceCredential is the credential the yard fetches a source's files
// with: the source's own, from the env file the operator set for it, as
// telling through it reads it (a read). The files are the delivery's, and
// the delivery the yard's, so no crew member's or user's credential is
// asked for.
func (rc *Receiver) sourceCredential(ctx context.Context, d *source.Declaration) (string, error) {
	src, err := rc.st.GetSource(ctx, d.Name)
	if err != nil {
		return "", err
	}
	cred, _, err := d.ReadCredential(ctx, src.EnvFile, src.Mint, source.Read)
	if err != nil {
		return "", err
	}
	if cred == "" {
		return "", fmt.Errorf("source %s has no credential set", d.Name)
	}
	return cred, nil
}

// blobMax is the yard's cap on one blob, which PutBlob holds a file to:
// the command is told it, so a file over it is refused before it is
// downloaded.
func (rc *Receiver) blobMax(ctx context.Context) int64 {
	all, _ := rc.st.Settings(ctx)
	if n, err := strconv.ParseInt(all[store.BlobMaxSetting], 10, 64); err == nil && n > 0 {
		return n
	}
	return store.DefaultBlobMax
}

// Wait waits for the fetches of files under way: a test's, or a receiver's
// that stops while the store is still open.
func (rc *Receiver) Wait() { rc.fetching.Wait() }
