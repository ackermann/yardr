package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
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
