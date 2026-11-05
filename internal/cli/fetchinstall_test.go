package cli

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/store"
)

// herdrScript is the stand-in for the file that is fetched: a program that
// answers its version flag the way a tool does.
const herdrScript = "#!/bin/sh\necho 'herdr 0.9.4'\n"

// pinnedFile is a download that pins body, at url, to be put at path.
func pinnedFile(url, path, body string) *store.Download {
	sum := sha256.Sum256([]byte(body))
	return &store.Download{Version: "0.9.4", Path: path, FetchFile: store.FetchFile{
		URL: url, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}}
}

// trusting is a client that trusts srv's certificate and follows redirects
// as the one that downloads for yardr does.
func trusting(srv *httptest.Server) *http.Client {
	c := *srv.Client()
	c.CheckRedirect = fetchHTTP.CheckRedirect
	return &c
}

// dirEntries are the names in dir, nil where there is no such directory.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

// closedAddress is an address on this machine that nothing listens on.
func closedAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// What is downloaded is kept only when it is the file yardr pinned and it
// runs: the size and the sha256 are the pinned ones, a redirect goes to https
// alone, and the version flag answers. Each way it can fail writes nothing
// that stays (a directory made for it excepted, once the file was proved),
// a file at the place is never replaced, and what is put in place is the
// whole file with mode 0755. A serial test: it starts the files it writes,
// and a fork of a test beside it, holding the write end of one for a moment,
// would fail that start with ETXTBSY.
func TestInstallDownload(t *testing.T) {
	closed := closedAddress(t)
	n := len(herdrScript)
	flipped := []byte(herdrScript)
	flipped[n-2] ^= 1 // same length, another sum
	storage := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file" {
			http.Redirect(w, r, "/storage/abc?sig=SECRET", http.StatusFound)
			return
		}
		io.WriteString(w, herdrScript)
	}
	serves := func(body string) func(string) http.HandlerFunc {
		return func(string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }
		}
	}
	redirectTo := func(to string) func(string) http.HandlerFunc {
		return func(string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) }
		}
	}
	const failing = "#!/bin/sh\necho no good >&2\nexit 3\n"
	const silent = "#!/bin/sh\nexit 0\n"
	const hangs = "#!/bin/sh\nexec sleep 60\n"
	// What was at the place is there still.
	stillMine := func(t *testing.T, path string) {
		t.Helper()
		if b, _ := os.ReadFile(path); string(b) != "mine" {
			t.Errorf("the file at the place was replaced: %q", b)
		}
	}
	for _, tc := range []struct {
		name    string
		serve   func(path string) http.HandlerFunc // the stand-in's answer, given where the program goes
		pin     string                             // the file pinned: herdrScript when ""
		before  func(t *testing.T, path string)    // the state of the place
		after   func(t *testing.T, path string)    // what it must be after a failure
		timeout time.Duration                      // for the version flag; a long one when 0
		member  string                             // the pinned file is a member of an archive
		hits    int32                              // times the stand-in is asked
		want    string                             // part of the error; "" installs
		not     string                             // and what the error does not say
		left    []string                           // the directory's entries after
		madeDir bool                               // the directory was made: the file was proved first
	}{
		{name: "installs", serve: serves(herdrScript), hits: 1, left: []string{"herdr"}, madeDir: true},
		{name: "follows a redirect to https storage", serve: func(string) http.HandlerFunc { return storage }, hits: 2, left: []string{"herdr"}, madeDir: true},
		{name: "another sha256 of the same size", serve: serves(string(flipped)), hits: 1, want: "sha256 ("},
		{name: "a shorter file", serve: serves(herdrScript[:20]), hits: 1, want: fmt.Sprintf("is 20 bytes, not the %d yardr pins", n)},
		{name: "a longer file", serve: serves(herdrScript + "more"), hits: 1, want: fmt.Sprintf("larger than the %d bytes yardr pins", n)},
		{name: "a refusal", serve: func(string) http.HandlerFunc { return http.NotFound }, hits: 1, want: "answered with HTTP 404"},
		{name: "a redirect to http", serve: redirectTo("http://" + closed + "/x"), hits: 1, want: "not https"},
		{name: "a redirect that never ends", serve: redirectTo("/file"), hits: 6, want: "more than 5 times"},
		{name: "a redirect that goes nowhere", serve: redirectTo("https://" + closed + "/s?sig=SECRET"), hits: 1,
			want: "the download failed", not: "SECRET"},
		{name: "a program that fails its version flag", pin: failing, serve: serves(failing), hits: 1,
			want: "does not run here (exit status 3: no good)", madeDir: true},
		{name: "a program that prints no version", pin: silent, serve: serves(silent), hits: 1,
			want: "printed no version", madeDir: true},
		{name: "a program that does not answer", pin: hangs, serve: serves(hangs), timeout: time.Second, hits: 1,
			want: "did not answer --version within 1s", madeDir: true},
		{name: "a file at the place", serve: serves(herdrScript),
			before: func(t *testing.T, path string) {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("mine"), 0o755); err != nil {
					t.Fatal(err)
				}
			}, after: stillMine, want: "is there already, and is not replaced", left: []string{"herdr"}, madeDir: true},
		// A link is something there, whether or not it leads anywhere.
		{name: "a link to nowhere at the place", serve: serves(herdrScript),
			before: func(t *testing.T, path string) {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("nowhere", path); err != nil {
					t.Fatal(err)
				}
			}, after: func(t *testing.T, path string) {
				if to, err := os.Readlink(path); err != nil || to != "nowhere" {
					t.Errorf("the link at the place: %q, %v", to, err)
				}
			}, want: "is there already, and is not replaced", left: []string{"herdr"}, madeDir: true},
		{name: "a file that appears while it downloads",
			serve: func(path string) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					os.MkdirAll(filepath.Dir(path), 0o755)
					os.WriteFile(path, []byte("mine"), 0o755)
					io.WriteString(w, herdrScript)
				}
			}, after: stillMine, hits: 1, want: "is there already, and is not replaced", left: []string{"herdr"}, madeDir: true},
		{name: "a member of an archive", serve: serves(herdrScript), member: "herdr", want: "not the program itself"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub(t, &versionTimeout, cmp.Or(tc.timeout, fakeToolsTimeout))
			var hits atomic.Int32
			path := filepath.Join(t.TempDir(), ".local", "bin", "herdr")
			handler := tc.serve(path)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				handler(w, r)
			}))
			defer srv.Close()
			if tc.before != nil {
				tc.before(t, path)
			}
			d := pinnedFile(srv.URL+"/file", path, cmp.Or(tc.pin, herdrScript))
			d.Member = tc.member

			says, err := installDownload(context.Background(), trusting(srv), d)

			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("installDownload: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("installDownload said %q (%v), want an error with %q", says, err, tc.want)
			case tc.not != "" && strings.Contains(err.Error(), tc.not):
				t.Fatalf("the error says %q, which a line for a person must not: %v", tc.not, err)
			}
			if got := hits.Load(); got != tc.hits {
				t.Errorf("asked the stand-in %d times, want %d", got, tc.hits)
			}
			dir := filepath.Dir(path)
			if got := dirEntries(t, dir); !slices.Equal(got, tc.left) {
				t.Errorf("%s holds %v, want %v", dir, got, tc.left)
			}
			if _, err := os.Stat(dir); (err == nil) != tc.madeDir {
				t.Errorf("the directory was made = %v, want %v (%v)", err == nil, tc.madeDir, err)
			}
			if tc.want != "" {
				if tc.after != nil {
					tc.after(t, path)
				}
				return
			}
			b, err := os.ReadFile(path)
			if fi, serr := os.Stat(path); err != nil || serr != nil || string(b) != herdrScript || fi.Mode().Perm() != 0o755 {
				t.Errorf("the program at %s: %q, %v, %v", path, b, err, serr)
			}
			if says != "herdr 0.9.4" {
				t.Errorf("the version flag said %q", says)
			}
		})
	}
}

