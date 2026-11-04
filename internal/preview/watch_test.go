package preview

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// siteFlow is the flow of a bead a watch is made for: the stages a builder's
// bead passes.
const siteFlow = `
backlog  -> new
new      -> review on done | decide on question | backlog
review   -> approved on done | new on changes | decide on question
approved -> new on failed | merged
decide   -> backlog | new
merged terminal
`

// watching makes a bead in stage new whose session recorded a worktree
// holding files (a name to its content; a name ending in a slash is an
// empty directory), and starts ada's watch of it.
func (y *yard) watching(t *testing.T, files map[string]string) (*store.Preview, *store.Bead, string) {
	t.Helper()
	ctx := context.Background()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if strings.HasSuffix(name, "/") {
			must(t, os.MkdirAll(path, 0o755))
			continue
		}
		must(t, os.MkdirAll(filepath.Dir(path), 0o755))
		must(t, os.WriteFile(path, []byte(content), 0o644))
	}
	must(t, y.st.PutDepot(ctx, store.Depot{Name: "site", Base: "origin/main"}, "ada"))
	flow, err := store.ParseFlow(siteFlow)
	must(t, err)
	flow.Name = "site"
	_, err = y.st.PutFlow(ctx, *flow, "ada")
	must(t, err)
	must(t, y.st.SetFlowPointer(ctx, "site", "", "site", "ada"))
	must(t, y.st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "ada"))
	b, err := y.st.CreateBead(ctx, store.NewBead{Title: "site", Depot: "site", Stage: "new"}, "ada")
	must(t, err)
	sess, err := y.st.Claim(ctx, b.ID, "builders", "new", "site")
	must(t, err)
	must(t, y.st.SetHandle(ctx, sess.ID, map[string]any{"worktree": dir}))
	p, err := y.st.WatchPreview(ctx, b.ID, "", "ada")
	must(t, err)
	return p, b, dir
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// asked is a client that takes a redirect as the answer.
var asked = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// fetch answers the status, headers and body of a request to url.
func fetch(t *testing.T, method, url string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	must(t, err)
	resp, err := asked.Do(req)
	must(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	must(t, err)
	return resp.StatusCode, resp.Header, string(body)
}

func TestWatchPathRefusesAnyPartStartingWithADot(t *testing.T) {
	t.Parallel()
	for rest, want := range map[string]string{
		"":                  "",
		"/":                 "",
		"index.html":        "index.html",
		"a//b/":             "a/b",
		"a/b.min.js":        "a/b.min.js",
		".env":              "!",
		".git/config":       "!",
		"a/.hidden/x":       "!",
		"a/.htaccess":       "!",
		"..":                "!",
		"a/../b":            "!",
		"./a":               "!",
		".yardr-watch/data": "!",
		"a\x00b":            "!",
	} {
		got, ok := watchPath(rest)
		if want == "!" {
			if ok {
				t.Errorf("watchPath(%q) = %q, want a refusal", rest, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("watchPath(%q) = %q, %v, want %q", rest, got, ok, want)
		}
	}
}

func TestWatchServesTheFilesOfItsDirectoryReadOnly(t *testing.T) {
	y := newYard(t)
	const page = "<html><body><h1>site</h1></body></html>"
	p, _, dir := y.watching(t, map[string]string{
		"index.html":       page,
		"bare.html":        "<h1>no body tag</h1>",
		"UPPER.html":       "<BODY>x</BODY>",
		"latin1.html":      "<body>caf\xe9</body>",
		"style.css":        "body { color: red } /* </body> */",
		"app.js":           "document.write('</body>')",
		"data.json":        `{"html":"</body>"}`,
		"notes":            "plain, no extension",
		"sub/index.html":   "<body>sub</body>",
		"empty/":           "",
		".env":             "SECRET=1",
		".git/config":      "[core]",
		"sub/.hidden/x":    "x",
		"secret-outside.t": "",
	})
	// What lies outside the directory, reached through links in it.
	outside := t.TempDir()
	must(t, os.WriteFile(filepath.Join(outside, "passwd"), []byte("root:x"), 0o644))
	must(t, os.Symlink(filepath.Join(outside, "passwd"), filepath.Join(dir, "link-out")))
	must(t, os.Symlink(outside, filepath.Join(dir, "link-out-dir")))
	must(t, os.Symlink(filepath.Join(dir, ".git"), filepath.Join(dir, "link-dot")))
	must(t, os.Symlink(filepath.Join(dir, ".env"), filepath.Join(dir, "link-dotfile")))
	must(t, os.Symlink(filepath.Join(dir, "style.css"), filepath.Join(dir, "link-in.css")))
	srv := httptest.NewServer(y.sup.Handler())
	defer srv.Close()
	base := srv.URL + "/preview/" + p.App

	reloader := `<script data-yardr-watch>new EventSource("/.yardr-watch/events?v=` + watchSignature(p.Dir) + `").onmessage=function(){location.reload()}</script>`
	for _, tc := range []struct {
		method, path string
		status       int
		ctype        string // the media type, "" for none to check
		body         string // the whole body; "~" for any
	}{
		// A page gets the reloader before its closing body tag, or at its end.
		{"GET", "/", 200, "text/html", "<html><body><h1>site</h1>" + reloader + "</body></html>"},
		{"GET", "", 200, "text/html", "<html><body><h1>site</h1>" + reloader + "</body></html>"},
		{"GET", "/index.html", 200, "text/html", "<html><body><h1>site</h1>" + reloader + "</body></html>"},
		{"GET", "/bare.html", 200, "text/html", "<h1>no body tag</h1>" + reloader},
		{"GET", "/UPPER.html", 200, "text/html", "<BODY>x" + reloader + "</BODY>"},
		// A page that is not UTF-8 is cut where its body ends all the same.
		{"GET", "/latin1.html", 200, "text/html", "<body>caf\xe9" + reloader + "</body>"},
		{"GET", "/sub/", 200, "text/html", "<body>sub" + reloader + "</body>"},
		// Nothing but HTML is touched.
		{"GET", "/style.css", 200, "text/css", "body { color: red } /* </body> */"},
		{"GET", "/app.js", 200, "text/javascript", "document.write('</body>')"},
		{"GET", "/data.json", 200, "application/json", `{"html":"</body>"}`},
		{"GET", "/link-in.css", 200, "text/css", "body { color: red } /* </body> */"},
		{"GET", "/notes", 200, "text/plain", "plain, no extension"},
		// A directory is its index, at an address that ends in a slash.
		{"GET", "/sub", 302, "", "~"},
		{"GET", "/empty/", 404, "", "~"},
		{"GET", "/nowhere.html", 404, "", "~"},
		// Dot files and directories, ways up, and links that lead out or into a dot.
		{"GET", "/.env", 404, "", "~"},
		{"GET", "/.git/config", 404, "", "~"},
		{"GET", "/sub/.hidden/x", 404, "", "~"},
		{"GET", "/sub/../.env", 404, "", "~"},
		{"GET", "/%2e%2e/%2e%2e/etc/passwd", 404, "", "~"},
		{"GET", "/.yardr-watch/other", 404, "", "~"},
		{"GET", "/link-out", 404, "", "~"},
		{"GET", "/link-out-dir/passwd", 404, "", "~"},
		{"GET", "/link-dot/config", 404, "", "~"},
		{"GET", "/link-dotfile", 404, "", "~"},
		// Read-only.
		{"POST", "/index.html", 405, "", "~"},
		{"PUT", "/new.html", 405, "", "~"},
		{"DELETE", "/index.html", 405, "", "~"},
	} {
		status, h, body := fetch(t, tc.method, base+tc.path)
		if status != tc.status {
			t.Errorf("%s %s = %d %q, want %d", tc.method, tc.path, status, body, tc.status)
			continue
		}
		if mt, _, _ := strings.Cut(h.Get("Content-Type"), ";"); tc.ctype != "" && mt != tc.ctype {
			t.Errorf("%s %s is %s, want %s", tc.method, tc.path, h.Get("Content-Type"), tc.ctype)
		}
		if tc.body != "~" && body != tc.body {
			t.Errorf("%s %s = %q, want %q", tc.method, tc.path, body, tc.body)
		}
		if status == 200 && (h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff") {
			t.Errorf("%s %s is cached or sniffed: %v", tc.method, tc.path, h)
		}
		if status == 405 && h.Get("Allow") != "GET, HEAD" {
			t.Errorf("%s %s allows %q", tc.method, tc.path, h.Get("Allow"))
		}
	}
	// A directory's address ends in a slash, found from where the request
	// stands, and a query survives.
	if _, h, _ := fetch(t, "GET", base+"/sub?x=1"); h.Get("Location") != "sub/?x=1" {
		t.Errorf("redirect to %q, want sub/?x=1", h.Get("Location"))
	}
	// A HEAD says what a GET would, with no body.
	status, h, body := fetch(t, "HEAD", base+"/index.html")
	if status != 200 || body != "" || h.Get("Content-Length") != strconv.Itoa(len(page)+len(reloader)) {
		t.Errorf("HEAD = %d, length %s, body %q, want the length of the page with its reloader", status, h.Get("Content-Length"), body)
	}
}

// stream opens the watch's events and answers what it sends, line by line,
// and what closes it.
func stream(t *testing.T, url string) (lines <-chan string, closeIt func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	must(t, err)
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events answered %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	ch := make(chan string, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if line := sc.Text(); line != "" {
				ch <- line
			}
		}
	}()
	return ch, cancel
}

// event waits for the next data line of the stream.
func event(t *testing.T, lines <-chan string) string {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("the stream ended before an event")
			}
			if sig, ok := strings.CutPrefix(line, "data: "); ok {
				return sig
			}
		case <-timeout:
			t.Fatal("no event")
		}
	}
}

