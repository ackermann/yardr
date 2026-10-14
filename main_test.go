package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// The roles are data in a pack: no Go code yardr is built from, outside the
// pack reader (internal/pack) and the packs themselves (packs/), names the
// file of a pack's role, so none can read one, write one or tell one from
// another. What a group does is its prompt's to say, and which file that is
// the pack's.
//
// Two files of help text show a group's prompt= by example
// (roles/builder.md, the relative form a depot or a home with roles of its
// own is read by): there the name is allowed in that form and no other.
// Tests are not held to this: they say what the embedded packs install, and
// build depots with roles of their own.
func TestNoGoCodeNamesARole(t *testing.T) {
	role := regexp.MustCompile(`(roles/)?\b(builder|reviewer|merger)\.md\b`)
	examples := []string{"internal/cli/groupform.go", "internal/cli/ops.go"}
	var files int
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path = filepath.ToSlash(path)
		if d.IsDir() {
			if path == "internal/pack" || path == "packs" || path == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			for _, m := range role.FindAllString(line, -1) {
				if strings.HasPrefix(m, "roles/") && slices.Contains(examples, path) {
					continue
				}
				t.Errorf("%s:%d names %s: a role's file is the pack's to name (internal/pack, packs/): %s", path, i+1, m, strings.TrimSpace(line))
			}
		}
		return nil
	})
	// Not for want of looking: the walk started at the repository's root.
	if err != nil || files < 100 {
		t.Fatalf("%d Go files read, %v", files, err)
	}
}

