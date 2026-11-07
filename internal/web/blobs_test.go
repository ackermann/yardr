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

// as serves f's yard on its own view, acting as the user as: the store's
// home is the view's, as blobFixture's is.
func (f *fixture) as(t *testing.T, as string) *fixture {
	t.Helper()
	srv, err := web.NewServer(f.st, f.home, nil)
	if err != nil {
		t.Fatal(err)
	}
	token := actAs(t, srv, as)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	acting := &fixture{t: t, st: f.st, ts: ts, home: f.home}
	acting.signIn(srv, token)
	return acting
}

// /blobs/<hash> serves a blob a note on some bead names, with its type,
// to anyone; on a view that acts it serves a mail's file too, to the
// mail's sender or recipient. Anything else answers as a missing blob:
// one the yard never held or no longer holds, a name that is no hash, a
// mail's file to anyone else or where the view acts as nobody. A page
// opens under the page policy, inert; an image keeps the yard page's
// policy.
func TestBlobRoute(t *testing.T) {
	t.Parallel()
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
	must(t, f.st.Mail(ctx, "sam", "paint it", "me", store.BlobFile{Hash: mailed.Hash}))
	must(t, f.st.Mail(ctx, "sam", "the fence, drawn", "me", store.BlobFile{Hash: mailedPage.Hash}))
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, hung", "me", "", store.BlobFile{Hash: noted.Hash}, store.BlobFile{Hash: page.Hash})
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
		{"Referrer-Policy", "same-origin"},
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
	// A photo a kept hook delivery carried is the yard's, as a note's
	// file is: served wherever the view acts as anybody or nobody.
	// blobFixture's store is the owning one, which the receiver records
	// through.
	own := f.st.(*store.Store)
	hooked, err := own.PutBlob(ctx, []byte("\x89PNG\r\n\x1a\n the shed from the phone"))
	must(t, err)
	seq, _, err := own.RecordHook(ctx, "telegram", map[string]any{"source": "telegram", "type": "message"}, []byte(`{}`), nil)
	must(t, err)
	must(t, own.RecordDeliveryFiles(ctx, seq, []store.BlobRef{{Hash: hooked.Hash}}))
	if r := f.get("/blobs/" + hooked.Hash); r.status != 200 || r.header.Get("Content-Type") != "image/png" {
		t.Errorf("the delivery's image: status %d, type %q", r.status, r.header.Get("Content-Type"))
	}
	// A note on a closed bead names still: its page shows the picture.
	_, err = f.st.CloseBead(ctx, b.ID, "hung", store.Expect{}, "me")
	must(t, err)
	if r := f.get("/blobs/" + noted.Hash); r.status != 200 {
		t.Errorf("the closed bead's image: status %d, want 200", r.status)
	}
	// A mail's file answers as a missing blob where the view acts as
	// nobody; where it acts, to the mail's sender and recipient, and to
	// nobody else.
	for _, hash := range []string{mailed.Hash, mailedPage.Hash, strings.Repeat("0", 64), "nope", strings.ToUpper(noted.Hash)} {
		if r := f.get("/blobs/" + hash); r.status != 404 {
			t.Errorf("/blobs/%s: status %d, want 404: %s", hash, r.status, r.body)
		}
	}
	sam, sender, stranger := f.as(t, "sam"), f.as(t, "me"), f.as(t, "kay")
	for _, v := range []struct {
		name string
		f    *fixture
	}{{"sam", sam}, {"me", sender}} {
		r := v.f.get("/blobs/" + mailed.Hash)
		if r.status != 200 || r.header.Get("Content-Type") != "image/jpeg" || r.body != string(shotJPEG) {
			t.Errorf("the mail's image to %s: status %d, type %q, %d bytes", v.name, r.status, r.header.Get("Content-Type"), len(r.body))
		}
		if etag := r.header.Get("ETag"); etag != `"`+mailed.Hash+`"` {
			t.Errorf("the mail's image to %s: ETag %q, want the hash", v.name, etag)
		}
		if csp := r.header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("the mail's image to %s: Content-Security-Policy %q, want the yard page's", v.name, csp)
		}
		r = v.f.get("/blobs/" + mailedPage.Hash)
		if r.status != 200 || r.header.Get("Content-Type") != "text/html; charset=utf-8" || r.body != string(pageFence) {
			t.Errorf("the mail's page to %s: status %d, type %q, %d bytes", v.name, r.status, r.header.Get("Content-Type"), len(r.body))
		}
		assertPagePolicy(t, r.header.Get("Content-Security-Policy"))
	}
	for _, hash := range []string{mailed.Hash, mailedPage.Hash} {
		if r := stranger.get("/blobs/" + hash); r.status != 404 {
			t.Errorf("the mail's file to a stranger: status %d, want 404: %s", r.status, r.body)
		}
	}
	if r := sam.do("HEAD", "/blobs/"+mailed.Hash); r.status != 200 || r.header.Get("Content-Type") != "image/jpeg" || r.body != "" {
		t.Errorf("HEAD the mail's image: status %d, type %q, %d bytes", r.status, r.header.Get("Content-Type"), len(r.body))
	}
	must(t, os.Remove(store.BlobPath(f.home, noted.Hash)))
	if r := f.get("/blobs/" + noted.Hash); r.status != 404 {
		t.Errorf("what the yard no longer holds: status %d, want 404", r.status)
	}
	must(t, os.Remove(store.BlobPath(f.home, page.Hash)))
	if r := f.get("/blobs/" + page.Hash); r.status != 404 {
		t.Errorf("the page the yard no longer holds: status %d, want 404", r.status)
	}
	must(t, os.Remove(store.BlobPath(f.home, mailed.Hash)))
	if r := sam.get("/blobs/" + mailed.Hash); r.status != 404 {
		t.Errorf("the mail's image the yard no longer holds: status %d, want 404", r.status)
	}
	if r := f.do("POST", "/blobs/"+noted.Hash); r.status != http.StatusMethodNotAllowed {
		t.Errorf("POST: status %d, want 405", r.status)
	}
}

