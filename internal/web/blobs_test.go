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
	srv.ActAs(as)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	return &fixture{t: t, st: f.st, ts: ts, home: f.home}
}

// /blobs/<hash> serves a blob a note on some bead names, with its type,
// to anyone; on a view that acts it serves a mail's file too, to the
// mail's sender or recipient. Anything else answers as a missing blob:
// one the yard never held or no longer holds, a name that is no hash, a
// mail's file to anyone else or where the view acts as nobody. A page
// opens under the page policy, inert; an image keeps the yard page's
// policy.
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

// The board above the notes lays the bead's note pages out in note order,
// each with its note beside it: one entry per page, the link first and the
// note's words under it. Images stand on no board, and the JSON names the
// files as the notes have them, with no board of its own.
func TestBeadBoardLaysPagesInNoteOrder(t *testing.T) {
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
	_, err = f.st.AddNotedAs(ctx, b.ID, `the door, first pass <b>bold</b>`, "me", "", first.Hash)
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "a photo of the door", "me", "", shot.Hash)
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, second pass", "me", "", second.Hash, third.Hash)
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
	_, err = f.st.AddNotedAs(ctx, b.ID, "a photo of the door", "me", "", shot.Hash)
	must(t, err)
	if body := f.get("/beads/" + b.ID).body; strings.Contains(body, `id="board"`) {
		t.Fatal("a bead with an image only shows a board")
	}
}

// Mail pages stand on no board: the board is the bead's note pages, and
// the bead's page shows no mail at all.
func TestBeadBoardHoldsNoMailPage(t *testing.T) {
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
	must(t, f.st.Mail(ctx, "sam", "the fence, drawn", "me", mailed.Hash))
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, hung", "me", "", noted.Hash)
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
	f := blobFixture(t)
	ctx := context.Background()
	b, err := f.st.CreateBead(ctx, store.NewBead{Title: "a board"}, "me")
	must(t, err)
	ref, err := f.st.PutBlob(ctx, pageHTML)
	must(t, err)
	_, err = f.st.AddNotedAs(ctx, b.ID, "the door, hung", "me", "", ref.Hash)
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
