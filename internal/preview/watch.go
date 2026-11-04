package preview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// A watch (store.PreviewKindWatch) is a preview that runs nothing: the
// supervisor serves the files of one directory of a bead's worktree
// itself, read-only, through the same endpoint, and the page reloads
// itself when a file changes, so a person watches a site take shape while
// its builder works. No command of the builder's runs and nothing is
// approved: the yard reads files it already holds.
//
// What is served is plain files. A site that needs a build step runs the
// builder's code, and stays a preview with an approval.

const (
	// watchEvents is the path, under a watch's address, of its server-sent
	// events. It starts with a dot, which the serving of files refuses, so
	// no file of a worktree can be found at it.
	watchEvents = ".yardr-watch/events"

	// The poll of a stream: how often it reads the directory, how long the
	// directory must stand still after a change before the page is told, and
	// how often an idle stream says it is there, so nothing between the page
	// and the yard closes it. Supervisor.WatchPoll and WatchSettle take the
	// place of the first two (tests shorten them).
	defaultWatchPoll   = 500 * time.Millisecond
	defaultWatchSettle = 300 * time.Millisecond
	watchKeepAlive     = 20 * time.Second

	// watchScanCap is how many entries a read of the directory takes in: a
	// reload signal is best effort, and a directory of more than this (a
	// node_modules among them) reloads late or not at all, in return for a
	// poll that cannot cost much.
	watchScanCap = 20000
	// watchInjectMax is the largest page the reloader is put into; a larger
	// one is served as it is.
	watchInjectMax = 8 << 20
)

func (s *Supervisor) watchPoll() time.Duration {
	if s.WatchPoll > 0 {
		return s.WatchPoll
	}
	return defaultWatchPoll
}

func (s *Supervisor) watchSettle() time.Duration {
	if s.WatchSettle > 0 {
		return s.WatchSettle
	}
	return defaultWatchSettle
}

// servePreviewWatch is servePreview for a watch: the request is counted in
// flight and is the watch's latest, as a preview's is, and the answer is the
// supervisor's own.
func (s *Supervisor) servePreviewWatch(w http.ResponseWriter, r *http.Request, p *store.Preview, rest string) {
	leave, ok := s.admit(w, r, p.App)
	if !ok {
		return
	}
	defer leave()
	now := time.Now()
	_, _ = s.Store.UpdatePreview(r.Context(), p.App, store.PreviewUpdate{LastRequest: &now})
	s.serveWatch(w, r, p, rest)
}