// A file downloads, never inline: /blobs/<hash> answers it with
// Content-Disposition: attachment and the sender's name encoded, by kind
// and not by type — a PDF, an SVG and prose all download. An image and a
// page carry no such header, served as ever.
func TestBlobFileDownloads(t *testing.T) {
	t.Parallel()
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "the report"}, "me")
	must(t, err)
	pdf := []byte("%PDF-1.4 the report")
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="3"/></svg>`)
	shot, err := f.st.PutBlob(ctx, shotPNG)
	must(t, err)
	report, err := f.st.PutBlob(ctx, pdf)
	must(t, err)
	diagram, err := f.st.PutBlob(ctx, svg)
	must(t, err)
	if _, err := f.st.AddNotedAs(ctx, b.ID, "the report, filed", "me", "",
		store.BlobFile{Hash: report.Hash, Name: "report.pdf"},
		store.BlobFile{Hash: diagram.Hash, Name: "diagram.svg"},
		store.BlobFile{Hash: shot.Hash, Name: "shot.png"}); err != nil {
		t.Fatal(err)
	}

	r := f.get("/blobs/" + report.Hash)
	if r.status != 200 || r.body != string(pdf) {
		t.Fatalf("the PDF: status %d, %d bytes", r.status, len(r.body))
	}
	if got, want := r.header.Get("Content-Type"), "application/pdf"; got != want {
		t.Errorf("the PDF's Content-Type %q, want %q", got, want)
	}
	if got, want := r.header.Get("Content-Disposition"), "attachment; filename*=UTF-8''report.pdf"; got != want {
		t.Errorf("the PDF's Content-Disposition %q, want %q", got, want)
	}
	r = f.get("/blobs/" + diagram.Hash)
	if r.status != 200 || r.body != string(svg) {
		t.Fatalf("the SVG: status %d, %d bytes", r.status, len(r.body))
	}
	if got := r.header.Get("Content-Disposition"); !strings.HasPrefix(got, "attachment; filename*=UTF-8''diagram.svg") {
		t.Errorf("the SVG's Content-Disposition %q, want an attachment", got)
	}
	// An image and a page carry no such header: served as ever, inline.
	for _, hash := range []string{shot.Hash} {
		if got := f.get("/blobs/" + hash).header.Get("Content-Disposition"); got != "" {
			t.Errorf("the image's Content-Disposition %q, want none", got)
		}
	}
	// The name is a sender's word, never a header's: only attr-chars pass
	// through, the rest is percent-encoded, so no quote, semicolon or
	// line break reaches the header.
	tricky, err := f.st.PutBlob(ctx, []byte("%PDF-1.4 tricky"))
	must(t, err)
	if _, err := f.st.AddNotedAs(ctx, b.ID, "tricky", "me", "", store.BlobFile{Hash: tricky.Hash, Name: `a"b;c d%e€.pdf`}); err != nil {
		t.Fatal(err)
	}
	r = f.get("/blobs/" + tricky.Hash)
	if got, want := r.header.Get("Content-Disposition"), "attachment; filename*=UTF-8''a%22b%3Bc%20d%25e%E2%82%AC.pdf"; got != want {
		t.Errorf("the tricky name's Content-Disposition %q, want %q", got, want)
	}
	// Empty, for a paste, downloads under the URL's hash.
	plain, err := f.st.PutBlob(ctx, []byte("%PDF-1.4 plain"))
	must(t, err)
	if _, err := f.st.AddNotedAs(ctx, b.ID, "plain", "me", "", store.BlobFile{Hash: plain.Hash}); err != nil {
		t.Fatal(err)
	}
	if got := f.get("/blobs/" + plain.Hash).header.Get("Content-Disposition"); got != "attachment" {
		t.Errorf("the paste's Content-Disposition %q, want %q", got, "attachment")
	}
}

