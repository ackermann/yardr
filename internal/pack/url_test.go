package pack_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// servePack serves dir as the pack at /starter/: pack.yaml and the files it
// names, as a web server holds a pack's directory.
func servePack(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/starter/", http.StripPrefix("/starter/", http.FileServer(http.FS(os.DirFS(dir)))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// allFiles are every file of the pack Load reads: what Files names, and the
// stage files of its flows.
func allFiles(p *pack.Pack) []string {
	out := append([]string{}, p.Files()...)
	for _, f := range p.Flows {
		for _, s := range f.Stages {
			if s.File != "" {
				out = append(out, s.File)
			}
		}
	}
	return out
}

// A pack at a URL loads as the same pack in its directory: its names, its
// flows, groups and crew, and the contents of every file it reads.
func TestURLPackLoadsLikeItsDirectory(t *testing.T) {
	srv := servePack(t, starter)
	base := srv.URL + "/starter/"
	got, err := pack.Load(pack.URLFS(base), base)
	if err != nil {
		t.Fatal(err)
	}
	want, err := pack.LoadDir(starter)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != want.Name || !reflect.DeepEqual(got.Flows, want.Flows) ||
		!reflect.DeepEqual(got.Groups, want.Groups) || !reflect.DeepEqual(got.Crew, want.Crew) {
		t.Fatalf("URL pack = %+v, directory pack = %+v", got, want)
	}
	if !reflect.DeepEqual(got.Files(), want.Files()) {
		t.Fatalf("URL files = %v, directory files = %v", got.Files(), want.Files())
	}
	for _, rel := range allFiles(want) {
		resp, err := http.Get(base + rel)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s%s: %s", base, rel, resp.Status)
		}
		fromURL, err := fs.ReadFile(pack.URLFS(base), rel)
		if err != nil {
			t.Fatal(err)
		}
		fromDir, err := os.ReadFile(filepath.Join(starter, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fromURL, fromDir) {
			t.Errorf("%s differs over HTTP", rel)
		}
	}
	// The trailing slash is the caller's to leave out: the pack is the same.
	if p, err := pack.Load(pack.URLFS(strings.TrimSuffix(base, "/")), strings.TrimSuffix(base, "/")); err != nil || p.Name != want.Name {
		t.Errorf("without the trailing slash: %v, %v", p, err)
	}
	// And a plan builds from it, reading every file over HTTP.
	st, o := yard(t)
	urlPlan := build(t, st, got, o)
	dirPlan := build(t, st, want, o)
	if !reflect.DeepEqual(actions(urlPlan, "create"), actions(dirPlan, "create")) {
		t.Errorf("URL plan creates %v, directory plan %v", actions(urlPlan, "create"), actions(dirPlan, "create"))
	}
	sums := func(plan *pack.Plan) map[string]string {
		out := map[string]string{}
		for _, obj := range plan.Objects {
			if obj.Kind == "file" {
				out[obj.Name] = obj.File.SHA256
			}
		}
		return out
	}
	if !reflect.DeepEqual(sums(urlPlan), sums(dirPlan)) {
		t.Errorf("URL plan files differ from the directory plan's")
	}
}

// A file missing from the served pack is the directory's mistake, with the
// URL where the directory would be.
func TestURLPackMissingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(starter)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "roles", "reviewer.md")); err != nil {
		t.Fatal(err)
	}
	_, dirErr := pack.LoadDir(dir)
	var want pack.Errors
	if !errors.As(dirErr, &want) {
		t.Fatalf("LoadDir = %v", dirErr)
	}
	srv := servePack(t, dir)
	base := srv.URL + "/starter/"
	_, urlErr := pack.Load(pack.URLFS(base), base)
	var got pack.Errors
	if !errors.As(urlErr, &got) {
		t.Fatalf("Load over HTTP = %v", urlErr)
	}
	if len(got) != len(want) {
		t.Fatalf("URL errors = %v, directory errors = %v", got, want)
	}
	for i := range want {
		// The same mistake, with the URL where the directory would be: the
		// message names the file it looked for.
		wantMsg := strings.Replace(want[i].Msg, dir, strings.TrimSuffix(base, "/"), -1)
		if got[i].Line != want[i].Line || got[i].Key != want[i].Key || got[i].Msg != wantMsg {
			t.Errorf("URL error %v, directory error %v", got[i], want[i])
		}
		rel := filepath.ToSlash(want[i].File[len(dir)+1:])
		if got[i].File != strings.TrimSuffix(base, "/")+"/"+rel {
			t.Errorf("URL error file = %q, want the URL of %q", got[i].File, rel)
		}
		if !strings.Contains(got[i].Error(), srv.URL) {
			t.Errorf("URL error names no URL: %v", got[i])
		}
	}
}

