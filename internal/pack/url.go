package pack

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"

	"yardr.dev/yardr/internal/source"
)

// MaxURLFile bounds one file of a pack fetched by URL, a MiB. A pack is
// roles, flows and scripts, text a person wrote: at its largest yardr
// embeds a file under 30 KB and a whole pack under 130 KB (October 2026),
// so a MiB is far above either. Each file is held whole in memory to be
// hashed, and the timeout alone would let a server stream as much as the
// link carries in ten seconds.
const MaxURLFile = 1 << 20

// IsURL reports whether s names a pack by where it is served: an http or
// https URL. Such a pack is neither a directory nor a name, so it is kept
// apart from both wherever a pack is named. Whether it may be fetched is
// URLFS's to say, with the reason: refusing plain http here would read the
// URL as a directory, and the error would say nothing of http.
func IsURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// urlClient fetches a pack's files, one GET per file: the one client, so a
// pack's files share its connections. A file that hangs holds the command,
// so each has a timeout of a few seconds. A redirect is held to packRedirect.
var urlClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: packRedirect}

// maxRedirects is how many redirects a file's GET follows: the limit the
// client has of its own, which a CheckRedirect replaces.
const maxRedirects = 10

// packRedirect is urlClient's CheckRedirect. A file of a pack may move
// within the host the pack is served from, and no further: a pack is roles
// and scripts the yard goes on to run, so a redirect to another host would
// leave who writes them to whoever redirects, and one from https to plain
// http would take the transport's guarantee away on the way. An error names
// the host it refused and not the rest of the URL, which may carry a token.
func packRedirect(req *http.Request, via []*http.Request) error {
	switch {
	case len(via) >= maxRedirects:
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	case !strings.EqualFold(req.URL.Host, via[0].URL.Host):
		return fmt.Errorf("redirected to %s, which is not the host the pack is served from: give the URL the pack is at", req.URL.Host)
	case via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https":
		return fmt.Errorf("redirected from https to plain %s: a pack is fetched over https", req.URL.Scheme)
	}
	return nil
}

// checkFetch holds the URL a pack's file is fetched from to the rule a
// declaration's base is held to: https, or plain http to this machine for a
// stand-in (source.LoopbackHost). What a pack brings is run by the yard, and
// over plain http to another machine it is whatever the network made of it.
func checkFetch(u *url.URL) error {
	switch {
	case u.Scheme == "https" || (u.Scheme == "http" && source.LoopbackHost(u.Hostname())):
		return nil
	case u.Scheme == "http":
		return fmt.Errorf("plain http to %s, which is not this machine: a pack is fetched over https, and plain http is for a stand-in on this machine alone", u.Hostname())
	}
	return fmt.Errorf("%q is not a scheme a pack is fetched by: want https://", u.Scheme)
}

// URLFS is the pack at base URL as an fs.FS: what Load reads a pack from,
// over HTTP. Open GETs base with the file's slash path on it, so base is
// the pack's directory on a web server, with or without its trailing slash:
// pack.yaml is at its root, and the files the pack names under it. Each
// file is held to what a pack from a stranger must be: over https (plain
// http only to this machine), from the host the pack is served from, and of
// at most MaxURLFile bytes. A file is fetched every time it is opened, so a
// caller that checks a file and then installs it keeps the bytes it checked.
func URLFS(base string) fs.FS {
	return &urlFS{base: strings.TrimSuffix(base, "/") + "/"}
}

type urlFS struct {
	base string // the pack's directory URL, ending in "/"
}

func (f *urlFS) Open(name string) (fs.File, error) {
	// What Load reads a pack by is a clean slash path in its directory;
	// anything else never names a file of the pack.
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	u := f.base + escapePath(name)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	// Before any connection: nothing goes to a host the rule refuses.
	if err := checkFetch(req.URL); err != nil {
		return nil, err
	}
	resp, err := urlClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		// A byte past the cap is what says the body is over it: a body of
		// exactly the cap is a file.
		data, err := io.ReadAll(io.LimitReader(resp.Body, MaxURLFile+1))
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", u, err)
		}
		if len(data) > MaxURLFile {
			return nil, fmt.Errorf("GET %s: more than %d bytes: a file of a pack is not that big", u, MaxURLFile)
		}
		return &urlFile{info: urlFileInfo{name: path.Base(name), size: int64(len(data))}, data: data}, nil
	case http.StatusNotFound:
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	default:
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
}

// escapePath escapes a slash path of the pack for its URL, segment by
// segment, so a file with a space or another such character in its name is
// still the same file the pack names.
func escapePath(name string) string {
	segs := strings.Split(name, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// urlFile is one fetched file: what fs.ReadFile needs is its bytes and a
// Stat saying it is a regular file, which is also what refuses a link (a
// URL has no links to name one with).
type urlFile struct {
	info urlFileInfo
	data []byte
	off  int
}

func (f *urlFile) Stat() (fs.FileInfo, error) { return f.info, nil }

func (f *urlFile) Read(b []byte) (int, error) {
	if f.off >= len(f.data) {
		return 0, io.EOF
	}
	n := copy(b, f.data[f.off:])
	f.off += n
	return n, nil
}

func (f *urlFile) Close() error { return nil }

type urlFileInfo struct {
	name string
	size int64
}

func (i urlFileInfo) Name() string       { return i.name }
func (i urlFileInfo) Size() int64        { return i.size }
func (i urlFileInfo) Mode() fs.FileMode  { return 0o444 }
func (i urlFileInfo) ModTime() time.Time { return time.Time{} }
func (i urlFileInfo) IsDir() bool        { return false }
func (i urlFileInfo) Sys() any           { return nil }

// joinName joins rel, a slash path in the pack, with dir, the directory
// Load was told the pack is in: a URL stays a URL (filepath would collapse
// the // after its scheme), anything else joins as a path.
func joinName(dir, rel string) string {
	if IsURL(dir) {
		return strings.TrimSuffix(dir, "/") + "/" + rel
	}
	return filepath.Join(dir, filepath.FromSlash(rel))
}

// compile-time checks that the URL pack reads as the directory one does.
var (
	_ fs.FS   = (*urlFS)(nil)
	_ fs.File = (*urlFile)(nil)
)