// One blob can carry different names on different letters while the route
// keys by hash, so the name is in the URL: /blobs/<hash>/<name> serves
// the blob under that name when the name passes the rule; a name that
// fails it is a 404, as is a hash no letter names.
func TestBlobNamedURL(t *testing.T) {
	t.Parallel()
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "the report"}, "me")
	must(t, err)
	pdf := []byte("%PDF-1.4 the report")
	report, err := f.st.PutBlob(ctx, pdf)
	must(t, err)
	if _, err := f.st.AddNotedAs(ctx, b.ID, "the report, filed", "me", "", store.BlobFile{Hash: report.Hash, Name: "report.pdf"}); err != nil {
		t.Fatal(err)
	}

	r := f.get("/blobs/" + report.Hash + "/report.pdf")
	if r.status != 200 || r.body != string(pdf) {
		t.Fatalf("the named file: status %d, %d bytes", r.status, len(r.body))
	}
	if got, want := r.header.Get("Content-Disposition"), "attachment; filename*=UTF-8''report.pdf"; got != want {
		t.Errorf("the named file's Content-Disposition %q, want %q", got, want)
	}
	// Under whatever name the URL carries, when it passes the rule —
	// even one no letter gave it.
	r = f.get("/blobs/" + report.Hash + "/from-the-board.pdf")
	if r.status != 200 {
		t.Fatalf("another name: status %d, want 200", r.status)
	}
	if got, want := r.header.Get("Content-Disposition"), "attachment; filename*=UTF-8''from-the-board.pdf"; got != want {
		t.Errorf("another name's Content-Disposition %q, want %q", got, want)
	}
	// An escaped name serves decoded.
	r = f.get("/blobs/" + report.Hash + "/a%22b%3Bc%20d.pdf")
	if got, want := r.header.Get("Content-Disposition"), "attachment; filename*=UTF-8''a%22b%3Bc%20d.pdf"; r.status != 200 || got != want {
		t.Errorf("the escaped name: status %d, Content-Disposition %q, want %q", r.status, got, want)
	}
	// A name that fails the rule is a 404: a slash, a line break, too long.
	for _, name := range []string{"a%2Fb.pdf", "a%0Ab.pdf", strings.Repeat("a", 256) + ".pdf"} {
		if r := f.get("/blobs/" + report.Hash + "/" + name); r.status != 404 {
			t.Errorf("/blobs/<hash>/%s: status %d, want 404: %s", name, r.status, r.body)
		}
	}
	// The hash must still be one a letter names.
	if r := f.get("/blobs/" + strings.Repeat("0", 64) + "/report.pdf"); r.status != 404 {
		t.Errorf("a hash nothing names with a name: status %d, want 404", r.status)
	}
	// An image under a name serves as ever, inline and nameless.
	shot, err := f.st.PutBlob(ctx, shotPNG)
	must(t, err)
	if _, err := f.st.AddNotedAs(ctx, b.ID, "the shed", "me", "", store.BlobFile{Hash: shot.Hash, Name: "shot.png"}); err != nil {
		t.Fatal(err)
	}
	if r := f.get("/blobs/" + shot.Hash + "/shot.png"); r.status != 200 || r.header.Get("Content-Disposition") != "" {
		t.Errorf("the named image: status %d, Content-Disposition %q", r.status, r.header.Get("Content-Disposition"))
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
	t.Parallel()
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door"}, "me")
	must(t, err)
	ref, err := f.st.PutBlob(ctx, shotPNG)
	must(t, err)
	must(t, f.st.AddNote(ctx, b.ID, "plain words", "me"))
	_, err = f.st.AddNotedAs(ctx, b.ID, `the "door", hung <firmly>`, "me", "", store.BlobFile{Hash: ref.Hash})
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

// A note's file stands under its words as a download link with its name,
// never drawn as an image and never previewed; one the yard no longer
// holds is a line saying so, never a broken link. The JSON names the
// files with their names.
func TestBeadPageNoteFiles(t *testing.T) {
	t.Parallel()
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "the report"}, "me")
	must(t, err)
	ref, err := f.st.PutBlob(ctx, []byte("%PDF-1.4 the report"))
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "the report, filed", "me", "", store.BlobFile{Hash: ref.Hash, Name: "report.pdf"})
	must(t, err)

	var got web.BeadPage
	f.getJSON("/beads/"+b.ID, &got)
	var files []store.BlobRef
	for _, n := range got.Notes {
		files = append(files, n.Files...)
	}
	if len(files) != 1 || files[0].Hash != ref.Hash || files[0].Name != "report.pdf" {
		t.Fatalf("the JSON names %v", files)
	}

	body := f.get("/beads/" + b.ID).body
	if want := `<a href="/blobs/` + ref.Hash + `/report.pdf">file report.pdf</a>`; !strings.Contains(body, want) {
		t.Errorf("the page lacks %s", want)
	}
	if strings.Contains(body, "<img") {
		t.Error("the page draws the file as an image")
	}

	must(t, os.Remove(store.BlobPath(f.home, ref.Hash)))
	body = f.get("/beads/" + b.ID).body
	if want := `file ` + ref.Hash[:8] + `: the yard no longer has it`; !strings.Contains(body, want) {
		t.Errorf("the page lacks %q", want)
	}
}