// The core asks a source's adapter what it can do and never compares a
// source's name: the adapters live in internal/hooks (and the trial
// stand-in in internal/standin), and no code outside them names one
// ("github", "linear") or names an adapter's type (hooks.GitHub). The one
// permitted list is internal/source's BuiltIn, which a declared source's
// name must keep clear of; it goes when the sources leave the binary
// (YRD-29).
func TestCoreComparesNoSourceName(t *testing.T) {
	name := regexp.MustCompile(`"github"|"linear"|hooks\.(GitHub|Linear)\b`)
	var files int
	err := filepath.WalkDir("internal", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path = filepath.ToSlash(path)
		if d.IsDir() {
			if path == "internal/hooks" || path == "internal/standin" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			if path == "internal/source/declaration.go" && strings.Contains(line, "BuiltIn") {
				continue
			}
			if name.MatchString(line) {
				t.Errorf("%s:%d names a source: ask its adapter instead (internal/hooks): %s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	// Not for want of looking: the walk read the core.
	if err != nil || files < 100 {
		t.Fatalf("%d Go files read, %v", files, err)
	}
}

// A service that tells a user outside the yard is a pack: no Go code yardr
// is built from names one, so none can know one from another. The packs
// live in packs/, their fixtures in tests, and the telling in each pack's
// setup notes and docs/pushover.md. The one permitted line is the go:embed
// directive that carries the packs in the binary, which names their
// directories as data: a bare word (yardr pack apply telegram) resolves
// through it.
func TestNoGoCodeNamesANotifyService(t *testing.T) {
	name := regexp.MustCompile(`(?i)telegram|pushover`)
	var files int
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path = filepath.ToSlash(path)
		if d.IsDir() {
			if path == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(body), "\n") {
			if path == "packs/packs.go" && strings.Contains(line, "go:embed") {
				continue
			}
			if m := name.FindString(line); m != "" {
				t.Errorf("%s:%d names %s: a service that tells a user is a pack's to name (packs/, tests' fixtures, docs): %s", path, i+1, m, strings.TrimSpace(line))
			}
		}
		return nil
	})
	// Not for want of looking: the walk started at the repository's root.
	if err != nil || files < 100 {
		t.Fatalf("%d Go files read, %v", files, err)
	}
}

// GitHub left the core for packs/github: no Go code yardr is built from
// names it, except what the move audited (bead yardr-2c8b): the pack's own
// name; the stand-in and the trial sources, which serve its API shape so
// the pack proves without the real service; the problem and schema URL
// namespaces and the fetched programs' release addresses, which live under
// github.com; and help texts, examples and comments that use GitHub as the
// example source. Tests are not held to this: they prove the pack against
// the stand-in. A line importing a Go module (github.com/...) cannot go.
func TestNoGoCodeNamesGitHub(t *testing.T) {
	name := regexp.MustCompile(`(?i)github`)
	kept := []string{
		"contrib/isolated-yard/sources/main.go",
		"internal/bdp/server.go",
		"internal/cli/crew.go",
		"internal/cli/hooks.go",
		"internal/cli/link.go",
		"internal/cli/needs.go",
		"internal/cli/ops.go",
		"internal/cli/pack.go",
		"internal/cli/reply.go",
		"internal/cli/sourcecmd.go",
		"internal/cli/subscriptions.go",
		"internal/cli/threadopen.go",
		"internal/cli/threadstate.go",
		"internal/hooks/open.go",
		"internal/standin/standin.go",
		"packs/packs.go",
	}
	// A Go import of a module: an optional name, the quoted path, and
	// nothing else. Anything longer that names github.com (a URL built or
	// held) is the code's to own, and fails outside the kept files.
	isImport := regexp.MustCompile(`^\s*(\S+ )?"github\.com/[^"]*"(,)?\s*$`)
	var files int
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path = filepath.ToSlash(path)
		if d.IsDir() {
			if path == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if slices.Contains(kept, path) {
			return nil
		}
		for i, line := range strings.Split(string(body), "\n") {
			if isImport.MatchString(line) {
				continue
			}
			if m := name.FindString(line); m != "" {
				t.Errorf("%s:%d names %s: GitHub is a pack's to name (packs/github): %s", path, i+1, m, strings.TrimSpace(line))
			}
		}
		return nil
	})
	// Not for want of looking: the walk started at the repository's root.
	if err != nil || files < 100 {
		t.Fatalf("%d Go files read, %v", files, err)
	}
}

// The README is what a reader meets first, and it is kept short: what yardr
// is, its principles, the install, the concepts, a quick start, the commands
// and where the rest is. What is cut from it lives in docs/, so the README is
// mostly links, and a renamed heading or a moved page would leave one
// pointing at nothing. This follows every link to a file of the repository,
// and to a heading where the link names one, and holds the README to its
// length.
func TestReadmeLinksResolve(t *testing.T) {
	body, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), "\n"); n >= 150 {
		t.Errorf("README.md has %d lines, want under 150: what is added moves to a page of docs/", n)
	}
	links := regexp.MustCompile(`\]\(([^)\s]+)\)`).FindAllStringSubmatch(string(body), -1)
	var local int
	for _, m := range links {
		file, anchor, _ := strings.Cut(m[1], "#")
		if strings.Contains(file, "://") {
			continue
		}
		local++
		if file == "" {
			file = "README.md"
		}
		page, err := os.ReadFile(filepath.FromSlash(file))
		if err != nil {
			t.Errorf("README.md links to %s: %v", m[1], err)
			continue
		}
		if anchor != "" && !slices.Contains(anchors(string(page)), anchor) {
			t.Errorf("README.md links to %s: %s has no heading with that anchor", m[1], file)
		}
	}
	// Not for want of looking: the README names its pages.
	if local < 5 {
		t.Fatalf("%d links to files found in README.md", local)
	}
}

// anchors is the anchor of each heading of a Markdown page, as GitHub makes
// it: lowercase, spaces to hyphens, everything but letters, digits, hyphens
// and underscores dropped. Headings inside a code block are not headings.
func anchors(page string) []string {
	var out []string
	fenced := false
	for _, line := range strings.Split(page, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
			continue
		}
		title := strings.TrimLeft(line, "#")
		if fenced || title == line || !strings.HasPrefix(title, " ") {
			continue
		}
		var b strings.Builder
		for _, r := range strings.ToLower(strings.TrimSpace(title)) {
			switch {
			case r == ' ':
				b.WriteRune('-')
			case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
				b.WriteRune(r)
			}
		}
		out = append(out, b.String())
	}
	return out
}
