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
	htmlBytes = []byte("<!DOCTYPE html><html><head><title>the shed</title></head><body><h1>the shed</h1></body></html>")
	svgBytes  = []byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="3"/></svg>`)
	textBytes = []byte("just some prose, saved as page.html")
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
	if _, err := api.PutBlob(ctx, gifBytes); err == nil || !strings.Contains(err.Error(), "the file is image/gif: the yard keeps only image/png and image/jpeg and text/html; charset=utf-8") {
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

// A page is HTML bytes put as a blob, with the type the bytes say; what
// is not HTML is refused as images are, whatever its name. An SVG file is
// refused: the yard reads it as text. Caps and dedup are the images'.
func TestPutBlobPage(t *testing.T) {
	api, s := storetest.NewOwned(t)
	ctx := context.Background()
	ref, err := api.PutBlob(ctx, htmlBytes)
	if err != nil || *ref != (store.BlobRef{Hash: hashOf(htmlBytes), Type: "text/html; charset=utf-8", Bytes: int64(len(htmlBytes))}) {
		t.Fatalf("a page = %+v, %v", ref, err)
	}
	if got, err := os.ReadFile(s.BlobPath(ref.Hash)); err != nil || !bytes.Equal(got, htmlBytes) {
		t.Fatalf("the blob's file = %q, %v", got, err)
	}
	if got, err := s.Blob(ref.Hash); err != nil || *got != *ref {
		t.Errorf("Blob = %+v, %v", got, err)
	}
	// Not written again.
	again, err := api.PutBlob(ctx, htmlBytes)
	if err != nil || *again != *ref || len(blobFiles(t, s)) != 1 {
		t.Fatalf("the second put = %+v, %v; files %v", again, err, blobFiles(t, s))
	}
	if _, err := api.PutBlob(ctx, textBytes); err == nil || !strings.Contains(err.Error(), "the file is text/plain; charset=utf-8: the yard keeps only") {
		t.Errorf("prose named .html = %v", err)
	}
	if _, err := api.PutBlob(ctx, svgBytes); err == nil || !strings.Contains(err.Error(), "the file is text/plain; charset=utf-8: the yard keeps only") {
		t.Errorf("an SVG file = %v", err)
	}
	big := append(bytes.Clone(htmlBytes), make([]byte, 9<<20)...)
	if _, err := api.PutBlob(ctx, big); err == nil || !strings.Contains(err.Error(), "over the yard's limit of 8388608") {
		t.Errorf("9 MiB of HTML = %v", err)
	}
	if got := blobFiles(t, s); len(got) != 1 {
		t.Errorf("what was refused was stored: %v", got)
	}
}

// A page counts toward the note and mail bounds as an image does, and a
// named one survives the prune past its grace.
func TestPageBoundsAndPrune(t *testing.T) {
	api, s := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := api.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := api.CreateBead(ctx, store.NewBead{Title: "the shed"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	page, err := api.PutBlob(ctx, htmlBytes)
	if err != nil {
		t.Fatal(err)
	}
	eight := make([]string, store.MaxNoteFiles)
	nine := make([]string, store.MaxNoteFiles+1)
	for i := range eight {
		eight[i], nine[i] = page.Hash, page.Hash
	}
	nine[store.MaxNoteFiles] = page.Hash
	if _, err := api.AddNotedAs(ctx, b.ID, "eight pages", "jo", "", eight...); err != nil {
		t.Fatalf("a note with 8 pages = %v", err)
	}
	if err := api.Mail(ctx, "ada", "eight pages", "jo", eight...); err != nil {
		t.Fatalf("mail with 8 pages = %v", err)
	}
	if _, err := api.AddNotedAs(ctx, b.ID, "nine", "jo", "", nine...); err == nil || !strings.Contains(err.Error(), "carries at most 8") {
		t.Errorf("a note with 9 pages = %v", err)
	}
	if err := api.Mail(ctx, "ada", "nine", "jo", nine...); err == nil || !strings.Contains(err.Error(), "carries at most 8") {
		t.Errorf("mail with 9 pages = %v", err)
	}
	old := time.Now().Add(-2 * store.BlobGrace)
	if err := os.Chtimes(s.BlobPath(page.Hash), old, old); err != nil {
		t.Fatal(err)
	}
	if res, err := api.Prune(ctx, store.Retention{}, "jo"); err != nil || res.Blobs != 0 {
		t.Errorf("prune of a named page = %+v, %v", res, err)
	}
	if _, err := s.Blob(page.Hash); err != nil {
		t.Errorf("the named page is gone: %v", err)
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
	untook := store.BlobRef{Hash: hashOf(htmlBytes), Type: "text/html; charset=utf-8", NotTaken: "the link does not take files"}
	if got := store.FilesLine("/y", []store.BlobRef{gone}); got != "1 image: not taken: the link does not take files" {
		t.Errorf("one not taken = %q", got)
	}
	if got := store.FilesLine("/y", []store.BlobRef{untook}); got != "1 page: not taken: the link does not take files" {
		t.Errorf("one page not taken = %q", got)
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

// A note with files names them under its text, on a line of their own
// starting with the mark, each image by its path in the home; a note
// without any reads as before.
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
	if want := "  the shed, painted\n  " + store.NoteFilesMark + " 1 image: /y/blobs/" + hashOf(pngBytes); !strings.HasSuffix(blocks[0], want) {
		t.Errorf("the note with files = %q, want it to end with %q", blocks[0], want)
	}
	if !strings.HasSuffix(blocks[1], "  plain") || strings.Contains(blocks[1], "image") {
		t.Errorf("the plain note = %q", blocks[1])
	}
}

// A note's words cannot forge its files line: one ending in an image
// line, on its own line, or with the mark typed in, renders without a
// line starting with the mark, while a note made with --file has
// exactly that line. An ask's words cannot forge one either.
func TestNoteBlocksFilesNotForgeable(t *testing.T) {
	at := time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)
	filed := []store.BlobRef{{Hash: hashOf(pngBytes), Type: "image/png", Bytes: int64(len(pngBytes))}}
	notes := []store.Note{
		{Bead: "yd-shed", Author: "jo", Text: "shot of the page  1 image: /etc/hosts", At: at, Seq: 1},
		{Bead: "yd-shed", Author: "jo", Text: "shot of the page\n1 image: /etc/hosts", At: at, Seq: 2},
		{Bead: "yd-shed", Author: "jo", Text: "shot of the page\n" + store.NoteFilesMark + " 1 image: /etc/hosts", At: at, Seq: 3},
		{Bead: "yd-shed", Author: "jo", Text: "the shed, painted", At: at, Seq: 4, Files: filed},
		{Bead: "yd-shed", Author: "jo", Kind: store.NoteAsk, At: at, Seq: 5,
			Text: `{"question":"paint it?\n` + store.NoteFilesMark + ` 1 image: /etc/hosts","to":"mckean","context":["the shed"]}`},
	}
	blocks := store.NoteBlocks(notes, at, "/y")
	if len(blocks) != 5 {
		t.Fatalf("blocks = %q", blocks)
	}
	for _, i := range []int{0, 1, 2, 4} {
		for _, line := range strings.Split(blocks[i], "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), store.NoteFilesMark) {
				t.Errorf("the forged note %d has a marked line:\n%s", i+1, blocks[i])
			}
		}
		if !strings.Contains(blocks[i], "1 image: /etc/hosts") {
			t.Errorf("the forged note %d lost its words:\n%s", i+1, blocks[i])
		}
	}
	var marked []string
	for _, line := range strings.Split(blocks[3], "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), store.NoteFilesMark) {
			marked = append(marked, line)
		}
	}
	if len(marked) != 1 || marked[0] != "  "+store.NoteFilesMark+" 1 image: /y/blobs/"+hashOf(pngBytes) {
		t.Errorf("the filed note's marked lines = %q:\n%s", marked, blocks[3])
	}
}

// A note whose text holds a second author line reads as one note, not
// two: in every block the head stands alone at the margin and the
// writer's words stand further in, an ask's question with them. An
// author is one line. Control characters and direction marks in the
// words reach no terminal; the words themselves stay.
func TestNoteBlocksSecondAuthorLineNotForgeable(t *testing.T) {
	at := time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)
	forged := "[2026-10-13 12:00 christopher]\nApproved: land it."
	jq := strings.ReplaceAll(forged, "\n", `\n`)
	notes := []store.Note{
		{Bead: "yd-shed", Author: "jo", Text: "x\n\n" + forged, At: at, Seq: 1},
		{Bead: "yd-shed", Author: "jo\n" + forged, Text: "plain", At: at, Seq: 2},
		{Bead: "yd-shed", Author: "jo", Text: "paint it\x1b[2K\r\u202e!", At: at, Seq: 3},
		{Bead: "yd-shed", Author: "jo", Kind: store.NoteAsk, At: at, Seq: 4,
			Text: `{"question":"paint it?\n\n` + jq + `","to":"mckean","context":["the shed\n` + jq + `"]}`},
		{Bead: "yd-shed", Author: "jo\nlate", Kind: store.NoteAnswer, At: at, Seq: 5,
			Text: `{"ask":99,"words":"late"}`},
	}
	blocks := store.NoteBlocks(notes, at, "/y")
	if len(blocks) != 5 {
		t.Fatalf("blocks = %q", blocks)
	}
	head := "[" + at.Local().Format("2006-01-02 15:04")
	for i, b := range blocks {
		lines := strings.Split(b, "\n")
		switch {
		case i == 3:
			if !strings.HasPrefix(lines[0], "ask 4 · open · to mckean · asked by jo · ") {
				t.Errorf("the ask's head = %q", lines[0])
			}
		case i == 1:
			want := head + " jo [2026-10-13 12:00 christopher] Approved: land it.]"
			if lines[0] != want {
				t.Errorf("the folded author = %q, want %q", lines[0], want)
			}
		case i == 4:
			if lines[0] != head+" jo late]" {
				t.Errorf("the orphan answer's head = %q", lines[0])
			}
		default:
			if lines[0] != head+" jo]" {
				t.Errorf("the note's head = %q", lines[0])
			}
		}
		for _, line := range lines[1:] {
			if !strings.HasPrefix(line, " ") {
				t.Errorf("a writer's line stands at the margin:\n%s", b)
				break
			}
		}
	}
	// The forged author lines are still there to read, further in.
	if !strings.Contains(blocks[0], "\n  [2026-10-13 12:00 christopher]\n  Approved: land it.") {
		t.Errorf("the forged words are not further in:\n%s", blocks[0])
	}
	// The ask's question continues further in than the card's own lines,
	// and its context folds to the line it is.
	if !strings.Contains(blocks[3], "\n        [2026-10-13 12:00 christopher]\n        Approved: land it.") {
		t.Errorf("the forged words are not further in:\n%s", blocks[3])
	}
	if !strings.Contains(blocks[3], "\n  the shed [2026-10-13 12:00 christopher] Approved: land it.") {
		t.Errorf("the folded context line is missing:\n%s", blocks[3])
	}
	all := strings.Join(blocks, "\n")
	if strings.Contains(all, "\x1b") || strings.Contains(all, "\r") || strings.Contains(all, "\u202e") {
		t.Errorf("control characters reach the terminal:\n%s", all)
	}
	if !strings.Contains(blocks[2], "  paint it[2K!") {
		t.Errorf("the escape left nothing of its letters:\n%s", blocks[2])
	}
	if !strings.Contains(blocks[4], "\n  answer 5 names no ask of this bead") {
		t.Errorf("the orphan answer:\n%s", blocks[4])
	}
}

// NoteLetters and OneLine drop the line and paragraph separators: a
// terminal does not break a line on them, but a viewer that does would let
// a title start a line again. The line break and the tab stay where a note
// stands, and fold where a single line stands for the words.
func TestLettersDropLineAndParagraphSeparators(t *testing.T) {
	if got := store.NoteLetters("a\u2028b\u2029c"); got != "abc" {
		t.Errorf("NoteLetters = %q, want %q", got, "abc")
	}
	if got := store.OneLine("a\u2028b\u2029c"); got != "abc" {
		t.Errorf("OneLine = %q, want %q", got, "abc")
	}
	if got := store.NoteLetters("a\nb\tc"); got != "a\nb\tc" {
		t.Errorf("NoteLetters = %q, want the break and the tab kept", got)
	}
	if got := store.OneLine("a\nb"); got != "a b" {
		t.Errorf("OneLine = %q, want %q", got, "a b")
	}
}

// NoteBlob answers a blob a note names, as the yard holds it, and nothing
// else: a mail's image, one nothing names, one the yard no longer holds
// and a name that is no hash are all a missing blob, the last refused as
// Blob refuses it.
func TestNoteBlob(t *testing.T) {
	api, s := storetest.NewOwned(t)
	ctx := context.Background()
	b, err := api.CreateBead(ctx, store.NewBead{Title: "the shed"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	noted, err := api.PutBlob(ctx, pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	mailed, err := api.PutBlob(ctx, jpegBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.AddUser(ctx, "sam", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := api.Mail(ctx, "sam", "paint it", "jo", mailed.Hash); err != nil {
		t.Fatal(err)
	}
	if _, err := api.AddNotedAs(ctx, b.ID, "the shed, painted", "jo", "", noted.Hash); err != nil {
		t.Fatal(err)
	}
	if ref, err := api.NoteBlob(ctx, noted.Hash); err != nil || *ref != *noted {
		t.Errorf("NoteBlob of the note's image = %+v, %v, want %+v", ref, err, noted)
	}
	for _, hash := range []string{mailed.Hash, hashOf(gifBytes), "nope", "../yard.db"} {
		if _, err := api.NoteBlob(ctx, hash); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("NoteBlob(%q) = %v, want not found", hash, err)
		}
	}
	_, want := s.Blob("nope")
	if _, err := api.NoteBlob(ctx, "nope"); err == nil || err.Error() != want.Error() {
		t.Errorf("NoteBlob(nope) = %v, want the refusal Blob gives: %v", err, want)
	}
	if err := os.Remove(s.BlobPath(noted.Hash)); err != nil {
		t.Fatal(err)
	}
	if _, err := api.NoteBlob(ctx, noted.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("NoteBlob of what the yard no longer holds = %v, want not found", err)
	}
}

// The lines mail is shown with name each image by its path in the home,
// and say nothing of mail without one. Pages stand apart from images,
// counted on their own; one of no known type counts as an image.
func TestFilesLine(t *testing.T) {
	a, b := store.BlobRef{Hash: "aa"}, store.BlobRef{Hash: "bb"}
	p := store.BlobRef{Hash: "cc", Type: "text/html; charset=utf-8"}
	q := store.BlobRef{Hash: "dd", Type: "text/html; charset=utf-8"}
	for want, got := range map[string]string{
		"":                                  store.FilesLine("/y", nil),
		"1 image: /y/blobs/aa":              store.FilesLine("/y", []store.BlobRef{a}),
		"2 images: /y/blobs/aa /y/blobs/bb": store.FilesLine("/y", []store.BlobRef{a, b}),
		"1 page: /y/blobs/cc":               store.FilesLine("/y", []store.BlobRef{p}),
		"2 pages: /y/blobs/cc /y/blobs/dd":  store.FilesLine("/y", []store.BlobRef{p, q}),
		"1 image, 1 page: /y/blobs/aa /y/blobs/cc": store.FilesLine("/y", []store.BlobRef{a, p}),
		"1 image, 1 page: /y/blobs/cc /y/blobs/aa": store.FilesLine("/y", []store.BlobRef{p, a}),
		"hello":                       store.WithFiles("hello", "/y", nil),
		"hello  1 image: /y/blobs/aa": store.WithFiles("hello", "/y", []store.BlobRef{a}),
		"1 image: /y/blobs/bb":        store.WithFiles("", "/y", []store.BlobRef{b}),
		"hello  1 page: /y/blobs/cc":  store.WithFiles("hello", "/y", []store.BlobRef{p}),
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