// A note's page stands under its words as a link opening the bytes
// top-level; one the yard no longer holds is a line saying so, never a
// broken link. The JSON names the files, and the bead's own page keeps
// the yard page's policy.
func TestBeadPageNotePages(t *testing.T) {
	t.Parallel()
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a door"}, "me")
	must(t, err)
	ref, err := f.st.PutBlob(ctx, pageHTML)
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, hung", "me", "", store.BlobFile{Hash: ref.Hash})
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

// The board above the notes lays the bead's note pages out in note order,
// each with its note beside it: one entry per page, the link first and the
// note's words under it. Images stand on no board, and the JSON names the
// files as the notes have them, with no board of its own.
func TestBeadBoardLaysPagesInNoteOrder(t *testing.T) {
	t.Parallel()
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a board"}, "me")
	must(t, err)
	first, err := f.st.PutBlob(ctx, pageHTML)
	must(t, err)
	second, err := f.st.PutBlob(ctx, pageFence)
	must(t, err)
	third, err := f.st.PutBlob(ctx, []byte("<!DOCTYPE html><html><body>an arch</body></html>"))
	must(t, err)
	shot, err := f.st.PutBlob(ctx, shotPNG)
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, `the door, first pass <b>bold</b>`, "me", "", store.BlobFile{Hash: first.Hash})
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "a photo of the door", "me", "", store.BlobFile{Hash: shot.Hash})
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, second pass", "me", "", store.BlobFile{Hash: second.Hash}, store.BlobFile{Hash: third.Hash})
	must(t, err)

	var got web.BeadPage
	r := f.getJSON("/beads/"+b.ID, &got)
	if strings.Contains(r.body, `"board":`) {
		t.Error("the JSON carries a board; the notes name the files")
	}

	body := f.get("/beads/" + b.ID).body
	boardAt := strings.Index(body, `id="board"`)
	notesAt := strings.Index(body, `id="notes"`)
	if boardAt < 0 {
		t.Fatalf("the page shows no board: %s", body)
	}
	if notesAt < boardAt {
		t.Fatalf("the board stands below the notes")
	}
	board := body[boardAt:notesAt]
	at := func(hash string) int { return strings.Index(board, "/blobs/"+hash) }
	firstAt, secondAt, thirdAt := at(first.Hash), at(second.Hash), at(third.Hash)
	if firstAt < 0 || secondAt < 0 || thirdAt < 0 {
		t.Fatalf("the board lacks a page: %d %d %d", firstAt, secondAt, thirdAt)
	}
	if !(firstAt < secondAt && secondAt < thirdAt) {
		t.Errorf("the board is not in note order: %d %d %d", firstAt, secondAt, thirdAt)
	}
	// Each page's note stands beside it, as text, never as markup; the
	// image's note stands on no board.
	if firstWords := strings.Index(board, "the door, first pass"); firstWords < 0 {
		t.Error("the board lacks the first page's note")
	} else if !(firstAt < firstWords && firstWords < secondAt) {
		t.Error("the first page's note stands beside no page")
	}
	if !strings.Contains(board, "&lt;b&gt;bold&lt;/b&gt;") || strings.Contains(board, "<b>bold</b>") {
		t.Error("the board does not show the note's words as text")
	}
	if n := strings.Count(board, "the door, second pass"); n != 2 {
		t.Errorf("the second note stands beside %d pages, want 2", n)
	}
	if strings.Contains(board, "a photo of the door") || strings.Contains(board, shot.Hash) {
		t.Error("the board holds the image's note")
	}
	if strings.Contains(board, "<img") {
		t.Error("the board draws an image")
	}
}

