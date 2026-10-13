package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// What a type is read from is the first bytes, so these are images as far
// as the yard asks.
var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\n" + "a picture of the shed")
	jpegBytes = []byte("\xff\xd8\xff\xe0" + "another of the fence")
	gifBytes  = []byte("GIF89a" + "one that moves")
)

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// blobFiles are the names in the yard's blobs directory.
func blobFiles(t *testing.T, s *store.Store) []string {
	t.Helper()
	files, err := os.ReadDir(filepath.Dir(s.BlobPath("x")))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, f.Name())
	}
	return names
}

// A blob is the file of its hash in the home, with the type its bytes say;
// the same bytes put again are not written again, and what is no PNG or
// JPEG, or over the yard's cap, is refused with nothing stored.
func TestPutBlob(t *testing.T) {
	api, s := storetest.NewOwned(t)
	ctx := context.Background()
	ref, err := api.PutBlob(ctx, pngBytes)
	if err != nil || *ref != (store.BlobRef{Hash: hashOf(pngBytes), Type: "image/png", Bytes: int64(len(pngBytes))}) {
		t.Fatalf("put = %+v, %v", ref, err)
	}
	path := s.BlobPath(ref.Hash)
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, pngBytes) {
		t.Fatalf("the blob's file = %q, %v", got, err)
	}
	if filepath.Base(filepath.Dir(path)) != store.BlobsDir || filepath.Base(path) != ref.Hash {
		t.Errorf("the blob is at %s, want <home>/%s/<hash>", path, store.BlobsDir)
	}
	if got, err := s.Blob(ref.Hash); err != nil || *got != *ref {
		t.Errorf("Blob = %+v, %v", got, err)
	}

	// Not written again: the file a second put leaves is the first one's,
	// which a write would have replaced.
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := api.PutBlob(ctx, pngBytes)
	if err != nil || *again != *ref {
		t.Fatalf("the second put = %+v, %v", again, err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || len(blobFiles(t, s)) != 1 {
		t.Errorf("a second put wrote: same file %v, files %v, %v", os.SameFile(before, after), blobFiles(t, s), err)
	}

	if ref, err := api.PutBlob(ctx, jpegBytes); err != nil || ref.Type != "image/jpeg" {
		t.Errorf("a JPEG = %+v, %v", ref, err)
	}
	if _, err := api.PutBlob(ctx, gifBytes); err == nil || !strings.Contains(err.Error(), "the file is image/gif: the yard keeps only image/png and image/jpeg") {
		t.Errorf("a GIF = %v", err)
	}
	big := append(bytes.Clone(pngBytes), make([]byte, 9<<20)...)
	if _, err := api.PutBlob(ctx, big); err == nil || !strings.Contains(err.Error(), "over the yard's limit of 8388608 (yardr settings set blob_max_bytes <bytes>)") {
		t.Errorf("9 MiB = %v", err)
	}
	if err := api.SetSetting(ctx, store.BlobMaxSetting, "16", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutBlob(ctx, []byte("\x89PNG\r\n\x1a\n and seventeen")); err == nil || !strings.Contains(err.Error(), "over the yard's limit of 16") {
		t.Errorf("over the cap that is set = %v", err)
	}
	if got := blobFiles(t, s); len(got) != 2 {
		t.Errorf("what was refused was stored: %v", got)
	}
	for _, name := range []string{"nope", "../yard.db", hashOf(gifBytes)} {
		if _, err := s.Blob(name); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("Blob(%q) = %v, want not found", name, err)
		}
	}
	if err := api.SetSetting(ctx, store.BlobMaxSetting, "0", "jo"); err == nil {
		t.Error("a cap of 0 was taken")
	}
}

// Concurrent puts of the same bytes share one complete blob.
func TestConcurrentPutBlob(t *testing.T) {
	api, s := storetest.NewOwned(t)
	const puts = 12
	var wg sync.WaitGroup
	errs := make(chan error, puts)
	for i := 0; i < puts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref, err := api.PutBlob(context.Background(), pngBytes)
			if err == nil && ref.Hash != hashOf(pngBytes) {
				err = errors.New("put returned a different hash")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := blobFiles(t, s); len(got) != 1 || got[0] != hashOf(pngBytes) {
		t.Fatalf("concurrent puts left %v", got)
	}
	if got, err := os.ReadFile(s.BlobPath(hashOf(pngBytes))); err != nil || !bytes.Equal(got, pngBytes) {
		t.Fatalf("concurrent puts left %q, %v", got, err)
	}
}

// Mail names the blobs it carries, and the user's inbox, the conversation
// and the crew member's inbox carry the list; mail without any carries
// none. A hash the yard does not hold refuses the mail.
func TestMailFiles(t *testing.T) {
	api, _ := storetest.NewOwned(t)
	ctx := context.Background()
	party(t, api, "clerk")
	if _, err := api.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	png, err := api.PutBlob(ctx, pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	jpeg, err := api.PutBlob(ctx, jpegBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.Mail(ctx, "ada", "plain", "clerk"); err != nil {
		t.Fatal(err)
	}
	if err := api.Mail(ctx, "ada", "the shed", "clerk", png.Hash, jpeg.Hash); err != nil {
		t.Fatal(err)
	}
	// A picture says enough: mail with one needs no text.
	if err := api.Mail(ctx, "clerk", "", "ada", png.Hash); err != nil {
		t.Fatal(err)
	}
	want := []store.BlobRef{*png, *jpeg}
	check := func(what string, l store.Letter, files []store.BlobRef) {
		t.Helper()
		if len(l.Files) != len(files) {
			t.Fatalf("%s: files = %+v, want %+v", what, l.Files, files)
		}
		for i := range files {
			if l.Files[i] != files[i] {
				t.Errorf("%s: file %d = %+v, want %+v", what, i, l.Files[i], files[i])
			}
		}
	}
	box, err := api.UserInbox(ctx, "ada", 0)
	if err != nil || len(box.Mail) != 2 {
		t.Fatalf("inbox = %+v, %v", box, err)
	}
	check("inbox, plain", box.Mail[0], nil)
	check("inbox", box.Mail[1], want)
	convo, _, err := api.Conversation(ctx, "ada", "clerk", 0, 0, 0)
	if err != nil || len(convo) != 3 {
		t.Fatalf("conversation = %+v, %v", convo, err)
	}
	check("conversation, plain", convo[0], nil)
	check("conversation", convo[1], want)
	check("conversation, no text", convo[2], want[:1])
	page, err := api.Inbox(ctx, "clerk", 0)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("crew inbox = %+v, %v", page, err)
	}
	if got := store.MailFiles(page.Events[0]); len(got) != 1 || got[0] != *png {
		t.Errorf("the mail event's files = %+v", got)
	}

	head, err := api.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for why, files := range map[string][]string{
		"the yard does not hold it": {hashOf(gifBytes)},
		"not found":                 {"../yard.db"},
		"at most 8 images, not 9":   slicesOf(png.Hash, 9),
	} {
		if err := api.Mail(ctx, "ada", "look", "clerk", files...); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("mail with %v = %v, want %q", files, err, why)
		}
	}
	if err := api.Mail(ctx, "ada", "", "clerk"); err == nil {
		t.Error("mail with neither text nor file was sent")
	}
	if now, err := api.Head(ctx); err != nil || now != head {
		t.Errorf("refused mail was logged: head %d, was %d, %v", now, head, err)
	}
}

func slicesOf(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// A file of a peer's mail that the yard did not take has its reason where
// its path would be, and the line parts such files so that a reason's own
// spaces read as its own.
func TestFilesLineNotTaken(t *testing.T) {
	held := store.BlobRef{Hash: hashOf(pngBytes), Type: "image/png", Bytes: 7}
	gone := store.BlobRef{Hash: hashOf(jpegBytes), NotTaken: "the link does not take files"}
	if got := store.FilesLine("/y", []store.BlobRef{gone}); got != "1 image: not taken: the link does not take files" {
		t.Errorf("one not taken = %q", got)
	}
	if got, want := store.FilesLine("/y", []store.BlobRef{held, gone}), "2 images: "+store.BlobPath("/y", held.Hash)+"; not taken: the link does not take files"; got != want {
		t.Errorf("one of two not taken = %q, want %q", got, want)
	}
	if got, want := store.FilesLine("/y", []store.BlobRef{held, held}), "2 images: "+store.BlobPath("/y", held.Hash)+" "+store.BlobPath("/y", held.Hash); got != want {
		t.Errorf("two held = %q, want %q", got, want)
	}
}

// Prune removes a blob no letter names and keeps one that is named, and one
// put so lately that its mail may still come.
func TestPruneBlobs(t *testing.T) {
	api, s := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := api.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	named, err := api.PutBlob(ctx, pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	loose, err := api.PutBlob(ctx, jpegBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.Mail(ctx, "ada", "the shed", "jo", named.Hash); err != nil {
		t.Fatal(err)
	}
	if res, err := api.Prune(ctx, store.Retention{}, "jo"); err != nil || res.Blobs != 0 || len(blobFiles(t, s)) != 2 {
		t.Fatalf("prune of blobs just put = %+v, %v; files %v", res, err, blobFiles(t, s))
	}
	old := time.Now().Add(-2 * store.BlobGrace)
	for _, ref := range []*store.BlobRef{named, loose} {
		if err := os.Chtimes(s.BlobPath(ref.Hash), old, old); err != nil {
			t.Fatal(err)
		}
	}
	res, err := api.Prune(ctx, store.Retention{}, "jo")
	if err != nil || res.Blobs != 1 {
		t.Fatalf("prune = %+v, %v", res, err)
	}
	if got := blobFiles(t, s); len(got) != 1 || got[0] != named.Hash {
		t.Errorf("after the prune the yard holds %v, want the named %s", got, named.Hash)
	}
	evs, err := api.EventsOfKind(ctx, "store_pruned", 1)
	if err != nil || len(evs) != 1 || evs[0].Data["blobs"] != float64(1) {
		t.Errorf("store_pruned = %+v, %v", evs, err)
	}
	// Put again, the blob that went is young again, and stays for its mail.
	if _, err := api.PutBlob(ctx, jpegBytes); err != nil {
		t.Fatal(err)
	}
	if res, err := api.Prune(ctx, store.Retention{}, "jo"); err != nil || res.Blobs != 0 || len(blobFiles(t, s)) != 2 {
		t.Errorf("prune after a second put = %+v, %v; files %v", res, err, blobFiles(t, s))
	}
}

// A blob put again is marked as put now, though its bytes stay as they
// were: an old one nobody names would otherwise be pruned under the mail
// about to name it.
func TestPutBlobAgainIsYoung(t *testing.T) {
	api, s := storetest.NewOwned(t)
	ctx := context.Background()
	ref, err := api.PutBlob(ctx, pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * store.BlobGrace)
	if err := os.Chtimes(s.BlobPath(ref.Hash), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutBlob(ctx, pngBytes); err != nil {
		t.Fatal(err)
	}
	if res, err := api.Prune(ctx, store.Retention{}, "jo"); err != nil || res.Blobs != 0 {
		t.Errorf("prune = %+v, %v", res, err)
	}
	if _, err := s.Blob(ref.Hash); err != nil {
		t.Errorf("the blob put again is gone: %v", err)
	}
}

// A note written with files names the blobs, in order, and the bead whole
// names them too; more than the bound, and one the yard does not hold,
// are refused with nothing written.
func TestAddNotedAsFiles(t *testing.T) {
	api, _ := storetest.NewOwned(t)
	ctx := context.Background()
	b, err := api.CreateBead(ctx, store.NewBead{Title: "the shed"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	png, err := api.PutBlob(ctx, pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	jpeg, err := api.PutBlob(ctx, jpegBytes)
	if err != nil {
		t.Fatal(err)
	}
	n, err := api.AddNotedAs(ctx, b.ID, "the shed, painted", "jo", "", png.Hash, jpeg.Hash)
	if err != nil || n.Seq == 0 {
		t.Fatalf("a note with files = %+v, %v", n, err)
	}
	if len(n.Files) != 2 || n.Files[0] != *png || n.Files[1] != *jpeg {
		t.Fatalf("the note carries %+v", n.Files)
	}
	notes, err := api.Notes(ctx, b.ID)
	if err != nil || len(notes) != 1 || len(notes[0].Files) != 2 {
		t.Fatalf("the bead's notes = %+v, %v", notes, err)
	}
	view, err := api.Bead(ctx, b.ID)
	if err != nil || len(view.Notes) != 1 || len(view.Notes[0].Files) != 2 || view.Notes[0].Files[0].Hash != png.Hash {
		t.Fatalf("the bead whole = %+v, %v", view, err)
	}

	many := make([]string, store.MaxNoteFiles+1)
	for i := range many {
		many[i] = png.Hash
	}
	if _, err := api.AddNotedAs(ctx, b.ID, "too many", "jo", "", many...); err == nil ||
		!strings.Contains(err.Error(), "a note carries at most 8 images, not 9") {
		t.Errorf("9 files = %v", err)
	}
	if _, err := api.AddNotedAs(ctx, b.ID, " \n\t ", "jo", "", png.Hash); err == nil ||
		!strings.Contains(err.Error(), "a note with images needs words") {
		t.Errorf("an image note without words = %v", err)
	}
	unknown := strings.Repeat("0", 64)
	if _, err := api.AddNotedAs(ctx, b.ID, "a ghost", "jo", "", unknown); err == nil ||
		!strings.Contains(err.Error(), "the yard does not hold it") {
		t.Errorf("an unknown hash = %v", err)
	}
	if notes, _ := api.Notes(ctx, b.ID); len(notes) != 1 {
		t.Errorf("what was refused was written: %+v", notes)
	}
}

// Prune keeps a blob a note names, past the grace, and sweeps one nothing
// names.
func TestPruneBlobsKeepsNoteFiles(t *testing.T) {
	api, s := storetest.NewOwned(t)
	ctx := context.Background()
	b, err := api.CreateBead(ctx, store.NewBead{Title: "the shed"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	named, err := api.PutBlob(ctx, pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	loose, err := api.PutBlob(ctx, jpegBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.AddNotedAs(ctx, b.ID, "the shed, painted", "jo", "", named.Hash); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * store.BlobGrace)
	for _, ref := range []*store.BlobRef{named, loose} {
		if err := os.Chtimes(s.BlobPath(ref.Hash), old, old); err != nil {
			t.Fatal(err)
		}
	}
	res, err := api.Prune(ctx, store.Retention{}, "jo")
	if err != nil || res.Blobs != 1 {
		t.Fatalf("prune = %+v, %v", res, err)
	}
	if got := blobFiles(t, s); len(got) != 1 || got[0] != named.Hash {
		t.Errorf("after the prune the yard holds %v, want the note's %s", got, named.Hash)
	}
}

// A note with files ends its block with the mail line, each image by its
// path in the home; a note without any reads as before.
func TestNoteBlocksFiles(t *testing.T) {
	at := time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)
	notes := []store.Note{
		{Bead: "yd-shed", Author: "jo", Text: "the shed, painted", At: at, Seq: 1,
			Files: []store.BlobRef{{Hash: hashOf(pngBytes), Type: "image/png", Bytes: int64(len(pngBytes))}}},
		{Bead: "yd-shed", Author: "jo", Text: "plain", At: at, Seq: 2},
	}
	blocks := store.NoteBlocks(notes, at, "/y")
	if len(blocks) != 2 {
		t.Fatalf("blocks = %q", blocks)
	}
	if want := "the shed, painted  1 image: /y/blobs/" + hashOf(pngBytes); !strings.HasSuffix(blocks[0], want) {
		t.Errorf("the note with files = %q, want it to end with %q", blocks[0], want)
	}
	if !strings.HasSuffix(blocks[1], "plain") || strings.Contains(blocks[1], "image") {
		t.Errorf("the plain note = %q", blocks[1])
	}
}

// The lines mail is shown with name each image by its path in the home,
// and say nothing of mail without one.
func TestFilesLine(t *testing.T) {
	a, b := store.BlobRef{Hash: "aa"}, store.BlobRef{Hash: "bb"}
	for want, got := range map[string]string{
		"":                                  store.FilesLine("/y", nil),
		"1 image: /y/blobs/aa":              store.FilesLine("/y", []store.BlobRef{a}),
		"2 images: /y/blobs/aa /y/blobs/bb": store.FilesLine("/y", []store.BlobRef{a, b}),
		"hello":                             store.WithFiles("hello", "/y", nil),
		"hello  1 image: /y/blobs/aa":       store.WithFiles("hello", "/y", []store.BlobRef{a}),
		"1 image: /y/blobs/bb":              store.WithFiles("", "/y", []store.BlobRef{b}),
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