// quiet fails if a data line comes within d.
func quiet(t *testing.T, lines <-chan string, d time.Duration, why string) {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case line, ok := <-lines:
			if ok && strings.HasPrefix(line, "data: ") {
				t.Fatalf("an event %s: %q", why, line)
			}
			if !ok {
				return
			}
		case <-timeout:
			return
		}
	}
}

func TestWatchEventsAreOnePerSettledChange(t *testing.T) {
	y := newYard(t)
	y.sup.WatchPoll, y.sup.WatchSettle = 20*time.Millisecond, 150*time.Millisecond
	p, _, dir := y.watching(t, map[string]string{"index.html": "<body>one</body>"})
	srv := httptest.NewServer(y.sup.Handler())
	defer srv.Close()
	base := srv.URL + "/preview/" + p.App

	// The page says which state of the directory it was served from.
	_, _, page := fetch(t, "GET", base+"/")
	sig := watchSignature(dir)
	if !strings.Contains(page, `EventSource("/.yardr-watch/events?v=`+sig+`")`) {
		t.Fatalf("page %q does not ask for events from %s", page, sig)
	}
	lines, _ := stream(t, base+"/"+watchEvents+"?v="+sig)
	quiet(t, lines, 2*y.sup.WatchSettle, "while nothing changed")

	// A change is one event, and the event says the state it is.
	must(t, os.WriteFile(filepath.Join(dir, "two.html"), []byte("two"), 0o644))
	if got, want := event(t, lines), watchSignature(dir); got != want {
		t.Fatalf("event %q, want the directory's signature %q", got, want)
	}
	quiet(t, lines, 2*y.sup.WatchSettle, "after the change settled")

	// A burst of writes that stand still together is one event.
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		must(t, os.WriteFile(filepath.Join(dir, name+".css"), []byte(name), 0o644))
	}
	if got, want := event(t, lines), watchSignature(dir); got != want {
		t.Fatalf("event %q after the burst, want %q", got, want)
	}
	quiet(t, lines, 3*y.sup.WatchSettle, "more than one for a burst")

	// What the watch does not serve moves nothing: a dot file, a dot
	// directory (a commit writes in .git), and a change that is undone.
	must(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, ".swp"), []byte("x"), 0o644))
	quiet(t, lines, 3*y.sup.WatchSettle, "for a file the watch does not serve")

	// A page that was served before a change asks from its old state, and is
	// told at once, with nothing more to change.
	late, _ := stream(t, base+"/"+watchEvents+"?v="+sig)
	if got, want := event(t, late), watchSignature(dir); got != want {
		t.Fatalf("event %q for a page behind, want %q", got, want)
	}

	// The stream ends when the watch stops, and its last request is when
	// the person was last there.
	before := y.state(p.App).LastRequestAt
	if _, err := y.st.PreviewStop(context.Background(), p.App, "ada"); err != nil {
		t.Fatal(err)
	}
	for range lines { // drains to the close
	}
	waitFor(t, "the stream's end recorded as a request", func() bool {
		at := y.state(p.App).LastRequestAt
		return at != nil && at.After(*before)
	})
}

