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
	shotPNG   = []byte("\x89PNG\r\n\x1a\n" + "a picture of the door")
	shotJPEG  = []byte("\xff\xd8\xff\xe0" + "another of the fence")
	pageHTML  = []byte("<!DOCTYPE html><html><body>a door</body></html>")
	pageFence = []byte("<!DOCTYPE html><html><body>a fence</body></html>")
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
// and answers anything else as a missing blob: a mail's file, one the
// yard never held or no longer holds, a name that is no hash. The view
// has no login, so what it serves is only what its bead pages show. A
// page opens under the page policy, inert; an image keeps the yard
// page's policy.
func TestBlobRoute(t *testing.T) {
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door"}, "me")
	must(t, err)
	noted, err := f.st.PutBlob(ctx, shotPNG)
	must(t, err)
	mailed, err := f.st.PutBlob(ctx, shotJPEG)
	must(t, err)
	page, err := f.st.PutBlob(ctx, pageHTML)
	must(t, err)
	mailedPage, err := f.st.PutBlob(ctx, pageFence)
	must(t, err)
	if _, err := f.st.AddUser(ctx, "sam", "me"); err != nil {
		t.Fatal(err)
	}
	must(t, f.st.Mail(ctx, "sam", "paint it", "me", mailed.Hash))
	must(t, f.st.Mail(ctx, "sam", "the fence, drawn", "me", mailedPage.Hash))
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, hung", "me", "", noted.Hash, page.Hash)
	must(t, err)

	r := f.get("/blobs/" + noted.Hash)
	if r.status != 200 || r.header.Get("Content-Type") != "image/png" || r.body != string(shotPNG) {
		t.Errorf("the note's image: status %d, type %q, %d bytes", r.status, r.header.Get("Content-Type"), len(r.body))
	}
	if etag := r.header.Get("ETag"); etag != `"`+noted.Hash+`"` {
		t.Errorf("ETag %q, want the hash", etag)
	}
	if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("the image's Content-Security-Policy %q, want the yard page's", csp)
	}
	r = f.get("/blobs/" + page.Hash)
	if r.status != 200 || r.header.Get("Content-Type") != "text/html; charset=utf-8" || r.body != string(pageHTML) {
		t.Errorf("the note's page: status %d, type %q, %d bytes", r.status, r.header.Get("Content-Type"), len(r.body))
	}
	if etag := r.header.Get("ETag"); etag != `"`+page.Hash+`"` {
		t.Errorf("the page's ETag %q, want the hash", etag)
	}
	assertPagePolicy(t, r.header.Get("Content-Security-Policy"))
	for _, h := range [][2]string{
		{"X-Content-Type-Options", "nosniff"},
		{"Referrer-Policy", "no-referrer"},
	} {
		if got := r.header.Get(h[0]); got != h[1] {
			t.Errorf("the page's %s %q, want %q", h[0], got, h[1])
		}
	}
	if r := f.do("HEAD", "/blobs/"+noted.Hash); r.status != 200 || r.header.Get("Content-Type") != "image/png" || r.body != "" {
		t.Errorf("HEAD: status %d, type %q, %d bytes", r.status, r.header.Get("Content-Type"), len(r.body))
	}
	r = f.do("HEAD", "/blobs/"+page.Hash)
	if r.status != 200 || r.header.Get("Content-Type") != "text/html; charset=utf-8" || r.body != "" {
		t.Errorf("HEAD the page: status %d, type %q, %d bytes", r.status, r.header.Get("Content-Type"), len(r.body))
	}
	assertPagePolicy(t, r.header.Get("Content-Security-Policy"))
	// A note on a closed bead names still: its page shows the picture.
	_, err = f.st.CloseBead(ctx, b.ID, "hung", store.Expect{}, "me")
	must(t, err)
	if r := f.get("/blobs/" + noted.Hash); r.status != 200 {
		t.Errorf("the closed bead's image: status %d, want 200", r.status)
	}
	// A mail's page answers as a missing blob, like a mail's image.
	for _, hash := range []string{mailed.Hash, mailedPage.Hash, strings.Repeat("0", 64), "nope", strings.ToUpper(noted.Hash)} {
		if r := f.get("/blobs/" + hash); r.status != 404 {
			t.Errorf("/blobs/%s: status %d, want 404: %s", hash, r.status, r.body)
		}
	}
	must(t, os.Remove(store.BlobPath(f.home, noted.Hash)))
	if r := f.get("/blobs/" + noted.Hash); r.status != 404 {
		t.Errorf("what the yard no longer holds: status %d, want 404", r.status)
	}
	must(t, os.Remove(store.BlobPath(f.home, page.Hash)))
	if r := f.get("/blobs/" + page.Hash); r.status != 404 {
		t.Errorf("the page the yard no longer holds: status %d, want 404", r.status)
	}
	if r := f.do("POST", "/blobs/"+noted.Hash); r.status != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d, want 405", r.status)
	}
}