// offerConf sets a run of the onboarding apart from the plain one: herdr is
// the only program missing, there is a terminal, the file pinned for the
// system is served whole, and the person says yes.
type offerConf struct {
	answer string // the key pressed at the offer: "n" says no; "" leaves it at its default
	noTTY  bool
	found  []string                        // on PATH besides git
	goos   string                          // the system; linux when ""
	goarch string                          // amd64 when ""
	before func(t *testing.T, home string) // the state of the home
	serve  http.HandlerFunc                // the stand-in's answer; the whole file when nil
	onPath bool                            // ~/.local/bin is on PATH already
}

// offerRun is a run of the onboarding against the stand-in server that
// serves herdr's pinned file.
type offerRun struct {
	home  string
	out   string
	err   error
	pages []string // the view of each page asked
	hits  atomic.Int32
}

// offerEnv is the world of a run: a home and a PATH of its own, in which
// herdr is missing, herdr's pin pointed at a local server for the systems
// the test names, and the real offer put back. It asks the person nothing:
// the offer is answered by c.answer, and any page that is no question ends
// the form there.
func offerEnv(t *testing.T, c offerConf) *offerRun {
	t.Helper()
	r := &offerRun{home: t.TempDir()}
	bin := filepath.Join(r.home, ".local", "bin")
	// tilde reads the home from the environment, and a PATH of the system's
	// own is what a terminal that lacks ~/.local/bin has.
	t.Setenv("HOME", r.home)
	t.Setenv("PATH", "/usr/bin:/bin")
	if c.onPath {
		t.Setenv("PATH", "/usr/bin:/bin:"+bin)
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		if c.serve != nil {
			c.serve(w, req)
			return
		}
		io.WriteString(w, herdrScript)
	}))
	t.Cleanup(srv.Close)
	pin := func(file string) store.FetchFile {
		return pinnedFile(srv.URL+"/herdrdev/herdr/releases/download/v0.9.4/"+file, "", herdrScript).FetchFile
	}
	stub(t, herdrFetch, store.Fetch{
		Verified: store.Verified{From: "https://herdr.example/docs/install/", On: "2026-11-05"},
		Version:  "0.9.4", Path: "~/.local/bin/herdr",
		Targets: map[string]store.FetchFile{"linux/amd64": pin("herdr-linux-x86_64"), "darwin/arm64": pin("herdr-macos-aarch64")},
	})
	stub(t, &userHomeDir, func() (string, error) { return r.home, nil })
	stub(t, &goos, cmp.Or(c.goos, "linux"))
	stub(t, &goarch, cmp.Or(c.goarch, "amd64"))
	stub(t, &fetchHTTP, trusting(srv))
	stub(t, &offerInstall, realOfferInstall)
	setInteractive(t, !c.noTTY)
	stubLookPath(t, append([]string{"git"}, c.found...)...)
	stubAsk(t, func(fields ...huh.Field) error {
		r.pages = append(r.pages, fields[0].View())
		confirm, ok := fields[0].(*huh.Confirm)
		if !ok {
			return huh.ErrUserAborted
		}
		if strings.Contains(confirm.View(), "Install herdr") && c.answer != "" {
			confirm.WithKeyMap(huh.NewDefaultKeyMap())
			confirm.Focus()
			confirm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(c.answer)})
		}
		return nil
	})
	if c.before != nil {
		c.before(t, r.home)
	}
	return r
}

