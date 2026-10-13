package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// What a type is read from is the first bytes, so these are images as far
// as the yard asks.
var (
	shotPNG  = []byte("\x89PNG\r\n\x1a\n" + "a picture of the door")
	shotJPEG = []byte("\xff\xd8\xff\xe0" + "another of the fence")
)

// blobFixture serves a yard whose web view reads the blobs where the yard
// keeps them, as production does: the store's home is the view's.
func blobFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	st := storetest.Open(t, filepath.Join(dir, "yard.db"))
	srv, err := web.NewServer(st, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	return &fixture{t: t, st: st, ts: ts, home: dir}
}

// /blobs/<hash> serves a blob a note on some bead names, with its type,
// and answers anything else as a missing blob: a mail's image, one the
// yard never held or no longer holds, a name that is no hash. The view
// has no login, so what it serves is only what its bead pages show.
func TestBlobRoute(t *testing.T) {
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door"}, "me")
	must(t, err)
	noted, err := f.st.PutBlob(ctx, shotPNG)
	must(t, err)
	mailed, err := f.st.PutBlob(ctx, shotJPEG)
	must(t, err)
	if _, err := f.st.AddUser(ctx, "sam", "me"); err != nil {
		t.Fatal(err)
	}
	must(t, f.st.Mail(ctx, "sam", "paint it", "me", mailed.Hash))
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, hung", "me", "", noted.Hash)
	must(t, err)

	r := f.get("/blobs/" + noted.Hash)
	if r.status != 200 || r.header.Get("Content-Type") != "image/png" || r.body != string(shotPNG) {
		t.Errorf("the note's image: status %d, type %q, %d bytes", r.status, r.header.Get("Content-Type"), len(r.body))
	}
	if etag := r.header.Get("ETag"); etag != `"`+noted.Hash+`"` {
		t.Errorf("ETag %q, want the hash", etag)
	}
	if r := f.do("HEAD", "/blobs/"+noted.Hash); r.status != 200 || r.header.Get("Content-Type") != "image/png" || r.body != "" {
		t.Errorf("HEAD: status %d, type %q, %d bytes", r.status, r.header.Get("Content-Type"), len(r.body))
	}
	// A note on a closed bead names still: its page shows the picture.
	_, err = f.st.CloseBead(ctx, b.ID, "hung", store.Expect{}, "me")
	must(t, err)
	if r := f.get("/blobs/" + noted.Hash); r.status != 200 {
		t.Errorf("the closed bead's image: status %d, want 200", r.status)
	}
	for _, hash := range []string{mailed.Hash, strings.Repeat("0", 64), "nope", strings.ToUpper(noted.Hash)} {
		if r := f.get("/blobs/" + hash); r.status != 404 {
			t.Errorf("/blobs/%s: status %d, want 404: %s", hash, r.status, r.body)
		}
	}
	must(t, os.Remove(store.BlobPath(f.home, noted.Hash)))
	if r := f.get("/blobs/" + noted.Hash); r.status != 404 {
		t.Errorf("what the yard no longer holds: status %d, want 404", r.status)
	}
	if r := f.do("POST", "/blobs/"+noted.Hash); r.status != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d, want 405", r.status)
	}
}

// The bead page draws each note's images below its words, with the note's
// words as their alt text; one the yard no longer holds is a line saying
// so, never a broken icon. The JSON names the files.
func TestBeadPageNoteImages(t *testing.T) {
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door"}, "me")
	must(t, err)
	ref, err := f.st.PutBlob(ctx, shotPNG)
	must(t, err)
	must(t, f.st.AddNote(ctx, b.ID, "plain words", "me"))
	_, err = f.st.AddNotedAs(ctx, b.ID, `the "door", hung <firmly>`, "me", "", ref.Hash)
	must(t, err)

	var got web.BeadPage
	f.getJSON("/beads/"+b.ID, &got)
	var files []store.BlobRef
	for _, n := range got.Notes {
		files = append(files, n.Files...)
	}
	if len(files) != 1 || files[0] != *ref {
		t.Fatalf("the JSON names %v, want %v", files, ref)
	}

	body := f.get("/beads/" + b.ID).body
	if want := `<img src="/blobs/` + ref.Hash + `" alt="the &#34;door&#34;, hung &lt;firmly&gt;">`; !strings.Contains(body, want) {
		t.Errorf("the page lacks %s", want)
	}
	if n := strings.Count(body, "<img"); n != 1 {
		t.Errorf("the page draws %d images, want 1", n)
	}

	must(t, os.Remove(store.BlobPath(f.home, ref.Hash)))
	body = f.get("/beads/" + b.ID).body
	if want := `image ` + ref.Hash[:8] + `: the yard no longer has it`; !strings.Contains(body, want) {
		t.Errorf("the page lacks %q", want)
	}
	if strings.Contains(body, "<img") {
		t.Error("the page draws what the yard no longer holds")
	}
}