// What the URL file system does not serve is an error of its own: a missing
// file is not there, a path outside the pack is invalid, and anything else
// names the URL and its status. What it will not fetch is refused with the
// reason: plain http to another machine, a redirect off the pack's host, a
// body past MaxURLFile.
func TestURLFSErrors(t *testing.T) {
	srv := servePack(t, starter)
	base := srv.URL + "/starter/"
	fsys := pack.URLFS(base)
	if _, err := fs.ReadFile(fsys, "pack.yaml"); err != nil {
		t.Fatalf("pack.yaml: %v", err)
	}
	if _, err := fsys.Open("roles/no-such.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file: %v", err)
	}
	for _, name := range []string{"", "/pack.yaml", "../pack.yaml", "roles/../../x"} {
		if _, err := fsys.Open(name); !errors.Is(err, fs.ErrInvalid) {
			t.Errorf("Open(%q) = %v, want invalid", name, err)
		}
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "try later", http.StatusBadGateway)
	}))
	defer bad.Close()
	if _, err := fs.ReadFile(pack.URLFS(bad.URL+"/starter/"), "pack.yaml"); err == nil ||
		!strings.Contains(err.Error(), bad.URL+"/starter/pack.yaml") || !strings.Contains(err.Error(), "502") {
		t.Errorf("a bad status: %v", err)
	}

	// A pack comes over https: plain http is for this machine alone (every
	// server here is), and is refused before a connection is made.
	// 192.0.2.1 is an address for documentation, which nothing answers on.
	for base, want := range map[string]string{
		"http://192.0.2.1/starter/":                  "plain http to 192.0.2.1, which is not this machine",
		"http://localhost.example.com:8080/starter/": "plain http to localhost.example.com, which is not this machine",
		"ftp://packs.example/starter/":               `"ftp" is not a scheme a pack is fetched by`,
	} {
		if _, err := fs.ReadFile(pack.URLFS(base), "pack.yaml"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", base, err, want)
		}
	}

	// A file may move within the host the pack is on and no further: a
	// redirect to another host is refused, and the other host is never asked.
	var asked atomic.Int32
	away := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked.Add(1) }))
	defer away.Close()
	mux := http.NewServeMux()
	mux.Handle("/starter/", http.StripPrefix("/starter/", http.FileServer(http.FS(os.DirFS(starter)))))
	mux.Handle("/moved/pack.yaml", http.RedirectHandler("/starter/pack.yaml", http.StatusMovedPermanently))
	mux.HandleFunc("/away/pack.yaml", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, away.URL+"/starter/pack.yaml", http.StatusFound)
	})
	// A body is read to MaxURLFile and no further: a file of exactly that
	// is a file, and a server that never stops is refused at the first byte
	// past it, not when the timeout comes.
	mux.HandleFunc("/exact/pack.yaml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), pack.MaxURLFile))
	})
	mux.HandleFunc("/endless/pack.yaml", func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("x"), 32<<10)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	site := httptest.NewServer(mux)
	defer site.Close()
	want, err := os.ReadFile(filepath.Join(starter, "pack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := fs.ReadFile(pack.URLFS(site.URL+"/moved/"), "pack.yaml"); err != nil || !bytes.Equal(got, want) {
		t.Errorf("a redirect within the host: %d bytes, %v", len(got), err)
	}
	if _, err := fs.ReadFile(pack.URLFS(site.URL+"/away/"), "pack.yaml"); err == nil ||
		!strings.Contains(err.Error(), "redirected to "+strings.TrimPrefix(away.URL, "http://")+", which is not the host the pack is served from") {
		t.Errorf("a redirect to another host: %v", err)
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("the other host was asked %d times", n)
	}
	if got, err := fs.ReadFile(pack.URLFS(site.URL+"/exact/"), "pack.yaml"); err != nil || len(got) != pack.MaxURLFile {
		t.Errorf("a body of exactly the cap: %d bytes, %v", len(got), err)
	}
	if _, err := fs.ReadFile(pack.URLFS(site.URL+"/endless/"), "pack.yaml"); err == nil ||
		!strings.Contains(err.Error(), "more than 1048576 bytes") {
		t.Errorf("a body with no end: %v", err)
	}
}

// onPut is a store that does something just before it saves a source: the
// moment an import has finished checking the pack and begins to write.
type onPut struct {
	store.API
	before func()
}

func (s onPut) PutSource(ctx context.Context, file, text, actor string) (*store.Source, error) {
	s.before()
	return s.API.PutSource(ctx, file, text, actor)
}

// What an import installs from a pack at a URL is the bytes it checked. The
// server here answers with the pack until the import starts to write and
// with something else after: an install that read the pack again would take
// the something else, which nothing hashed.
func TestURLPackInstallsTheBytesItChecked(t *testing.T) {
	files := sourcePackFiles()
	site := http.StripPrefix("/starter/", http.FileServer(http.FS(os.DirFS(writePack(t, files)))))
	var writing atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writing.Load() && strings.HasPrefix(r.URL.Path, "/starter/acme/") {
			_, _ = io.WriteString(w, "#!/bin/sh\necho swapped\n")
			return
		}
		site.ServeHTTP(w, r)
	}))
	defer srv.Close()
	base := srv.URL + "/starter/"
	p, err := pack.Load(pack.URLFS(base), base)
	if err != nil {
		t.Fatal(err)
	}
	st := storetest.New(t)
	home := t.TempDir()
	plan := build(t, st, p, pack.Options{Home: home})
	if _, err := pack.Apply(ctx, onPut{API: st, before: func() { writing.Store(true) }}, plan, pack.ApplyOptions{Actor: "jo"}); err != nil {
		t.Fatal(err)
	}
	if !writing.Load() {
		t.Fatal("the import never reached the write the server waits for")
	}
	for _, prog := range []string{"acme/normalize", "acme/thread"} {
		got, err := os.ReadFile(filepath.Join(home, "sources", "acme", filepath.FromSlash(prog)))
		if err != nil || string(got) != files[prog] {
			t.Errorf("the copy of %s = %q, %v, want the file the plan hashed: %q", prog, got, err, files[prog])
		}
	}
}
