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
)

// IsURL reports whether s names a pack by where it is served: an http or
// https URL. Such a pack is neither a directory nor a name, so it is kept
// apart from both wherever a pack is named.
func IsURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// urlClient fetches a pack's files, one GET per file: the one client, so a
// pack's files share its connections. A file that hangs holds the command,
// so each has a timeout of a few seconds.
var urlClient = &http.Client{Timeout: 10 * time.Second}

// URLFS is the pack at base URL as an fs.FS: what Load reads a pack from,
// over HTTP. Open GETs base with the file's slash path on it, so base is
// the pack's directory on a web server, with or without its trailing slash:
// pack.yaml is at its root, and the files the pack names under it.
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
	resp, err := urlClient.Get(u)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", u, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", u, err)
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