// assertPagePolicy holds the response's policy to the page policy of the
// design (section 3.3), exactly: scripts dead, and an external image,
// stylesheet, script, fetch and frame each forbidden by the headers.
func assertPagePolicy(t *testing.T, csp string) {
	t.Helper()
	if want := "sandbox allow-top-navigation-by-user-activation; default-src 'none'; script-src 'none'; connect-src 'none'; frame-src 'none'; child-src 'none'; object-src 'none'; font-src 'none'; media-src 'none'; img-src data:; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"; csp != want {
		t.Fatalf("Content-Security-Policy %q, want %q", csp, want)
	}
	got := map[string]string{}
	for _, d := range strings.Split(csp, ";") {
		name, vals, _ := strings.Cut(strings.TrimSpace(d), " ")
		got[name] = vals
	}
	for directive, want := range map[string]string{
		// Scripts dead twice over: the sandbox without allow-scripts,
		// and script-src 'none'.
		"sandbox":    "allow-top-navigation-by-user-activation",
		"script-src": "'none'",
		// An external image, stylesheet, fetch and frame each
		// forbidden: only data: images and inline styles render.
		"img-src":     "data:",
		"style-src":   "'unsafe-inline'",
		"connect-src": "'none'",
		"frame-src":   "'none'",
		"child-src":   "'none'",
		"default-src": "'none'",
		// Never embedded, never posted from.
		"frame-ancestors": "'none'",
		"form-action":     "'none'",
		"base-uri":        "'none'",
	} {
		if got[directive] != want {
			t.Errorf("%s %q, want %q", directive, got[directive], want)
		}
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

// A note's page stands under its words as a link opening the bytes
// top-level; one the yard no longer holds is a line saying so, never a
// broken link. The JSON names the files, and the bead's own page keeps
// the yard page's policy.
func TestBeadPageNotePages(t *testing.T) {
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door"}, "me")
	must(t, err)
	ref, err := f.st.PutBlob(ctx, pageHTML)
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, hung", "me", "", ref.Hash)
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

	r := f.get("/beads/" + b.ID)
	if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "sandbox") {
		t.Errorf("the bead's own Content-Security-Policy %q, want the yard page's", csp)
	}
	if want := `<a href="/blobs/` + ref.Hash + `">page ` + ref.Hash[:8] + `</a>`; !strings.Contains(r.body, want) {
		t.Errorf("the page lacks %s", want)
	}
	if strings.Contains(r.body, "<img") {
		t.Error("the page draws the page as an image")
	}

	must(t, os.Remove(store.BlobPath(f.home, ref.Hash)))
	body := f.get("/beads/" + b.ID).body
	if want := `page ` + ref.Hash[:8] + `: the yard no longer has it`; !strings.Contains(body, want) {
		t.Errorf("the page lacks %q", want)
	}
	if strings.Contains(body, "/blobs/"+ref.Hash) {
		t.Error("the page links what the yard no longer holds")
	}
}