func TestWatchEnds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		end    func(t *testing.T, y *yard, p *store.Preview, b *store.Bead)
		detail string
	}{
		{"the bead leaves its stage", func(t *testing.T, y *yard, _ *store.Preview, b *store.Bead) {
			_, err := y.st.Advance(context.Background(), b.ID, "review", store.Expect{}, "ada")
			must(t, err)
		}, "stage left"},
		{"the bead is closed", func(t *testing.T, y *yard, _ *store.Preview, b *store.Bead) {
			_, err := y.st.CloseBead(context.Background(), b.ID, "dropped", store.Expect{}, "ada")
			must(t, err)
		}, "bead closed"},
		{"it stands idle", func(t *testing.T, y *yard, p *store.Preview, _ *store.Bead) {
			old := time.Now().Add(-5 * time.Hour)
			_, err := y.st.UpdatePreview(context.Background(), p.App, store.PreviewUpdate{LastRequest: &old})
			must(t, err)
		}, "idle"},
		{"it is as old as a preview may be", func(t *testing.T, y *yard, p *store.Preview, _ *store.Bead) {
			old := time.Now().Add(-store.PreviewAgeLimit - time.Hour)
			_, err := y.st.UpdatePreview(context.Background(), p.App, store.PreviewUpdate{StartedAt: &old})
			must(t, err)
		}, "aged"},
		{"it is stopped", func(t *testing.T, y *yard, p *store.Preview, _ *store.Bead) {
			_, err := y.st.PreviewStop(context.Background(), p.App, "ada")
			must(t, err)
		}, "stopped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			y := newYard(t)
			p, b, _ := y.watching(t, map[string]string{"index.html": "<body>one</body>"})
			srv := httptest.NewServer(y.sup.Handler())
			defer srv.Close()
			ctx := context.Background()

			// A pass over a watch that is not over leaves it, and so does a
			// restart of the server: it runs no code to resurrect.
			y.sup.reconcile(ctx, true)
			if got := y.state(p.App); got.State != store.PreviewRunning {
				t.Fatalf("a watch not over = %+v", got)
			}
			if status, _, _ := fetch(t, "GET", srv.URL+"/preview/"+p.App+"/"); status != 200 {
				t.Fatalf("a running watch answers %d", status)
			}

			tc.end(t, y, p, b)
			y.sup.reconcile(ctx, false)
			got := y.state(p.App)
			if got.State != store.PreviewStopped || got.StateDetail != tc.detail {
				t.Fatalf("after the end = %s (%s), want stopped (%s)", got.State, got.StateDetail, tc.detail)
			}
			// Its address says it ended, and how to start it again.
			status, h, body := fetch(t, "GET", srv.URL+"/preview/"+p.App+"/")
			if status != 404 || !strings.Contains(body, "ended ("+tc.detail+")") || !strings.Contains(body, "yardr preview watch "+b.ID) ||
				h.Get("Cache-Control") != "no-store" {
				t.Fatalf("an ended watch answers %d %q, cached as %q", status, body, h.Get("Cache-Control"))
			}
			if status, _, _ := fetch(t, "GET", srv.URL+"/preview/"+p.App+"/"+watchEvents); status != 404 {
				t.Fatalf("an ended watch's events answer %d", status)
			}
			if tc.detail == "bead closed" {
				return // there is nothing left to watch
			}
			// Started again, it is a running watch of the same app.
			again, err := y.st.WatchPreview(ctx, b.ID, "", "ada")
			must(t, err)
			y.sup.reconcile(ctx, false)
			if got := y.state(p.App); again.App != p.App || got.State != store.PreviewRunning {
				t.Fatalf("a watch started again = %+v, now %+v", again, got)
			}
		})
	}
}