// serveWatch answers a request for the watch p: its events, or the file the
// path past the app names.
func (s *Supervisor) serveWatch(w http.ResponseWriter, r *http.Request, p *store.Preview, rest string) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if p.State != store.PreviewRunning {
		http.Error(w, fmt.Sprintf("the watch of %s ended (%s): yardr preview watch %s starts it again", p.Bead, p.StateDetail, p.Bead), http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "a watch is read-only", http.StatusMethodNotAllowed)
		return
	}
	if rest == watchEvents && r.Method == http.MethodGet {
		s.serveWatchEvents(w, r, p)
		return
	}
	rel, ok := watchPath(rest)
	if !ok {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(p.Dir)
	if err != nil {
		http.Error(w, "the directory the watch of "+p.Bead+" serves is gone", http.StatusNotFound)
		return
	}
	defer root.Close()
	f, info, inside, err := openWatched(root, p.Dir, rel)
	if err == nil && info.IsDir() {
		f.Close()
		if rest != "" && !strings.HasSuffix(rest, "/") {
			// A page in a directory names its neighbours relative to the
			// directory, so the address must end in a slash. The address is
			// followed from where it stands, because the path the edge
			// prefixes to the one the page was asked for is not the page's.
			to := path.Base(rest) + "/"
			if r.URL.RawQuery != "" {
				to += "?" + r.URL.RawQuery
			}
			h.Set("Location", to)
			w.WriteHeader(http.StatusFound)
			return
		}
		f, info, inside, err = openWatched(root, p.Dir, path.Join(inside, "index.html"))
	}
	if err != nil || info.IsDir() {
		if f != nil {
			f.Close()
		}
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	name := path.Base(inside)
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		var sniff [512]byte
		n, _ := io.ReadFull(f, sniff[:])
		ctype = http.DetectContentType(sniff[:n])
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	h.Set("Content-Type", ctype)
	if mt, _, _ := mime.ParseMediaType(ctype); mt != "text/html" || info.Size() > watchInjectMax {
		http.ServeContent(w, r, name, info.ModTime(), f)
		return
	}
	// The directory is read before the page, so the page is never older than
	// the signature it carries (watchSignature).
	sig := watchSignature(p.Dir)
	page, err := io.ReadAll(io.LimitReader(f, watchInjectMax+1))
	if err != nil {
		http.Error(w, "the file could not be read", http.StatusInternalServerError)
		return
	}
	if len(page) > watchInjectMax {
		// It grew past the bound since it was measured.
		if _, err := f.Seek(0, io.SeekStart); err == nil {
			http.ServeContent(w, r, name, info.ModTime(), f)
		}
		return
	}
	page = injectReloader(page, sig)
	h.Set("Content-Length", strconv.Itoa(len(page)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(page)
}

// watchPath reads the path past a watch's app as a path under its
// directory: "" for the directory itself. A part starting with a dot is
// refused wherever it stands: it is a dotfile or a dot directory (.git,
// .env), or a way up out of the directory.
func watchPath(rest string) (string, bool) {
	if strings.IndexByte(rest, 0) >= 0 {
		return "", false
	}
	var parts []string
	for _, part := range strings.Split(rest, "/") {
		switch {
		case part == "":
		case strings.HasPrefix(part, "."):
			return "", false
		default:
			parts = append(parts, part)
		}
	}
	return path.Join(parts...), true
}

// openWatched opens rel under the directory dir, which root holds open, and
// answers its path inside dir with every link resolved. A link that leaves
// the directory, or comes to a part starting with a dot, is no file of the
// watch. Both checks read the path as it is resolved now; the open itself
// is root's, so a link swapped after them still cannot lead out.
func openWatched(root *os.Root, dir, rel string) (*os.File, os.FileInfo, string, error) {
	real, err := filepath.EvalSymlinks(filepath.Join(dir, rel))
	if err != nil {
		return nil, nil, "", err
	}
	inside, err := filepath.Rel(dir, real)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return nil, nil, "", fs.ErrNotExist
	}
	for _, part := range strings.Split(inside, string(filepath.Separator)) {
		if part != "." && strings.HasPrefix(part, ".") {
			return nil, nil, "", fs.ErrNotExist
		}
	}
	f, err := root.Open(inside)
	if err != nil {
		return nil, nil, "", err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, "", err
	}
	return f, info, filepath.ToSlash(inside), nil
}

// injectReloader puts the script that reloads the page into page: before
// its last </body>, else at its end. The script listens on the watch's
// events path, and says which state of the directory the page was served
// from (sig), so a change between the page and its stream is not lost.
func injectReloader(page []byte, sig string) []byte {
	script := `<script data-yardr-watch>new EventSource("/` + watchEvents + `?v=` + sig +
		`").onmessage=function(){location.reload()}</script>`
	// Folded a byte at a time: a page that is not UTF-8 would change length
	// under bytes.ToLower, and the place found would not be the page's.
	folded := make([]byte, len(page))
	for i, c := range page {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		folded[i] = c
	}
	at := bytes.LastIndex(folded, []byte("</body"))
	if at < 0 {
		at = len(page)
	}
	out := make([]byte, 0, len(page)+len(script))
	return append(append(append(out, page[:at]...), script...), page[at:]...)
}

// watchSignature stands for the state of the directory dir: a hash of the
// name, size and modification moment of every entry the watch would serve.
// A dot entry is left out, so a commit or an editor's scratch file moves
// nothing, and a link is read as a link and not followed.
func watchSignature(dir string) string {
	h := fnv.New64a()
	n := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil || p == dir:
			return nil // gone since it was listed, or the directory itself
		case strings.HasPrefix(d.Name(), "."):
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if n++; n > watchScanCap {
			return fs.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", rel, info.Size(), info.ModTime().UnixNano())
		return nil
	})
	return strconv.FormatUint(h.Sum64(), 16)
}

// serveWatchEvents streams the watch's events: one data line for each
// change of the directory once it has stood still for the settle time. A
// page asks from the state it was served from (?v=), and is told at once if
// the directory has moved since. The stream ends with the request, or when
// the watch is no longer running. A request that ends after the person was
// there a long while is the watch's last request.
func (s *Supervisor) serveWatchEvents(w http.ResponseWriter, r *http.Request, p *store.Preview) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "this endpoint carries no streams", http.StatusInternalServerError)
		return
	}
	ctx := r.Context()
	defer func() {
		at := time.Now()
		_, _ = s.Store.UpdatePreview(context.WithoutCancel(ctx), p.App, store.PreviewUpdate{LastRequest: &at})
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(line string) bool {
		if _, err := io.WriteString(w, line+"\n\n"); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	if !send(": watching " + p.Bead) {
		return
	}
	poll, settle := s.watchPoll(), s.watchSettle()
	cur := watchSignature(p.Dir)
	told := cur // the state the page has, or has been told of
	if v := r.URL.Query().Get("v"); v != "" && v != cur {
		told = v
	}
	var changed time.Time // when the directory last differed from the pass before
	next, kept := poll, time.Now()
	if told != cur {
		next = 0 // the page is behind already
	}
	timer := time.NewTimer(next)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if row, err := s.Store.GetPreview(ctx, p.App); err != nil || row.State != store.PreviewRunning {
			return
		}
		now := time.Now()
		switch sig := watchSignature(p.Dir); {
		case sig != cur:
			// Changing: look again soon, until it stands still.
			cur, changed, next = sig, now, settle/3
		case told != cur && (changed.IsZero() || now.Sub(changed) >= settle):
			told, changed, next = cur, time.Time{}, poll
			if !send("data: " + cur) {
				return
			}
			kept = now
		case !changed.IsZero() && now.Sub(changed) >= settle:
			changed, next = time.Time{}, poll // it changed back to what the page has
		}
		if now.Sub(kept) >= watchKeepAlive {
			if !send(":") {
				return
			}
			kept = now
		}
		timer.Reset(next)
	}
}