// preflight runs the onboarding's preflight in the world offerEnv made.
func (r *offerRun) preflight(ctx context.Context) {
	var out strings.Builder
	r.err = preflight(ctx, &out)
	r.out = out.String()
}

// At a terminal, with herdr missing and a file pinned for the system, the
// onboarding asks whether to install it, and on yes puts the verified file in
// ~/.local/bin and goes on with herdr found, even when ~/.local/bin is not on
// PATH yet. On no, or when anything goes wrong, it prints the command as it
// always did, and nothing stays in ~/.local/bin. It asks nothing without a
// terminal, nothing for a herdr that is there, and replaces nothing.
func TestPreflightOffersToInstallHerdr(t *testing.T) {
	const question = "Install herdr 0.9.4 into ~/.local/bin now?"
	const command = "    curl -fsSL https://herdr.dev/install.sh | sh\n\n"
	const again = "Once herdr is installed, run yardr again.\n\n"
	const pathLine = "    export PATH=\"$HOME/.local/bin:$PATH\"\n"
	binOf := func(r *offerRun) string { return filepath.Join(r.home, ".local", "bin") }
	run := func(t *testing.T, c offerConf) *offerRun {
		t.Helper()
		r := offerEnv(t, c)
		r.preflight(context.Background())
		return r
	}
	// A run that went on: the offer alone was asked, no command printed, no stop.
	wentOn := func(t *testing.T, r *offerRun) {
		t.Helper()
		if r.err != nil || len(r.pages) != 1 || !strings.Contains(r.pages[0], question) ||
			strings.Contains(r.out, command) || strings.Contains(r.out, again) {
			t.Fatalf("preflight stopped or asked more: %v, %d pages\n%s", r.err, len(r.pages), r.out)
		}
	}
	// A run that fell back to the printed command, and was asked whether to go
	// on without herdr, which starts at stop.
	fellBack := func(t *testing.T, r *offerRun, pages int) {
		t.Helper()
		if !errors.Is(r.err, huh.ErrUserAborted) || len(r.pages) != pages || !strings.Contains(r.out, command+again) {
			t.Fatalf("preflight: %v, %d pages, want %d\n%s", r.err, len(r.pages), pages, r.out)
		}
		if !strings.Contains(r.pages[pages-1], "Go on without herdr?") {
			t.Errorf("the last page: %s", r.pages[pages-1])
		}
	}
	// Nothing was written under the home.
	wroteNothing := func(t *testing.T, r *offerRun) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(r.home, ".local")); err == nil {
			t.Errorf("%v was made under the home", dirEntries(t, filepath.Join(r.home, ".local")))
		}
		if strings.Contains(os.Getenv("PATH"), ".local") {
			t.Errorf("PATH was changed: %s", os.Getenv("PATH"))
		}
	}

	t.Run("yes installs and goes on, with herdr found though ~/.local/bin is off PATH", func(t *testing.T) {
		r := run(t, offerConf{})
		wentOn(t, r)
		herdr := filepath.Join(binOf(r), "herdr")
		for _, want := range []string{
			"herdr is not on PATH: it starts every agent",
			"Downloading herdr 0.9.4 (0.0 MB)…\n",
			`Installed herdr 0.9.4 at ~/.local/bin/herdr: it says "herdr 0.9.4".` + "\n",
			"~/.local/bin is not on PATH here, so yardr looks in it for this run.",
			pathLine,
		} {
			if !strings.Contains(r.out, want) {
				t.Errorf("the output lacks %q:\n%s", want, r.out)
			}
		}
		if !strings.Contains(r.pages[0], "Yes") || r.hits.Load() != 1 {
			t.Errorf("the offer %q, asked the stand-in %d times", r.pages[0], r.hits.Load())
		}
		if b, err := os.ReadFile(herdr); err != nil || string(b) != herdrScript {
			t.Errorf("%s: %q, %v", herdr, b, err)
		}
		if got, err := exec.LookPath("herdr"); err != nil || got != herdr {
			t.Errorf("herdr is found at %q (%v), want %s: PATH is %s", got, err, herdr, os.Getenv("PATH"))
		}
		if got := dirEntries(t, binOf(r)); !slices.Equal(got, []string{"herdr"}) {
			t.Errorf("%s holds %v", binOf(r), got)
		}
		// No shell profile is written.
		for _, profile := range []string{".zshrc", ".bashrc", ".profile", ".zprofile", ".bash_profile"} {
			if _, err := os.Stat(filepath.Join(r.home, profile)); err == nil {
				t.Errorf("%s was written", profile)
			}
		}
	})

	t.Run("on a Mac", func(t *testing.T) {
		r := run(t, offerConf{goos: "darwin", goarch: "arm64"})
		wentOn(t, r)
		if _, err := os.Stat(filepath.Join(binOf(r), "herdr")); err != nil || !strings.Contains(r.out, "Installed herdr 0.9.4") {
			t.Errorf("%v\n%s", err, r.out)
		}
	})

	t.Run("yes with ~/.local/bin on PATH says nothing of PATH", func(t *testing.T) {
		r := run(t, offerConf{onPath: true})
		wentOn(t, r)
		path := os.Getenv("PATH")
		if strings.Contains(r.out, "export PATH") || strings.Count(path, binOf(r)) != 1 || !strings.Contains(r.out, "Installed herdr 0.9.4") {
			t.Errorf("PATH %s\n%s", path, r.out)
		}
	})

	t.Run("no prints the command, and the answer to go on starts at stop", func(t *testing.T) {
		r := run(t, offerConf{answer: "n"})
		fellBack(t, r, 2)
		if r.hits.Load() != 0 || strings.Contains(r.out, "Downloading") {
			t.Errorf("something was fetched: asked %d times\n%s", r.hits.Load(), r.out)
		}
		wroteNothing(t, r)
	})

	t.Run("a sha256 that is not the pinned one writes nothing and prints the command", func(t *testing.T) {
		r := run(t, offerConf{serve: func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, strings.Replace(herdrScript, "herdr", "herds", 1))
		}})
		fellBack(t, r, 2)
		if !strings.Contains(r.out, "herdr was not installed: the download's sha256 (") || !strings.Contains(r.out, "nothing of it was kept.\n") ||
			strings.Contains(r.out, "Installed") || r.hits.Load() != 1 {
			t.Errorf("asked %d times\n%s", r.hits.Load(), r.out)
		}
		wroteNothing(t, r)
	})

	t.Run("a refusal of the download prints the command", func(t *testing.T) {
		r := run(t, offerConf{serve: http.NotFound})
		fellBack(t, r, 2)
		if !strings.Contains(r.out, "herdr was not installed: the download was answered with HTTP 404;") {
			t.Errorf("output:\n%s", r.out)
		}
		wroteNothing(t, r)
	})

	t.Run("without a terminal nothing is offered", func(t *testing.T) {
		r := run(t, offerConf{noTTY: true})
		fellBack(t, r, 1)
		if r.hits.Load() != 0 || strings.Contains(r.out, "Install") || strings.Contains(r.out, "Downloading") {
			t.Errorf("asked %d times\n%s", r.hits.Load(), r.out)
		}
		wroteNothing(t, r)
	})

	t.Run("with herdr on PATH nothing is asked", func(t *testing.T) {
		r := run(t, offerConf{found: []string{"herdr"}})
		if r.err != nil || len(r.pages) != 0 || r.out != "" || r.hits.Load() != 0 {
			t.Errorf("preflight: %v, %d pages, asked %d times\n%s", r.err, len(r.pages), r.hits.Load(), r.out)
		}
	})

	t.Run("a herdr already in ~/.local/bin is used, not fetched over", func(t *testing.T) {
		r := run(t, offerConf{before: func(t *testing.T, home string) {
			if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".local", "bin", "herdr"), []byte("newer"), 0o755); err != nil {
				t.Fatal(err)
			}
		}})
		if r.err != nil || len(r.pages) != 0 || r.hits.Load() != 0 || strings.Contains(r.out, command) {
			t.Fatalf("preflight: %v, %d pages, asked %d times\n%s", r.err, len(r.pages), r.hits.Load(), r.out)
		}
		if !strings.Contains(r.out, "herdr is at ~/.local/bin/herdr already.\n") || !strings.Contains(r.out, pathLine) {
			t.Errorf("output:\n%s", r.out)
		}
		if b, _ := os.ReadFile(filepath.Join(binOf(r), "herdr")); string(b) != "newer" {
			t.Errorf("the herdr there was replaced: %q", b)
		}
		if got, err := exec.LookPath("herdr"); err != nil || got != filepath.Join(binOf(r), "herdr") {
			t.Errorf("herdr is found at %q (%v)", got, err)
		}
	})

	t.Run("something at the place that is no program is left, and the command printed", func(t *testing.T) {
		for name, put := range map[string]func(path string) error{
			"a directory":               func(path string) error { return os.MkdirAll(path, 0o755) },
			"a file that cannot be run": func(path string) error { return os.WriteFile(path, []byte("x"), 0o644) },
			"a link to an executable": func(path string) error {
				if err := os.WriteFile(path+"-target", []byte(herdrScript), 0o755); err != nil {
					return err
				}
				return os.Symlink(path+"-target", path)
			},
		} {
			r := run(t, offerConf{before: func(t *testing.T, home string) {
				path := filepath.Join(home, ".local", "bin", "herdr")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := put(path); err != nil {
					t.Fatal(err)
				}
			}})
			fellBack(t, r, 1)
			if r.hits.Load() != 0 || strings.Contains(r.out, "Install") {
				t.Errorf("%s: asked %d times\n%s", name, r.hits.Load(), r.out)
			}
		}
	})

	t.Run("a system with nothing pinned is offered nothing", func(t *testing.T) {
		r := run(t, offerConf{goos: "plan9"})
		fellBack(t, r, 1)
		if r.hits.Load() != 0 || strings.Contains(r.out, "Install") {
			t.Errorf("asked %d times\n%s", r.hits.Load(), r.out)
		}
		wroteNothing(t, r)
	})

	t.Run("ctrl+c while the file comes down leaves the onboarding and writes nothing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		r := offerEnv(t, offerConf{})
		r.preflight(ctx)
		if !errors.Is(r.err, huh.ErrUserAborted) || r.hits.Load() != 0 || strings.Contains(r.out, "was not installed") {
			t.Errorf("preflight: %v, asked %d times\n%s", r.err, r.hits.Load(), r.out)
		}
		wroteNothing(t, r)
	})

	t.Run("the form goes on to its first page once herdr is in", func(t *testing.T) {
		r := offerEnv(t, offerConf{})
		var out strings.Builder
		_, err := onboardForm(context.Background(), &out, filepath.Join(r.home, ".yardr"), nil, func(string) string { return "ada" })
		// The offer, then the harness: the page that stops this test.
		if !errors.Is(err, huh.ErrUserAborted) || len(r.pages) != 2 || !strings.Contains(r.pages[0], question) ||
			!strings.Contains(r.pages[1], "Harness") || !strings.Contains(out.String(), "Installed herdr 0.9.4") {
			t.Errorf("onboardForm: %v, pages %s\n%s", err, fmt.Sprint(len(r.pages)), out.String())
		}
	})
}