// A stream open is a request in flight, so a watch somebody looks at is not
// idle however long ago its last request began.
func TestWatchWithAStreamOpenIsNotIdle(t *testing.T) {
	y := newYard(t)
	y.sup.WatchPoll, y.sup.WatchSettle = 20*time.Millisecond, 50*time.Millisecond
	p, _, _ := y.watching(t, map[string]string{"index.html": "<body>one</body>"})
	srv := httptest.NewServer(y.sup.Handler())
	defer srv.Close()
	lines, closeIt := stream(t, srv.URL+"/preview/"+p.App+"/"+watchEvents)
	<-lines // the stream is open
	backdate := func() {
		old := time.Now().Add(-5 * time.Hour)
		_, err := y.st.UpdatePreview(context.Background(), p.App, store.PreviewUpdate{LastRequest: &old})
		must(t, err)
	}
	backdate()
	y.sup.reconcile(context.Background(), false)
	if got := y.state(p.App); got.State != store.PreviewRunning {
		t.Fatalf("a watch with a stream open was stopped: %+v", got)
	}
	closeIt()
	waitFor(t, "the stream left", func() bool {
		y.sup.mu.Lock()
		defer y.sup.mu.Unlock()
		return y.sup.inflight[p.App] == 0
	})
	// Its end was the person's last visit; an hour on, nobody has come.
	waitFor(t, "the stream's end recorded", func() bool {
		at := y.state(p.App).LastRequestAt
		return at != nil && time.Since(*at) < time.Minute
	})
	backdate()
	y.sup.reconcile(context.Background(), false)
	if got := y.state(p.App); got.State != store.PreviewStopped || got.StateDetail != "idle" {
		t.Fatalf("after the stream left = %+v, want stopped (idle)", got)
	}
}