// reapWatch ends the running watch p if it is over, for the shared reaper
// (reconcile): it is PreviewAgeLimit old, its bead has left the stage it
// was watched in or is closed or gone, or it has stood idle PreviewIdle
// with no request in flight (a stream open is one). The row is then
// stopped with the reason, and the same command starts it again.
func (s *Supervisor) reapWatch(ctx context.Context, p store.Preview, now time.Time, idle time.Duration) {
	started := now
	if p.StartedAt != nil {
		started = *p.StartedAt
	}
	last := started
	if p.LastRequestAt != nil {
		last = *p.LastRequestAt
	}
	reason := ""
	switch b, err := s.Store.GetBead(ctx, p.Bead); {
	case now.Sub(started) > store.PreviewAgeLimit:
		reason = "aged"
	case errors.Is(err, store.ErrNotFound):
		reason = "bead gone"
	case err != nil:
		// Not read: judged again on the next pass.
	case b.ClosedAt != nil:
		reason = "bead closed"
	case b.Stage != p.Stage:
		reason = "stage left"
	case now.Sub(last) > idle:
		s.mu.Lock()
		busy := s.inflight[p.App] > 0
		s.mu.Unlock()
		if !busy {
			reason = "idle"
		}
	}
	if reason != "" {
		s.stop(ctx, p.App, reason)
	}
}