// A bead with no note page shows no board: no note, a plain note, or an
// image's note only.
func TestBeadBoardAbsentWithoutPages(t *testing.T) {
	t.Parallel()
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "no board"}, "me")
	must(t, err)
	if body := f.get("/beads/" + b.ID).body; strings.Contains(body, `id="board"`) {
		t.Fatal("a bead with no note shows a board")
	}
	must(t, f.st.AddNote(ctx, b.ID, "plain words", "me"))
	if body := f.get("/beads/" + b.ID).body; strings.Contains(body, `id="board"`) {
		t.Fatal("a bead with a plain note shows a board")
	}
	shot, err := f.st.PutBlob(ctx, shotPNG)
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "a photo of the door", "me", "", store.BlobFile{Hash: shot.Hash})
	must(t, err)
	if body := f.get("/beads/" + b.ID).body; strings.Contains(body, `id="board"`) {
		t.Fatal("a bead with an image only shows a board")
	}
}

// Mail pages stand on no board: the board is the bead's note pages, and
// the bead's page shows no mail at all.
func TestBeadBoardHoldsNoMailPage(t *testing.T) {
	t.Parallel()
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a board"}, "me")
	must(t, err)
	noted, err := f.st.PutBlob(ctx, pageHTML)
	must(t, err)
	mailed, err := f.st.PutBlob(ctx, pageFence)
	must(t, err)
	if _, err := f.st.AddUser(ctx, "sam", "me"); err != nil {
		t.Fatal(err)
	}
	must(t, f.st.Mail(ctx, "sam", "the fence, drawn", "me", store.BlobFile{Hash: mailed.Hash}))
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, hung", "me", "", store.BlobFile{Hash: noted.Hash})
	must(t, err)

	body := f.get("/beads/" + b.ID).body
	boardAt := strings.Index(body, `id="board"`)
	if boardAt < 0 {
		t.Fatalf("the page shows no board: %s", body)
	}
	board := body[boardAt:]
	if !strings.Contains(board, "/blobs/"+noted.Hash) {
		t.Error("the board lacks the note's page")
	}
	if strings.Contains(board, mailed.Hash) {
		t.Error("the board holds the mail's page")
	}
	if strings.Contains(body, mailed.Hash) {
		t.Error("the bead's page shows the mail's page")
	}
}

// A board page the yard no longer holds is the Missing line, never a
// broken link.
func TestBeadBoardMissingPage(t *testing.T) {
	t.Parallel()
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a board"}, "me")
	must(t, err)
	ref, err := f.st.PutBlob(ctx, pageHTML)
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, hung", "me", "", store.BlobFile{Hash: ref.Hash})
	must(t, err)
	must(t, os.Remove(store.BlobPath(f.home, ref.Hash)))

	body := f.get("/beads/" + b.ID).body
	boardAt := strings.Index(body, `id="board"`)
	notesAt := strings.Index(body, `id="notes"`)
	if boardAt < 0 || notesAt < boardAt {
		t.Fatalf("the page shows no board above the notes: %s", body)
	}
	board := body[boardAt:notesAt]
	if want := `page ` + ref.Hash[:8] + `: the yard no longer has it`; !strings.Contains(board, want) {
		t.Errorf("the board lacks %q", want)
	}
	if strings.Contains(board, "/blobs/"+ref.Hash) {
		t.Error("the board links what the yard no longer holds")
	}
}
