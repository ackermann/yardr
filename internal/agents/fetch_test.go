package agents

import (
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// The fetches yardr is built with: claude and codex have one, for both
// kinds of Mac and for nothing else, and a kind whose maker's way was not
// verified has none, which is what says it is installed by hand.
func TestFetchOfHarnessFiles(t *testing.T) {
	for _, h := range Harnesses {
		want := h.Kind == "claude" || h.Kind == "codex"
		if (h.Fetch != nil) != want {
			t.Errorf("%s: fetch %v, want one: %v", h.Kind, h.Fetch, want)
		}
		if h.Fetch == nil {
			if h.Fetch.For("darwin", "arm64", "/Users/x") != nil {
				t.Errorf("%s: a download of no fetch", h.Kind)
			}
			continue
		}
		for _, arch := range []string{"arm64", "amd64"} {
			d := h.Fetch.For("darwin", arch, "/Users/x")
			if d == nil || d.Path != "/Users/x/.local/bin/"+h.Kind || d.Version != h.Fetch.Version || d.Verified != h.Fetch.Verified ||
				d.URL == "" || len(d.SHA256) != 64 || d.Size == 0 {
				t.Errorf("%s on darwin/%s: %+v", h.Kind, arch, d)
			}
		}
		for _, sys := range [][2]string{{"linux", "amd64"}, {"linux", "arm64"}, {"windows", "amd64"}} {
			if d := h.Fetch.For(sys[0], sys[1], "/home/x"); d != nil {
				t.Errorf("%s is fetched on %s/%s: %+v", h.Kind, sys[0], sys[1], d)
			}
		}
	}
	// Claude Code is put in place by its own install command, of the very
	// version that was fetched; codex is the one member of its archive.
	claude, codex := byName["claude"].Fetch, byName["codex"].Fetch
	if got := strings.Join(claude.Run, " "); got != "install "+claude.Version {
		t.Errorf("claude is run with %q", got)
	}
	if d := codex.For("darwin", "arm64", "/Users/x"); d.Member != "codex-aarch64-apple-darwin" || d.Run != nil {
		t.Errorf("codex: %+v", d)
	}
}

// A fetch that a program could not go by, or that would fetch what was not
// verified, is refused when the file is read.
func TestFetchCheck(t *testing.T) {
	good := func() *store.Fetch {
		return &store.Fetch{
			Verified: store.Verified{From: "https://tool.example/install", On: "2026-10-13"},
			Version:  "1.2.3", Path: "~/.local/bin/tool",
			Targets: map[string]store.FetchFile{"darwin/arm64": {
				URL: "https://tool.example/1.2.3/tool.tar.gz", SHA256: strings.Repeat("a1", 32), Size: 10, Member: "tool-arm64"}},
		}
	}
	if err := good().Check("tool"); err != nil {
		t.Fatalf("a good fetch: %v", err)
	}
	for want, spoil := range map[string]func(f *store.Fetch){
		"verified.from":      func(f *store.Fetch) { f.Verified.From = "http://tool.example/install" },
		"verified.on":        func(f *store.Fetch) { f.Verified.On = "13 Oct 2026" },
		"no version":         func(f *store.Fetch) { f.Version = "" },
		"under the home":     func(f *store.Fetch) { f.Path = "/usr/local/bin/tool" },
		"is not a clean":     func(f *store.Fetch) { f.Path = "~/../other/tool" },
		"does not end in":    func(f *store.Fetch) { f.Path = "~/.local/bin/other" },
		"no targets":         func(f *store.Fetch) { f.Targets = nil },
		"an empty argument":  func(f *store.Fetch) { f.Run = []string{"install", ""} },
		"only a Mac fetches": func(f *store.Fetch) { f.Targets["linux/amd64"] = f.Targets["darwin/arm64"] },
		"no https address": func(f *store.Fetch) {
			f.Targets["darwin/arm64"] = store.FetchFile{URL: "http://tool.example/1.2.3/tool", SHA256: strings.Repeat("a1", 32), Size: 10}
		},
		"does not name version": func(f *store.Fetch) {
			f.Targets["darwin/arm64"] = store.FetchFile{URL: "https://tool.example/latest/tool", SHA256: strings.Repeat("a1", 32), Size: 10}
		},
		"64 lower-case hex": func(f *store.Fetch) {
			f.Targets["darwin/arm64"] = store.FetchFile{URL: "https://tool.example/1.2.3/tool", SHA256: strings.Repeat("A1", 32), Size: 10}
		},
		"no size": func(f *store.Fetch) {
			f.Targets["darwin/arm64"] = store.FetchFile{URL: "https://tool.example/1.2.3/tool", SHA256: strings.Repeat("a1", 32)}
		},
		"plain file name": func(f *store.Fetch) {
			f.Targets["darwin/arm64"] = store.FetchFile{URL: "https://tool.example/1.2.3/tool", SHA256: strings.Repeat("a1", 32), Size: 10, Member: "../tool"}
		},
	} {
		f := good()
		spoil(f)
		if err := f.Check("tool"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want an error with %q, got %v", want, err)
		}
	}
}
